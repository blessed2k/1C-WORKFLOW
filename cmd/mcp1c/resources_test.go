package main

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResourceMetadataTree(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "onec://metadata"})
	if err != nil {
		t.Fatalf("read onec://metadata: %v", err)
	}
	if len(res.Contents) == 0 || !strings.Contains(res.Contents[0].Text, "Товары") {
		t.Errorf("metadata resource missing Товары; got: %+v", res.Contents)
	}
}

func TestResourceObjectTemplate(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	// Template advertised.
	lt, err := cs.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	found := false
	for _, tpl := range lt.ResourceTemplates {
		if tpl.URITemplate == "onec://object/{type}/{name}" {
			found = true
		}
	}
	if !found {
		t.Fatalf("object resource template not advertised")
	}

	// Read a concrete object through the template. RFC 6570 expansion
	// percent-encodes the Cyrillic name, as a compliant client would.
	uri := "onec://object/Catalog/" + url.PathEscape("Товары")
	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("read object resource: %v", err)
	}
	if len(res.Contents) == 0 || !strings.Contains(res.Contents[0].Text, "Артикул") {
		t.Errorf("object resource missing Артикул; got: %+v", res.Contents)
	}
}

func TestCompletionObjectName(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	r, err := cs.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: "onec://object/{type}/{name}"},
		Argument: mcp.CompleteParamsArgument{Name: "name", Value: "Тов"},
		Context:  &mcp.CompleteContext{Arguments: map[string]string{"type": "Catalog"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !containsStr(r.Completion.Values, "Товары") {
		t.Errorf("completion for name=Тов, type=Catalog missing Товары; got %v", r.Completion.Values)
	}
}

func TestFilterPrefixCap(t *testing.T) {
	var many []string
	for i := 0; i < 150; i++ {
		many = append(many, "Объект")
	}
	if got := filterPrefix(many, "об", 100); len(got) != 100 {
		t.Errorf("filterPrefix cap = %d, want 100", len(got))
	}
	if got := filterPrefix([]string{"Товары", "Валюты", "Контрагенты"}, "валю", 100); len(got) != 1 || got[0] != "Валюты" {
		t.Errorf("filterPrefix substring = %v, want [Валюты]", got)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
