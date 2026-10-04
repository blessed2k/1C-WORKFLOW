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

// contextTaskInput: вход get_context_for_task (архитектура §23).
type contextTaskInput struct {
	Task           string   `json:"task" jsonschema:"the task in words: bugfix, signature change, register/form/rights question, add-attribute"`
	Project        string   `json:"project,omitempty" jsonschema:"expected active project; a mismatch gives not_found"`
	ComponentHints []string `json:"componentHints,omitempty" jsonschema:"search anchors only in these components"`
	BudgetChars    int      `json:"budgetChars,omitempty" jsonschema:"hard cap in characters for the packed context, default 16000; what does not fit is reported partial/missing (the small readyMethods block is outside it)"`
	BudgetTokens   int      `json:"budgetTokens,omitempty" jsonschema:"budget in tokens (chars = tokens * 3) when budgetChars is unset"`
	FocusHints     []string `json:"focusHints,omitempty" jsonschema:"known exact symbol/object names, tried first"`
	View           string   `json:"view,omitempty" jsonschema:"raw (default) or effective: overlays applying extensions, each fact with its own layer. bugfix, exchange, extension, signature-change, form, posting: interceptors of the anchor or handler; register: writer_intercepts (intercepted writers and writes made by interceptors); query: query_intercepts of the anchor symbol plus those interceptors own query texts (other extension queries are not collected); add-attribute and rights: the object borrowed by extensions (its attributes, forms, extension roles, rights and RLS). Warns on competing &Вместо and unparsed targets"`
	MaxDepth       int      `json:"maxDepth,omitempty" jsonschema:"call-graph depth, default 2, max 6"`
	IncludeCode    string   `json:"includeCode,omitempty" jsonschema:"signatures (default), bodies or none"`
	Freshness      string   `json:"freshness,omitempty" jsonschema:"allow-stale (default): answer now with a warning; require-fresh: wait or fail with index_not_fresh"`
}

// registerContextTool регистрирует get_context_for_task поверх
// internal/retrieve.Run — единственный индексный инструмент, идущий
// напрямую через internal/retrieve, а не через свой internal/app.XxxService
// (граница слоёв: cmd/mcp1c не импортирует
// internal/index/store/resolve/parse/* напрямую, только через
// internal/app/internal/retrieve, и именно retrieve владеет
// intent-классификатором/expansion/scoring/packing/sufficiency, а не app).
// app.Projects по-прежнему единственный резолвер активного проекта — тот же
// deps.projects, что у всех остальных idx_*.go.
//
// Вторым чтением, уже после retrieve.Run, к ответу дописывается блок готовых
// методов (attachReadyMethods): его отдаёт app.APIService своей
// read-транзакцией, и поколение индекса у него может быть другим, чем у
// основного ответа.
func registerContextTool(server *mcp.Server, deps indexToolDeps) {
	falseHint := false
	api := app.NewAPIService(deps.projects)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_context_for_task",
		Description: "Describe a task in words and get the minimal sufficient context for it: the definition and the exact place to edit first, then only what proved relevant for the classified intent (callers/callees, references, register access, form bindings, metadata, role rights), each fact with whyIncluded, provenance and confidence, within a hard character budget. requiredCoverage[] says per mandatory category: complete_inline, complete_via_resource (+ suggestedNextAction), complete_empty (collected, honestly nothing), partial or missing; sufficiencyStatus sums it up. coreMethods names the БСП methods the applied code of this configuration calls most (names only, grouped by module): before writing a helper check whether one of them already does it. readyMethods lists up to 8 ready-made methods of БСП and the configuration that share words with the task (a lexical hint, often unrelated on a broad task); both blocks are outside the budget (coreMethods is about 3 thousand characters and the same on every call) and absent for rights and exchange questions: glance at them before writing a helper of your own and ask find_api with what the helper must do. No anchor found gives a warning and suggestedNextTools, not a guess. Call it first on any non-trivial task, then follow up with find_symbol, get_object or find_impact.",
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
		attachReadyMethods(ctx, api, &resp, in.Task)
		return nil, resp, nil
	})
}

