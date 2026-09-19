package index

import (
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// publishFormDecls вставляет формы, объявленные владельцем (аспект
// form_declaration, архитектура §15): ownerObjectID — id объекта метаданных,
// в чьём файле встретились эти FormDeclFact (у CommonForm это её же id —
// «форма, которой является сам объект», meta/facts.go). Вызывается из
// publishMetadataObject: FormDecls всегда идут вместе с Object в одном файле.
func publishFormDecls(tx *store.WriteTx, ts *txState, rel string, fileID, ownerObjectID int64, decls []store.Form) error {
	for _, f := range decls {
		f.OwnerObjectID = ownerObjectID
		formID, err := tx.EnsureForm(f)
		if err != nil {
			return fmt.Errorf("form (decl) %s/%s: %w", rel, f.NameNorm, err)
		}
		ts.nodes.remember(f.IdentityKey, formID)
		if err := tx.PutFormDeclaration(formID, fileID); err != nil {
			return fmt.Errorf("form_declaration %s/%s: %w", rel, f.NameNorm, err)
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
func publishFormStructure(tx *store.WriteTx, ts *txState, rel string, fileID int64, fp *formStructurePlan) error {
	var ownerID int64
	if fp.ownerKey != "" {
		// По строке metadata_object, не по узлу: узел объекта, чей XML удалён
		// в этом же инкременте, живёт до reconciliation (issue #14). XML
		// объекта сортируется раньше своего каталога ("X.xml" < "X/..."),
		// поэтому переопубликуемый объект к этому моменту уже вставлен.
		if id, found, err := tx.MetadataObjectID(fp.ownerKey); err != nil {
			return fmt.Errorf("владелец формы %s: %w", rel, err)
		} else if found {
			ownerID = id
		}
	}
	row := fp.row
	row.OwnerObjectID = ownerID
	formID, err := tx.EnsureForm(row)
	if err != nil {
		return fmt.Errorf("form (structure) %s: %w", rel, err)
	}
	ts.nodes.remember(fp.key, formID)
	if err := tx.PutFormStructure(formID, fileID); err != nil {
		return fmt.Errorf("form_structure %s: %w", rel, err)
	}
	for _, e := range fp.elements {
		e.FormID, e.OriginFileID = formID, fileID
		id, err := tx.InsertFormElement(e)
		if err != nil {
			return fmt.Errorf("form_element %s/%s: %w", rel, e.NameNorm, err)
		}
		ts.nodes.remember(e.IdentityKey, id)
		ts.counts.formElement++
	}
	for _, c := range fp.commands {
		c.FormID, c.OriginFileID = formID, fileID
		id, err := tx.InsertFormCommand(c)
		if err != nil {
			return fmt.Errorf("form_command %s/%s: %w", rel, c.NameNorm, err)
		}
		ts.nodes.remember(c.IdentityKey, id)
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
