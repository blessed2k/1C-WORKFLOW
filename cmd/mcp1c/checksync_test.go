package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// fakeLiveSource implements source.LiveSource with fixed ConfigurationInfo and
// MetadataTree values. check_sync is the only caller under test here, and it
// never reaches the query/event-log/subsystem methods, so those just error.
type fakeLiveSource struct {
	info *source.ConfigurationInfo
	tree *source.MetadataTree
}

func (f *fakeLiveSource) ConfigurationInfo(context.Context) (*source.ConfigurationInfo, error) {
	return f.info, nil
}
func (f *fakeLiveSource) MetadataTree(context.Context) (*source.MetadataTree, error) {
	return f.tree, nil
}
func (f *fakeLiveSource) ObjectStructure(context.Context, string, string) (*source.ObjectStructure, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) FormStructure(context.Context, string, string, string) (*source.FormStructure, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) SearchCode(context.Context, source.SearchParams) (*source.SearchResult, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) Close() error { return nil }
func (f *fakeLiveSource) ExecuteQuery(context.Context, source.QueryParams) (*source.QueryResult, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) ValidateQuery(context.Context, string) (*source.ValidateResult, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) EventLog(context.Context, source.EventLogParams) (*source.EventLogResult, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) Subsystem(context.Context, string) (*source.SubsystemInfo, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) Predefined(context.Context, string, string) (*source.PredefinedList, error) {
	return nil, errors.New("not used by check_sync")
}
func (f *fakeLiveSource) AnalyzeQuery(context.Context, string) (*source.QueryAnalysis, error) {
	return nil, errors.New("not used by check_sync")
}

// dumpFixture is internal/source/testdata/dump: Configuration ДемоКонфигурация,
// version 1.2.3, with Catalog Контрагенты/Товары, Document
// РеализацияТоваровУслуг, CommonModule ДемоМодуль, AccumulationRegister
// ТоварыНаСкладах, InformationRegister КурсыВалют, Language Русский, Role
// ЧтениеТоваров (ground truth printed via a throwaway XMLSource run, not
// guessed from the export's file layout).
func dumpFixturePath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "dump"))
	if err != nil {
		t.Fatalf("resolve dump fixture path: %v", err)
	}
	return abs
}

// matchingLiveTree mirrors the dump fixture's metadata tree exactly.
func matchingLiveTree() *source.MetadataTree {
	return &source.MetadataTree{
		Configuration: "ДемоКонфигурация",
		Groups: []source.MetadataGroup{
			{Type: "Catalog", Objects: []string{"Контрагенты", "Товары"}},
			{Type: "Document", Objects: []string{"РеализацияТоваровУслуг"}},
			{Type: "CommonModule", Objects: []string{"ДемоМодуль"}},
			{Type: "AccumulationRegister", Objects: []string{"ТоварыНаСкладах"}},
			{Type: "InformationRegister", Objects: []string{"КурсыВалют"}},
			{Type: "Language", Objects: []string{"Русский"}},
			{Type: "Role", Objects: []string{"ЧтениеТоваров"}},
		},
	}
}

func checkSyncClient(t *testing.T, ctx context.Context, live source.LiveSource) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerCheckSync(server, func() source.LiveSource { return live })
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
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

func TestCheckSyncInSync(t *testing.T) {
	ctx := context.Background()
	live := &fakeLiveSource{
		info: &source.ConfigurationInfo{Name: "ДемоКонфигурация", Version: "1.2.3"},
		tree: matchingLiveTree(),
	}
	cs := checkSyncClient(t, ctx, live)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_sync",
		Arguments: map[string]any{"dumpPath": dumpFixturePath(t)},
	})
	if err != nil {
		t.Fatalf("call check_sync: %v", err)
	}
	if res.IsError {
		t.Fatalf("check_sync returned a tool error: %s", contentText(res))
	}
	got := contentText(res)
	if !strings.Contains(got, `"inSync":true`) {
		t.Errorf("expected inSync=true for a matching tree; got: %s", got)
	}
	if strings.Contains(got, `"typeDiffs":[{`) {
		t.Errorf("expected no type diffs for a matching tree; got: %s", got)
	}
}

func TestCheckSyncVersionMismatch(t *testing.T) {
	ctx := context.Background()
	live := &fakeLiveSource{
		info: &source.ConfigurationInfo{Name: "ДемоКонфигурация", Version: "1.2.4"},
		tree: matchingLiveTree(),
	}
	cs := checkSyncClient(t, ctx, live)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_sync",
		Arguments: map[string]any{"dumpPath": dumpFixturePath(t)},
	})
	if err != nil {
		t.Fatalf("call check_sync: %v", err)
	}
	got := contentText(res)
	if !strings.Contains(got, `"inSync":false`) || !strings.Contains(got, `"versionMatch":false`) {
		t.Errorf("expected a version mismatch to break sync; got: %s", got)
	}
}

