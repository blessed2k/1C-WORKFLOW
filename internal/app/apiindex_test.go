package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestFindAPILibraryMap: без запроса и без модуля find_api отдаёт карту
// библиотеки: подсистемы и их модули с числом действующих методов
// программного интерфейса. Переопределяемый модуль и модуль без интерфейса
// на карту не попадают.
func TestFindAPILibraryMap(t *testing.T) {
	p := newAPIFixtureProject(t, "api-map")
	for _, q := range []string{"", "   "} {
		resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Query: q})
		if err != nil {
			t.Fatalf("FindAPI(%q): %v", q, err)
		}
		item := resp.Items[0]
		want := []APIMapSubsystem{
			{Name: "БазоваяФункциональность", Title: "Подсистема БазоваяФункциональность", Modules: []APIMapModule{
				{Name: "Справочники.Товары", Methods: 1},
				{Name: "СтроковыеУтилиты", Methods: 2},
			}},
			{Name: "СтандартныеПодсистемы", Title: "Подсистема СтандартныеПодсистемы",
				Modules: []APIMapModule{{Name: "ОбновлениеИнформационнойБазыБСП", Methods: 1}}},
		}
		if !reflect.DeepEqual(item.Map, want) {
			t.Errorf("карта = %+v, want %+v", item.Map, want)
		}
		// Секции в режиме карты пусты, но есть: в JSON это [], а не null.
		if item.BSP == nil || item.Other == nil {
			t.Errorf("секции в режиме карты: bsp %v, other %v, want пустые срезы", item.BSP, item.Other)
		}
		if len(item.BSP) != 0 || len(item.Other) != 0 || item.BSPVersion != "3.1.11.366" {
			t.Errorf("в режиме карты: bsp %d, other %d, версия %q", len(item.BSP), len(item.Other), item.BSPVersion)
		}
	}
}

// TestFindAPIModuleListing: с модулем и без запроса find_api отдаёт весь
// программный интерфейс модуля в порядке объявления, устаревший метод с
// пометкой; имя модуля не зависит от регистра, менеджер объекта называется
// как в коде.
func TestFindAPIModuleListing(t *testing.T) {
	p := newAPIFixtureProject(t, "api-module")
	svc := NewAPIService(p)
	resp, err := svc.FindAPI(context.Background(), FindAPIInput{Module: "строковыеутилиты"})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	item := resp.Items[0]
	var calls []string
	for _, m := range item.Methods {
		calls = append(calls, m.Call)
	}
	want := []string{"СтроковыеУтилиты.РазложитьСтрокуВМассив", "СтроковыеУтилиты.СуммаПрописью", "СтроковыеУтилиты.РазбитьСтроку"}
	if !reflect.DeepEqual(calls, want) || item.Module != "СтроковыеУтилиты" || item.MethodsTotal != 3 {
		t.Errorf("методы = %q, модуль %q, всего %d; want %q", calls, item.Module, item.MethodsTotal, want)
	}
	if !item.Methods[2].Deprecated || item.Methods[0].Summary == "" || item.Methods[0].UID == "" {
		t.Errorf("записи списка: %+v", item.Methods)
	}

	resp, err = svc.FindAPI(context.Background(), FindAPIInput{Module: "Справочники.Товары"})
	if err != nil || len(resp.Items[0].Methods) != 1 || resp.Items[0].Methods[0].Call != "Справочники.Товары.ЦенаТовара" {
		t.Errorf("менеджер объекта: %+v, ошибка %v", resp.Items, err)
	}

	_, err = svc.FindAPI(context.Background(), FindAPIInput{Module: "Строковые"})
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != CodeNotFound || !strings.Contains(appErr.Hint, "СтроковыеУтилиты") {
		t.Errorf("неизвестный модуль: err = %v, want not_found с подсказкой СтроковыеУтилиты", err)
	}
}

// TestFindAPISearchInsideModule: с модулем и запросом поиск идёт только среди
// методов этого модуля.
func TestFindAPISearchInsideModule(t *testing.T) {
	p := newAPIFixtureProject(t, "api-module-query")
	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Query: "разбить строку", Module: "ПродажиВызовСервера"})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	item := resp.Items[0]
	if len(item.BSP) != 0 || len(item.Other) != 1 || item.Other[0].Call != "ПродажиВызовСервера.РазбитьСтрокуЗаказа" {
		t.Errorf("bsp = %q, other = %q; want только ПродажиВызовСервера.РазбитьСтрокуЗаказа", apiCalls(item.BSP), apiCalls(item.Other))
	}
}

