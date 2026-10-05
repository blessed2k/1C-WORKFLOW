package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

const apiFixtureXMLNS = `xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.20"`

// общийМодульXML: объявление общего модуля с флагами контекста исполнения.
func общийМодульXML(name string, server, client, serverCall bool) string {
	flag := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject ` + apiFixtureXMLNS + `>
  <CommonModule uuid="00000000-0000-0000-0000-000000000001">
    <Properties>
      <Name>` + name + `</Name>
      <Global>false</Global>
      <ClientManagedApplication>` + flag(client) + `</ClientManagedApplication>
      <Server>` + flag(server) + `</Server>
      <ServerCall>` + flag(serverCall) + `</ServerCall>
    </Properties>
  </CommonModule>
</MetaDataObject>`
}

// подсистемаXML: объявление подсистемы с составом и дочерними подсистемами.
func подсистемаXML(name string, content, children []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject ` + apiFixtureXMLNS + `>
  <Subsystem uuid="00000000-0000-0000-0000-000000000002">
    <Properties>
      <Name>` + name + `</Name>
      <Synonym>
        <v8:item xmlns:v8="http://v8.1c.ru/8.1/data/core">
          <v8:lang>ru</v8:lang>
          <v8:content>Подсистема ` + name + `</v8:content>
        </v8:item>
      </Synonym>
      <Content>`)
	for _, c := range content {
		b.WriteString(`
        <xr:Item xsi:type="xr:MDObjectRef">` + c + `</xr:Item>`)
	}
	b.WriteString(`
      </Content>
    </Properties>
    <ChildObjects>`)
	for _, c := range children {
		b.WriteString(`
      <Subsystem>` + c + `</Subsystem>`)
	}
	b.WriteString(`
    </ChildObjects>
  </Subsystem>
</MetaDataObject>`)
	return b.String()
}

const apiFixtureСтроковыеУтилиты = `#Область ПрограммныйИнтерфейс

// Разбивает строку на части по разделителю.
//
// Параметры:
//  Значение - Строка - исходная строка.
//
Функция РазложитьСтрокуВМассив(Знач Значение, Разделитель = ",") Экспорт
	Возврат Новый Массив;
КонецФункции

#Область РаботаСЧислами

// Возвращает сумму прописью.
Функция СуммаПрописью(Сумма) Экспорт
	Возврат "";
КонецФункции

#КонецОбласти

#Область УстаревшиеПроцедурыИФункции

// Устарела. Следует использовать СтроковыеУтилиты.РазложитьСтрокуВМассив.
Функция РазбитьСтроку(Значение) Экспорт
	Возврат Неопределено;
КонецФункции

#КонецОбласти

#КонецОбласти

#Область СлужебныйПрограммныйИнтерфейс

// Разбивает строку для внутренних нужд подсистемы.
Функция РазбитьСтрокуСлужебная(Значение) Экспорт
	Возврат Неопределено;
КонецФункции

#КонецОбласти

#Область СлужебныеПроцедурыИФункции

// Разбивает строку внутри модуля.
Функция РазбитьВнутри(Значение)
	Возврат Неопределено;
КонецФункции

#КонецОбласти
`

