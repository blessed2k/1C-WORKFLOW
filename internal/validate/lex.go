package validate

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// litMark opens the id of a string literal in the code view of a line: the
// literal "текст" becomes "<id>". The text itself is not code (an object
// named in a message is not a reference, a comma in it is not a separator), but
// a check that needs the literal (a query text, a СтрШаблон template) finds it
// by the id.
const litMark = ''

type lineKind int

const (
	lineCode lineKind = iota
	lineBlank
	linePreproc    // #Если, #Область
	lineAnnotation // &НаСервере, &Перед("...")
)

// srcLine is one source line as the checks see it.
type srcLine struct {
	no   int    // 1-based
	raw  string // the source text without \r
	code string // comments cut, string contents replaced by litMark+id, date literals emptied
	kind lineKind
}

// literal is a string literal; a multi-line one is joined with "\n", the
// leading | of each continuation line dropped and "" read as one quote.
type literal struct {
	line int
	text string
	// lineNos: the source line of each "\n"-separated line of text; a
	// commented line inside the literal is not in text, so counting newlines
	// alone would drift.
	lineNos []int
}

// lineOf returns the source line of byte offset off of the literal's text.
func (l literal) lineOf(off int) int {
	n := strings.Count(l.text[:off], "\n")
	if n < len(l.lineNos) {
		return l.lineNos[n]
	}
	return l.line + n
}

// scan splits a module into lines with a code view each, and collects its
// string literals. It follows a literal across lines: a line that continues
// one starts with |, and a // line in the middle of it is a comment (query
// texts keep commented-out lines that way).
func scan(code string) ([]srcLine, []literal) {
	rawLines := strings.Split(code, "\n")
	lines := make([]srcLine, 0, len(rawLines))
	var lits []literal
	inStr := false
	var cur strings.Builder
	curLine := 0
	var curNos []int
	closeLit := func() {
		lits = append(lits, literal{line: curLine, text: cur.String(), lineNos: curNos})
		cur.Reset()
		curNos = nil
		inStr = false
	}

	for i, raw := range rawLines {
		raw = strings.TrimSuffix(raw, "\r")
		var out strings.Builder
		rs := []rune(raw)
		j := 0
		if inStr {
			t := strings.TrimLeft(raw, " \t")
			switch {
			case strings.HasPrefix(t, "//"):
				lines = append(lines, srcLine{no: i + 1, raw: raw, kind: lineBlank})
				continue
			case strings.HasPrefix(t, "|"):
				j = len([]rune(raw)) - len([]rune(t)) + 1
				cur.WriteByte('\n')
				curNos = append(curNos, i+1)
			default:
				// A literal left open without a continuation: the source is
				// broken; close it and read this line as code.
				closeLit()
			}
		}
		for ; j < len(rs); j++ {
			r := rs[j]
			if inStr {
				if r == '"' {
					if j+1 < len(rs) && rs[j+1] == '"' {
						cur.WriteRune('"')
						j++
						continue
					}
					out.WriteRune('"')
					closeLit()
					continue
				}
				cur.WriteRune(r)
				continue
			}
			switch {
			case r == '"':
				inStr = true
				curLine = i + 1
				curNos = []int{i + 1}
				out.WriteRune('"')
				out.WriteRune(litMark)
				out.WriteString(strconv.Itoa(len(lits)))
			case r == '/' && j+1 < len(rs) && rs[j+1] == '/':
				j = len(rs)
			case r == '\'':
				// Date literal '20240101': no code inside.
				k := j + 1
				for k < len(rs) && rs[k] != '\'' {
					k++
				}
				out.WriteString("''")
				j = k
			default:
				out.WriteRune(r)
			}
		}
		l := srcLine{no: i + 1, raw: raw, code: out.String()}
		t := strings.TrimSpace(l.code)
		switch {
		case t == "":
			l.kind = lineBlank
		case strings.HasPrefix(t, "#"):
			l.kind = linePreproc
		case strings.HasPrefix(t, "&"):
			l.kind = lineAnnotation
		default:
			l.kind = lineCode
		}
		lines = append(lines, l)
	}
	if inStr {
		closeLit()
	}
	markDeleted(lines)
	return lines, lits
}

var (
	reDeleteStart = regexp.MustCompile(`(?i)^\s*#\s*(удаление|delete)` + wordEnd)
	reDeleteEnd   = regexp.MustCompile(`(?i)^\s*#\s*(конецудаления|enddelete)` + wordEnd)
)

