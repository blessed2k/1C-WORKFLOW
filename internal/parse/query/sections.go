package query

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// tempDef — определение временной таблицы (ПОМЕСТИТЬ ИмяВТ), до связывания с
// её использованиями.
type tempDef struct {
	name string
	span domain.Span
}

// parsedFacts — сырые факты, накопленные одним проходом parseTokens (в том
// числе рекурсивным — по содержимому скобок вложенного запроса или параметров
// виртуальной таблицы).
type parsedFacts struct {
	tables    []Table
	fields    []Field
	tempDefs  []tempDef
	tempDrops []string
}

func mergeFacts(dst *parsedFacts, src parsedFacts) {
	dst.tables = append(dst.tables, src.tables...)
	dst.fields = append(dst.fields, src.fields...)
	dst.tempDefs = append(dst.tempDefs, src.tempDefs...)
	dst.tempDrops = append(dst.tempDrops, src.tempDrops...)
}

// ключевые слова языка запросов 1С, русский и английский синтаксис. Разбиты по
// назначению: clauseWords запускают секцию при появлении в позиции ключевого
// слова, noiseWords — модификаторы и служебные слова, которые никогда не
// являются именем поля/таблицы, но сами секцию не начинают.
var clauseWords = map[string]bool{
	"выбрать": true, "select": true,
	"из": true, "from": true,
	"где": true, "where": true,
	"сгруппировать": true, "group": true,
	"имеющие": true, "having": true,
	"упорядочить": true, "order": true,
	"итоги": true, "totals": true,
	"объединить": true, "union": true,
	"поместить": true, "into": true,
	"уничтожить": true, "drop": true,
}

var noiseWords = map[string]bool{
	"как": true, "as": true,
	"различные": true, "distinct": true,
	"разрешенные": true, "разрешённые": true, "allowed": true,
	"первые": true, "top": true,
	"все": true, "all": true,
	"соединение": true, "join": true,
	"по": true, "on": true, "by": true,
	"левое": true, "правое": true, "полное": true, "внутреннее": true, "внешнее": true,
	"left": true, "right": true, "full": true, "inner": true, "outer": true,
	"индексировать": true, "index": true,
}

func isKeyword(w string) bool {
	return clauseWords[w] || noiseWords[w]
}

var joinModifiers = map[string]bool{
	"левое": true, "правое": true, "полное": true, "внутреннее": true, "внешнее": true,
	"left": true, "right": true, "full": true, "inner": true, "outer": true,
}

// virtualSuffixes — последний сегмент точечного имени таблицы, определяющий
// виртуальную таблицу регистра, в каноническом (русском) написании для
// VirtualKind. Английский синтаксис 1С даёт то же VirtualKind, что и русский
// эквивалент — вызывающий код не обязан знать, на каком языке был написан
// текст запроса, чтобы сравнивать вид виртуальной таблицы.
var virtualSuffixes = map[string]string{
	"обороты":             "Обороты",
	"turnovers":           "Обороты",
	"остатки":             "Остатки",
	"balance":             "Остатки",
	"остаткииобороты":     "ОстаткиИОбороты",
	"balanceandturnovers": "ОстаткиИОбороты",
	"срезпоследних":       "СрезПоследних",
	"slicelast":           "СрезПоследних",
	"срезпервых":          "СрезПервых",
	"slicefirst":          "СрезПервых",
}

func lower(s string) string { return strings.ToLower(s) }

