package retrieve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Отзыв заявлений forms/rls (ADR-030, ADR-035): сбой чтения заимствований и
// анкер, который builder не развернул, не имеют права превратить пустую
// категорию в complete_empty.

// seedNoteFixture: документ Заметка в cfg и его заимствование в ext-a, без
// форм; одна базовая роль с правом без RLS. withSymbol добавляет общий модуль
// с процедурой ПроверитьЗаметку: её имя в тексте задачи даёт второй анкер
// вида symbol, который add-attribute и rights не разворачивают.
func seedNoteFixture(t *testing.T, st *store.Store, withSymbol bool) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		seedEffComponents(t, tx)
		oid := seedEffObject(t, tx, "cfg", "Document", "Заметка")
		seedEffObject(t, tx, effExt, "Document", "Заметка")
		decl, _, err := tx.SourceFileID("cfg", declPath("Document", "Заметка"))
		if err != nil {
			return err
		}
		rid, err := tx.EnsureRole(store.Role{ComponentID: "cfg", NameNorm: "читатель", NameDisplay: "Читатель", FileID: decl, Layer: "base"})
		if err != nil {
			return err
		}
		if err := tx.InsertRoleRight(store.RoleRight{
			RoleID: rid, ObjectID: oid, ObjectNameNorm: "document.заметка", RightName: "Чтение", Value: true, OriginFileID: decl,
		}); err != nil {
			return err
		}
		if withSymbol {
			seedEffMethod(t, tx, "cfg", commonModulePath("ЗаметкиСервер"), "ПроверитьЗаметку",
				"Процедура ПроверитьЗаметку() Экспорт\nКонецПроцедуры")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedNoteFixture: %v", err)
	}
}

type failingBorrowed struct{}

func (failingBorrowed) BorrowedObjects(store.MetadataObjectRow) ([]store.MetadataObjectRow, error) {
	return nil, errors.New("отказ чтения заимствований")
}

func buildWith(t *testing.T, st *store.Store, seams readSeams, req Request) Result {
	t.Helper()
	var out Result
	err := st.Read(context.Background(), func(tx *store.ReadTx) error {
		r, berr := buildWithSeams(context.Background(), tx, seams, req)
		out = r
		return berr
	})
	if err != nil {
		t.Fatalf("buildWithSeams: %v", err)
	}
	return out
}

const (
	noteAddAttrTask = "Добавь реквизит Автор в документ Заметка"
	noteRightsTask  = "Пользователь не видит документ Заметка, нужен разбор прав и RLS"
)

// TestEffectiveDeclarationRevokedOnBorrowedReadFailure: заимствования не
// прочитались, значит пустота forms/rls ничего не доказывает: не
// complete_empty, и сбой назван предупреждением. Контроль на том же входе с
// рабочим источником даёт complete_empty: тест различает исходы, а не
// совпадает с ними случайно.
func TestEffectiveDeclarationRevokedOnBorrowedReadFailure(t *testing.T) {
	cases := []struct{ task, category string }{
		{noteAddAttrTask, "forms"},
		{noteRightsTask, "rls"},
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			st := openFixtureStore(t)
			seedNoteFixture(t, st, false)
			req := Request{Task: tc.task, ProjectID: "p", View: "effective"}

			ok := buildWith(t, st, readSeams{}, req)
			requireCoverageStatus(t, ok, tc.category, CompleteEmpty)

			failed := buildWith(t, st, readSeams{objects: failingBorrowed{}}, req)
			requireCoverageStatus(t, failed, tc.category, Missing)
			if !hasWarning(failed, "effective_borrowed_objects_read_failed") {
				t.Fatalf("сбой чтения заимствований обязан быть назван: %+v", failed.Warnings)
			}
		})
	}
}

