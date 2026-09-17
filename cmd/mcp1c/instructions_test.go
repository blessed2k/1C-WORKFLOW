package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// liveOnlyTools are routed only by a live server; offline instructions must
// not name them.
var liveOnlyTools = []string{
	"set_base", "list_bases", "execute_query", "validate_query", "analyze_query",
	"get_event_log", "get_predefined", "check_sync",
}

// offlineOnlyTools are routed only by an offline server.
var offlineOnlyTools = []string{
	"set_dump", "list_projects", "context_pack", "find_metadata_usages", "get_query_schema",
	"query_advisor", "extension_context", "rights_audit", "validate_bsl", "form_impact", "get_movements",
}

// instructionVariant is one server shape whose handshake text is checked.
type instructionVariant struct {
	name    string
	mode    режимИнструмента
	profile toolsProfile
	index   реестрСостояние
}

func instructionVariants() []instructionVariant {
	var out []instructionVariant
	for _, mode := range []struct {
		name string
		v    режимИнструмента
	}{{"offline", режимОффлайн}, {"live", режимLive}} {
		for _, profile := range []toolsProfile{profileFull, profileCore} {
			for _, idx := range []struct {
				name string
				v    реестрСостояние
			}{{"index", реестрОткрыт}, {"no-index", безРеестра}} {
				out = append(out, instructionVariant{
					name: mode.name + "/" + string(profile) + "/" + idx.name,
					mode: mode.v, profile: profile, index: idx.v,
				})
			}
		}
	}
	return out
}

// handshake connects to the variant and returns its instructions and tools.
func handshake(t *testing.T, v instructionVariant) (string, map[string]bool) {
	t.Helper()
	cs := сессияКлиента(t, optionsFor(t, v.mode, v.profile, v.index))
	list, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	have := map[string]bool{}
	for _, tool := range list.Tools {
		have[tool.Name] = true
	}
	return cs.InitializeResult().Instructions, have
}

// mentions reports whether text names tool as a whole word, so that
// get_object is not found inside get_object_structure.
func mentions(text, tool string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], tool)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(tool)
		before := start == 0 || !isNameByte(text[start-1])
		after := end == len(text) || !isNameByte(text[end])
		if before && after {
			return true
		}
		i = end
	}
}

func isNameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// TestInstructionsDelivered checks that the routing map reaches the client at
// handshake (it lands in the model's system context) and routes the phases that
// matter most for the mode.
func TestInstructionsDelivered(t *testing.T) {
	for _, v := range instructionVariants() {
		t.Run(v.name, func(t *testing.T) {
			got, _ := handshake(t, v)
			if got == "" {
				t.Fatal("server sent no instructions")
			}
			want := []string{"server_info", "bsl_syntax", "WRITE CODE", "VERIFY WHAT WAS WRITTEN"}
			if v.mode == режимОффлайн {
				want = append(want, "context_pack", "find_metadata_usages", "get_query_schema",
					"query_advisor", "rights_audit", "get_movements", "form_impact", "validate_bsl")
				if v.profile == profileFull {
					want = append(want, "extension_context")
				}
			} else {
				want = append(want, "execute_query", "validate_query", "analyze_query", "check_sync")
			}
			if v.index == реестрОткрыт {
				want = append(want, "INDEXED", "get_context_for_task", "find_impact")
			}
			for _, w := range want {
				if !strings.Contains(got, w) {
					t.Errorf("instructions do not route %q:\n%s", w, got)
				}
			}
		})
	}
}

// TestInstructionsNameOnlyRegisteredTools: every tool the text names is one the
// client can call; offline never names live tools and the other way round,
// a server without the index has no INDEXED section, core does not name what
// it left out.
func TestInstructionsNameOnlyRegisteredTools(t *testing.T) {
	for _, v := range instructionVariants() {
		t.Run(v.name, func(t *testing.T) {
			got, have := handshake(t, v)
			for _, к := range контрактыИнструментов {
				if mentions(got, к.имя) && !have[к.имя] {
					t.Errorf("instructions name %q, which this server does not register", к.имя)
				}
			}
			forbidden := liveOnlyTools
			if v.mode == режимLive {
				forbidden = offlineOnlyTools
			}
			for _, name := range forbidden {
				if mentions(got, name) {
					t.Errorf("instructions name %q from the other mode", name)
				}
			}
			if v.profile == profileCore {
				for _, name := range coreExcludedTools {
					if mentions(got, name) {
						t.Errorf("core instructions name excluded %q", name)
					}
				}
			}
			if v.index != реестрОткрыт && strings.Contains(got, "INDEXED") {
				t.Error("no index, but the INDEXED section is there")
			}
			if mentions(got, "posting_review") {
				t.Error("instructions still route the removed posting_review")
			}
		})
	}
}

