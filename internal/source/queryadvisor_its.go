package source

import (
	"regexp"
	"strings"
)

// Rules taken from the 1C development standards (its.1c.ru/db/v8std). Each one
// names its standard in the message so the finding can be checked against the
// source. They parse the text heuristically, like the rest of the advisor, and
// every rule was measured on a typical configuration before it was kept: a rule
// that fires on most queries carries no signal (TestRealDumpAdvisorRuleFrequency).
//
// Two findings of the standards are deliberately not rules here. СрезПоследних
// called with a date (#std708) is the normal way to read a value as of a
// document date, so the text alone does not tell a mistake from intent. Sorting
// by a field that may be NULL (#std412 п.1.2) needs the nullability of the
// field, which heuristic parsing does not give.
var (
	reSumOfOne = regexp.MustCompile(`(?i)СУММА\s*\(\s*1\s*\)`)
	// A branch of ВЫБОР whose whole result is the literal 1.
	reBranchOfOne = regexp.MustCompile(`(?i)(?:ТОГДА|ИНАЧЕ)\s+1\s+(?:ИНАЧЕ|КОГДА|КОНЕЦ)`)
	// The left boundary keeps a table or an alias that ends with the keyword
	// (ВТ_Полное СОЕДИНЕНИЕ ...) from reading as a full join.
	reFullOuterJoin = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])ПОЛНОЕ\s+(?:ВНЕШНЕЕ\s+)?СОЕДИНЕНИЕ`)
	// A dot after Регистратор dereferences a composite-type field: the register's
	// recorder is typed by every document that writes to it.
	// упрощение: only the field named Регистратор is recognised; other composite
	// fields need the attribute types from metadata (indexedFields reads the same XML).
	reRecorderDeref = regexp.MustCompile(`(?i)[\p{L}\d_]\.Регистратор\.([\p{L}\d_]+)`)
	// A function wrapped around a field and compared with a parameter or a literal,
	// in the two forms the standard shows a rewrite for: a date function, and
	// ПОДСТРОКА taken from the first character. A comparison with another field is
	// a correlation between tables, not a filter, and is left alone.
	reFieldFunction = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(?:(ГОД|КВАРТАЛ|МЕСЯЦ|ДЕНЬ|НАЧАЛОПЕРИОДА|КОНЕЦПЕРИОДА)\s*\(\s*[\p{L}\d_]+\.[\p{L}\d_]+[^()]*\)|(ПОДСТРОКА)\s*\(\s*[\p{L}\d_]+\.[\p{L}\d_]+\s*,\s*1\s*,[^()]*\))\s*(?:=|>=?|<=?)\s*(?:&|\d|"|ДАТАВРЕМЯ)`)
	reQualifiedName = regexp.MustCompile(`(?:^|[^\p{L}\d_&.])([\p{L}_][\p{L}\d_]*)\.([\p{L}_][\p{L}\d_]*)`)
	// reSourceName matches the table name that follows a join keyword.
	reSourceName = regexp.MustCompile(`^\s*[\p{L}\d_.]+`)
)

// whereEnders end a ГДЕ clause when met outside parentheses.
var whereEnders = append([]string{"ИМЕЮЩИЕ", "АВТОУПОРЯДОЧИВАНИЕ", "ДЛЯ"}, whereTerminators...)

