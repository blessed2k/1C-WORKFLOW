package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/retrieve"
)

func init() {
	registerIndexTool(registerContextTool)
}

// contextTaskInput — вход get_context_for_task (тикет 15, архитектура §23).
type contextTaskInput struct {
	Task           string   `json:"task" jsonschema:"the task in words: bugfix, signature change, register/form/rights question, add-attribute"`
	Project        string   `json:"project,omitempty" jsonschema:"expected active project; a mismatch gives not_found"`
	ComponentHints []string `json:"componentHints,omitempty" jsonschema:"search anchors only in these components"`
	BudgetChars    int      `json:"budgetChars,omitempty" jsonschema:"hard cap in characters, default 16000; what does not fit is reported partial/missing"`
	BudgetTokens   int      `json:"budgetTokens,omitempty" jsonschema:"budget in tokens (chars = tokens * 3) when budgetChars is unset"`
	FocusHints     []string `json:"focusHints,omitempty" jsonschema:"known exact symbol/object names, tried first"`
	View           string   `json:"view,omitempty" jsonschema:"raw (default) or effective: overlays applying extensions, each fact with its own layer. bugfix, signature-change, form, posting: interceptors of the anchor or handler; register: writer_intercepts (intercepted writers and writes made by interceptors); query: query_intercepts plus the interceptors own query texts; add-attribute and rights: the object borrowed by extensions (its attributes, forms, extension roles, rights and RLS). Warns on competing &Вместо and unparsed targets"`
	MaxDepth       int      `json:"maxDepth,omitempty" jsonschema:"call-graph depth, default 2, max 6"`
	IncludeCode    string   `json:"includeCode,omitempty" jsonschema:"signatures (default), bodies or none"`
	Freshness      string   `json:"freshness,omitempty" jsonschema:"allow-stale (default): answer now with a warning; require-fresh: wait or fail with index_not_fresh"`
}

// registerContextTool регистрирует get_context_for_task поверх
// internal/retrieve.Run — единственный индексный инструмент, идущий
// напрямую через internal/retrieve, а не через свой internal/app.XxxService
// (interfaces.md, зона тикета 15: "cmd/mcp1c не импортирует
// internal/index/store/resolve/parse/* напрямую — только через
// internal/app/internal/retrieve", и именно retrieve владеет
// intent-классификатором/expansion/scoring/packing/sufficiency, а не app).
// app.Projects по-прежнему единственный резолвер активного проекта — тот же
// deps.projects, что у всех остальных idx_*.go.
func registerContextTool(server *mcp.Server, deps indexToolDeps) {
	falseHint := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_context_for_task",
		Description: "Describe a task in words and get the minimal sufficient context for it: the definition and the exact place to edit first, then only what proved relevant for the classified intent (callers/callees, references, register access, form bindings, metadata, role rights), each fact with whyIncluded, provenance and confidence, within a hard character budget. requiredCoverage[] says per mandatory category: complete_inline, complete_via_resource (+ suggestedNextAction), complete_empty (collected, honestly nothing), partial or missing; sufficiencyStatus sums it up. No anchor found gives a warning and suggestedNextTools, not a guess. Call it first on any non-trivial task, then follow up with find_symbol, get_object or find_impact.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &falseHint, DestructiveHint: &falseHint,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contextTaskInput) (*mcp.CallToolResult, app.Response[retrieve.Result], error) {
		resp, err := runGetContextForTask(ctx, deps.projects, in)
		if err != nil {
			return nil, app.Response[retrieve.Result]{}, err
		}
		// retrieve suggests tools by intent and knows nothing of the mode or
		// the profile: keep only what this server advertises.
		keepAdvertisedNextTools(&resp, surfaceOf(server))
		return nil, resp, nil
	})
}

// runGetContextForTask разрешает активный проект и зовёт retrieve.Run,
// который сам делает freshness-precheck (§24 шаг 2) и открывает ОДНУ
// read-транзакцию (§24 шаг 3) — cmd/mcp1c не открывает store сам (см.
// doc-комментарий registerContextTool и retrieve.Run).
func runGetContextForTask(ctx context.Context, projects *app.Projects, in contextTaskInput) (app.Response[retrieve.Result], error) {
	op, err := projects.Active(ctx)
	if err != nil {
		return app.Response[retrieve.Result]{}, err
	}

	task := strings.TrimSpace(in.Task)
	if task == "" {
		return app.Response[retrieve.Result]{}, app.NewError(app.CodeNotFound,
			"get_context_for_task требует непустой task", "опишите задачу словами").WithProject(op.Entry.ID)
	}
	if in.Project != "" && string(op.Entry.ID) != in.Project {
		return app.Response[retrieve.Result]{}, app.NewError(app.CodeNotFound,
			fmt.Sprintf("проект %q сейчас не активен", in.Project),
			"активный проект: "+string(op.Entry.ID)).WithProject(op.Entry.ID)
	}

	req := retrieve.Request{
		Task: task, Project: in.Project, ProjectID: op.Entry.ID, ComponentHints: in.ComponentHints,
		BudgetChars: in.BudgetChars, BudgetTokens: in.BudgetTokens, FocusHints: in.FocusHints,
		View: in.View, MaxDepth: in.MaxDepth, IncludeCode: in.IncludeCode, Freshness: in.Freshness,
	}

	result, err := retrieve.Run(ctx, op.Service, op.Store, req)
	if err != nil {
		var nf *retrieve.NotFreshError
		if errors.As(err, &nf) {
			return app.Response[retrieve.Result]{}, app.NewError(app.CodeIndexNotFresh,
				"индекс сейчас не свежий: "+nf.Progress,
				"повторите вызов позже, либо передайте freshness=allow-stale и учтите warning об устаревшем ответе").
				WithProject(op.Entry.ID)
		}
		return app.Response[retrieve.Result]{}, fmt.Errorf("проект %s: get_context_for_task: %w", op.Entry.ID, err)
	}

	return app.Response[retrieve.Result]{
		Generation: result.Generation,
		Stale:      len(result.Warnings) > 0 && hasStaleWarning(result.Warnings),
		Warnings:   convertWarnings(result.Warnings),
		Items:      []retrieve.Result{result},
		TotalCount: 1,
	}, nil
}

func hasStaleWarning(ws []retrieve.Warning) bool {
	for _, w := range ws {
		if w.Code == "stale_index" {
			return true
		}
	}
	return false
}

func convertWarnings(ws []retrieve.Warning) []app.Warning {
	if len(ws) == 0 {
		return nil
	}
	out := make([]app.Warning, len(ws))
	for i, w := range ws {
		out[i] = app.Warning{Code: w.Code, Message: w.Message, Hint: w.Hint}
	}
	return out
}

// keepAdvertisedNextTools drops suggestedNextTools the server does not
// advertise (live mode has no offline tools, core leaves some out).
func keepAdvertisedNextTools(resp *app.Response[retrieve.Result], surface *toolSurface) {
	for i := range resp.Items {
		resp.Items[i].SuggestedNextTools = surface.filter(resp.Items[i].SuggestedNextTools)
	}
}
