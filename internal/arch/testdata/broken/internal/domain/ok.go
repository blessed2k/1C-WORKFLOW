package domain

import "fmt"

// Span — честная часть фикстуры: только stdlib, нарушений давать не должна.
type Span struct {
	Start, End int
}

func (s Span) String() string { return fmt.Sprintf("%d..%d", s.Start, s.End) }
