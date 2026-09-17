package source

import (
	"context"
	"fmt"
	"path/filepath"
)

// queryPrefix maps a metadata type to its Russian query-table prefix.
var queryPrefix = map[string]string{
	"Catalog":                    "Справочник",
	"Document":                   "Документ",
	"InformationRegister":        "РегистрСведений",
	"AccumulationRegister":       "РегистрНакопления",
	"AccountingRegister":         "РегистрБухгалтерии",
	"CalculationRegister":        "РегистрРасчета",
	"ChartOfCharacteristicTypes": "ПланВидовХарактеристик",
	"ChartOfAccounts":            "ПланСчетов",
	"ChartOfCalculationTypes":    "ПланВидовРасчета",
	"ExchangePlan":               "ПланОбмена",
	"BusinessProcess":            "БизнесПроцесс",
	"Task":                       "Задача",
	"Enum":                       "Перечисление",
}

// QuerySchema returns the query-language view of one object: standard fields,
// own fields, tabular sections and register virtual tables. Standard fields and
// virtual tables are derived deterministically from the object's properties
// (hierarchy, ownership, posting, periodicity, write mode, register type),
// because the exported StandardAttributes list the full type set regardless of
// whether a field actually applies.
func (s *XMLSource) QuerySchema(_ context.Context, objectType, name string) (*QuerySchema, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	path := filepath.Join(s.root, folderForType(objectType), name+".xml")
	var root xmlObjectRoot
	if err := readXML(path, &root); err != nil {
		return nil, err
	}
	obj := root.Object
	p := obj.Properties
	kind := obj.XMLName.Local

	prefix := queryPrefix[objectType]
	if prefix == "" {
		prefix = objectType
	}
	out := &QuerySchema{
		TableName:  prefix + "." + p.Name,
		ObjectType: kind,
		Name:       p.Name,
	}

	// Own fields and tabular sections from the object's child objects.
	for _, ch := range obj.ChildObjects.Items {
		switch ch.XMLName.Local {
		case "Attribute":
			out.OwnFields = append(out.OwnFields, QueryField{Name: ch.name(), Role: "Реквизит", Type: russifyTypes(ch.typeList())})
		case "Dimension":
			out.OwnFields = append(out.OwnFields, QueryField{Name: ch.name(), Role: "Измерение", Type: russifyTypes(ch.typeList())})
		case "Resource":
			out.OwnFields = append(out.OwnFields, QueryField{Name: ch.name(), Role: "Ресурс", Type: russifyTypes(ch.typeList())})
		case "TabularSection":
			// A tabular section is queryable as Объект.ИмяТЧ with standard fields
			// Ссылка (owner reference) and НомерСтроки in addition to its own attributes.
			ts := QueryTable{Name: ch.name(), Fields: []QueryField{std("Ссылка"), std("НомерСтроки")}}
			if ch.ChildObjects != nil {
				for _, sub := range ch.ChildObjects.Items {
					if sub.XMLName.Local == "Attribute" {
						ts.Fields = append(ts.Fields, QueryField{Name: sub.name(), Role: "Реквизит", Type: russifyTypes(sub.typeList())})
					}
				}
			}
			out.TabularSections = append(out.TabularSections, ts)
		}
	}

	out.StandardFields, out.VirtualTables = standardSchema(kind, p, out.TableName)
	return out, nil
}

// std builds a standard field with the "Стандартный" role.
func std(name string) QueryField { return QueryField{Name: name, Role: "Стандартный"} }

// standardSchema derives the standard fields and virtual tables for an object
// from its metadata type and properties.
func standardSchema(kind string, p xmlObjProps, table string) ([]QueryField, []VirtualTable) {
	switch kind {
	case "Catalog":
		f := []QueryField{std("Ссылка"), std("ПометкаУдаления"), std("Предопределенный"), std("ИмяПредопределенныхДанных")}
		if p.hasCode() {
			f = append(f, std("Код"))
		}
		if p.hasName() {
			f = append(f, std("Наименование"))
		}
		if p.hierarchical() {
			f = append(f, std("Родитель"))
			if p.hasGroups() {
				f = append(f, std("ЭтоГруппа"))
			}
		}
		if p.subordinate() {
			f = append(f, std("Владелец"))
		}
		f = append(f, std("Представление"))
		return f, nil

	case "Document":
		f := []QueryField{std("Ссылка"), std("ПометкаУдаления"), std("Дата"), std("Номер")}
		if p.posts() {
			f = append(f, std("Проведен"))
		}
		f = append(f, std("Представление"))
		return f, nil

	case "InformationRegister":
		var f []QueryField
		if p.byRecorder() {
			f = append(f, std("Регистратор"), std("НомерСтроки"), std("Активность"))
		}
		if p.periodic() {
			f = append(f, std("Период"))
		}
		if !p.periodic() {
			return f, nil
		}
		return f, []VirtualTable{
			{Name: table + ".СрезПоследних", Suffix: "СрезПоследних", Parameters: []string{"Период", "Условие"}},
			{Name: table + ".СрезПервых", Suffix: "СрезПервых", Parameters: []string{"Период", "Условие"}},
		}

	case "AccumulationRegister":
		f := []QueryField{std("Регистратор"), std("НомерСтроки"), std("Период"), std("Активность")}
		if p.balanceType() {
			f = append(f, std("ВидДвижения"))
			return f, []VirtualTable{
				{Name: table + ".Остатки", Suffix: "Остатки", Parameters: []string{"Период", "Условие"}},
				{Name: table + ".Обороты", Suffix: "Обороты", Parameters: []string{"НачалоПериода", "КонецПериода", "Периодичность", "Условие"}},
				{Name: table + ".ОстаткиИОбороты", Suffix: "ОстаткиИОбороты", Parameters: []string{"НачалоПериода", "КонецПериода", "Периодичность", "МетодДополнения", "Условие"}},
			}
		}
		return f, []VirtualTable{
			{Name: table + ".Обороты", Suffix: "Обороты", Parameters: []string{"НачалоПериода", "КонецПериода", "Периодичность", "Условие"}},
		}
	}

	// Other reference types (charts, exchange plans, business processes, tasks):
	// expose the reference and deletion mark only. Non-reference types (enums,
	// accounting/calculation registers) are not modelled here.
	if referenceType[kind] {
		return []QueryField{std("Ссылка"), std("ПометкаУдаления")}, nil
	}
	return nil, nil
}

// referenceType lists the reference metadata types that expose Ссылка and
// ПометкаУдаления as standard fields.
var referenceType = map[string]bool{
	"Catalog":                    true,
	"Document":                   true,
	"ChartOfCharacteristicTypes": true,
	"ChartOfAccounts":            true,
	"ChartOfCalculationTypes":    true,
	"ExchangePlan":               true,
	"BusinessProcess":            true,
	"Task":                       true,
}
