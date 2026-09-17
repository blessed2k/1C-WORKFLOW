package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type querySchemaInput struct {
	Type string `json:"type" jsonschema:"metadata type, e.g. Catalog, AccumulationRegister"`
	Name string `json:"name" jsonschema:"object name without the type prefix, e.g. Контрагенты"`
}

// registerQuerySchema wires the offline get_query_schema tool. It needs the XML
// source directly (query-schema derivation is offline only), so it asserts the
// active source to *XMLSource and errors otherwise.
func registerQuerySchema(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_query_schema",
		Description: "Query-language schema of an object: table name, standard fields (Ссылка, Код, Период, ...), own attributes/dimensions/resources, tabular sections, register virtual tables with their parameters. Offline. Consult it before writing a 1C query, for exact field and virtual-table names.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in querySchemaInput) (*mcp.CallToolResult, source.QuerySchema, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.QuerySchema{}, errNoSource
		}
		sc, err := xs.QuerySchema(ctx, in.Type, in.Name)
		if err != nil {
			return nil, source.QuerySchema{}, err
		}
		return nil, *sc, nil
	})
}