// apiFixtureFiles: конфигурация с библиотекой (подсистема СтандартныеПодсистемы
// с вложенной подсистемой), прикладным общим модулем и модулем менеджера.
// Модуль обновления библиотеки назван как в БСП: по этому имени ищется версия.
func apiFixtureFiles() map[string]string {
	subsystem := workspace.DumpDeclarationPath("Subsystem", "СтандартныеПодсистемы")
	return map[string]string{
		subsystem: подсистемаXML("СтандартныеПодсистемы",
			[]string{"CommonModule.ОбновлениеИнформационнойБазыБСП"}, []string{"БазоваяФункциональность"}),
		workspace.DumpChildSubsystemPath(subsystem, "БазоваяФункциональность"): подсистемаXML("БазоваяФункциональность",
			[]string{"CommonModule.СтроковыеУтилиты", "CommonModule.СтроковыеУтилитыПереопределяемый", "Catalog.Товары"}, nil),

		declPath("CommonModule", "СтроковыеУтилиты"):                 общийМодульXML("СтроковыеУтилиты", true, true, false),
		commonModulePath("СтроковыеУтилиты"):                         apiFixtureСтроковыеУтилиты,
		declPath("CommonModule", "СтроковыеУтилитыПереопределяемый"): общийМодульXML("СтроковыеУтилитыПереопределяемый", true, false, false),
		commonModulePath("СтроковыеУтилитыПереопределяемый"): `#Область ПрограммныйИнтерфейс

// Позволяет переопределить разделитель, по которому разбивается строка.
Процедура ПриОпределенииРазделителяСтроки(Разделитель) Экспорт
КонецПроцедуры

#КонецОбласти
`,
		declPath("CommonModule", "ОбновлениеИнформационнойБазыБСП"): общийМодульXML("ОбновлениеИнформационнойБазыБСП", true, false, false),
		commonModulePath("ОбновлениеИнформационнойБазыБСП"): `#Область ПрограммныйИнтерфейс

// Заполняет основные сведения о библиотеке.
Процедура ПриДобавленииПодсистемы(Описание) Экспорт

	Описание.Имя    = "СтандартныеПодсистемы";
	Описание.Версия = "3.1.11.366";

КонецПроцедуры

#КонецОбласти
`,
		declPath("CommonModule", "ПродажиВызовСервера"): общийМодульXML("ПродажиВызовСервера", true, false, true),
		commonModulePath("ПродажиВызовСервера"): `#Область ПрограммныйИнтерфейс

// Разбивает строку заказа на несколько строк по количеству.
Процедура РазбитьСтрокуЗаказа(Заказ, Количество) Экспорт
КонецПроцедуры

#КонецОбласти
`,
		declPath("Catalog", "Товары"): `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject ` + apiFixtureXMLNS + `>
  <Catalog uuid="00000000-0000-0000-0000-000000000003">
    <Properties>
      <Name>Товары</Name>
    </Properties>
  </Catalog>
</MetaDataObject>`,
		workspace.DumpModulePath("Catalog", "Товары", workspace.ModuleManager): `#Область ПрограммныйИнтерфейс

// Возвращает цену товара на дату.
Функция ЦенаТовара(Товар, Дата = Неопределено) Экспорт
	Возврат 0;
КонецФункции

#КонецОбласти
`,
	}
}

func newAPIFixtureProject(t *testing.T, id domain.ProjectID) *Projects {
	t.Helper()
	p, _ := newFilesFixtureProject(t, id, apiFixtureFiles(), index.Config{})
	return p
}

// TestGetModuleStructureReportsRegionPaths: области модуля приходят из
// индекса путями от внешней к внутренней, в порядке появления в модуле.
func TestGetModuleStructureReportsRegionPaths(t *testing.T) {
	p := newAPIFixtureProject(t, "api-regions")
	svc := &SymbolService{projects: p}

	resp, err := svc.GetModuleStructure(context.Background(),
		GetModuleStructureInput{Module: commonModulePath("СтроковыеУтилиты"), Component: "cfg"})
	if err != nil {
		t.Fatalf("GetModuleStructure: %v", err)
	}
	want := []string{
		"ПрограммныйИнтерфейс",
		"ПрограммныйИнтерфейс/РаботаСЧислами",
		"ПрограммныйИнтерфейс/УстаревшиеПроцедурыИФункции",
		"СлужебныйПрограммныйИнтерфейс",
		"СлужебныеПроцедурыИФункции",
	}
	if got := resp.Items[0].Regions; !reflect.DeepEqual(got, want) {
		t.Fatalf("Regions = %q, want %q", got, want)
	}
	for _, w := range resp.Warnings {
		if w.Code == "regions_not_indexed" {
			t.Fatalf("предупреждение regions_not_indexed при заполненных областях: %+v", w)
		}
	}
}

