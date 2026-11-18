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

// scriptedLiveSource answers the calls these tests make and remembers the event
// log parameters it was asked for.
type scriptedLiveSource struct {
	fakeLiveSource
	queryErr  error
	entries   []source.EventLogEntry
	truncated bool
	gotLog    source.EventLogParams
}

func (s *scriptedLiveSource) ObjectStructure(_ context.Context, typ, name string) (*source.ObjectStructure, error) {
	return &source.ObjectStructure{Type: typ, Name: name, Attributes: []source.Field{{Name: "Цена", Kind: "Attribute"}}}, nil
}

func (s *scriptedLiveSource) ExecuteQuery(context.Context, source.QueryParams) (*source.QueryResult, error) {
	return nil, s.queryErr
}

func (s *scriptedLiveSource) EventLog(_ context.Context, p source.EventLogParams) (*source.EventLogResult, error) {
	s.gotLog = p
	return &source.EventLogResult{Entries: s.entries, Count: len(s.entries), Truncated: s.truncated}, nil
}

// liveSession serves the given registration over an in-memory transport.
func liveSession(t *testing.T, register func(*mcp.Server)) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	register(srv)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// TestLiveConfigToolsTakeBase: with several bases none is selected, and the
// config tools used to answer "no configuration source" to every call. With
// base they read the named base and say which one.
func TestLiveConfigToolsTakeBase(t *testing.T) {
	lp := &stubProvider{active: "", order: []string{"trade", "wms"}, sources: map[string]source.LiveSource{
		"trade": &scriptedLiveSource{fakeLiveSource: fakeLiveSource{
			tree: &source.MetadataTree{Configuration: "Торговля", TotalObjects: 1,
				Groups: []source.MetadataGroup{{Type: "Catalog", Objects: []string{"Товары"}}}},
			// ObjectCounts as HTTPSource fills it: live has no counts, the map is empty.
			info: &source.ConfigurationInfo{Name: "Торговля", ObjectCounts: map[string]int{}},
		}},
		"wms": &scriptedLiveSource{},
	}}
	cs := liveSession(t, func(s *mcp.Server) { registerLiveConfigTools(s, lp) })

	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get_object_structure", map[string]any{"type": "Catalog", "name": "Товары", "base": "trade"}, `"Цена"`},
		{"get_metadata_tree", map[string]any{"type": "Catalog", "base": "trade"}, `"Товары"`},
		{"object_exists", map[string]any{"type": "Catalog", "name": "Товары", "base": "trade"}, `"exists":true`},
		{"get_configuration_info", map[string]any{"base": "trade"}, `"Торговля"`},
	} {
		res := callTool(t, cs, c.tool, c.args)
		if res.IsError {
			t.Errorf("%s with base failed: %s", c.tool, contentText(res))
			continue
		}
		got := contentText(res)
		if !strings.Contains(got, `"base":"trade"`) || !strings.Contains(got, c.want) {
			t.Errorf("%s: answer does not name its base or lacks %s: %s", c.tool, c.want, got)
		}
	}

	res := callTool(t, cs, "get_object_structure", map[string]any{"type": "Catalog", "name": "Товары"})
	if !res.IsError || !strings.Contains(contentText(res), "base=<name>") {
		t.Errorf("without base and without an active one the error must point at base=: %s", contentText(res))
	}
}

