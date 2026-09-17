package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type writePathInput struct {
	ObjectType string `json:"objectType" jsonschema:"metadata type, e.g. Document, Catalog, InformationRegister"`
	Name       string `json:"name" jsonschema:"object name without the type prefix, e.g. ЗаказКлиента"`
}

// registerWritePath wires the offline write_path tool. Like the other analysis
// tools it needs the XML source directly and errors in live mode.
func registerWritePath(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "write_path",
		Description: "What runs when an object is written or posted, in platform order: object module handlers, every subscription on this object (with handler), register movements, exchange plans the write registers into. Warns where the order is not guaranteed (several subscriptions on one event) and where a handler misfires under exchange. Offline. Call it before adding code to ПередЗаписью, ПриЗаписи or ОбработкаПроведения, and when something runs on write that the module does not explain.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in writePathInput) (*mcp.CallToolResult, source.WritePathReport, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.WritePathReport{}, errNoSource
		}
		rep, err := xs.WritePath(ctx, in.ObjectType, in.Name)
		if err != nil {
			return nil, source.WritePathReport{}, err
		}
		return nil, *rep, nil
	})
}