func findAPIItem(t *testing.T, p *Projects, query string) (APISearchItem, Response[APISearchItem]) {
	t.Helper()
	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Query: query})
	if err != nil {
		t.Fatalf("FindAPI(%q): %v", query, err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("FindAPI(%q): Items = %d, want 1", query, len(resp.Items))
	}
	return resp.Items[0], resp
}

func apiCalls(items []APIMethodItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Call
	}
	return out
}

// TestFindAPITwoSections: метод библиотеки и прикладной метод приходят в
// разных секциях, каждый готовым выражением вызова, с сигнатурой, первой
// строкой описания и контекстом исполнения модуля; версия библиотеки названа.
func TestFindAPITwoSections(t *testing.T) {
	p := newAPIFixtureProject(t, "api-sections")
	item, _ := findAPIItem(t, p, "разбить строку по разделителю")

	if len(item.BSP) == 0 {
		t.Fatalf("секция bsp пуста, other = %q", apiCalls(item.Other))
	}
	got := item.BSP[0]
	want := APIMethodItem{
		Call: "СтроковыеУтилиты.РазложитьСтрокуВМассив", Kind: "function",
		Signature: `РазложитьСтрокуВМассив(Знач Значение, Разделитель = ",")`,
		Summary:   "Разбивает строку на части по разделителю.",
		Context:   "клиент-сервер",
		Module:    commonModulePath("СтроковыеУтилиты"), Component: "cfg",
		Line: got.Line, UID: got.UID,
	}
	if got != want {
		t.Fatalf("bsp[0] =\n%+v\nwant\n%+v", got, want)
	}
	if got.Line != 8 || got.UID == "" {
		t.Fatalf("bsp[0]: line = %d (want 8), uid = %q (want непустой)", got.Line, got.UID)
	}

	if calls := apiCalls(item.Other); !reflect.DeepEqual(calls, []string{"ПродажиВызовСервера.РазбитьСтрокуЗаказа"}) {
		t.Fatalf("other = %q, want только прикладной метод", calls)
	}
	if ctx := item.Other[0].Context; ctx != "вызов сервера" {
		t.Fatalf("other[0].Context = %q, want %q", ctx, "вызов сервера")
	}
	if item.BSPVersion != "3.1.11.366" {
		t.Fatalf("BSPVersion = %q, want 3.1.11.366", item.BSPVersion)
	}
}

// TestFindAPIDeprecatedLastAndOverridableExcluded: устаревший метод помечен и
// стоит в хвосте отданной секции, хотя по словам запроса совпал лучше всех.
// Модуль *Переопределяемый, служебный программный интерфейс и неэкспортные
// методы в выдачу не попадают.
func TestFindAPIDeprecatedLastAndOverridableExcluded(t *testing.T) {
	p := newAPIFixtureProject(t, "api-deprecated")
	item, _ := findAPIItem(t, p, "разбить строку")

	// СуммаПрописью совпала одним словом из двух, и только именем модуля
	// («Строковые»): она слабее обоих методов, покрывших запрос целиком, но
	// действующая, поэтому стоит перед устаревшим.
	want := []string{"СтроковыеУтилиты.РазложитьСтрокуВМассив", "СтроковыеУтилиты.СуммаПрописью", "СтроковыеУтилиты.РазбитьСтроку"}
	if calls := apiCalls(item.BSP); !reflect.DeepEqual(calls, want) {
		t.Fatalf("bsp = %q, want %q", calls, want)
	}
	if item.BSP[0].Deprecated || item.BSP[1].Deprecated {
		t.Fatalf("действующие методы помечены устаревшими: %+v", item.BSP[:2])
	}
	last := item.BSP[2]
	if !last.Deprecated {
		t.Fatalf("устаревший метод не помечен: %+v", last)
	}
	if !strings.Contains(last.Summary, "Следует использовать СтроковыеУтилиты.РазложитьСтрокуВМассив") {
		t.Fatalf("Summary устаревшего = %q, замена не названа", last.Summary)
	}
	if item.BSPMatched != 3 {
		t.Fatalf("BSPMatched = %d, want 3", item.BSPMatched)
	}
}

