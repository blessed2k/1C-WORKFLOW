package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// PostingReport is a review of how a document is posted. Posting is where the
// expensive defects live: a race on balances, movements that are never written,
// a query repeated once per row of a tabular section.
type PostingReport struct {
	Document string           `json:"document"`
	Style    string           `json:"style" jsonschema:"inline, delegated, none or absent"`
	Handlers []string         `json:"handlers,omitempty" jsonschema:"posting handlers found in the object module"`
	Findings []PostingFinding `json:"findings,omitempty"`
	Note     string           `json:"note,omitempty"`
}

// PostingFinding is one defect of the posting code.
type PostingFinding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Line    int    `json:"line,omitempty" jsonschema:"line in the object module, 1-based"`
}

// Posting styles. The rules that apply depend on the style: a configuration on
// БСП usually delegates posting to a mechanism (168 of 280 documents in УТ),
// forms no movements in the document module at all, and demanding inline locks
// or Записывать flags there would be pure noise.
const (
	postingInline    = "inline"    // movements are formed in the object module
	postingDelegated = "delegated" // posting is handed to a mechanism
	postingNone      = "none"      // handler exists but forms nothing
	postingAbsent    = "absent"    // no posting handler at all
)

// Movements (Движения, RegisterRecords) and the handlers themselves come from
// the BSL parser, see movementUses; the rest of the rules stay line-based.
var (
	// Delegation is recognised by the METHOD name, not the module name. Keying on
	// the module matched exactly one call in УТ (ПроведениеДокументов....) and left
	// 38 documents classified as "no movements at all", a lie on every seventh
	// document: ИнтеграцияИСПереопределяемый.ОбработкаПроведения,
	// ИнтеграцияИС.ЗаписатьНаборыЗаписей, ОстаткиАлкогольнойПродукцииЕГАИС.ОтразитьДвижения
	// live in modules whose names say nothing about posting.
	reDelegate = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])[\p{L}\d_]+\.[\p{L}\d_]*(?:Проведени|Движени|НаборыЗаписей)[\p{L}\d_]*\s*\(`)
	// RE2 \b is an ASCII word boundary and never matches after a Cyrillic letter,
	// so every boundary here is spelled out as "not a word character".
	reLock = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(?:УправлениеБлокировкойДанных|БлокировкаДанных|Заблокировать)(?:[^\p{L}\d_]|$)`)
	// Only a register table in a query text counts: a plain ".Остатки" is also a
	// field name, a data-set name and a property (verified in the УТ export).
	reBalanceRead = regexp.MustCompile(`(?i)(?:РегистрНакопления|AccumulationRegister)\.[\p{L}\d_]+\.Остатки(?:ИОбороты)?(?:[^\p{L}\d_]|$)`)
	reLoopStart   = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(?:Для|Пока)[^\p{L}\d_].*[^\p{L}\d_]Цикл(?:[^\p{L}\d_]|$)`)
	reLoopEnd     = regexp.MustCompile(`(?i)^\s*КонецЦикла`)
	reQueryRun    = regexp.MustCompile(`(?i)\.(?:Выполнить|ВыполнитьПакет)\s*\(`)
	reStringLit   = regexp.MustCompile(`"[^"]*"`)
)

// stripLiterals blanks string literals, so that BSL text quoted inside a query
// or a message cannot be read as code: a query text containing "Для ... Цикл"
// used to open a loop that never closes.
func stripLiterals(line string) string {
	return reStringLit.ReplaceAllString(line, `""`)
}

// PostingReview reviews the posting code of one document.
func (s *XMLSource) PostingReview(ctx context.Context, name string) (*PostingReport, error) {
	if name == "" {
		return nil, fmt.Errorf("document name is required")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("document name must be a metadata name, not a path")
	}
	var root xmlObjectRoot
	if err := readXML(filepath.Join(s.root, "Documents", name+".xml"), &root); err != nil {
		return nil, err
	}
	posts := root.Object.Properties.posts()
	out := &PostingReport{Document: "Документ." + name, Style: postingAbsent}

	data, err := os.ReadFile(filepath.Join(s.root, "Documents", name, "Ext", "ObjectModule.bsl"))
	if err != nil {
		out.Note = "модуль объекта отсутствует в выгрузке: проведение выполняется целиком механизмами конфигурации"
		return out, nil
	}
	text := string(stripBOM(data))
	lines := strings.Split(text, "\n")
	mod := parseModule(text)

	handlers := postingHandlerBodies(mod, lines)
	if len(handlers) > 0 {
		// Follow the calls into the module's own procedures before classifying:
		// the style and every rule below read the handler body, and posting is
		// routinely split out of the handler into private procedures.
		procs := moduleProcedures(mod, lines)
		for i := range handlers {
			handlers[i] = expandLocalCalls(handlers[i], procs)
		}
	}
	if len(handlers) == 0 {
		if posts {
			out.Note = "документ проводится, но обработчика проведения в модуле объекта нет: движения формируются подпиской или механизмом конфигурации"
		} else {
			out.Note = "проведение для документа запрещено свойством Проведение"
		}
		return out, nil
	}
	for _, h := range handlers {
		out.Handlers = append(out.Handlers, h.name)
	}
	out.Style = postingStyle(handlers)
	switch out.Style {
	case postingDelegated:
		out.Note = "проведение делегировано механизму конфигурации: движения формируются вне модуля объекта, проверять их состав надо там"
	case postingNone:
		out.Note = "обработчик есть, но движений не нашлось ни в нём, ни в процедурах модуля, которые он вызывает: либо документ не делает движений, либо они формируются подпиской или механизмом за пределами модуля"
	}

	// The flag may be set anywhere in the module, not only inside the handler.
	moduleText := text
	var movements *MovementsReport
	if out.Style == postingInline {
		movements, _ = s.Movements(ctx, name)
	}
	for _, h := range handlers {
		// Rules follow the style of THIS handler: clearing movements in
		// ОбработкаУдаленияПроведения is a normal idiom and must not drag the
		// delegated ОбработкаПроведения into the inline rule set.
		out.Findings = append(out.Findings, reviewHandler(h, handlerStyle(h), movements, moduleText)...)
	}
	return out, nil
}

