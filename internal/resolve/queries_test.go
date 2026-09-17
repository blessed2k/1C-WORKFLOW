package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// TestLiteralTextSimple — литерал в одну строку без спецсимволов: значение
// совпадает с содержимым между кавычками.
func TestLiteralTextSimple(t *testing.T) {
	got, ok := literalText([]byte(`"ВЫБРАТЬ Товары.Код ИЗ Справочник.Товары КАК Товары"`))
	if !ok {
		t.Fatal("literalText вернул ok=false на корректном литерале")
	}
	want := "ВЫБРАТЬ Товары.Код ИЗ Справочник.Товары КАК Товары"
	if got != want {
		t.Errorf("literalText = %q, ожидалось %q", got, want)
	}
}

// TestLiteralTextEscapedQuote — удвоенная кавычка внутри литерала
// разворачивается в одну: BSL-идиома экранирования.
func TestLiteralTextEscapedQuote(t *testing.T) {
	got, ok := literalText([]byte(`"a""b"`))
	if !ok {
		t.Fatal("literalText вернул ok=false")
	}
	if want := `a"b`; got != want {
		t.Errorf("literalText = %q, ожидалось %q", got, want)
	}
}

// TestLiteralTextMultilineContinuation — многострочный литерал: пробелы
// ПЕРЕД '|' продолжения — это отступ исходника BSL и в значение не входят;
// сам '|' — маркер переноса, тоже не входит; всё, что идёт ПОСЛЕ '|' на
// строке (включая собственные пробелы/табы), — часть значения. Ожидаемое
// значение посчитано вручную по этому правилу, не выводом literalText.
func TestLiteralTextMultilineContinuation(t *testing.T) {
	raw := "\"ВЫБРАТЬ\n" +
		"\t|\tТовары.Наименование\n" +
		"\t|ИЗ\n" +
		"\t|\tСправочник.Товары КАК Товары\""
	got, ok := literalText([]byte(raw))
	if !ok {
		t.Fatal("literalText вернул ok=false")
	}
	want := "ВЫБРАТЬ\n\tТовары.Наименование\nИЗ\n\tСправочник.Товары КАК Товары"
	if got != want {
		t.Errorf("literalText =\n%q\nожидалось\n%q", got, want)
	}
}

// TestLiteralTextNotAString — байты, не являющиеся литералом (нет кавычек по
// краям), — ok=false, не паника и не мусор.
func TestLiteralTextNotAString(t *testing.T) {
	if _, ok := literalText([]byte("нетКавычек")); ok {
		t.Error("ok=true на байтах без кавычек")
	}
	if _, ok := literalText([]byte(`"`)); ok {
		t.Error("ok=true на одной кавычке (меньше 2 байт)")
	}
}

// TestDeriveQueryReferenceStaticQuery — критерий "query_reference (таблица/
// поле/параметр/ВТ -> metadata_object/member)": статичный текст запроса
// целиком разбирается, таблица и поле резолвятся в объект/член метаданных,
// а поле без такого члена в Env остаётся честно unresolved, не теряется.
// Ожидаемые имена посчитаны вручную по тексту запроса, не взяты из вывода
// query.Parse или DeriveQueryReference.
func TestDeriveQueryReferenceStaticQuery(t *testing.T) {
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tЗапрос = Новый Запрос;\n" +
		"\tЗапрос.Текст =\n" +
		"\t\"ВЫБРАТЬ\n" +
		"\t|\tТовары.Наименование,\n" +
		"\t|\tТовары.Код\n" +
		"\t|ИЗ\n" +
		"\t|\tСправочник.Товары КАК Товары\";\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "CommonModules/X/Ext/Module.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик парсера быть не должно: %v", diags)
	}
	if len(mod.Queries) != 1 || mod.Queries[0].Staticity != bsl.StaticityStatic {
		t.Fatalf("ожидался ровно один статичный QueryLiteral, получено %+v", mod.Queries)
	}

	env := mustEnv(t, EnvInput{
		Component: testComponent,
		Objects: []ObjectRef{
			{MType: "Catalog", NameNorm: "товары", IdentityKey: "metadata:Catalog:товары"},
		},
		Members: []MemberRef{
			{ObjectMType: "Catalog", ObjectNameNorm: "товары", NameNorm: "наименование", IdentityKey: "member:Catalog:товары:наименование"},
			// "Код" намеренно не добавлен — должен остаться unresolved.
		},
	}, nil)

	groups := DeriveQueryReference(mod, env)
	if len(groups) != 1 {
		t.Fatalf("групп = %d, ожидалась 1 (один статичный литерал): %+v", len(groups), groups)
	}
	if groups[0].LiteralIndex != 0 {
		t.Errorf("LiteralIndex = %d, ожидался 0", groups[0].LiteralIndex)
	}
	results := groups[0].References

	var tables, fields, params []QueryReferenceResult
	for _, r := range results {
		switch r.Kind {
		case QueryRefTable:
			tables = append(tables, r)
		case QueryRefField:
			fields = append(fields, r)
		case QueryRefParameter:
			params = append(params, r)
		}
	}
	if len(tables) != 1 {
		t.Fatalf("таблиц = %d, ожидалась 1: %+v", len(tables), tables)
	}
	if tables[0].NameNorm != "товары" || !tables[0].ObjectResolved || tables[0].ObjectKey != "metadata:Catalog:товары" {
		t.Errorf("table = %+v, ожидался товары/resolved/metadata:Catalog:товары", tables[0])
	}

	if len(fields) != 2 {
		t.Fatalf("полей = %d, ожидалось 2: %+v", len(fields), fields)
	}
	byName := map[string]QueryReferenceResult{}
	for _, f := range fields {
		byName[f.NameNorm] = f
	}
	naim, ok := byName["наименование"]
	if !ok {
		t.Fatalf("нет поля 'наименование' среди %+v", fields)
	}
	if !naim.ObjectResolved || !naim.MemberResolved || naim.MemberKey != "member:Catalog:товары:наименование" {
		t.Errorf("поле 'наименование' = %+v, ожидался resolved член", naim)
	}
	kod, ok := byName["код"]
	if !ok {
		t.Fatalf("нет поля 'код' среди %+v", fields)
	}
	if !kod.ObjectResolved {
		t.Errorf("поле 'код': объект-владелец должен резолвиться (%+v)", kod)
	}
	if kod.MemberResolved {
		t.Errorf("поле 'код': член НЕ заведён в Env, MemberResolved обязан быть false (%+v)", kod)
	}

	if len(params) != 0 {
		t.Errorf("параметров = %d, ожидалось 0", len(params))
	}
}