// parseTokens — один проход по срезу токенов: находит секции ВЫБРАТЬ, ИЗ,
// ПОМЕСТИТЬ, УНИЧТОЖИТЬ по ключевым словам, где бы они ни встретились (без
// учёта глубины скобок) — вложенные запросы подхватываются тем же проходом,
// когда до их токенов доходит очередь после того, как объемлющая секция
// вернула управление. Секции ГДЕ/СГРУППИРОВАТЬ/ИМЕЮЩИЕ/УПОРЯДОЧИТЬ/ИТОГИ/
// ОБЪЕДИНИТЬ не производят собственных фактов — их слова только останавливают
// сканирование предыдущей секции (описано в scanSelectFields/scanFromSources).
func parseTokens(text string, toks []token, li *lineIndex) parsedFacts {
	var facts parsedFacts
	i := 0
	for i < len(toks) {
		t := toks[i]
		if t.kind == tokWord {
			w := lower(t.text)
			switch {
			case w == "выбрать" || w == "select":
				fields, next := scanSelectFields(text, toks, i+1, li)
				facts.fields = append(facts.fields, fields...)
				i = next
				continue
			case w == "из" || w == "from":
				tables, inner, next := scanFromSources(text, toks, i+1, li)
				facts.tables = append(facts.tables, tables...)
				mergeFacts(&facts, inner)
				i = next
				continue
			case w == "поместить" || w == "into":
				def, next := scanIntoTarget(text, toks, i+1, li)
				if def != nil {
					facts.tempDefs = append(facts.tempDefs, *def)
				}
				i = next
				continue
			case w == "уничтожить" || w == "drop":
				names, next := scanDropTargets(toks, i+1)
				facts.tempDrops = append(facts.tempDrops, names...)
				i = next
				continue
			}
		}
		i++
	}
	return facts
}

// scanSelectFields разбирает список полей от токена после ВЫБРАТЬ/SELECT до
// ближайшего ИЗ/FROM или ПОМЕСТИТЬ/INTO на той же глубине скобок (глубина
// считается только для того, чтобы не спутать ИЗ вложенного скалярного
// подзапроса поля с ИЗ самой секции ВЫБРАТЬ — содержимое такого подзапроса в
// поля не разбирается: это зафиксированный предел, вложенные запросы
// разбираются только как источники ИЗ, а не как скалярные выражения списка
// полей).
func scanSelectFields(text string, toks []token, start int, li *lineIndex) ([]Field, int) {
	var fields []Field
	depth := 0
	i := start
	for i < len(toks) {
		t := toks[i]
		switch t.kind {
		case tokLParen:
			depth++
			i++
		case tokRParen:
			if depth == 0 {
				return fields, i
			}
			depth--
			i++
		case tokWord:
			w := lower(t.text)
			if depth == 0 && (w == "из" || w == "from" || w == "поместить" || w == "into") {
				return fields, i
			}
			if isKeyword(w) {
				i++
				continue
			}
			field, next := tryParseFieldRef(text, toks, i, li)
			if field != nil {
				fields = append(fields, *field)
				i = next
				continue
			}
			i++
		default:
			i++
		}
	}
	return fields, i
}

// tryParseFieldRef пытается разобрать [Квалификатор.]Имя [КАК|AS Алиас],
// начиная с токена i. Возвращает nil, если токен i не подходит на роль начала
// такого выражения (ключевое слово или не идентификатор).
func tryParseFieldRef(text string, toks []token, i int, li *lineIndex) (*Field, int) {
	if toks[i].kind != tokWord || isKeyword(lower(toks[i].text)) {
		return nil, i
	}
	startTok := toks[i]
	qualifier := ""
	name := toks[i].text
	endTok := toks[i]
	j := i + 1
	if j+1 < len(toks) && toks[j].kind == tokDot && toks[j+1].kind == tokWord {
		qualifier = name
		name = toks[j+1].text
		endTok = toks[j+1]
		j += 2
	}
	alias := ""
	if j < len(toks) && toks[j].kind == tokWord {
		w := lower(toks[j].text)
		if w == "как" || w == "as" {
			if j+1 < len(toks) && toks[j+1].kind == tokWord && !isKeyword(lower(toks[j+1].text)) {
				alias = toks[j+1].text
				endTok = toks[j+1]
				j += 2
			} else {
				j++
			}
		}
	}
	return &Field{
		Qualifier: qualifier,
		Name:      name,
		Alias:     alias,
		Span:      spanFor(text, li, startTok.start, endTok.end),
	}, j
}