// markDeleted takes the lines between #Удаление and #КонецУдаления out of the
// code: in a method with &ИзменениеИКонтроль they are the base text the
// extension removes, and they are not compiled.
func markDeleted(lines []srcLine) {
	in := false
	for i := range lines {
		switch {
		case reDeleteStart.MatchString(lines[i].code):
			in = true
		case reDeleteEnd.MatchString(lines[i].code):
			in = false
		case in:
			lines[i].kind = linePreproc
		}
	}
}

// literalAt reads the literal whose marker starts at s ("\"<id>..."),
// returning its index.
func literalAt(s string) (int, bool) {
	rs := []rune(s)
	if len(rs) < 3 || rs[0] != '"' || rs[1] != litMark {
		return 0, false
	}
	k := 2
	for k < len(rs) && rs[k] >= '0' && rs[k] <= '9' {
		k++
	}
	n, err := strconv.Atoi(string(rs[2:k]))
	return n, err == nil
}

// proc is a procedure or function of the checked text with what is declared
// inside it.
type proc struct {
	name       string
	start      int // line of the header
	end        int // line of КонецПроцедуры/КонецФункции; 0 while open
	params     map[string]bool
	locals     map[string]bool // Перем inside the method
	localLines map[string]int  // the line of each Перем
	assigned   map[string]bool // names first written by assignment: implicit locals
	directive  string          // &НаКлиенте and the like, lower-cased, without &
	export     bool
}

// module is the analysed text.
type module struct {
	lines      []srcLine
	lits       []literal
	methods    []*proc
	declared   map[string]*proc // lower name -> method
	moduleVars map[string]bool  // Перем outside methods
	stmts      []stmt
}

// stmt is the start of a statement: the code a statement begins with, on its
// line. Only statement starts can be assignments; a line that continues a
// condition (Или Документы = Неопределено Тогда) is not one.
type stmt struct {
	line int
	text string
	m    *proc
}

const ident = `[\p{L}_][\p{L}\d_]*`

// wordEnd ends a word: Go's \b knows only ASCII letters, and a Cyrillic
// keyword followed by the end of the line would not match it.
const wordEnd = `(?:$|[^\p{L}\d_])`

