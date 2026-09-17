package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// seedPostingOwnerUnknownFixture — документ Заказ, объявление которого лежит
// в файле, чьё имя НЕ совпадает с именем объекта ("Documents/
// Заказ_Метаданные.xml"): objectModuleDir не выводит каталог модулей ни по
// каталогу файла ("documents" — общий префикс всех документов), ни по пути без
// расширения ("documents/заказ_метаданные"). Владение вывести не из чего, и
// findPostingHandler откатывается на правило подстроки.
//
// Рядом заведён документ-омоним ЗаказКлиента с таким же обработчиком, и его
// модуль вставлен ПЕРВЫМ: правило подстроки отдаёт именно его — тот самый
// дефект D04, ради которого правило владения и появилось. Тест фиксирует, что
// откат ВИДЕН потребителю, а не применяется молча.
func seedPostingOwnerUnknownFixture(t *testing.T, st *store.Store) {
	t.Helper()
	baseBody := "Процедура ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ТоварыНаСкладах.Записать();\nКонецПроцедуры"

	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		fZKMeta := fileHelper(t, tx, "cfg", declPath("Document", "ЗаказКлиента"), "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказклиента", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказклиента", NameDisplay: "ЗаказКлиента", FileID: fZKMeta, Layer: "base",
		}); err != nil {
			return err
		}
		fZKMod := fileHelper(t, tx, "cfg", postingModuleZK, baseBody)
		mZKMod := moduleHelper(t, tx, "cfg", postingModuleZK, "заказклиента", "ЗаказКлиента", fZKMod)
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-op-zk", componentID: "cfg", nameNorm: "обработкапроведения",
			nameDisplay: "ОбработкаПроведения", kind: "procedure", moduleID: mZKMod, fileID: fZKMod,
			span: spanOf(0, len(baseBody)),
		})

		// НАМЕРЕННО неконформный литерал: по ФОРМЕ раскладка правильная
		// (гард fileHelper её принимает), а по СМЫСЛУ имя файла не совпадает
		// с именем объекта — это и есть проверяемый случай «каталог модулей
		// вывести не из чего». Через declPath он невыразим: declPath всегда
		// даёт имя по объекту. Не заменять на помощник.
		fZakazMeta := fileHelper(t, tx, "cfg", "Documents/Заказ_Метаданные.xml", "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказ", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fZakazMeta, Layer: "base",
		}); err != nil {
			return err
		}
		fZakazMod := fileHelper(t, tx, "cfg", postingModuleZakaz, baseBody)
		mZakazMod := moduleHelper(t, tx, "cfg", postingModuleZakaz, "заказ", "Заказ", fZakazMod)
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-op-zakaz", componentID: "cfg", nameNorm: "обработкапроведения",
			nameDisplay: "ОбработкаПроведения", kind: "procedure", moduleID: mZakazMod, fileID: fZakazMod,
			span: spanOf(0, len(baseBody)),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("seedPostingOwnerUnknownFixture: %v", err)
	}
}

// TestPostingHandlerSubstringFallbackIsVisible — пункт 4 таска 09: когда
// каталог модулей объекта вывести не из чего, findPostingHandler откатывается
// на правило подстроки — то самое, которое отдавало обработчик ЧУЖОГО
// документа (D04). В ответе это обязано быть видно предупреждением, иначе
// исправленный дефект возвращается тихо.
func TestPostingHandlerSubstringFallbackIsVisible(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingOwnerUnknownFixture(t, st)

	res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p"})
	if warningCodes(res)["posting_handler_owner_unknown"] != 1 {
		t.Fatalf("posting_handler_owner_unknown = %d, want 1: %+v",
			warningCodes(res)["posting_handler_owner_unknown"], res.Warnings)
	}
	// Откат действительно применён и действительно опасен: выигрывает
	// обработчик омонима ЗаказКлиента. Предупреждение — единственное, что об
	// этом говорит потребителю.
	var handlerModule string
	for _, s := range res.Snippets {
		if strings.Contains(s.WhyIncluded, "(posting_handler)") {
			handlerModule = s.Module
		}
	}
	if handlerModule != postingModuleZK {
		t.Errorf("posting_handler Module = %q, want %q (откат по подстроке отдаёт омонима)", handlerModule, postingModuleZK)
	}
}

