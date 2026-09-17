package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Elicitation lets the server ask the user a question mid-call instead of
// failing with "no source, call set_dump first" and hoping the model recovers.
// It is optional in the protocol: a client that does not declare the capability
// gets exactly the previous behaviour, so nothing here may become a hard
// dependency. It is used only where the answer is genuinely the user's to give
// (which export, which base) — never to have the model's own work confirmed.

// elicitSupport is what the connected client declared at handshake.
type elicitSupport struct {
	Supported bool `json:"supported"`
	Form      bool `json:"form" jsonschema:"form mode: a schema-driven prompt"`
	URL       bool `json:"url" jsonschema:"url mode: the user is sent to a page"`
}

// clientElicitation reads the capability off the session. A client that declares
// elicitation without naming a mode is treated as form-capable, which is what
// the SDK does when it dispatches the call.
func clientElicitation(session *mcp.ServerSession) elicitSupport {
	if session == nil {
		return elicitSupport{}
	}
	params := session.InitializeParams()
	if params == nil || params.Capabilities == nil || params.Capabilities.Elicitation == nil {
		return elicitSupport{}
	}
	caps := params.Capabilities.Elicitation
	out := elicitSupport{Supported: true, Form: caps.Form != nil, URL: caps.URL != nil}
	if caps.Form == nil && caps.URL == nil {
		out.Form = true
	}
	return out
}

// elicitOption is one offered value and the label shown for it.
type elicitOption struct {
	Value string
	Label string
}

// elicitPick asks the user for one value of field. With options the prompt is a
// closed list, without them a free-text field.
//
// An empty answer with a nil error means "no answer": the client cannot ask, the
// user declined or dismissed, or the call was refused. Callers fall back to
// their usual error then — the question is a shortcut, never a requirement.
func elicitPick(ctx context.Context, session *mcp.ServerSession, field, message, title, description string, options []elicitOption) (string, error) {
	if !clientElicitation(session).Form {
		return "", nil
	}

	property := map[string]any{"type": "string", "title": title}
	if description != "" {
		property["description"] = description
	}
	if len(options) > 0 {
		values := make([]string, 0, len(options))
		labels := make([]string, 0, len(options))
		for _, o := range options {
			values = append(values, o.Value)
			label := o.Label
			if label == "" {
				label = o.Value
			}
			labels = append(labels, label)
		}
		property["enum"] = values
		property["enumNames"] = labels
	}

	res, err := session.Elicit(ctx, &mcp.ElicitParams{
		Mode:    "form",
		Message: message,
		RequestedSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{field: property},
			"required":   []string{field},
		},
	})
	if err != nil {
		// A client may declare the capability and still refuse the call; that is
		// not a reason to fail the tool the user actually asked for.
		return "", nil
	}
	if res == nil || res.Action != "accept" {
		return "", nil
	}
	value, _ := res.Content[field].(string)
	return value, nil
}

// elicitChoice is elicitPick over a plain list of names.
func elicitChoice(ctx context.Context, session *mcp.ServerSession, field, message, title string, names []string) (string, error) {
	options := make([]elicitOption, 0, len(names))
	for _, n := range names {
		options = append(options, elicitOption{Value: n})
	}
	return elicitPick(ctx, session, field, message, title, "", options)
}

// elicitDumpDir asks which XML export to work with, offering the exports found
// under projectsRoot when there are any.
func elicitDumpDir(ctx context.Context, session *mcp.ServerSession, projectsRoot string) (string, error) {
	message := "Не выбрана XML-выгрузка. Укажите папку с Configuration.xml."
	var options []elicitOption
	if projectsRoot != "" {
		if projects := findProjects(projectsRoot, 3); len(projects) > 0 {
			for _, p := range projects {
				options = append(options, elicitOption{Value: p.Path, Label: p.Name})
			}
			message = fmt.Sprintf("Не выбрана XML-выгрузка. Найдено проектов в %s: %d. Какой взять?", projectsRoot, len(projects))
		}
	}
	return elicitPick(ctx, session, "path", message, "Папка XML-выгрузки",
		"Каталог, в котором лежит Configuration.xml", options)
}
