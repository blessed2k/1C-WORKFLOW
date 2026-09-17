package source

import (
	"context"
	"testing"
)

// adviseIn runs the advisor against an arbitrary export root, for rules that
// need metadata the shared dump does not have (e.g. a multi-dimension register).
func adviseIn(t *testing.T, root, text string) *QueryAdvice {
	t.Helper()
	adv, err := NewXMLSource(root).AdviseQuery(context.Background(), text)
	if err != nil {
		t.Fatalf("AdviseQuery: %v", err)
	}
	return adv
}

func TestAdviseSelectStar(t *testing.T) {
	star := advise(t, `ВЫБРАТЬ * ИЗ Справочник.Товары КАК Т`)
	if _, ok := adviceCodes(star)["SelectStar"]; !ok {
		t.Errorf("ВЫБРАТЬ * must warn; got %+v", star.Warnings)
	}

	// The keywords between ВЫБРАТЬ and * must not hide the star.
	withKeywords := advise(t, `ВЫБРАТЬ РАЗРЕШЕННЫЕ РАЗЛИЧНЫЕ ПЕРВЫЕ 10 * ИЗ Справочник.Товары КАК Т`)
	if _, ok := adviceCodes(withKeywords)["SelectStar"]; !ok {
		t.Errorf("ВЫБРАТЬ РАЗРЕШЕННЫЕ РАЗЛИЧНЫЕ ПЕРВЫЕ 10 * must warn; got %+v", withKeywords.Warnings)
	}

	// КОЛИЧЕСТВО(*) is not a star select.
	count := advise(t, `ВЫБРАТЬ КОЛИЧЕСТВО(*) КАК Всего ИЗ Справочник.Товары КАК Т`)
	if _, ok := adviceCodes(count)["SelectStar"]; ok {
		t.Errorf("КОЛИЧЕСТВО(*) must not warn; got %+v", count.Warnings)
	}
}

func TestAdviseUnindexedJoin(t *testing.T) {
	// ЕдиницаИзмерения is not indexed -> joining by it warns.
	adv := advise(t, `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
		ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК П
		ПО П.ЕдиницаИзмерения = Т.ЕдиницаИзмерения`)
	w, ok := adviceCodes(adv)["UnindexedJoin"]
	if !ok {
		t.Fatalf("join on a non-indexed field must warn; got %+v", adv.Warnings)
	}
	if w.Field != "ЕдиницаИзмерения" {
		t.Errorf("warning field = %q, want ЕдиницаИзмерения", w.Field)
	}
	// The condition is a join condition, not a filter.
	if _, ok := adviceCodes(adv)["UnindexedFilter"]; ok {
		t.Errorf("join condition must not be reported as a filter; got %+v", adv.Warnings)
	}
}

func TestAdviseIndexedJoinNoWarn(t *testing.T) {
	adv := advise(t, `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
		ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК П ПО П.Ссылка = Т.Ссылка`)
	if w, ok := adviceCodes(adv)["UnindexedJoin"]; ok {
		t.Errorf("join by Ссылка must not warn; got field %q", w.Field)
	}
}

func TestAdviseJoinConditionDoesNotEatOrderBy(t *testing.T) {
	// УПОРЯДОЧИТЬ ПО must end the join condition, otherwise its fields would be
	// checked as if they were join keys.
	conds := extractJoinConditions(`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
		ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК П ПО П.Ссылка = Т.Ссылка
		УПОРЯДОЧИТЬ ПО Т.ЕдиницаИзмерения`)
	if len(conds) != 1 {
		t.Fatalf("join conditions = %q, want 1", conds)
	}
	if got := conds[0]; !containsSubstring(got, "П.Ссылка") || containsSubstring(got, "ЕдиницаИзмерения") {
		t.Errorf("join condition = %q, want only the ПО condition", got)
	}
}

