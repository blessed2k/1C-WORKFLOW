package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type metadataUsagesInput struct {
	Type          string `json:"type" jsonschema:"metadata type, e.g. Catalog"`
	WithTemplates bool   `json:"withTemplates,omitempty" jsonschema:"also scan DCS schemas and text templates (slower)"`
	Name          string `json:"name" jsonschema:"object name, e.g. Контрагенты"`
}

// registerMetadataUsages wires the offline find_metadata_usages tool. Like
// get_query_schema it needs the XML source directly and errors in live mode.
func registerMetadataUsages(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_metadata_usages",
		Description: "Where a metadata object is used: as a reference type in other objects' fields and in role rights. Offline. Use it before changing or removing an object. withTemplates=true also scans report schemas and exchange rules, which keep queries as text and break first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in metadataUsagesInput) (*mcp.CallToolResult, source.UsageReport, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.UsageReport{}, errNoSource
		}
		rep, err := xs.MetadataUsages(ctx, in.Type, in.Name, in.WithTemplates)
		if err != nil {
			return nil, source.UsageReport{}, err
		}
		return nil, *rep, nil
	})
}
