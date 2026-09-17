package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

func init() {
	registerIndexTool(registerSymbolTools)
}

// findSymbolInput — вход find_symbol (архитектура §21).
type findSymbolInput struct {
	Name      string `json:"name" jsonschema:"exact name or substring, case-insensitive"`
	Kind      string `json:"kind,omitempty" jsonschema:"procedure, function or variable"`
	Module    string `json:"module,omitempty" jsonschema:"module path relative to the component root"`
	Component string `json:"component,omitempty" jsonschema:"one component"`
	View      string `json:"view,omitempty" jsonschema:"ignored here"`
	Limit     int    `json:"limit,omitempty" jsonschema:"page size, default 50, max 200"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page"`
}

// getSymbolInput — вход get_symbol.
type getSymbolInput struct {
	UID         string `json:"uid,omitempty" jsonschema:"symbol uid from find_symbol"`
	Module      string `json:"module,omitempty" jsonschema:"module path, with name"`
	Name        string `json:"name,omitempty" jsonschema:"symbol name, with module"`
	Component   string `json:"component,omitempty" jsonschema:"component, when module+name is ambiguous"`
	View        string `json:"view,omitempty" jsonschema:"raw (default) or effective: adds extension interceptors, warns on competing &Вместо"`
	IncludeBody bool   `json:"includeBody,omitempty" jsonschema:"add the source text from the index"`
}

// getModuleStructureInput — вход get_module_structure.
type getModuleStructureInput struct {
	Module    string `json:"module" jsonschema:"e.g. CommonModules/Foo/Ext/Module.bsl"`
	Component string `json:"component,omitempty" jsonschema:"component, when the path is ambiguous"`
	View      string `json:"view,omitempty" jsonschema:"raw (default) or effective: adds extension interceptors per symbol"`
}

// findReferencesInput — вход find_references.
type findReferencesInput struct {
	UID       string   `json:"uid" jsonschema:"symbol uid"`
	Kinds     []string `json:"kinds,omitempty" jsonschema:"reference kinds, e.g. call"`
	Component string   `json:"component,omitempty" jsonschema:"one component"`
	View      string   `json:"view,omitempty" jsonschema:"raw (default) or effective: warns about interceptors of the target only"`
	Limit     int      `json:"limit,omitempty" jsonschema:"page size, default 50, max 200"`
	Cursor    string   `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page"`
}

// traceCallGraphInput — вход trace_call_graph.
type traceCallGraphInput struct {
	UID             string `json:"uid" jsonschema:"start symbol uid"`
	Direction       string `json:"direction,omitempty" jsonschema:"callees (default) or callers; also out (callees) or in (callers)"`
	Depth           int    `json:"depth,omitempty" jsonschema:"default 2, max 8"`
	ExpandAmbiguous bool   `json:"expandAmbiguous,omitempty" jsonschema:"walk through ambiguous call sites"`
	View            string `json:"view,omitempty" jsonschema:"raw (default) or effective: warns about interceptors of the root only"`
	Limit           int    `json:"limit,omitempty" jsonschema:"max nodes, default 100, max 500"`
	Cursor          string `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page"`
}

