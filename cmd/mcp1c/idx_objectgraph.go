package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

func init() {
	registerIndexTool(registerObjectGraphTool)
}

// objectGraphMaxDepth — жёсткий потолок глубины object_graph (тикет 11,
// spec.md §8/§9: «depth (потолок 2)»). Отдельный, МЕНЬШИЙ потолок, чем
// maxRadiusDepth сервиса (internal/app/objectgraph.go:33, =6, тот обслуживает
// HTTP graph-режим таска 09 с его собственным UI-контролем радиуса): здесь
// превышение ОТВЕРГАЕТСЯ понятной ошибкой, а не тихо обрезается до потолка
// сервиса — если бы тул просто передавал большой Depth дальше,
// RadiusInput.Depth молча упёрся бы в 6, а не в обещанные 2.
const objectGraphMaxDepth = 2

// objectGraphInput — вход object_graph (тикет 11, spec.md §8/§9: «object,
// direction, kinds, depth (потолок 2), minConfidence, project»). Адресация
// цели — ровно один из (objectId) или (objectType+objectName[+component]),
// тот же контракт, что у ObjectTarget/impactInput: проверка и нормализация —
// в internal/app.normalizeObjectTarget (транспорт не несёт бизнес-правил).
type objectGraphInput struct {
	ObjectID      int64    `json:"objectId,omitempty" jsonschema:"object id from a previous answer; or objectType+objectName"`
	ObjectType    string   `json:"objectType,omitempty" jsonschema:"metadata type, e.g. Catalog; with objectName"`
	ObjectName    string   `json:"objectName,omitempty" jsonschema:"object name; with objectType"`
	Component     string   `json:"component,omitempty" jsonschema:"component, when the name is in base and extension"`
	Direction     string   `json:"direction,omitempty" jsonschema:"in, out or both (default); also callers (in) or callees (out)"`
	Kinds         []string `json:"kinds,omitempty" jsonschema:"writes-register (code), writes-declared (RegisterRecords); default both"`
	Depth         int      `json:"depth,omitempty" jsonschema:"default 2, max 2; more is an error"`
	MinConfidence float64  `json:"minConfidence,omitempty" jsonschema:"minimum confidence (0..1]"`
	View          string   `json:"view,omitempty" jsonschema:"raw (base only), effective (with extensions, borrowed objects merged) or diff (edges added by extensions); default: layers as stored"`
	Project       string   `json:"project,omitempty" jsonschema:"root of another registered project, for this call only"`
	Limit         int      `json:"limit,omitempty" jsonschema:"page size, default 50, max 200"`
	Cursor        string   `json:"cursor,omitempty" jsonschema:"nextCursor of the previous page"`
}

// registerObjectGraphTool регистрирует object_graph поверх
// internal/app.ObjectGraphService.Radius, построенного локально над
// deps.projects (interfaces.md, «Из таска 10»: indexToolDeps не несёт
// готовых сервисов). radiusNodesCap=0 -> DefaultRadiusNodesCap: тот же
// process-wide флаг -graph-radius-nodes сегодня потребляет только graph-режим
// (cmd/mcp1c/graph.go), indexToolDeps его не несёт (реестр закрыт правкам, а
// добавлять новое поле — не в зоне этого тикета), так что этот путь
// намеренно остаётся на дефолте сервиса, как и HTTP-транспорт без --project
// на своих собственных дефолтах.
func registerObjectGraphTool(server *mcp.Server, deps indexToolDeps) {
	svc := app.NewObjectGraphService(deps.projects, 0)
	falseHint := false

	mcp.AddTool(server, &mcp.Tool{
		Name:        "object_graph",
		Description: "The data map around one metadata object in one call: edges writes-register (from code) and writes-declared (from RegisterRecords) out to depth 2, each with both ends' type and name. Call it when you need the object's whole data neighbourhood instead of chaining find_register_writes and find_references; it complements the symbol-level tools. project= reads another registered project for this call only; view=diff shows only the edges extensions add.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &falseHint,
			DestructiveHint: &falseHint,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in objectGraphInput) (*mcp.CallToolResult, app.Response[app.RadiusEdgeItem], error) {
		if in.Depth > objectGraphMaxDepth {
			return nil, app.Response[app.RadiusEdgeItem]{}, fmt.Errorf(
				"object_graph: depth=%d превышает потолок %d — понизьте depth явно, тихого обрезания здесь нет",
				in.Depth, objectGraphMaxDepth)
		}
		resp, err := svc.Radius(ctx, app.RadiusInput{
			Target: app.ObjectTarget{
				ObjectID:   in.ObjectID,
				ObjectType: in.ObjectType,
				ObjectName: in.ObjectName,
				Component:  in.Component,
			},
			Direction:     in.Direction,
			Depth:         in.Depth,
			Kinds:         in.Kinds,
			View:          in.View,
			MinConfidence: in.MinConfidence,
			Limit:         in.Limit,
			Cursor:        in.Cursor,
			ProjectRoot:   in.Project,
		})
		if err != nil {
			return nil, app.Response[app.RadiusEdgeItem]{}, err
		}
		return nil, resp, nil
	})
}