// itsTextRules runs the standard-based rules that need only the query text.
func itsTextRules(text string) []AdviceItem {
	clean := maskQuery(text)
	var out []AdviceItem

	if sumAsCount(clean) {
		out = append(out, AdviceItem{
			Code: "SumAsCount", Severity: "medium",
			Message:    "Подсчёт количества через СУММА(1) (#std787): у литерала 1 малая разрядность, от 10 млн строк сумма переполняется.",
			Suggestion: "Считать КОЛИЧЕСТВО(*) или КОЛИЧЕСТВО(Поле); для условного счёта писать ВЫРАЗИТЬ(1 КАК ЧИСЛО(17, 0)) внутри ВЫБОР.",
		})
	}
	if reFullOuterJoin.MatchString(clean) {
		out = append(out, AdviceItem{
			Code: "FullOuterJoin", Severity: "medium",
			Message:    "ПОЛНОЕ СОЕДИНЕНИЕ (#std435): на PostgreSQL выполняется медленно, а вместе с табличной частью в списке выборки запрещено.",
			Suggestion: "Заменить на ОБЪЕДИНИТЬ ВСЕ двух выборок с группировкой по ключу, если результат при этом не меняется.",
		})
	}
	if unionWithoutAll(clean) {
		out = append(out, AdviceItem{
			Code: "UnionWithoutAll", Severity: "low",
			Message:    "ОБЪЕДИНИТЬ без ВСЕ (#std434): СУБД сворачивает одинаковые строки, даже когда их заведомо нет.",
			Suggestion: "Писать ОБЪЕДИНИТЬ ВСЕ; оставить ОБЪЕДИНИТЬ, только если свёртка одинаковых строк нужна по смыслу.",
		})
	}
	if nestedJoin(clean) {
		out = append(out, AdviceItem{
			Code: "NestedJoin", Severity: "high",
			Message:    "Вложенное соединение (#std655 п.3): второе СОЕДИНЕНИЕ стоит до условия ПО первого, для СУБД это соединение с подзапросом.",
			Suggestion: "Переписать на последовательные соединения (СОЕДИНЕНИЕ ... ПО ..., затем следующее СОЕДИНЕНИЕ ... ПО ...) и проверить, что результат прежний; иначе вынести во временную таблицу.",
		})
	}
	if m := reRecorderDeref.FindStringSubmatch(clean); m != nil {
		out = append(out, AdviceItem{
			Code: "CompositeDereference", Severity: "high", Field: "Регистратор." + m[1],
			Message:    "Обращение через точку от поля составного типа (#std654): Регистратор." + m[1] + " соединяет запрос с таблицами всех документов-регистраторов и их ограничениями доступа.",
			Suggestion: "Ограничить тип: ВЫРАЗИТЬ(Т.Регистратор КАК Документ.Вид)." + m[1] + " вместе с условием Т.Регистратор ССЫЛКА Документ.Вид, либо хранить значение реквизитом регистра.",
		})
	}
	// АВТОУПОРЯДОЧИВАНИЕ orders the outer query, so only a ПЕРВЫЕ of that same
	// query counts: one inside a subquery has an order of its own.
	for _, stmt := range strings.Split(clean, ";") {
		if topLevelWord(stmt, "ПЕРВЫЕ") && topLevelWord(stmt, "АВТОУПОРЯДОЧИВАНИЕ") {
			out = append(out, AdviceItem{
				Code: "TopWithAutoOrder", Severity: "medium",
				Message:    "ПЕРВЫЕ вместе с АВТОУПОРЯДОЧИВАНИЕ (#std412 п.3): какие строки попадут в выборку, зависит от полей, которые платформа выберет сама.",
				Suggestion: "Задать порядок явно через УПОРЯДОЧИТЬ ПО и убрать АВТОУПОРЯДОЧИВАНИЕ.",
			})
			break
		}
	}

	var orFields, fnName string
	for _, where := range whereClauses(clean) {
		if orFields == "" {
			orFields = orAcrossFields(where)
		}
		if fnName == "" {
			// A nested subquery has a ГДЕ of its own; its select list is not a filter.
			if m := reFieldFunction.FindStringSubmatch(maskSubqueries(where)); m != nil {
				fnName = strings.ToUpper(m[1] + m[2])
			}
		}
	}
	if orFields != "" {
		out = append(out, AdviceItem{
			Code: "OrAcrossFields", Severity: "medium", Field: orFields,
			Message:    "ИЛИ по разным полям на верхнем уровне ГДЕ (#std658 п.2): условие не сводится к В, поиск по индексу невозможен.",
			Suggestion: "Разбить на запросы по каждому полю и соединить через ОБЪЕДИНИТЬ ВСЕ, если результат тот же, либо добавить основное условие через И, а ИЛИ оставить дополнительным.",
		})
	}
	if fnName != "" {
		out = append(out, AdviceItem{
			Code: "FunctionOnFilterField", Severity: "low", Field: fnName,
			Message:    "Функция " + fnName + " над полем в условии ГДЕ (#std658 п.3-4): если это основной отбор, индекс по полю не используется.",
			Suggestion: "Сравнивать само поле: ПОДСТРОКА(Поле, 1, N) = заменить на ПОДОБНО \"текст%\", функции дат на Поле МЕЖДУ &Начало И &Конец.",
		})
	}

	// The sources are parsed again from the masked text: a commented-out source
	// must not be reported.
	_, sources := parseSources(clean, &QueryAdvice{})
	for _, src := range sources {
		if !isVirtualTable(src.table) || !src.hasParams {
			continue
		}
		if item, ok := complexVirtualParams(src); ok {
			out = append(out, item)
		}
	}
	return out
}

