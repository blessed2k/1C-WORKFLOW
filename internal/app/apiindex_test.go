package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
)

// apiDocFixtureModule: модуль, у методов которого слова запроса стоят в разных
// местах: в имени, в первой строке, в описании параметра.
const apiDocFixtureModule = `#Область ПрограммныйИнтерфейс

// Возвращает сведения о юридическом или физическом лице.
//
// Параметры:
//  ЮрФизЛицо - СправочникСсылка.Организации, СправочникСсылка.Контрагенты - лицо.
//  Период - Дата - дата, на которую выбираются сведения.
//
// Возвращаемое значение:
//  Структура - собранные сведения.
//
Функция СведенияОЮрФизЛице(ЮрФизЛицо, Период) Экспорт
	Возврат Неопределено;
КонецФункции

// Возвращает наименование организации.
Функция НаименованиеОрганизации(Организация) Экспорт
	Возврат "";
КонецФункции

// Заполняет шапку документа.
//
// Параметры:
//  Документ - ДокументОбъект - документ; контрагент берётся из его шапки.
//
Процедура ЗаполнитьШапку(Документ) Экспорт
КонецПроцедуры

// Возвращает значение реквизита объекта.
Функция ЗначениеРеквизитаОбъекта(Ссылка, ИмяРеквизита) Экспорт
	Возврат Неопределено;
КонецФункции

// Возвращает значение реквизита объекта по умолчанию для формы.
Функция ЗначениеРеквизитаОбъектаПоУмолчаниюДляФормы(Ссылка, ИмяРеквизита) Экспорт
	Возврат Неопределено;
КонецФункции

#КонецОбласти
`

func apiDocFixture(t *testing.T, id string) (*Projects, *openProject) {
	t.Helper()
	files := map[string]string{
		declPath("CommonModule", "ПечатьДокументов"): общийМодульXML("ПечатьДокументов", true, false, false),
		commonModulePath("ПечатьДокументов"):         apiDocFixtureModule,
	}
	return newFilesFixtureProject(t, domain.ProjectID("api-doc-"+id), files, index.Config{})
}

// TestFindAPISearchesFullComment: метод находится по слову, которое стоит
// только в описании параметра, а не в имени и не в первой строке.
func TestFindAPISearchesFullComment(t *testing.T) {
	p, _ := apiDocFixture(t, "full")
	item, _ := findAPIItem(t, p, "информация о контрагенте на дату")
	if len(item.Other) == 0 || item.Other[0].Call != "ПечатьДокументов.СведенияОЮрФизЛице" {
		t.Fatalf("other = %q, want первым ПечатьДокументов.СведенияОЮрФизЛице", apiCalls(item.Other))
	}
	// Первая строка описания в ответе прежняя: полный комментарий в summary не идёт.
	if got, want := item.Other[0].Summary, "Возвращает сведения о юридическом или физическом лице."; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

// TestFindAPINameOutranksComment: слово в имени метода весит больше того же
// слова в описании параметра.
func TestFindAPINameOutranksComment(t *testing.T) {
	p, _ := apiDocFixture(t, "name")
	item, _ := findAPIItem(t, p, "организация")
	want := []string{"ПечатьДокументов.НаименованиеОрганизации", "ПечатьДокументов.СведенияОЮрФизЛице"}
	if got := apiCalls(item.Other); !reflect.DeepEqual(got, want) {
		t.Errorf("other = %q, want %q", got, want)
	}
}

// TestFindAPIExactNameBeforeLongerName: при равном охвате выше метод, имя
// которого запрос покрывает целиком.
func TestFindAPIExactNameBeforeLongerName(t *testing.T) {
	p, _ := apiDocFixture(t, "fit")
	item, _ := findAPIItem(t, p, "значение реквизита объекта")
	want := []string{"ПечатьДокументов.ЗначениеРеквизитаОбъекта", "ПечатьДокументов.ЗначениеРеквизитаОбъектаПоУмолчаниюДляФормы"}
	if got := apiCalls(item.Other); len(got) < 2 || !reflect.DeepEqual(got[:2], want) {
		t.Errorf("other = %q, want первыми %q", got, want)
	}
}

// TestFindAPIMoreList: методы за пределом limit приходят коротким списком:
// вызов, первая строка описания и uid, по которому get_symbol отдаёт остальное.
func TestFindAPIMoreList(t *testing.T) {
	p, _ := apiDocFixture(t, "more")
	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Query: "возвращает", Limit: 1})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	item := resp.Items[0]
	if len(item.Other) != 1 || item.OtherMatched != 4 {
		t.Fatalf("other = %q, совпало %d; want один метод из четырёх", apiCalls(item.Other), item.OtherMatched)
	}
	if len(item.OtherMore) != 3 {
		t.Fatalf("otherMore = %+v, want три метода", item.OtherMore)
	}
	seen := map[string]bool{item.Other[0].Call: true}
	for _, m := range item.OtherMore {
		if seen[m.Call] {
			t.Errorf("метод %s повторён в коротком списке", m.Call)
		}
		seen[m.Call] = true
		if m.UID == "" || m.Summary == "" {
			t.Errorf("короткая запись без uid или описания: %+v", m)
		}
	}
	if len(item.BSPMore) != 0 {
		t.Errorf("bspMore = %+v, want пусто: библиотеки в конфигурации нет", item.BSPMore)
	}
}

// TestFindAPIIndexFollowsGeneration: индекс слов строится один раз на
// поколение и перестраивается после переиндексации: новый метод находится, а
// сервис тот же.
func TestFindAPIIndexFollowsGeneration(t *testing.T) {
	p, op := apiDocFixture(t, "gen")
	svc := NewAPIService(p)
	ctx := context.Background()
	find := func(query string) []string {
		t.Helper()
		resp, err := svc.FindAPI(ctx, FindAPIInput{Query: query})
		if err != nil {
			t.Fatalf("FindAPI(%q): %v", query, err)
		}
		return apiCalls(resp.Items[0].Other)
	}
	if got := find("рассчитать скидку"); len(got) != 0 {
		t.Fatalf("до правки найдено %q, want пусто", got)
	}
	first := svc.indexes.indexes[op.Entry.ID]
	if find("наименование организации"); svc.indexes.indexes[op.Entry.ID] != first {
		t.Errorf("индекс слов перестроен при том же поколении")
	}

	module := filepath.Join(op.Entry.Root, "cfg", filepath.FromSlash(commonModulePath("ПечатьДокументов")))
	added := apiDocFixtureModule + `
#Область ПрограммныйИнтерфейс

// Рассчитывает скидку по строке.
Функция РассчитатьСкидку(Строка) Экспорт
	Возврат 0;
КонецФункции

#КонецОбласти
`
	if err := os.WriteFile(module, []byte(added), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if got := find("рассчитать скидку"); len(got) != 1 || got[0] != "ПечатьДокументов.РассчитатьСкидку" {
		t.Errorf("после правки найдено %q, want ПечатьДокументов.РассчитатьСкидку", got)
	}
	if svc.indexes.indexes[op.Entry.ID] == first {
		t.Errorf("индекс слов не перестроен после смены поколения")
	}
}
