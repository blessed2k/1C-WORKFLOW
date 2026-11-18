package source

import (
	"fmt"
	"strings"
	"unicode"
)

// QueryErrorHint names the likely cause of a failed 1C query when the failure is
// one of the mistakes a language model repeats. The rules come from the
// execute_query errors in the Claude Code journals of 21.09-18.11.2026: a few
// patterns make up most of them, and the bare 1C text ("Ожидается имя",
// "Неверные параметры") does not say what to change. message is the 1C error
// text, which carries the failing fragment after the <<?>> marker; text is the
// query. The answer is "" when no rule applies: a hint that guesses is worse
// than none.
func QueryErrorHint(message, text string) string {
	f := queryFailure{
		message: strings.ToUpper(message),
		near:    strings.ToUpper(nearMarker(message)),
		word:    wordAfterMarker(message),
		tokens:  tokenizeQuery(text),
	}
	for _, rule := range queryHintRules {
		if hint := rule(f); hint != "" {
			return hint
		}
	}
	return ""
}

// queryFailure is what the rules look at: the message upper-cased, the part of
// it around the <<?>> marker, the word the marker points at (original case) and
// the words of the query.
type queryFailure struct {
	message string
	near    string
	word    string
	tokens  []queryToken
}

// queryHintRules are tried in order, the first hint wins: one short line is what
// the caller reads, two rules rarely fire on one failure.
var queryHintRules = []func(queryFailure) string{
	reservedAliasHint,
	unionAfterOrderHint,
	aggregateArgumentHint,
	presentationOrderHint,
	presentationConditionHint,
	unlimitedStringHint,
	predefinedValueHint,
	fieldNotFoundHint,
	tableNotFoundHint,
	ambiguousFieldHint,
	groupingHint,
}

// queryKeywords are the words the 1C query parser reads as syntax, so they cannot
// be an alias. Only words that certainly are: Ссылка, Значение, Дата, Количество
// are ordinary aliases and must stay out.
var queryKeywords = map[string]bool{
	"В": true, "И": true, "ИЛИ": true, "НЕ": true, "ПО": true, "КОГДА": true, "ТОГДА": true,
	"ИНАЧЕ": true, "КОНЕЦ": true, "ЕСТЬ": true, "ИЗ": true, "ГДЕ": true, "КАК": true,
	"ВЫБОР": true, "МЕЖДУ": true, "ПОДОБНО": true, "ВЫБРАТЬ": true, "ОБЪЕДИНИТЬ": true,
	"СОЕДИНЕНИЕ": true, "ЛЕВОЕ": true, "ПРАВОЕ": true, "ПОЛНОЕ": true, "ВНУТРЕННЕЕ": true,
	"УПОРЯДОЧИТЬ": true, "СГРУППИРОВАТЬ": true, "ИМЕЮЩИЕ": true, "ПОМЕСТИТЬ": true,
	"РАЗЛИЧНЫЕ": true, "ПЕРВЫЕ": true, "РАЗРЕШЕННЫЕ": true, "ИСТИНА": true, "ЛОЖЬ": true,
	"NULL": true, "НЕОПРЕДЕЛЕНО": true, "ВОЗР": true, "УБЫВ": true, "ДЛЯ": true,
	"ИЕРАРХИИ": true, "ВСЕ": true,
	"IN": true, "AND": true, "OR": true, "NOT": true, "BY": true, "WHEN": true, "THEN": true,
	"ELSE": true, "END": true, "IS": true, "FROM": true, "WHERE": true, "AS": true,
	"CASE": true, "SELECT": true, "UNION": true, "ON": true, "TRUE": true, "FALSE": true,
}

func reservedAliasHint(f queryFailure) string {
	word := ""
	if strings.Contains(f.message, "ОЖИДАЕТСЯ ИМЯ") && queryKeywords[strings.ToUpper(f.word)] {
		word = f.word
	}
	if word == "" {
		// The marker is not always there (a message cut short); the text itself
		// is certain: a keyword after КАК is always an error.
		for i := 0; i+1 < len(f.tokens); i++ {
			if (f.tokens[i].upper == "КАК" || f.tokens[i].upper == "AS") && queryKeywords[f.tokens[i+1].upper] {
				word = f.tokens[i+1].word
				break
			}
		}
	}
	if word == "" {
		return ""
	}
	return fmt.Sprintf("«%s» это ключевое слово языка запросов, псевдонимом его взять нельзя: назовите таблицу или поле иначе (Т1, Вер).", word)
}

