package source

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// reverseQueryPrefix maps a Russian query-table prefix back to its metadata type.
var reverseQueryPrefix = func() map[string]string {
	m := make(map[string]string, len(queryPrefix))
	for en, ru := range queryPrefix {
		m[ru] = en
	}
	return m
}()

// virtualSuffix is the set of register virtual-table suffixes.
var virtualSuffix = map[string]bool{
	"Остатки": true, "Обороты": true, "ОстаткиИОбороты": true,
	"СрезПоследних": true, "СрезПервых": true,
}

// Query-analysis regexes. Cyrillic word chars need \p{L}; \w and \b are
// ASCII-only in RE2, so keyword boundaries are handled with (?:^|\s) or by
// scanning bytes, never \b. Source parsing scans balanced parentheses manually
// because RE2 cannot match nested parens.
var (
	reSourceHead   = regexp.MustCompile(`(?i)(?:^|\s)(?:ИЗ|СОЕДИНЕНИЕ)\s+([\p{L}\d_.]+)`)
	reAlias        = regexp.MustCompile(`(?i)^\s*КАК\s+([\p{L}\d_]+)`)
	reJoinSubquery = regexp.MustCompile(`(?i)СОЕДИНЕНИЕ\s*\(`)
	reOuterJoin    = regexp.MustCompile(`(?i)(ЛЕВОЕ|ПРАВОЕ|ПОЛНОЕ)\s+(ВНЕШНЕЕ\s+)?СОЕДИНЕНИЕ`)
	reIsNull       = regexp.MustCompile(`(?i)ЕСТЬNULL`)
	reLeadingLike  = regexp.MustCompile(`(?i)ПОДОБНО\s*"%`)
	reWhereField   = regexp.MustCompile(`([\p{L}\d_]+)\.([\p{L}\d_]+)`)
	// The star is only a star at the head of the select list; КОЛИЧЕСТВО(*) and
	// the keywords that may precede it are handled here rather than by matching
	// a bare "*" anywhere in the text.
	reSelectStar = regexp.MustCompile(`(?i)(?:^|\s)ВЫБРАТЬ\s+(?:РАЗРЕШЕННЫЕ\s+)?(?:РАЗЛИЧНЫЕ\s+)?(?:ПЕРВЫЕ\s+\d+\s+)?\*`)
)

// whereTerminators end the WHERE clause.
var whereTerminators = []string{"СГРУППИРОВАТЬ", "УПОРЯДОЧИТЬ", "ИТОГИ", "ОБЪЕДИНИТЬ", "ИНДЕКСИРОВАТЬ"}

// joinTerminators end a join ПО condition. It ends at anything that starts the
// next clause, including the next join and a nested ВЫБРАТЬ.
var joinTerminators = append([]string{"ГДЕ", "СОЕДИНЕНИЕ", "ПОМЕСТИТЬ", "ВЫБРАТЬ"}, whereTerminators...)

// registerKind lists the metadata types whose dimensions form the composite
// index of the table, in declaration order.
var registerKind = map[string]bool{
	"InformationRegister":  true,
	"AccumulationRegister": true,
	"AccountingRegister":   true,
	"CalculationRegister":  true,
}

// AdviseQuery statically analyses a 1C query: it flags heavy anti-patterns with
// concrete rewrites and, using object metadata, warns about filters on
// non-indexed fields. It parses the query text heuristically (not a full
// parser) and never runs it; the real SQL plan is out of scope offline.
func (s *XMLSource) AdviseQuery(_ context.Context, text string) (*QueryAdvice, error) {
	adv, aliases, sources := adviseQueryText(text)
	adv.Warnings = append(adv.Warnings, s.unindexedFilters(text, aliases)...)
	adv.Warnings = append(adv.Warnings, s.unindexedJoins(text, aliases)...)
	adv.Warnings = append(adv.Warnings, s.nonLeadingDimensionFilters(text, sources)...)
	adv.Count = len(adv.Warnings)
	return adv, nil
}