// TestFindAPIDeprecatedSurvivesLimit: устаревший метод, совпавший с запросом
// лучше остальных, не вытесняется срезом limit: он остаётся в ответе, в хвосте
// отданного, вместе с названной заменой.
func TestFindAPIDeprecatedSurvivesLimit(t *testing.T) {
	p := newAPIFixtureProject(t, "api-deprecated-limit")
	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Query: "разбить строку", Limit: 2})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	want := []string{"СтроковыеУтилиты.РазложитьСтрокуВМассив", "СтроковыеУтилиты.РазбитьСтроку"}
	if calls := apiCalls(resp.Items[0].BSP); !reflect.DeepEqual(calls, want) {
		t.Fatalf("bsp = %q, want %q", calls, want)
	}
}

// TestFindAPIGlobalModuleCall: метод глобального общего модуля назван без
// имени модуля: так его и зовут.
func TestFindAPIGlobalModuleCall(t *testing.T) {
	files := apiFixtureFiles()
	files[declPath("CommonModule", "ПечатьГлобальный")] = strings.Replace(
		общийМодульXML("ПечатьГлобальный", false, true, false), "<Global>false</Global>", "<Global>true</Global>", 1)
	files[commonModulePath("ПечатьГлобальный")] = `#Область ПрограммныйИнтерфейс

// Открывает форму печати этикеток.
Процедура ОткрытьПечатьЭтикеток() Экспорт
КонецПроцедуры

#КонецОбласти
`
	p, _ := newFilesFixtureProject(t, "api-global", files, index.Config{})
	item, _ := findAPIItem(t, p, "печать этикеток")
	if len(item.Other) != 1 || item.Other[0].Call != "ОткрытьПечатьЭтикеток" || item.Other[0].Context != "клиент" {
		t.Fatalf("other = %+v, want один метод ОткрытьПечатьЭтикеток на клиенте", item.Other)
	}
}

// TestFindAPIFullCoverageBeforeScore: метод, у которого нашлись все три слова
// запроса, стоит выше метода, у которого два слова совпали сразу в имени,
// модуле и описании и набрали больший вес места.
func TestFindAPIFullCoverageBeforeScore(t *testing.T) {
	files := apiFixtureFiles()
	files[declPath("CommonModule", "ЗаказыРазбиение")] = общийМодульXML("ЗаказыРазбиение", true, false, false)
	files[commonModulePath("ЗаказыРазбиение")] = `#Область ПрограммныйИнтерфейс

// Разбить заказ.
Процедура РазбитьЗаказ(Заказ) Экспорт
КонецПроцедуры

#КонецОбласти
`
	p, _ := newFilesFixtureProject(t, "api-rank", files, index.Config{})
	item, _ := findAPIItem(t, p, "разбить заказ по количеству")

	want := []string{"ПродажиВызовСервера.РазбитьСтрокуЗаказа", "ЗаказыРазбиение.РазбитьЗаказ"}
	if calls := apiCalls(item.Other); !reflect.DeepEqual(calls, want) {
		t.Fatalf("other = %q, want %q", calls, want)
	}
}

// TestFindAPIMatchesDerivedWordForm: запрос отглагольным существительным
// находит метод, названный глаголом: «разложение» это «Разложить».
func TestFindAPIMatchesDerivedWordForm(t *testing.T) {
	p := newAPIFixtureProject(t, "api-derived")
	item, _ := findAPIItem(t, p, "разложение")
	// Вторым идёт устаревший метод: слово нашлось в его описании, где названа
	// замена («Следует использовать ...РазложитьСтрокуВМассив»).
	want := []string{"СтроковыеУтилиты.РазложитьСтрокуВМассив", "СтроковыеУтилиты.РазбитьСтроку"}
	if calls := apiCalls(item.BSP); !reflect.DeepEqual(calls, want) {
		t.Fatalf("bsp = %q, want %q", calls, want)
	}
}

