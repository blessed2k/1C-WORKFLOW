package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// effectiveInterceptModulePath — модуль, заимствуемый расширением "ext" в
// обеих фикстурах ниже (signature-change и form).
var effectiveInterceptModulePath = commonModulePath("ОбщегоНазначения99")

// seedEffectiveInterceptFixture строит проект (cfg + ext, ext.AppliesTo=cfg,
// ApplyOrder=1) с базовой функцией ПолучитьСкидку в cfg и ОДНОИМЁННОЙ
// функцией в ext, заимствующей тот же модуль. Оба слоя несут символ с одним
// и тем же именем (нужен, чтобы старая эвристика signatureInterceptors —
// name-match, expand.go — тоже нашла кандидата: тест ниже сравнивает её с
// точным effective-путём НА ОДНИХ И ТЕХ ЖЕ данных).
//
// withAnnotation переключает РЕАЛЬНОЕ содержимое BSL-текста расширения:
// true — функция несёт настоящую аннотацию &Вместо("ПолучитьСкидку")
// (значит это на самом деле перехватчик); false — функция с тем же именем,
// но БЕЗ какой-либо аннотации перехвата (одноимённое совпадение, которое
// НЕ является перехватом — просто две функции с одинаковым именем в разных
// слоях). Это ровно тот мутационный переключатель, которым тест ниже
// доказывает, что effective-путь (resolve.DeriveIntercepts) действительно
// читает аннотацию, а не выдаёт совпадение по имени за перехват.
func seedEffectiveInterceptFixture(t *testing.T, st *store.Store, withAnnotation bool) (uid string) {
	t.Helper()
	ctx := context.Background()
	uid = "sym-poluchit-skidku"
	modulePath := effectiveInterceptModulePath
	baseBody := "Функция ПолучитьСкидку(Товар) Экспорт\n\tВозврат 0;\nКонецФункции"
	extBody := "// одноимённая функция БЕЗ аннотации перехвата\nФункция ПолучитьСкидку(Товар) Экспорт\n\tВозврат 5;\nКонецФункции"
	if withAnnotation {
		extBody = "&Вместо(\"ПолучитьСкидку\")\nФункция ПолучитьСкидку(Товар) Экспорт\n\tВозврат ПродолжитьВызов(Товар) + 10;\nКонецФункции"
	}
	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		if err := tx.UpsertComponent(store.Component{ID: "ext", Kind: "extension", Root: "ext", AppliesTo: "cfg", ApplyOrder: 1}); err != nil {
			return err
		}

		fBase := fileHelper(t, tx, "cfg", modulePath, baseBody)
		mBase := moduleHelper(t, tx, "cfg", modulePath, "общегоназначения99", "ОбщегоНазначения99", fBase)
		symbolHelper(t, tx, symbolSpec{
			uid: uid, componentID: "cfg", nameNorm: "получитьскидку", nameDisplay: "ПолучитьСкидку",
			kind: "function", moduleID: mBase, fileID: fBase, export: true, span: spanOf(0, len(baseBody)),
		})

		fExt := fileHelper(t, tx, "ext", modulePath, extBody)
		mExt := moduleHelper(t, tx, "ext", modulePath, "общегоназначения99", "ОбщегоНазначения99", fExt)
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-poluchit-skidku-ext", componentID: "ext", nameNorm: "получитьскидку", nameDisplay: "ПолучитьСкидку",
			kind: "function", moduleID: mExt, fileID: fExt, export: true, span: sp(),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("seedEffectiveInterceptFixture: %v", err)
	}
	return uid
}

