package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// ErrorCode: машинный код actionable-ошибки в структурированном ответе. Семь
// кодов ниже обязательны к существованию; значения сверх
// них заводятся там, где живёт их смысл (транспортные — в
// internal/graphweb/errors.go).
type ErrorCode string

const (
	CodeNoActiveProject        ErrorCode = "no_active_project"
	CodeNotFound               ErrorCode = "not_found"
	CodeCursorExpired          ErrorCode = "cursor_expired"
	CodeResourceExpired        ErrorCode = "resource_expired"
	CodeIndexNotFresh          ErrorCode = "index_not_fresh"
	CodePathOutsideWorkspace   ErrorCode = domain.DiagPathOutsideWorkspace
	CodeComponentNotRegistered ErrorCode = "component_not_registered"
	// CodeInvalidArgument — значение параметра не разбирается: не опечатка в
	// ИМЕНИ чего-то существующего (это CodeNotFound с подсказкой похожих
	// имён), а значение вне закрытого перечня, который инструмент объявляет
	// сам. HTTP-карта переводит его в 400, а не в 404: искать нечего.
	CodeInvalidArgument ErrorCode = "invalid_argument"
)

// Error — actionable-ошибка индексных инструментов: машинный код, текст
// по-русски, подсказка что делать, плюс активный проект и generation, если
// они были известны в момент отказа.
//
// Реализует error. Наружу (в MCP-транспорт) уходит через err.Error() —
// go-sdk's ToolHandlerFor маршалит structuredContent из типизированного Out
// ТОЛЬКО на успешном пути (см. AddTool/toolForErr): при ненулевом error он
// строит CallToolResult из err.Error() и не трогает structuredContent вовсе,
// поэтому structuredContent для ошибки в этой версии SDK недостижим без
// ухода от типизированного ToolHandlerFor, которым построены все 43
// существующих инструмента. Error() поэтому обязан быть самодостаточным
// текстом: код, сообщение и подсказка в одну строку, как читает клиент.
type Error struct {
	Code       ErrorCode         `json:"code"`
	Message    string            `json:"message"`
	Hint       string            `json:"hint,omitempty"`
	Project    domain.ProjectID  `json:"project,omitempty"`
	Generation domain.Generation `json:"generation,omitempty"`
}

// Error возвращает "[код] сообщение (подсказка: ...) [проект: ..., generation: ...]".
func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", e.Code, e.Message)
	if e.Hint != "" {
		fmt.Fprintf(&b, " (подсказка: %s)", e.Hint)
	}
	if e.Project != "" || e.Generation != "" {
		b.WriteString(" [")
		if e.Project != "" {
			fmt.Fprintf(&b, "проект: %s", e.Project)
		}
		if e.Generation != "" {
			if e.Project != "" {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "generation: %s", e.Generation)
		}
		b.WriteString("]")
	}
	return b.String()
}

// NewError строит actionable-ошибку без привязки к проекту/поколению.
func NewError(code ErrorCode, message, hint string) *Error {
	return &Error{Code: code, Message: message, Hint: hint}
}

// WithProject возвращает копию ошибки с указанным активным проектом.
func (e *Error) WithProject(id domain.ProjectID) *Error {
	c := *e
	c.Project = id
	return &c
}

// WithGeneration возвращает копию ошибки с указанным generation.
func (e *Error) WithGeneration(g domain.Generation) *Error {
	c := *e
	c.Generation = g
	return &c
}

// NotFoundError — «не найдено», называющее ближайшие имена (паттерн
// explainMissing/similarName из cmd/mcp1c/tools.go и
// internal/source/xmlsource.go — переиспользован, не изобретён заново).
func NotFoundError(what, asked string, candidates []string) *Error {
	near := nearestNames(candidates, asked, 5)
	hint := "проверьте точное написание имени"
	if len(near) > 0 {
		hint = "похожие имена: " + strings.Join(near, ", ")
	}
	return NewError(CodeNotFound, fmt.Sprintf("%s %q не найден", what, asked), hint)
}

// nearestNames отбирает из candidates те, что похожи на asked: общая
// подстрока в любую сторону, либо совпадающий префикс от 4 символов и не
// меньше 70% длины короче­го имени — то же правило, что similarName в
// cmd/mcp1c/tools.go (сохранён явно, а не через общий импорт: cmd не
// экспортирует свои приватные хелперы, а дублировать правило пришлось бы
// одному из двух пакетов в любом случае).
func nearestNames(candidates []string, asked string, limit int) []string {
	var out []string
	a := strings.ToLower(strings.TrimSpace(asked))
	for _, c := range candidates {
		b := strings.ToLower(strings.TrimSpace(c))
		if similar(a, b) {
			out = append(out, c)
		}
		if len(out) >= limit {
			break
		}
	}
	sort.Strings(out)
	return out
}

func similar(a, b string) bool {
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	ar, br := []rune(a), []rune(b)
	shared := 0
	for shared < len(ar) && shared < len(br) && ar[shared] == br[shared] {
		shared++
	}
	shortest := len(ar)
	if len(br) < shortest {
		shortest = len(br)
	}
	if shortest == 0 {
		return false
	}
	return shared >= 4 && shared*10 >= shortest*7
}

// ComponentNotRegisteredError — component=<id> передан инструменту, но не
// описан в манифесте активного проекта.
func ComponentNotRegisteredError(id domain.ComponentID, known []domain.ComponentID) *Error {
	names := make([]string, len(known))
	for i, k := range known {
		names[i] = string(k)
	}
	sort.Strings(names)
	hint := "известные компоненты проекта: " + strings.Join(names, ", ")
	if len(names) == 0 {
		hint = "в манифесте проекта нет ни одного компонента"
	}
	return NewError(CodeComponentNotRegistered,
		fmt.Sprintf("компонент %q не зарегистрирован в манифесте проекта", id), hint)
}

// ResourceExpiredError — resource-ссылка (onec://src/..., onec://symbol/...,
// onec://references/...) больше не воспроизводима: blob вычищен по TTL, или
// generation ресурса разошлось с запрошенным (архитектура §21). Строится
// здесь, используется инструментами, отдающими resource links.
func ResourceExpiredError(uri, reason string) *Error {
	return NewError(CodeResourceExpired,
		fmt.Sprintf("ресурс %s больше не воспроизводим: %s", uri, reason),
		"повторите инструмент, который выдал эту ссылку, — он вернёт свежую")
}

// FromPathError переводит ошибку workspace.SafeJoin (workspace.ErrPathOutsideWorkspace
// / *workspace.PathError) в actionable-ошибку. Возвращает nil, если err не об
// этом — тогда вызывающий обязан обработать err как обычную ошибку.
func FromPathError(err error) *Error {
	if err == nil || !errors.Is(err, workspace.ErrPathOutsideWorkspace) {
		return nil
	}
	return NewError(CodePathOutsideWorkspace,
		"путь ведёт за пределы workspace: "+err.Error(),
		"используйте путь внутри корня зарегистрированного компонента, без .. и абсолютных путей")
}

// FromIndexNotFresh переводит index.ErrIndexNotFresh (require-fresh не
// дождался публикации в пределах deadline) в actionable-ошибку.
// Возвращает nil, если err не об этом.
func FromIndexNotFresh(err error) *Error {
	var e *index.ErrIndexNotFresh
	if !errors.As(err, &e) {
		return nil
	}
	return NewError(CodeIndexNotFresh,
		"индекс сейчас не свежий: "+e.Progress,
		"повторите вызов позже, либо передайте freshness=allow-stale и учтите warning об устаревшем ответе")
}
