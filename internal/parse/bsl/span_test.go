package bsl

import "testing"

// Контракт координат зафиксирован в architecture-index.md §18.3:
//   - байтовые смещения 0-based, полуинтервал, по СЫРЫМ байтам файла;
//   - строки 1-based, границей строки считаются \n и \r\n, одиночный \r границей НЕ является;
//   - колонки 1-based в Unicode code points от начала строки;
//   - BOM входит в байтовые смещения, но исключается из счёта строк и колонок:
//     строка 1 колонка 1 — первый символ после BOM.
//
// Ожидаемые значения посчитаны руками по этому тексту спецификации, а не кодом.
func TestLineIndexПоКонтракту183(t *testing.T) {
	const bom = "\xef\xbb\xbf"

	tests := []struct {
		name     string
		src      string
		offset   int
		wantLine int
		wantCol  int
	}{
		{"начало файла", "Процедура", 0, 1, 1},
		{"кириллица считается в code points", "Процедура", 6, 1, 4}, // 3 руны по 2 байта
		{"первый символ после LF", "аб\nвг", 5, 2, 1},               // "аб"=4 байта, \n на 4
		{"второй символ после LF", "аб\nвг", 7, 2, 2},
		{"CRLF: конец первой строки", "аб\r\nвг", 4, 1, 3},
		{"CRLF: начало второй строки", "аб\r\nвг", 6, 2, 1},
		{"одиночный CR не разрывает строку", "аб\rвг", 5, 1, 4},
		{"BOM не сдвигает колонку", bom + "Аб", 3, 1, 1},
		{"BOM: смещение байтовое", bom + "Аб", 5, 1, 2},
		{"пустая строка", "а\n\nб", 3, 2, 1},
		{"строка после пустой", "а\n\nб", 4, 3, 1},
		{"конец файла", "аб", 4, 1, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			li := NewLineIndex([]byte(tt.src))
			line, col := li.At(tt.offset)
			if line != tt.wantLine || col != tt.wantCol {
				t.Fatalf("At(%d) = (%d,%d), ожидалось (%d,%d)", tt.offset, line, col, tt.wantLine, tt.wantCol)
			}
		})
	}
}

// Span собирается в domain.Span: байтовый полуинтервал плюс производные
// координаты обоих концов. Значения ниже посчитаны руками по тексту.
func TestLineIndexSpanЗаполняетОбаКонца(t *testing.T) {
	src := []byte("Процедура А()\nКонецПроцедуры\n")
	li := NewLineIndex(src)

	// "Процедура" — 9 рун по 2 байта, смещения [0,18).
	sp := li.Span(0, 18)
	if sp.StartByte != 0 || sp.EndByte != 18 {
		t.Fatalf("байтовые границы: %+v", sp)
	}
	if sp.StartLine != 1 || sp.StartCol != 1 || sp.EndLine != 1 || sp.EndCol != 10 {
		t.Fatalf("производные координаты: %+v, ожидалось 1:1..1:10", sp)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("span не проходит domain.Span.Validate: %v", err)
	}
	if got := string(src[sp.StartByte:sp.EndByte]); got != "Процедура" {
		t.Fatalf("src[start:end] = %q", got)
	}

	// Многострочный span: от начала файла до конца "КонецПроцедуры".
	full := li.Span(0, len(src)-1)
	if full.StartLine != 1 || full.EndLine != 2 || full.EndCol != 15 {
		t.Fatalf("многострочный span: %+v, ожидалось 1:1..2:15", full)
	}
}
