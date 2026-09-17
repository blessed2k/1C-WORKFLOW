package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// indexToolDeps — то единственное, что нужно любому индексному инструменту,
// чтобы зарегистроваться: общий резолвер активного проекта
// (internal/app.Projects). Он один на сервер (interfaces.md: «активный
// проект» один), а конкретный app.XxxService каждый idx_*.go строит у себя,
// локально, поверх deps.projects — так индексТулDeps не растёт полем на
// каждый таск, и этот файл не приходится трогать снова: контракт с тасками
// 11-15 — deps.projects и ничего больше.
type indexToolDeps struct {
	projects *app.Projects
}

// newIndexToolDeps собирает deps один раз в newServer. workspaceRoot — то же,
// что у существующего --projects-root (docs/architecture-index.md §8:
// «Workspace: каталог, заданный --projects-root»); пустой workspaceRoot —
// легитимное состояние (сервер запущен без флага), Projects это переживает
// без обращений к диску (internal/app.NewProjects).
func newIndexToolDeps(workspaceRoot string, builtins *syntax.Index) (indexToolDeps, error) {
	projects, err := app.NewProjects(workspaceRoot, builtins, app.DefaultIndexConfig())
	if err != nil {
		return indexToolDeps{}, err
	}
	return indexToolDeps{projects: projects}, nil
}

// indexTool — регистрация одного индексного инструмента на сервере. Каждый
// idx_*.go файл кладёт свою функцию в registerIndexTool() из init().
type indexTool func(server *mcp.Server, deps indexToolDeps)

// indexTools — реестр индексных инструментов, наполняемый init()-ами
// idx_*.go файлов при загрузке пакета main.
var indexTools []indexTool

// registerIndexTool добавляет инструмент в реестр индексных инструментов.
// Каждый следующий таск (11, 12, 13, 15) кладёт СВОЙ файл (idx_symbol.go,
// idx_meta.go, idx_impact.go, idx_context.go) с init(), зовущим эту функцию.
//
// После таска 10 этот файл никто, включая таск 10 сам, больше не редактирует:
// так параллельные таски не дерутся за одну строку (interfaces.md, «Как
// регистрируются новые инструменты»).
func registerIndexTool(t indexTool) {
	indexTools = append(indexTools, t)
}

// registerAllIndexTools регистрирует все накопленные индексные инструменты на
// server. Вызывается не более одного раза из newServer (finishSurface в
// tools.go), в обоих режимах (offline/live), и только когда реестр проектов
// открыт: без --projects-root или с неоткрывшимся реестром индексные
// инструменты не регистрируются вовсе (C3), иначе каждый из них умел бы
// лишь отвечать no_active_project.
func registerAllIndexTools(server *mcp.Server, deps indexToolDeps) {
	for _, t := range indexTools {
		t(server, deps)
	}
}
