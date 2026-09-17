package source

import (
	"context"
	"testing"
)

func newTestSource() *XMLSource { return NewXMLSource("testdata/dump") }

func TestConfigurationInfo(t *testing.T) {
	info, err := newTestSource().ConfigurationInfo(context.Background())
	if err != nil {
		t.Fatalf("ConfigurationInfo: %v", err)
	}

	if info.Name != "ДемоКонфигурация" {
		t.Errorf("Name = %q, want ДемоКонфигурация", info.Name)
	}
	if info.Synonym != "Демонстрационная конфигурация" {
		t.Errorf("Synonym = %q", info.Synonym)
	}
	if info.Vendor != "ООО Ромашка" {
		t.Errorf("Vendor = %q", info.Vendor)
	}
	if info.Version != "1.2.3" {
		t.Errorf("Version = %q", info.Version)
	}
	if info.IsExtension {
		t.Errorf("IsExtension = true, want false for a base configuration")
	}
	if got := info.ObjectCounts["Catalog"]; got != 2 {
		t.Errorf("Catalog count = %d, want 2", got)
	}
	if got := info.ObjectCounts["Document"]; got != 1 {
		t.Errorf("Document count = %d, want 1", got)
	}
	if got := info.ObjectCounts["CommonModule"]; got != 1 {
		t.Errorf("CommonModule count = %d, want 1", got)
	}
}

func TestMetadataTree(t *testing.T) {
	tree, err := newTestSource().MetadataTree(context.Background())
	if err != nil {
		t.Fatalf("MetadataTree: %v", err)
	}
	if tree.Configuration != "ДемоКонфигурация" {
		t.Errorf("Configuration = %q", tree.Configuration)
	}
	if tree.TotalObjects != 8 {
		t.Errorf("TotalObjects = %d, want 8", tree.TotalObjects)
	}

	catalogs := findGroup(tree.Groups, "Catalog")
	if catalogs == nil {
		t.Fatalf("no Catalog group; groups: %+v", tree.Groups)
	}
	if len(catalogs.Objects) != 2 || catalogs.Objects[0] != "Контрагенты" || catalogs.Objects[1] != "Товары" {
		t.Errorf("Catalog objects = %v, want [Контрагенты Товары] (sorted)", catalogs.Objects)
	}
}

func TestObjectStructure(t *testing.T) {
	obj, err := newTestSource().ObjectStructure(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("ObjectStructure: %v", err)
	}
	if obj.Type != "Catalog" || obj.Name != "Товары" {
		t.Errorf("Type/Name = %q/%q, want Catalog/Товары", obj.Type, obj.Name)
	}
	if len(obj.Attributes) != 2 {
		t.Fatalf("attributes = %d, want 2: %+v", len(obj.Attributes), obj.Attributes)
	}
	if obj.Attributes[0].Name != "Артикул" || len(obj.Attributes[0].Type) != 1 || obj.Attributes[0].Type[0] != "Строка" {
		t.Errorf("attribute[0] = %+v, want Артикул Строка", obj.Attributes[0])
	}
	if obj.Attributes[1].Type[0] != "СправочникСсылка.ЕдиницыИзмерения" {
		t.Errorf("attribute[1].Type = %v, want [СправочникСсылка.ЕдиницыИзмерения]", obj.Attributes[1].Type)
	}
	if obj.Attributes[0].Kind != "Attribute" {
		t.Errorf("attribute[0].Kind = %q, want Attribute", obj.Attributes[0].Kind)
	}
	if len(obj.TabularSections) != 1 || obj.TabularSections[0].Name != "Цены" {
		t.Fatalf("tabular sections = %+v, want one named Цены", obj.TabularSections)
	}
	if len(obj.TabularSections[0].Attributes) != 1 || obj.TabularSections[0].Attributes[0].Name != "Цена" {
		t.Errorf("tabular section attrs = %+v, want [Цена]", obj.TabularSections[0].Attributes)
	}
	if len(obj.Forms) != 2 || obj.Forms[0] != "ФормаЭлемента" || obj.Forms[1] != "ФормаСписка" {
		t.Errorf("forms = %v, want [ФормаЭлемента ФормаСписка] in source order", obj.Forms)
	}
}

func TestObjectStructureRequiresTypeAndName(t *testing.T) {
	if _, err := newTestSource().ObjectStructure(context.Background(), "", "Товары"); err == nil {
		t.Error("expected an error for an empty type")
	}
}

