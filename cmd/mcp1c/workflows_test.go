package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestWorkflowPromptsDelivered checks that every chain is offered to the client
// and that a chain names real tools of this server in order.
func TestWorkflowPromptsDelivered(t *testing.T) {
	ctx := context.Background()
	cs := visClient(t, ctx)

	list, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	offered := map[string]bool{}
	for _, p := range list.Prompts {
		offered[p.Name] = true
	}
	for _, want := range []string{"add-attribute", "new-object", "why-invisible", "not-in-exchange", "change-posting"} {
		if !offered[want] {
			t.Errorf("prompt %q is not offered", want)
		}
	}

	res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "new-object",
		Arguments: map[string]string{"objectType": "Document", "name": "ЗаказПоставщику", "like": "ЗаказКлиента"},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d", len(res.Messages))
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	// The chain must carry the arguments into the steps and name the tools that
	// close the task, not only the first one.
	for _, want := range []string{"ЗаказПоставщику", "ЗаказКлиента", "new_object_checklist", "visibility_audit", "1.", "2."} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt text missing %q:\n%s", want, text)
		}
	}
}

// TestWorkflowPromptRequiresArguments pins that a chain refuses to produce a
// plan with a hole in it.
func TestWorkflowPromptRequiresArguments(t *testing.T) {
	ctx := context.Background()
	cs := visClient(t, ctx)
	if _, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "new-object",
		Arguments: map[string]string{"objectType": "Document", "name": "ЗаказПоставщику"},
	}); err == nil {
		t.Error("a chain without its reference object must be an error")
	}
}

// TestWorkflowPromptsNameExistingTools guards the chains against drift: a step
// naming a tool this server does not have sends the model nowhere. Every chain
// is rendered and scanned for every tool name the server has ever had; a name
// found there must be registered offline in both profiles (live-only tools
// excepted), and the removed posting_review must not appear at all.
func TestWorkflowPromptsNameExistingTools(t *testing.T) {
	known := map[string]режимИнструмента{"posting_review": 0}
	for _, к := range контрактыИнструментов {
		known[к.имя] = к.режимы
	}
	args := map[string]string{"objectType": "Document", "name": "Х", "like": "Y", "attribute": "Z", "role": "R"}
	var chains strings.Builder
	for _, wf := range workflowPrompts() {
		steps, err := wf.steps(args)
		if err != nil {
			t.Fatalf("%s: %v", wf.name, err)
		}
		chains.WriteString(strings.Join(steps, "\n") + "\n")
	}
	text := chains.String()
	for _, profile := range []toolsProfile{profileFull, profileCore} {
		have := map[string]bool{}
		for _, name := range registeredToolNames(t, options{toolsProfile: profile}) {
			have[name] = true
		}
		for name, modes := range known {
			if !strings.Contains(text, name) {
				continue
			}
			if modes == режимLive { // named as a live-mode step
				continue
			}
			if !have[name] {
				t.Errorf("%s: chains mention %q, which the server does not register", profile, name)
			}
		}
	}
	for _, name := range []string{"exchange_audit", "new_object_checklist", "get_movements"} {
		if !strings.Contains(text, name) {
			t.Errorf("chains no longer mention %q: revisit coreExcludedTools", name)
		}
	}
}
