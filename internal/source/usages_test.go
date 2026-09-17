package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataUsagesTabularAttribute(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "Товары", false)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	// Товары is the type of tabular-section attribute РеализацияТоваровУслуг.Товары.Номенклатура.
	found := false
	for _, u := range rep.AsType {
		if u.Object == "Документ.РеализацияТоваровУслуг" && u.Field == "Товары.Номенклатура" {
			found = true
			if u.Kind != "Реквизит ТЧ" {
				t.Errorf("kind = %q, want Реквизит ТЧ", u.Kind)
			}
		}
	}
	if !found {
		t.Errorf("expected Товары used in a tabular-section attribute; got %+v", rep.AsType)
	}
}

func TestMetadataUsagesReferenceResource(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "Валюты", false)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	// Валюты is the type of register resource ТоварыНаСкладах.Валюта.
	found := false
	for _, u := range rep.AsType {
		if u.Object == "РегистрНакопления.ТоварыНаСкладах" && u.Field == "Валюта" && u.Kind == "Ресурс" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Валюты used as a resource type; got %+v", rep.AsType)
	}
}

func TestMetadataUsagesNoRolesDir(t *testing.T) {
	dir := t.TempDir()
	cfg := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses"><Configuration><Properties><Name>X</Name></Properties><ChildObjects/></Configuration></MetaDataObject>`
	if err := os.WriteFile(filepath.Join(dir, "Configuration.xml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// No Roles directory: MetadataUsages must not error, just return no role usages.
	rep, err := NewXMLSource(dir).MetadataUsages(context.Background(), "Catalog", "Контрагенты", false)
	if err != nil {
		t.Fatalf("MetadataUsages without Roles dir: %v", err)
	}
	if len(rep.InRoles) != 0 {
		t.Errorf("InRoles = %+v, want empty", rep.InRoles)
	}
}

func TestMetadataUsagesAsType(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "Товары", false)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	if rep.Object != "Справочник.Товары" {
		t.Errorf("Object = %q", rep.Object)
	}

	// Товары is used as the type of dimension ТоварыНаСкладах.Товар.
	found := false
	for _, u := range rep.AsType {
		if u.Object == "РегистрНакопления.ТоварыНаСкладах" && u.Field == "Товар" {
			found = true
			if u.Kind != "Измерение" {
				t.Errorf("kind = %q, want Измерение", u.Kind)
			}
		}
	}
	if !found {
		t.Errorf("expected Товары used as type in ТоварыНаСкладах.Товар; got %+v", rep.AsType)
	}
}

func TestMetadataUsagesAttribute(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "ЕдиницыИзмерения", false)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	// Товары has an attribute ЕдиницаИзмерения of type CatalogRef.ЕдиницыИзмерения.
	found := false
	for _, u := range rep.AsType {
		if u.Object == "Справочник.Товары" && u.Field == "ЕдиницаИзмерения" && u.Kind == "Реквизит" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ЕдиницыИзмерения used in Товары.ЕдиницаИзмерения; got %+v", rep.AsType)
	}
}

func TestMetadataUsagesInRoles(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "Товары", false)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	if len(rep.InRoles) != 1 {
		t.Fatalf("InRoles = %+v, want 1 role", rep.InRoles)
	}
	ru := rep.InRoles[0]
	if ru.Role != "ЧтениеТоваров" {
		t.Errorf("role = %q", ru.Role)
	}
	// Only rights with value=true are collected (Read, View — not Insert).
	if !containsStr(ru.Rights, "Read") || !containsStr(ru.Rights, "View") {
		t.Errorf("granted rights = %v, want Read+View", ru.Rights)
	}
	if containsStr(ru.Rights, "Insert") {
		t.Errorf("Insert has value=false and must not be listed; got %v", ru.Rights)
	}
}

func TestMetadataUsagesTotal(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "Товары", false)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	if rep.Total != len(rep.AsType)+len(rep.InRoles) {
		t.Errorf("Total = %d, want %d", rep.Total, len(rep.AsType)+len(rep.InRoles))
	}
}

func TestMetadataUsagesRequiresArgs(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	if _, err := s.MetadataUsages(context.Background(), "Catalog", "", false); err == nil {
		t.Error("expected error for empty name")
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestUsagesInTemplates pins the usages the type and rights scans cannot see: a
// report keeps its query inside a data composition schema, and an exchange plan
// keeps its registration rules in a template. Changing an attribute breaks these
// first, and until now they were invisible.
func TestUsagesInTemplates(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "Товары", true)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	byKind := map[string]TemplateUsage{}
	for _, u := range rep.InTemplates {
		byKind[u.Kind] = u
	}
	schema, ok := byKind["схема компоновки данных"]
	if !ok {
		t.Fatalf("data composition schema not found: %+v", rep.InTemplates)
	}
	if schema.Owner != "Отчет.ПродажиПоТоварам" || schema.Hits == 0 {
		t.Errorf("schema usage = %+v", schema)
	}
	if _, ok := byKind["правила регистрации"]; !ok {
		t.Errorf("registration rules template not found: %+v", rep.InTemplates)
	}
	if rep.Total != len(rep.AsType)+len(rep.InRoles)+len(rep.InTemplates) {
		t.Errorf("total %d does not include templates", rep.Total)
	}
}

// TestUsagesTemplatesAreOptional pins that the expensive scan is asked for, and
// that skipping it is stated rather than passed off as "not used anywhere".
func TestUsagesTemplatesAreOptional(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.MetadataUsages(context.Background(), "Catalog", "Товары", false)
	if err != nil {
		t.Fatalf("MetadataUsages: %v", err)
	}
	if len(rep.InTemplates) != 0 {
		t.Errorf("templates must not be scanned unless asked: %+v", rep.InTemplates)
	}
	if rep.TemplatesNote == "" {
		t.Error("the report must say that templates were not looked at")
	}
}