var (
	reHeader   = regexp.MustCompile(`(?i)^\s*(?:асинх\s+|async\s+)?(процедура|функция|procedure|function)\s+(` + ident + `)\s*\(`)
	reEnd      = regexp.MustCompile(`(?i)^\s*(конецпроцедуры|конецфункции|endprocedure|endfunction)` + wordEnd)
	reVar      = regexp.MustCompile(`(?i)^\s*(?:перем|var)\s+(.+)$`)
	reAssign   = regexp.MustCompile(`^\s*(?:~` + ident + `:\s*)?(` + ident + `)\s*=`)
	reForEach  = regexp.MustCompile(`(?i)^\s*(?:~` + ident + `:\s*)?(?:для|for)\s+(?:каждого|each)\s+(` + ident + `)\s+(?:из|in)` + wordEnd)
	reForTo    = regexp.MustCompile(`(?i)^\s*(?:~` + ident + `:\s*)?(?:для|for)\s+(` + ident + `)\s*=`)
	reExport   = regexp.MustCompile(`(?i)\)\s*(экспорт|export)` + wordEnd)
	reDirectiv = regexp.MustCompile(`(?i)^\s*&\s*([\p{L}]+)\s*$`)
	reStmtKw   = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(тогда|then|цикл|do|иначе|else|попытка|try|исключение|except)(?:$|[^\p{L}\d_])`)
)

// blockEnders end a statement without a semicolon.
var blockEnders = map[string]bool{
	"тогда": true, "then": true, "цикл": true, "do": true, "иначе": true, "else": true,
	"попытка": true, "try": true, "исключение": true, "except": true,
	"конецесли": true, "endif": true, "конеццикла": true, "enddo": true,
	"конецпопытки": true, "endtry": true, "конецпроцедуры": true, "endprocedure": true,
	"конецфункции": true, "endfunction": true, "экспорт": true, "export": true,
}

// analyze scans the text and finds its methods, their parameters and local
// declarations, and the statement starts.
func analyze(code string) *module {
	lines, lits := scan(code)
	m := &module{lines: lines, lits: lits, declared: map[string]*proc{}, moduleVars: map[string]bool{}}

	var cur *proc
	var header strings.Builder // parameter text of a header still open
	inHeader := false
	directive := ""
	cont := false // the previous statement goes on

	for _, l := range lines {
		switch l.kind {
		case lineBlank, linePreproc:
			continue
		case lineAnnotation:
			if d := reDirectiv.FindStringSubmatch(l.code); d != nil {
				directive = strings.ToLower(d[1])
			}
			continue
		}
		code := l.code
		if inHeader {
			header.WriteString(" " + code)
			if closed, params, rest := headerClosed(header.String()); closed {
				finishHeader(cur, params, rest)
				inHeader = false
				cont = false
			}
			continue
		}
		if h := reHeader.FindStringSubmatchIndex(code); h != nil {
			name := code[h[4]:h[5]]
			cur = &proc{
				name: name, start: l.no, params: map[string]bool{}, locals: map[string]bool{},
				localLines: map[string]int{}, assigned: map[string]bool{}, directive: directive,
			}
			directive = ""
			m.methods = append(m.methods, cur)
			m.declared[strings.ToLower(name)] = cur
			header.Reset()
			header.WriteString(code[h[1]:])
			if closed, params, rest := headerClosed(header.String()); closed {
				finishHeader(cur, params, rest)
				cont = false
			} else {
				inHeader = true
			}
			continue
		}
		directive = ""
		if reEnd.MatchString(code) {
			if cur != nil {
				cur.end = l.no
			}
			cur = nil
			cont = false
			continue
		}
		if v := reVar.FindStringSubmatch(code); v != nil && !cont {
			for _, n := range splitTop(strings.TrimSuffix(strings.TrimSpace(v[1]), ";")) {
				f := strings.Fields(n)
				if len(f) == 0 {
					continue
				}
				name := strings.ToLower(f[0])
				if cur != nil {
					cur.locals[name] = true
					cur.localLines[name] = l.no
				} else {
					m.moduleVars[name] = true
				}
			}
			cont = false
			continue
		}

		record := func(text string) {
			if strings.TrimSpace(text) == "" {
				return
			}
			m.stmts = append(m.stmts, stmt{line: l.no, text: text, m: cur})
			if cur != nil {
				if a := reAssign.FindStringSubmatch(text); a != nil {
					cur.assigned[strings.ToLower(a[1])] = true
				}
			}
		}
		parts := strings.Split(code, ";")
		for k, seg := range parts {
			if k > 0 || !cont {
				record(seg)
			}
			if loc := reStmtKw.FindAllStringSubmatchIndex(seg, -1); loc != nil {
				// Если А Тогда Б = 1: what follows the last block keyword
				// starts a statement of its own.
				record(seg[loc[len(loc)-1][3]:])
			}
		}
		last := strings.TrimSpace(parts[len(parts)-1])
		cont = last != "" && !blockEnders[strings.ToLower(lastWord(last))]
	}
	return m
}

// headerClosed reports whether the parameter list of a header (the text after
// its opening parenthesis) is closed: the parameters and what follows the
// closing parenthesis.
func headerClosed(s string) (closed bool, params, rest string) {
	depth := 1
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return true, s[:i], s[i+1:]
			}
		}
	}
	return false, "", ""
}

func finishHeader(m *proc, params, rest string) {
	for _, p := range splitTop(params) {
		f := strings.Fields(p)
		if len(f) == 0 {
			continue
		}
		name := f[0]
		if strings.EqualFold(name, "знач") || strings.EqualFold(name, "val") {
			if len(f) < 2 {
				continue
			}
			name = f[1]
		}
		if i := strings.Index(name, "="); i >= 0 {
			name = name[:i]
		}
		if name != "" {
			m.params[strings.ToLower(name)] = true
		}
	}
	m.export = reExport.MatchString(")" + rest)
}

// splitTop splits by commas outside parentheses.
func splitTop(s string) []string {
	var out []string
	depth, from := 0, 0
	for i, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[from:i])
				from = i + 1
			}
		}
	}
	return append(out, s[from:])
}

func lastWord(s string) string {
	rs := []rune(s)
	start := len(rs)
	for start > 0 && isIdentRune(rs[start-1]) {
		start--
	}
	return string(rs[start:])
}

func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// methodAt returns the method a line belongs to.
func (m *module) methodAt(line int) *proc {
	for _, md := range m.methods {
		if line >= md.start && (md.end == 0 || line <= md.end) {
			return md
		}
	}
	return nil
}

// isLocal reports a name declared in the method (parameter, Перем) or at the
// module level (Перем).
func (m *module) isLocal(md *proc, lower string) bool {
	if m.moduleVars[lower] {
		return true
	}
	return md != nil && (md.params[lower] || md.locals[lower])
}
