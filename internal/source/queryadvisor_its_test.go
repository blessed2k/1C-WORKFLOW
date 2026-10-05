package source

import (
	"strings"
	"testing"
)

// itsRuleCodes are the advisor codes that come from the ITS standards.
var itsRuleCodes = []string{
	"SumAsCount", "FullOuterJoin", "UnionWithoutAll", "NestedJoin", "CompositeDereference",
	"TopWithAutoOrder", "OrAcrossFields", "FunctionOnFilterField", "VirtualTableComplexParams",
	"VirtualTableFilterInWhere",
}

// TestAdviseITSRules checks every rule taken from the ITS standards from both
// sides: the text that must be reported and the nearest text that must not.
func TestAdviseITSRules(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		query string
		want  bool
	}{
		{"sum of one", "SumAsCount",
			`ВЫБРАТЬ СУММА(1) КАК Количество ИЗ Справочник.Товары КАК Т`, true},
		{"sum of one with spaces", "SumAsCount",
			`ВЫБРАТЬ СУММА( 1 ) КАК Количество ИЗ Справочник.Товары КАК Т`, true},
		{"conditional count over literal", "SumAsCount",
			`ВЫБРАТЬ СУММА(ВЫБОР КОГДА Т.ЭтоГруппа ТОГДА 1 ИНАЧЕ 0 КОНЕЦ) КАК Групп ИЗ Справочник.Товары КАК Т`, true},
		{"conditional count with explicit precision", "SumAsCount",
			`ВЫБРАТЬ СУММА(ВЫБОР КОГДА Т.ЭтоГруппа ТОГДА ВЫРАЗИТЬ(1 КАК ЧИСЛО(17, 0)) ИНАЧЕ 0 КОНЕЦ) КАК Групп ИЗ Справочник.Товары КАК Т`, false},
		{"sum of a field", "SumAsCount",
			`ВЫБРАТЬ СУММА(Т.Количество) КАК Количество ИЗ РегистрНакопления.Продажи КАК Т`, false},
		{"conditional sum of a field", "SumAsCount",
			`ВЫБРАТЬ СУММА(ВЫБОР КОГДА Т.Активность ТОГДА Т.Количество ИНАЧЕ 0 КОНЕЦ) КАК К ИЗ РегистрНакопления.Продажи КАК Т`, false},
		{"case expression as a signed factor", "SumAsCount",
			`ВЫБРАТЬ СУММА(ВЫБОР КОГДА Т.Приход ТОГДА 1 ИНАЧЕ -1 КОНЕЦ * Т.Количество) КАК К ИЗ РегистрНакопления.Продажи КАК Т`, false},
		{"literal multiplied by a field", "SumAsCount",
			`ВЫБРАТЬ СУММА(ВЫБОР КОГДА Т.Приход ТОГДА 1 * Т.Количество ИНАЧЕ 0 КОНЕЦ) КАК К ИЗ РегистрНакопления.Продажи КАК Т`, false},
		{"commented out", "SumAsCount",
			"ВЫБРАТЬ\n// СУММА(1) КАК Количество,\nТ.Ссылка ИЗ Справочник.Товары КАК Т", false},
		{"count function", "SumAsCount",
			`ВЫБРАТЬ КОЛИЧЕСТВО(*) КАК Количество ИЗ Справочник.Товары КАК Т`, false},

		{"full join", "FullOuterJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А ПОЛНОЕ СОЕДИНЕНИЕ Справочник.Товары КАК Б ПО А.Ссылка = Б.Ссылка`, true},
		{"full outer join", "FullOuterJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А ПОЛНОЕ ВНЕШНЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК Б ПО А.Ссылка = Б.Ссылка`, true},
		{"left join", "FullOuterJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А ЛЕВОЕ СОЕДИНЕНИЕ Справочник.Товары КАК Б ПО А.Ссылка = Б.Ссылка`, false},
		{"field named like the keyword", "FullOuterJoin",
			`ВЫБРАТЬ А.НаименованиеПолное ИЗ Справочник.Товары КАК А ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК Б ПО А.Ссылка = Б.Ссылка`, false},

		{"temporary table ending with the keyword", "FullOuterJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ ВТ_Полное СОЕДИНЕНИЕ Справочник.Товары КАК Б ПО ИСТИНА`, false},
		{"alias ending with the keyword", "FullOuterJoin",
			`ВЫБРАТЬ Б.Ссылка ИЗ Справочник.Товары КАК НеПолное СОЕДИНЕНИЕ Справочник.Товары КАК Б ПО ИСТИНА`, false},
		{"full join in lower case after a line break", "FullOuterJoin",
			"ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А\nполное соединение Справочник.Товары КАК Б ПО А.Ссылка = Б.Ссылка", true},

		{"union", "UnionWithoutAll",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А ОБЪЕДИНИТЬ ВЫБРАТЬ Б.Ссылка ИЗ Справочник.Товары КАК Б`, true},
		{"union all", "UnionWithoutAll",
			"ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А ОБЪЕДИНИТЬ ВСЕ\nВЫБРАТЬ Б.Ссылка ИЗ Справочник.Товары КАК Б", false},
		{"union all then union", "UnionWithoutAll",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А ОБЪЕДИНИТЬ ВСЕ ВЫБРАТЬ Б.Ссылка ИЗ Справочник.Товары КАК Б ОБЪЕДИНИТЬ ВЫБРАТЬ В.Ссылка ИЗ Справочник.Товары КАК В`, true},

		{"nested joins", "NestedJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А
				ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.Продажи КАК Б
					ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.ТоварыНаСкладах КАК В
					ПО Б.Товар = В.Товар
				ПО А.Ссылка = Б.Товар`, true},
		{"sequential joins", "NestedJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А
				ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.Продажи КАК Б
				ПО А.Ссылка = Б.Товар
				ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.ТоварыНаСкладах КАК В
				ПО А.Ссылка = В.Товар`, false},
		{"join inside a joined subquery", "NestedJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А
				ЛЕВОЕ СОЕДИНЕНИЕ (ВЫБРАТЬ Б.Товар ИЗ РегистрНакопления.Продажи КАК Б
					ВНУТРЕННЕЕ СОЕДИНЕНИЕ РегистрНакопления.ТоварыНаСкладах КАК В ПО Б.Товар = В.Товар) КАК Г
				ПО А.Ссылка = Г.Товар`, false},
		{"join inside virtual table parameters", "NestedJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А
				ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.ТоварыНаСкладах.Остатки(, Товар В (ВЫБРАТЬ Б.Товар ИЗ РегистрНакопления.Продажи КАК Б
					ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК В ПО Б.Товар = В.Ссылка)) КАК О
				ПО А.Ссылка = О.Товар`, false},
		{"field ending with the keyword letters", "NestedJoin",
			`ВЫБРАТЬ А.Ссылка ИЗ Справочник.Товары КАК А
				ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.Продажи КАК Депо
				ПО А.Ссылка = Депо.Товар
				ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.ТоварыНаСкладах КАК В
				ПО А.Ссылка = В.Товар`, false},

		{"recorder attribute through a dot", "CompositeDereference",
			`ВЫБРАТЬ Т.Регистратор.Дата КАК Дата ИЗ РегистрНакопления.Продажи КАК Т`, true},
		{"recorder itself", "CompositeDereference",
			`ВЫБРАТЬ Т.Регистратор КАК Регистратор ИЗ РегистрНакопления.Продажи КАК Т`, false},
		{"recorder cast to one type", "CompositeDereference",
			`ВЫБРАТЬ ВЫРАЗИТЬ(Т.Регистратор КАК Документ.Реализация).Дата КАК Дата ИЗ РегистрНакопления.Продажи КАК Т`, false},

		{"top with auto order", "TopWithAutoOrder",
			"ВЫБРАТЬ ПЕРВЫЕ 10 Т.Ссылка ИЗ Справочник.Товары КАК Т\nАВТОУПОРЯДОЧИВАНИЕ", true},
		{"top with explicit order", "TopWithAutoOrder",
			`ВЫБРАТЬ ПЕРВЫЕ 10 Т.Ссылка ИЗ Справочник.Товары КАК Т УПОРЯДОЧИТЬ ПО Т.Наименование`, false},
		{"top inside an ordered subquery", "TopWithAutoOrder",
			"ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Ссылка В (ВЫБРАТЬ ПЕРВЫЕ 5 П.Ссылка ИЗ Справочник.Товары КАК П УПОРЯДОЧИТЬ ПО П.Артикул)\nАВТОУПОРЯДОЧИВАНИЕ", false},
		{"top and auto order in different statements", "TopWithAutoOrder",
			"ВЫБРАТЬ ПЕРВЫЕ 10 Т.Ссылка ПОМЕСТИТЬ ВТ ИЗ Справочник.Товары КАК Т УПОРЯДОЧИТЬ ПО Т.Наименование;\nВЫБРАТЬ В.Ссылка ИЗ ВТ КАК В АВТОУПОРЯДОЧИВАНИЕ", false},

		{"or across fields", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Артикул = &А ИЛИ Т.Наименование = &Н`, true},
		{"and binds tighter than or", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Артикул = &А И Т.ЭтоГруппа ИЛИ Т.Наименование = &Н`, true},
		{"or over one field", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Артикул = &А ИЛИ Т.Артикул = &Б`, false},
		{"or inside an additional condition", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Артикул = &А И (Т.ЭтоГруппа ИЛИ Т.Наименование = &Н)`, false},
		{"parameter switch", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ &ВсеТовары ИЛИ Т.Артикул = &А`, false},
		{"or inside a case expression", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ ВЫБОР КОГДА Т.ЭтоГруппа ИЛИ Т.ПометкаУдаления ТОГДА ЛОЖЬ ИНАЧЕ ИСТИНА КОНЕЦ`, false},
		{"field ending with the keyword letters in a filter", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Оплатили = &А И Т.Артикул = &Б`, false},
		{"or inside a string literal", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Наименование = "да ИЛИ нет" И Т.Артикул = &А`, false},
		{"parenthesis inside a string literal", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Наименование ПОДОБНО "(%" И Т.Артикул = &А УПОРЯДОЧИТЬ ПО Т.Артикул ИЛИ Т.Наименование`, false},
		{"type name in another letter case", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Ссылка ССЫЛКА справочник.Товары ИЛИ Т.Ссылка = &С`, false},
		{"or over one unqualified field compared with system enumeration values", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ РегистрНакопления.Продажи КАК Т ГДЕ ВидДвижения = ЗНАЧЕНИЕ(ВидДвиженияНакопления.Приход) ИЛИ ВидДвижения = ЗНАЧЕНИЕ(ВидДвиженияНакопления.Расход)`, false},
		{"or across fields with a predefined value", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ РегистрНакопления.Продажи КАК Т ГДЕ Т.ВидДвижения = ЗНАЧЕНИЕ(ВидДвиженияНакопления.Приход) ИЛИ Т.Склад = &Склад`, true},
		{"or in a data composition filter", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т {ГДЕ Т.Артикул = &А ИЛИ Т.Наименование = &Н}`, false},
		{"or in a subquery filter", "OrAcrossFields",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ Т.Ссылка В (ВЫБРАТЬ П.Ссылка ИЗ Справочник.Товары КАК П ГДЕ П.Артикул = &А ИЛИ П.Наименование = &Н)`, true},

		{"date function compared with a literal", "FunctionOnFilterField",
			`ВЫБРАТЬ Т.Ссылка ИЗ Документ.Реализация КАК Т ГДЕ МЕСЯЦ(Т.Дата) = 1`, true},
		{"period start compared with a parameter", "FunctionOnFilterField",
			`ВЫБРАТЬ Т.Ссылка ИЗ Документ.Реализация КАК Т ГДЕ НАЧАЛОПЕРИОДА(Т.Дата, ДЕНЬ) = &Дата`, true},
		{"substring from the first character", "FunctionOnFilterField",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ ПОДСТРОКА(Т.Артикул, 1, 3) = "АБВ"`, true},
		{"substring from the middle", "FunctionOnFilterField",
			`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т ГДЕ ПОДСТРОКА(Т.Артикул, 5, 2) = "50"`, false},
		{"date functions correlating two tables", "FunctionOnFilterField",
			`ВЫБРАТЬ Т.Ссылка ИЗ Документ.Реализация КАК Т ВНУТРЕННЕЕ СОЕДИНЕНИЕ Документ.Реализация КАК Б ПО Т.Ссылка = Б.Ссылка ГДЕ ГОД(Т.Дата) = ГОД(Б.Дата)`, false},
		{"field compared directly", "FunctionOnFilterField",
			`ВЫБРАТЬ Т.Ссылка ИЗ Документ.Реализация КАК Т ГДЕ Т.Дата МЕЖДУ &Начало И &Конец`, false},
		{"function in the select list of a nested subquery", "FunctionOnFilterField",
			`ВЫБРАТЬ Т.Ссылка ИЗ Документ.Реализация КАК Т ГДЕ Т.Ссылка В (ВЫБРАТЬ ВЫБОР КОГДА МЕСЯЦ(П.Дата) = 1 ТОГДА П.Ссылка КОНЕЦ ИЗ Документ.Реализация КАК П)`, false},
		{"function in the select list only", "FunctionOnFilterField",
			`ВЫБРАТЬ МЕСЯЦ(Т.Дата) = 1 КАК Январь ИЗ Документ.Реализация КАК Т`, false},

		{"dereference in virtual table parameters", "VirtualTableComplexParams",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар.Родитель = &Группа) КАК О`, true},
		{"subquery with a join in virtual table parameters", "VirtualTableComplexParams",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(, Товар В (ВЫБРАТЬ Б.Товар ИЗ РегистрНакопления.Продажи КАК Б
				ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.Товары КАК В ПО Б.Товар = В.Ссылка)) КАК О`, true},
		{"simple conditions", "VirtualTableComplexParams",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар = &Товар И Товар В (&Список)) КАК О`, false},
		{"subquery over one temporary table", "VirtualTableComplexParams",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(, Товар В (ВЫБРАТЬ Т.Товар ИЗ ВТТовары КАК Т)) КАК О`, false},
		{"predefined value and type check", "VirtualTableComplexParams",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(, Товар <> ЗНАЧЕНИЕ(Справочник.Товары.ПустаяСсылка) И Товар ССЫЛКА Справочник.Товары) КАК О`, false},
		{"type name in another letter case", "VirtualTableComplexParams",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар ССЫЛКА справочник.Товары) КАК О`, false},
		{"commented-out source", "VirtualTableComplexParams",
			"ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т\n// ЛЕВОЕ СОЕДИНЕНИЕ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар.Родитель = &Г) КАК О\n", false},
		{"data composition parameters", "VirtualTableComplexParams",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки({&Дата}, {Товар.*}) КАК О`, false},

		{"dimension filtered in where", "VirtualTableFilterInWhere",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата) КАК О ГДЕ О.Товар = &Т`, true},
		{"dimension list filtered in where", "VirtualTableFilterInWhere",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата) КАК О ГДЕ О.Товар В ИЕРАРХИИ (&Группа)`, true},
		{"dimension filtered in parameters", "VirtualTableFilterInWhere",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар = &Т) КАК О ГДЕ О.КоличествоОстаток > 0`, false},
		{"resource filtered in where", "VirtualTableFilterInWhere",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата) КАК О ГДЕ О.КоличествоОстаток > 0`, false},
		{"dimension correlated with another table", "VirtualTableFilterInWhere",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата) КАК О, Справочник.Товары КАК Т ГДЕ О.Товар = Т.Ссылка`, false},
		{"alias reused by another statement of the batch", "VirtualTableFilterInWhere",
			"ВЫБРАТЬ О.Товар ПОМЕСТИТЬ ВТ ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата) КАК О;\n" +
				"ВЫБРАТЬ О.Товар ИЗ ВТ КАК О ГДЕ О.Товар = &Т", false},
		{"no parameters at all is another rule", "VirtualTableFilterInWhere",
			`ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки КАК О ГДЕ О.Товар = &Т`, false},
	}
	for _, tc := range cases {
		t.Run(tc.code+"/"+tc.name, func(t *testing.T) {
			adv := advise(t, tc.query)
			_, got := adviceCodes(adv)[tc.code]
			if got != tc.want {
				t.Errorf("%s reported = %v, want %v; warnings: %+v", tc.code, got, tc.want, adv.Warnings)
			}
		})
	}
}

// Every ITS rule must name its standard: the agent checks a finding against the
// source, and a rule without a reference is an opinion.
func TestAdviseITSRulesNameTheStandard(t *testing.T) {
	adv := advise(t, `ВЫБРАТЬ ПЕРВЫЕ 5 СУММА(1) КАК К, Т.Регистратор.Дата КАК Д
		ИЗ РегистрНакопления.Продажи КАК Т
			ПОЛНОЕ СОЕДИНЕНИЕ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата, Товар.Родитель = &Г) КАК О
				ЛЕВОЕ СОЕДИНЕНИЕ Справочник.Товары КАК Спр
				ПО О.Товар = Спр.Ссылка
			ПО Т.Товар = О.Товар
		ГДЕ МЕСЯЦ(Т.Период) = 1 И Т.Товар = &Т ИЛИ О.Товар = &Т
		ОБЪЕДИНИТЬ
		ВЫБРАТЬ 1, 2 ИЗ Справочник.Товары КАК Спр
		АВТОУПОРЯДОЧИВАНИЕ`)
	got := adviceCodes(adv)
	// The fixture register has a single dimension, already taken by the parameters
	// above, so the ГДЕ rule gets a query of its own.
	whereOnly := advise(t, `ВЫБРАТЬ О.Товар ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Дата) КАК О ГДЕ О.Товар = &Т`)
	got["VirtualTableFilterInWhere"] = adviceCodes(whereOnly)["VirtualTableFilterInWhere"]
	for _, code := range itsRuleCodes {
		w, ok := got[code]
		ok = ok && w.Code != ""
		if !ok {
			t.Errorf("%s not reported; warnings: %+v", code, adv.Warnings)
			continue
		}
		if !strings.Contains(w.Message, "#std") {
			t.Errorf("%s: message does not name the standard: %q", code, w.Message)
		}
		if w.Suggestion == "" {
			t.Errorf("%s: no rewrite suggested", code)
		}
	}
}

func TestIndexOfWord(t *testing.T) {
	cases := []struct {
		name, text, word string
		want             int
	}{
		// The byte before a word that follows a Cyrillic letter is a continuation
		// byte: the boundary check must decode the whole rune, not that byte.
		{"tail of a Cyrillic identifier", "Т.Оплатили = 1", "ИЛИ", -1},
		{"end of a Cyrillic identifier", "СкладПО = 1", "ПО", -1},
		{"head of a longer identifier", "ИтогиПродаж = 5", "ИТОГИ", -1},
		{"standalone word", "А = 1 ИЛИ Б = 2", "ИЛИ", strings.Index("А = 1 ИЛИ Б = 2", "ИЛИ")},
		{"another letter case", "а = 1 или б = 2", "ИЛИ", strings.Index("а = 1 или б = 2", "или")},
		// Upper-casing ɐ (U+0250) makes it one byte longer; the offset must still
		// point into the original text.
		{"rune that grows when upper-cased", "ɐɐɐɐ СУММА(1)", "СУММА", strings.Index("ɐɐɐɐ СУММА(1)", "СУММА")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := indexOfWord(tc.text, tc.word, 0); got != tc.want {
				t.Errorf("indexOfWord(%q, %q) = %d, want %d", tc.text, tc.word, got, tc.want)
			}
		})
	}
}

// A rune whose upper-case form is longer used to shift the offsets the scanners
// slice the text with, and the advisor panicked.
func TestAdviseSurvivesRunesThatGrowWhenUpperCased(t *testing.T) {
	prefix := strings.Repeat("ɐ", 20)
	for _, q := range []string{
		prefix + ` СУММА(`,
		prefix + ` ОБЪЕДИНИТЬ`,
		prefix + ` СОЕДИНЕНИЕ`,
		prefix + ` ГДЕ Т.А = 1 ИЛИ Т.Б = 2`,
	} {
		advise(t, q)
	}
}

func TestMaskQuery(t *testing.T) {
	text := "ВЫБРАТЬ \"а; (б\"\" ИЛИ в\" КАК П // ГДЕ (\nИЗ Т"
	got := maskQuery(text)
	if len(got) != len(text) {
		t.Fatalf("length changed: %d -> %d", len(text), len(got))
	}
	for _, gone := range []string{";", "(", "ИЛИ", "ГДЕ", "//"} {
		if strings.Contains(got, gone) {
			t.Errorf("masked text still holds %q: %q", gone, got)
		}
	}
	for _, kept := range []string{"ВЫБРАТЬ \"", "\" КАК П", "\nИЗ Т"} {
		if !strings.Contains(got, kept) {
			t.Errorf("masked text lost %q: %q", kept, got)
		}
	}
}

func TestWhereClauses(t *testing.T) {
	got := whereClauses(`ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
		ГДЕ Т.Ссылка В (ВЫБРАТЬ П.Ссылка ИЗ Справочник.Товары КАК П ГДЕ П.Артикул = &А СГРУППИРОВАТЬ ПО П.Ссылка) И Т.ЭтоГруппа
		УПОРЯДОЧИТЬ ПО Т.Наименование`)
	if len(got) != 2 {
		t.Fatalf("clauses = %d, want 2: %q", len(got), got)
	}
	if !strings.Contains(got[0], "Т.ЭтоГруппа") || strings.Contains(got[0], "УПОРЯДОЧИТЬ") {
		t.Errorf("outer clause must run past the subquery and stop at УПОРЯДОЧИТЬ: %q", got[0])
	}
	if !strings.Contains(got[1], "П.Артикул") || strings.Contains(got[1], "СГРУППИРОВАТЬ") || strings.Contains(got[1], "ЭтоГруппа") {
		t.Errorf("inner clause must stop inside the subquery: %q", got[1])
	}
}

func TestQueryLiterals(t *testing.T) {
	module := "Процедура П()\n" +
		"\t// \"ВЫБРАТЬ 1 ИЗ Комментарий\"\n" +
		"\tЗапрос.Текст =\n" +
		"\t\"ВЫБРАТЬ\n" +
		"\t|\tТ.Ссылка КАК Ссылка\n" +
		"\t// пояснение между строками литерала\n" +
		"\t|ИЗ\n" +
		"\t|\tСправочник.Товары КАК Т\n" +
		"\t|ГДЕ Т.Наименование = \"\"Стол\"\"\";\n" +
		"\tСообщение = \"не запрос\";\n" +
		"КонецПроцедуры\n"
	got := queryLiterals(module)
	if len(got) != 1 {
		t.Fatalf("literals = %d, want 1: %q", len(got), got)
	}
	for _, want := range []string{"Т.Ссылка КАК Ссылка", "Справочник.Товары КАК Т", `Т.Наименование = "Стол"`} {
		if !strings.Contains(got[0], want) {
			t.Errorf("literal lacks %q: %q", want, got[0])
		}
	}
	if strings.Contains(got[0], "|") || strings.Contains(got[0], "пояснение") {
		t.Errorf("literal keeps continuation bars or the comment line: %q", got[0])
	}
}
