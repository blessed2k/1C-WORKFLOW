package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

func init() {
	registerIndexTool(registerIndexStatusTools)
}

// indexStatusInput — вход index_status (архитектура §21: «project?»).
// Project зарезервирован под будущий выбор проекта: сегодня фасад работает с
// единственным активным проектом реестра (internal/app.Projects.Active), и
// project, если передан, только СВЕРЯЕТСЯ с активным — несовпадение отдаёт
// actionable not_found, а не тихо отвечает не про тот проект.
type indexStatusInput struct {
	Project               string `json:"project,omitempty" jsonschema:"expected active project; a mismatch gives not_found"`
	IncludeAllDiagnostics bool   `json:"includeAllDiagnostics,omitempty" jsonschema:"every diagnostic, not only the first few plus diagnosticsDigest"`
}

// reindexInput — вход reindex (тикет 10 п.6; projectRoot — дозакрытие
// критического пробела: до него ни один production-инструмент не писал в
// workspace.Registry, так что ни у одного пользователя не было способа
// сделать хоть один индексный инструмент рабочим без ручной правки
// .mcp1c/registry.json).
type reindexInput struct {
	Mode        string `json:"mode,omitempty" jsonschema:"incremental (default) or full"`
	Component   string `json:"component,omitempty" jsonschema:"one component; default all"`
	ProjectRoot string `json:"projectRoot,omitempty" jsonschema:"directory with 1c-project.json; a new root is registered, activated and fully built"`

	IncludeAllDiagnostics bool `json:"includeAllDiagnostics,omitempty" jsonschema:"every diagnostic, not only the first few plus diagnosticsDigest"`
}

// registerIndexStatusTools регистрирует index_status и reindex — первые два
// индексных инструмента, поверх internal/app.IndexStatusService, построенного
// локально над deps.projects (см. doc-комментарий indexToolDeps: deps не
// несёт готовых сервисов, только резолвер проекта).
func registerIndexStatusTools(server *mcp.Server, deps indexToolDeps) {
	svc := app.NewIndexStatusService(deps.projects)
	falseHint := false

	mcp.AddTool(server, &mcp.Tool{
		Name:        "index_status",
		Description: "State of the active project's index: phase, generation, age, ETA of a rebuild, epoch/WAL sizes, components found on disk but missing from the manifest, last reindex counts, and dosed diagnostics (the first few plus diagnosticsDigest). Call it before reindex, or when you need to know whether the index is fresh.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &falseHint,
			DestructiveHint: &falseHint,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in indexStatusInput) (*mcp.CallToolResult, app.Response[app.StatusItem], error) {
		resp, err := svc.Status(ctx, app.StatusInput{IncludeAllDiagnostics: in.IncludeAllDiagnostics})
		if err != nil {
			return nil, app.Response[app.StatusItem]{}, err
		}
		if in.Project != "" && len(resp.Items) > 0 && string(resp.Items[0].Project) != in.Project {
			return nil, app.Response[app.StatusItem]{}, app.NotFoundError("проект", in.Project,
				[]string{string(resp.Items[0].Project)}).WithGeneration(resp.Generation)
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "reindex",
		Description: "Rebuilds the active project's index: mode=incremental (default) takes files changed since the last run, mode=full rebuilds and publishes a new epoch; component limits it to one component. projectRoot (directory with 1c-project.json) registers and activates a project, the only way to bootstrap a workspace. Returns generation, duration, per-component counts, stage timings and dosed diagnostics. Call it after a bulk change this server did not see (git pull, an edit in 1С).",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			OpenWorldHint:   &falseHint,
			DestructiveHint: &falseHint,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reindexInput) (*mcp.CallToolResult, app.Response[app.ReindexResultItem], error) {
		resp, err := svc.Reindex(ctx, app.ReindexInput{
			Mode:                  in.Mode,
			Component:             in.Component,
			ProjectRoot:           in.ProjectRoot,
			IncludeAllDiagnostics: in.IncludeAllDiagnostics,
		})
		if err != nil {
			return nil, app.Response[app.ReindexResultItem]{}, err
		}
		return nil, resp, nil
	})
}
