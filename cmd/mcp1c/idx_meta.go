package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

func init() {
	registerIndexTool(registerMetaTools)
}

// getObjectInput — вход get_object.
type getObjectInput struct {
	Type      string `json:"type" jsonschema:"metadata type, e.g. Catalog, Document"`
	Name      string `json:"name" jsonschema:"object name, e.g. Товары"`
	Component string `json:"component,omitempty" jsonschema:"component, when the object is in several"`
	Parts     string `json:"parts,omitempty" jsonschema:"comma-separated: members, forms, subscriptions, jobs, roles; default all"`
	View      string `json:"view,omitempty" jsonschema:"raw (default) or effective: merges members and forms of base and extensions"`
}

// getFormHandlersInput — вход get_form_handlers.
type getFormHandlersInput struct {
	Type      string `json:"type" jsonschema:"owner type, e.g. Catalog"`
	Name      string `json:"name" jsonschema:"owner name"`
	Form      string `json:"form,omitempty" jsonschema:"one form; default all"`
	Component string `json:"component,omitempty" jsonschema:"component, when the owner is in several"`
	View      string `json:"view,omitempty" jsonschema:"raw (default); extension handlers are not merged yet"`
}

// findQueriesUsingInput — вход find_queries_using.
type findQueriesUsingInput struct {
	Type      string `json:"type,omitempty" jsonschema:"object type, with name"`
	Name      string `json:"name,omitempty" jsonschema:"object name, with type"`
	Field     string `json:"field,omitempty" jsonschema:"field name, alone or with type+name"`
	Component string `json:"component,omitempty" jsonschema:"one component"`
	View      string `json:"view,omitempty" jsonschema:"ignored here"`
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, default 50, max 200"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page"`
}

// findRegisterWritesInput — вход find_register_writes.
type findRegisterWritesInput struct {
	Register      string  `json:"register" jsonschema:"register name, e.g. ОстаткиТоваров"`
	Modes         string  `json:"modes,omitempty" jsonschema:"comma-separated write, read, movement, clear; default write"`
	Component     string  `json:"component,omitempty" jsonschema:"one component"`
	Symbol        string  `json:"symbol,omitempty" jsonschema:"owning symbol uid"`
	MinConfidence float64 `json:"minConfidence,omitempty" jsonschema:"minimum confidence (0..1]"`
	View          string  `json:"view,omitempty" jsonschema:"ignored here"`
	Limit         int     `json:"limit,omitempty" jsonschema:"page size, default 50, max 200"`
	Cursor        string  `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page"`
}

// registerMetaTools регистрирует get_object, get_form_handlers,
// find_queries_using, find_register_writes — тикет 12. Сервисы строятся
// здесь же, локально, поверх deps.projects (см. doc-комментарий
// indexToolDeps в indexreg.go — общий файл реестра не редактируется).
func registerMetaTools(server *mcp.Server, deps indexToolDeps) {
	metaSvc := app.NewMetadataService(deps.projects)
	querySvc := app.NewQueryService(deps.projects)
	registerSvc := app.NewRegisterService(deps.projects)
	falseHint := false

	roHints := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &falseHint, DestructiveHint: &falseHint}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_object",
		Description: "A metadata object's structure from the index: members (attributes, tabular sections, dimensions, resources), uuid, forms with commands, subscriptions on it, scheduled jobs, role rights; parts narrows the answer. Use it when the index is available; without the index or in live mode use get_object_structure.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getObjectInput) (*mcp.CallToolResult, app.Response[app.ObjectItem], error) {
		resp, err := metaSvc.GetObject(ctx, app.GetObjectInput{
			Type: in.Type, Name: in.Name, Component: in.Component, Parts: in.Parts, View: in.View,
		})
		if err != nil {
			return nil, app.Response[app.ObjectItem]{}, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_form_handlers",
		Description: "A form's event handler bindings, each resolved to its BSL symbol with a span; a handler missing from the module comes back unresolved with a diagnostic, never as a silent gap. Call it before changing a form's module.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getFormHandlersInput) (*mcp.CallToolResult, app.Response[app.FormHandlerItem], error) {
		resp, err := metaSvc.GetFormHandlers(ctx, app.GetFormHandlersInput{
			OwnerType: in.Type, OwnerName: in.Name, Form: in.Form, Component: in.Component, View: in.View,
		})
		if err != nil {
			return nil, app.Response[app.FormHandlerItem]{}, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_queries_using",
		Description: "Queries that use an object (any field or table) or one field, from the index of parsed queries, not a text search. Each result: owning symbol, span, staticity (static/partial/dynamic) and confidence. Call it before renaming a field or changing a register's structure.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in findQueriesUsingInput) (*mcp.CallToolResult, app.Response[app.QueryUsageItem], error) {
		resp, err := querySvc.FindQueriesUsing(ctx, app.FindQueriesUsingInput{
			Type: in.Type, Name: in.Name, Field: in.Field, Component: in.Component, View: in.View, Limit: in.Limit, Cursor: in.Cursor,
		})
		if err != nil {
			return nil, app.Response[app.QueryUsageItem]{}, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_register_writes",
		Description: "Accesses to a register: writers by default (modes=write), readers, movements or clears via modes. Each result: mode, owning symbol, span, transaction flag, confidence. An empty answer is [] and names documents that only declare movements (see object_graph). Call it before changing a register's structure.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in findRegisterWritesInput) (*mcp.CallToolResult, app.Response[app.RegisterAccessItem], error) {
		resp, err := registerSvc.FindRegisterWrites(ctx, app.FindRegisterWritesInput{
			Register: in.Register, Modes: in.Modes, Component: in.Component, Symbol: in.Symbol,
			MinConfidence: in.MinConfidence, View: in.View, Limit: in.Limit, Cursor: in.Cursor,
		})
		if err != nil {
			return nil, app.Response[app.RegisterAccessItem]{}, err
		}
		return nil, resp, nil
	})
}
