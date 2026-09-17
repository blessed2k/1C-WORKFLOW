// Package app — сценарии индексных инструментов поверх internal/index и
// internal/store: общая обёртка ответа, пагинация, actionable-ошибки, и по
// одному сервису на инструмент (interfaces.md, «Из таска 09»/«таск 10»).
//
// cmd/mcp1c ходит в индекс ТОЛЬКО через этот пакет (RuleCmdThroughApp,
// internal/arch) — SQL, курсоры пагинации и склейка аспектов живут здесь, не
// в транспорте.
package app

import "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// Warning — предупреждение в структурированном ответе (spec §Формат
// структурированного ответа): код, текст по-русски, подсказка.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Response — общая обёртка ВСЕХ индексных инструментов (spec §Формат
// структурированного ответа, тикет 10 п.2): поколение, из которого построен
// ответ, признак устаревшего снапшота, предупреждения, полезная нагрузка,
// общее число элементов (когда известно) и курсор следующей страницы.
//
// Items — всегда список, даже когда инструмент физически отдаёт один факт
// (index_status, reindex): это держит все инструменты на одном контракте
// конверта, как того явно требует спецификация («общая обёртка ВСЕХ
// индексных инструментов»), а не только тех, что реально пагинируются.
type Response[T any] struct {
	Generation domain.Generation `json:"generation"`
	Stale      bool              `json:"stale"`
	Warnings   []Warning         `json:"warnings,omitempty"`
	Items      []T               `json:"items"`
	TotalCount int               `json:"totalCount,omitempty"`
	NextCursor string            `json:"nextCursor,omitempty"`
}
