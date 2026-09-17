package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// epClient connects a client to a server over the extension-points fixture.
func epClient(t *testing.T, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	dump, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "ep"))
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

func TestExtensionPointsTool(t *testing.T) {
	ctx := context.Background()
	res, err := epClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "bsp_extension_points",
		Arguments: map[string]any{"query": "печать документа"},
	})
	if err != nil {
		t.Fatalf("call bsp_extension_points: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"ПечатьПереопределяемый", "ПриФормированииСпискаКомандПечати", "Вместо"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got: %s", want, got)
		}
	}
}

func TestExtensionPointsOfflineOnly(t *testing.T) {
	ctx := context.Background()
	res, err := scaffoldClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "bsp_extension_points",
		Arguments: map[string]any{"query": "печать"},
	})
	if err == nil && !res.IsError {
		t.Errorf("bsp_extension_points should error without a dump; got: %s", contentText(res))
	}
}
