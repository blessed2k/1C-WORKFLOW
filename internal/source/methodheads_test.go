package source

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The declarations below break a header regular expression: the annotation or
// the directive sits on the same line as the keyword. The parser reads them as
// any other declaration, and a commented-out one stays out.

func TestParseInterceptorsFromParser(t *testing.T) {
	module := `&НаСервере
&Перед("ПередЗаписью") Процедура Расш_ПередЗаписью(Отказ)
КонецПроцедуры

&After("OnWrite")
// the handler of the base configuration writes the log
Procedure Ext_OnWrite(Cancel)
EndProcedure

// &Вместо("Закомментирован")
// Процедура Расш_Закомментирован()
`
	want := []Interceptor{
		{Kind: "Перед", Target: "ПередЗаписью", Method: "Расш_ПередЗаписью"},
		{Kind: "После", Target: "OnWrite", Method: "Ext_OnWrite"},
	}
	if got := parseInterceptors([]byte(module)); !reflect.DeepEqual(got, want) {
		t.Errorf("interceptors = %+v, want %+v", got, want)
	}
}

func TestObjectModuleHandlersFromParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ObjectModule.bsl")
	module := bom + `&НаСервере Процедура ПередЗаписью(Отказ)
КонецПроцедуры

// Процедура ПриЗаписи(Отказ)
// КонецПроцедуры
`
	if err := os.WriteFile(path, []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	got := objectModuleHandlers(path)
	if !got["ПередЗаписью"] || got["ПриЗаписи"] {
		t.Errorf("handlers = %v, want ПередЗаписью only", got)
	}
}