func unionAfterOrderHint(f queryFailure) string {
	ordered := -1 // statement of the last top-level УПОРЯДОЧИТЬ ПО seen
	for i, t := range f.tokens {
		if t.depth != 0 {
			continue
		}
		next := ""
		if i+1 < len(f.tokens) {
			next = f.tokens[i+1].upper
		}
		switch {
		case (t.upper == "УПОРЯДОЧИТЬ" && next == "ПО") || (t.upper == "ORDER" && next == "BY"):
			ordered = t.statement
		case (t.upper == "ОБЪЕДИНИТЬ" || t.upper == "UNION") && ordered == t.statement:
			return "УПОРЯДОЧИТЬ ПО ставится один раз, после последней части ОБЪЕДИНИТЬ, и сортирует всё объединение: уберите его из верхних частей и упорядочивайте по псевдонимам первой части."
		}
	}
	return ""
}

func aggregateArgumentHint(f queryFailure) string {
	for _, fn := range []string{`"МАКСИМУМ"`, `"МИНИМУМ"`, `"MAX"`, `"MIN"`} {
		if strings.Contains(f.message, "НЕВЕРНЫЕ ПАРАМЕТРЫ "+fn) {
			return "МАКСИМУМ и МИНИМУМ не принимают ссылку, ПРЕДСТАВЛЕНИЕ(), ТИПЗНАЧЕНИЯ() и строку неограниченной длины: " +
				"берите дату или число, строку через ПОДСТРОКА(Поле, 1, 100), а ссылку выводите в СГРУППИРОВАТЬ ПО или считайте КОЛИЧЕСТВО(РАЗЛИЧНЫЕ Поле)."
		}
	}
	return ""
}

func presentationOrderHint(f queryFailure) string {
	if !strings.Contains(f.message, `ОПЕРАЦИЯ НЕ РАЗРЕШЕНА В ПРЕДЛОЖЕНИИ "УПОРЯДОЧИТЬ"`) {
		return ""
	}
	return "В УПОРЯДОЧИТЬ ПО нельзя ПРЕДСТАВЛЕНИЕ() и псевдоним колонки с ним: упорядочивайте по самому полю-ссылке " +
		"(1С сортирует ссылки по представлению) или по его реквизиту (Поле.Наименование)."
}