// scanFromSources разбирает список источников от токена после ИЗ/FROM:
// сначала перечисление через запятую, затем цепочку СОЕДИНЕНИЕ/JOIN. inner —
// факты, найденные рекурсивно внутри скобок источников (вложенный запрос,
// параметры виртуальной таблицы).
func scanFromSources(text string, toks []token, start int, li *lineIndex) ([]Table, parsedFacts, int) {
	var tables []Table
	var inner parsedFacts
	i := start
	for {
		tbl, innerFacts, next, ok := parseTableRef(text, toks, i, li)
		if !ok {
			break
		}
		tables = append(tables, tbl)
		mergeFacts(&inner, innerFacts)
		i = next
		if i < len(toks) && toks[i].kind == tokComma {
			i++
			continue
		}
		break
	}
	for {
		joinAt, ok := findJoinKeyword(toks, i)
		if !ok {
			break
		}
		tbl, innerFacts, next, ok := parseTableRef(text, toks, joinAt+1, li)
		if !ok {
			break
		}
		tables = append(tables, tbl)
		mergeFacts(&inner, innerFacts)
		i = skipJoinCondition(toks, next)
	}
	return tables, inner, i
}

// findJoinKeyword ищет СОЕДИНЕНИЕ/JOIN, начиная с i, пропуская модификаторы
// вида ЛЕВОЕ/ВНЕШНЕЕ. Останавливается на первом токене, который не модификатор
// и не сам JOIN — цепочка на этом заканчивается.
func findJoinKeyword(toks []token, i int) (int, bool) {
	j := i
	for j < len(toks) {
		if toks[j].kind != tokWord {
			return 0, false
		}
		w := lower(toks[j].text)
		if w == "соединение" || w == "join" {
			return j, true
		}
		if !joinModifiers[w] {
			return 0, false
		}
		j++
	}
	return 0, false
}

// skipJoinCondition пропускает условие ПО/ON после источника JOIN до
// следующего терминатора секции или следующего JOIN на той же глубине скобок.
func skipJoinCondition(toks []token, i int) int {
	if i < len(toks) && toks[i].kind == tokWord {
		w := lower(toks[i].text)
		if w == "по" || w == "on" {
			i++
		}
	}
	depth := 0
	for i < len(toks) {
		t := toks[i]
		switch t.kind {
		case tokLParen:
			depth++
		case tokRParen:
			if depth == 0 {
				return i
			}
			depth--
		case tokWord:
			if depth == 0 {
				w := lower(t.text)
				if clauseWords[w] || w == "соединение" || w == "join" || joinModifiers[w] {
					return i
				}
			}
		}
		i++
	}
	return i
}

// parseTableRef разбирает один источник, начиная с токена i: точечное имя
// (с опознаванием виртуальной таблицы и её параметров), скобочный вложенный
// запрос или источник-параметр (&Имя). ok=false — токен i не источник
// (например, сразу терминатор секции); scanFromSources в этом случае
// останавливает список источников.
func parseTableRef(text string, toks []token, i int, li *lineIndex) (Table, parsedFacts, int, bool) {
	if i >= len(toks) {
		return Table{}, parsedFacts{}, i, false
	}

	if toks[i].kind == tokLParen {
		closeIdx, ok := matchParen(toks, i)
		if !ok {
			return Table{}, parsedFacts{}, i, false
		}
		inner := parseTokens(text, toks[i+1:closeIdx], li)
		span := spanFor(text, li, toks[i].start, toks[closeIdx].end)
		j := closeIdx + 1
		alias, j := scanOptionalAlias(toks, j)
		return Table{Kind: TableSubquery, Alias: alias, Span: span}, inner, j, true
	}

	if toks[i].kind == tokAmp && i+1 < len(toks) && toks[i+1].kind == tokWord {
		nameTok := toks[i+1]
		endTok := nameTok
		j := i + 2
		alias, j := scanOptionalAlias(toks, j)
		return Table{
			Kind:  TableParameter,
			Name:  nameTok.text,
			Alias: alias,
			Span:  spanFor(text, li, toks[i].start, endTok.end),
		}, parsedFacts{}, j, true
	}

	if toks[i].kind != tokWord || isKeyword(lower(toks[i].text)) {
		return Table{}, parsedFacts{}, i, false
	}

	startTok := toks[i]
	nameParts := []string{toks[i].text}
	endTok := toks[i]
	j := i + 1
	for j+1 < len(toks) && toks[j].kind == tokDot && toks[j+1].kind == tokWord {
		nameParts = append(nameParts, toks[j+1].text)
		endTok = toks[j+1]
		j += 2
	}
	fullName := strings.Join(nameParts, ".")

	kind := TableBase
	virtualKind := ""
	if last := lower(nameParts[len(nameParts)-1]); virtualSuffixes[last] != "" {
		kind = TableVirtual
		virtualKind = virtualSuffixes[last]
	}

	params := ""
	var inner parsedFacts
	if j < len(toks) && toks[j].kind == tokLParen {
		closeIdx, ok := matchParen(toks, j)
		if ok {
			if closeIdx > j+1 {
				params = text[toks[j+1].start:toks[closeIdx-1].end]
				inner = parseTokens(text, toks[j+1:closeIdx], li)
			}
			endTok = toks[closeIdx]
			j = closeIdx + 1
		}
	}

	alias, j := scanOptionalAlias(toks, j)

	tbl := Table{
		Name:        fullName,
		Alias:       alias,
		Kind:        kind,
		VirtualKind: virtualKind,
		Params:      params,
		Span:        spanFor(text, li, startTok.start, endTok.end),
	}
	return tbl, inner, j, true
}

