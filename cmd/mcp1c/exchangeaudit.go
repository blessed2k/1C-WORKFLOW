package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type exchangeAuditInput struct {
	ObjectType string `json:"objectType" jsonschema:"metadata type, e.g. Document, Catalog, InformationRegister"`
	Name       string `json:"name" jsonschema:"object name without the type prefix, e.g. ЗаказКлиента"`
}

// registerExchangeAudit wires the offline exchange_audit tool.
func registerExchangeAudit(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "exchange_audit",
		Description: "Why an object does or does not reach the other side of an exchange: plans that include it or not, auto-registration in each, distributed infobase flag, subscriptions calling ЗарегистрироватьИзменения for it. Offline. Call it when an object is missing on the receiving side, before changing exchange rules or adding an object to an exchange.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exchangeAuditInput) (*mcp.CallToolResult, source.ExchangeAuditReport, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.ExchangeAuditReport{}, errNoSource
		}
		rep, err := xs.ExchangeAudit(ctx, in.ObjectType, in.Name)
		if err != nil {
			return nil, source.ExchangeAuditReport{}, err
		}
		return nil, *rep, nil
	})
}
