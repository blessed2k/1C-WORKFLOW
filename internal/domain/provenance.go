package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// ProvenanceSource — способ, которым получен факт.
type ProvenanceSource string

const (
	// SourceBSLParser — разбор BSL точным парсером.
	SourceBSLParser ProvenanceSource = "parser-bsl"
	// SourceXML — чтение XML метаданных.
	SourceXML ProvenanceSource = "parser-xml"
	// SourceQueryParser — разбор текста запроса 1С.
	SourceQueryParser ProvenanceSource = "parser-query"
	// SourceDerived — факт выведен из других фактов (резолвер, деривации).
	SourceDerived ProvenanceSource = "derived"
	// SourceHeuristic — факт получен эвристикой (regex, догадка по имени).
	SourceHeuristic ProvenanceSource = "heuristic"
)

// Valid сообщает, известен ли источник факта.
func (s ProvenanceSource) Valid() bool {
	switch s {
	case SourceBSLParser, SourceXML, SourceQueryParser, SourceDerived, SourceHeuristic:
		return true
	default:
		return false
	}
}

// Exact сообщает, может ли источник давать confidence = 1.
func (s ProvenanceSource) Exact() bool {
	return s == SourceBSLParser || s == SourceXML || s == SourceQueryParser || s == SourceDerived
}

// Provenance — происхождение факта: чем получен и из какого файла.
// File — канонический путь относительно корня компонента, со слешами.
type Provenance struct {
	Source ProvenanceSource `json:"source"`
	File   string           `json:"file,omitempty"`
	Detail string           `json:"detail,omitempty"`
}

// Validate проверяет происхождение вместе с доверием к факту: эвристика с
// confidence = 1 запрещена спецификацией — это regex, выдающий себя за парсер.
func (p Provenance) Validate(c Confidence) error {
	if !p.Source.Valid() {
		return fmt.Errorf("provenance: источник %q неизвестен", p.Source)
	}
	if !c.Valid() {
		return fmt.Errorf("confidence %v вне полуинтервала (0..1]", float64(c))
	}
	if !p.Source.Exact() && c >= ConfidenceExact {
		return fmt.Errorf("provenance: источник %q эвристический, confidence обязан быть меньше 1", p.Source)
	}
	return nil
}

// Generation — непрозрачный идентификатор поколения индекса вида e<эпоха>.g<номер>
// (архитектура §14). Наружу отдаётся как строка и сравнивается только на
// равенство; разбирать её вне store и index не нужно.
type Generation string

// NewGeneration собирает идентификатор поколения.
func NewGeneration(epoch, number uint64) Generation {
	return Generation("e" + strconv.FormatUint(epoch, 10) + ".g" + strconv.FormatUint(number, 10))
}

// ParseGeneration разбирает идентификатор поколения обратно на эпоху и номер.
func ParseGeneration(g Generation) (epoch, number uint64, err error) {
	s := string(g)
	rest, ok := strings.CutPrefix(s, "e")
	if !ok {
		return 0, 0, fmt.Errorf("поколение %q: ожидался вид e<эпоха>.g<номер>", s)
	}
	epochPart, numberPart, ok := strings.Cut(rest, ".g")
	if !ok {
		return 0, 0, fmt.Errorf("поколение %q: ожидался вид e<эпоха>.g<номер>", s)
	}
	epoch, err = strconv.ParseUint(epochPart, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("поколение %q: эпоха не число", s)
	}
	number, err = strconv.ParseUint(numberPart, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("поколение %q: номер не число", s)
	}
	return epoch, number, nil
}

// Valid сообщает, разбирается ли идентификатор поколения.
func (g Generation) Valid() bool {
	_, _, err := ParseGeneration(g)
	return err == nil
}