func TestAdviseJoinConditionSkipsSubquery(t *testing.T) {
	// The subquery's own СГРУППИРОВАТЬ ПО must not be taken for the join
	// condition, otherwise its grouping fields are checked as join keys.
	conds := extractJoinConditions(`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
		ЛЕВОЕ СОЕДИНЕНИЕ (ВЫБРАТЬ П.ЕдиницаИзмерения КАК Е ИЗ Справочник.Товары КАК П
		СГРУППИРОВАТЬ ПО П.ЕдиницаИзмерения) КАК В
		ПО В.Е = Т.ЕдиницаИзмерения`)
	if len(conds) != 1 {
		t.Fatalf("join conditions = %q, want 1", conds)
	}
	if !containsSubstring(conds[0], "В.Е") {
		t.Errorf("join condition = %q, want the outer ПО condition", conds[0])
	}
}

func TestAdviseVirtualTablesJoined(t *testing.T) {
	adv := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар В (&Список)) КАК О
		ВНУТРЕННЕЕ СОЕДИНЕНИЕ РегистрСведений.ЦеныНоменклатуры.СрезПоследних(&Дата) КАК Ц
		ПО Ц.Номенклатура = О.Товар`)
	if _, ok := adviceCodes(adv)["VirtualTablesJoined"]; !ok {
		t.Errorf("two joined virtual tables must warn; got %+v", adv.Warnings)
	}

	single := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар В (&Список)) КАК О
		ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК Т ПО Т.Ссылка = О.Товар`)
	if _, ok := adviceCodes(single)["VirtualTablesJoined"]; ok {
		t.Errorf("one virtual table joined to a real table must not warn; got %+v", single.Warnings)
	}
}

func TestAdviseNonLeadingDimension(t *testing.T) {
	root := "testdata/qa"
	// Организация is the first dimension; filtering only by Номенклатура skips
	// the leading part of the composite index.
	adv := adviseIn(t, root, `ВЫБРАТЬ Р.Количество ИЗ РегистрНакопления.ТоварыОрганизаций КАК Р
		ГДЕ Р.Номенклатура = &Н`)
	w, ok := adviceCodes(adv)["NonLeadingDimensionFilter"]
	if !ok {
		t.Fatalf("filter on a non-leading dimension must warn; got %+v", adv.Warnings)
	}
	if w.Field != "Номенклатура" {
		t.Errorf("warning field = %q, want Номенклатура", w.Field)
	}

	// The leading dimension is filtered too -> the index is usable.
	withLeading := adviseIn(t, root, `ВЫБРАТЬ Р.Количество ИЗ РегистрНакопления.ТоварыОрганизаций КАК Р
		ГДЕ Р.Организация = &О И Р.Номенклатура = &Н`)
	if _, ok := adviceCodes(withLeading)["NonLeadingDimensionFilter"]; ok {
		t.Errorf("filter including the leading dimension must not warn; got %+v", withLeading.Warnings)
	}
}

func TestAdviseNonLeadingDimensionInVirtualTableParams(t *testing.T) {
	root := "testdata/qa"
	// The filter lives in the virtual-table parameters, not in ГДЕ.
	adv := adviseIn(t, root, `ВЫБРАТЬ О.Количество ИЗ РегистрНакопления.ТоварыОрганизаций.Остатки(&Дата, Склад = &С) КАК О`)
	if _, ok := adviceCodes(adv)["NonLeadingDimensionFilter"]; !ok {
		t.Errorf("non-leading dimension in VT parameters must warn; got %+v", adv.Warnings)
	}

	ok := adviseIn(t, root, `ВЫБРАТЬ О.Количество ИЗ РегистрНакопления.ТоварыОрганизаций.Остатки(&Дата, Организация = &О И Склад = &С) КАК О`)
	if _, found := adviceCodes(ok)["NonLeadingDimensionFilter"]; found {
		t.Errorf("leading dimension in VT parameters must not warn; got %+v", ok.Warnings)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && indexOfWord(s, sub, 0) >= 0
}
