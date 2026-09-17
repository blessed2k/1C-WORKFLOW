package source

import (
	"reflect"
	"testing"
)

func TestRussifyType(t *testing.T) {
	cases := map[string]string{
		"xs:string":                            "Строка",
		"xs:decimal":                           "Число",
		"xs:boolean":                           "Булево",
		"xs:dateTime":                          "Дата",
		"xs:base64Binary":                      "ХранилищеЗначения",
		"cfg:CatalogRef.Валюты":                "СправочникСсылка.Валюты",
		"cfg:DocumentRef.СписаниеСРасчетного":  "ДокументСсылка.СписаниеСРасчетного",
		"cfg:EnumRef.СтатусАвтопроверкиHH":     "ПеречислениеСсылка.СтатусАвтопроверкиHH",
		"cfg:CatalogObject.Товары":             "СправочникОбъект.Товары",
		"cfg:InformationRegisterRecordKey.Цен": "РегистрСведенийКлючЗаписи.Цен",
		"d5p1:CatalogRef.Компании":            "СправочникСсылка.Компании", // dynamic export prefix
		"d6p1:DocumentRef.Заказ":              "ДокументСсылка.Заказ",
		"v8:ValueTable":                        "ТаблицаЗначений",
		"v8:ValueStorage":                      "ХранилищеЗначения",
		"cfg:UnknownKindRef.X":                 "cfg:UnknownKindRef.X", // unknown kind -> passthrough
		"some:WeirdType":                       "some:WeirdType",       // unknown -> passthrough
	}
	for in, want := range cases {
		if got := russifyType(in); got != want {
			t.Errorf("russifyType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRussifyTypesComposite(t *testing.T) {
	got := russifyTypes([]string{"cfg:CatalogRef.Валюты", "xs:string"})
	want := []string{"СправочникСсылка.Валюты", "Строка"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("russifyTypes composite = %v, want %v", got, want)
	}
}
