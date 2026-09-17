package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// namedLiveSource answers queries with the base name it belongs to, so a test
// can tell which base an answer actually came from.
type namedLiveSource struct {
	fakeLiveSource
	base string
}

func (n *namedLiveSource) ExecuteQuery(context.Context, source.QueryParams) (*source.QueryResult, error) {
	return &source.QueryResult{
		Columns: []string{"База"},
		Rows:    []map[string]any{{"База": n.base}},
		Count:   1,
	}, nil
}

// stubProvider is a liveProvider over a fixed set of named bases.
type stubProvider struct {
	sources map[string]source.LiveSource
	active  string
	order   []string
}

func (s *stubProvider) live() source.LiveSource {
	if s.active == "" {
		return nil
	}
	return s.sources[s.active]
}

func (s *stubProvider) liveFor(name string) (source.LiveSource, error) {
	src, ok := s.sources[name]
	if !ok {
		return nil, errNoBase
	}
	return src, nil
}

func (s *stubProvider) currentName() string { return s.active }
func (s *stubProvider) names() []string     { return s.order }

func newStubProvider(active string, names ...string) *stubProvider {
	p := &stubProvider{sources: map[string]source.LiveSource{}, active: active, order: names}
	for _, n := range names {
		p.sources[n] = &namedLiveSource{base: n}
	}
	return p
}

// callLiveTool connects an in-memory client to a server exposing only the live
// tools over lp, and calls one tool.
func callLiveTool(t *testing.T, lp liveProvider, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerLiveTools(srv, lp)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	return res
}

// TestLiveAnswerNamesItsBase is the guard against the costliest failure this
// server had: an answer from an unrelated base that reads as a valid finding.
// Every live answer must say which base produced it.
func TestLiveAnswerNamesItsBase(t *testing.T) {
	lp := newStubProvider("service", "service", "trade", "wms")
	res := callLiveTool(t, lp, "execute_query", map[string]any{"text": "ВЫБРАТЬ 1"})
	if res.IsError {
		t.Fatalf("execute_query failed: %s", contentText(res))
	}
	got := contentText(res)
	if !strings.Contains(got, `"base":"service"`) {
		t.Errorf("answer does not name its base; got: %s", got)
	}
}

// TestExplicitBaseWinsOverActive covers the escape hatch from server state: a
// call that names its target cannot be redirected by a reconnect that reset the
// selection.
func TestExplicitBaseWinsOverActive(t *testing.T) {
	lp := newStubProvider("wms", "service", "trade", "wms")
	res := callLiveTool(t, lp, "execute_query", map[string]any{"text": "ВЫБРАТЬ 1", "base": "trade"})
	if res.IsError {
		t.Fatalf("execute_query failed: %s", contentText(res))
	}
	got := contentText(res)
	if !strings.Contains(got, `"base":"trade"`) || !strings.Contains(got, `"База":"trade"`) {
		t.Errorf("explicit base was not honoured; got: %s", got)
	}
}

// TestNoBaseSelectedListsChoices: with nothing selected the error has to name
// the options, since that error is the first thing a fresh session hits.
func TestNoBaseSelectedListsChoices(t *testing.T) {
	lp := newStubProvider("", "service", "trade", "wms")
	res := callLiveTool(t, lp, "execute_query", map[string]any{"text": "ВЫБРАТЬ 1"})
	if !res.IsError {
		t.Fatalf("expected an error when no base is selected; got: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"service", "trade", "wms", "set_base"} {
		if !strings.Contains(got, want) {
			t.Errorf("error message missing %q; got: %s", want, got)
		}
	}
}

// TestMultiBaseStartsUnselected pins the startup rule: with several bases
// configured the server must not silently adopt the first one. That default is
// what made post-reconnect answers come from an unrelated base.
func TestMultiBaseStartsUnselected(t *testing.T) {
	dir := t.TempDir()
	basesFile := filepath.Join(dir, "bases.json")
	payload := []baseConfig{
		{Name: "wms", URL: "http://example.invalid/wms/hs/mcp-1c", User: "u"},
		{Name: "service", URL: "http://example.invalid/service/hs/mcp-1c", User: "u"},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal bases: %v", err)
	}
	if err := os.WriteFile(basesFile, data, 0o600); err != nil {
		t.Fatalf("write bases file: %v", err)
	}

	ctx := context.Background()
	srv := newServer(options{basesFile: basesFile})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "execute_query",
		Arguments: map[string]any{"text": "ВЫБРАТЬ 1"},
	})
	if err != nil {
		t.Fatalf("call execute_query: %v", err)
	}
	if !res.IsError {
		t.Fatalf("query ran without an explicit base choice; got: %s", contentText(res))
	}
	if got := contentText(res); !strings.Contains(got, "no live base selected") {
		t.Errorf("unexpected error text: %s", got)
	}
}

// TestSingleBaseIsSelectedAutomatically: one configured base is unambiguous, so
// the convenience of auto-selecting it stays.
func TestSingleBaseIsSelectedAutomatically(t *testing.T) {
	ls := newLiveState()
	ls.add(baseConfig{Name: "only", URL: "http://example.invalid/only/hs/mcp-1c"})
	if ls.count() != 1 {
		t.Fatalf("expected one base, got %d", ls.count())
	}
	if _, err := ls.setBase(ls.firstName()); err != nil {
		t.Fatalf("set base: %v", err)
	}
	if ls.currentName() != "only" {
		t.Errorf("current base = %q, want only", ls.currentName())
	}
}

// TestLiveToolsAdvertiseBaseArgument makes sure the per-call escape hatch is
// visible in the schema: an argument the model cannot see does not exist.
func TestLiveToolsAdvertiseBaseArgument(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerLiveTools(srv, newStubProvider("trade", "trade"))

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, name := range []string{"execute_query", "validate_query", "get_event_log", "get_subsystem", "get_predefined", "analyze_query"} {
		var found *mcp.Tool
		for _, tool := range lt.Tools {
			if tool.Name == name {
				found = tool
				break
			}
		}
		if found == nil {
			t.Errorf("%s not advertised", name)
			continue
		}
		schema, err := json.Marshal(found.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema of %s: %v", name, err)
		}
		if !strings.Contains(string(schema), `"base"`) {
			t.Errorf("%s does not advertise the base argument; schema: %s", name, schema)
		}
	}
}

// TestQueryInputKeepsQueryFields guards the embedded-struct trick: adding base
// must not drop the original query parameters from the schema.
func TestQueryInputKeepsQueryFields(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerLiveTools(srv, newStubProvider("trade", "trade"))

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range lt.Tools {
		if tool.Name != "execute_query" {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		if !strings.Contains(string(schema), `"text"`) {
			t.Fatalf("execute_query lost its text parameter; schema: %s", schema)
		}
		return
	}
	t.Fatal("execute_query not advertised")
}
