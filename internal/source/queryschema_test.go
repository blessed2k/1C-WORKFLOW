package source

import (
	"context"
	"testing"
)

func fieldNames(fields []QueryField) map[string]string {
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		m[f.Name] = f.Role
	}
	return m
}

func TestQuerySchemaCatalog(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	sc, err := s.QuerySchema(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("QuerySchema: %v", err)
	}
	if sc.TableName != "Справочник.Товары" {
		t.Errorf("TableName = %q, want Справочник.Товары", sc.TableName)
	}

	std := fieldNames(sc.StandardFields)
	for _, want := range []string{"Ссылка", "ПометкаУдаления", "Предопределенный", "Код", "Представление"} {
		if _, ok := std[want]; !ok {
			t.Errorf("standard fields missing %q; got %v", want, std)
		}
	}
	// Товары is not hierarchical and has no owner and no description length.
	for _, unwanted := range []string{"Родитель", "ЭтоГруппа", "Владелец", "Наименование"} {
		if _, ok := std[unwanted]; ok {
			t.Errorf("standard fields must not include %q for a flat catalog", unwanted)
		}
	}

	own := fieldNames(sc.OwnFields)
	if own["Артикул"] != "Реквизит" || own["ЕдиницаИзмерения"] != "Реквизит" {
		t.Errorf("own fields = %v", own)
	}
	if len(sc.TabularSections) != 1 || sc.TabularSections[0].Name != "Цены" {
		t.Errorf("tabular sections = %+v", sc.TabularSections)
	}
	if len(sc.VirtualTables) != 0 {
		t.Errorf("catalog must have no virtual tables; got %+v", sc.VirtualTables)
	}
}

func TestQuerySchemaInformationRegister(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	sc, err := s.QuerySchema(context.Background(), "InformationRegister", "КурсыВалют")
	if err != nil {
		t.Fatalf("QuerySchema: %v", err)
	}
	if sc.TableName != "РегистрСведений.КурсыВалют" {
		t.Errorf("TableName = %q", sc.TableName)
	}

	own := fieldNames(sc.OwnFields)
	if own["Валюта"] != "Измерение" {
		t.Errorf("Валюта role = %q, want Измерение", own["Валюта"])
	}
	if own["Курс"] != "Ресурс" {
		t.Errorf("Курс role = %q, want Ресурс", own["Курс"])
	}

	std := fieldNames(sc.StandardFields)
	if _, ok := std["Период"]; !ok {
		t.Errorf("periodic register must expose Период; got %v", std)
	}
	// Independent register: no Регистратор.
	if _, ok := std["Регистратор"]; ok {
		t.Errorf("independent register must not expose Регистратор")
	}

	vt := virtualSuffixes(sc.VirtualTables)
	for _, want := range []string{"СрезПоследних", "СрезПервых"} {
		if !vt[want] {
			t.Errorf("periodic register missing virtual table %q; got %v", want, vt)
		}
	}
}

func TestQuerySchemaAccumulationRegister(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	sc, err := s.QuerySchema(context.Background(), "AccumulationRegister", "ТоварыНаСкладах")
	if err != nil {
		t.Fatalf("QuerySchema: %v", err)
	}
	if sc.TableName != "РегистрНакопления.ТоварыНаСкладах" {
		t.Errorf("TableName = %q", sc.TableName)
	}

	std := fieldNames(sc.StandardFields)
	for _, want := range []string{"Регистратор", "Период", "Активность", "ВидДвижения"} {
		if _, ok := std[want]; !ok {
			t.Errorf("balance register missing standard field %q; got %v", want, std)
		}
	}

	vt := virtualSuffixes(sc.VirtualTables)
	for _, want := range []string{"Остатки", "Обороты", "ОстаткиИОбороты"} {
		if !vt[want] {
			t.Errorf("balance register missing virtual table %q; got %v", want, vt)
		}
	}
	// Check the Остатки virtual table full name and parameters.
	for _, v := range sc.VirtualTables {
		if v.Suffix == "Остатки" {
			if v.Name != "РегистрНакопления.ТоварыНаСкладах.Остатки" {
				t.Errorf("Остатки name = %q", v.Name)
			}
			if len(v.Parameters) == 0 {
				t.Errorf("Остатки must have parameters")
			}
		}
	}
}

