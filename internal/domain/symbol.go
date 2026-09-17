package domain

import "fmt"

// SymbolKind — вид символа BSL.
type SymbolKind string

const (
	// SymbolProcedure — процедура.
	SymbolProcedure SymbolKind = "procedure"
	// SymbolFunction — функция.
	SymbolFunction SymbolKind = "function"
	// SymbolVariable — переменная модуля (Перем).
	SymbolVariable SymbolKind = "variable"
)

// Valid сообщает, известен ли вид символа.
func (k SymbolKind) Valid() bool {
	switch k {
	case SymbolProcedure, SymbolFunction, SymbolVariable:
		return true
	default:
		return false
	}
}

// Symbol — символ BSL: процедура, функция или переменная модуля.
//
// NameNorm и NameDisplay ходят парой (§14): искать нужно по нормализованному
// имени, показывать — оригинальное написание. Directive хранит директиву
// компиляции ровно так, как она написана в исходнике: её потеря — главный
// дефект, ради которого строится индекс.
type Symbol struct {
	UID         SymbolUID   `json:"uid"`
	Project     ProjectID   `json:"project"`
	Component   ComponentID `json:"component"`
	ModulePath  string      `json:"modulePath"`
	Kind        SymbolKind  `json:"kind"`
	NameNorm    string      `json:"nameNorm"`
	NameDisplay string      `json:"nameDisplay"`
	Export      bool        `json:"export"`
	Async       bool        `json:"async,omitempty"`
	Directive   string      `json:"directive,omitempty"`
	Params      []Parameter `json:"params,omitempty"`
	Span        Span        `json:"span"`
	BodySpan    Span        `json:"bodySpan,omitempty"`
	Layer       Layer       `json:"layer"`
	Provenance  Provenance  `json:"provenance"`
	Confidence  Confidence  `json:"confidence"`
}

// Validate проверяет инварианты символа.
func (s Symbol) Validate() error {
	if s.UID == "" {
		return fmt.Errorf("символ %q: пустой uid", s.NameDisplay)
	}
	if s.NameNorm == "" || s.NameDisplay == "" {
		return fmt.Errorf("символ %q: name_norm и name_display заполняются оба", s.NameDisplay)
	}
	if s.NameNorm != NormalizeName(s.NameDisplay) {
		return fmt.Errorf("символ %q: name_norm %q не соответствует name_display", s.NameDisplay, s.NameNorm)
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("символ %q: вид %q неизвестен", s.NameDisplay, s.Kind)
	}
	if err := s.Span.Validate(); err != nil {
		return fmt.Errorf("символ %q: %w", s.NameDisplay, err)
	}
	if err := s.Provenance.Validate(s.Confidence); err != nil {
		return fmt.Errorf("символ %q: %w", s.NameDisplay, err)
	}
	return nil
}

// Parameter — параметр процедуры или функции.
// Default хранит текст значения по умолчанию как в исходнике; HasDefault
// отличает «= Неопределено» от параметра без значения по умолчанию.
type Parameter struct {
	Index       int    `json:"index"`
	NameNorm    string `json:"nameNorm"`
	NameDisplay string `json:"nameDisplay"`
	ByValue     bool   `json:"byValue,omitempty"`
	HasDefault  bool   `json:"hasDefault,omitempty"`
	Default     string `json:"default,omitempty"`
	Span        Span   `json:"span"`
}

// Reference — ссылка из кода на цель: вызов, обращение к метаданным, к методу
// платформы. Хранит и то, куда ссылка ведёт, и то, насколько мы в этом уверены.
type Reference struct {
	Project     ProjectID   `json:"project"`
	Component   ComponentID `json:"component"`
	FromSymbol  SymbolUID   `json:"fromSymbol,omitempty"`
	ModulePath  string      `json:"modulePath"`
	NameNorm    string      `json:"nameNorm"`
	NameDisplay string      `json:"nameDisplay"`
	Qualifier   string      `json:"qualifier,omitempty"`
	Span        Span        `json:"span"`
	Resolution  Resolution  `json:"resolution"`
	TargetClass TargetClass `json:"targetClass,omitempty"`
	TargetUID   SymbolUID   `json:"targetUid,omitempty"`
	TargetKey   string      `json:"targetKey,omitempty"`
	Confidence  Confidence  `json:"confidence"`
	Layer       Layer       `json:"layer"`
	Provenance  Provenance  `json:"provenance"`
}

// Validate проверяет инварианты ссылки, включая связь resolution и target_class.
func (r Reference) Validate() error {
	if r.NameNorm == "" {
		return fmt.Errorf("ссылка в %s: пустое name_norm", r.ModulePath)
	}
	if err := ValidateResolution(r.Resolution, r.TargetClass); err != nil {
		return fmt.Errorf("ссылка %q в %s: %w", r.NameDisplay, r.ModulePath, err)
	}
	if err := r.Span.Validate(); err != nil {
		return fmt.Errorf("ссылка %q в %s: %w", r.NameDisplay, r.ModulePath, err)
	}
	if err := r.Provenance.Validate(r.Confidence); err != nil {
		return fmt.Errorf("ссылка %q в %s: %w", r.NameDisplay, r.ModulePath, err)
	}
	return nil
}
