package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/retrieve"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// suggestable is every tool retrieve's next-step hints can name (nextToolsFor
// and the no-anchor path in internal/retrieve/build.go).
var suggestable = []string{
	"trace_call_graph", "find_references", "find_register_writes", "get_movements",
	"form_impact", "get_form_handlers", "new_object_checklist", "rights_audit",
	"find_metadata_usages", "get_query_schema", "query_advisor", "visibility_audit",
	"validate_bsl", "find_symbol", "get_object", "get_metadata_tree",
}

// TestNextToolHintsOnlyRegisteredTools: in live and in core the hints of
// get_context_for_task and get_object_structure keep only the tools the server
// advertises, read from the same tools/list as the instructions.
func TestNextToolHintsOnlyRegisteredTools(t *testing.T) {
	for _, v := range []struct {
		name    string
		mode    режимИнструмента
		profile toolsProfile
	}{
		{"live/full", режимLive, profileFull},
		{"live/core", режимLive, profileCore},
		{"offline/core", режимОффлайн, profileCore},
	} {
		t.Run(v.name, func(t *testing.T) {
			opts := optionsFor(t, v.mode, v.profile, реестрОткрыт)
			server, closer := newServerWithCloser(opts)
			t.Cleanup(func() { closer.Close() })
			ct, st := mcp.NewInMemoryTransports()
			ss, err := server.Connect(context.Background(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ss.Close() })
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(context.Background(), ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cs.Close() })
			list, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
			if err != nil {
				t.Fatal(err)
			}
			have := map[string]bool{}
			for _, tool := range list.Tools {
				have[tool.Name] = true
			}

			surface := surfaceOf(server)
			resp := app.Response[retrieve.Result]{Items: []retrieve.Result{{SuggestedNextTools: suggestable}}}
			keepAdvertisedNextTools(&resp, surface)
			got := resp.Items[0].SuggestedNextTools
			for _, name := range got {
				if !have[name] {
					t.Errorf("suggestedNextTools names %q, which this server does not register", name)
				}
			}
			for _, name := range suggestable {
				if have[name] && !slices.Contains(got, name) {
					t.Errorf("registered %q was dropped", name)
				}
			}
			if v.mode == режимLive && slices.Contains(got, "get_movements") {
				t.Error("live must not suggest get_movements")
			}
			for _, step := range nextStepsFor(sampleDocument(), surface.has) {
				for _, name := range []string{"get_query_schema", "get_movements", "form_impact", "find_metadata_usages"} {
					if strings.Contains(step, name) && !have[name] {
						t.Errorf("get_object_structure hint names unregistered %q", name)
					}
				}
			}
		})
	}
}

// TestInstructionsFallbackWhenToolListFails: if tools/list cannot be read, the
// client still gets the full map and the reason lands on stderr.
func TestInstructionsFallbackWhenToolListFails(t *testing.T) {
	var stderr bytes.Buffer
	surface := &toolSurface{stderr: &stderr}
	next := func(_ context.Context, method string, _ mcp.Request) (mcp.Result, error) {
		if method == "tools/list" {
			return nil, errors.New("сломано")
		}
		return &mcp.InitializeResult{}, nil
	}
	res, err := surface.instructionsMiddleware(next)(context.Background(), "initialize", &mcp.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := res.(*mcp.InitializeResult).Instructions
	if want := buildInstructions(func(string) bool { return true }); got != want {
		t.Errorf("fallback instructions differ from the full text:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "сломано") {
		t.Errorf("stderr does not name the failure: %q", stderr.String())
	}
	if !surface.has("execute_query") {
		t.Error("an unread tool list must not filter hints")
	}
}

func sampleDocument() source.ObjectStructure {
	return source.ObjectStructure{Type: "Document", Name: "Х", Forms: []string{"Ф"}}
}
