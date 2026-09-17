package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// packClient connects to a server backed by the isolated context-pack dump.
func packClient(t *testing.T, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	dump, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "pack"))
	srv := newServer(options{dumpDir: dump})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestContextPackTool(t *testing.T) {
	ctx := context.Background()
	cs := packClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "context_pack",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err != nil {
		t.Fatalf("call context_pack: %v", err)
	}
	if res.IsError {
		t.Fatalf("context_pack returned a tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"Справочник.Товары", "НайтиПоАртикулу", "Артикул"} {
		if !strings.Contains(got, want) {
			t.Errorf("context_pack output missing %q; got: %s", want, got)
		}
	}
}

func TestContextPackToolOptions(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx) // the dump fixture has a form and a register

	plain, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "context_pack",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err != nil {
		t.Fatalf("call context_pack: %v", err)
	}
	// structure.forms (the plain list of names) is there either way, so the check
	// looks for markers that only the summaries produce.
	if got := contentText(plain); strings.Contains(got, `"querySchema"`) || strings.Contains(got, `"handlersTotal"`) {
		t.Errorf("extra parts must be off by default; got: %s", got)
	}

	full, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "context_pack",
		Arguments: map[string]any{
			"type": "Catalog", "name": "Товары",
			"withQuerySchema": true, "withForms": true,
		},
	})
	if err != nil {
		t.Fatalf("call context_pack with options: %v", err)
	}
	if full.IsError {
		t.Fatalf("context_pack with options returned a tool error: %s", contentText(full))
	}
	got := contentText(full)
	for _, want := range []string{`"querySchema"`, "standardFields", `"forms"`, "ФормаЭлемента", "ПриСозданииНаСервере"} {
		if !strings.Contains(got, want) {
			t.Errorf("context_pack with options missing %q; got: %s", want, got)
		}
	}
}

func TestContextPackOfflineOnly(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx) // none mode
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "context_pack",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err == nil && !res.IsError {
		t.Errorf("context_pack should error without a dump; got: %s", contentText(res))
	}
}
