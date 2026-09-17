package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func visClient(t *testing.T, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	dump, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "vis"))
	srv := newServer(options{dumpDir: dump})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestVisibilityAuditTool(t *testing.T) {
	ctx := context.Background()
	res, err := visClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "visibility_audit",
		Arguments: map[string]any{"objectType": "Document", "name": "ЗаказКлиента"},
	})
	if err != nil {
		t.Fatalf("call visibility_audit: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"Продажи.ОптовыеПродажи", "ИспользоватьЗаказы", "Константа.ИспользоватьЗаказы", "реквизит Склад"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got: %s", want, got)
		}
	}
}

func TestVisibilityAuditOfflineOnly(t *testing.T) {
	ctx := context.Background()
	res, err := scaffoldClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "visibility_audit",
		Arguments: map[string]any{"objectType": "Document", "name": "ЗаказКлиента"},
	})
	if err == nil && !res.IsError {
		t.Errorf("visibility_audit should error without a dump; got: %s", contentText(res))
	}
}
