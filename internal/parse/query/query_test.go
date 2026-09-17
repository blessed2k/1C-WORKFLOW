package query

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestParseSimpleSelect — простейший запрос: одно поле, одна таблица, без
// алиасов. Ожидаемые значения посчитаны вручную по тексту, а не выведены из
// кода парсера.
func TestParseSimpleSelect(t *testing.T) {
	text := "ВЫБРАТЬ Ссылка ИЗ Справочник.Номенклатура"
	q, diags := Parse(text)
	if len(diags) != 0 {
		t.Fatalf("диагностики неожиданны: %+v", diags)
	}
	if q.Text != text {
		t.Errorf("Text = %q, ожидалось %q", q.Text, text)
	}
	if q.Staticity != StaticityStatic {
		t.Errorf("Staticity = %q, ожидалось static", q.Staticity)
	}
	if q.Confidence != domain.ConfidenceExact {
		t.Errorf("Confidence = %v, ожидалось %v (текст-литерал целиком)", q.Confidence, domain.ConfidenceExact)
	}
	if len(q.Fields) != 1 || q.Fields[0].Name != "Ссылка" || q.Fields[0].Qualifier != "" {
		t.Fatalf("Fields = %+v, ожидалось одно поле Ссылка без квалификатора", q.Fields)
	}
	if len(q.Tables) != 1 || q.Tables[0].Name != "Справочник.Номенклатура" || q.Tables[0].Kind != TableBase {
		t.Fatalf("Tables = %+v, ожидалась одна базовая таблица Справочник.Номенклатура", q.Tables)
	}
}

// TestParseQualifiedFieldWithAlias — квалифицированное поле (через точку) с
// алиасом и алиасом таблицы-источника.
func TestParseQualifiedFieldWithAlias(t *testing.T) {
	text := "ВЫБРАТЬ Ном.Наименование КАК Название ИЗ Справочник.Номенклатура КАК Ном"
	q, _ := Parse(text)
	if len(q.Fields) != 1 {
		t.Fatalf("Fields = %+v, ожидалось одно поле", q.Fields)
	}
	f := q.Fields[0]
	if f.Qualifier != "Ном" || f.Name != "Наименование" || f.Alias != "Название" {
		t.Errorf("Field = %+v, ожидалось {Qualifier:Ном Name:Наименование Alias:Название}", f)
	}
	if len(q.Tables) != 1 || q.Tables[0].Alias != "Ном" {
		t.Fatalf("Tables = %+v, ожидался алиас Ном", q.Tables)
	}
}

// TestParseParameter — параметр запроса &Имя в условии ГДЕ.
func TestParseParameter(t *testing.T) {
	text := "ВЫБРАТЬ Ссылка ИЗ Справочник.Номенклатура ГДЕ Код = &Код"
	q, _ := Parse(text)
	if len(q.Parameters) != 1 || q.Parameters[0].Name != "Код" {
		t.Fatalf("Parameters = %+v, ожидался один параметр Код", q.Parameters)
	}
	// span параметра обязан указывать ровно на "&Код" в тексте.
	want := "&Код"
	got := text[q.Parameters[0].Span.StartByte:q.Parameters[0].Span.EndByte]
	if got != want {
		t.Errorf("span параметра указывает на %q, ожидалось %q", got, want)
	}
}

