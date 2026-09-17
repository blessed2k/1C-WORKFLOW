package source

import (
	"context"
	"strings"
	"testing"
)

func adviceCodes(adv *QueryAdvice) map[string]AdviceItem {
	m := make(map[string]AdviceItem, len(adv.Warnings))
	for _, w := range adv.Warnings {
		m[w.Code] = w
	}
	return m
}

func advise(t *testing.T, text string) *QueryAdvice {
	t.Helper()
	adv, err := NewXMLSource("testdata/dump").AdviseQuery(context.Background(), text)
	if err != nil {
		t.Fatalf("AdviseQuery: %v", err)
	}
	return adv
}

func TestAdviseJoinSubquery(t *testing.T) {
	adv := advise(t, `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
		ВНУТРЕННЕЕ СОЕДИНЕНИЕ (ВЫБРАТЬ П.Ссылка ИЗ Справочник.Товары КАК П) КАК В
		ПО В.Ссылка = Т.Ссылка`)
	if _, ok := adviceCodes(adv)["JoinWithSubquery"]; !ok {
		t.Errorf("expected JoinWithSubquery; got %+v", adv.Warnings)
	}
}

func TestAdviseVirtualTableNoParams(t *testing.T) {
	withParams := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар = &Товар) КАК О`)
	if _, ok := adviceCodes(withParams)["VirtualTableNoParams"]; ok {
		t.Errorf("virtual table WITH params must not warn; got %+v", withParams.Warnings)
	}

	noParams := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки КАК О`)
	if _, ok := adviceCodes(noParams)["VirtualTableNoParams"]; !ok {
		t.Errorf("virtual table WITHOUT params must warn; got %+v", noParams.Warnings)
	}
}

func TestAdviseVirtualTableNestedParams(t *testing.T) {
	// Filter with a nested list "В (&Список)" is parametrised -> no warning.
	list := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар В (&Список)) КАК О`)
	if _, ok := adviceCodes(list)["VirtualTableNoParams"]; ok {
		t.Errorf("nested-list params must not warn; got %+v", list.Warnings)
	}
	// Filter with a subquery in the parameter -> also parametrised.
	sub := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(, Товар В (ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т)) КАК О`)
	if _, ok := adviceCodes(sub)["VirtualTableNoParams"]; ok {
		t.Errorf("subquery param must not warn; got %+v", sub.Warnings)
	}
}

func TestAdviseVirtualTableAsSource(t *testing.T) {
	// A virtual table with params must be recognised as a source so its filters
	// are checked. Товар is an indexed dimension -> no UnindexedFilter on it.
	adv := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата) КАК О ГДЕ О.Товар = &Т`)
	if !containsString(adv.Tables, "РегистрНакопления.ТоварыНаСкладах.Остатки") {
		t.Errorf("virtual table source not recognised; tables = %v", adv.Tables)
	}
	if _, ok := adviceCodes(adv)["UnindexedFilter"]; ok {
		t.Errorf("filter on indexed dimension Товар must not warn; got %+v", adv.Warnings)
	}
}

func TestExtractWhereWordBoundary(t *testing.T) {
	// A field name containing a terminator substring must not truncate WHERE,
	// but a real terminator keyword must.
	w := extractWhere(`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.ИтогиПродаж = 5 УПОРЯДОЧИТЬ ПО Т.Артикул`)
	if !strings.Contains(w, "ИтогиПродаж") {
		t.Errorf("WHERE truncated on substring ИТОГИ inside ИтогиПродаж: %q", w)
	}
	if strings.Contains(w, "УПОРЯДОЧИТЬ") {
		t.Errorf("WHERE not cut at УПОРЯДОЧИТЬ: %q", w)
	}
}

func TestAdviseOuterJoinIsNull(t *testing.T) {
	noIsNull := advise(t, `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
		ЛЕВОЕ СОЕДИНЕНИЕ Справочник.Товары КАК П ПО П.Ссылка = Т.Ссылка`)
	if _, ok := adviceCodes(noIsNull)["OuterJoinWithoutIsNull"]; !ok {
		t.Errorf("left join without ЕСТЬNULL must warn; got %+v", noIsNull.Warnings)
	}

	withIsNull := advise(t, `ВЫБРАТЬ ЕСТЬNULL(П.Ссылка, "") КАК Р ИЗ Справочник.Товары КАК Т
		ЛЕВОЕ СОЕДИНЕНИЕ Справочник.Товары КАК П ПО П.Ссылка = Т.Ссылка`)
	if _, ok := adviceCodes(withIsNull)["OuterJoinWithoutIsNull"]; ok {
		t.Errorf("left join WITH ЕСТЬNULL must not warn; got %+v", withIsNull.Warnings)
	}
}

func TestAdviseLeadingWildcard(t *testing.T) {
	adv := advise(t, `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Артикул ПОДОБНО "%шуруп%"`)
	if _, ok := adviceCodes(adv)["LeadingWildcardLike"]; !ok {
		t.Errorf("leading-wildcard LIKE must warn; got %+v", adv.Warnings)
	}
}

func TestAdviseUnindexedFilter(t *testing.T) {
	// ЕдиницаИзмерения is not indexed -> warn.
	adv := advise(t, `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.ЕдиницаИзмерения = &Е`)
	w, ok := adviceCodes(adv)["UnindexedFilter"]
	if !ok {
		t.Fatalf("filter on non-indexed field must warn; got %+v", adv.Warnings)
	}
	if w.Field != "ЕдиницаИзмерения" {
		t.Errorf("warning field = %q, want ЕдиницаИзмерения", w.Field)
	}
}

func TestAdviseIndexedFilterNoWarn(t *testing.T) {
	// Артикул is indexed and Ссылка is a standard indexed field -> no warning.
	adv := advise(t, `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Артикул = &А И Т.Ссылка = &С`)
	if w, ok := adviceCodes(adv)["UnindexedFilter"]; ok {
		t.Errorf("indexed/standard fields must not warn; got field %q", w.Field)
	}
}

func TestAdviseCleanQuery(t *testing.T) {
	adv := advise(t, `ВЫБРАТЬ Т.Ссылка КАК Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Артикул = &А`)
	if adv.Count != 0 {
		t.Errorf("clean query must have no warnings; got %+v", adv.Warnings)
	}
	if len(adv.Tables) != 1 || adv.Tables[0] != "Справочник.Товары" {
		t.Errorf("tables = %v, want [Справочник.Товары]", adv.Tables)
	}
}
