package source

import (
	"bytes"
	"os"
	"strings"
	"unicode"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// parseModule reads a module with the BSL parser of the index layer, in memory
// and without the index. The raw analyzers take methods, the export flag and
// register accesses from it, so they answer from the same facts as
// get_module_structure and find_register_writes instead of a regular
// expression of their own: comments and string literals are already cut, and
// both spellings of the language are known.
//
// src must be BOM-free: the facts carry byte offsets and 1-based line numbers
// of exactly these bytes, and callers split the same text into lines.
func parseModule(src []byte) *bsl.Module {
	// The parser is tolerant: a broken module still yields every method it
	// could read, and its diagnostics are of no use to a raw report.
	mod, _ := bsl.Parse(src, bsl.Options{})
	return mod
}

// parseDeclarations is parseModule for a caller that needs only the methods:
// their names, export flags, declarations and bodies. References, queries
// and register accesses are not collected, which makes the parse much cheaper.
func parseDeclarations(src []byte) *bsl.Module {
	mod, _ := bsl.Parse(src, bsl.Options{SkipReferences: true})
	return mod
}

// moduleCache holds the modules one tool call has parsed, keyed by file path.
// A common module shared by many subscriptions (the БСП exchange handlers) is
// then read and parsed once per call, not once per handler. A module that
// cannot be read is kept as nil, so it is not retried either.
type moduleCache map[string]*bsl.Module

// module returns the parsed module at path, nil when the file cannot be read.
// Only the declarations are parsed: callers look for methods by name.
func (c moduleCache) module(path string) *bsl.Module {
	if mod, ok := c[path]; ok {
		return mod
	}
	var mod *bsl.Module
	if data, err := os.ReadFile(path); err == nil {
		mod = parseDeclarations(stripBOM(data))
	}
	c[path] = mod
	return mod
}

// methodIn returns the first method of the module named name. Method names are
// case-insensitive in 1C. A nil module has no methods.
func methodIn(mod *bsl.Module, name string) (bsl.Method, bool) {
	if mod == nil {
		return bsl.Method{}, false
	}
	for _, m := range mod.Methods {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return bsl.Method{}, false
}

// methodSource returns the whole source lines of a method: from the line of its
// first directive or annotation to the line of its closing keyword, trailing
// comment included, without the final line break. A method without the closing
// keyword ends before the line where the next declaration starts, its
// directives included, or at the end of the module.
func methodSource(mod *bsl.Module, m bsl.Method) string {
	src := mod.Source()
	start := bytes.LastIndexByte(src[:m.Span.StartByte], '\n') + 1
	end := len(src)
	if m.Complete {
		if i := bytes.IndexByte(src[m.Span.EndByte:], '\n'); i >= 0 {
			end = m.Span.EndByte + i
		}
		return string(src[start:end])
	}
	// The parser closes an unclosed method at the keyword of the next
	// declaration; the directives above that keyword belong to the next one.
	stop := m.Span.EndByte
	for _, next := range mod.Methods {
		if next.Span.StartByte > m.Span.StartByte && next.Span.StartByte < stop {
			stop = next.Span.StartByte
		}
	}
	if stop < len(src) {
		end = max(bytes.LastIndexByte(src[:stop], '\n'), start)
	}
	return string(src[start:end])
}

// bodyText returns the body of a method: the source between its declaration
// and its closing keyword.
func bodyText(mod *bsl.Module, m bsl.Method) string {
	return spanText(mod, m.BodySpan.StartByte, m.BodySpan.EndByte)
}

// interceptorOf returns the interceptor annotation of a method: its canonical
// kind and target. With several annotations the last one wins. ok is false
// for a method without an interceptor annotation whose target is a name.
func interceptorOf(m bsl.Method) (kind, target string, ok bool) {
	for _, a := range m.Annotations {
		k, known := canonicalKind[strings.ToLower(strings.TrimPrefix(a.Name, "&"))]
		if known && a.Arg != "" {
			kind, target, ok = k, a.Arg, true
		}
	}
	return kind, target, ok
}

// spanText returns the source between two byte offsets of the module.
func spanText(mod *bsl.Module, start, end int) string {
	return string(mod.Text(domain.Span{StartByte: start, EndByte: end}))
}

// methodDecl is the declaration of one method, as written, without comments.
type methodDecl struct {
	async   string // Асинх or Async, "" when absent
	keyword string // Процедура, Функция, Procedure or Function
	call    string // name and parameter list, whitespace runs collapsed
	export  string // Экспорт or Export, "" when not exported
}

// header is the declaration without the modifiers: "Процедура Имя(Параметр)".
func (d methodDecl) header() string { return d.keyword + " " + d.call }

// signature is the whole declaration: "Асинх Процедура Имя(Параметр) Экспорт".
func (d methodDecl) signature() string {
	var parts []string
	for _, p := range []string{d.async, d.keyword, d.call, d.export} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

// declarationOf rebuilds the declaration of m from the source. The parser
// knows where the name starts and where the body starts; the text in between
// is the parameter list and the export keyword, possibly wrapped over several
// lines and interleaved with comments.
func declarationOf(mod *bsl.Module, m bsl.Method) methodDecl {
	var d methodDecl
	// Before the name: directives, annotations, Асинх and the keyword.
	for _, word := range strings.Fields(withoutComments(spanText(mod, m.Span.StartByte, m.NameSpan.StartByte))) {
		switch {
		case equalsAny(word, bilingual("Асинх")...):
			d.async = word
		case equalsAny(word, bilingual("Процедура", "Функция")...):
			d.keyword = word
		}
	}
	if d.keyword == "" {
		d.keyword = "Процедура"
		if m.Kind == domain.SymbolFunction {
			d.keyword = "Функция"
		}
	}

	rest := withoutComments(spanText(mod, m.NameSpan.StartByte, m.BodySpan.StartByte))
	end := closingParen(rest)
	if end < 0 {
		d.call = strings.Join(strings.Fields(rest), " ")
		return d
	}
	d.call = strings.Join(strings.Fields(rest[:end+1]), " ")
	if m.Export {
		if after := strings.Fields(rest[end+1:]); len(after) > 0 {
			d.export = after[0]
		}
	}
	return d
}

// withoutComments cuts the //-comment off every line of text and joins the
// lines with a space.
func withoutComments(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = stripLineComment(strings.TrimRight(line, "\r"))
	}
	return strings.Join(lines, " ")
}

// closingParen returns the index of the ")" that closes the first "(" of s, or
// -1. Parentheses inside string literals do not count: a default value such as
// ")" must not end the parameter list.
func closingParen(s string) int {
	depth := 0
	inString := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inString = !inString
		case inString:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// equalsAny reports whether word is one of words, ignoring case.
func equalsAny(word string, words ...string) bool {
	for _, w := range words {
		if strings.EqualFold(word, w) {
			return true
		}
	}
	return false
}

// movementUse is one use of the movements collection of a document
// (Движения, RegisterRecords), as the parser reports it.
type movementUse struct {
	register string // the segment after the collection: a register or a method of the collection
	member   string // the segment after the register, "" when absent
	line     int    // 1-based line of the use
	method   int    // index of the method in Module.Methods, bsl.NoMethod outside methods
	call     bool   // the last segment is called: Движения.X.Добавить(
	assign   bool   // the last segment is assigned: Движения.X.Записывать =
	value    string // with assign, the first word of the assigned value
	boundVar string // Запись in "Запись = Движения.X.Добавить()", "" otherwise
}

// movementUses lists the uses of the movements collection in the module. Only
// the collection of the module's own object counts (a use that starts the
// expression), exactly as for find_register_writes in the index: comments and
// query texts in string literals are not code and never match.
func movementUses(mod *bsl.Module) []movementUse {
	src := string(mod.Source())
	var out []movementUse
	for _, ra := range mod.RegisterAccesses {
		if ra.Kind != bsl.AccessMovements {
			continue
		}
		u := movementUse{register: mod.Name(ra.NameSpan), line: ra.Span.StartLine, method: ra.Method}
		// The member is what follows the register: the use may start with
		// ЭтотОбъект, so the segments of the whole span are not counted.
		u.member = strings.Trim(spanText(mod, ra.NameSpan.EndByte, ra.Span.EndByte), ". \t\r\n")
		after := strings.TrimLeft(src[ra.Span.EndByte:], " \t")
		switch {
		case strings.HasPrefix(after, "("):
			u.call = true
		case strings.HasPrefix(after, "="):
			u.assign = true
			if value := strings.FieldsFunc(after[1:], func(r rune) bool { return !isIdentRune(r) }); len(value) > 0 {
				u.value = value[0]
			}
		}
		if u.fills() {
			u.boundVar = assignedVariable(src[:ra.Span.StartByte])
		}
		out = append(out, u)
	}
	return out
}

// assignedVariable returns Запись when the line ends with "Запись =" right
// before the use, that is when the use is the whole right side of an
// assignment at the start of a statement.
func assignedVariable(before string) string {
	line := before[strings.LastIndexByte(before, '\n')+1:]
	left, ok := strings.CutSuffix(strings.TrimSpace(line), "=")
	if !ok {
		return ""
	}
	name := strings.TrimSpace(left)
	if name == "" || strings.IndexFunc(name, func(r rune) bool { return !isIdentRune(r) }) >= 0 {
		return ""
	}
	return name
}

// isIdentRune reports whether r can be part of a BSL identifier.
func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// memberIs reports whether the segment after the register is one of words.
func (u movementUse) memberIs(words ...string) bool {
	return u.member != "" && equalsAny(u.member, words...)
}

// setsWriteFlag: Движения.X.Записывать = Истина.
func (u movementUse) setsWriteFlag() bool {
	return u.assign && u.memberIs(bilingual("Записывать")...) && equalsAny(u.value, bilingual("Истина")...)
}

// writesSet: Движения.X.Записать(), as good as raising the flag.
func (u movementUse) writesSet() bool {
	return u.call && u.memberIs(bilingual("Записать")...)
}

// writesAll: Движения.Записать() writes every set of the document at once.
func (u movementUse) writesAll() bool {
	return u.call && u.member == "" && equalsAny(u.register, bilingual("Записать")...)
}

// fills: the set gets records, Движения.X.Добавить() or Движения.X.Загрузить().
func (u movementUse) fills() bool {
	return u.call && u.memberIs(bilingual("Добавить", "Загрузить")...)
}

// forms: the use forms movements. Движения.X.ДополнительныеСвойства.Вставить()
// is NOT forming: it passes options to the mechanism that will post the
// document, and counting it as inline posting turns a delegated document into
// a false positive.
func (u movementUse) forms() bool {
	return (u.call || u.assign) && u.memberIs(bilingual(
		"Добавить", "Загрузить", "Очистить", "Записывать", "Записать", "Прочитать")...)
}

// isCollectionMethod: the segment after the collection is a method of the
// collection itself (Движения.Записать()), not a register.
func (u movementUse) isCollectionMethod() bool {
	return movCollectionMethods[strings.ToLower(u.register)]
}