// seedPostingOwnerUnknownNoHandlerFixture — тот же документ Заказ с
// объявлением, из которого каталог модулей не выводится, но в индексе НЕТ ни
// одного обработчика проведения, чей путь модуля содержал бы "заказ". Откат
// на правило подстроки применяется и не находит ничего: исход, при котором
// прежний текст предупреждения («отдаёт обработчик документа-омонима»)
// описывает не тот факт — омонима в ответе нет, в ответе нет обработчика
// вообще.
func seedPostingOwnerUnknownNoHandlerFixture(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		// НАМЕРЕННО неконформный литерал: по ФОРМЕ раскладка правильная
		// (гард fileHelper её принимает), а по СМЫСЛУ имя файла не совпадает
		// с именем объекта — это и есть проверяемый случай «каталог модулей
		// вывести не из чего». Через declPath он невыразим: declPath всегда
		// даёт имя по объекту. Не заменять на помощник.
		fZakazMeta := fileHelper(t, tx, "cfg", "Documents/Заказ_Метаданные.xml", "<meta/>")
		_, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказ", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fZakazMeta, Layer: "base",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seedPostingOwnerUnknownNoHandlerFixture: %v", err)
	}
}

// postingOwnerUnknownMessage — текст единственного предупреждения
// posting_handler_owner_unknown в ответе.
func postingOwnerUnknownMessage(t *testing.T, res Result) string {
	t.Helper()
	var out []string
	for _, w := range res.Warnings {
		if w.Code == "posting_handler_owner_unknown" {
			out = append(out, w.Message)
		}
	}
	if len(out) != 1 {
		t.Fatalf("posting_handler_owner_unknown встретилось %d раз(а), want 1: %+v", len(out), res.Warnings)
	}
	return out[0]
}

// TestPostingOwnerUnknownWarningNamesOutcome — находка ревью по таску 09:
// один и тот же текст уходил на ДВА разных исхода отката. Утверждение
// «прежнее правило подстроки отдаёт обработчик документа-омонима» верно
// только когда обработчик найден; когда откат не нашёл ничего, тот же текст
// описывает не тот факт — потребитель ищет в ответе омонима, которого там
// нет. Предупреждение обязано называть фактический исход.
func TestPostingOwnerUnknownWarningNamesOutcome(t *testing.T) {
	t.Run("откат отдал омонима", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingOwnerUnknownFixture(t, st)

		res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p"})
		msg := postingOwnerUnknownMessage(t, res)
		if !strings.Contains(msg, "омоним") {
			t.Errorf("исход «обработчик найден откатом» не назван омонимией: %q", msg)
		}
		if !strings.Contains(msg, postingModuleZK) {
			t.Errorf("предупреждение не называет модуль найденного обработчика %q: %q", postingModuleZK, msg)
		}
	})

	t.Run("откат не нашёл ничего", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingOwnerUnknownNoHandlerFixture(t, st)

		res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p"})
		for _, s := range res.Snippets {
			if strings.Contains(s.WhyIncluded, "(posting_handler)") {
				t.Fatalf("фикстура не выражает случай «обработчик не найден»: %+v", res.Snippets)
			}
		}
		msg := postingOwnerUnknownMessage(t, res)
		if strings.Contains(msg, "омоним") {
			t.Errorf("обработчик не найден вовсе, а предупреждение говорит про омонима: %q", msg)
		}
		if !strings.Contains(msg, "не найден") {
			t.Errorf("предупреждение не называет фактический исход «обработчик не найден»: %q", msg)
		}
	})
}
