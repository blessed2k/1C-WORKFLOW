package domain

// Severity — вес диагностики.
type Severity string

const (
	// SeverityError — факт не построен или конфигурация проекта не валидна.
	SeverityError Severity = "error"
	// SeverityWarning — факт построен, но с оговоркой.
	SeverityWarning Severity = "warning"
	// SeverityInfo — сообщение без последствий для индекса.
	SeverityInfo Severity = "info"
)

// Коды диагностик, на которые ссылаются спецификация и инструменты.
const (
	// DiagPathOutsideWorkspace — путь ведёт за пределы зарегистрированного корня.
	DiagPathOutsideWorkspace = "path_outside_workspace"
	// DiagNestedRoot — корень компонента вложен в другой зарегистрированный корень.
	DiagNestedRoot = "nested_root"
	// DiagManifestInvalid — манифест проекта не проходит валидацию.
	DiagManifestInvalid = "manifest_invalid"
	// DiagComponentKindMismatch — объявленный вид компонента не совпал с содержимым каталога.
	DiagComponentKindMismatch = "component_kind_mismatch"
)

// Diagnostic — сообщение о проблеме, привязанное к месту, где она нашлась.
// Message пишется по-русски и обязан говорить, что делать, а не только что не так.
type Diagnostic struct {
	Code      string      `json:"code"`
	Severity  Severity    `json:"severity"`
	Message   string      `json:"message"`
	Project   ProjectID   `json:"project,omitempty"`
	Component ComponentID `json:"component,omitempty"`
	File      string      `json:"file,omitempty"`
	Span      Span        `json:"span,omitempty"`
}

// Error делает диагностику пригодной как ошибка: код и текст.
func (d Diagnostic) Error() string {
	if d.File != "" {
		return d.Code + ": " + d.File + ": " + d.Message
	}
	return d.Code + ": " + d.Message
}