// handlerBody is one posting handler with its position in the module. lineNos
// carries the real module line of every entry of lines: once the bodies of the
// called procedures are appended, the position inside the slice stops being the
// position in the file, and a finding must still point at the right line.
type handlerBody struct {
	name    string
	start   int // 1-based line of the declaration
	lines   []string
	lineNos []int
}

// postingHandlers names the posting handlers in both spellings of the language.
var postingHandlers = map[string]bool{
	"обработкапроведения": true, "обработкаудаленияпроведения": true,
	"posting": true, "undoposting": true,
}

// postingHandlerBodies extracts ОбработкаПроведения and ОбработкаУдаленияПроведения
// (Posting and UndoPosting).
func postingHandlerBodies(mod *bsl.Module, lines []string) []handlerBody {
	var out []handlerBody
	for _, m := range mod.Methods {
		if m.Kind != domain.SymbolProcedure || !postingHandlers[strings.ToLower(m.Name)] {
			continue
		}
		start, body := methodLines(mod, m, lines)
		h := handlerBody{name: m.Name, start: start, lines: body}
		for k := range body {
			h.lineNos = append(h.lineNos, start+k)
		}
		out = append(out, h)
	}
	return out
}

// methodLines returns the 1-based line of the declaration of m and its lines
// up to the closing keyword. A module truncated in the export has no
// КонецПроцедуры: the parser closes the method at the next declaration, whose
// line is not part of it, or the defects of a neighbour get blamed on posting.
func methodLines(mod *bsl.Module, m bsl.Method, lines []string) (int, []string) {
	start, end := m.NameSpan.StartLine, m.Span.EndLine
	if !m.Complete && m.Span.EndByte < len(mod.Source()) {
		end--
	}
	if start < 1 || end > len(lines) || end < start {
		return start, nil
	}
	return start, lines[start-1 : end]
}

// maxNestedDepth bounds how far the expansion follows calls inside the module.
// Posting split across more than three levels of private procedures is rare,
// and every extra level dilutes what the rules are looking at.
const maxNestedDepth = 3

// Calls to the module's own procedures are matched by reLocalCall from
// exchangeaudit.go: a bare identifier followed by "(", not preceded by a dot.
// A dot would make it Модуль.Метод, which is delegation and is recognised
// separately.

// moduleProcedure is a procedure of the module kept with its real first line.
type moduleProcedure struct {
	start int
	lines []string
}

// moduleProcedures indexes every procedure and function of the module by
// lower-cased name.
func moduleProcedures(mod *bsl.Module, lines []string) map[string]moduleProcedure {
	out := map[string]moduleProcedure{}
	for _, m := range mod.Methods {
		start, body := methodLines(mod, m, lines)
		out[strings.ToLower(m.Name)] = moduleProcedure{start: start, lines: body}
	}
	return out
}

// expandLocalCalls appends the bodies of the module procedures the handler
// calls, so the rules see the whole posting path instead of its first line.
// Splitting ОбработкаПроведения into private procedures of the same module is
// an ordinary refactor — in a real industry configuration the handler is
// three calls and every movement lives one level down — and reading only the
// handler body classified the document as "движений нет" and skipped every
// check in silence.
func expandLocalCalls(h handlerBody, procs map[string]moduleProcedure) handlerBody {
	out := h
	out.lines = append([]string(nil), h.lines...)
	out.lineNos = append([]int(nil), h.lineNos...)

	seen := map[string]bool{strings.ToLower(h.name): true}
	frontier := [][]string{h.lines}
	for depth := 0; depth < maxNestedDepth && len(frontier) > 0; depth++ {
		var next [][]string
		for _, body := range frontier {
			for _, raw := range body {
				line := stripLiterals(stripLineComment(raw))
				for _, m := range reLocalCall.FindAllStringSubmatch(line, -1) {
					key := strings.ToLower(m[1])
					if seen[key] {
						continue
					}
					p, ok := procs[key]
					if !ok {
						// Not a procedure of this module: a platform method, a
						// keyword or a constructor. Nothing to follow.
						continue
					}
					seen[key] = true
					for k, l := range p.lines {
						out.lines = append(out.lines, l)
						out.lineNos = append(out.lineNos, p.start+k)
					}
					next = append(next, p.lines)
				}
			}
		}
		frontier = next
	}
	return out
}

