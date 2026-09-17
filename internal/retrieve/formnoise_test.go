package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestFormIntentSuppressesNoFormsNoiseFromMatchedHomonym — находка F3: у объекта "Заказ"
// есть омоним в другом компоненте, у которого форм нет вовсе. До фикса ответ
// нёс одновременно настоящие находки по компоненту cfg (форма, обработчик,
// серверный вызов) И предупреждение no_forms про омоним из ext — то есть
// выглядел противоречащим сам себе. После фикса: раз хотя бы один анкер
// intent=form дал настоящую находку, шум от анкера-омонима без форм не
// попадает в ответ.
func TestFormIntentSuppressesNoFormsNoiseFromMatchedHomonym(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st) // component "cfg": Document Заказ, форма, обработчик СкладПриИзменении
	seedFormlessHomonymInExt(t, st)

	task := "Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ"
	res := buildFor(t, st, Request{Task: task, ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
	}

	// Настоящая находка по cfg — на месте (тот же контракт, что TestScenario4Form).
	text := factsAndRelationsText(res)
	if !strings.Contains(text, "СкладПриИзменении") {
		t.Fatalf("тело обработчика cfg-компонента не найдено, находка потеряна вместе с шумом: %q", text)
	}

	for _, w := range res.Warnings {
		if w.Code == "no_forms" || w.Code == "form_binding_not_matched" {
			t.Errorf("предупреждение %q про омоним без форм просочилось в ответ рядом с настоящей находкой: %+v", w.Code, w)
		}
	}
}

// TestFormIntentKeepsNoFormsWhenNoMatchAnywhere — зеркальное условие: когда
// НИ один анкер intent=form не дал настоящей находки, no_forms остаётся
// честным предупреждением, не шумом. Без этого теста фикс мог бы тихо
// подавлять no_forms всегда, а не только когда есть чему его заменить.
func TestFormIntentKeepsNoFormsWhenNoMatchAnywhere(t *testing.T) {
	st := openFixtureStore(t)
	seedFormlessObjectOnly(t, st)

	res := buildFor(t, st, Request{Task: "Что происходит на форме документа БезФорм", ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
	}
	found := false
	for _, w := range res.Warnings {
		if w.Code == "no_forms" {
			found = true
		}
	}
	if !found {
		t.Errorf("no_forms не отдан при полном отсутствии находок: %+v", res.Warnings)
	}
}

// seedFormlessHomonymInExt добавляет в УЖЕ зарегистрированный компонент
// "ext" (seedScenarioFixture его заводит) объект Document "Заказ" — тот же
// NameNorm, что и настоящий объект в "cfg", — без единой формы.
func seedFormlessHomonymInExt(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		fMeta := fileHelper(t, tx, "ext", declPath("Document", "Заказ"), "<meta/>")
		_, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "ext\x00object\x00Document\x00заказ", ComponentID: "ext",
			MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fMeta, Layer: "extension",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seedFormlessHomonymInExt: %v", err)
	}
}

// seedFormlessObjectOnly — единственный объект во всей фикстуре, форм нет —
// зеркальная сцена без второго анкера, который мог бы дать настоящую находку.
func seedFormlessObjectOnly(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		fMeta := fileHelper(t, tx, "cfg", declPath("Document", "БезФорм"), "<meta/>")
		_, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00безформ", ComponentID: "cfg",
			MType: "Document", NameNorm: "безформ", NameDisplay: "БезФорм", FileID: fMeta, Layer: "base",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seedFormlessObjectOnly: %v", err)
	}
}

// TestFormIntentDoesNotSuppressNoFormsForUnrelatedObject — находка ревью
// (Codex stop-gate) над прогоном reindex-timings: первая версия
// suppressFormNoiseWhenMatched гасила no_forms/form_binding_not_matched при
// ЛЮБОЙ настоящей находке во всём ответе, не только у анкеров ТОГО ЖЕ имени.
// Если задача упоминает два РАЗНЫХ объекта — не омонима один другого, — и у
// одного есть форма и обработчик, а у другого форм действительно нет, честное
// no_forms про второй объект пряталось за находкой по первому. Это не тот
// шум, который чинит F3: два разных объекта — не два анкера одного омонима,
// и предупреждение про второй объект — единственная содержательная
// информация о нём, её терять нельзя.
func TestFormIntentDoesNotSuppressNoFormsForUnrelatedObject(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st) // component "cfg": Document Заказ, форма, обработчик СкладПриИзменении
	seedFormlessDifferentObject(t, st)

	task := "Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ, " +
		"заодно посмотри форму документа СчетФактура"
	res := buildFor(t, st, Request{Task: task, ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
	}

	// Настоящая находка по Заказу — на месте, как и раньше.
	text := factsAndRelationsText(res)
	if !strings.Contains(text, "СкладПриИзменении") {
		t.Fatalf("тело обработчика Заказа не найдено: %q", text)
	}

	// Честное no_forms про СЧЕТФАКТУРУ (объект без форм, НЕ омоним Заказа)
	// обязано остаться — оно не про Заказ и не шум от него.
	found := false
	for _, w := range res.Warnings {
		if w.Code == "no_forms" && strings.Contains(w.Message, "СчетФактура") {
			found = true
		}
		if w.Code == "no_forms" && strings.Contains(w.Message, "Заказ") {
			t.Errorf("no_forms про Заказ не должно быть вовсе — у Заказа есть форма: %+v", w)
		}
	}
	if !found {
		t.Errorf("no_forms про СчетФактуру пропало — подавлено находкой по НЕСВЯЗАННОМУ объекту Заказ: %+v", res.Warnings)
	}
}

