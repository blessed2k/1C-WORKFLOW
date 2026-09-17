// Package bsl — точный tolerant-парсер BSL по решению ADR-3 (кандидат (d)):
// собственный лексер и парсер на Go, без внешних зависимостей, с координатами
// по контракту architecture-index.md §18.3. bsl-language-server остаётся
// оракулом в тестах и в runtime не подключается.
package bsl

import (
	"unicode/utf8"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// BOM — маркер порядка байтов UTF-8. По §18.3 входит в байтовые смещения,
// но исключается из счёта строк и колонок.
var BOM = []byte{0xEF, 0xBB, 0xBF}

// hasBOM сообщает, начинается ли файл с BOM.
func hasBOM(src []byte) bool {
	return len(src) >= 3 && src[0] == BOM[0] && src[1] == BOM[1] && src[2] == BOM[2]
}

// LineIndex переводит байтовое смещение в строку и колонку по контракту §18.3.
// Строки 1-based, границей строки считаются \n и \r\n; одиночный \r границей не является.
// Колонки 1-based в Unicode code points от начала строки.
type LineIndex struct {
	src []byte
	// lineStarts[i] — байтовое смещение начала строки i+1.
	lineStarts []int
}

// NewLineIndex строит индекс строк по сырым байтам файла.
func NewLineIndex(src []byte) *LineIndex {
	li := &LineIndex{src: src}
	start := 0
	if hasBOM(src) {
		// BOM не участвует в счёте колонок: строка 1 начинается после него.
		start = 3
	}
	li.lineStarts = append(li.lineStarts, start)
	for i := start; i < len(src); i++ {
		if src[i] == '\n' {
			li.lineStarts = append(li.lineStarts, i+1)
		}
	}
	return li
}

// At возвращает 1-based строку и 1-based колонку в code points для байтового смещения.
// Смещения внутри BOM и до начала первой строки приводятся к строке 1 колонке 1.
func (li *LineIndex) At(offset int) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(li.src) {
		offset = len(li.src)
	}
	// Двоичный поиск последней строки, начинающейся не позже offset.
	lo, hi := 0, len(li.lineStarts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if li.lineStarts[mid] <= offset {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	lineStart := li.lineStarts[lo]
	if offset < lineStart {
		return 1, 1
	}
	return lo + 1, utf8.RuneCount(li.src[lineStart:offset]) + 1
}

// Span собирает domain.Span по байтовому полуинтервалу: первичные координаты —
// смещения, производные — строка и колонка обоих концов.
func (li *LineIndex) Span(start, end int) domain.Span {
	if start < 0 {
		start = 0
	}
	if end > len(li.src) {
		end = len(li.src)
	}
	if end < start {
		end = start
	}
	sl, sc := li.At(start)
	el, ec := li.At(end)
	return domain.Span{
		StartByte: start, EndByte: end,
		StartLine: sl, StartCol: sc,
		EndLine: el, EndCol: ec,
	}
}

// LineCount — число строк в файле.
func (li *LineIndex) LineCount() int { return len(li.lineStarts) }