// TestEffectiveSignatureChangeInterceptorsPreciseVsHeuristic — критерий
// приёмки D08 п.3/4а/6: на ОДНИХ И ТЕХ ЖЕ данных (одноимённый символ в
// расширении, применяющемся к базовому компоненту) view=raw остаётся на
// старой эвристике (name-match, confidence 0.5, находит "перехватчика" ДАЖЕ
// когда аннотации нет), а view=effective строит точный факт из
// resolve.DeriveIntercepts: находит перехватчик ТОЛЬКО когда в исходнике
// реально есть &Вместо (и несёт confidence=1 + подлинный текст перехватчика
// с ПродолжитьВызов), и честно отдаёт missing, когда аннотации нет —
// мутация (withAnnotation false/true) доказывает, что effective-проверка
// содержательна, не заглушка "raw + пустой warning".
func TestEffectiveSignatureChangeInterceptorsPreciseVsHeuristic(t *testing.T) {
	const task = "Измени сигнатуру экспортной ОбщегоНазначения99.ПолучитьСкидку"

	t.Run("аннотация есть", func(t *testing.T) {
		st := openFixtureStore(t)
		seedEffectiveInterceptFixture(t, st, true)

		raw := buildFor(t, st, Request{Task: task, ProjectID: "p"})
		if raw.Intent.Primary != IntentSignatureChange {
			t.Fatalf("Intent.Primary = %q, want %q", raw.Intent.Primary, IntentSignatureChange)
		}
		rawCov, _ := coverageOf(raw, "interceptors")
		if rawCov.TotalCount != 1 {
			t.Fatalf("raw interceptors coverage = %+v, want total=1 (эвристика по имени)", rawCov)
		}
		var rawConf float64
		for _, s := range raw.Signatures {
			if s.Kind != "" && strings.Contains(s.WhyIncluded, "кандидат в перехватчик") {
				rawConf = float64(s.Confidence)
			}
		}
		if rawConf != 0.5 {
			t.Fatalf("raw interceptor confidence = %v, want 0.5 (heuristic)", rawConf)
		}

		eff := buildFor(t, st, Request{Task: task, ProjectID: "p", View: "effective"})
		effCov, _ := coverageOf(eff, "interceptors")
		if effCov.TotalCount != 1 {
			t.Fatalf("effective interceptors coverage = %+v, want total=1 (точный факт &Вместо)", effCov)
		}
		var found bool
		for _, s := range eff.Signatures {
			if s.Kind == "Вместо" {
				found = true
				if s.Confidence != 1 {
					t.Errorf("effective interceptor confidence = %v, want 1 (точный факт)", s.Confidence)
				}
				if !strings.Contains(s.Text, "ПродолжитьВызов") {
					t.Errorf("effective interceptor text = %q, want фактический текст перехватчика (ПродолжитьВызов)", s.Text)
				}
			}
		}
		if !found {
			t.Fatalf("effective Signatures = %+v, want запись с Kind=Вместо", eff.Signatures)
		}
		for _, w := range eff.Warnings {
			if w.Code == "no_interceptor_candidates" {
				t.Errorf("effective не должен нести no_interceptor_candidates, когда перехватчик реально найден: %+v", w)
			}
		}
	})

	t.Run("аннотации нет — одноимённое совпадение", func(t *testing.T) {
		st := openFixtureStore(t)
		seedEffectiveInterceptFixture(t, st, false)

		raw := buildFor(t, st, Request{Task: task, ProjectID: "p"})
		rawCov, _ := coverageOf(raw, "interceptors")
		if rawCov.TotalCount != 1 {
			t.Fatalf("raw interceptors coverage = %+v, want total=1 (эвристика по имени срабатывает и БЕЗ аннотации — это её честно названный предел)", rawCov)
		}

		eff := buildFor(t, st, Request{Task: task, ProjectID: "p", View: "effective"})
		effCov, ok := coverageOf(eff, "interceptors")
		if !ok || effCov.Status != Missing || effCov.TotalCount != 0 {
			t.Fatalf("effective interceptors coverage = %+v (ok=%v), want Missing/0 — одноимённая функция БЕЗ аннотации не перехватчик", effCov, ok)
		}
		found := false
		for _, w := range eff.Warnings {
			if w.Code == "no_interceptor_candidates" {
				found = true
				if !strings.Contains(w.Message, "ext") {
					t.Errorf("Message = %q, want упоминание слоя ext", w.Message)
				}
			}
		}
		if !found {
			t.Fatalf("effective.Warnings = %+v, want no_interceptor_candidates (модуль заимствован, но без аннотации)", eff.Warnings)
		}
	})
}