func TestQuerySchemaDocument(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	sc, err := s.QuerySchema(context.Background(), "Document", "РеализацияТоваровУслуг")
	if err != nil {
		t.Fatalf("QuerySchema: %v", err)
	}
	if sc.TableName != "Документ.РеализацияТоваровУслуг" {
		t.Errorf("TableName = %q", sc.TableName)
	}
	std := fieldNames(sc.StandardFields)
	for _, want := range []string{"Ссылка", "ПометкаУдаления", "Дата", "Номер", "Проведен", "Представление"} {
		if _, ok := std[want]; !ok {
			t.Errorf("document standard fields missing %q; got %v", want, std)
		}
	}
	// The posting flag must be spelled with "е" (Проведен), the 1C field name.
	if _, ok := std["Проведён"]; ok {
		t.Errorf("standard field must be Проведен (е), not Проведён (ё)")
	}
	// Tabular section Товары is queryable with standard Ссылка/НомерСтроки plus own attributes.
	if len(sc.TabularSections) != 1 || sc.TabularSections[0].Name != "Товары" {
		t.Fatalf("tabular sections = %+v", sc.TabularSections)
	}
	tf := fieldNames(sc.TabularSections[0].Fields)
	for _, want := range []string{"Ссылка", "НомерСтроки", "Номенклатура"} {
		if _, ok := tf[want]; !ok {
			t.Errorf("tabular section fields missing %q; got %v", want, tf)
		}
	}
}

func TestQuerySchemaHierarchicalCatalog(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	sc, err := s.QuerySchema(context.Background(), "Catalog", "Подразделения")
	if err != nil {
		t.Fatalf("QuerySchema: %v", err)
	}
	std := fieldNames(sc.StandardFields)
	for _, want := range []string{"Родитель", "ЭтоГруппа", "Владелец", "Код", "Наименование"} {
		if _, ok := std[want]; !ok {
			t.Errorf("hierarchical/subordinate catalog missing %q; got %v", want, std)
		}
	}
}

func TestQuerySchemaTurnoversRegister(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	sc, err := s.QuerySchema(context.Background(), "AccumulationRegister", "Продажи")
	if err != nil {
		t.Fatalf("QuerySchema: %v", err)
	}
	vt := virtualSuffixes(sc.VirtualTables)
	if !vt["Обороты"] {
		t.Errorf("turnovers register must expose Обороты; got %v", vt)
	}
	for _, unwanted := range []string{"Остатки", "ОстаткиИОбороты"} {
		if vt[unwanted] {
			t.Errorf("turnovers register must not expose %q", unwanted)
		}
	}
	// No ВидДвижения on a turnovers register.
	if _, ok := fieldNames(sc.StandardFields)["ВидДвижения"]; ok {
		t.Errorf("turnovers register must not expose ВидДвижения")
	}
}

func TestQuerySchemaRecorderInformationRegister(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	sc, err := s.QuerySchema(context.Background(), "InformationRegister", "ЦеныНоменклатуры")
	if err != nil {
		t.Fatalf("QuerySchema: %v", err)
	}
	std := fieldNames(sc.StandardFields)
	for _, want := range []string{"Регистратор", "НомерСтроки", "Активность", "Период"} {
		if _, ok := std[want]; !ok {
			t.Errorf("recorder-subordinate register missing %q; got %v", want, std)
		}
	}
	vt := virtualSuffixes(sc.VirtualTables)
	if !vt["СрезПоследних"] || !vt["СрезПервых"] {
		t.Errorf("periodic register missing slice virtual tables; got %v", vt)
	}
}

func TestQuerySchemaRequiresTypeAndName(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	if _, err := s.QuerySchema(context.Background(), "", "X"); err == nil {
		t.Error("expected error for empty type")
	}
	if _, err := s.QuerySchema(context.Background(), "Catalog", ""); err == nil {
		t.Error("expected error for empty name")
	}
}

func virtualSuffixes(vts []VirtualTable) map[string]bool {
	m := make(map[string]bool, len(vts))
	for _, v := range vts {
		m[v.Suffix] = true
	}
	return m
}
