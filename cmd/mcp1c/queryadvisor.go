package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type queryAdvisorInput struct {
	Text string `json:"text" jsonschema:"1C query text"`
}

// registerQueryAdvisor wires the offline query_advisor tool. It needs the XML
// source for the index-based checks, so it asserts *XMLSource and errors in live
// mode (like get_query_schema).
func registerQueryAdvisor(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "query_advisor",
		Description: "Static review of a 1C query: anti-patterns with concrete rewrites (ВЫБРАТЬ *, join with a subquery, virtual table without parameters, outer join without ЕСТЬNULL, leading-wildcard LIKE) and index hints from metadata (filters and join keys no index covers, filters skipping a register's leading dimension). Use it after writing a query, or on a slow one. Offline; it neither runs the query nor gives an SQL plan.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryAdvisorInput) (*mcp.CallToolResult, source.QueryAdvice, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.QueryAdvice{}, errNoSource
		}
		adv, err := xs.AdviseQuery(ctx, in.Text)
		if err != nil {
			return nil, source.QueryAdvice{}, err
		}
		return nil, *adv, nil
	})
}