func TestCheckSyncObjectDiff(t *testing.T) {
	ctx := context.Background()
	tree := matchingLiveTree()
	for i, g := range tree.Groups {
		if g.Type == "Catalog" {
			// Live is missing Товары and has a catalog the dump doesn't.
			tree.Groups[i].Objects = []string{"Контрагенты", "НовыйСправочник"}
		}
	}
	live := &fakeLiveSource{
		info: &source.ConfigurationInfo{Name: "ДемоКонфигурация", Version: "1.2.3"},
		tree: tree,
	}
	cs := checkSyncClient(t, ctx, live)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_sync",
		Arguments: map[string]any{"dumpPath": dumpFixturePath(t)},
	})
	if err != nil {
		t.Fatalf("call check_sync: %v", err)
	}
	got := contentText(res)
	for _, want := range []string{`"inSync":false`, `"type":"Catalog"`, "НовыйСправочник", "Товары"} {
		if !strings.Contains(got, want) {
			t.Errorf("object diff missing %q; got: %s", want, got)
		}
	}
}

// TestCheckSyncTypeOnlyInDumpNotCompared is the bug this fix is about: the live
// /metadata endpoint enumerates only the main types, so on a real base whole
// auxiliary types (pictures, styles, XDTO, web services) are absent live. The
// old code reported every one of their objects as "only in dump" and screamed
// "not in sync" on an export that actually matched. Such a type must land in
// notComparedTypes, never in typeDiffs, and must not by itself break inSync.
func TestCheckSyncTypeOnlyInDumpNotCompared(t *testing.T) {
	ctx := context.Background()
	// Live is identical to the dump except it does not enumerate Role at all.
	tree := matchingLiveTree()
	kept := tree.Groups[:0]
	for _, g := range tree.Groups {
		if g.Type != "Role" {
			kept = append(kept, g)
		}
	}
	tree.Groups = kept

	live := &fakeLiveSource{
		info: &source.ConfigurationInfo{Name: "ДемоКонфигурация", Version: "1.2.3"},
		tree: tree,
	}
	cs := checkSyncClient(t, ctx, live)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_sync",
		Arguments: map[string]any{"dumpPath": dumpFixturePath(t)},
	})
	if err != nil {
		t.Fatalf("call check_sync: %v", err)
	}
	got := contentText(res)
	if !strings.Contains(got, `"inSync":true`) {
		t.Errorf("a type the live side does not list must not break sync; got: %s", got)
	}
	if strings.Contains(got, `"type":"Role"`) && strings.Contains(got, `"typeDiffs"`) &&
		strings.Contains(got[strings.Index(got, `"typeDiffs"`):], "Role") {
		t.Errorf("Role must not appear as a real diff; got: %s", got)
	}
	if !strings.Contains(got, `"notComparedTypes"`) || !strings.Contains(got, `"Role"`) {
		t.Errorf("Role must be reported as not compared; got: %s", got)
	}
	if !strings.Contains(got, `"side":"dump"`) {
		t.Errorf("not-compared Role is present only in the dump; got: %s", got)
	}
	if !strings.Contains(got, `"note"`) {
		t.Errorf("a partial comparison must carry a note; got: %s", got)
	}
}

func TestCheckSyncNameMismatch(t *testing.T) {
	ctx := context.Background()
	live := &fakeLiveSource{
		info: &source.ConfigurationInfo{Name: "ДругаяКонфигурация", Version: "1.2.3"},
		tree: matchingLiveTree(),
	}
	cs := checkSyncClient(t, ctx, live)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_sync",
		Arguments: map[string]any{"dumpPath": dumpFixturePath(t)},
	})
	if err != nil {
		t.Fatalf("call check_sync: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected a tool error for mismatched configuration names, got: %s", contentText(res))
	}
	if !strings.Contains(contentText(res), "wrong export path") {
		t.Errorf("error should hint at a wrong export path; got: %s", contentText(res))
	}
}

func TestCheckSyncNoBase(t *testing.T) {
	ctx := context.Background()
	cs := checkSyncClient(t, ctx, nil)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_sync",
		Arguments: map[string]any{"dumpPath": dumpFixturePath(t)},
	})
	if err != nil {
		t.Fatalf("call check_sync: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected a tool error when no live base is selected, got: %s", contentText(res))
	}
}

func TestCheckSyncBadDumpPath(t *testing.T) {
	ctx := context.Background()
	live := &fakeLiveSource{
		info: &source.ConfigurationInfo{Name: "ДемоКонфигурация", Version: "1.2.3"},
		tree: matchingLiveTree(),
	}
	cs := checkSyncClient(t, ctx, live)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_sync",
		Arguments: map[string]any{"dumpPath": "/no/such/directory"},
	})
	if err != nil {
		t.Fatalf("call check_sync: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected a tool error for a nonexistent dump path, got: %s", contentText(res))
	}
}

func TestCapNames(t *testing.T) {
	names := []string{"a", "b", "c"}
	got, truncated := capNames(names, 2)
	if truncated != true || len(got) != 2 {
		t.Errorf("capNames(3 names, limit 2) = %v, %v; want 2 names, truncated", got, truncated)
	}
	got, truncated = capNames(names, 5)
	if truncated != false || len(got) != 3 {
		t.Errorf("capNames(3 names, limit 5) = %v, %v; want 3 names, not truncated", got, truncated)
	}
}