// TestFindAPIRareWordOutweighsCommonWords: метод, совпавший одним редким
// словом запроса, стоит выше методов, совпавших двумя словами, которые есть
// почти в каждом имени: редкое слово говорит о назначении больше.
func TestFindAPIRareWordOutweighsCommonWords(t *testing.T) {
	files := apiFixtureFiles()
	var tables strings.Builder
	tables.WriteString("#Область ПрограммныйИнтерфейс\n")
	for _, verb := range []string{"Заполнить", "Очистить", "Скопировать", "Свернуть", "Сгруппировать", "Отсортировать", "Выгрузить"} {
		tables.WriteString("\nПроцедура " + verb + "ТаблицуДанных(Таблица) Экспорт\nКонецПроцедуры\n")
	}
	tables.WriteString("\n#КонецОбласти\n")
	files[declPath("CommonModule", "ОбработкаКоллекций")] = общийМодульXML("ОбработкаКоллекций", true, false, false)
	files[commonModulePath("ОбработкаКоллекций")] = tables.String()
	files[declPath("CommonModule", "КолонкиСервер")] = общийМодульXML("КолонкиСервер", true, false, false)
	files[commonModulePath("КолонкиСервер")] = `#Область ПрограммныйИнтерфейс

// Строит индекс по колонкам.
Процедура ИндексацияКолонок(Колонки) Экспорт
КонецПроцедуры

#КонецОбласти
`
	p, _ := newFilesFixtureProject(t, "api-rare", files, index.Config{})
	item, _ := findAPIItem(t, p, "индексировать таблицу данных")

	if len(item.Other) != 8 {
		t.Fatalf("other: отдано %d методов, want 8 (семь табличных и один с индексацией): %q", len(item.Other), apiCalls(item.Other))
	}
	if got := item.Other[0].Call; got != "КолонкиСервер.ИндексацияКолонок" {
		t.Fatalf("other[0] = %q, want КолонкиСервер.ИндексацияКолонок; вся секция: %q", got, apiCalls(item.Other))
	}
}

// TestFindAPIManagerModuleCall: метод модуля менеджера назван выражением
// вызова через коллекцию менеджеров и исполняется на сервере. Справочник
// входит в состав подсистемы библиотеки, поэтому его метод стоит в секции bsp:
// библиотека это не только общие модули.
func TestFindAPIManagerModuleCall(t *testing.T) {
	p := newAPIFixtureProject(t, "api-manager")
	item, _ := findAPIItem(t, p, "цена товара на дату")

	if len(item.BSP) == 0 {
		t.Fatalf("секция bsp пуста, other = %q", apiCalls(item.Other))
	}
	got := item.BSP[0]
	if got.Call != "Справочники.Товары.ЦенаТовара" || got.Context != "сервер" {
		t.Fatalf("bsp[0]: call = %q, context = %q; want Справочники.Товары.ЦенаТовара на сервере", got.Call, got.Context)
	}
	if got.Signature != "ЦенаТовара(Товар, Дата = Неопределено)" {
		t.Fatalf("bsp[0].Signature = %q", got.Signature)
	}
	if len(item.Other) != 0 {
		t.Fatalf("other = %q, want пусто", apiCalls(item.Other))
	}
}