// scanOptionalAlias разбирает необязательный алиас после источника: КАК/AS
// Слово, либо (в 1С это допустимо) голое Слово без КАК, если оно не является
// ключевым словом языка запросов.
func scanOptionalAlias(toks []token, j int) (string, int) {
	if j >= len(toks) || toks[j].kind != tokWord {
		return "", j
	}
	w := lower(toks[j].text)
	if w == "как" || w == "as" {
		if j+1 < len(toks) && toks[j+1].kind == tokWord && !isKeyword(lower(toks[j+1].text)) {
			return toks[j+1].text, j + 2
		}
		return "", j + 1
	}
	if !isKeyword(w) {
		return toks[j].text, j + 1
	}
	return "", j
}

// matchParen находит индекс токена ')' парного открывающему '(' на позиции
// openIdx (openIdx обязан указывать на tokLParen).
func matchParen(toks []token, openIdx int) (int, bool) {
	depth := 0
	for i := openIdx; i < len(toks); i++ {
		switch toks[i].kind {
		case tokLParen:
			depth++
		case tokRParen:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// scanIntoTarget разбирает имя временной таблицы сразу после ПОМЕСТИТЬ/INTO.
func scanIntoTarget(text string, toks []token, i int, li *lineIndex) (*tempDef, int) {
	if i >= len(toks) || toks[i].kind != tokWord || isKeyword(lower(toks[i].text)) {
		return nil, i
	}
	nameTok := toks[i]
	return &tempDef{name: nameTok.text, span: spanFor(text, li, nameTok.start, nameTok.end)}, i + 1
}

// scanDropTargets разбирает список имён временных таблиц после
// УНИЧТОЖИТЬ/DROP, через запятую.
func scanDropTargets(toks []token, i int) ([]string, int) {
	var names []string
	for i < len(toks) && toks[i].kind == tokWord && !isKeyword(lower(toks[i].text)) {
		names = append(names, toks[i].text)
		i++
		if i < len(toks) && toks[i].kind == tokComma {
			i++
			continue
		}
		break
	}
	return names, i
}

// scanParamRefs — независимый проход по ВСЕМ токенам текста запроса, ищущий
// &Имя. Не зависит от того, какие диапазоны токенов "потребили" секции
// ВЫБРАТЬ/ИЗ: параметр внутри скобок виртуальной таблицы, во вложенном
// запросе или в условии ГДЕ находится одинаково надёжно и никогда не попадает
// в список полей (Field формируется только словом-идентификатором, не "&").
func scanParamRefs(text string, toks []token, li *lineIndex) []Parameter {
	var params []Parameter
	for i := 0; i < len(toks); i++ {
		if toks[i].kind == tokAmp && i+1 < len(toks) && toks[i+1].kind == tokWord {
			params = append(params, Parameter{
				Name: toks[i+1].text,
				Span: spanFor(text, li, toks[i].start, toks[i+1].end),
			})
			i++
		}
	}
	return params
}
