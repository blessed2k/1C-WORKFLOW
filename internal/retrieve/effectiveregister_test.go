package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Общие помощники тестов effective-вида для register/query/rights/
// add-attribute (ADR-035). Расширение ext-a применяется к cfg: без AppliesTo
// наложение его не видит вовсе (effective.ApplyingTo), и тест проверял бы
// пустоту, а не наложение.

const effExt = "ext-a"

func seedEffComponents(t *testing.T, tx *store.WriteTx) {
	t.Helper()
	if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
		t.Fatalf("UpsertComponent(cfg): %v", err)
	}
	if err := tx.UpsertComponent(store.Component{ID: effExt, Kind: "extension", Root: effExt, AppliesTo: "cfg", ApplyOrder: 1}); err != nil {
		t.Fatalf("UpsertComponent(%s): %v", effExt, err)
	}
}

// seedEffObject заводит объект метаданных в компоненте componentID с файлом
// объявления по раскладке выгрузки. У заимствованного объекта строка своя в
// каждом компоненте: так их публикует индекс (identity_key включает компонент).
func seedEffObject(t *testing.T, tx *store.WriteTx, componentID, mtype, nameDisplay string) int64 {
	t.Helper()
	nameNorm := domain.NormalizeName(nameDisplay)
	f := fileHelper(t, tx, componentID, declPath(mtype, nameDisplay), "<meta/>")
	layer := "base"
	if componentID != "cfg" {
		layer = componentID
	}
	id, err := tx.EnsureMetadataObject(store.MetadataObject{
		IdentityKey: componentID + "\x00object\x00" + mtype + "\x00" + nameNorm, ComponentID: componentID,
		MType: mtype, NameNorm: nameNorm, NameDisplay: nameDisplay, FileID: f, Layer: layer,
	})
	if err != nil {
		t.Fatalf("EnsureMetadataObject(%s.%s@%s): %v", mtype, nameDisplay, componentID, err)
	}
	return id
}

// seedEffMethod заводит модуль rel в компоненте componentID с текстом body и
// один символ на весь текст. Текст модуля расширения разбирается наложением
// (internal/effective) на чтении, поэтому аннотация перехвата живёт в body.
func seedEffMethod(t *testing.T, tx *store.WriteTx, componentID, rel, methodDisplay, body string) (symbolID, fileID int64) {
	t.Helper()
	f := fileHelper(t, tx, componentID, rel, body)
	m := moduleHelper(t, tx, componentID, rel, domain.NormalizeName(rel), rel, f)
	s := symbolHelper(t, tx, symbolSpec{
		uid: "sym-" + componentID + "-" + rel + "-" + methodDisplay, componentID: componentID,
		nameNorm: domain.NormalizeName(methodDisplay), nameDisplay: methodDisplay, kind: "procedure",
		moduleID: m, fileID: f, span: spanOf(0, len(body)),
	})
	return s, f
}

func hasWarning(r Result, code string) bool {
	for _, w := range r.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func signaturesOfKind(r Result, kind string) []Signature {
	var out []Signature
	for _, s := range r.Signatures {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// --- register ---------------------------------------------------------

const effRegisterTask = "Кто пишет в регистр ОстаткиТоваров"

// seedEffRegisterFixture: регистр ОстаткиТоваров и два документа.
//
//   - Приход: базовый ОбработкаПроведения пишет в регистр; ext-a перехватывает
//     его через &Вместо, сам перехватчик в регистр не пишет. Запись базового
//     слоя в effective-виде заменена: без факта перехвата агент её видит как
//     исполняемую.
//   - Расход: базовый обработчик в регистр не пишет; перехватчик ext-a
//     &После("ОбработкаПроведения") пишет (register_access со слоем ext-a).
//     В raw эта запись видна как запись безымянной процедуры расширения, без
//     связи с проведением базового документа.
//
// Оба перехватчика одного расширения метят в одно и то же имя
// ОбработкаПроведения в РАЗНЫХ модулях: ключ кандидата перехвата обязан их
// различать, иначе второй молча пропадает при дедупликации.
func seedEffRegisterFixture(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		seedEffComponents(t, tx)
		oReg := seedEffObject(t, tx, "cfg", "AccumulationRegister", "ОстаткиТоваров")
		seedEffObject(t, tx, "cfg", "Document", "Приход")
		seedEffObject(t, tx, "cfg", "Document", "Расход")

		sPrihod, fPrihod := seedEffMethod(t, tx, "cfg", objModulePath("Document", "Приход"), "ОбработкаПроведения",
			"Процедура ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ОстаткиТоваров.Записать();\nКонецПроцедуры")
		if err := tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fPrihod, SymbolID: sPrihod, ObjectID: oReg, RegisterNameNorm: "остаткитоваров",
			Mode: "movement", Static: true, Confidence: 1, Span: sp(), Layer: "base",
		}); err != nil {
			return err
		}
		seedEffMethod(t, tx, "cfg", objModulePath("Document", "Расход"), "ОбработкаПроведения",
			"Процедура ОбработкаПроведения(Отказ, РежимПроведения)\nКонецПроцедуры")

		seedEffMethod(t, tx, effExt, objModulePath("Document", "Приход"), "РасшА_ОбработкаПроведения",
			"&Вместо(\"ОбработкаПроведения\")\nПроцедура РасшА_ОбработкаПроведения(Отказ, РежимПроведения)\nКонецПроцедуры")
		sAfter, fAfter := seedEffMethod(t, tx, effExt, objModulePath("Document", "Расход"), "РасшА_ПослеПроведения",
			"&После(\"ОбработкаПроведения\")\nПроцедура РасшА_ПослеПроведения(Отказ, РежимПроведения)\n\tДвижения.ОстаткиТоваров.Записать();\nКонецПроцедуры")
		return tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fAfter, SymbolID: sAfter, ObjectID: oReg, RegisterNameNorm: "остаткитоваров",
			Mode: "movement", Static: true, Confidence: 1, Span: sp(), Layer: effExt,
		})
	})
	if err != nil {
		t.Fatalf("seedEffRegisterFixture: %v", err)
	}
}