// TestFindAPIWithoutLibrarySubsystem: в конфигурации без подсистемы
// СтандартныеПодсистемы все методы приходят в other, и ответ говорит почему
// секция bsp пуста.
func TestFindAPIWithoutLibrarySubsystem(t *testing.T) {
	files := apiFixtureFiles()
	top := workspace.DumpDeclarationPath("Subsystem", "СтандартныеПодсистемы")
	delete(files, top)
	delete(files, workspace.DumpChildSubsystemPath(top, "БазоваяФункциональность"))
	p, _ := newFilesFixtureProject(t, "api-nolib", files, index.Config{})

	item, resp := findAPIItem(t, p, "разбить строку по разделителю")
	if len(item.BSP) != 0 {
		t.Fatalf("bsp = %q, want пусто", apiCalls(item.BSP))
	}
	if len(item.Other) == 0 || item.Other[0].Call != "СтроковыеУтилиты.РазложитьСтрокуВМассив" {
		t.Fatalf("other = %q, want первым СтроковыеУтилиты.РазложитьСтрокуВМассив", apiCalls(item.Other))
	}
	if !hasWarning(resp.Warnings, "bsp_library_not_found") {
		t.Fatalf("нет предупреждения bsp_library_not_found: %+v", resp.Warnings)
	}
}

// TestFindAPIWithLibraryHasNoLibraryWarning: при найденной библиотеке
// предупреждения о ней нет.
func TestFindAPIWithLibraryHasNoLibraryWarning(t *testing.T) {
	p := newAPIFixtureProject(t, "api-lib-ok")
	_, resp := findAPIItem(t, p, "разбить строку")
	if hasWarning(resp.Warnings, "bsp_library_not_found") || hasWarning(resp.Warnings, "api_regions_not_indexed") {
		t.Fatalf("лишние предупреждения: %+v", resp.Warnings)
	}
}

// TestFindAPIWithoutRegionMarkup: модули без области ПрограммныйИнтерфейс
// (или индекс, собранный до появления путей областей) дают пустой ответ с
// предупреждением, а не молчаливое «ничего не нашлось».
func TestFindAPIWithoutRegionMarkup(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "api-noregions")
	item, resp := findAPIItem(t, p, "помощь")
	if len(item.BSP)+len(item.Other) != 0 {
		t.Fatalf("методы вне области программного интерфейса попали в ответ: %q %q", apiCalls(item.BSP), apiCalls(item.Other))
	}
	if !hasWarning(resp.Warnings, "api_regions_not_indexed") {
		t.Fatalf("нет предупреждения api_regions_not_indexed: %+v", resp.Warnings)
	}
}

// TestFindAPIRejectsEmptyQuery: запрос, в котором нет ни одного значимого
// слова, отклонён как неверный аргумент. Пустой запрос сюда не относится: это
// режим карты библиотеки.
func TestFindAPIRejectsEmptyQuery(t *testing.T) {
	p := newAPIFixtureProject(t, "api-empty")
	for _, q := range []string{"в по на", "или для при"} {
		_, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Query: q})
		var appErr *Error
		if !errors.As(err, &appErr) || appErr.Code != CodeInvalidArgument {
			t.Fatalf("FindAPI(%q): err = %v, want %s", q, err, CodeInvalidArgument)
		}
	}
}

// TestFindAPILimit: limit режет каждую секцию отдельно, счётчик совпавших
// остаётся полным.
func TestFindAPILimit(t *testing.T) {
	p := newAPIFixtureProject(t, "api-limit")
	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Query: "разбить строку", Limit: 1})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	item := resp.Items[0]
	if len(item.BSP) != 1 || item.BSPMatched != 3 {
		t.Fatalf("bsp: отдано %d, совпало %d; want 1 и 3", len(item.BSP), item.BSPMatched)
	}
	if len(item.Other) != 1 {
		t.Fatalf("other: отдано %d, want 1", len(item.Other))
	}
}