// TestEffectiveTwoInsteadConflictWarning — критерий приёмки D08: два
// расширения перехватывают один метод через &Вместо -> instead_conflict
// (diagnostic с обоими слоями, confidence<1), та же семантика, что
// internal/app/effective_test.go:TestGetSymbolEffectiveTwoInsteadConflict,
// воспроизведённая для get_context_for_task.
func TestEffectiveTwoInsteadConflictWarning(t *testing.T) {
	st := openFixtureStore(t)
	ctx := context.Background()
	uid := "sym-poluchit-skidku"
	modulePath := effectiveInterceptModulePath
	baseBody := "Функция ПолучитьСкидку(Товар) Экспорт\n\tВозврат 0;\nКонецФункции"
	insteadBody := "&Вместо(\"ПолучитьСкидку\")\nФункция ПолучитьСкидку(Товар) Экспорт\n\tВозврат 1;\nКонецФункции"

	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		if err := tx.UpsertComponent(store.Component{ID: "exta", Kind: "extension", Root: "exta", AppliesTo: "cfg", ApplyOrder: 1}); err != nil {
			return err
		}
		if err := tx.UpsertComponent(store.Component{ID: "extb", Kind: "extension", Root: "extb", AppliesTo: "cfg", ApplyOrder: 2}); err != nil {
			return err
		}
		fBase := fileHelper(t, tx, "cfg", modulePath, baseBody)
		mBase := moduleHelper(t, tx, "cfg", modulePath, "общегоназначения99", "ОбщегоНазначения99", fBase)
		symbolHelper(t, tx, symbolSpec{
			uid: uid, componentID: "cfg", nameNorm: "получитьскидку", nameDisplay: "ПолучитьСкидку",
			kind: "function", moduleID: mBase, fileID: fBase, export: true, span: spanOf(0, len(baseBody)),
		})
		for _, ext := range []string{"exta", "extb"} {
			fExt := fileHelper(t, tx, ext, modulePath, insteadBody)
			moduleHelper(t, tx, ext, modulePath, "общегоназначения99", "ОбщегоНазначения99", fExt)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	eff := buildFor(t, st, Request{Task: "Измени сигнатуру экспортной ОбщегоНазначения99.ПолучитьСкидку", ProjectID: "p", View: "effective"})
	var conflict *Warning
	for i := range eff.Warnings {
		if eff.Warnings[i].Code == "instead_conflict" {
			conflict = &eff.Warnings[i]
		}
	}
	if conflict == nil {
		t.Fatalf("Warnings = %+v, want instead_conflict", eff.Warnings)
	}
	if !strings.Contains(conflict.Message, "exta") || !strings.Contains(conflict.Message, "extb") {
		t.Errorf("Message = %q, want оба слоя exta и extb", conflict.Message)
	}
	effCov, _ := coverageOf(eff, "interceptors")
	if effCov.TotalCount != 2 {
		t.Fatalf("interceptors coverage = %+v, want total=2 (оба перехватчика — конфликт не выбирает один)", effCov)
	}
}

// seedEffectiveFormInterceptFixture строит Document Заказ с формой
// ФормаДокумента (обработчик СкладПриИзменении, событие ПриИзменении
// элемента "Склад") — базовая часть буквально повторяет form-раздел
// seedScenarioFixture (fixture_test.go), но с component ext.AppliesTo=cfg
// (там этого поля не было — тест сигнатур-конфликта form добавляется
// отдельным, чистым проектом, чтобы не трогать общую фикстуру пяти
// сценариев §25/fixture_test.go). ext заимствует ТОТ ЖЕ модуль формы и
// (при withAnnotation) перехватывает СкладПриИзменении через &Вместо.
func seedEffectiveFormInterceptFixture(t *testing.T, st *store.Store, withAnnotation bool) {
	t.Helper()
	ctx := context.Background()
	formModulePath := formModulePathOf("Document", "Заказ", "ФормаДокумента")
	baseBody := "Процедура СкладПриИзменении(Элемент) Экспорт\n\t// база\nКонецПроцедуры"
	extBody := "// одноимённая процедура БЕЗ аннотации перехвата\nПроцедура СкладПриИзменении(Элемент) Экспорт\n\t// не перехватчик\nКонецПроцедуры"
	if withAnnotation {
		extBody = "&Вместо(\"СкладПриИзменении\")\nПроцедура СкладПриИзменении(Элемент) Экспорт\n\tПродолжитьВызов(Элемент);\nКонецПроцедуры"
	}

	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		if err := tx.UpsertComponent(store.Component{ID: "ext", Kind: "extension", Root: "ext", AppliesTo: "cfg", ApplyOrder: 1}); err != nil {
			return err
		}

		fZakazMeta := fileHelper(t, tx, "cfg", declPath("Document", "Заказ"), "<meta/>")
		oZakaz, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказ", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fZakazMeta, Layer: "base",
		})
		if err != nil {
			return err
		}
		formZakaz, err := tx.EnsureForm(store.Form{
			IdentityKey: "cfg\x00form\x00Document.Заказ.Form.ФормаДокумента", ComponentID: "cfg",
			OwnerObjectID: oZakaz, NameNorm: "формадокумента", NameDisplay: "ФормаДокумента",
		})
		if err != nil {
			return err
		}
		fFormZakaz := fileHelper(t, tx, "cfg", formModulePath, baseBody)
		if err := tx.PutFormDeclaration(formZakaz, fFormZakaz); err != nil {
			return err
		}
		if _, err := tx.InsertFormElement(store.FormElement{
			IdentityKey: "cfg\x00formel\x00Document.Заказ.Form.ФормаДокумента.Склад", ComponentID: "cfg",
			FormID: formZakaz, OriginFileID: fFormZakaz, NameNorm: "склад", NameDisplay: "Склад",
			EType: "Field", DataPath: "Объект.Склад",
		}); err != nil {
			return err
		}
		mFormZakaz := moduleHelper(t, tx, "cfg", formModulePath, "формадокумента", "ФормаДокумента", fFormZakaz)
		sHandler := symbolHelper(t, tx, symbolSpec{
			uid: "sym-sklad-pri-izm-eff", componentID: "cfg", nameNorm: "складприизменении", nameDisplay: "СкладПриИзменении",
			kind: "procedure", moduleID: mFormZakaz, fileID: fFormZakaz, export: true, span: spanOf(0, len(baseBody)),
		})
		if err := tx.InsertHandlerBinding(store.HandlerBinding{
			FormID: formZakaz, Source: "склад", Event: "ПриИзменении", HandlerNameNorm: "складприизменении",
			HandlerSymbolID: sHandler, OriginFileID: fFormZakaz, Resolution: "resolved",
		}); err != nil {
			return err
		}

		fExt := fileHelper(t, tx, "ext", formModulePath, extBody)
		moduleHelper(t, tx, "ext", formModulePath, "формадокумента", "ФормаДокумента", fExt)
		return nil
	})
	if err != nil {
		t.Fatalf("seedEffectiveFormInterceptFixture: %v", err)
	}
}