// runGetContextForTask разрешает активный проект и зовёт retrieve.Run,
// который сам делает freshness-precheck (§24 шаг 2) и открывает ОДНУ
// read-транзакцию (§24 шаг 3) — cmd/mcp1c не открывает store сам (см.
// doc-комментарий registerContextTool и retrieve.Run). Блок готовых методов
// дописывается после, отдельным чтением (attachReadyMethods).
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

// readyMethodsSearcher: готовые методы для ответа get_context_for_task: по
// тексту задачи и ходовые методы библиотеки. Интерфейс объявлен у
// потребителя: тест подставляет заглушку вместо индексного проекта.
type readyMethodsSearcher interface {
	ReadyForTask(ctx context.Context, task string) (app.Response[app.APITaskMethod], error)
	CoreMethods(ctx context.Context) (app.Response[app.APICoreItem], error)
}

// readyMethodsSkipIntents: при каком назначении задачи блок готовых методов не
// нужен: вопрос о правах и вопрос об обмене кода не пишут.
var readyMethodsSkipIntents = map[string]bool{
	retrieve.IntentRights:   true,
	retrieve.IntentExchange: true,
}

// attachReadyMethods дописывает к ответу готовые методы программного
// интерфейса, близкие задаче по словам. Вопрос «нет ли готового метода»
// агент до письма кода чаще не задаёт; здесь на него отвечают заранее, тем же
// вызовом, которым агент начинает задачу.
//
// Блок лежит вне бюджета пакинга, его потолок свой (app.APITaskLibraryLimit
// и app.APITaskOtherLimit строк). Эталоном блок не мерялся: в наборе
// evals/findapi нет задач уровня постановки.
//
// упрощение: сбой поиска готовых методов ответ не роняет и даёт только
// предупреждение ready_methods_unavailable: блок необязателен, а основной
// ответ уже собран. Устаревание индекса блок не помечает: его уже пометил
// retrieve.Run.
func attachReadyMethods(ctx context.Context, api readyMethodsSearcher, resp *app.Response[retrieve.Result], task string) {
	if len(resp.Items) == 0 || readyMethodsSkipIntents[resp.Items[0].Intent.Primary] {
		return
	}
	// Ходовые методы библиотеки: от задачи не зависят, одни и те же на базу.
	// Агент видит их, ничего не решая: готовый метод он чаще теряет не на
	// поиске, а до него, не подумав искать.
	// Блок один и тот же на каждом вызове в сессии: сервер о сессии не знает.
	if core, cerr := api.CoreMethods(ctx); cerr != nil {
		resp.Warnings = append(resp.Warnings, app.Warning{
			Code:    "core_methods_unavailable",
			Message: "ходовые методы библиотеки (coreMethods) не посчитаны: " + cerr.Error(),
			Hint:    "готовые методы ищет find_api по описанию того, что должна сделать функция",
		})
	} else if len(core.Items) > 0 {
		for _, m := range core.Items[0].Modules {
			resp.Items[0].CoreMethods = append(resp.Items[0].CoreMethods,
				retrieve.CoreModule{Module: m.Module, Methods: append([]string(nil), m.Methods...)})
		}
	}
	found, err := api.ReadyForTask(ctx, task)
	if err != nil {
		resp.Warnings = append(resp.Warnings, app.Warning{
			Code:    "ready_methods_unavailable",
			Message: "блок готовых методов (readyMethods) не собран: " + err.Error(),
			Hint:    "готовые методы ищет find_api по описанию того, что должна сделать функция",
		})
		return
	}
	ready := make([]retrieve.ReadyMethod, 0, len(found.Items))
	for _, it := range found.Items {
		ready = append(ready, retrieve.ReadyMethod{Section: it.Section, Call: it.Call, Summary: it.Summary, UID: it.UID})
	}
	if len(ready) > 0 {
		resp.Items[0].ReadyMethods = ready
	}
}
