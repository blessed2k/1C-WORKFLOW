package domain

import "fmt"

// Resolution — состояние разрешения ссылки (архитектура §14). Конечный автомат:
// либо цель найдена, либо кандидатов несколько, либо цели нет, либо имя
// вычисляется в рантайме и статически не разрешимо.
type Resolution string

const (
	// ResolutionResolved — цель найдена однозначно.
	ResolutionResolved Resolution = "resolved"
	// ResolutionAmbiguous — кандидатов несколько, выбрать нельзя.
	ResolutionAmbiguous Resolution = "ambiguous"
	// ResolutionUnresolved — цель не найдена.
	ResolutionUnresolved Resolution = "unresolved"
	// ResolutionDynamic — имя собирается в рантайме (Выполнить, Вычислить,
	// имя из переменной): статически цели нет и быть не может.
	ResolutionDynamic Resolution = "dynamic"
)

// Valid сообщает, известно ли состояние разрешения.
func (r Resolution) Valid() bool {
	switch r {
	case ResolutionResolved, ResolutionAmbiguous, ResolutionUnresolved, ResolutionDynamic:
		return true
	default:
		return false
	}
}

// TargetClass — класс цели ссылки. Определён ТОЛЬКО при resolved: пока цель не
// выбрана, её класс неизвестен, а dynamic — состояние разрешения, а не класс.
type TargetClass string

const (
	// TargetSymbol — цель ссылки: символ BSL.
	TargetSymbol TargetClass = "symbol"
	// TargetMetadata — цель ссылки: объект или член метаданных.
	TargetMetadata TargetClass = "metadata"
	// TargetPlatform — цель ссылки: глобальный метод или тип платформы.
	TargetPlatform TargetClass = "platform"
)

// Valid сообщает, известен ли класс цели.
func (t TargetClass) Valid() bool {
	switch t {
	case TargetSymbol, TargetMetadata, TargetPlatform:
		return true
	default:
		return false
	}
}

// ValidateResolution проверяет инвариант §14: target_class заполняется ровно
// тогда, когда resolution = resolved.
func ValidateResolution(r Resolution, t TargetClass) error {
	if !r.Valid() {
		return fmt.Errorf("resolution %q неизвестно", r)
	}
	if r == ResolutionResolved {
		if !t.Valid() {
			return fmt.Errorf("resolution=resolved требует target_class из {symbol, metadata, platform}, получено %q", t)
		}
		return nil
	}
	if t != "" {
		return fmt.Errorf("target_class %q задан при resolution=%s: класс цели определён только у resolved", t, r)
	}
	return nil
}

// Confidence — доверие к факту, полуинтервал (0..1]. Единица только у фактов от
// точного парсера или из XML; эвристика (regex) обязана быть строго меньше.
type Confidence float64

// ConfidenceExact — факт от точного парсера или из XML.
const ConfidenceExact Confidence = 1

// Valid сообщает, лежит ли доверие в (0..1].
func (c Confidence) Valid() bool { return c > 0 && c <= 1 }
