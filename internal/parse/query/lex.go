package query

import (
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// tokenKind — вид лексемы токенизатора текста запроса.
type tokenKind int

const (
	tokWord      tokenKind = iota // идентификатор или ключевое слово
	tokAmp                        // &
	tokDot                        // .
	tokComma                      // ,
	tokLParen                     // (
	tokRParen                     // )
	tokSemicolon                  // ;
	tokStar                       // *
	tokString                     // "..." (с удвоением кавычки как escape)
	tokGap                        // GapMarker — заглушка неизвестного фрагмента
	tokOther                      // всё остальное (операторы, числа, пунктуация)
)

// token — одна лексема с байтовыми границами [start,end) в исходном тексте.
type token struct {
	kind  tokenKind
	text  string // заполнено только для tokWord (исходное написание)
	start int
	end   int
}

// gapRange — диапазон незакрытого строкового литерала, для диагностики.
type unterminated struct {
	start, end int
}

// tokenize разбирает текст запроса на лексемы. Не паникует ни на каком входе:
// незакрытый строковый литерал завершается концом текста и попадает в
// возвращаемый список unterminated, а не роняет разбор.
func tokenize(text string) ([]token, []unterminated) {
	var toks []token
	var bad []unterminated
	n := len(text)
	i := 0
	for i < n {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f':
			i++
		case c == 0:
			start := i
			for i < n && text[i] == 0 {
				i++
			}
			toks = append(toks, token{kind: tokGap, start: start, end: i})
		case c == '/' && i+1 < n && text[i+1] == '/':
			j := indexByteFrom(text, '\n', i)
			if j < 0 {
				i = n
			} else {
				i = j
			}
		case c == '"':
			start := i
			i++
			closed := false
			for i < n {
				if text[i] == '"' {
					if i+1 < n && text[i+1] == '"' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			toks = append(toks, token{kind: tokString, start: start, end: i})
			if !closed {
				bad = append(bad, unterminated{start: start, end: i})
			}
		case c == '&':
			toks = append(toks, token{kind: tokAmp, start: i, end: i + 1})
			i++
		case c == '.':
			toks = append(toks, token{kind: tokDot, start: i, end: i + 1})
			i++
		case c == ',':
			toks = append(toks, token{kind: tokComma, start: i, end: i + 1})
			i++
		case c == '(':
			toks = append(toks, token{kind: tokLParen, start: i, end: i + 1})
			i++
		case c == ')':
			toks = append(toks, token{kind: tokRParen, start: i, end: i + 1})
			i++
		case c == ';':
			toks = append(toks, token{kind: tokSemicolon, start: i, end: i + 1})
			i++
		case c == '*':
			toks = append(toks, token{kind: tokStar, start: i, end: i + 1})
			i++
		default:
			r, size := utf8.DecodeRuneInString(text[i:])
			if size <= 0 {
				// невалидный UTF-8 байт — не паникуем, пропускаем один байт.
				i++
				continue
			}
			if isIdentStart(r) {
				start := i
				i += size
				for i < n {
					r2, size2 := utf8.DecodeRuneInString(text[i:])
					if size2 <= 0 || !isIdentPart(r2) {
						break
					}
					i += size2
				}
				toks = append(toks, token{kind: tokWord, text: text[start:i], start: start, end: i})
			} else {
				toks = append(toks, token{kind: tokOther, start: i, end: i + size})
				i += size
			}
		}
	}
	return toks, bad
}

func indexByteFrom(s string, b byte, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdentPart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// lineIndex переводит байтовое смещение в строку/колонку (контракт
// domain.Span: строки и колонки с единицы, колонка в code points). Строится
// один раз на весь текст запроса.
type lineIndex struct {
	starts []int // byte-смещение начала каждой строки, starts[0] == 0
}

func newLineIndex(text string) *lineIndex {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &lineIndex{starts: starts}
}

// at возвращает 1-based строку и колонку байтового смещения offset. offset
// вне [0,len(text)] обрезается к границам — вызывающий код (spanFor) передаёt
// только офсеты токенов, которые всегда внутри текста, но защита от паники на
// произвольном офсете дешева и оправдана для tolerant-парсера.
func (li *lineIndex) at(text string, offset int) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	i := sort.Search(len(li.starts), func(i int) bool { return li.starts[i] > offset }) - 1
	if i < 0 {
		i = 0
	}
	line = i + 1
	col = utf8.RuneCountInString(text[li.starts[i]:offset]) + 1
	return line, col
}

// spanFor строит domain.Span для полуинтервала [start,end) в тексте.
func spanFor(text string, li *lineIndex, start, end int) domain.Span {
	if end < start {
		end = start
	}
	sl, sc := li.at(text, start)
	el, ec := li.at(text, end)
	return domain.Span{StartByte: start, EndByte: end, StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec}
}