func TestFormStructure(t *testing.T) {
	form, err := newTestSource().FormStructure(context.Background(), "Catalog", "Товары", "ФормаЭлемента")
	if err != nil {
		t.Fatalf("FormStructure: %v", err)
	}
	if form.Name != "ФормаЭлемента" || form.Owner != "Catalog.Товары" {
		t.Errorf("Name/Owner = %q/%q", form.Name, form.Owner)
	}

	// Attributes, including the main one.
	if len(form.Attributes) != 2 {
		t.Fatalf("attributes = %d, want 2: %+v", len(form.Attributes), form.Attributes)
	}
	if form.Attributes[0].Name != "Объект" || !form.Attributes[0].Main {
		t.Errorf("attribute[0] = %+v, want Объект main=true", form.Attributes[0])
	}
	if len(form.Attributes[0].Type) != 1 || form.Attributes[0].Type[0] != "СправочникОбъект.Товары" {
		t.Errorf("attribute[0].Type = %v, want [СправочникОбъект.Товары]", form.Attributes[0].Type)
	}

	// Items flattened in layout order.
	wantItems := []struct{ name, kind string }{
		{"ГруппаШапка", "UsualGroup"},
		{"Артикул", "InputField"},
		{"КнопкаЗаполнить", "Button"},
		{"ТаблицаЦены", "Table"},
		{"ТаблицаЦеныЦена", "InputField"},
	}
	if len(form.Items) != len(wantItems) {
		t.Fatalf("items = %d, want %d: %+v", len(form.Items), len(wantItems), form.Items)
	}
	for i, w := range wantItems {
		if form.Items[i].Name != w.name || form.Items[i].Kind != w.kind {
			t.Errorf("item[%d] = %s/%s, want %s/%s", i, form.Items[i].Name, form.Items[i].Kind, w.name, w.kind)
		}
	}
	if form.Items[2].Command != "Form.Command.Заполнить" {
		t.Errorf("button command = %q", form.Items[2].Command)
	}

	// Commands.
	if len(form.Commands) != 1 || form.Commands[0].Name != "Заполнить" || form.Commands[0].Action != "Заполнить" {
		t.Errorf("commands = %+v, want [{Заполнить Заполнить}]", form.Commands)
	}

	// Handlers: form-level OnCreateAtServer + item-level Selection on ТаблицаЦены.
	if !hasHandler(form.Handlers, "Form", "OnCreateAtServer", "ПриСозданииНаСервере") {
		t.Errorf("missing form handler; got %+v", form.Handlers)
	}
	if !hasHandler(form.Handlers, "ТаблицаЦены", "Selection", "ТаблицаЦеныВыбор") {
		t.Errorf("missing item handler; got %+v", form.Handlers)
	}
}

func TestFormStructureCommonFormPathRequiresName(t *testing.T) {
	if _, err := newTestSource().FormStructure(context.Background(), "Catalog", "Товары", ""); err == nil {
		t.Error("expected an error when the form name is missing for a non-common form")
	}
}

func hasHandler(hs []FormHandler, src, event, handler string) bool {
	for _, h := range hs {
		if h.Source == src && h.Event == event && h.Handler == handler {
			return true
		}
	}
	return false
}

func TestSearchCodeSubstring(t *testing.T) {
	res, err := newTestSource().SearchCode(context.Background(), SearchParams{Query: "Экспорт"})
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("matches = %d, want 2; %+v", len(res.Matches), res.Matches)
	}
	want := "CommonModules/ДемоМодуль/Ext/Module.bsl"
	if res.Matches[0].File != want {
		t.Errorf("File = %q, want %q", res.Matches[0].File, want)
	}
}

func TestSearchCodeRegex(t *testing.T) {
	// \w in Go's RE2 is ASCII-only; BSL identifiers are Cyrillic, so a realistic
	// regex must use a Unicode class like \p{L}.
	res, err := newTestSource().SearchCode(context.Background(), SearchParams{Query: `Функция\s+\p{L}+`, Regex: true})
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(res.Matches))
	}
	if !contains(res.Matches[0].Text, "НайтиКонтрагента") {
		t.Errorf("match text = %q, want it to contain НайтиКонтрагента", res.Matches[0].Text)
	}
}

func TestSearchCodeMaxResults(t *testing.T) {
	res, err := newTestSource().SearchCode(context.Background(), SearchParams{Query: "Экспорт", MaxResults: 1})
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Errorf("matches = %d, want 1", len(res.Matches))
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
}

func findGroup(groups []MetadataGroup, typ string) *MetadataGroup {
	for i := range groups {
		if groups[i].Type == typ {
			return &groups[i]
		}
	}
	return nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