// postingStyle classifies the document as a whole: inline wins over delegated,
// because inline code is what the rules can actually check.
func postingStyle(handlers []handlerBody) string {
	style := postingNone
	for _, h := range handlers {
		switch handlerStyle(h) {
		case postingInline:
			return postingInline
		case postingDelegated:
			style = postingDelegated
		}
	}
	return style
}

// handlerStyle classifies one handler. The expanded body is a run of whole
// procedures, so it parses as a module of its own.
func handlerStyle(h handlerBody) string {
	for _, u := range movementUses(parseModule(strings.Join(h.lines, "\n"))) {
		if u.forms() {
			return postingInline
		}
	}
	for _, raw := range h.lines {
		if reDelegate.MatchString(stripLineComment(raw)) {
			return postingDelegated
		}
	}
	return postingNone
}

// reviewHandler applies the rules that make sense for the posting style.
func reviewHandler(h handlerBody, style string, movements *MovementsReport, moduleText string) []PostingFinding {
	var out []PostingFinding
	// The offset indexes the expanded body, which mixes lines of several
	// procedures: only lineNos knows where each of them really is.
	at := func(offset int) int {
		if offset >= 0 && offset < len(h.lineNos) {
			return h.lineNos[offset]
		}
		return h.start
	}

	var (
		lockAt       = -1 // line offset of the first lock
		readsBalance = -1 // line offset of the first balance read
		written      = map[string]bool{}
		filled       = map[string]bool{} // register sets actually filled here
		writesAll    bool                // Движения.Записать() somewhere in the module
		flagged      bool                // some Записывать = Истина somewhere in the module
		loopDepth    int
		queryInLoop  int
	)
	for i, raw := range h.lines {
		// String literals are stripped as well: a lock mentioned inside a message
		// or a query text is not a lock.
		line := stripLiterals(stripLineComment(raw))
		if lockAt < 0 && reLock.MatchString(line) {
			lockAt = i
		}
		if readsBalance < 0 && reBalanceRead.MatchString(line) {
			readsBalance = i
		}
		if reLoopEnd.MatchString(line) && loopDepth > 0 {
			loopDepth--
		}
		if reLoopStart.MatchString(line) {
			loopDepth++
		}
		if loopDepth > 0 && queryInLoop == 0 && reQueryRun.MatchString(line) {
			queryInLoop = i
		}
	}

	// The expanded body is a run of whole procedures and parses as a module of
	// its own. Writing the set explicitly is as good as the flag.
	for _, u := range movementUses(parseModule(strings.Join(h.lines, "\n"))) {
		switch {
		case u.setsWriteFlag(), u.writesSet():
			written[strings.ToLower(u.register)] = true
		case u.fills():
			filled[strings.ToLower(u.register)] = true
		}
	}
	for _, u := range movementUses(parseModule(moduleText)) {
		writesAll = writesAll || u.writesAll()
		flagged = flagged || u.setsWriteFlag()
	}

	if queryInLoop > 0 {
		out = append(out, PostingFinding{
			Code:    "QueryInLoop",
			Message: "запрос выполняется внутри цикла: на документе с большой табличной частью это столько обращений к базе, сколько строк",
			Line:    at(queryInLoop),
		})
	}
	// The rules below only make sense where the movements are formed here.
	if style != postingInline {
		return out
	}
	if readsBalance >= 0 && (lockAt < 0 || lockAt > readsBalance) {
		out = append(out, PostingFinding{
			Code:    "BalanceReadWithoutLock",
			Message: "остатки читаются без управляемой блокировки, поставленной ДО чтения: между чтением и записью движений другой сеанс успеет провести свой документ, контроль остатков пропустит минус",
			Line:    at(readsBalance),
		})
	}
	if movements != nil {
		for _, r := range movements.Registers {
			short := r.Register
			if _, nm, ok := strings.Cut(r.Register, "."); ok {
				short = nm
			}
			key := strings.ToLower(short)
			// Движения.Записать() пишет все наборы сразу, флаг тогда не нужен.
			if filled[key] && !written[key] && !writesAll && !flagged {
				out = append(out, PostingFinding{
					Code:    "NoWriteFlag",
					Message: fmt.Sprintf("для %s не найдено Движения.%s.Записывать = Истина: набор заполняется, но может не записаться", r.Register, short),
					Line:    at(0),
				})
			}
		}
	}
	return out
}