// maskQuery blanks // comments and the contents of string literals, keeping the
// length of the text and the quotes themselves. The rules then never read a
// keyword, a parenthesis or a semicolon out of a comment or a literal, and byte
// offsets stay valid for the original text.
func maskQuery(text string) string {
	b := []byte(text)
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '"':
			for i++; i < len(b); i++ {
				if b[i] == '"' {
					if i+1 < len(b) && b[i+1] == '"' { // escaped quote
						b[i], b[i+1] = ' ', ' '
						i++
						continue
					}
					break
				}
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		}
	}
	return string(b)
}

// sumAsCount reports counting rows with СУММА over the literal 1, either
// directly or as a ВЫБОР expression whose branch is the bare literal. The
// literal has a short default precision, so the sum overflows on tables of 10M
// rows and more. A ВЫБОР that is only a factor (... КОНЕЦ * Т.Количество) sums a
// field and is left alone.
func sumAsCount(text string) bool {
	if reSumOfOne.MatchString(text) {
		return true
	}
	from := 0
	for {
		i := indexOfWord(text, "СУММА", from)
		if i < 0 {
			return false
		}
		from = i + len("СУММА")
		inner, _, ok := balancedParen(text[from:])
		if !ok {
			continue
		}
		arg := strings.TrimSpace(inner)
		if indexOfWord(arg, "ВЫБОР", 0) == 0 && endsWithWord(arg, "КОНЕЦ") &&
			reBranchOfOne.MatchString(arg) && indexOfWord(arg, "ВЫРАЗИТЬ", 0) < 0 {
			return true
		}
	}
}

// unionWithoutAll reports an ОБЪЕДИНИТЬ that is not followed by ВСЕ.
func unionWithoutAll(text string) bool {
	from := 0
	for {
		i := indexOfWord(text, "ОБЪЕДИНИТЬ", from)
		if i < 0 {
			return false
		}
		from = i + len("ОБЪЕДИНИТЬ")
		if indexOfWord(strings.TrimSpace(text[from:]), "ВСЕ", 0) != 0 {
			return true
		}
	}
}

// nestedJoin reports a join whose ПО comes only after the next СОЕДИНЕНИЕ: the
// inner join is then evaluated first, as a subquery would be. A parenthesised
// group right after the keyword (a subquery or virtual-table parameters) is
// skipped so that joins inside it do not count.
func nestedJoin(text string) bool {
	from := 0
	for {
		join := indexOfWord(text, "СОЕДИНЕНИЕ", from)
		if join < 0 {
			return false
		}
		from = join + len("СОЕДИНЕНИЕ")
		pos := from
		if _, end, ok := balancedParen(text[pos:]); ok {
			pos += end // join with a subquery
		} else if loc := reSourceName.FindStringIndex(text[pos:]); loc != nil {
			pos += loc[1]
			if _, end, ok := balancedParen(text[pos:]); ok {
				pos += end // virtual-table parameters
			}
		}
		on := indexOfWord(text, "ПО", pos)
		next := indexOfWord(text, "СОЕДИНЕНИЕ", pos)
		if next >= 0 && (on < 0 || next < on) {
			return true
		}
	}
}

// topLevelWord reports whether word occurs in s outside parentheses.
func topLevelWord(s, word string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && !letterOrDigitBefore(s, i) && hasWordAt(s, i, word) {
				return true
			}
		}
	}
	return false
}

// whereClauses returns the text of every ГДЕ clause, each cut where the clause
// ends: at a clause keyword outside parentheses, at the parenthesis that closes
// the enclosing subquery, or at the end of the statement. The data-composition
// form {ГДЕ ...} is not a filter of the query and is skipped.
func whereClauses(text string) []string {
	var out []string
	from := 0
	for {
		i := indexOfWord(text, "ГДЕ", from)
		if i < 0 {
			return out
		}
		from = i + len("ГДЕ")
		if strings.HasSuffix(strings.TrimRight(text[:i], " \t\r\n"), "{") {
			continue
		}
		out = append(out, clauseBody(text[from:]))
	}
}

