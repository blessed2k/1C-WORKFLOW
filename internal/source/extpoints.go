package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ExtensionPointsReport lists the legal places to put custom code in a
// configuration on БСП, ranked against the task at hand. It exists because the
// default move — borrow the method into an extension and override it with
// &Вместо — is the most expensive one: it breaks on the next update and silently
// drops whatever the original method did.
type ExtensionPointsReport struct {
	Query  string           `json:"query"`
	Count  int              `json:"count"`
	Points []ExtensionPoint `json:"points"`
	Note   string           `json:"note"`
}

// ExtensionPoint is one exported procedure of an overridable module.
type ExtensionPoint struct {
	Module      string `json:"module" jsonschema:"e.g. ОбщегоНазначенияПереопределяемый"`
	Procedure   string `json:"procedure"`
	Line        int    `json:"line" jsonschema:"line of the declaration in the module, 1-based"`
	Signature   string `json:"signature" jsonschema:"the exported declaration, as written"`
	Context     string `json:"context" jsonschema:"сервер or клиент, by the module name"`
	Summary     string `json:"summary,omitempty" jsonschema:"first line of the doc comment"`
	Implemented bool   `json:"implemented" jsonschema:"the point is already used in this configuration"`
}

// extensionPointsNote is returned with every report: the ranking answers "where
// can I put this", the note answers "why not just override the method".
const extensionPointsNote = "Порядок выбора: переопределяемый модуль -> механизм БСП (подключаемые команды, дополнительные реквизиты, дополнительные отчёты и обработки) -> подписка на событие -> и только в последнюю очередь перехватчик в расширении. " +
	"&Вместо без ПродолжитьВызов не выполняет базовый метод, а в нём обычно висит обвязка БСП (печать, подключаемые команды), поэтому ломает её молча. " +
	"Помимо переопределяемых модулей у БСП есть модули интеграции подсистем (ИнтеграцияПодсистем*), где библиотеки подписываются на события друг друга: если подходящей точки ниже нет, смотри их. " +
	"Точка с implemented=true уже используется в этой конфигурации: смотри её текущий код, чтобы не затереть чужую логику."

// ExtensionPoints ranks the overridable procedures of the configuration against
// a free-text task description.
func (s *XMLSource) ExtensionPoints(ctx context.Context, query string, limit int) (*ExtensionPointsReport, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required: describe the task, e.g. \"печать\" or \"заполнение документа\"")
	}
	switch {
	case limit <= 0:
		limit = 15
	case limit > 100:
		limit = 100
	}
	terms := searchTerms(query)
	if len(terms) == 0 {
		return nil, fmt.Errorf("query has no searchable words")
	}

	entries, err := os.ReadDir(filepath.Join(s.root, "CommonModules"))
	if err != nil {
		return nil, fmt.Errorf("no CommonModules in the export: %w", err)
	}
	type scored struct {
		point ExtensionPoint
		score int
	}
	var found []scored
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.IsDir() || !isOverridableModule(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, "CommonModules", e.Name(), "Ext", "Module.bsl"))
		if err != nil {
			continue
		}
		for _, p := range parseExportedProcedures(string(stripBOM(data))) {
			p.Module = e.Name()
			p.Context = moduleContext(e.Name())
			if score := scorePoint(p, terms); score > 0 {
				found = append(found, scored{point: p, score: score})
			}
		}
	}
	// Best match first; ties broken by name so the output is stable.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		if found[i].point.Module != found[j].point.Module {
			return found[i].point.Module < found[j].point.Module
		}
		return found[i].point.Procedure < found[j].point.Procedure
	})

	out := &ExtensionPointsReport{Query: query, Count: len(found), Points: []ExtensionPoint{}, Note: extensionPointsNote}
	for i, f := range found {
		if i >= limit {
			break
		}
		out.Points = append(out.Points, f.point)
	}
	return out, nil
}

// isOverridableModule reports whether a common module is an extension point of
// БСП. The convention is the name suffix; both spellings appear in exports.
func isOverridableModule(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "переопределяемый") || strings.Contains(lower, "overridable")
}

// moduleContext tells server modules from client ones by name, which is the
// convention БСП follows and the only signal available without reading the
// module's own XML for every candidate.
func moduleContext(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "клиентсервер"), strings.Contains(lower, "clientserver"):
		return "клиент-сервер"
	case strings.Contains(lower, "клиент"), strings.Contains(lower, "client"):
		return "клиент"
	default:
		return "сервер"
	}
}

// searchTerms splits a free-text query into search stems, dropping the one- and
// two-letter noise ("в", "по", "на").
//
// The stem matters: metadata names are nouns in the nominative case
// (УправлениеПечатью, РегламентныеЗадания), a task is described in whatever case
// fits the sentence ("добавить печатную форму", "регламентное задание"), and a
// plain substring match finds neither. Cutting the ending off the query word is
// crude, but it turns "печатную" into "печат" and finds Печать.
func searchTerms(query string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !isWordRune(r)
	}) {
		if r := []rune(w); len(r) > 2 {
			out = append(out, stem(r))
		}
	}
	return out
}

