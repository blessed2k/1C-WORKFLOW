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
// the BSL parser, see movementUses; the rest of the rules stay line-based and
// take the English spelling of their words from bslEnglish.
var (
	// Delegation is recognised by the METHOD name, not the module name. Keying on
	// the module matched exactly one call in УТ (ПроведениеДокументов....) and left
	// 38 documents classified as "no movements at all", a lie on every seventh
	// document: ИнтеграцияИСПереопределяемый.ОбработкаПроведения,
	// ИнтеграцияИС.ЗаписатьНаборыЗаписей, ОстаткиАлкогольнойПродукцииЕГАИС.ОтразитьДвижения
	// live in modules whose names say nothing about posting.
	reDelegate = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])[\p{L}\d_]+\.[\p{L}\d_]*(?:` + wordAlt("Проведени", "Движени", "НаборыЗаписей") + `)[\p{L}\d_]*\s*\(`)
	// RE2 \b is an ASCII word boundary and never matches after a Cyrillic letter,
	// so every boundary here is spelled out as "not a word character".
	reLock = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(?:УправлениеБлокировкойДанных|` + wordAlt("БлокировкаДанных", "Заблокировать") + `)(?:[^\p{L}\d_]|$)`)
	// Only a register table in a query text counts: a plain ".Остатки" is also a
	// field name, a data-set name and a property (verified in the УТ export).
	reBalanceRead = regexp.MustCompile(`(?i)(?:` + wordAlt("РегистрНакопления") + `)\.[\p{L}\d_]+\.(?:` + wordAlt("ОстаткиИОбороты", "Остатки") + `)(?:[^\p{L}\d_]|$)`)
	reLoopStart   = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(?:` + wordAlt("Для", "Пока") + `)[^\p{L}\d_].*[^\p{L}\d_](?:` + wordAlt("Цикл") + `)(?:[^\p{L}\d_]|$)`)
	reLoopEnd     = regexp.MustCompile(`(?i)^\s*(?:` + wordAlt("КонецЦикла") + `)`)
	reQueryRun    = regexp.MustCompile(`(?i)\.(?:` + wordAlt("Выполнить", "ВыполнитьПакет") + `)\s*\(`)
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
	// The module is parsed once: handlers, procedures and movements all come
	// from this one result.
	mod := parseModule(stripBOM(data))
	uses := movementUses(mod)
	moves := movesByMethod(uses)

	handlers := postingHandlerBodies(mod, lines, moves)
	if len(handlers) > 0 {
		// Follow the calls into the module's own procedures before classifying:
		// the style and every rule below read the handler body, and posting is
		// routinely split out of the handler into private procedures.
		procs := moduleProcedures(mod, lines, moves)
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
	writes := moduleWritesOf(uses)
	var movements *MovementsReport
	if out.Style == postingInline {
		movements, _ = s.movements(name, mod)
	}
	for _, h := range handlers {
		// Rules follow the style of THIS handler: clearing movements in
		// ОбработкаУдаленияПроведения is a normal idiom and must not drag the
		// delegated ОбработкаПроведения into the inline rule set.
		out.Findings = append(out.Findings, reviewHandler(h, handlerStyle(h), movements, writes)...)
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
	moves   []movementUse // movements of the handler and of the procedures it calls
}

// postingHandlers names the posting handlers in both spellings of the language.
var postingHandlers = bilingualSet("ОбработкаПроведения", "ОбработкаУдаленияПроведения")

// postingHandlerBodies extracts ОбработкаПроведения and ОбработкаУдаленияПроведения
// (Posting and UndoPosting).
func postingHandlerBodies(mod *bsl.Module, lines []string, moves map[int][]movementUse) []handlerBody {
	var out []handlerBody
	for i, m := range mod.Methods {
		if m.Kind != domain.SymbolProcedure || !postingHandlers[strings.ToLower(m.Name)] {
			continue
		}
		start, body := methodLines(mod, m, lines)
		h := handlerBody{name: m.Name, start: start, lines: body, moves: moves[i]}
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

// moduleWrites is what the whole module does to write its movements.
type moduleWrites struct {
	all     bool // Движения.Записать() somewhere in the module
	flagged bool // some Записывать = Истина somewhere in the module
}

func moduleWritesOf(uses []movementUse) moduleWrites {
	var w moduleWrites
	for _, u := range uses {
		w.all = w.all || u.writesAll()
		w.flagged = w.flagged || u.setsWriteFlag()
	}
	return w
}

// movesByMethod groups the movement uses by the method they are in.
func movesByMethod(uses []movementUse) map[int][]movementUse {
	out := map[int][]movementUse{}
	for _, u := range uses {
		out[u.method] = append(out[u.method], u)
	}
	return out
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
	moves []movementUse
}

// moduleProcedures indexes every procedure and function of the module by
// lower-cased name.
func moduleProcedures(mod *bsl.Module, lines []string, moves map[int][]movementUse) map[string]moduleProcedure {
	out := map[string]moduleProcedure{}
	for i, m := range mod.Methods {
		start, body := methodLines(mod, m, lines)
		out[strings.ToLower(m.Name)] = moduleProcedure{start: start, lines: body, moves: moves[i]}
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
	out.moves = append([]movementUse(nil), h.moves...)

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
					out.moves = append(out.moves, p.moves...)
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

// handlerStyle classifies one handler.
func handlerStyle(h handlerBody) string {
	for _, u := range h.moves {
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
func reviewHandler(h handlerBody, style string, movements *MovementsReport, writes moduleWrites) []PostingFinding {
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

	// Writing the set explicitly is as good as the flag.
	for _, u := range h.moves {
		switch {
		case u.setsWriteFlag(), u.writesSet():
			written[strings.ToLower(u.register)] = true
		case u.fills():
			filled[strings.ToLower(u.register)] = true
		}
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
			if filled[key] && !written[key] && !writes.all && !writes.flagged {
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