// TestBaseNamesInSchema: the configured names reach the model as the enum of
// every base argument and of set_base's name, so it needs no list_bases first.
// An unknown name still reaches the handler, which lists the bases.
func TestBaseNamesInSchema(t *testing.T) {
	dir := t.TempDir()
	basesFile := filepath.Join(dir, "bases.json")
	data, _ := json.Marshal([]baseConfig{
		{Name: "Склад", URL: "http://example.invalid/wms/hs/mcp-1c"},
		{Name: "Торговля", URL: "http://example.invalid/trade/hs/mcp-1c"},
	})
	if err := os.WriteFile(basesFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cs := сессияКлиента(t, options{basesFile: basesFile, toolsProfile: profileCore})

	for round := 0; round < 2; round++ { // the second listing must not see a mutated registry
		list, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		checked := 0
		for _, tool := range list.Tools {
			key := "base"
			if tool.Name == "set_base" {
				key = "name"
			}
			raw, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			prop, ok := schema.Properties[key]
			if !ok {
				continue
			}
			checked++
			if strings.Join(prop.Enum, "|") != "Склад|Торговля" {
				t.Errorf("round %d: %s.%s enum = %v, want the configured bases", round, tool.Name, key, prop.Enum)
			}
		}
		if checked < 10 {
			t.Errorf("round %d: only %d tools carry the base names, want every live tool", round, checked)
		}
	}

	res := callTool(t, cs, "execute_query", map[string]any{"text": "ВЫБРАТЬ 1", "base": "Нет такой"})
	if !res.IsError || !strings.Contains(contentText(res), "Торговля") {
		t.Errorf("an unknown base must reach the handler and get the list of bases: %s", contentText(res))
	}
}

// TestExecuteQueryFailureCarriesHint: a query 1C refused comes back with the
// hint for the known mistake; a timeout gets none.
func TestExecuteQueryFailureCarriesHint(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
		none bool
	}{
		{name: "keyword alias", err: &source.ConnectorError{Path: "/query", Status: 400, Code: "query_failed",
			Message: "Ошибка выполнения запроса: {(1, 40)}: Ожидается имя\nИЗ Справочник.Товары КАК <<?>>В"},
			want: "Подсказка: «В» это ключевое слово"},
		{name: "timeout", err: &source.ConnectorError{Path: "/query", Status: 408, Code: "query_timeout",
			Message: "Запрос выполнялся дольше 30 с и прерван"}, none: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			lp := &stubProvider{active: "trade", order: []string{"trade"},
				sources: map[string]source.LiveSource{"trade": &scriptedLiveSource{queryErr: c.err}}}
			cs := liveSession(t, func(s *mcp.Server) { registerLiveTools(s, lp) })
			res := callTool(t, cs, "execute_query", map[string]any{"text": "ВЫБРАТЬ 1 ИЗ Справочник.Товары КАК В"})
			got := contentText(res)
			if !res.IsError {
				t.Fatalf("expected an error, got %s", got)
			}
			if c.none && strings.Contains(got, "Подсказка") {
				t.Errorf("no hint expected for %s: %s", c.name, got)
			}
			if !c.none && !strings.Contains(got, c.want) {
				t.Errorf("error %q lacks %q", got, c.want)
			}
		})
	}
}

// TestEventLogSummary: summary=true reads the widest window of one call with a
// short comment and answers with groups instead of entries.
func TestEventLogSummary(t *testing.T) {
	src := &scriptedLiveSource{truncated: true, entries: []source.EventLogEntry{
		{Date: "2026-11-01T10:00:00", Level: "Ошибка", Event: "Проведение", Comment: "Не заполнен контрагент в Счет 101"},
		{Date: "2026-11-02T10:00:00", Level: "Ошибка", Event: "Проведение", Comment: "Не заполнен контрагент в Счет 102"},
		{Date: "2026-11-02T11:00:00", Level: "Ошибка", Event: "Обмен", Comment: "Узел недоступен"},
	}}
	lp := &stubProvider{active: "trade", order: []string{"trade"}, sources: map[string]source.LiveSource{"trade": src}}
	cs := liveSession(t, func(s *mcp.Server) { registerLiveTools(s, lp) })

	res := callTool(t, cs, "get_event_log", map[string]any{"summary": true, "level": "Error"})
	if res.IsError {
		t.Fatalf("get_event_log: %s", contentText(res))
	}
	if src.gotLog.Limit != summaryEventLogLimit || src.gotLog.MaxComment != summaryEventLogMaxComment || src.gotLog.Level != "Error" {
		t.Errorf("asked the connector for %+v, want limit %d, maxComment %d and the level kept",
			src.gotLog, summaryEventLogLimit, summaryEventLogMaxComment)
	}
	var out struct {
		Entries []source.EventLogEntry `json:"entries"`
		Groups  []source.EventLogGroup `json:"groups"`
		Note    string                 `json:"note"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 0 || len(out.Groups) != 2 || out.Groups[0].Count != 2 || out.Groups[0].LastDate != "2026-11-02T10:00:00" {
		t.Errorf("summary = %+v", out)
	}
	if !strings.Contains(out.Note, "Окно обрезано") {
		t.Errorf("a cut window must be named in the note: %q", out.Note)
	}
}
