package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

func init() {
	registerIndexTool(registerImpactTool)
}

// impactInput — вход find_impact (тикет 13, архитектура §21: «цель, kinds?,
// depth, budget»; тело тикета добавляет view и cursor). Цель — ровно один из
// (symbolUid) или (objectType+objectName): проверка и нормализация — в
// internal/app.ImpactService (транспорт не содержит бизнес-правил).
type impactInput struct {
	SymbolUID  string   `json:"symbolUid,omitempty" jsonschema:"symbol uid; or objectType+objectName"`
	ObjectType string   `json:"objectType,omitempty" jsonschema:"metadata type, e.g. Catalog; with objectName"`
	ObjectName string   `json:"objectName,omitempty" jsonschema:"object name; with objectType"`
	Component  string   `json:"component,omitempty" jsonschema:"component, when the name is in base and extension"`
	Kinds      []string `json:"kinds,omitempty" jsonschema:"call_edge, reference, handler_binding, register_access, role_right, dependency_edge; default all"`
	Depth      int      `json:"depth,omitempty" jsonschema:"max hops, default 3, max 8"`
	Budget     int      `json:"budget,omitempty" jsonschema:"max nodes visited, default 500, max 5000; truncation is warned"`
	View       string   `json:"view,omitempty" jsonschema:"raw (default); effective falls back to raw with a warning"`
	Cursor     string   `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page"`
}

// registerImpactTool регистрирует find_impact поверх internal/app.ImpactService,
// построенного локально над deps.projects (interfaces.md, «Из таска 10»:
// indexToolDeps не несёт готовых сервисов).
func registerImpactTool(server *mcp.Server, deps indexToolDeps) {
	svc := app.NewImpactService(deps.projects)
	falseHint := false

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_impact",
		Description: "What depends on a symbol or a metadata object (reverse walk): items ranked by distance and confidence, each with the chain of typed edges (call_edge, reference, handler_binding, register_access, role_right, dependency_edge) back to the target. Call it before changing a signature or an object's structure. Limits: queries that read the object are not walked (use find_queries_using); dependency_edge covers field types only (no subsystem, exchange plan or EPF membership); references to a metadata object as a whole are not found.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &falseHint,
			DestructiveHint: &falseHint,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in impactInput) (*mcp.CallToolResult, app.Response[app.ImpactItem], error) {
		resp, err := svc.Impact(ctx, app.ImpactInput{
			Target: app.ImpactTarget{
				SymbolUID:  in.SymbolUID,
				ObjectType: in.ObjectType,
				ObjectName: in.ObjectName,
				Component:  in.Component,
			},
			Kinds:  in.Kinds,
			Depth:  in.Depth,
			Budget: in.Budget,
			View:   in.View,
			Cursor: in.Cursor,
		})
		if err != nil {
			return nil, app.Response[app.ImpactItem]{}, err
		}
		return nil, resp, nil
	})
}
