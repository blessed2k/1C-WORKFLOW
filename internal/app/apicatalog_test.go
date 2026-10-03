package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

func apiCatalogItem(t *testing.T, p *Projects) (APICatalogItem, Response[APICatalogItem]) {
	t.Helper()
	resp, err := NewAPIService(p).Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("Catalog: Items = %d, want 1", len(resp.Items))
	}
	return resp.Items[0], resp
}

// TestAPICatalogSections: каталог отдаёт все методы программного интерфейса
// двумя секциями, в порядке выражений вызова, тем же отбором, что find_api:
// без служебных областей, без неэкспортных, без переопределяемых модулей.
func TestAPICatalogSections(t *testing.T) {
	p := newAPIFixtureProject(t, "api-catalog")
	item, resp := apiCatalogItem(t, p)

	wantBSP := []string{
		"ОбновлениеИнформационнойБазыБСП.ПриДобавленииПодсистемы",
		"Справочники.Товары.ЦенаТовара",
		"СтроковыеУтилиты.РазбитьСтроку",
		"СтроковыеУтилиты.РазложитьСтрокуВМассив",
		"СтроковыеУтилиты.СуммаПрописью",
	}
	if got := apiCalls(item.BSP); !reflect.DeepEqual(got, wantBSP) {
		t.Errorf("BSP = %q, want %q", got, wantBSP)
	}
	wantOther := []string{"ПродажиВызовСервера.РазбитьСтрокуЗаказа"}
	if got := apiCalls(item.Other); !reflect.DeepEqual(got, wantOther) {
		t.Errorf("Other = %q, want %q", got, wantOther)
	}
	if item.BSPVersion != "3.1.11.366" {
		t.Errorf("BSPVersion = %q, want 3.1.11.366", item.BSPVersion)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("Warnings = %+v, want none", resp.Warnings)
	}
}

// TestAPICatalogMethodFields: метод каталога описан так же, как в ответе
// find_api: сигнатура со значениями по умолчанию, первая строка описания,
// контекст исполнения, пометка устаревшего, место объявления.
func TestAPICatalogMethodFields(t *testing.T) {
	p := newAPIFixtureProject(t, "api-catalog-fields")
	item, _ := apiCatalogItem(t, p)

	byCall := map[string]APIMethodItem{}
	for _, m := range item.BSP {
		byCall[m.Call] = m
	}
	m, ok := byCall["СтроковыеУтилиты.РазложитьСтрокуВМассив"]
	if !ok {
		t.Fatalf("в каталоге нет СтроковыеУтилиты.РазложитьСтрокуВМассив: %q", apiCalls(item.BSP))
	}
	if want := `РазложитьСтрокуВМассив(Знач Значение, Разделитель = ",")`; m.Signature != want {
		t.Errorf("Signature = %q, want %q", m.Signature, want)
	}
	if want := "Разбивает строку на части по разделителю."; m.Summary != want {
		t.Errorf("Summary = %q, want %q", m.Summary, want)
	}
	if m.Context != "клиент-сервер" || m.Kind != "function" || m.Deprecated {
		t.Errorf("Context = %q, Kind = %q, Deprecated = %v", m.Context, m.Kind, m.Deprecated)
	}
	if m.Module != commonModulePath("СтроковыеУтилиты") || m.Line == 0 || m.UID == "" {
		t.Errorf("Module = %q, Line = %d, UID = %q", m.Module, m.Line, m.UID)
	}
	if !byCall["СтроковыеУтилиты.РазбитьСтроку"].Deprecated {
		t.Errorf("устаревший метод СтроковыеУтилиты.РазбитьСтроку пришёл без пометки")
	}

	// Каталог и поиск называют один метод одинаково.
	found, _ := findAPIItem(t, p, "разбить строку по разделителю")
	if len(found.BSP) == 0 {
		t.Fatalf("find_api не нашёл метод, который есть в каталоге")
	}
	if got, want := found.BSP[0], byCall[found.BSP[0].Call]; !reflect.DeepEqual(got, want) {
		t.Errorf("метод в поиске и в каталоге описан по-разному:\n поиск   %+v\n каталог %+v", got, want)
	}
}

// TestAPICatalogWithoutLibrarySubsystem: без подсистемы библиотеки все методы
// в other, с тем же предупреждением, что у find_api.
func TestAPICatalogWithoutLibrarySubsystem(t *testing.T) {
	files := apiFixtureFiles()
	top := workspace.DumpDeclarationPath("Subsystem", "СтандартныеПодсистемы")
	delete(files, top)
	delete(files, workspace.DumpChildSubsystemPath(top, "БазоваяФункциональность"))
	p, _ := newFilesFixtureProject(t, "api-catalog-nolib", files, index.Config{})
	item, resp := apiCatalogItem(t, p)
	if len(item.BSP) != 0 {
		t.Errorf("BSP = %q, want пусто", apiCalls(item.BSP))
	}
	if len(item.Other) != 6 {
		t.Errorf("Other = %q, want 6 методов", apiCalls(item.Other))
	}
	if !hasWarning(resp.Warnings, "bsp_library_not_found") {
		t.Errorf("Warnings = %+v, want bsp_library_not_found", resp.Warnings)
	}
}
