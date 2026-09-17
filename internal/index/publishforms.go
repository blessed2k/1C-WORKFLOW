package index

import (
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// publishFormDecls вставляет формы, объявленные владельцем (аспект
// form_declaration, архитектура §15): ownerObjectID — id объекта метаданных,
// в чьём файле встретились эти FormDeclFact (у CommonForm это её же id —
// «форма, которой является сам объект», meta/facts.go). Вызывается из
// publishMetadataObject: FormDecls всегда идут вместе с Object в одном файле.
func publishFormDecls(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID, ownerObjectID int64, decls []meta.FormDeclFact) error {
	for _, fd := range decls {
		formKey := formIdentityKey(in.component, fd.Key)
		formID, err := tx.EnsureForm(store.Form{
			IdentityKey: formKey, ComponentID: string(in.component),
			OwnerObjectID: ownerObjectID, NameNorm: fd.NameNorm, NameDisplay: fd.NameDisplay,
		})
		if err != nil {
			return fmt.Errorf("form (decl) %s/%s: %w", rel, fd.NameNorm, err)
		}
		ts.nodes.remember(formKey, formID)
		if err := tx.PutFormDeclaration(formID, fileID); err != nil {
			return fmt.Errorf("form_declaration %s/%s: %w", rel, fd.NameNorm, err)
		}
	}
	return nil
}

// publishFormStructure вставляет структуру формы (аспект form_structure) и
// её элементы/команды. Владелец восстанавливается из ПУТИ файла
// (formOwnerFromPath) — тем же способом, каким его вывел бы FormDeclFact со
// стороны владельца: оба аспекта делят identity_key (fd.Key ==
// FormStructureFact.Key, meta/facts.go), а EnsureForm — безусловный upsert,
// поэтому owner_object_id обязан совпадать с обеих сторон, иначе один аспект
// затирал бы owner другого нулём.
func publishFormStructure(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID int64, rec *fileRecord) error {
	fs := rec.metaFacts.FormStructure
	formKey := formIdentityKey(in.component, fs.Key)

	var ownerID int64
	if mtype, nameNorm, ok := formOwnerFromPath(rel); ok {
		objKey := metadataObjectIdentityKey(in.component, mtype, nameNorm)
		if id, found, err := ts.nodes.lookup(tx, objKey); err != nil {
			return fmt.Errorf("владелец формы %s: %w", rel, err)
		} else if found {
			ownerID = id
		}
	}

	// Имя формы для form.name_norm/name_display: FormStructureFact их не
	// несёт (meta/facts.go — только Elements/Commands/Handlers), берём
	// последний сегмент имени формы из пути (тот же, что видит FormDeclFact
	// со стороны владельца).
	nameDisplay := formNameFromPath(rel)
	formID, err := tx.EnsureForm(store.Form{
		IdentityKey: formKey, ComponentID: string(in.component),
		OwnerObjectID: ownerID, NameNorm: domain.NormalizeName(nameDisplay), NameDisplay: nameDisplay,
	})
	if err != nil {
		return fmt.Errorf("form (structure) %s: %w", rel, err)
	}
	ts.nodes.remember(formKey, formID)
	if err := tx.PutFormStructure(formID, fileID); err != nil {
		return fmt.Errorf("form_structure %s: %w", rel, err)
	}

	for i, e := range fs.Elements {
		elKey := formKey + "\x00element\x00" + itoaIndex(i) + "\x00" + e.NameNorm
		id, err := tx.InsertFormElement(store.FormElement{
			IdentityKey: elKey, ComponentID: string(in.component), FormID: formID, OriginFileID: fileID,
			NameNorm: e.NameNorm, NameDisplay: e.NameDisplay, EType: e.EType, DataPath: e.DataPath,
		})
		if err != nil {
			return fmt.Errorf("form_element %s/%s: %w", rel, e.NameNorm, err)
		}
		ts.nodes.remember(elKey, id)
		ts.counts.formElement++
	}
	for i, c := range fs.Commands {
		cmdKey := formKey + "\x00command\x00" + itoaIndex(i) + "\x00" + c.NameNorm
		id, err := tx.InsertFormCommand(store.FormCommand{
			IdentityKey: cmdKey, ComponentID: string(in.component), FormID: formID, OriginFileID: fileID,
			NameNorm: c.NameNorm, NameDisplay: c.NameDisplay, ActionNorm: c.ActionNorm,
		})
		if err != nil {
			return fmt.Errorf("form_command %s/%s: %w", rel, c.NameNorm, err)
		}
		ts.nodes.remember(cmdKey, id)
		ts.counts.formCommand++
	}
	return nil
}

// formOwnerFromPath выводит MType+имя владельца формы из пути Form.xml
// (архитектура §8, платформенная конвенция выгрузки): ".../Forms/<Форма>/Ext/Form.xml"
// у объектов конфигурации — владелец два сегмента перед "Forms"; у общей
// формы ("CommonForms/<Форма>/Ext/Form.xml", "Forms" не появляется) владелец
// — она сама, вид CommonForm.
func formOwnerFromPath(relPath string) (mtype, nameNorm string, ok bool) {
	parts := strings.Split(domain.NormalizeModulePath(relPath), "/")
	for i, p := range parts {
		if strings.EqualFold(p, "Forms") && i >= 2 {
			if mt, known := ownerTypeToMType(parts[i-2]); known {
				return mt, domain.NormalizeName(parts[i-1]), true
			}
			return "", "", false
		}
	}
	if len(parts) >= 2 && strings.EqualFold(parts[0], "CommonForms") {
		return "CommonForm", domain.NormalizeName(parts[1]), true
	}
	return "", "", false
}

// formNameFromPath достаёт имя формы из пути: сегмент перед последним "Ext"
// (та же идиома, что bsl.ClassifyModule.formName — независимая копия,
// потому что тот словарь закрыт в internal/parse/bsl и наружу не выставлен).
func formNameFromPath(relPath string) string {
	parts := strings.Split(domain.NormalizeModulePath(relPath), "/")
	for i := len(parts) - 1; i >= 1; i-- {
		if strings.EqualFold(parts[i], "Ext") {
			return parts[i-1]
		}
	}
	return ""
}

func itoaIndex(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