// adviseQueryText runs the half of the analysis that needs nothing but the query
// text, and returns the parsed aliases and sources so a caller with metadata can
// add the index checks. A live base reaches the same rules through this function:
// the index checks read the Indexing flag, which exists only in the XML export.
func adviseQueryText(text string) (*QueryAdvice, map[string]string, []source) {
	adv := &QueryAdvice{Warnings: []AdviceItem{}}
	aliases, sources := parseSources(text, adv)

	virtual := 0
	for _, src := range sources {
		if !isVirtualTable(src.table) {
			continue
		}
		virtual++
		if !src.hasParams {
			vt := lastSegment(src.table)
			adv.Warnings = append(adv.Warnings, AdviceItem{
				Code: "VirtualTableNoParams", Severity: "medium", Field: vt,
				Message:    "Виртуальная таблица " + vt + " вызвана без параметров — читается весь регистр.",
				Suggestion: "Перенести отбор в параметры виртуальной таблицы, напр. " + vt + "(&Период, Измерение В (&Список)).",
			})
		}
	}

	// Each virtual table is itself a subquery over the register; joining two of
	// them leaves the optimiser without indexes on either side, the same problem
	// as JoinWithSubquery but invisible in the text.
	if virtual > 1 {
		adv.Warnings = append(adv.Warnings, AdviceItem{
			Code: "VirtualTablesJoined", Severity: "high",
			Message:    "В запросе соединяются виртуальные таблицы (" + strconv.Itoa(virtual) + " шт.) — каждая разворачивается в подзапрос, соединение считается без индексов.",
			Suggestion: "Разложить на временные таблицы: каждую виртуальную таблицу с отбором ПОМЕСТИТЬ в ВТ (при необходимости ИНДЕКСИРОВАТЬ ПО), затем соединять временные таблицы.",
		})
	}

	if reSelectStar.MatchString(text) {
		adv.Warnings = append(adv.Warnings, AdviceItem{
			Code: "SelectStar", Severity: "medium",
			Message:    "ВЫБРАТЬ * читает все поля таблицы, включая неиспользуемые и потенциально тяжёлые (ХранилищеЗначения), и ломает покрытие запроса индексом.",
			Suggestion: "Перечислить поля явно, оставив только нужные.",
		})
	}

	if reJoinSubquery.MatchString(text) {
		adv.Warnings = append(adv.Warnings, AdviceItem{
			Code: "JoinWithSubquery", Severity: "high",
			Message:    "Соединение с вложенным запросом — оптимизатор не использует индексы подзапроса.",
			Suggestion: "Вынести подзапрос во временную таблицу (МенеджерВременныхТаблиц, ПОМЕСТИТЬ) и соединять с ней.",
		})
	}

	if reOuterJoin.MatchString(text) && !reIsNull.MatchString(text) {
		adv.Warnings = append(adv.Warnings, AdviceItem{
			Code: "OuterJoinWithoutIsNull", Severity: "low",
			Message:    "Внешнее соединение без ЕСТЬNULL — поля правой таблицы могут быть NULL.",
			Suggestion: "Оборачивать поля внешнего соединения в ЕСТЬNULL(Поле, 0) при использовании в выражениях и итогах.",
		})
	}

	if reLeadingLike.MatchString(text) {
		adv.Warnings = append(adv.Warnings, AdviceItem{
			Code: "LeadingWildcardLike", Severity: "medium",
			Message:    "ПОДОБНО с ведущим шаблоном \"%...\" — индекс не используется, идёт скан.",
			Suggestion: "Избегать поиска по вхождению; для поиска по подстроке использовать полнотекстовый поиск или отбор по началу строки.",
		})
	}

	adv.Count = len(adv.Warnings)
	return adv, aliases, sources
}

// source is one recognised query source.
type source struct {
	table     string
	alias     string
	hasParams bool
	params    string // virtual-table parameters as written, for filter detection
}