// clauseBody cuts s at the first clause terminator met at parenthesis depth 0.
func clauseBody(s string) string {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return s[:i]
			}
		case ';', '{':
			if depth == 0 {
				return s[:i]
			}
		default:
			if depth != 0 || letterOrDigitBefore(s, i) {
				continue
			}
			for _, word := range whereEnders {
				if hasWordAt(s, i, word) {
					return s[:i]
				}
			}
		}
	}
	return s
}

// hasWordAt reports whether word starts at byte i of s and ends at a word
// boundary, case-insensitively. The caller checks the boundary before i.
func hasWordAt(s string, i int, word string) bool {
	if len(s)-i < len(word) || !strings.EqualFold(s[i:i+len(word)], word) {
		return false
	}
	return !letterOrDigitAt(s, i+len(word))
}

// endsWithWord reports whether s, ignoring trailing whitespace, ends with word
// as a whole word, case-insensitively.
func endsWithWord(s, word string) bool {
	s = strings.TrimRight(s, " \t\r\n")
	if len(s) < len(word) || !strings.EqualFold(s[len(s)-len(word):], word) {
		return false
	}
	return !letterOrDigitBefore(s, len(s)-len(word))
}

// orAcrossFields returns the fields joined by a top-level ИЛИ when they differ,
// or "" when the clause has no such ИЛИ. A top-level ИЛИ leaves no condition that
// narrows the selection on its own; an ИЛИ over one field reduces to В and is
// fine, and an operand without a field (a parameter switch) is not counted.
func orAcrossFields(where string) string {
	// The argument of ЗНАЧЕНИЕ(...) is a predefined value, not a field: a system
	// enumeration there (ВидДвиженияНакопления.Приход) has no table prefix to
	// tell it from a field.
	parts := splitTopLevelOr(maskValueLiterals(where))
	if len(parts) < 2 {
		return ""
	}
	var fields []string
	for _, part := range parts {
		for _, m := range reQualifiedName.FindAllStringSubmatch(part, -1) {
			if isTablePrefix(m[1]) {
				continue // a type or predefined-value name, not a field
			}
			if field := m[1] + "." + m[2]; !containsFold(fields, field) {
				fields = append(fields, field)
			}
			break // the first field of the operand is the one compared
		}
	}
	if len(fields) < 2 {
		return ""
	}
	return strings.Join(fields, ", ")
}

// splitTopLevelOr splits a condition at every ИЛИ that is outside parentheses
// and outside a ВЫБОР ... КОНЕЦ expression.
func splitTopLevelOr(s string) []string {
	var parts []string
	depth, cases, start := 0, 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 || letterOrDigitBefore(s, i) {
			continue
		}
		switch {
		case hasWordAt(s, i, "ВЫБОР"):
			cases++
		case hasWordAt(s, i, "КОНЕЦ"):
			if cases > 0 {
				cases--
			}
		case cases == 0 && hasWordAt(s, i, "ИЛИ"):
			parts = append(parts, s[start:i])
			start = i + len("ИЛИ")
		}
	}
	return append(parts, s[start:])
}

// complexVirtualParams reports virtual-table parameters that are more than
// simple conditions on dimensions: a subquery that joins tables, or a field
// reached through a dot. Both add tables to the subquery the virtual table
// expands into.
func complexVirtualParams(src source) (AdviceItem, bool) {
	vt := lastSegment(src.table)
	if indexOfWord(src.params, "ВЫБРАТЬ", 0) >= 0 && indexOfWord(src.params, "СОЕДИНЕНИЕ", 0) >= 0 {
		return AdviceItem{
			Code: "VirtualTableComplexParams", Severity: "high", Field: vt,
			Message:    "Подзапрос с соединением в параметрах виртуальной таблицы " + vt + " (#std657 п.2): условие усложняет запрос, в который она разворачивается.",
			Suggestion: "Подготовить значения во временной таблице и передать в параметры простое условие: Измерение В (ВЫБРАТЬ Т.Поле ИЗ ВТ КАК Т), без соединений и без ГДЕ.",
		}, true
	}
	for _, m := range reQualifiedName.FindAllStringSubmatch(dimensionConditions(src.params), -1) {
		if isTablePrefix(m[1]) {
			continue // a type name after ССЫЛКА, e.g. Справочник.Товары
		}
		field := m[1] + "." + m[2]
		return AdviceItem{
			Code: "VirtualTableComplexParams", Severity: "medium", Field: field,
			Message:    "Обращение через точку (" + field + ") в параметрах виртуальной таблицы " + vt + " (#std657 п.2): неявное соединение внутри запроса, в который она разворачивается.",
			Suggestion: "Отбирать по самому измерению: получить нужные значения заранее и передать Измерение В (&Список) или Измерение В (ВЫБРАТЬ ... ИЗ ВТ).",
		}, true
	}
	return AdviceItem{}, false
}