// TestEffectiveFormHandlerIntercepts — критерий приёмки D08 п.4б: форма
// перехвачена расширением через модуль формы. view=raw не знает о
// перехватчике вовсе (нет категории handler_intercepts в выдаче); view=
// effective добавляет её с точным фактом (confidence=1, Kind=Вместо,
// подлинный текст с ПродолжитьВызов); мутация (withAnnotation=false) —
// одноимённая процедура без аннотации не порождает handler_intercepts.
func TestEffectiveFormHandlerIntercepts(t *testing.T) {
	const task = "Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ"

	t.Run("аннотация есть", func(t *testing.T) {
		st := openFixtureStore(t)
		seedEffectiveFormInterceptFixture(t, st, true)

		raw := buildFor(t, st, Request{Task: task, ProjectID: "p"})
		if raw.Intent.Primary != IntentForm {
			t.Fatalf("Intent.Primary = %q, want %q", raw.Intent.Primary, IntentForm)
		}
		for _, s := range raw.Signatures {
			if s.Kind == "Вместо" {
				t.Fatalf("raw не должен знать о перехватчиках формы: %+v", raw.Signatures)
			}
		}

		eff := buildFor(t, st, Request{Task: task, ProjectID: "p", View: "effective"})
		var found bool
		for _, s := range eff.Signatures {
			if s.Kind == "Вместо" {
				found = true
				if s.Confidence != 1 {
					t.Errorf("confidence = %v, want 1", s.Confidence)
				}
				if !strings.Contains(s.Text, "ПродолжитьВызов") {
					t.Errorf("Text = %q, want фактический текст перехватчика", s.Text)
				}
			}
		}
		if !found {
			t.Fatalf("effective Signatures = %+v, want запись Kind=Вместо (handler_intercepts)", eff.Signatures)
		}
	})

	t.Run("аннотации нет", func(t *testing.T) {
		st := openFixtureStore(t)
		seedEffectiveFormInterceptFixture(t, st, false)

		eff := buildFor(t, st, Request{Task: task, ProjectID: "p", View: "effective"})
		for _, s := range eff.Signatures {
			if s.Kind == "Вместо" {
				t.Fatalf("effective не должен считать одноимённую процедуру без аннотации перехватчиком: %+v", eff.Signatures)
			}
		}
	})
}
