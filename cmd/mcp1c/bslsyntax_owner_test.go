package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// TestBSLSyntaxOwner drives the owner forms of bsl_syntax through an MCP client
// over the synthetic corpus: the answer a caller parses, not the Go value.
func TestBSLSyntaxOwner(t *testing.T) {
	cs := connectServer(t, newServer(options{syntaxIndex: syntaxtest.FixtureFile(t)}))

	type answer struct {
		Count   int    `json:"count"`
		Owner   string `json:"owner"`
		Total   int    `json:"total"`
		Note    string `json:"note"`
		Matches []struct {
			NameRu string `json:"nameRu"`
			Owner  string `json:"owner"`
			Params string `json:"params"`
		} `json:"matches"`
		Members []map[string]any `json:"members"`
		Type    *struct {
			Name         string `json:"name"`
			Members      int    `json:"members"`
			Constructors []struct {
				Signature string `json:"signature"`
			} `json:"constructors"`
		} `json:"type"`
		Results []struct {
			Owner   string `json:"owner"`
			Matches []struct {
				Owner string `json:"owner"`
			} `json:"matches"`
		} `json:"results"`
	}
	call := func(t *testing.T, args map[string]any) answer {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "bsl_syntax", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("bsl_syntax %v: %v %s", args, err, contentText(res))
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var a answer
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("structured content: %v", err)
		}
		return a
	}

	for _, tc := range []struct {
		name  string
		args  map[string]any
		check func(t *testing.T, a answer)
	}{
		{"owner alone lists compact members", map[string]any{"owner": "ТаблицаЗначений"}, func(t *testing.T, a answer) {
			if a.Owner != "ТаблицаЗначений" || a.Count != 5 || a.Total != 5 || len(a.Members) != 5 || len(a.Matches) != 0 {
				t.Fatalf("got owner %q count %d total %d members %d matches %d", a.Owner, a.Count, a.Total, len(a.Members), len(a.Matches))
			}
			if a.Members[0]["kind"] != "constructor" {
				t.Errorf("constructors must come first: %v", a.Members[0])
			}
			for _, m := range a.Members {
				for _, full := range []string{"params", "description", "example"} {
					if _, ok := m[full]; ok {
						t.Errorf("compact member carries %q: %v", full, m)
					}
				}
			}
		}},
		{"Type.Member gives the one member in full", map[string]any{"query": "ТаблицаЗначений.Свернуть"}, func(t *testing.T, a answer) {
			if a.Owner != "ТаблицаЗначений" || len(a.Matches) != 1 || a.Matches[0].Owner != "ТаблицаЗначений" || a.Matches[0].Params == "" {
				t.Fatalf("got %+v", a)
			}
		}},
		{"query+owner filters by owner", map[string]any{"query": "Свернуть", "owner": "ТаблицаФормы"}, func(t *testing.T, a answer) {
			if len(a.Matches) != 1 || a.Matches[0].Owner != "ТаблицаФормы" {
				t.Fatalf("got %+v", a.Matches)
			}
		}},
		{"a type name reports its constructors", map[string]any{"query": "HTTPСоединение"}, func(t *testing.T, a answer) {
			if a.Type == nil || a.Type.Name != "HTTPСоединение" || a.Type.Members != 3 || len(a.Type.Constructors) != 2 ||
				!strings.HasPrefix(a.Type.Constructors[0].Signature, "Новый HTTPСоединение(") {
				t.Fatalf("got type %+v", a.Type)
			}
		}},
		{"a global function answers as before", map[string]any{"query": "СтрНайти"}, func(t *testing.T, a answer) {
			if a.Type != nil || a.Owner != "" || len(a.Matches) == 0 || a.Matches[0].NameRu != "СтрНайти" {
				t.Fatalf("got %+v", a)
			}
		}},
		{"owner applies to every query of a batch", map[string]any{"queries": []string{"Свернуть", "Найти"}, "owner": "ТаблицаЗначений"}, func(t *testing.T, a answer) {
			if len(a.Results) != 2 {
				t.Fatalf("got %d results", len(a.Results))
			}
			for _, r := range a.Results {
				if r.Owner != "ТаблицаЗначений" || len(r.Matches) != 1 || r.Matches[0].Owner != "ТаблицаЗначений" {
					t.Errorf("batch result %+v", r)
				}
			}
		}},
		{"unknown owner is a note, not an error", map[string]any{"owner": "НетТакогоТипа"}, func(t *testing.T, a answer) {
			if a.Count != 0 || !strings.Contains(a.Note, "не найден") {
				t.Fatalf("got count %d note %q", a.Count, a.Note)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, call(t, tc.args)) })
	}

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "bsl_syntax", Arguments: map[string]any{}})
	if err == nil && !res.IsError {
		t.Errorf("bsl_syntax without query, queries and owner must be an error")
	}
}
