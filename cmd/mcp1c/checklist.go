package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type checklistInput struct {
	ObjectType string `json:"objectType" jsonschema:"metadata type, e.g. Document, Catalog"`
	Name       string `json:"name" jsonschema:"the new object"`
	Like       string `json:"like" jsonschema:"existing object of the same kind"`
}

// registerChecklist wires the offline new_object_checklist tool.
func registerChecklist(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "new_object_checklist",
		Description: "Everywhere an existing object of the same kind is registered, and whether the new one is there too: subsystems, roles, functional options, exchange plans, subscriptions, journals, filter criteria. Offline. Call it right after adding an object, before calling the work done: the forgotten half shows up later as \"не видно\" or \"не ушло в обмен\".",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in checklistInput) (*mcp.CallToolResult, source.ChecklistReport, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.ChecklistReport{}, errNoSource
		}
		rep, err := xs.NewObjectChecklist(ctx, in.ObjectType, in.Name, in.Like)
		if err != nil {
			return nil, source.ChecklistReport{}, err
		}
		return nil, *rep, nil
	})
}
