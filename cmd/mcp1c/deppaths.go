package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type dependencyPathsInput struct {
	FromType string `json:"fromType" jsonschema:"start object type, e.g. Catalog"`
	FromName string `json:"fromName" jsonschema:"start object name"`
	ToType   string `json:"toType" jsonschema:"target object type"`
	ToName   string `json:"toName" jsonschema:"target object name"`
	MaxDepth int    `json:"maxDepth,omitempty" jsonschema:"1..6, default 4"`
	MaxPaths int    `json:"maxPaths,omitempty" jsonschema:"1..20, default 5"`
}

// registerDependencyPaths wires the offline find_dependency_paths tool. Like
// find_metadata_usages it needs the XML source directly and errors in live mode.
func registerDependencyPaths(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_dependency_paths",
		Description: "How two metadata objects are connected: shortest chains through typed fields and document movements (Справочник.Товары -> реквизит ТЧ документа -> движения регистра). Offline. Use it when you need to know whether a change in one object reaches another; find_metadata_usages covers one step.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dependencyPathsInput) (*mcp.CallToolResult, source.DependencyReport, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.DependencyReport{}, errNoSource
		}
		rep, err := xs.DependencyPaths(ctx, in.FromType, in.FromName, in.ToType, in.ToName, in.MaxDepth, in.MaxPaths)
		if err != nil {
			return nil, source.DependencyReport{}, err
		}
		return nil, *rep, nil
	})
}