// unlimitedMessage reports the messages 1C gives for a string of unlimited length
// where only a bounded one is allowed. ПРЕДСТАВЛЕНИЕ() returns such a string.
func unlimitedMessage(m string) bool {
	for _, s := range []string{
		"НЕЛЬЗЯ СРАВНИВАТЬ ПОЛЯ НЕОГРАНИЧЕННОЙ ДЛИНЫ",
		"НЕЛЬЗЯ ИСПОЛЬЗОВАТЬ ПОЛЯ НЕОГРАНИЧЕННОЙ",
		`НЕВЕРНЫЕ ПАРАМЕТРЫ "ПОДОБНО"`,
		`НЕВЕРНЫЕ ПАРАМЕТРЫ "+"`,
		"НЕДОПУСТИМОЕ ПОЛЕ ДЛЯ УПОРЯДОЧИВАНИЯ",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

func presentationConditionHint(f queryFailure) string {
	if !unlimitedMessage(f.message) || !strings.Contains(f.near, "ПРЕДСТАВЛЕНИЕ(") {
		return ""
	}
	return "ПРЕДСТАВЛЕНИЕ() даёт строку неограниченной длины: её нельзя сравнивать, складывать и ставить в ПОДОБНО. " +
		"Отбирайте по самой ссылке (параметром) или по её реквизиту (Поле.Наименование ПОДОБНО ...), представление выводите отдельной колонкой."
}

func unlimitedStringHint(f queryFailure) string {
	if !unlimitedMessage(f.message) {
		return ""
	}
	return "Строку неограниченной длины (Комментарий, Описание) нельзя сравнивать, складывать, упорядочивать и брать в РАЗЛИЧНЫЕ: " +
		"оберните ВЫРАЗИТЬ(Поле КАК СТРОКА(200)) или ПОДСТРОКА(Поле, 1, 200)."
}

func predefinedValueHint(f queryFailure) string {
	if !strings.Contains(f.message, "НЕВЕРНЫЕ ПАРАМЕТРЫ") || !strings.Contains(f.near, "ЗНАЧЕНИЕ(<<?>>") {
		return ""
	}
	return "Такого предопределённого значения в этой базе нет: список даёт get_predefined; путь пишется Справочник.Имя.ИмяЭлемента, у перечисления Перечисление.Имя.ИмяЗначения."
}

func fieldNotFoundHint(f queryFailure) string {
	if !strings.Contains(f.message, "ПОЛЕ НЕ НАЙДЕНО") {
		return ""
	}
	return "Сверьте имя через get_object_structure (type, name, base): он отдаёт реквизиты, измерения и ресурсы этой базы. " +
		"У виртуальных таблиц поля с суффиксом: СуммаОстаток, СуммаОборот."
}

func tableNotFoundHint(f queryFailure) string {
	if !strings.Contains(f.message, "ТАБЛИЦА НЕ НАЙДЕНА") {
		return ""
	}
	return "Сверьте имя объекта через object_exists или get_metadata_tree like=<часть имени> (с base). В тексте запроса тип пишется по-русски: Документ.Имя, РегистрСведений.Имя."
}

func ambiguousFieldHint(f queryFailure) string {
	if !strings.Contains(f.message, "НЕОДНОЗНАЧНОЕ ПОЛЕ") {
		return ""
	}
	return "Поле с таким именем есть в нескольких таблицах запроса: пишите его с псевдонимом таблицы (Т.Поле)."
}

func groupingHint(f queryFailure) string {
	if !strings.Contains(f.message, "ПОЛЕ НЕ ВХОДИТ В ГРУППУ") &&
		!strings.Contains(f.message, "СОДЕРЖАЩЕМ ПРЕДЛОЖЕНИЕ СГРУППИРОВАТЬ ПО") {
		return ""
	}
	return "Каждое поле выборки без агрегатной функции должно стоять в СГРУППИРОВАТЬ ПО; иначе оберните его в МАКСИМУМ() или уберите из выборки."
}

// queryMarker is where 1C points at the failing place in its message.
const queryMarker = "<<?>>"

// nearMarker is the message around the marker, where the failing construct is;
// the whole message when there is no marker.
func nearMarker(message string) string {
	i := strings.Index(message, queryMarker)
	if i < 0 {
		return message
	}
	before := []rune(message[:i])
	if len(before) > 60 {
		before = before[len(before)-60:]
	}
	after := []rune(message[i:])
	if len(after) > 60 {
		after = after[:60]
	}
	return string(before) + string(after)
}

// wordAfterMarker is the identifier the marker points at, as written.
func wordAfterMarker(message string) string {
	i := strings.Index(message, queryMarker)
	if i < 0 {
		return ""
	}
	rest := []rune(message[i+len(queryMarker):])
	n := 0
	for n < len(rest) && isQueryWordRune(rest[n]) {
		n++
	}
	return string(rest[:n])
}

func isQueryWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// queryToken is one word of a query text: string literals and // comments are
// skipped, so a keyword inside "..." is not one. depth counts parentheses,
// statement the ; separated parts of a batch.
type queryToken struct {
	word      string
	upper     string
	depth     int
	statement int
}

func tokenizeQuery(text string) []queryToken {
	var out []queryToken
	runes := []rune(text)
	depth, statement := 0, 0
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '"':
			// A doubled quote inside a literal closes and reopens it, which the
			// loop handles without a special case.
			for i++; i < len(runes) && runes[i] != '"'; i++ {
			}
		case r == '/' && i+1 < len(runes) && runes[i+1] == '/':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
		case r == '(':
			depth++
		case r == ')':
			if depth > 0 {
				depth--
			}
		case r == ';':
			statement++
		case r == '&':
			// A parameter name is not a keyword.
			for i+1 < len(runes) && isQueryWordRune(runes[i+1]) {
				i++
			}
		case isQueryWordRune(r):
			start := i
			for i+1 < len(runes) && isQueryWordRune(runes[i+1]) {
				i++
			}
			word := string(runes[start : i+1])
			out = append(out, queryToken{word: word, upper: strings.ToUpper(word), depth: depth, statement: statement})
		}
	}
	return out
}
