package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// индексныеИнструменты: the tools that exist only when the project registry
// opened. Pinned here literally, not taken from the registry, so that a tool
// moving between the layers shows up as a deliberate edit of this list.
var индексныеИнструменты = []string{
	"find_api", "find_impact", "find_queries_using", "find_references", "find_register_writes",
	"find_symbol", "get_context_for_task", "get_form_handlers", "get_module_structure",
	"get_object", "get_symbol", "index_status", "object_graph", "reindex", "trace_call_graph",
}

// исключеныВCore: what --tools=core leaves out. exchange_audit,
// new_object_checklist and object_graph stay in core on purpose: the
// not-in-exchange and new-object prompts and the find_register_writes hint
// route to them.
var исключеныВCore = []string{
	"bsp_extension_points", "command_visibility", "dump_diff",
	"extension_context", "get_configuration_info", "list_projects",
}

// реестрСостояние is the third axis of the surface: no --projects-root, a
// registry that opened, and a registry file that cannot be read.
type реестрСостояние int

const (
	безРеестра реестрСостояние = iota
	реестрОткрыт
	реестрСломан
)

func projectsRootFor(t *testing.T, состояние реестрСостояние) string {
	t.Helper()
	switch состояние {
	case реестрОткрыт:
		return t.TempDir()
	case реестрСломан:
		root := t.TempDir()
		dir := filepath.Join(root, ".mcp1c")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "registry.json"), []byte("{не json"), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	return ""
}

func optionsFor(t *testing.T, режим режимИнструмента, профиль toolsProfile, состояние реестрСостояние) options {
	t.Helper()
	opts := параметрыРежима(режим)
	opts.toolsProfile = профиль
	opts.projectsRoot = projectsRootFor(t, состояние)
	return opts
}

func registeredToolNames(t *testing.T, opts options) []string {
	t.Helper()
	список, err := сессияКлиента(t, opts).ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var names []string
	for _, tool := range список.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// TestToolSurfaceByModeProfileRegistry: the advertised set is exactly the
// contract of the mode, minus the indexed tools when the registry is not
// available, minus the core exclusions under --tools=core.
func TestToolSurfaceByModeProfileRegistry(t *testing.T) {
	индексные := map[string]bool{}
	for _, имя := range индексныеИнструменты {
		индексные[имя] = true
	}
	исключённые := map[string]bool{}
	for _, имя := range исключеныВCore {
		исключённые[имя] = true
	}
	for _, режим := range []struct {
		имя  string
		флаг режимИнструмента
	}{{"offline", режимОффлайн}, {"live", режимLive}} {
		for _, профиль := range []toolsProfile{profileFull, profileCore} {
			for _, состояние := range []struct {
				имя string
				v   реестрСостояние
			}{{"no-registry", безРеестра}, {"registry", реестрОткрыт}, {"broken-registry", реестрСломан}} {
				t.Run(режим.имя+"/"+string(профиль)+"/"+состояние.имя, func(t *testing.T) {
					want := map[string]bool{}
					for _, к := range контрактыИнструментов {
						if к.режимы&режим.флаг == 0 {
							continue
						}
						if индексные[к.имя] && состояние.v != реестрОткрыт {
							continue
						}
						if профиль == profileCore && исключённые[к.имя] {
							continue
						}
						want[к.имя] = true
					}
					got := registeredToolNames(t, optionsFor(t, режим.флаг, профиль, состояние.v))
					gotSet := map[string]bool{}
					for _, имя := range got {
						gotSet[имя] = true
						if !want[имя] {
							t.Errorf("зарегистрирован лишний инструмент %q", имя)
						}
					}
					for имя := range want {
						if !gotSet[имя] {
							t.Errorf("не зарегистрирован инструмент %q", имя)
						}
					}
					if gotSet["posting_review"] {
						t.Error("posting_review удалён, его вход живёт в get_movements review=true")
					}
				})
			}
		}
	}
}

// TestToolSurfaceIndexedListComplete keeps индексныеИнструменты honest: with an
// open registry every one of them is registered in both modes.
func TestToolSurfaceIndexedListComplete(t *testing.T) {
	for _, режим := range []режимИнструмента{режимОффлайн, режимLive} {
		with := map[string]bool{}
		for _, имя := range registeredToolNames(t, optionsFor(t, режим, profileFull, реестрОткрыт)) {
			with[имя] = true
		}
		without := map[string]bool{}
		for _, имя := range registeredToolNames(t, optionsFor(t, режим, profileFull, безРеестра)) {
			without[имя] = true
		}
		var diff []string
		for имя := range with {
			if !without[имя] {
				diff = append(diff, имя)
			}
		}
		sort.Strings(diff)
		want := append([]string(nil), индексныеИнструменты...)
		sort.Strings(want)
		if len(diff) != len(want) {
			t.Fatalf("режим %d: индексные по факту %v, в списке %v", режим, diff, want)
		}
		for i := range want {
			if diff[i] != want[i] {
				t.Fatalf("режим %d: индексные по факту %v, в списке %v", режим, diff, want)
			}
		}
	}
}

// TestServerInfoReportsProfile: server_info names the active tool profile.
func TestServerInfoReportsProfile(t *testing.T) {
	for _, профиль := range []toolsProfile{"", profileFull, profileCore} {
		opts := options{toolsProfile: профиль}
		res, err := сессияКлиента(t, opts).CallTool(context.Background(),
			&mcp.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Fatalf("server_info: %v %s", err, contentText(res))
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var out struct {
			Profile string `json:"profile"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if want := string(профиль.orDefault()); out.Profile != want {
			t.Errorf("profile = %q, want %q", out.Profile, want)
		}
	}
}