// isWordRune reports whether r can be part of a searchable word.
func isWordRune(r rune) bool {
	return r == '_' || (r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') || r == 'ё' || r == 'Ё'
}

// stem cuts the inflectional ending off a word: up to three runes, never below
// four, which keeps short words ("ввод", "цена") intact.
func stem(word []rune) string {
	cut := len(word) - 4
	if cut > 3 {
		cut = 3
	}
	if cut < 0 {
		cut = 0
	}
	return string(word[:len(word)-cut])
}

// scorePoint weighs a point against the query terms: the procedure name is the
// strongest signal of what a point is for, the doc comment the weakest.
func scorePoint(p ExtensionPoint, terms []string) int {
	proc := strings.ToLower(p.Procedure)
	mod := strings.ToLower(p.Module)
	sum := strings.ToLower(p.Summary)
	score := 0
	for _, t := range terms {
		if strings.Contains(proc, t) {
			score += 3
		}
		if strings.Contains(mod, t) {
			score += 2
		}
		if strings.Contains(sum, t) {
			score++
		}
	}
	return score
}

// parseExportedProcedures returns the exported procedures and functions of a
// module together with the doc comment above each one and whether its body has
// any code at all.
//
// A declaration may span several lines: when the parameters are wrapped, the
// Экспорт keyword ends up on a later line. Looking for it on the header line
// alone lost 197 of the 3320 points in УТ, silently — including the whole
// "интеграция с сайтом" group.
func parseExportedProcedures(module string) []ExtensionPoint {
	lines := strings.Split(module, "\n")
	var out []ExtensionPoint
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		h := reMethodHead.FindStringSubmatch(line)
		if h == nil {
			continue
		}
		decl, end := declaration(lines, i)
		if !reExportKeyword.MatchString(decl) {
			continue
		}
		out = append(out, ExtensionPoint{
			Procedure:   h[2],
			Line:        i + 1,
			Signature:   strings.Join(strings.Fields(decl), " "),
			Summary:     docSummary(lines, i),
			Implemented: hasBody(lines, end),
		})
	}
	return out
}

// reExportKeyword matches the Экспорт keyword as a word, outside a comment. The
// declaration text passed to it is already comment-free.
var reExportKeyword = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(?:Экспорт|Export)(?:[^\p{L}\d_]|$)`)

// declaration collects the whole declaration starting at line i: the header plus
// any wrapped parameter lines, up to and including the line that closes the
// parameter list. Comments are stripped, so a trailing "// не экспорт" cannot be
// mistaken for the keyword. Returns the text and the index of its last line.
func declaration(lines []string, i int) (string, int) {
	var b strings.Builder
	depth := 0
	for j := i; j < len(lines) && j < i+20; j++ {
		line := stripLineComment(strings.TrimRight(lines[j], "\r"))
		b.WriteString(line)
		b.WriteByte(' ')
		depth += strings.Count(line, "(") - strings.Count(line, ")")
		if depth <= 0 && strings.Contains(line, ")") {
			return b.String(), j
		}
	}
	return b.String(), i
}

// docSummary returns the first meaningful line of the comment block directly
// above the declaration at index i.
func docSummary(lines []string, i int) string {
	var block []string
	for j := i - 1; j >= 0; j-- {
		text := strings.TrimSpace(strings.TrimRight(lines[j], "\r"))
		if text == "" && len(block) == 0 {
			continue // one blank line between the comment and the declaration
		}
		if !strings.HasPrefix(text, "//") {
			break
		}
		block = append([]string{strings.TrimSpace(strings.TrimPrefix(text, "//"))}, block...)
	}
	for _, text := range block {
		if text == "" || strings.HasPrefix(text, "//") {
			continue // blank line or a //////// separator
		}
		// A block that opens with a section header carries no description at all:
		// what follows is a parameter, not a summary of the point.
		if strings.HasPrefix(text, "Параметры") || strings.HasPrefix(text, "Возвращаемое") {
			return ""
		}
		return text
	}
	return ""
}

// hasBody reports whether the method whose declaration ends at index i has any
// logic in it, as opposed to being an empty БСП stub.
//
// A bare "Возврат;" is a stub too: it is how БСП writes an empty procedure that
// must not fall through. Counting it as implemented mislabelled 195 points in
// УТ, each of them telling the caller to go and read code that is not there.
// A function returning a value ("Возврат Ложь;") is logic and stays implemented.
func hasBody(lines []string, i int) bool {
	for j := i + 1; j < len(lines); j++ {
		raw := strings.TrimRight(lines[j], "\r")
		if isMethodEnd(raw) || reMethodHead.MatchString(raw) {
			return false
		}
		text := strings.TrimSpace(stripLineComment(raw))
		if text == "" || reBareReturn.MatchString(text) {
			continue
		}
		return true
	}
	return false
}

// reBareReturn matches a return with no value.
var reBareReturn = regexp.MustCompile(`(?i)^(?:Возврат|Return)\s*;?$`)
