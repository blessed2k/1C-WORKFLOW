package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type extensionPointsInput struct {
	Query string `json:"query" jsonschema:"the task in your words, e.g. печать этикеток"`
	Limit int    `json:"limit,omitempty" jsonschema:"1..100, default 15"`
}

// registerExtensionPoints wires the offline bsp_extension_points tool.
func registerExtensionPoints(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bsp_extension_points",
		Description: "Finds legal places for custom code in a БСП configuration: exported procedures of the overridable modules (*Переопределяемый), ranked against the task, with signature, summary and whether the point is already used. Offline. Call it before deciding how to extend typical code: &Вместо in an extension is the last resort, not the first move.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in extensionPointsInput) (*mcp.CallToolResult, source.ExtensionPointsReport, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.ExtensionPointsReport{}, errNoSource
		}
		rep, err := xs.ExtensionPoints(ctx, in.Query, in.Limit)
		if err != nil {
			return nil, source.ExtensionPointsReport{}, err
		}
		return nil, *rep, nil
	})
}