// registerSymbolTools регистрирует find_symbol, get_symbol,
// get_module_structure, find_references, trace_call_graph и их resource-links
// (onec://symbol/..., onec://references/..., onec://src/...) — тикет 11.
// app.SymbolService/app.GraphService строятся здесь же, локально, поверх
// deps.projects (см. doc-комментарий indexToolDeps в indexreg.go).
func registerSymbolTools(server *mcp.Server, deps indexToolDeps) {
	symSvc := app.NewSymbolService(deps.projects)
	graphSvc := app.NewGraphService(deps.projects)
	falseHint := false

	roHints := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &falseHint, DestructiveHint: &falseHint}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_symbol",
		Description: "Finds BSL procedures, functions and module variables by name or substring without reading modules. Use it when you know roughly what you look for; follow up with get_symbol or get_module_structure.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in findSymbolInput) (*mcp.CallToolResult, app.Response[app.SymbolItem], error) {
		resp, err := symSvc.FindSymbol(ctx, app.FindSymbolInput{
			Name: in.Name, Kind: in.Kind, Module: in.Module, Component: in.Component, View: in.View, Limit: in.Limit, Cursor: in.Cursor,
		})
		if err != nil {
			return nil, app.Response[app.SymbolItem]{}, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_symbol",
		Description: "One BSL symbol exactly, by uid (from find_symbol) or module+name: signature, parameters with defaults, export/async/directive, span. includeBody adds the text from the index; long bodies come as a resource link. Use it after find_symbol when you need the precise signature or the body.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getSymbolInput) (*mcp.CallToolResult, app.Response[app.SymbolDetail], error) {
		resp, err := symSvc.GetSymbol(ctx, app.GetSymbolInput{
			UID: in.UID, Module: in.Module, Name: in.Name, Component: in.Component, View: in.View, IncludeBody: in.IncludeBody,
		})
		if err != nil {
			return nil, app.Response[app.SymbolDetail]{}, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_module_structure",
		Description: "Outline of a BSL module: every symbol's signature, module variables and counts, never the text. Use it before reading one symbol with get_symbol, instead of opening the file.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getModuleStructureInput) (*mcp.CallToolResult, app.Response[app.ModuleStructureItem], error) {
		resp, err := symSvc.GetModuleStructure(ctx, app.GetModuleStructureInput{Module: in.Module, Component: in.Component, View: in.View})
		if err != nil {
			return nil, app.Response[app.ModuleStructureItem]{}, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_references",
		Description: "Every reference to a symbol, grouped by module, with resolution (resolved/ambiguous/unresolved/dynamic), confidence and ranked candidates for ambiguous ones. Call it before changing a signature or renaming a symbol. Large results come as a resource link.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in findReferencesInput) (*mcp.CallToolResult, app.Response[app.ReferenceGroup], error) {
		resp, err := graphSvc.FindReferences(ctx, app.FindReferencesInput{
			UID: in.UID, Kinds: in.Kinds, Component: in.Component, View: in.View, Limit: in.Limit, Cursor: in.Cursor,
		})
		if err != nil {
			return nil, app.Response[app.ReferenceGroup]{}, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "trace_call_graph",
		Description: "Walks the call graph from a symbol to a bounded depth, cycle-safe, each step explained (edge kind, resolution, call site); platform calls are their own kind, ambiguous call sites stop the walk unless expandAmbiguous. Use it to see control flow or blast radius around a symbol.",
		Annotations: roHints,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in traceCallGraphInput) (*mcp.CallToolResult, app.Response[app.CallGraphNodeItem], error) {
		resp, err := graphSvc.TraceCallGraph(ctx, app.TraceCallGraphInput{
			UID: in.UID, Direction: in.Direction, Depth: in.Depth, ExpandAmbiguous: in.ExpandAmbiguous, View: in.View,
			Limit: in.Limit, Cursor: in.Cursor,
		})
		if err != nil {
			return nil, app.Response[app.CallGraphNodeItem]{}, err
		}
		return nil, resp, nil
	})

	registerSymbolResources(server, symSvc, graphSvc)
}

// registerSymbolResources регистрирует resource-links, отдаваемые
// find_symbol/get_symbol/find_references при усечении (архитектура §21):
// onec://symbol/{project}/{uid}{?gen} — полный текст символа;
// onec://references/{project}/{uid}{?gen} — полный список ссылок;
// onec://src/{project}/{component}/{path}{?hash,start,end} — точный фрагмент
// по content hash. Несовпадение hash/gen -> resource_expired (err.Error()
// уже несёт код в фиксированной позиции — interfaces.md, «Из таска 10»).
func registerSymbolResources(server *mcp.Server, symSvc *app.SymbolService, graphSvc *app.GraphService) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "onec://symbol/{project}/{uid}{?gen}",
		Name:        "symbol-body",
		Title:       "Полный текст символа BSL",
		Description: "Неусечённый текст символа (процедуры/функции), по которому построен его span. Отдаётся get_symbol/find_symbol при усечении инлайна.",
		MIMEType:    "text/plain",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		project, uid, gen, ok := app.ParseSymbolResourceURI(req.Params.URI)
		if !ok {
			return nil, fmt.Errorf("invalid symbol resource URI %q; expected onec://symbol/{project}/{uid}", req.Params.URI)
		}
		text, err := symSvc.ResourceSymbolBody(ctx, project, uid, gen)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: text}},
		}, nil
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "onec://references/{project}/{uid}{?gen}",
		Name:        "symbol-references",
		Title:       "Полный список ссылок на символ",
		Description: "Все ссылки на символ, сгруппированные по модулю, без усечения. Отдаётся find_references при усечении инлайна.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		project, uid, gen, ok := app.ParseReferencesResourceURI(req.Params.URI)
		if !ok {
			return nil, fmt.Errorf("invalid references resource URI %q; expected onec://references/{project}/{uid}", req.Params.URI)
		}
		resp, err := graphSvc.ResourceReferences(ctx, project, uid, gen)
		if err != nil {
			return nil, err
		}
		return jsonResource(req.Params.URI, resp)
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "onec://src/{project}/{component}/{path}{?hash,start,end}",
		Name:        "src-fragment",
		Title:       "Точный фрагмент исходника по content hash",
		Description: "Байтовый фрагмент [start,end) файла с указанным content hash. Несовпадение hash с тем, что сейчас хранит индекс (GC по TTL, смена эпохи), отдаёт resource_expired.",
		MIMEType:    "text/plain",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		project, _, _, hash, start, end, ok := app.ParseSrcResourceURI(req.Params.URI)
		if !ok {
			return nil, fmt.Errorf("invalid src resource URI %q; expected onec://src/{project}/{component}/{path}?hash=...&start=...&end=...", req.Params.URI)
		}
		text, err := symSvc.ResourceSrcFragment(ctx, project, hash, start, end)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: text}},
		}, nil
	})
}