// TestParseVirtualTableAllFiveKinds — все пять видов виртуальных таблиц
// распознаются, параметры виртуальной таблицы извлекаются в Query.Parameters
// (не путаются с обычными полями списка выборки).
func TestParseVirtualTableAllFiveKinds(t *testing.T) {
	cases := []struct {
		suffix string
		want   string
	}{
		{"Остатки", "Остатки"},
		{"Обороты", "Обороты"},
		{"ОстаткиИОбороты", "ОстаткиИОбороты"},
		{"СрезПоследних", "СрезПоследних"},
		{"СрезПервых", "СрезПервых"},
	}
	for _, c := range cases {
		t.Run(c.suffix, func(t *testing.T) {
			text := "ВЫБРАТЬ Сумма ИЗ РегистрНакопления.Продажи." + c.suffix + "(&Дата, Номенклатура В (&Список))"
			q, _ := Parse(text)
			if len(q.Tables) != 1 {
				t.Fatalf("Tables = %+v, ожидалась одна таблица", q.Tables)
			}
			tbl := q.Tables[0]
			if tbl.Kind != TableVirtual || tbl.VirtualKind != c.want {
				t.Errorf("Table = %+v, ожидалось Kind=virtual VirtualKind=%s", tbl, c.want)
			}
			wantName := "РегистрНакопления.Продажи." + c.suffix
			if tbl.Name != wantName {
				t.Errorf("Table.Name = %q, ожидалось %q", tbl.Name, wantName)
			}
			// параметр виртуальной таблицы обязан попасть в Parameters...
			names := paramNames(q.Parameters)
			if !containsStr(names, "Дата") || !containsStr(names, "Список") {
				t.Errorf("Parameters = %+v, ожидались Дата и Список", q.Parameters)
			}
			// ...и "Номенклатура" внутри параметров виртуальной таблицы не
			// обязана попасть в Fields (список выборки — только "Сумма").
			if len(q.Fields) != 1 || q.Fields[0].Name != "Сумма" {
				t.Errorf("Fields = %+v, ожидалось ровно одно поле Сумма — параметры ВТ не поля", q.Fields)
			}
		})
	}
}

