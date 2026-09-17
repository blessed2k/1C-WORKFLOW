package source

import (
	"context"
	"strings"
	"testing"
)

func moduleByKind(pack *ContextPack, kind string) *ModuleInterface {
	for i := range pack.Modules {
		if pack.Modules[i].Kind == kind {
			return &pack.Modules[i]
		}
	}
	return nil
}

func TestContextPack(t *testing.T) {
	s := NewXMLSource("testdata/pack")
	pack, err := s.ContextPack(context.Background(), "Catalog", "Товары", ContextPackOptions{})
	if err != nil {
		t.Fatalf("ContextPack: %v", err)
	}

	if pack.Object != "Справочник.Товары" {
		t.Errorf("Object = %q", pack.Object)
	}
	if pack.Structure == nil || pack.Structure.Name != "Товары" {
		t.Fatalf("structure missing or wrong: %+v", pack.Structure)
	}
	if pack.Usages == nil {
		t.Error("usages missing")
	}

	// Both object and manager modules must be present.
	if len(pack.Modules) != 2 {
		t.Fatalf("expected object + manager module, got %d: %+v", len(pack.Modules), pack.Modules)
	}

	obj := moduleByKind(pack, "ObjectModule")
	if obj == nil {
		t.Fatal("ObjectModule missing")
	}
	objExports := strings.Join(obj.Exports, "\n")
	if !strings.Contains(objExports, "Функция НайтиПоАртикулу(Артикул)") ||
		!strings.Contains(objExports, "Процедура ПересчитатьЦены(Процент)") {
		t.Errorf("object exports = %v", obj.Exports)
	}
	// Non-exported and commented-out methods must not leak.
	for _, unwanted := range []string{"ВспомогательныйМетод", "ЗакомментированныйМетод"} {
		if strings.Contains(objExports, unwanted) {
			t.Errorf("export list leaked %q", unwanted)
		}
	}

	mgr := moduleByKind(pack, "ManagerModule")
	if mgr == nil {
		t.Fatal("ManagerModule missing")
	}
	mgrExports := strings.Join(mgr.Exports, "\n")
	// Multi-line parameter list is collapsed to one line.
	if !strings.Contains(mgrExports, "Контрагент, Дата, Сумма") {
		t.Errorf("multi-line signature not collapsed; got %v", mgr.Exports)
	}
	// Async exported method is captured (Асинх modifier stripped from the header).
	if !strings.Contains(mgrExports, "ЗагрузитьДанные") {
		t.Errorf("async exported method missing; got %v", mgr.Exports)
	}
}

func TestContextPackObjectWithoutModule(t *testing.T) {
	// Валюты exists but has no Ext/ modules: ContextPack succeeds with no modules.
	s := NewXMLSource("testdata/pack")
	pack, err := s.ContextPack(context.Background(), "Catalog", "Валюты", ContextPackOptions{})
	if err != nil {
		t.Fatalf("ContextPack: %v", err)
	}
	if len(pack.Modules) != 0 {
		t.Errorf("expected no modules for Валюты, got %+v", pack.Modules)
	}
	if pack.Structure == nil {
		t.Error("structure missing")
	}
}

func TestContextPackMissingObject(t *testing.T) {
	// A non-existent object: ObjectStructure errors before the module loop.
	s := NewXMLSource("testdata/pack")
	if _, err := s.ContextPack(context.Background(), "Catalog", "НесуществующийОбъект", ContextPackOptions{}); err == nil {
		t.Error("expected error for a missing object")
	}
}

func TestContextPackOptionsOffByDefault(t *testing.T) {
	// The extra parts cost extra reads, so an unasked pack must not carry them.
	pack, err := NewXMLSource("testdata/dump").ContextPack(context.Background(), "Catalog", "Товары", ContextPackOptions{})
	if err != nil {
		t.Fatalf("ContextPack: %v", err)
	}
	if pack.QuerySchema != nil {
		t.Errorf("querySchema must be absent without the option: %+v", pack.QuerySchema)
	}
	if len(pack.Forms) != 0 {
		t.Errorf("forms must be absent without the option: %+v", pack.Forms)
	}
}

func TestContextPackWithQuerySchema(t *testing.T) {
	pack, err := NewXMLSource("testdata/dump").ContextPack(context.Background(), "Catalog", "Товары",
		ContextPackOptions{QuerySchema: true})
	if err != nil {
		t.Fatalf("ContextPack: %v", err)
	}
	if pack.QuerySchema == nil {
		t.Fatal("querySchema requested but missing")
	}
	if pack.QuerySchema.TableName != "Справочник.Товары" {
		t.Errorf("table = %q", pack.QuerySchema.TableName)
	}
	// The point of the schema is field names as the query language sees them.
	if len(pack.QuerySchema.StandardFields) == 0 || len(pack.QuerySchema.OwnFields) == 0 {
		t.Errorf("schema without fields: %+v", pack.QuerySchema)
	}
}

func TestContextPackWithForms(t *testing.T) {
	pack, err := NewXMLSource("testdata/dump").ContextPack(context.Background(), "Catalog", "Товары",
		ContextPackOptions{Forms: true})
	if err != nil {
		t.Fatalf("ContextPack: %v", err)
	}
	if len(pack.Forms) != len(pack.Structure.Forms) {
		t.Fatalf("forms = %d, want one per declared form (%d)", len(pack.Forms), len(pack.Structure.Forms))
	}

	byName := map[string]FormBrief{}
	for _, f := range pack.Forms {
		byName[f.Name] = f
	}

	item, ok := byName["ФормаЭлемента"]
	if !ok {
		t.Fatalf("ФормаЭлемента missing: %+v", pack.Forms)
	}
	if item.Missing {
		t.Error("ФормаЭлемента exists in the export and must not be marked missing")
	}
	if item.Attributes == 0 || item.Items == 0 {
		t.Errorf("counts not filled: %+v", item)
	}
	if item.HandlersTotal != len(item.Handlers) || len(item.Handlers) == 0 {
		t.Errorf("handlers = %v, total = %d", item.Handlers, item.HandlersTotal)
	}
	if !containsStr(item.Handlers, "ПриСозданииНаСервере") {
		t.Errorf("form event handler missing: %v", item.Handlers)
	}

	// A form declared in metadata but absent from the export is reported, not skipped.
	list, ok := byName["ФормаСписка"]
	if !ok {
		t.Fatalf("ФормаСписка missing from the summary: %+v", pack.Forms)
	}
	if !list.Missing {
		t.Errorf("ФормаСписка has no file in the export and must be marked missing: %+v", list)
	}
}
