package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// TestServerInfoTool drives the server through an in-memory MCP client: it
// verifies the handshake, that server_info is advertised, and that calling it
// reports the active data source. This exercises the real transport without the
// encoding pitfalls of piping over OS stdio.
func TestServerInfoTool(t *testing.T) {
	ctx := context.Background()
	srv := newServer(options{dumpDir: "testdata/export"})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if !hasTool(lt.Tools, "server_info") {
		t.Fatalf("server_info not advertised; got %v", toolNames(lt.Tools))
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call server_info: %v", err)
	}
	if res.IsError {
		t.Fatalf("server_info returned a tool error: %s", contentText(res))
	}

	got := contentText(res)
	for _, want := range []string{
		`"name":"1C-WORKFLOW"`,
		`"mode":"offline"`,
		`"source":"testdata/export"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("server_info content missing %q; got: %s", want, got)
		}
	}
}

// TestBslSyntaxTool checks the self-contained syntax reference tool end-to-end.
func TestBslSyntaxTool(t *testing.T) {
	ctx := context.Background()
	srv := newServer(options{syntaxIndex: syntaxtest.FixtureFile(t)}) // no source needed; bsl_syntax reads the index file

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

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "bsl_syntax", Arguments: map[string]any{"query": "Сообщить", "limit": 5}})
	if err != nil {
		t.Fatalf("call bsl_syntax: %v", err)
	}
	if got := contentText(res); !strings.Contains(got, "Сообщить(") {
		t.Errorf("bsl_syntax result missing the signature; got: %s", got)
	}
}

// TestLiveToolsAdvertised checks that live-only tools appear with --base and not
// with --dump.
func TestLiveToolsAdvertised(t *testing.T) {
	ctx := context.Background()

	offline := listToolNames(t, ctx, newServer(options{dumpDir: "testdata/export"}))
	if hasName(offline, "execute_query") {
		t.Errorf("offline mode must not advertise execute_query; got %v", offline)
	}
	if hasName(offline, "check_sync") {
		t.Errorf("offline mode must not advertise check_sync; got %v", offline)
	}

	live := listToolNames(t, ctx, newServer(options{baseURL: "http://localhost/base/hs/mcp1c"}))
	for _, want := range []string{"execute_query", "validate_query", "get_event_log", "get_subsystem", "get_predefined", "analyze_query", "check_sync"} {
		if !hasName(live, want) {
			t.Errorf("live mode missing %q; got %v", want, live)
		}
	}
}

// TestSetDumpTool checks that set_dump switches the offline source at runtime.
func TestSetDumpTool(t *testing.T) {
	ctx := context.Background()
	srv := newServer(options{}) // start with no source (none mode)

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

	// Before set_dump: no source, get_configuration_info must error.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_configuration_info", Arguments: map[string]any{}})
	if err == nil && !res.IsError {
		t.Errorf("get_configuration_info should fail before set_dump")
	}

	dump, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "dump"))
	sr, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_dump", Arguments: map[string]any{"path": dump}})
	if err != nil {
		t.Fatalf("set_dump: %v", err)
	}
	if got := contentText(sr); !strings.Contains(got, `"ok":true`) || !strings.Contains(got, "ДемоКонфигурация") {
		t.Errorf("set_dump result = %s", got)
	}

	// After set_dump: get_configuration_info returns the switched configuration.
	res2, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_configuration_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("get_configuration_info after set_dump: %v", err)
	}
	if got := contentText(res2); !strings.Contains(got, "ДемоКонфигурация") {
		t.Errorf("after set_dump, config = %s", got)
	}
}

func listToolNames(t *testing.T, ctx context.Context, srv *mcp.Server) []string {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	return toolNames(lt.Tools)
}

func hasName(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

func hasTool(tools []*mcp.Tool, name string) bool {
	for _, tl := range tools {
		if tl.Name == name {
			return true
		}
	}
	return false
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, tl := range tools {
		names[i] = tl.Name
	}
	return names
}

func contentText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// scaffoldClient connects an in-memory client to a fresh server with no source.
func scaffoldClient(t *testing.T, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	srv := newServer(options{})
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
