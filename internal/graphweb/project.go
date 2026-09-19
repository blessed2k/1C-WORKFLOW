package graphweb

import (
	"context"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// ProjectHandle — один --project корень, как его видит HTTP-слой: стабильный
// ID для адресации (?project=, /api/projects), путь для сообщений об ошибке
// и фабрика свежей пары app.Projects/app.ObjectGraphService (см. doc.go —
// «Несколько проектов и переоткрытие эпохи»).
//
// ID вычисляется вызывающим (cmd/mcp1c) ОДИН раз при валидации на старте —
// открыть проект, чтобы узнать его ID, значит открыть store, и это ровно то
// действие, ради быстрого отказа которого граф-режим и проверяет индекс до
// того, как слушатель поднят (индекса нет: ошибка с точной
// командой reindex, ничего не индексируя).
type ProjectHandle struct {
	ID             domain.ProjectID
	Root           string
	RadiusNodesCap int
	// Open строит СВЕЖИЙ app.Projects поверх Root. Вызывается на каждый
	// HTTP-запрос — см. doc.go.
	Open func(ctx context.Context) (*app.Projects, error)
}