// parseSources scans ИЗ/СОЕДИНЕНИЕ sources, filling adv.Tables, and returns the
// alias->table map and the parsed sources. Parameters (which may nest, e.g.
// Остатки(&Дата, Товар В (&Список))) are scanned with balanced parentheses.
func parseSources(text string, adv *QueryAdvice) (map[string]string, []source) {
	aliases := map[string]string{}
	var sources []source
	for _, loc := range reSourceHead.FindAllStringSubmatchIndex(text, -1) {
		table := text[loc[2]:loc[3]]
		rest := text[loc[3]:]

		hasParams, params := false, ""
		if inner, end, ok := balancedParen(rest); ok {
			params = inner
			hasParams = strings.TrimSpace(inner) != ""
			rest = rest[end:]
		}

		// Псевдоним необязателен: "ИЗ РегистрНакопления.Х.Остатки" без КАК — такой же
		// источник, и терять его нельзя, иначе анализ виртуальных таблиц молчит.
		alias := ""
		if am := reAlias.FindStringSubmatch(rest); am != nil {
			alias = am[1]
			aliases[strings.ToLower(alias)] = table
		}
		if !containsString(adv.Tables, table) {
			adv.Tables = append(adv.Tables, table)
		}
		sources = append(sources, source{table: table, alias: alias, hasParams: hasParams, params: params})
	}
	return aliases, sources
}

// balancedParen, given s that may start with whitespace then '(', returns the
// inner text and the index in s just past the matching ')'. ok is false when s
// does not start with '(' after optional whitespace. ASCII '(' and ')' never
// collide with UTF-8 continuation bytes, so a byte scan is safe.
func balancedParen(s string) (inner string, end int, ok bool) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	if i >= len(s) || s[i] != '(' {
		return "", 0, false
	}
	depth, start := 0, i+1
	for ; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[start:i], i + 1, true
			}
		}
	}
	return s[start:], len(s), true // unbalanced input
}

// unindexedFilters inspects the WHERE clause and warns about comparisons on
// fields that are not indexed in the source object's metadata.
func (s *XMLSource) unindexedFilters(text string, aliases map[string]string) []AdviceItem {
	where := extractWhere(text)
	if strings.TrimSpace(where) == "" {
		return nil
	}

	var out []AdviceItem
	for _, p := range s.unindexedPairs(where, aliases) {
		out = append(out, AdviceItem{
			Code: "UnindexedFilter", Severity: "medium", Field: p.field,
			Message:    "Отбор по полю " + p.field + " (" + p.table + ") не покрыт индексом.",
			Suggestion: "Проиндексировать реквизит " + p.field + " (Индексировать) или пересмотреть условие отбора.",
		})
	}
	return out
}

// unindexedJoins warns about join keys that are not indexed. A filter on a
// non-indexed field scans one table; a join by one scans it once per row of the
// other side, so it is reported separately from UnindexedFilter.
func (s *XMLSource) unindexedJoins(text string, aliases map[string]string) []AdviceItem {
	var out []AdviceItem
	seen := map[string]bool{}
	for _, cond := range extractJoinConditions(text) {
		for _, p := range s.unindexedPairs(cond, aliases) {
			key := p.table + "." + p.field
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, AdviceItem{
				Code: "UnindexedJoin", Severity: "high", Field: p.field,
				Message:    "Соединение по полю " + p.field + " (" + p.table + ") не покрыто индексом — таблица сканируется на каждую строку соединения.",
				Suggestion: "Проиндексировать реквизит " + p.field + " (Индексировать), соединять по ссылке, либо предварительно поместить выборку в ВТ с ИНДЕКСИРОВАТЬ ПО " + p.field + ".",
			})
		}
	}
	return out
}

// fieldRef is one alias-qualified field resolved to its table.
type fieldRef struct {
	table string
	field string
}

// unindexedPairs returns the alias-qualified fields of clause that the source
// object's metadata does not cover with an index, without duplicates.
func (s *XMLSource) unindexedPairs(clause string, aliases map[string]string) []fieldRef {
	indexCache := map[string]map[string]bool{}
	seen := map[string]bool{}
	var out []fieldRef
	for _, f := range reWhereField.FindAllStringSubmatch(clause, -1) {
		alias, field := strings.ToLower(f[1]), f[2]
		table, ok := aliases[alias]
		if !ok {
			continue
		}
		idx, ok := indexCache[table]
		if !ok {
			idx = s.indexedFieldsForTable(table)
			indexCache[table] = idx
		}
		if idx == nil {
			continue // table not resolvable to a configuration object
		}
		key := table + "." + field
		if seen[key] || idx[field] {
			continue
		}
		seen[key] = true
		out = append(out, fieldRef{table: table, field: field})
	}
	return out
}