// TestAPIWords: идентификатор делится по границам CamelCase, аббревиатура
// остаётся одним словом, «ё» сворачивается в «е».
func TestAPIWords(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"РазложитьСтрокуВМассив", []string{"разложить", "строку", "в", "массив"}},
		{"URLАдрес", []string{"url", "адрес"}},
		{"ПолучитьURL", []string{"получить", "url"}},
		{"Форма2Уровня", []string{"форма", "2", "уровня"}},
		{"Имя_Служебное", []string{"имя", "служебное"}},
		{"Учёт товаров, на складе.", []string{"учет", "товаров", "на", "складе"}},
		{"", nil},
	} {
		if got := apiWords(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("apiWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAPIQueryTerms: запрос сводится к словам без повторов, коротких и
// служебных слов; идентификатор в запросе делится так же, как имя метода.
func TestAPIQueryTerms(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"разбить строку по разделителю", []string{"разбить", "строку", "разделителю"}},
		{"цена цена на дату", []string{"цена", "дату"}},
		{"цена цену номенклатуры", []string{"цена", "номенклатуры"}},
		{"в по на", nil},
		{"организация или контрагент для печати", []string{"организация", "контрагент", "печати"}},
		{"ЗначениеРеквизитаОбъекта", []string{"значение", "реквизита", "объекта"}},
	} {
		if got := apiQueryTerms(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("apiQueryTerms(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAPISameWord: два слова считаются одним, когда у них общее начало, а
// расходятся они только русскими суффиксами и окончаниями. Пары выписаны по
// смыслу слов: родственные формы обязаны сойтись, чужие слова с общим началом
// обязаны разойтись.
func TestAPISameWord(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		// падежи и числа
		{"строку", "строка", true},
		{"файлы", "файлов", true},
		{"file", "files", true},
		{"http", "https", true},
		{"полям", "полей", true},
		{"цена", "цену", true},
		{"дату", "дата", true},
		{"организации", "организация", true},
		{"сумм", "сумма", true},
		{"url", "url", true},
		// глагол, отглагольное существительное, причастие
		{"удаление", "удалить", true},
		{"форматирование", "формат", true},
		{"индексировать", "индексацию", true},
		{"упорядочение", "упорядочивания", true},
		{"получить", "получаемых", true},
		{"записей", "записать", true},
		{"разложение", "разложить", true},
		{"план", "планирование", true},
		// существительные на -ка и беглая гласная
		{"загрузка", "загрузить", true},
		{"проверка", "проверить", true},
		{"обработка", "обработать", true},
		{"сортировка", "сортировать", true},
		{"настройка", "настроить", true},
		{"настроек", "настройки", true},
		{"ошибок", "ошибка", true},
		{"ссылок", "ссылка", true},
		{"остатки", "остаток", true},
		// прилагательные
		{"печать", "печатных", true},
		{"склад", "складской", true},
		{"файл", "файловый", true},
		// чужие слова с общим началом
		{"строку", "строитель", false},
		{"цена", "центр", false},
		{"прописью", "пропуск", false},
		{"записей", "запроса", false},
		{"документ", "документооборот", false},
		{"выборка", "выбрать", false},
		// общее начало в три буквы сводит только окончания
		{"вид", "видимость", false},
		{"цена", "ценник", false},
		{"тип", "типовой", false},
	} {
		if got := apiSameWord(c.a, c.b); got != c.want {
			t.Errorf("apiSameWord(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := apiSameWord(c.b, c.a); got != c.want {
			t.Errorf("apiSameWord(%q, %q) = %v, want %v (сравнение обязано быть симметричным)", c.b, c.a, got, c.want)
		}
	}
}

// TestAPIQueryTermsLimit: запрос длиннее apiMaxTerms слов усекается до первых
// apiMaxTerms: совпавшие слова метода хранятся битами одного числа.
func TestAPIQueryTermsLimit(t *testing.T) {
	var words []string
	for i := 0; i < apiMaxTerms+8; i++ {
		// Разные слова: общий префикс короче трёх букв, словоформами друг
		// друга они не считаются.
		words = append(words, string([]rune{'а' + rune(i%30), 'б' + rune(i/30), 'в', 'г', 'д'}))
	}
	got := apiQueryTerms(strings.Join(words, " "))
	if len(got) != apiMaxTerms || !reflect.DeepEqual(got, words[:apiMaxTerms]) {
		t.Fatalf("apiQueryTerms: %d слов, want первые %d из %d; получено %q", len(got), apiMaxTerms, len(words), got)
	}
}