// apiRankFixture собирает проект с одним общим модулем из заданных методов:
// имя, первая строка описания, остальной комментарий.
func apiRankFixture(t *testing.T, id string, methods [][3]string) *Projects {
	t.Helper()
	var b strings.Builder
	b.WriteString("#Область ПрограммныйИнтерфейс\n\n")
	for _, m := range methods {
		if m[1] != "" {
			b.WriteString("// " + m[1] + "\n")
		}
		if m[2] != "" {
			b.WriteString("//\n// Параметры:\n//  " + m[2] + "\n")
		}
		b.WriteString("Процедура " + m[0] + "() Экспорт\nКонецПроцедуры\n\n")
	}
	b.WriteString("#КонецОбласти\n")
	files := map[string]string{
		declPath("CommonModule", "Прочее"): общийМодульXML("Прочее", true, false, false),
		commonModulePath("Прочее"):         b.String(),
	}
	p, _ := newFilesFixtureProject(t, domain.ProjectID("api-rank-"+id), files, index.Config{})
	return p
}

// TestFindAPINameFitDecides: при равном охвате и равном весе мест выше метод,
// имя которого запрос покрывает целиком, хотя по алфавиту он стоит позже.
func TestFindAPINameFitDecides(t *testing.T) {
	p := apiRankFixture(t, "fit", [][3]string{
		{"АЗначениеРеквизитаОбъектаФормы", "", ""},
		{"ЯЗначениеРеквизита", "", ""},
	})
	item, _ := findAPIItem(t, p, "значение реквизита")
	// Слово «А»/«Я» короче трёх букв и в счёт слов имени не идёт.
	want := []string{"Прочее.ЯЗначениеРеквизита", "Прочее.АЗначениеРеквизитаОбъектаФормы"}
	if got := apiCalls(item.Other); !reflect.DeepEqual(got, want) {
		t.Errorf("other = %q, want %q", got, want)
	}
}

// TestFindAPIFieldCoverDecides: редкое слово в имени метода весит больше, чем
// то же слово вместе с другим словом запроса в описании параметров. При
// равном весе полей порядок был бы обратным: у второго метода найдено два
// слова, у первого одно.
func TestFindAPIFieldCoverDecides(t *testing.T) {
	p := apiRankFixture(t, "cover", [][3]string{
		{"Прочее", "Служебный метод.", "Таблица - ТаблицаЗначений - куда выводится факсимиле."},
		{"ВывестиФаксимиле", "Выводит подпись в документ.", ""},
		{"Первый", "Ничего не делает.", ""},
		{"Второй", "Ничего не делает.", ""},
		{"Третий", "Ничего не делает.", ""},
	})
	item, _ := findAPIItem(t, p, "факсимиле таблица")
	want := []string{"Прочее.ВывестиФаксимиле", "Прочее.Прочее"}
	if got := apiCalls(item.Other); !reflect.DeepEqual(got, want) {
		t.Errorf("other = %q, want %q", got, want)
	}
}

// TestFindAPIWordRarityPerField: слово, которое стоит в комментарии почти
// каждого метода, не обесценивается в имени метода. По одной общей редкости
// слово «строку» в имени весило бы меньше редкого слова «разделитель» в имени
// соседнего метода, и нужный метод стоял бы вторым.
func TestFindAPIWordRarityPerField(t *testing.T) {
	methods := [][3]string{
		{"ЭтоРазделитель", "Проверяет символ.", "Символ - Строка - проверяемый символ."},
		{"РазложитьСтроку", "Делит текст по разделителю.", ""},
	}
	for i := 0; i < 18; i++ {
		methods = append(methods, [3]string{"Служебный" + string(rune('А'+i)), "Ничего не делает.", "Значение - Строка - произвольная строка."})
	}
	p := apiRankFixture(t, "rarity", methods)
	item, _ := findAPIItem(t, p, "строку разделителю")
	if got := apiCalls(item.Other); len(got) < 2 || got[0] != "Прочее.РазложитьСтроку" || got[1] != "Прочее.ЭтоРазделитель" {
		t.Errorf("other = %q, want первыми Прочее.РазложитьСтроку, Прочее.ЭтоРазделитель", got)
	}
}

// TestFindAPIMoreListIsCapped: короткий список не длиннее apiMoreCount, а
// счётчик совпавших остаётся полным.
func TestFindAPIMoreListIsCapped(t *testing.T) {
	var methods [][3]string
	for i := 0; i < 30; i++ {
		methods = append(methods, [3]string{"Метод" + string(rune('А'+i)), "Возвращает значение.", ""})
	}
	p := apiRankFixture(t, "cap", methods)
	item, _ := findAPIItem(t, p, "возвращает значение")
	if len(item.Other) != defaultAPILimit || len(item.OtherMore) != apiMoreCount || item.OtherMatched != 30 {
		t.Errorf("полных %d, коротких %d, совпало %d; want %d, %d, 30", len(item.Other), len(item.OtherMore), item.OtherMatched, defaultAPILimit, apiMoreCount)
	}
}