func paramNames(params []Parameter) []string {
	out := make([]string, len(params))
	for i, p := range params {
		out[i] = p.Name
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestParseTempTableLinking — ПОМЕСТИТЬ ВТ_X определяет временную таблицу,
// последующее ИЗ ВТ_X связывается с этим определением и таблица получает
// Kind=temp.
func TestParseTempTableLinking(t *testing.T) {
	text := `ВЫБРАТЬ Ссылка ПОМЕСТИТЬ ВТ_Товары ИЗ Справочник.Номенклатура
;
ВЫБРАТЬ Ссылка ИЗ ВТ_Товары`
	q, _ := Parse(text)
	if len(q.TempTables) != 1 {
		t.Fatalf("TempTables = %+v, ожидалась одна временная таблица", q.TempTables)
	}
	tt := q.TempTables[0]
	if tt.Name != "ВТ_Товары" {
		t.Errorf("TempTables[0].Name = %q, ожидалось ВТ_Товары", tt.Name)
	}
	if len(tt.UsedAt) != 1 {
		t.Fatalf("TempTables[0].UsedAt = %+v, ожидалось одно использование", tt.UsedAt)
	}

	// Таблица во второй ВЫБРАТЬ...ИЗ обязана иметь Kind=temp, а не base.
	var found bool
	for _, tbl := range q.Tables {
		if tbl.Name == "ВТ_Товары" {
			found = true
			if tbl.Kind != TableTemp {
				t.Errorf("Table(ВТ_Товары).Kind = %q, ожидалось temp", tbl.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("ВТ_Товары не найдена в Tables: %+v", q.Tables)
	}
}

// TestParseRussianEnglishEquivalence — пара эквивалентных запросов на русском
// и английском синтаксисе даёт структурно одинаковый результат: те же имена
// таблиц/полей/параметров, тот же Kind, та же Staticity.
func TestParseRussianEnglishEquivalence(t *testing.T) {
	ru := `ВЫБРАТЬ РАЗЛИЧНЫЕ
		Ном.Ссылка КАК Ссылка,
		Ном.Наименование КАК Название
	ИЗ
		Справочник.Номенклатура КАК Ном
	ЛЕВОЕ СОЕДИНЕНИЕ РегистрСведений.ЦеныНоменклатуры.СрезПоследних(&Дата, ) КАК Цены
	ПО Цены.Номенклатура = Ном.Ссылка
	ГДЕ
		Ном.Код = &Код
	УПОРЯДОЧИТЬ ПО
		Название`
	en := `SELECT DISTINCT
		Ном.Ссылка AS Ссылка,
		Ном.Наименование AS Название
	FROM
		Справочник.Номенклатура AS Ном
	LEFT JOIN РегистрСведений.ЦеныНоменклатуры.СрезПоследних(&Дата, ) AS Цены
	ON Цены.Номенклатура = Ном.Ссылка
	WHERE
		Ном.Код = &Код
	ORDER BY
		Название`

	ruQ, ruDiags := Parse(ru)
	enQ, enDiags := Parse(en)
	if len(ruDiags) != 0 || len(enDiags) != 0 {
		t.Fatalf("диагностики неожиданны: ru=%+v en=%+v", ruDiags, enDiags)
	}

	if ruQ.Staticity != enQ.Staticity {
		t.Errorf("Staticity: ru=%q en=%q", ruQ.Staticity, enQ.Staticity)
	}

	if len(ruQ.Fields) != len(enQ.Fields) {
		t.Fatalf("число полей: ru=%d en=%d (ru=%+v en=%+v)", len(ruQ.Fields), len(enQ.Fields), ruQ.Fields, enQ.Fields)
	}
	for i := range ruQ.Fields {
		rf, ef := ruQ.Fields[i], enQ.Fields[i]
		if rf.Qualifier != ef.Qualifier || rf.Name != ef.Name || rf.Alias != ef.Alias {
			t.Errorf("поле %d: ru=%+v en=%+v", i, rf, ef)
		}
	}

	if len(ruQ.Tables) != len(enQ.Tables) {
		t.Fatalf("число таблиц: ru=%d en=%d (ru=%+v en=%+v)", len(ruQ.Tables), len(enQ.Tables), ruQ.Tables, enQ.Tables)
	}
	for i := range ruQ.Tables {
		rt, et := ruQ.Tables[i], enQ.Tables[i]
		if rt.Name != et.Name || rt.Alias != et.Alias || rt.Kind != et.Kind || rt.VirtualKind != et.VirtualKind {
			t.Errorf("таблица %d: ru=%+v en=%+v", i, rt, et)
		}
	}

	if len(ruQ.Parameters) != len(enQ.Parameters) || len(ruQ.Parameters) != 2 {
		t.Fatalf("Parameters: ru=%+v en=%+v, ожидалось по 2", ruQ.Parameters, enQ.Parameters)
	}
}

// TestParseVirtualTableRussianEnglishEquivalence — золотой тест на пару:
// вызов виртуальной таблицы на русском (Остатки) и её английский эквивалент
// (Balance) дают одинаковую классификацию (Kind, VirtualKind), кроме самого
// имени таблицы. Найдено ревью: до фикса английский суффикс (Balance,
// Turnovers, BalanceAndTurnovers, SliceLast, SliceFirst) молча терялся —
// таблица классифицировалась как TableBase без VirtualKind.
func TestParseVirtualTableRussianEnglishEquivalence(t *testing.T) {
	cases := []struct {
		ruSuffix, enSuffix, wantKind string
	}{
		{"Остатки", "Balance", "Остатки"},
		{"Обороты", "Turnovers", "Обороты"},
		{"ОстаткиИОбороты", "BalanceAndTurnovers", "ОстаткиИОбороты"},
		{"СрезПоследних", "SliceLast", "СрезПоследних"},
		{"СрезПервых", "SliceFirst", "СрезПервых"},
	}
	for _, c := range cases {
		t.Run(c.wantKind, func(t *testing.T) {
			ru := "ВЫБРАТЬ Сумма ИЗ РегистрНакопления.Продажи." + c.ruSuffix + "(&Дата,)"
			en := "SELECT Сумма FROM AccumulationRegister.Продажи." + c.enSuffix + "(&Дата,)"

			ruQ, ruDiags := Parse(ru)
			enQ, enDiags := Parse(en)
			if len(ruDiags) != 0 || len(enDiags) != 0 {
				t.Fatalf("диагностики неожиданны: ru=%+v en=%+v", ruDiags, enDiags)
			}
			if len(ruQ.Tables) != 1 || len(enQ.Tables) != 1 {
				t.Fatalf("Tables: ru=%+v en=%+v, ожидалось по одной таблице", ruQ.Tables, enQ.Tables)
			}
			rt, et := ruQ.Tables[0], enQ.Tables[0]
			if rt.Kind != TableVirtual || et.Kind != TableVirtual {
				t.Fatalf("Kind: ru=%q en=%q, ожидалось virtual у обоих", rt.Kind, et.Kind)
			}
			if rt.VirtualKind != c.wantKind || et.VirtualKind != c.wantKind {
				t.Errorf("VirtualKind: ru=%q en=%q, ожидалось %q у обоих (тот же смысл на разных языках)",
					rt.VirtualKind, et.VirtualKind, c.wantKind)
			}
			if et.VirtualKind != rt.VirtualKind {
				t.Errorf("английский и русский синтаксис дали разную классификацию: ru=%+v en=%+v", rt, et)
			}
		})
	}
}

// TestParseStaticityLevels — static/partial/dynamic различаются по наличию
// GapMarker в тексте, а confidence у нестатичных строго меньше единицы
// (проверено через сам инвариант domain.Provenance.Validate, а не на глаз).
func TestParseStaticityLevels(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		q, _ := Parse("ВЫБРАТЬ Ссылка ИЗ Справочник.Номенклатура")
		if q.Staticity != StaticityStatic {
			t.Errorf("Staticity = %q, ожидалось static", q.Staticity)
		}
		if q.Confidence != domain.ConfidenceExact {
			t.Errorf("Confidence = %v, ожидалось %v", q.Confidence, domain.ConfidenceExact)
		}
	})

	t.Run("partial", func(t *testing.T) {
		text := "ВЫБРАТЬ Ссылка ИЗ Справочник." + GapMarker + " ГДЕ Код = &Код"
		q, _ := Parse(text)
		if q.Staticity != StaticityPartial {
			t.Errorf("Staticity = %q, ожидалось partial", q.Staticity)
		}
		if q.Confidence >= domain.ConfidenceExact {
			t.Errorf("Confidence = %v, ожидалось < %v у partial", q.Confidence, domain.ConfidenceExact)
		}
		if len(q.Parameters) != 1 {
			t.Errorf("Parameters = %+v, ожидался один параметр (частично разобрано)", q.Parameters)
		}
	})

	t.Run("dynamic пустой текст", func(t *testing.T) {
		q, _ := Parse("")
		if q.Staticity != StaticityDynamic {
			t.Errorf("Staticity = %q, ожидалось dynamic", q.Staticity)
		}
		if q.Confidence >= domain.ConfidenceExact {
			t.Errorf("Confidence = %v, ожидалось < %v у dynamic", q.Confidence, domain.ConfidenceExact)
		}
	})

	t.Run("dynamic только заглушка", func(t *testing.T) {
		q, _ := Parse(GapMarker)
		if q.Staticity != StaticityDynamic {
			t.Errorf("Staticity = %q, ожидалось dynamic (в тексте нет ничего, кроме заглушки)", q.Staticity)
		}
		if q.Confidence >= domain.ConfidenceExact {
			t.Errorf("Confidence = %v, ожидалось < %v у dynamic", q.Confidence, domain.ConfidenceExact)
		}
	})

	// Инвариант архитектуры (§14): эвристический/неполный факт не может иметь
	// confidence = 1 — это утверждение самого типа domain.Provenance, вызываем
	// его явно, а не полагаемся, что где-то по цепочке это подразумевается.
	t.Run("Provenance.Validate принимает результат partial и dynamic", func(t *testing.T) {
		partial, _ := Parse("ВЫБРАТЬ Ссылка ИЗ " + GapMarker)
		dynamic, _ := Parse("")
		for _, q := range []*Query{partial, dynamic} {
			if err := q.Provenance.Validate(q.Confidence); err != nil {
				t.Errorf("Provenance.Validate(%v) для staticity=%s: %v", q.Confidence, q.Staticity, err)
			}
		}
	})
}

// TestParseJoinAndSubquery — соединение и вложенный запрос как источник ИЗ
// (архитектура §14: "вложенные запросы" — источник таблицы).
func TestParseJoinAndSubquery(t *testing.T) {
	text := `ВЫБРАТЬ
		Продажи.Номенклатура
	ИЗ
		(ВЫБРАТЬ
			Отбор.Номенклатура КАК Номенклатура
		ИЗ
			Справочник.Номенклатура КАК Отбор) КАК Продажи
	ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Склады КАК Склады
	ПО Склады.Ссылка = Продажи.Номенклатура`
	q, _ := Parse(text)

	var sub, склад bool
	for _, tbl := range q.Tables {
		if tbl.Kind == TableSubquery && tbl.Alias == "Продажи" {
			sub = true
		}
		if tbl.Name == "Справочник.Склады" && tbl.Alias == "Склады" {
			склад = true
		}
	}
	if !sub {
		t.Errorf("Tables = %+v, ожидался подзапрос с алиасом Продажи", q.Tables)
	}
	if !склад {
		t.Errorf("Tables = %+v, ожидалась Справочник.Склады через СОЕДИНЕНИЕ", q.Tables)
	}

	// Таблица внутри вложенного запроса обязана быть найдена (рекурсивный
	// разбор источника-подзапроса), а не потеряна.
	var innerFound bool
	for _, tbl := range q.Tables {
		if tbl.Name == "Справочник.Номенклатура" && tbl.Alias == "Отбор" {
			innerFound = true
		}
	}
	if !innerFound {
		t.Errorf("Tables = %+v, таблица внутри вложенного запроса потеряна", q.Tables)
	}
}

// TestParseAllListedSections — все секции, перечисленные в тикете
// (ВЫБРАТЬ/ИЗ/ГДЕ/СГРУППИРОВАТЬ ПО/ИМЕЮЩИЕ/УПОРЯДОЧИТЬ ПО/ИТОГИ/ОБЪЕДИНИТЬ),
// присутствуют в одном тексте одновременно и не портят друг друга: слова из
// ГДЕ/СГРУППИРОВАТЬ/ИМЕЮЩИЕ/УПОРЯДОЧИТЬ/ИТОГИ не просачиваются в список полей
// как ложные обращения к полям, а поля до УПОРЯДОЧИТЬ разобраны верно.
func TestParseAllListedSections(t *testing.T) {
	text := `ВЫБРАТЬ
		Продажи.Номенклатура КАК Номенклатура,
		СУММА(Продажи.Сумма) КАК Сумма
	ИЗ
		РегистрНакопления.Продажи.Обороты(&Нач, &Кон) КАК Продажи
	ГДЕ
		Продажи.Организация = &Организация
	СГРУППИРОВАТЬ ПО
		Продажи.Номенклатура
	ИМЕЮЩИЕ
		СУММА(Продажи.Сумма) > 0
	ОБЪЕДИНИТЬ ВСЕ
	ВЫБРАТЬ
		Возвраты.Номенклатура,
		Возвраты.Сумма
	ИЗ
		РегистрНакопления.Возвраты.Обороты(&Нач, &Кон) КАК Возвраты
	ИТОГИ
		СУММА(Сумма)
	ПО
		Номенклатура
	УПОРЯДОЧИТЬ ПО
		Номенклатура`

	q, diags := Parse(text)
	if len(diags) != 0 {
		t.Fatalf("диагностики неожиданны: %+v", diags)
	}
	if q.Staticity != StaticityStatic {
		t.Fatalf("Staticity = %q, ожидалось static (текст целиком литерал)", q.Staticity)
	}

	// Ровно два ВЫБРАТЬ дали в сумме четыре поля списка выборки — ни слова из
	// ГДЕ/СГРУППИРОВАТЬ/ИМЕЮЩИЕ/ИТОГИ/УПОРЯДОЧИТЬ не попали как лишние поля.
	wantFields := map[string]bool{"Номенклатура": false, "Сумма": false}
	if len(q.Fields) < 4 {
		t.Fatalf("Fields = %+v, ожидалось не меньше 4 полей списка выборки двух ВЫБРАТЬ", q.Fields)
	}
	for _, f := range q.Fields {
		if _, known := wantFields[f.Name]; known {
			wantFields[f.Name] = true
		}
		// Слово "Организация" стоит только в ГДЕ (после &) — оно не должно
		// попасть в список полей ни как имя, ни как квалификатор.
		if f.Name == "Организация" || f.Qualifier == "Организация" {
			t.Errorf("поле из ГДЕ просочилось в список выборки: %+v", f)
		}
	}
	for name, seen := range wantFields {
		if !seen {
			t.Errorf("ожидалось поле %q среди Fields=%+v", name, q.Fields)
		}
	}

	if len(q.Tables) != 2 {
		t.Fatalf("Tables = %+v, ожидалось 2 таблицы (по одной на каждый ВЫБРАТЬ)", q.Tables)
	}
	for _, tbl := range q.Tables {
		if tbl.Kind != TableVirtual || tbl.VirtualKind != "Обороты" {
			t.Errorf("Table = %+v, ожидался Kind=virtual VirtualKind=Обороты", tbl)
		}
	}

	names := paramNames(q.Parameters)
	for _, want := range []string{"Нач", "Кон", "Организация"} {
		if !containsStr(names, want) {
			t.Errorf("Parameters = %+v, ожидался параметр %s", q.Parameters, want)
		}
	}
}
