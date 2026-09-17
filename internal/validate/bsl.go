// Package validate checks finished BSL against the real configuration and the
// platform syntax reference.
//
// It exists because of how the other tools actually get used. Tools that fire on
// a completed artefact ("the file is written — check it") get called on their
// own; tools that require catching an intention ("I am about to write a query —
// fetch the schema first") lose to the writer's own certainty and stay unused.
// So the same knowledge is offered again at the point where the trigger works:
// on the finished text, before it is applied.
//
// Every check here must be provable from the sources. A false positive costs
// more than a miss: findings that turn out wrong teach the reader to skip the
// whole report, and then the real ones go unread too.
package validate

import (
	"regexp"
	"strings"
)

// Finding is one problem proved against the configuration or the platform index.
type Finding struct {
	Code       string `json:"code"`
	Severity   string `json:"severity" jsonschema:"high, medium or low"`
	Line       int    `json:"line"`
	Text       string `json:"text,omitempty" jsonschema:"the source line"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// MetadataLookup reports whether an object of the given metadata type exists in
// the active configuration.
type MetadataLookup func(metadataType, name string) bool

// Method describes a global-context platform method as the syntax reference
// knows it.
type Method struct {
	Signature string
	Required  int // parameters the platform marks обязательный
	Total     int // all declared parameters
}

// MethodLookup returns a global-context method by its exact Russian or English
// name. Anything that is not such a method must return ok=false: a local
// procedure or a BSP common-module call must never be judged against a platform
// signature.
type MethodLookup func(name string) (Method, bool)

// managerCollections maps a manager collection to the metadata type behind it.
// Only unambiguous ones are listed: a wrong mapping would invent errors.
var managerCollections = map[string]string{
	"Справочники":             "Catalog",
	"Документы":               "Document",
	"РегистрыСведений":        "InformationRegister",
	"РегистрыНакопления":      "AccumulationRegister",
	"РегистрыБухгалтерии":     "AccountingRegister",
	"РегистрыРасчета":         "CalculationRegister",
	"Перечисления":            "Enum",
	"Обработки":               "DataProcessor",
	"Отчеты":                  "Report",
	"ПланыВидовХарактеристик": "ChartOfCharacteristicTypes",
	"ПланыСчетов":             "ChartOfAccounts",
	"ПланыВидовРасчета":       "ChartOfCalculationTypes",
	"БизнесПроцессы":          "BusinessProcess",
	"Задачи":                  "Task",
	"ПланыОбмена":             "ExchangePlan",
	"Константы":               "Constant",
}

const b = `(?:^|[^\p{L}\d_.])`

var (
	reManagerRef = regexp.MustCompile(b + `(` + collectionsAlt() + `)\.([\p{L}\d_]+)`)
	reCallHead   = regexp.MustCompile(b + `([\p{L}\d_]+)\s*\(`)
	reMethodHead = regexp.MustCompile(`(?i)^\s*(Процедура|Функция)\s+([\p{L}\d_]+)`)
	reLineNo     = regexp.MustCompile(`\r$`)
)

func collectionsAlt() string {
	names := make([]string, 0, len(managerCollections))
	for k := range managerCollections {
		names = append(names, k)
	}
	// Longest first: РегистрыСведений must win over a shorter prefix.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if len(names[j]) > len(names[i]) {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return strings.Join(names, "|")
}

// BSL checks the module text and returns what is provably wrong.
func BSL(code string, meta MetadataLookup, method MethodLookup) []Finding {
	lines := strings.Split(code, "\n")
	local := localProcedures(lines)
	out := []Finding{}

	for i := 0; i < len(lines); i++ {
		raw := reLineNo.ReplaceAllString(lines[i], "")
		// Comments and string contents are not code: an object named in a message
		// is not a reference, and a comma inside a literal is not an argument.
		line := blankLiterals(stripComment(raw))
		if strings.TrimSpace(line) == "" {
			continue
		}
		logical, consumed := joinCall(lines, i, line)

		out = append(out, checkManagerRefs(logical, raw, i+1, meta)...)
		out = append(out, checkCallArity(logical, raw, i+1, local, method)...)

		i += consumed
	}
	return out
}

// checkManagerRefs proves a reference against the configuration: Справочники.X
// where X is not in the metadata is a name that cannot resolve at runtime —
// a typo, or an object renamed out from under the code.
func checkManagerRefs(line, raw string, lineNo int, meta MetadataLookup) []Finding {
	if meta == nil {
		return nil
	}
	var out []Finding
	for _, m := range reManagerRef.FindAllStringSubmatch(line, -1) {
		collection, name := m[1], m[2]
		mdType := managerCollections[collection]
		if mdType == "" || meta(mdType, name) {
			continue
		}
		out = append(out, Finding{
			Code:     "UnknownMetadataObject",
			Severity: "high",
			Line:     lineNo,
			Text:     strings.TrimSpace(raw),
			Message: "В конфигурации нет объекта " + collection + "." + name +
				" — обращение не разрешится в рантайме.",
			Suggestion: "Проверить имя по get_metadata_tree: объект переименован, отсутствует в этой конфигурации или опечатка.",
		})
	}
	return out
}

// checkCallArity compares a call against the platform signature. Only
// global-context methods are judged, and only when the module does not define a
// procedure of the same name — everything else could be a common-module or local
// call, and guessing there would produce exactly the noise this tool must avoid.
func checkCallArity(line, raw string, lineNo int, local map[string]bool, method MethodLookup) []Finding {
	if method == nil {
		return nil
	}
	var out []Finding
	for _, loc := range reCallHead.FindAllStringSubmatchIndex(line, -1) {
		name := line[loc[2]:loc[3]]
		if local[strings.ToLower(name)] || isKeyword(name) {
			continue
		}
		sig, ok := method(name)
		if !ok {
			continue // not a platform global method: nothing provable here
		}
		// loc[1] is the end of the whole match, i.e. just past the opening
		// parenthesis; loc[3] would stop at the name and hand countArgs the
		// parenthesis it has already accounted for.
		args, ok := countArgs(line[loc[1]:])
		if !ok {
			continue // call is cut off or unbalanced: no reliable count
		}
		switch {
		case args < sig.Required:
			out = append(out, Finding{
				Code:     "WrongArgCount",
				Severity: "high",
				Line:     lineNo,
				Text:     strings.TrimSpace(raw),
				Message: name + " вызван с " + plural(args) + ", обязательных — " +
					itoa(sig.Required) + ". Сигнатура: " + sig.Signature,
				Suggestion: "Сверить вызов с сигнатурой: bsl_syntax (query=" + name + ").",
			})
		case sig.Total > 0 && args > sig.Total:
			out = append(out, Finding{
				Code:     "WrongArgCount",
				Severity: "high",
				Line:     lineNo,
				Text:     strings.TrimSpace(raw),
				Message: name + " вызван с " + plural(args) + ", в сигнатуре параметров — " +
					itoa(sig.Total) + ". Сигнатура: " + sig.Signature,
				Suggestion: "Сверить вызов с сигнатурой: bsl_syntax (query=" + name + ").",
			})
		}
	}
	return out
}

// localProcedures collects the module's own procedure and function names: a
// module may define a name that also exists in the platform, and then the
// platform signature says nothing about the call.
func localProcedures(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, l := range lines {
		if m := reMethodHead.FindStringSubmatch(l); m != nil {
			out[strings.ToLower(m[2])] = true
		}
	}
	return out
}

// countArgs counts top-level arguments of a call whose opening parenthesis has
// already been consumed. Empty parentheses are zero arguments; a skipped
// optional argument (Метод(А, , Б)) still occupies its position.
func countArgs(s string) (int, bool) {
	depth, n := 1, 1
	empty := true
	for _, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
			if depth == 0 {
				if empty && n == 1 {
					return 0, true
				}
				return n, true
			}
		case ',':
			if depth == 1 {
				n++
			}
		default:
			if depth == 1 && !isSpace(r) {
				empty = false
			}
		}
	}
	return 0, false // unbalanced: say nothing rather than guess
}

// joinCall joins continuation lines while parentheses stay open, so a call split
// across lines is still counted as one.
func joinCall(lines []string, i int, first string) (string, int) {
	text := first
	depth := strings.Count(text, "(") - strings.Count(text, ")")
	consumed := 0
	for depth > 0 && i+consumed+1 < len(lines) && consumed < 20 {
		consumed++
		next := blankLiterals(stripComment(reLineNo.ReplaceAllString(lines[i+consumed], "")))
		text += " " + strings.TrimSpace(next)
		depth += strings.Count(next, "(") - strings.Count(next, ")")
	}
	return text, consumed
}

// stripComment removes a // comment, ignoring // inside a string literal.
func stripComment(line string) string {
	inStr := false
	for i := 0; i < len(line)-1; i++ {
		if line[i] == '"' {
			inStr = !inStr
			continue
		}
		if !inStr && line[i] == '/' && line[i+1] == '/' {
			return line[:i]
		}
	}
	return line
}

// blankLiterals empties string literals, keeping them as literals: text inside a
// message is not a metadata reference, and a comma inside it is not a separator.
func blankLiterals(line string) string {
	var out strings.Builder
	inStr := false
	for _, r := range line {
		if r == '"' {
			inStr = !inStr
			out.WriteRune(r)
			continue
		}
		if !inStr {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// isKeyword filters BSL constructs that look like a call but are not one.
func isKeyword(name string) bool {
	switch strings.ToLower(name) {
	case "если", "тогда", "иначе", "иначеесли", "конецесли", "цикл", "конеццикла",
		"для", "пока", "каждого", "из", "по", "процедура", "функция", "конецпроцедуры",
		"конецфункции", "возврат", "новый", "попытка", "исключение", "конецпопытки",
		"и", "или", "не", "экспорт", "перем", "выполнить", "вычислить":
		return true
	}
	return false
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func plural(n int) string {
	s := itoa(n) + " аргумент"
	switch {
	case n%10 == 1 && n%100 != 11:
		return s + "ом"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
		return s + "ами"
	default:
		return s + "ами"
	}
}