// TestEffectiveDeclarationRevokedByUncollectedAnchor: второй анкер (символ
// ПроверитьЗаметку) builder add-attribute/rights не разворачивает, то есть
// категорию по нему никто не собирал. Заявление анкера-объекта не делает её
// complete_empty (пограничный случай ADR-030 с несколькими анкерами).
func TestEffectiveDeclarationRevokedByUncollectedAnchor(t *testing.T) {
	cases := []struct{ task, category string }{
		{noteAddAttrTask + " и проверь ПроверитьЗаметку", "forms"},
		{noteRightsTask + " в ПроверитьЗаметку", "rls"},
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			st := openFixtureStore(t)
			seedNoteFixture(t, st, true)
			res := buildFor(t, st, Request{Task: tc.task, ProjectID: "p", View: "effective"})
			var sawSymbol bool
			for _, a := range res.Anchors {
				sawSymbol = sawSymbol || a.Kind == "symbol"
			}
			if !sawSymbol {
				t.Fatalf("фикстура обязана дать анкер-символ: %+v", res.Anchors)
			}
			requireCoverageStatus(t, res, tc.category, Missing)
		})
	}
}

// failingAnchorQueries отказывает в чтении запросов ровно одного символа
// (анкера), запросы перехватчиков читаются из транзакции.
type failingAnchorQueries struct {
	tx       *store.ReadTx
	failName string
}

func (f failingAnchorQueries) QueriesBySymbolID(id int64) ([]store.QueryRow, error) {
	if sym, ok, _ := f.tx.SymbolByID(id); ok && sym.NameNorm == f.failName {
		return nil, errors.New("отказ чтения запросов анкера")
	}
	return f.tx.QueriesBySymbolID(id)
}

// TestEffectiveQueryAnchorReadFailureNamed: запросы анкера не прочитались, у
// перехватчика запрос есть. Ответ несёт запрос перехватчика и называет сбой
// query_anchor_read_failed, а не выдаёт картину «в базовом методе запросов нет».
func TestEffectiveQueryAnchorReadFailureNamed(t *testing.T) {
	st := openFixtureStore(t)
	seedEffQueryFixture(t, st, true)
	var res Result
	err := st.Read(context.Background(), func(tx *store.ReadTx) error {
		r, berr := buildWithSeams(context.Background(), tx,
			readSeams{queries: failingAnchorQueries{tx: tx, failName: "данныеостатков"}},
			Request{Task: effQueryTask, ProjectID: "p", View: "effective"})
		res = r
		return berr
	})
	if err != nil {
		t.Fatalf("buildWithSeams: %v", err)
	}
	if !hasWarning(res, "query_anchor_read_failed") {
		t.Fatalf("сбой чтения запросов анкера обязан быть назван: %+v", res.Warnings)
	}
	if hasWarning(res, "no_query_in_symbol") || len(snippetsOf(res, "query_text", effExt)) != 1 {
		t.Fatalf("запрос перехватчика обязан остаться в ответе: warnings=%+v snippets=%+v", res.Warnings, res.Snippets)
	}
}

// TestEffectiveQueryBaseTextMarkedReplaced: под &ИзменениеИКонтроль
// исполняется текст расширения; базовый текст запроса в effective помечен
// как изменённый перехватчиком, в raw пометки нет.
func TestEffectiveQueryBaseTextMarkedReplaced(t *testing.T) {
	st := openFixtureStore(t)
	seedEffQueryFixture(t, st, true)
	raw := buildFor(t, st, Request{Task: effQueryTask, ProjectID: "p"})
	eff := buildFor(t, st, Request{Task: effQueryTask, ProjectID: "p", View: "effective"})
	rawBase, effBase := snippetsOf(raw, "query_text", "cfg"), snippetsOf(eff, "query_text", "cfg")
	if len(rawBase) != 1 || len(effBase) != 1 {
		t.Fatalf("ожидался один базовый query_text в обоих view: raw=%+v eff=%+v", rawBase, effBase)
	}
	if strings.Contains(rawBase[0].WhyIncluded, "изменён перехватчиком") {
		t.Fatalf("raw не несёт пометки расширения: %q", rawBase[0].WhyIncluded)
	}
	if !strings.Contains(effBase[0].WhyIncluded, "изменён перехватчиком") || !strings.Contains(effBase[0].WhyIncluded, effExt) {
		t.Fatalf("effective обязан пометить базовый текст как изменённый перехватчиком %s: %q", effExt, effBase[0].WhyIncluded)
	}
}
