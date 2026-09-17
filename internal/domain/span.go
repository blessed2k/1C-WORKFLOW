package domain

import "fmt"

// Span — позиция факта в исходном файле.
//
// Байтовые смещения — полуинтервал [StartByte, EndByte) с нумерацией от нуля:
// по ним берётся ровно тот текст, по которому построен факт. Строка и колонка
// нумеруются с единицы (как их показывает редактор), колонка считается в code
// points, а не в байтах: кириллица в UTF-8 занимает два байта, и колонка,
// посчитанная в байтах, указывала бы мимо символа.
type Span struct {
	StartByte int `json:"startByte"`
	EndByte   int `json:"endByte"`
	StartLine int `json:"startLine"`
	StartCol  int `json:"startCol"`
	EndLine   int `json:"endLine"`
	EndCol    int `json:"endCol"`
}

// IsZero сообщает, что позиция не задана (факт без места в файле).
func (s Span) IsZero() bool { return s == Span{} }

// Len возвращает длину фрагмента в байтах.
func (s Span) Len() int { return s.EndByte - s.StartByte }

// Validate проверяет инварианты позиции.
func (s Span) Validate() error {
	if s.IsZero() {
		return nil
	}
	if s.StartByte < 0 || s.EndByte < s.StartByte {
		return fmt.Errorf("span: байтовый полуинтервал [%d,%d) некорректен", s.StartByte, s.EndByte)
	}
	if s.StartLine < 1 || s.EndLine < 1 || s.StartCol < 1 || s.EndCol < 1 {
		return fmt.Errorf("span: строка и колонка нумеруются с единицы, получено %d:%d..%d:%d", s.StartLine, s.StartCol, s.EndLine, s.EndCol)
	}
	if s.EndLine < s.StartLine {
		return fmt.Errorf("span: конец (строка %d) раньше начала (строка %d)", s.EndLine, s.StartLine)
	}
	if s.EndLine == s.StartLine && s.EndCol < s.StartCol {
		return fmt.Errorf("span: конец (колонка %d) раньше начала (колонка %d) в строке %d", s.EndCol, s.StartCol, s.StartLine)
	}
	return nil
}
