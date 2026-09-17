package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type contextPackInput struct {
	Type            string `json:"type" jsonschema:"metadata type, e.g. Catalog, Document"`
	Name            string `json:"name" jsonschema:"object name without the type prefix"`
	WithQuerySchema bool   `json:"withQuerySchema,omitempty" jsonschema:"add table name, exact fields, virtual tables"`
	WithForms       bool   `json:"withForms,omitempty" jsonschema:"add a summary of every form"`
}

// registerContextPack wires the offline context_pack tool: one call returns an
// object's structure, its modules' exported interface and where it is used.
func registerContextPack(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "context_pack",
		Description: "One object's working context in one call: structure (attributes, tabular sections, forms), the exported interface of its object/manager modules, and its usages (as a type, in role rights). withQuerySchema=true when writing a query about it, withForms=true when touching its forms. Offline. Use it instead of several object-level calls.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contextPackInput) (*mcp.CallToolResult, source.ContextPack, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.ContextPack{}, errNoSource
		}
		opts := source.ContextPackOptions{QuerySchema: in.WithQuerySchema, Forms: in.WithForms}
		pack, err := xs.ContextPack(ctx, in.Type, in.Name, opts)
		if err != nil {
			return nil, source.ContextPack{}, err
		}
		return nil, *pack, nil
	})
}