// seedFormlessDifferentObject — Document "СчетФактура": другое имя, не
// омоним "Заказ", форм не имеет. В том же компоненте "cfg", что и Заказ —
// разные объекты одного компонента должны быть так же независимы, как и
// объекты разных компонентов.
func seedFormlessDifferentObject(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		fMeta := fileHelper(t, tx, "cfg", declPath("Document", "СчетФактура"), "<meta/>")
		_, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00счетфактура", ComponentID: "cfg",
			MType: "Document", NameNorm: "счетфактура", NameDisplay: "СчетФактура", FileID: fMeta, Layer: "base",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seedFormlessDifferentObject: %v", err)
	}
}

// TestFormIntentDoesNotSuppressNoFormsForSameNameDifferentType — находка
// второго круга ревью (Codex stop-gate): группировка только по ObjectName
// (без ObjectType) объединяла объекты РАЗНЫХ видов с одним именем — тот же
// факт, что описан в комментарии dedupWarnings (build.go): одно имя
// резолвится MetadataObjectsByNameNormAnyType сразу в несколько объектов
// разных MType (например Catalog + CommonPicture «Номенклатура»). Документ
// «Заказ» с формой и картинка «Заказ» без форм — генуинно разные объекты,
// не один и тот же объект в другом компоненте. Находка по документу не
// должна гасить честное no_forms картинки.
func TestFormIntentDoesNotSuppressNoFormsForSameNameDifferentType(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st) // component "cfg": Document Заказ, форма, обработчик СкладПриИзменении
	seedFormlessSameNameDifferentType(t, st)

	task := "Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ"
	res := buildFor(t, st, Request{Task: task, ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
	}

	text := factsAndRelationsText(res)
	if !strings.Contains(text, "СкладПриИзменении") {
		t.Fatalf("тело обработчика документа Заказ не найдено: %q", text)
	}

	found := false
	for _, w := range res.Warnings {
		if w.Code == "no_forms" && strings.Contains(w.Message, "Заказ") {
			found = true
		}
	}
	if !found {
		t.Errorf("no_forms про картинку Заказ (другой тип, не документ) пропало — "+
			"подавлено находкой по документу того же имени, разного типа: %+v", res.Warnings)
	}
}

// seedFormlessSameNameDifferentType — CommonPicture "Заказ": то же имя, что
// у Document "Заказ" из seedScenarioFixture, другой ObjectType. Картинка
// структурно не может иметь форм.
func seedFormlessSameNameDifferentType(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		fMeta := fileHelper(t, tx, "cfg", declPath("CommonPicture", "Заказ"), "<meta/>")
		_, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00CommonPicture\x00заказ", ComponentID: "cfg",
			MType: "CommonPicture", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fMeta, Layer: "base",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seedFormlessSameNameDifferentType: %v", err)
	}
}

// TestFormIntentNoFormsMessageNamesTypeAndComponent — находка третьего круга
// ревью (Codex stop-gate): даже когда no_forms корректно НЕ подавлено (D02,
// D03 — генуинно разный объект), текст предупреждения нёс только голое
// Display-имя и был неотличим от предупреждения про омоним другого вида или
// компонента — рядом с настоящей находкой ЧИТАЛСЯ как противоречие, даже
// оставаясь технически верным решением. Текст обязан называть тип и
// компонент объекта, не только имя.
func TestFormIntentNoFormsMessageNamesTypeAndComponent(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	seedFormlessSameNameDifferentType(t, st)

	task := "Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ"
	res := buildFor(t, st, Request{Task: task, ProjectID: "retrieve-fixture"})

	for _, w := range res.Warnings {
		if w.Code != "no_forms" {
			continue
		}
		if !strings.Contains(w.Message, "CommonPicture") {
			t.Errorf("no_forms не называет тип объекта (CommonPicture): %q", w.Message)
		}
		if !strings.Contains(w.Message, "cfg") {
			t.Errorf("no_forms не называет компонент объекта (cfg): %q", w.Message)
		}
	}
}