// TestFindAPIConcurrent: параллельные вызовы на свежем сервисе строят индекс
// слов один раз и отвечают одинаково (тест для запуска с -race).
func TestFindAPIConcurrent(t *testing.T) {
	p, op := apiDocFixture(t, "parallel")
	svc := NewAPIService(p)
	const workers = 8
	results := make([][]string, workers)
	errs := make([]error, workers)
	done := make(chan int, workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer func() { done <- w }()
			resp, err := svc.FindAPI(context.Background(), FindAPIInput{Query: "наименование организации"})
			if err != nil {
				errs[w] = err
				return
			}
			results[w] = apiCalls(resp.Items[0].Other)
		}(w)
	}
	for w := 0; w < workers; w++ {
		<-done
	}
	for w := 0; w < workers; w++ {
		if errs[w] != nil {
			t.Fatalf("вызов %d: %v", w, errs[w])
		}
		if !reflect.DeepEqual(results[w], results[0]) {
			t.Errorf("вызов %d ответил %q, вызов 0 ответил %q", w, results[w], results[0])
		}
	}
	if len(svc.indexes.indexes) != 1 || svc.indexes.indexes[op.Entry.ID] == nil {
		t.Errorf("индексов слов %d, want один на проект", len(svc.indexes.indexes))
	}
}

// TestGetSymbolReturnsFullComment: get_symbol отдаёт полный комментарий
// метода: по нему агент читает параметры метода, найденного в коротком списке
// find_api.
func TestGetSymbolReturnsFullComment(t *testing.T) {
	p, _ := apiDocFixture(t, "symdoc")
	item, _ := findAPIItem(t, p, "сведения о юридическом или физическом лице")
	if len(item.Other) == 0 || item.Other[0].Call != "ПечатьДокументов.СведенияОЮрФизЛице" {
		t.Fatalf("other = %q", apiCalls(item.Other))
	}
	resp, err := (&SymbolService{projects: p}).GetSymbol(context.Background(), GetSymbolInput{UID: item.Other[0].UID})
	if err != nil || len(resp.Items) != 1 {
		t.Fatalf("GetSymbol: %+v, %v", resp.Items, err)
	}
	doc := resp.Items[0].Doc
	if !strings.Contains(doc, "Возвращает сведения о юридическом или физическом лице.") || !strings.Contains(doc, "Параметры:") {
		t.Errorf("doc = %q, want первая строка и раздел параметров", doc)
	}
}

// TestFindAPIModuleNotations: модуль принимается и в нотации метаданных, как
// его пишут остальные инструменты; на неизвестное имя ошибка называет
// похожие модули.
func TestFindAPIModuleNotations(t *testing.T) {
	p := newAPIFixtureProject(t, "api-module-names")
	svc := NewAPIService(p)
	for module, want := range map[string]string{
		"СтроковыеУтилиты":              "СтроковыеУтилиты",
		"CommonModule.СтроковыеУтилиты": "СтроковыеУтилиты",
		"ОбщийМодуль.СтроковыеУтилиты":  "СтроковыеУтилиты",
		"Справочники.Товары":            "Справочники.Товары",
		"Справочник.Товары":             "Справочники.Товары",
		"Catalog.Товары":                "Справочники.Товары",
	} {
		resp, err := svc.FindAPI(context.Background(), FindAPIInput{Module: module})
		if err != nil || resp.Items[0].Module != want || len(resp.Items[0].Methods) == 0 {
			t.Errorf("module %q: %+v, ошибка %v; want модуль %q", module, resp.Items, err, want)
		}
	}
	// Опечатка в имени: подсказка называет настоящий модуль.
	_, err := svc.FindAPI(context.Background(), FindAPIInput{Module: "СтроковыеУтилит"})
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != CodeNotFound || !strings.Contains(appErr.Hint, "СтроковыеУтилиты") {
		t.Errorf("опечатка в имени модуля: %v, want not_found с подсказкой СтроковыеУтилиты", err)
	}
}

// TestFindAPIMapWithoutLibrary: в конфигурации без библиотеки карты нет, и
// ответ говорит, что делать вместо неё.
func TestFindAPIMapWithoutLibrary(t *testing.T) {
	p := apiRankFixture(t, "nolib", [][3]string{{"ЗначениеРеквизита", "", ""}})
	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	item := resp.Items[0]
	if len(item.Map) != 0 || item.Note != apiNoLibraryMapNote || !hasWarning(resp.Warnings, "bsp_library_not_found") {
		t.Errorf("карта без библиотеки: map %+v, note %q, предупреждения %+v", item.Map, item.Note, resp.Warnings)
	}
}
