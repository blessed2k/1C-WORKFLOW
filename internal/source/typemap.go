package source

import "strings"

// russifyTypes renders raw 1C XML type identifiers (xs:decimal, cfg:CatalogRef.X,
// v8:ValueTable, ...) as their Russian 1C type names. Unknown identifiers pass
// through unchanged so no information is lost.
func russifyTypes(raw []string) []string {
	if len(raw) == 0 {
		return raw
	}
	out := make([]string, len(raw))
	for i, t := range raw {
		out[i] = russifyType(t)
	}
	return out
}

// primitiveTypes maps XML Schema primitives to 1C type names. Qualifiers
// (length, precision, date fractions) are separate from the type itself.
var primitiveTypes = map[string]string{
	"xs:string":       "Строка",
	"xs:decimal":      "Число",
	"xs:boolean":      "Булево",
	"xs:dateTime":     "Дата",
	"xs:date":         "Дата",
	"xs:base64Binary": "ХранилищеЗначения",
}

// platformTypes maps v8: platform types to 1C type names.
var platformTypes = map[string]string{
	"v8:ValueStorage":          "ХранилищеЗначения",
	"v8:ValueTable":            "ТаблицаЗначений",
	"v8:UUID":                  "УникальныйИдентификатор",
	"v8:Type":                  "Тип",
	"v8:StandardPeriod":        "СтандартныйПериод",
	"v8:StandardBeginningDate": "СтандартнаяДатаНачала",
}

// metadataKinds maps English metadata kinds to their Russian type prefix.
var metadataKinds = map[string]string{
	"Catalog":                    "Справочник",
	"Document":                   "Документ",
	"Enum":                       "Перечисление",
	"ChartOfCharacteristicTypes": "ПланВидовХарактеристик",
	"ChartOfAccounts":            "ПланСчетов",
	"ChartOfCalculationTypes":    "ПланВидовРасчета",
	"BusinessProcess":            "БизнесПроцесс",
	"Task":                       "Задача",
	"ExchangePlan":               "ПланОбмена",
	"InformationRegister":        "РегистрСведений",
	"AccumulationRegister":       "РегистрНакопления",
	"AccountingRegister":         "РегистрБухгалтерии",
	"CalculationRegister":        "РегистрРасчета",
	"DocumentJournal":            "ЖурналДокументов",
	"Constant":                   "Константа",
}

// refCategories maps reference/object category suffixes to Russian. Ordered
// longest-first so that e.g. "RecordKey" is matched before "Key" would be.
var refCategories = []struct{ en, ru string }{
	{"RecordManager", "МенеджерЗаписи"},
	{"RecordKey", "КлючЗаписи"},
	{"RecordSet", "НаборЗаписей"},
	{"Selection", "Выборка"},
	{"Manager", "Менеджер"},
	{"Object", "Объект"},
	{"List", "Список"},
	{"Ref", "Ссылка"},
}

// russifyType renders one type identifier, or returns it unchanged if unknown.
func russifyType(t string) string {
	if ru, ok := primitiveTypes[t]; ok {
		return ru
	}
	if ru, ok := platformTypes[t]; ok {
		return ru
	}
	// Configuration reference types carry a namespace prefix that the export
	// assigns dynamically (cfg:, d5p1:, d6p1:, ...). Strip any prefix and try to
	// russify the reference; primitives (xs:) and platform types (v8:) are
	// already handled above.
	if _, rest, ok := strings.Cut(t, ":"); ok {
		if ru, ok := russifyRef(rest); ok {
			return ru
		}
	}
	return t
}

// russifyRef turns "CatalogRef.Валюты" into "СправочникСсылка.Валюты".
func russifyRef(rest string) (string, bool) {
	head, name, ok := strings.Cut(rest, ".")
	if !ok {
		return "", false
	}
	for _, cat := range refCategories {
		if kindEn, found := strings.CutSuffix(head, cat.en); found {
			if kindRu, ok := metadataKinds[kindEn]; ok {
				return kindRu + cat.ru + "." + name, true
			}
			return "", false
		}
	}
	return "", false
}