// nonLeadingDimensionFilters warns when a register is filtered by a dimension
// that is not the first one while the first is not filtered at all. The register
// index follows the declaration order of the dimensions, so such a filter can
// only be served by scanning: the leading part of the composite index is
// missing. The filter is looked for both in ГДЕ and in the virtual-table
// parameters, because either place is a filter as far as the index is concerned.
func (s *XMLSource) nonLeadingDimensionFilters(text string, sources []source) []AdviceItem {
	where := extractWhere(text)
	var out []AdviceItem
	for _, src := range sources {
		dims := s.registerDimensions(src.table)
		if len(dims) < 2 {
			continue
		}

		filtered := map[string]bool{}
		for _, f := range reWhereField.FindAllStringSubmatch(where, -1) {
			if strings.EqualFold(f[1], src.alias) {
				filtered[f[2]] = true
			}
		}
		for _, d := range dims {
			if indexOfWord(src.params, d, 0) >= 0 {
				filtered[d] = true
			}
		}
		if filtered[dims[0]] {
			continue
		}
		for _, d := range dims[1:] {
			if !filtered[d] {
				continue
			}
			out = append(out, AdviceItem{
				Code: "NonLeadingDimensionFilter", Severity: "medium", Field: d,
				Message: "Отбор по измерению " + d + " (" + src.table + ") без отбора по первому измерению " + dims[0] +
					" — индекс регистра строится по измерениям в порядке объявления, его левая часть не задана.",
				Suggestion: "Добавить в отбор " + dims[0] + " либо изменить порядок измерений регистра так, чтобы " + d +
					" шло первым (порядок измерений задаёт порядок полей в индексе).",
			})
			break // one warning per source is enough to explain the problem
		}
	}
	return out
}

// registerDimensions returns the dimensions of a register table (base or
// virtual) in declaration order, or nil if the table is not a register.
func (s *XMLSource) registerDimensions(table string) []string {
	segs := strings.Split(table, ".")
	if len(segs) < 2 {
		return nil
	}
	objectType := reverseQueryPrefix[segs[0]]
	if !registerKind[objectType] {
		return nil
	}
	path := filepath.Join(s.root, folderForType(objectType), segs[1]+".xml")
	var root xmlObjectRoot
	if err := readXML(path, &root); err != nil {
		return nil
	}
	var dims []string
	for _, ch := range root.Object.ChildObjects.Items {
		if ch.XMLName.Local == "Dimension" {
			dims = append(dims, ch.name())
		}
	}
	return dims
}

// extractWhere returns the text of the WHERE clause, or "" if there is none. It
// finds the ГДЕ keyword and cuts at the next clause-terminating keyword, both
// matched as whole words (surrounded by non-letters) to avoid cutting inside an
// identifier that merely contains a terminator substring.
func extractWhere(text string) string {
	i := indexOfWord(text, "ГДЕ", 0)
	if i < 0 {
		return ""
	}
	rest := text[i+len("ГДЕ"):]
	end := len(rest)
	for _, term := range whereTerminators {
		if j := indexOfWord(rest, term, 0); j >= 0 && j < end {
			end = j
		}
	}
	return rest[:end]
}

