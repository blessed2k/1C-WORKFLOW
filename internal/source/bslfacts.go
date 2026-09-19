package source

import (
	"strings"

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
// text must be BOM-free: the facts carry byte offsets and 1-based line numbers
// of exactly this text, and callers split the same text into lines.
func parseModule(text string) *bsl.Module {
	// The parser is tolerant: a broken module still yields every method it
	// could read, and its diagnostics are of no use to a raw report.
	mod, _ := bsl.Parse([]byte(text), bsl.Options{})
	return mod
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
		case equalsAny(word, "Асинх", "Async"):
			d.async = word
		case equalsAny(word, "Процедура", "Функция", "Procedure", "Function"):
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
