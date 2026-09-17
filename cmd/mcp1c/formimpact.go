package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type formImpactInput struct {
	Type           string   `json:"type" jsonschema:"owner type (Document, Catalog, ...) or CommonForm"`
	Name           string   `json:"name" jsonschema:"owner name; for CommonForm the form name"`
	Form           string   `json:"form,omitempty" jsonschema:"form name, e.g. ФормаДокумента (omit for CommonForm)"`
	Dumps          []string `json:"dumps,omitempty" jsonschema:"export roots: base and every extension; default the active dump"`
	DraftCode      string   `json:"draftCode,omitempty" jsonschema:"BSL you are about to write (form module); checked against the real sources"`
	DraftExtension string   `json:"draftExtension,omitempty" jsonschema:"your extension name"`
	DraftPrefix    string   `json:"draftPrefix,omitempty" jsonschema:"your extension NamePrefix"`
}

// registerFormImpact wires the offline form_impact tool.
func registerFormImpact(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "form_impact",
		Description: "Who else changes a form programmatically across the base and every extension, how to add your code to THIS form safely, and predicted conflicts: &Вместо without ПродолжитьВызов (BSP commands silently vanish), ИзменитьРеквизиты with removals, name collisions between extensions, names without the extension prefix, moving or deleting foreign elements. Use it before programmatic form changes in a configuration with extensions; pass your planned code as draftCode to check it first. Offline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in formImpactInput) (*mcp.CallToolResult, source.FormImpact, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.FormImpact{}, errNoSource
		}
		var draft *source.DraftOptions
		if in.DraftCode != "" {
			draft = &source.DraftOptions{Code: in.DraftCode, Extension: in.DraftExtension, Prefix: in.DraftPrefix}
		}
		fi, err := xs.FormImpact(ctx, in.Type, in.Name, in.Form, in.Dumps, draft)
		if err != nil {
			return nil, source.FormImpact{}, err
		}
		return nil, *fi, nil
	})
}