// TestInstructionsSingleFirstRule: one "first" per variant, and it is the right
// one: get_context_for_task with the index, context_pack without it (offline).
func TestInstructionsSingleFirstRule(t *testing.T) {
	for _, v := range instructionVariants() {
		t.Run(v.name, func(t *testing.T) {
			got, _ := handshake(t, v)
			n := strings.Count(strings.ToLower(got), "first")
			switch {
			case v.index == реестрОткрыт:
				if n != 1 || !strings.Contains(got, "get_context_for_task first") {
					t.Errorf("want exactly one first rule on get_context_for_task, got %d:\n%s", n, got)
				}
			case v.mode == режимОффлайн:
				if n != 1 || !strings.Contains(got, "context_pack first") {
					t.Errorf("want exactly one first rule on context_pack, got %d:\n%s", n, got)
				}
			default:
				if n > 1 {
					t.Errorf("%d first rules:\n%s", n, got)
				}
			}
		})
	}
}

// TestServerInstructionsFitClientLimit guards the failure the delivery tests
// could not see: the client truncates instructions at instructionsLimit runes,
// silently and mid-sentence. The text had grown to 2568 runes, so the last 520
// (the entire VERIFY section) never reached the model, while the tests stayed
// green because they check what the server SENDS, not what the model RECEIVES.
// Every variant is checked, and the fullest text (every tool present) too.
func TestServerInstructionsFitClientLimit(t *testing.T) {
	check := func(t *testing.T, text string) {
		t.Helper()
		n := len([]rune(text))
		if n > instructionsLimit {
			t.Fatalf("instructions are %d runes, the client truncates at %d: the last %d runes never reach the model (the bottom sections go first). Shorten the text, do not raise the limit.",
				n, instructionsLimit, n-instructionsLimit)
		}
		// Headroom, so that adding one tool line cannot silently cross the limit.
		if left := instructionsLimit - n; left < 100 {
			t.Errorf("only %d runes left before truncation: shorten the text before adding tools", left)
		}
		if strings.ContainsRune(text, '\u2014') {
			t.Error("instructions contain an em dash")
		}
	}
	t.Run("all-tools", func(t *testing.T) {
		check(t, buildInstructions(func(string) bool { return true }))
	})
	for _, v := range instructionVariants() {
		t.Run(v.name, func(t *testing.T) {
			got, _ := handshake(t, v)
			t.Logf("%d runes", len([]rune(got)))
			check(t, got)
		})
	}
}

// TestInstructionsRouteWriteAndVerify pins the phases that the truncation ate.
// They sit at the bottom of the text, so they are the first to disappear when it
// grows, and they route exactly the tools the model would otherwise replace
// with its own recall.
func TestInstructionsRouteWriteAndVerify(t *testing.T) {
	text := buildInstructions(func(string) bool { return true })
	for _, want := range []string{
		"WRITE CODE", "VERIFY WHAT WAS WRITTEN",
		"bsl_syntax", "get_query_schema",
		"query_advisor", "form_impact", "get_movements", "validate_bsl",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions do not route %q", want)
		}
	}
}

// TestToolDescriptionsHaveTriggers guards the rule that a description must say
// WHEN to call the tool, not only what it does: the model picks tools by these.
func TestToolDescriptionsHaveTriggers(t *testing.T) {
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	// With a registry, so the indexed tools are checked too.
	ss, err := newServer(options{dumpDir: "testdata/export", projectsRoot: t.TempDir()}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	lt, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(lt.Tools) == 0 {
		t.Fatal("no tools advertised")
	}
	// A trigger phrase tells the model the situation in which to reach for it.
	triggers := []string{"Use it", "Call it", "Consult", "before", "after", "when"}
	for _, tool := range lt.Tools {
		has := false
		for _, tr := range triggers {
			if strings.Contains(tool.Description, tr) {
				has = true
				break
			}
		}
		if !has {
			t.Errorf("tool %q has no when-to-call trigger: %q", tool.Name, tool.Description)
		}
	}
}