// isTablePrefix reports whether name is a query-table prefix such as
// Справочник or Документ, in any letter case.
func isTablePrefix(name string) bool {
	for prefix := range reverseQueryPrefix {
		if strings.EqualFold(prefix, name) {
			return true
		}
	}
	return false
}

// dimensionConditions blanks everything in virtual-table parameters that is not
// a condition on a dimension: subqueries, predefined-value literals and
// data-composition groups in braces. The names inside them are aliases and type
// names, not dereferenced dimensions.
func dimensionConditions(params string) string {
	s := maskSubqueries(params)
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '{':
			if end := strings.IndexByte(s[i:], '}'); end >= 0 {
				blank(b, i, i+end+1)
				i += end
			}
		case '(':
			if _, end, ok := balancedParen(s[i:]); ok && endsWithWord(s[:i], "ЗНАЧЕНИЕ") {
				blank(b, i, i+end)
				i += end - 1
			}
		}
	}
	return string(b)
}

// maskValueLiterals blanks the argument of every ЗНАЧЕНИЕ(...).
func maskValueLiterals(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if b[i] != '(' {
			continue
		}
		if _, end, ok := balancedParen(s[i:]); ok && endsWithWord(s[:i], "ЗНАЧЕНИЕ") {
			blank(b, i, i+end)
			i += end - 1
		}
	}
	return string(b)
}

// maskSubqueries blanks every parenthesised group that holds a subquery.
func maskSubqueries(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i++ {
		if b[i] != '(' {
			continue
		}
		inner, end, ok := balancedParen(s[i:])
		if ok && indexOfWord(strings.TrimSpace(inner), "ВЫБРАТЬ", 0) == 0 {
			blank(b, i, i+end)
			i += end - 1
		}
	}
	return string(b)
}

func blank(b []byte, from, to int) {
	for i := from; i < to && i < len(b); i++ {
		b[i] = ' '
	}
}

func containsFold(ss []string, want string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// virtualTableFiltersInWhere warns when a dimension of a virtual table is
// compared with a parameter in ГДЕ although the table already takes parameters:
// the filter then applies after the virtual table has been computed. The table
// called without any parameters is reported by VirtualTableNoParams instead.
func (s *XMLSource) virtualTableFiltersInWhere(text string) []AdviceItem {
	var out []AdviceItem
	// Aliases are reused across the statements of a batch, so every statement is
	// matched against its own sources and its own ГДЕ clauses.
	// упрощение: an alias reused by a subquery of the same statement is not told
	// apart; scoping sources by parenthesis depth would fix it.
	for _, stmt := range strings.Split(maskQuery(text), ";") {
		wheres := whereClauses(stmt)
		if len(wheres) == 0 {
			continue
		}
		_, sources := parseSources(stmt, &QueryAdvice{})
		for _, src := range sources {
			if !isVirtualTable(src.table) || !src.hasParams || src.alias == "" {
				continue
			}
			for _, d := range s.registerDimensions(src.table) {
				if indexOfWord(src.params, d, 0) >= 0 {
					continue // already filtered in the parameters
				}
				re := regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_.])` + regexp.QuoteMeta(src.alias) + `\.` + regexp.QuoteMeta(d) +
					`\s*(?:=|В(?:\s+ИЕРАРХИИ)?)\s*\(?\s*&`)
				if !matchesAny(re, wheres) {
					continue
				}
				vt := lastSegment(src.table)
				out = append(out, AdviceItem{
					Code: "VirtualTableFilterInWhere", Severity: "medium", Field: d,
					Message: "Отбор по измерению " + d + " виртуальной таблицы " + vt +
						" стоит в ГДЕ (#std657 п.1): СУБД может сначала рассчитать всю таблицу и только потом отобрать строки.",
					Suggestion: "Перенести условие в параметры: " + vt + "(..., " + d + " = &Значение).",
				})
				break // one warning per source is enough to explain the problem
			}
		}
	}
	return out
}

func matchesAny(re *regexp.Regexp, texts []string) bool {
	for _, t := range texts {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}
