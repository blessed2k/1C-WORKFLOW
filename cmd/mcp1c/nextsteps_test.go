package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// TestBslSyntaxBatch covers the round that decides whether the tool is used at
// all: while writing a block, one call for every name it needs is a round the
// model makes, five separate calls is a round it skips in favour of recall.
// The single-query form stays intact for existing callers.
func TestBslSyntaxBatch(t *testing.T) {
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := newServer(options{dumpDir: "../../internal/source/testdata/dump", syntaxIndex: syntaxtest.FixtureFile(t)}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	call := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "bsl_syntax", Arguments: args})
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		return res
	}

	batch := call(map[string]any{"queries": []string{"ЗначениеЗаполнено", "НачатьТранзакцию"}, "limit": 3})
	if batch.IsError {
		t.Fatalf("batch lookup failed: %+v", batch.Content)
	}
	sc, ok := batch.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured output: %+v", batch.StructuredContent)
	}
	results, ok := sc["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %+v, want one entry per requested name", sc["results"])
	}

	single := call(map[string]any{"query": "ЗначениеЗаполнено", "limit": 3})
	if single.IsError {
		t.Fatalf("single lookup must keep working: %+v", single.Content)
	}

	if empty := call(map[string]any{}); !empty.IsError {
		t.Error("a call without query and queries must say so, not return an empty answer")
	}
}

// TestNextStepsAreObjectSpecific keeps the hints worth reading: a generic
// reminder is noise the model learns to skip, so every line must be true for
// THIS object. get_movements only makes sense for a document, form_impact only
// when the object actually has forms.
func TestNextStepsAreObjectSpecific(t *testing.T) {
	doc := nextStepsFor(source.ObjectStructure{Type: "Document", Name: "РеализацияТоваровУслуг", Forms: []string{"ФормаДокумента"}}, allTools)
	joined := strings.Join(doc, "\n")
	for _, want := range []string{"get_query_schema", "get_movements", "form_impact", "find_metadata_usages"} {
		if !strings.Contains(joined, want) {
			t.Errorf("document hints must route %q: %v", want, doc)
		}
	}
	if !strings.Contains(joined, "РеализацияТоваровУслуг") {
		t.Errorf("hints must carry the object so the next call can be made as is: %v", doc)
	}

	cat := strings.Join(nextStepsFor(source.ObjectStructure{Type: "Catalog", Name: "Товары"}, allTools), "\n")
	if strings.Contains(cat, "get_movements") {
		t.Errorf("a catalog does not post: %s", cat)
	}
	if strings.Contains(cat, "form_impact") {
		t.Errorf("no forms on this object, nothing to warn about: %s", cat)
	}
	if !strings.Contains(cat, "get_query_schema") {
		t.Errorf("a query can be written over any object: %s", cat)
	}
}

func allTools(string) bool { return true }

// TestNextStepsOnlyRegisteredTools: a hint never names a tool the server does
// not advertise. A live server has none of the four offline tools.
func TestNextStepsOnlyRegisteredTools(t *testing.T) {
	obj := source.ObjectStructure{Type: "Document", Name: "Х", Forms: []string{"Ф"}}
	live := map[string]bool{"get_object_structure": true, "execute_query": true}
	if got := nextStepsFor(obj, func(n string) bool { return live[n] }); len(got) != 0 {
		t.Errorf("live hints name offline tools: %v", got)
	}
	only := nextStepsFor(obj, func(n string) bool { return n == "get_movements" })
	if len(only) != 1 || !strings.Contains(only[0], "get_movements") {
		t.Errorf("want only the get_movements step, got %v", only)
	}
}