// TestDeriveQueryReferenceSkipsPartial — литерал-фрагмент конкатенации
// (Partial) НЕ разбирается этой функцией: сборка полного текста из
// GapMarker-фрагментов — дело таска 09 (см. doc-комментарий DeriveQueryReference).
func TestDeriveQueryReferenceSkipsPartial(t *testing.T) {
	// Литерал "ИЗ " не начинается с ВЫБРАТЬ/SELECT (иначе parser классифицирует
	// его Static независимо от конкатенации — это отдельная эвристика
	// парсера, не баг этого теста), стоит рядом с '+' и содержит ключевое
	// слово запроса "ИЗ " — ровно условие Partial в bsl.collectQueryLiteral.
	src := []byte("Функция Тест() Экспорт\n" +
		"\tТекст = \"ИЗ \" + ИмяТаблицы;\n" +
		"\tВозврат Текст;\n" +
		"КонецФункции\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "CommonModules/X/Ext/Module.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", diags)
	}
	var partial bool
	for _, q := range mod.Queries {
		if q.Staticity == bsl.StaticityPartial {
			partial = true
		}
	}
	if !partial {
		t.Fatalf("фикстура теста сломана: ожидался хотя бы один Partial QueryLiteral, получено %+v", mod.Queries)
	}

	env := mustEnv(t, EnvInput{Component: testComponent}, nil)
	groups := DeriveQueryReference(mod, env)
	if len(groups) != 0 {
		t.Errorf("Partial-литерал не должен давать группу query_reference: получено %+v", groups)
	}
}

// TestDeriveQueryReferenceGroupsByLiteral — критерий долга таска 12
// (interfaces.md, «Из таска 09»): ДВА статичных литерала в одном модуле дают
// ДВЕ отдельные группы со своими LiteralIndex, а не один смешанный список —
// иначе query_id второго литерала получил бы ссылки первого. Ожидаемые
// имена таблиц посчитаны вручную по тексту, не взяты из вывода функции.
func TestDeriveQueryReferenceGroupsByLiteral(t *testing.T) {
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tЗ1 = Новый Запрос(\"ВЫБРАТЬ Товары.Код ИЗ Справочник.Товары КАК Товары\");\n" +
		"\tЗ2 = Новый Запрос(\"ВЫБРАТЬ Контрагенты.ИНН ИЗ Справочник.Контрагенты КАК Контрагенты\");\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "CommonModules/X/Ext/Module.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик парсера быть не должно: %v", diags)
	}
	if len(mod.Queries) != 2 {
		t.Fatalf("ожидалось 2 QueryLiteral, получено %d: %+v", len(mod.Queries), mod.Queries)
	}

	env := mustEnv(t, EnvInput{
		Component: testComponent,
		Objects: []ObjectRef{
			{MType: "Catalog", NameNorm: "товары", IdentityKey: "metadata:Catalog:товары"},
			{MType: "Catalog", NameNorm: "контрагенты", IdentityKey: "metadata:Catalog:контрагенты"},
		},
	}, nil)

	groups := DeriveQueryReference(mod, env)
	if len(groups) != 2 {
		t.Fatalf("групп = %d, ожидалось 2: %+v", len(groups), groups)
	}
	if groups[0].LiteralIndex == groups[1].LiteralIndex {
		t.Fatalf("группы делят один LiteralIndex: %+v", groups)
	}

	byIndex := map[int]QueryLiteralReferences{groups[0].LiteralIndex: groups[0], groups[1].LiteralIndex: groups[1]}
	g0, ok := byIndex[0]
	if !ok {
		t.Fatalf("нет группы для литерала 0 среди %+v", groups)
	}
	g1, ok := byIndex[1]
	if !ok {
		t.Fatalf("нет группы для литерала 1 среди %+v", groups)
	}

	tableOf := func(g QueryLiteralReferences) string {
		for _, r := range g.References {
			if r.Kind == QueryRefTable {
				return r.NameNorm
			}
		}
		return ""
	}
	if got := tableOf(g0); got != "товары" {
		t.Errorf("литерал 0: таблица = %q, ожидалась 'товары' (своя, не 'контрагенты' из другого литерала)", got)
	}
	if got := tableOf(g1); got != "контрагенты" {
		t.Errorf("литерал 1: таблица = %q, ожидалась 'контрагенты' (своя, не 'товары' из другого литерала)", got)
	}
}
