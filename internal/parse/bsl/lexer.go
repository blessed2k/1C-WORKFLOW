package bsl

import (
	"unicode"
	"unicode/utf8"
)

type tokenKind uint8

const (
	tokEOF tokenKind = iota
	tokIdent
	tokNumber
	tokString
	tokDate
	tokComment
	tokPunct
	tokDirective // &Имя — директива компиляции или аннотация расширения
	tokPreproc   // #Имя ... до конца строки
)

// span — байтовый полуинтервал во время разбора. В domain.Span он переводится
// на выходе: считать строку и колонку для каждого промежуточного токена дорого.
type span struct {
	start int
	end   int
}

type token struct {
	kind tokenKind
	sp   span
	lit  []byte // подслайс исходных байт, без копирования
}

// rawDiag — диагностика до перевода координат.
type rawDiag struct {
	code string
	msg  string
	sp   span
}

// lexer — толерантный лексер BSL: не останавливается на ошибке, а помечает её
// диагностикой и продолжает с ближайшей разумной позиции.
type lexer struct {
	src   []byte
	pos   int
	diags []rawDiag
}

func newLexer(src []byte) *lexer {
	l := &lexer{src: src}
	// BOM входит в байтовые смещения, но не является токеном.
	if hasBOM(src) {
		l.pos = 3
	}
	return l
}

func (l *lexer) errorf(code string, start, end int, msg string) {
	l.diags = append(l.diags, rawDiag{code: code, msg: msg, sp: span{start: start, end: end}})
}

func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func (l *lexer) peekRune() (rune, int) {
	if l.pos >= len(l.src) {
		return 0, 0
	}
	if c := l.src[l.pos]; c < utf8.RuneSelf {
		return rune(c), 1
	}
	return utf8.DecodeRune(l.src[l.pos:])
}

// next возвращает следующий токен. Комментарии возвращаются как токены:
// без них нельзя доказать, что ложные вызовы в комментариях отброшены.
func (l *lexer) next() token {
	l.skipSpace()
	if l.pos >= len(l.src) {
		return token{kind: tokEOF, sp: span{start: l.pos, end: l.pos}}
	}
	start := l.pos
	c := l.src[l.pos]

	switch {
	case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/':
		l.skipToEOL()
		return l.tok(tokComment, start)
	case c == '"':
		l.scanString()
		return l.tok(tokString, start)
	case c == '\'':
		l.scanDate()
		return l.tok(tokDate, start)
	case c == '#':
		l.pos++
		l.skipToEOL()
		return l.tok(tokPreproc, start)
	case c == '&':
		l.pos++
		l.scanIdentTail()
		return l.tok(tokDirective, start)
	case c >= '0' && c <= '9':
		l.scanNumber()
		return l.tok(tokNumber, start)
	}

	r, size := l.peekRune()
	if isIdentStart(r) {
		l.pos += size
		l.scanIdentTail()
		return l.tok(tokIdent, start)
	}

	// Двухсимвольные операторы.
	if l.pos+1 < len(l.src) {
		switch string(l.src[l.pos : l.pos+2]) {
		case "<=", ">=", "<>":
			l.pos += 2
			return l.tok(tokPunct, start)
		}
	}
	l.pos += size
	if size == 0 {
		l.pos++
	}
	return l.tok(tokPunct, start)
}

func (l *lexer) tok(k tokenKind, start int) token {
	return token{kind: k, sp: span{start: start, end: l.pos}, lit: l.src[start:l.pos]}
}

func (l *lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\r', '\n', '\v', '\f':
			l.pos++
		default:
			return
		}
	}
}

func (l *lexer) skipToEOL() {
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.pos++
	}
}

func (l *lexer) scanIdentTail() {
	for l.pos < len(l.src) {
		r, size := l.peekRune()
		if !isIdentPart(r) {
			return
		}
		l.pos += size
	}
}

func (l *lexer) scanNumber() {
	seenDot := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c >= '0' && c <= '9' {
			l.pos++
			continue
		}
		if c == '.' && !seenDot {
			seenDot = true
			l.pos++
			continue
		}
		return
	}
}

// scanString читает строковый литерал BSL: кавычка удваивается для экранирования,
// строка может продолжаться на следующей строке, если та начинается с '|'.
func (l *lexer) scanString() {
	start := l.pos
	l.pos++ // открывающая кавычка
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '"' {
				l.pos += 2
				continue
			}
			l.pos++
			return
		case '\n':
			// Продолжение допустимо, если следующая содержательная строка начинается
			// с '|'. Пустые строки внутри литерала платформа допускает: так написаны
			// тексты запросов в типовых конфигурациях, и они компилируются.
			save := l.pos
			l.pos++
			for l.pos < len(l.src) {
				switch c := l.src[l.pos]; {
				case c == ' ' || c == '\t' || c == '\r' || c == '\n':
					l.pos++
					continue
				case c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/':
					// Строка-комментарий между строками продолжения литерал не обрывает:
					// так написаны тексты запросов в типовых конфигурациях.
					l.skipToEOL()
					continue
				}
				break
			}
			if l.pos < len(l.src) && l.src[l.pos] == '|' {
				l.pos++
				continue
			}
			l.pos = save
			l.errorf(DiagUnclosedString, start, l.pos, "незакрытый строковый литерал")
			return
		default:
			l.pos++
		}
	}
	l.errorf(DiagUnclosedString, start, l.pos, "незакрытый строковый литерал в конце файла")
}

func (l *lexer) scanDate() {
	start := l.pos
	l.pos++
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\'':
			l.pos++
			return
		case '\n':
			l.errorf(DiagUnclosedDate, start, l.pos, "незакрытый литерал даты")
			return
		default:
			l.pos++
		}
	}
	l.errorf(DiagUnclosedDate, start, l.pos, "незакрытый литерал даты в конце файла")
}
