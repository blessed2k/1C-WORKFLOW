package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// wpClient connects a client to a server over the write-path fixture, which is
// the only fixture carrying event subscriptions and an exchange plan.
func wpClient(t *testing.T, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	dump, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "wp"))
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

func TestWritePathTool(t *testing.T) {
	ctx := context.Background()
	cs := wpClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "write_path",
		Arguments: map[string]any{"objectType": "Document", "name": "ЗаказКлиента"},
	})
	if err != nil {
		t.Fatalf("call write_path: %v", err)
	}
	if res.IsError {
		t.Fatalf("write_path returned a tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{
		"ПередЗаписью", "ОбработкаПроведения",
		"ПодпискаНаСобытие.УведомитьОЗаказе",
		"SubscriptionOrder", "WriteInsideHandler",
		"ПланОбмена.ОбменСБухгалтерией",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("write_path output missing %q; got: %s", want, got)
		}
	}
}

func TestWritePathOfflineOnly(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx) // none mode
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "write_path",
		Arguments: map[string]any{"objectType": "Document", "name": "ЗаказКлиента"},
	})
	if err == nil && !res.IsError {
		t.Errorf("write_path should error without a dump; got: %s", contentText(res))
	}
}
