package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExchangeAuditTool(t *testing.T) {
	ctx := context.Background()
	res, err := wpClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "exchange_audit",
		Arguments: map[string]any{"objectType": "Document", "name": "ЗаказКлиента"},
	})
	if err != nil {
		t.Fatalf("call exchange_audit: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"ОбменСБухгалтерией", "ВсеДокументыПриЗаписи"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got: %s", want, got)
		}
	}
}

func TestExchangeAuditOfflineOnly(t *testing.T) {
	ctx := context.Background()
	res, err := scaffoldClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "exchange_audit",
		Arguments: map[string]any{"objectType": "Document", "name": "ЗаказКлиента"},
	})
	if err == nil && !res.IsError {
		t.Errorf("exchange_audit should error without a dump; got: %s", contentText(res))
	}
}