// extractJoinConditions returns the text of every join ПО condition. The ПО is
// anchored to the preceding СОЕДИНЕНИЕ rather than searched for on its own,
// because the same word ends СГРУППИРОВАТЬ ПО, УПОРЯДОЧИТЬ ПО and ИТОГИ ... ПО.
func extractJoinConditions(text string) []string {
	var out []string
	from := 0
	for {
		join := indexOfWord(text, "СОЕДИНЕНИЕ", from)
		if join < 0 {
			return out
		}
		from = join + len("СОЕДИНЕНИЕ")
		// A join with a subquery carries its own ПО inside the parentheses
		// (СГРУППИРОВАТЬ ПО, УПОРЯДОЧИТЬ ПО); skip the whole group so the
		// condition of THIS join is found.
		if _, end, ok := balancedParen(text[from:]); ok {
			from += end
		}
		on := indexOfWord(text, "ПО", from)
		if on < 0 {
			return out
		}
		rest := text[on+len("ПО"):]
		end := len(rest)
		for _, term := range joinTerminators {
			if j := indexOfWord(rest, term, 0); j >= 0 && j < end {
				end = j
			}
		}
		out = append(out, rest[:end])
		from = on + len("ПО")
	}
}

// indexOfWord returns the index of the first whole-word (case-insensitive)
// occurrence of word in s at or after from, or -1. A whole word is not adjacent
// to a letter or digit on either side.
func indexOfWord(s, word string, from int) int {
	up := strings.ToUpper(s)
	uw := strings.ToUpper(word)
	for {
		j := strings.Index(up[from:], uw)
		if j < 0 {
			return -1
		}
		pos := from + j
		if !letterOrDigitAt(up, pos-1) && !letterOrDigitAt(up, pos+len(uw)) {
			return pos
		}
		from = pos + len(uw)
	}
}

// letterOrDigitAt reports whether the rune starting at byte position pos in s is
// a letter or digit. Out-of-range positions are treated as non-letters.
func letterOrDigitAt(s string, pos int) bool {
	if pos < 0 || pos >= len(s) {
		return false
	}
	r := []rune(s[pos:])
	if len(r) == 0 {
		return false
	}
	return isLetterOrDigit(r[0])
}

func isLetterOrDigit(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
		(r >= 'А' && r <= 'я') || r == 'Ё' || r == 'ё' || r == '_'
}

// indexedFieldsForTable resolves a query-table name (optionally a register
// virtual table) to its base object and returns its indexed field set, or nil
// if the table is not a configuration object.
func (s *XMLSource) indexedFieldsForTable(table string) map[string]bool {
	segs := strings.Split(table, ".")
	if len(segs) < 2 {
		return nil
	}
	objectType := reverseQueryPrefix[segs[0]]
	if objectType == "" {
		return nil
	}
	return s.indexedFields(objectType, segs[1])
}

// indexedFields returns the indexed fields of an object: own indexed attributes
// plus the standard fields that are always indexed for its kind.
func (s *XMLSource) indexedFields(objectType, name string) map[string]bool {
	path := filepath.Join(s.root, folderForType(objectType), name+".xml")
	var root xmlObjectRoot
	if err := readXML(path, &root); err != nil {
		return nil
	}
	obj := root.Object
	idx := map[string]bool{}

	for _, ch := range obj.ChildObjects.Items {
		switch ch.XMLName.Local {
		case "Attribute":
			if ch.indexed() {
				idx[ch.name()] = true
			}
		case "Dimension": // register dimensions are indexed
			idx[ch.name()] = true
		}
	}

	// Ссылка is indexed for every reference type.
	if referenceType[obj.XMLName.Local] {
		idx["Ссылка"] = true
	}
	switch obj.XMLName.Local {
	case "Catalog", "ChartOfCharacteristicTypes":
		if obj.Properties.hasCode() {
			idx["Код"] = true
		}
		if obj.Properties.hasName() {
			idx["Наименование"] = true
		}
		if obj.Properties.hierarchical() {
			idx["Родитель"] = true
		}
		if obj.Properties.subordinate() {
			idx["Владелец"] = true
		}
	case "Document":
		idx["Дата"] = true
		idx["Номер"] = true
	case "InformationRegister", "AccumulationRegister":
		idx["Период"] = true
		idx["Регистратор"] = true
	}
	return idx
}

// isVirtualTable reports whether a table name ends with a register virtual-table
// suffix (e.g. РегистрНакопления.Товары.Остатки).
func isVirtualTable(table string) bool {
	return virtualSuffix[lastSegment(table)]
}

func lastSegment(table string) string {
	if i := strings.LastIndex(table, "."); i >= 0 {
		return table[i+1:]
	}
	return table
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
