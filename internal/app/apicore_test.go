package app

import (
	"context"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// apiCoreFixture: библиотека из apiFixtureFiles и прикладные общие модули,
// которые её зовут. calls: что зовёт каждый прикладной модуль. extra: ещё
// файлы поверх.
func apiCoreFixture(t *testing.T, id string, calls []string, extra map[string]string) *Projects {
	t.Helper()
	files := apiFixtureFiles()
	for i, body := range calls {
		name := "Прикладной" + strconv.Itoa(i+1)
		files[declPath("CommonModule", name)] = общийМодульXML(name, true, false, false)
		files[commonModulePath(name)] = "Процедура Работа() Экспорт\n" + body + "КонецПроцедуры\n"
	}
	// Модуль самой библиотеки тоже зовёт её метод: в счёт это не идёт.
	files[commonModulePath("ОбновлениеИнформационнойБазыБСП")] = `#Область ПрограммныйИнтерфейс

Процедура ПриДобавленииПодсистемы(Описание) Экспорт
	Описание.Версия = "3.1.11.366";
	Сумма = СтроковыеУтилиты.СуммаПрописью(1);
КонецПроцедуры

#КонецОбласти
`
	for path, text := range extra {
		files[path] = text
	}
	p, _ := newFilesFixtureProject(t, domain.ProjectID("api-core-"+id), files, index.Config{})
	return p
}

const (
	callSplit      = "\tА = СтроковыеУтилиты.РазложитьСтрокуВМассив(\"а,б\");\n"
	callSum        = "\tБ = СтроковыеУтилиты.СуммаПрописью(1);\n"
	callDeprecated = "\tВ = СтроковыеУтилиты.РазбитьСтроку(\"а\");\n"
	callPrice      = "\tГ = Справочники.Товары.ЦенаТовара(Неопределено);\n"
)

func coreItem(t *testing.T, p *Projects) APICoreItem {
	t.Helper()
	resp, err := NewAPIService(p).CoreMethods(context.Background())
	if err != nil || len(resp.Items) != 1 {
		t.Fatalf("CoreMethods: %+v, %v", resp.Items, err)
	}
	return resp.Items[0]
}

// TestCoreMethods: ходовые методы библиотеки считаются по числу прикладных
// объектов, которые их зовут; вызовы из самой библиотеки не в счёт; устаревший
// метод и метод, которым пользуются меньше чем в трёх объектах, в список не
// идут; методы сгруппированы по модулям, самые ходовые первыми.
func TestCoreMethods(t *testing.T) {
	p := apiCoreFixture(t, "main", []string{
		callSplit + callSum + callSum + callDeprecated + callPrice,
		callSplit + callSum + callDeprecated + callPrice,
		callSplit + callSum + callDeprecated,
		callSplit,
	}, nil)
	item := coreItem(t, p)
	want := []APICoreModule{{Module: "СтроковыеУтилиты", Methods: []string{"РазложитьСтрокуВМассив", "СуммаПрописью"}}}
	if !reflect.DeepEqual(item.Modules, want) || item.Methods != 2 || item.Candidates != 3 || item.BSPVersion != "3.1.11.366" {
		t.Errorf("ходовые методы = %+v (методов %d, кандидатов %d, версия %q), want %+v",
			item.Modules, item.Methods, item.Candidates, item.BSPVersion, want)
	}
	// Вызовов методов-кандидатов из прикладного кода: 4 и 4 у двух ходовых и
	// 2 у ЦенаТовара, которой пользуются всего в двух объектах. Устаревший
	// метод кандидатом не является, и его вызовы в счёт не идут.
	if wantCoverage := 8.0 / 10; math.Abs(item.Coverage-wantCoverage) > 1e-9 {
		t.Errorf("покрытие = %v, want %v", item.Coverage, wantCoverage)
	}
}

// TestCoreMethodsCountObjects: модуль объекта и формы одного документа это
// один объект: метод, который зовут только из них, ходовым не становится.
// Переопределяемый модуль библиотеки считается прикладным: код в нём пишет
// разработчик конфигурации.
func TestCoreMethodsCountObjects(t *testing.T) {
	doc := map[string]string{
		declPath("Document", "Заказ"): `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject ` + apiFixtureXMLNS + `>
  <Document uuid="00000000-0000-0000-0000-000000000009">
    <Properties>
      <Name>Заказ</Name>
    </Properties>
  </Document>
</MetaDataObject>`,
		workspace.DumpModulePath("Document", "Заказ", workspace.ModuleObject):  "Процедура Раз()\n" + callSum + "КонецПроцедуры\n",
		workspace.DumpModulePath("Document", "Заказ", workspace.ModuleManager): "Процедура Два() Экспорт\n" + callSum + "КонецПроцедуры\n",
		workspace.DumpFormModulePath("Document", "Заказ", "ФормаДокумента"):    "Процедура Три()\n" + callSum + "КонецПроцедуры\n",
	}
	p := apiCoreFixture(t, "objects", []string{callSplit, callSplit}, doc)
	if item := coreItem(t, p); len(item.Modules) != 0 || item.Candidates != 2 {
		t.Errorf("три файла одного документа сочтены тремя объектами: %+v, кандидатов %d", item.Modules, item.Candidates)
	}

	// Третий объект с вызовом РазложитьСтрокуВМассив: переопределяемый модуль
	// библиотеки.
	doc[commonModulePath("СтроковыеУтилитыПереопределяемый")] = "#Область ПрограммныйИнтерфейс\n\nПроцедура ПриОпределенииРазделителяСтроки(Разделитель) Экспорт\n" +
		callSplit + "КонецПроцедуры\n\n#КонецОбласти\n"
	p = apiCoreFixture(t, "overridable", []string{callSplit, callSplit}, doc)
	want := []APICoreModule{{Module: "СтроковыеУтилиты", Methods: []string{"РазложитьСтрокуВМассив"}}}
	if item := coreItem(t, p); !reflect.DeepEqual(item.Modules, want) {
		t.Errorf("вызов из переопределяемого модуля не сочтён прикладным: %+v, want %+v", item.Modules, want)
	}
}

// TestCallerObject: вызывающий файл относится к объекту по каталогу коллекции
// и имени; библиотечный объект опознаётся вместе со своими формами.
func TestCallerObject(t *testing.T) {
	ix := &apiIndex{libraryObjects: map[string]bool{
		apiObjectKey("cfg", "CommonModule.ОбщегоНазначения"):                 true,
		apiObjectKey("cfg", "CommonModule.ОбщегоНазначенияПереопределяемый"): true,
		apiObjectKey("cfg", "Catalog.Пользователи"):                          true,
	}}
	for _, tc := range []struct {
		component, path string
		object          string
		library         bool
	}{
		{"cfg", "CommonModules/ОбщегоНазначения/Ext/Module.bsl", "CommonModules/ОбщегоНазначения", true},
		{"cfg", "CommonModules/ОбщегоНазначенияПереопределяемый/Ext/Module.bsl", "CommonModules/ОбщегоНазначенияПереопределяемый", false},
		{"cfg", "Catalogs/Пользователи/Forms/ФормаЭлемента/Ext/Form/Module.bsl", "Catalogs/Пользователи", true},
		{"cfg", "Catalogs/Пользователи/Ext/ManagerModule.bsl", "Catalogs/Пользователи", true},
		{"cfg", "Catalogs/Пользователи.xml", "Catalogs/Пользователи", true},
		{"cfg", "Documents/Заказ/Ext/ObjectModule.bsl", "Documents/Заказ", false},
		// Тот же объект в расширении: другой компонент, библиотекой не считается.
		{"ext", "Catalogs/Пользователи/Ext/ManagerModule.bsl", "Catalogs/Пользователи", false},
		// Модуль приложения и неизвестный каталог: прикладной объект сам по себе.
		{"cfg", "Ext/ManagedApplicationModule.bsl", "Ext/ManagedApplicationModule.bsl", false},
		{"cfg", "НеКоллекция/Имя/Ext/Module.bsl", "НеКоллекция/Имя", false},
	} {
		object, library := ix.callerObject(tc.component, tc.path)
		if want := tc.component + "\x00" + tc.object; object != want || library != tc.library {
			t.Errorf("callerObject(%s, %s) = %q, %v; want %q, %v", tc.component, tc.path, object, library, want, tc.library)
		}
	}
}

// TestCoreMethodsSkipHandlers: обработчик события библиотеки в список не
// идёт, сколько бы объектов его ни звало: это обвязка, а не помощник.
func TestCoreMethodsSkipHandlers(t *testing.T) {
	const callHandler = "\tОбновлениеИнформационнойБазыБСП.ПриДобавленииПодсистемы(Неопределено);\n"
	p := apiCoreFixture(t, "handlers", []string{callHandler + callSplit, callHandler + callSplit, callHandler + callSplit}, nil)
	want := []APICoreModule{{Module: "СтроковыеУтилиты", Methods: []string{"РазложитьСтрокуВМассив"}}}
	if item := coreItem(t, p); !reflect.DeepEqual(item.Modules, want) || item.Coverage != 1 {
		t.Errorf("ходовые методы = %+v, покрытие %v; want %+v без обработчика", item.Modules, item.Coverage, want)
	}
}

// TestCoreMethodsWithoutCallers: библиотеку никто из прикладного кода не
// зовёт, или библиотеки нет вовсе: список пуст, ошибки нет; об отсутствии
// библиотеки говорит предупреждение.
func TestCoreMethodsWithoutCallers(t *testing.T) {
	resp, err := NewAPIService(newAPIFixtureProject(t, "api-core-idle")).CoreMethods(context.Background())
	if err != nil || len(resp.Items) != 1 || len(resp.Items[0].Modules) != 0 || resp.Items[0].Coverage != 0 {
		t.Errorf("библиотеку не зовут: %+v, %v", resp.Items, err)
	}
	resp, err = NewAPIService(apiRankFixture(t, "core-nolib", [][3]string{{"ЗначениеРеквизита", "", ""}})).CoreMethods(context.Background())
	if err != nil || len(resp.Items[0].Modules) != 0 || !hasWarning(resp.Warnings, "bsp_library_not_found") {
		t.Errorf("библиотеки нет: %+v, предупреждения %+v, %v", resp.Items, resp.Warnings, err)
	}
}

// TestCoreMethodsCarriedAcrossGenerations: после смены поколения индекса
// список прошлого поколения переносится, пока состав программного интерфейса
// прежний, и считается заново, когда он изменился.
func TestCoreMethodsCarriedAcrossGenerations(t *testing.T) {
	core := &apiCore{methods: 7, apiMethods: 3}
	same := &apiIndex{methods: make([]apiIndexMethod, 3), coreStale: core}
	if got, err := same.core(nil); err != nil || got != core {
		t.Errorf("список прошлого поколения не перенесён: %+v, %v", got, err)
	}
	// Состав интерфейса изменился: перенос не годится, расчёт идёт заново (на
	// пустом индексе он обходится без чтения базы и даёт пустой список).
	changed := &apiIndex{coreStale: core}
	if got, err := changed.core(nil); err != nil || got == core || got.methods != 0 {
		t.Errorf("список перенесён при изменившемся составе интерфейса: %+v, %v", got, err)
	}
}

// TestFindAPIMapCarriesCore: find_api без аргументов отдаёт вместе с картой
// библиотеки её ходовые методы.
func TestFindAPIMapCarriesCore(t *testing.T) {
	p := apiCoreFixture(t, "map", []string{callSplit, callSplit, callSplit}, nil)
	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	item := resp.Items[0]
	want := []APICoreModule{{Module: "СтроковыеУтилиты", Methods: []string{"РазложитьСтрокуВМассив"}}}
	if !reflect.DeepEqual(item.Core, want) || item.CoreCoverage != 1 || len(item.Map) == 0 {
		t.Errorf("core = %+v, покрытие %v, карта %d подсистем; want %+v", item.Core, item.CoreCoverage, len(item.Map), want)
	}
}
