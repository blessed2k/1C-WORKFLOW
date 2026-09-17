package source

import (
	"context"
	"strings"
	"testing"
)

func TestExtensionContext(t *testing.T) {
	s := NewXMLSource("testdata/ext")
	ec, err := s.ExtensionContext(context.Background(), "testdata/base")
	if err != nil {
		t.Fatalf("ExtensionContext: %v", err)
	}

	if ec.Extension != "ТестовоеРасширение" || ec.Prefix != "тст_" || ec.Purpose != "Customization" {
		t.Errorf("header = %q/%q/%q", ec.Extension, ec.Prefix, ec.Purpose)
	}
	if len(ec.Adopted) != 1 || ec.Adopted[0] != "Документ.ЗаказКлиента" {
		t.Errorf("adopted = %v", ec.Adopted)
	}
	// Own objects are labelled like the adopted ones (Документ.ЗаказКлиента),
	// not half in English: metadataLabel now knows the types that never appear
	// in a query, common modules among them.
	if len(ec.Own) != 1 || ec.Own[0] != "ОбщийМодуль.тст_Сервис" {
		t.Errorf("own = %v", ec.Own)
	}

	if len(ec.Interceptors) != 3 {
		t.Fatalf("interceptors = %d: %+v", len(ec.Interceptors), ec.Interceptors)
	}
	byTarget := map[string]Interceptor{}
	for _, ic := range ec.Interceptors {
		byTarget[ic.Target] = ic
	}

	after := byTarget["ПриСозданииНаСервере"]
	if after.Kind != "После" || after.Method != "тст_ПриСозданииНаСервереПосле" {
		t.Errorf("after interceptor = %+v", after)
	}
	// Original resolved from the base dump, including its directive line.
	if !strings.Contains(after.Original, "Процедура ПриСозданииНаСервере(Отказ, СтандартнаяОбработка)") ||
		!strings.Contains(after.Original, "&НаСервере") ||
		!strings.Contains(after.Original, "ЗаполнитьЗначенияПоУмолчанию();") ||
		!strings.Contains(after.Original, "КонецПроцедуры") {
		t.Errorf("original not extracted:\n%s", after.Original)
	}
	// The original must stop at its own end, not swallow the next method.
	if strings.Contains(after.Original, "Процедура ЗаполнитьЗначенияПоУмолчанию()") {
		t.Errorf("original swallowed the next method:\n%s", after.Original)
	}

	// Annotation separated from the header by a comment line must still parse.
	before := byTarget["ПриЗаписи"]
	if before.Kind != "Перед" || before.Method != "тст_ПриЗаписиПеред" {
		t.Errorf("before interceptor = %+v", before)
	}

	instead := byTarget["ОбработкаПроведения"]
	if instead.Kind != "Вместо" || instead.Method != "тст_ОбработкаПроведения" {
		t.Errorf("instead interceptor = %+v", instead)
	}
	// ОбработкаПроведения is not in the base form module -> no original.
	if instead.Original != "" {
		t.Errorf("unexpected original for missing base method: %q", instead.Original)
	}
}

func TestExtensionContextNoBase(t *testing.T) {
	s := NewXMLSource("testdata/ext")
	ec, err := s.ExtensionContext(context.Background(), "")
	if err != nil {
		t.Fatalf("ExtensionContext: %v", err)
	}
	for _, ic := range ec.Interceptors {
		if ic.Original != "" {
			t.Errorf("original must be empty without baseDump; got %q", ic.Original)
		}
	}
}

func TestExtensionContextNotAnExtension(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	if _, err := s.ExtensionContext(context.Background(), ""); err == nil {
		t.Error("expected error for a non-extension dump")
	}
}