// TestEffectiveRegisterWriterIntercepts: register под view=effective несёт
// перехватчики писателей регистра (категория writer_intercepts) в обе
// стороны: базовый писатель, перехваченный расширением, и запись, сделанная
// самим перехватчиком. raw остаётся прежним: обе записи на месте, фактов
// перехвата нет.
func TestEffectiveRegisterWriterIntercepts(t *testing.T) {
	st := openFixtureStore(t)
	seedEffRegisterFixture(t, st)

	raw := buildFor(t, st, Request{Task: effRegisterTask, ProjectID: "p"})
	if raw.Intent.Primary != IntentRegister {
		t.Fatalf("Intent.Primary = %q, want %q", raw.Intent.Primary, IntentRegister)
	}
	if n := len(signaturesOfKind(raw, "Вместо")) + len(signaturesOfKind(raw, "После")); n != 0 {
		t.Fatalf("view=raw не несёт фактов перехвата, получено %d: %+v", n, raw.Signatures)
	}
	requireCoverageStatus(t, raw, "writes_movements", CompleteInline)
	if c, _ := coverageOf(raw, "writes_movements"); c.TotalCount != 2 {
		t.Fatalf("raw writes_movements total = %d, want 2 (базовая запись и запись перехватчика, raw не меняется)", c.TotalCount)
	}

	eff := buildFor(t, st, Request{Task: effRegisterTask, ProjectID: "p", View: "effective"})
	if hasWarning(eff, "effective_view_partial_coverage") {
		t.Fatalf("register под effective не строится как raw, предупреждения быть не должно: %+v", eff.Warnings)
	}
	instead := signaturesOfKind(eff, "Вместо")
	if len(instead) != 1 || instead[0].Component != effExt || instead[0].Module != objModulePath("Document", "Приход") ||
		!strings.Contains(instead[0].Text, "РасшА_ОбработкаПроведения") {
		t.Fatalf("effective: ожидался перехватчик &Вместо базового писателя Приход из %s, получено %+v", effExt, instead)
	}
	if !strings.Contains(instead[0].WhyIncluded, "ОстаткиТоваров") {
		t.Errorf("WhyIncluded перехвата писателя обязан называть регистр: %q", instead[0].WhyIncluded)
	}
	after := signaturesOfKind(eff, "После")
	if len(after) != 1 || after[0].Component != effExt || after[0].Module != objModulePath("Document", "Расход") ||
		!strings.Contains(after[0].Text, "Движения.ОстаткиТоваров") {
		t.Fatalf("effective: ожидался перехватчик &После, сам пишущий в регистр (Расход, %s), получено %+v", effExt, after)
	}
	var extWrite *Relation
	for i := range eff.Relations {
		if eff.Relations[i].Component == effExt {
			extWrite = &eff.Relations[i]
		}
	}
	if extWrite == nil || !strings.Contains(extWrite.WhyIncluded, "перехватчик") {
		t.Fatalf("запись из перехватчика обязана называть себя перехватчиком базового обработчика: %+v", extWrite)
	}
}
