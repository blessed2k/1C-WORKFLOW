package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func dumpClient(t *testing.T, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	dump, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "dump"))
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

func TestGetQuerySchemaTool(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_query_schema",
		Arguments: map[string]any{"type": "AccumulationRegister", "name": "ТоварыНаСкладах"},
	})
	if err != nil {
		t.Fatalf("call get_query_schema: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_query_schema returned a tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"РегистрНакопления.ТоварыНаСкладах", "Регистратор", "Остатки", "ОстаткиИОбороты"} {
		if !strings.Contains(got, want) {
			t.Errorf("get_query_schema output missing %q; got: %s", want, got)
		}
	}
}

func TestGetQuerySchemaOfflineOnly(t *testing.T) {
	ctx := context.Background()
	// none mode (no dump) must error, since the schema needs the XML source.
	cs := scaffoldClient(t, ctx)
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_query_schema",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err == nil && !res.IsError {
		t.Errorf("get_query_schema should error without a dump; got: %s", contentText(res))
	}
}
