package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type visibilityInput struct {
	ObjectType string `json:"objectType" jsonschema:"metadata type, e.g. Document, Catalog, Report"`
	Name       string `json:"name" jsonschema:"object name without the type prefix, e.g. ЗаказКлиента"`
	WithRights bool   `json:"withRights,omitempty" jsonschema:"also count granting roles (slower)"`
}

// registerVisibilityAudit wires the offline visibility_audit tool.
func registerVisibilityAudit(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "visibility_audit",
		Description: "Why a user does not see an object, attribute or command. Visibility needs a subsystem that reaches the command interface, at least one covering functional option switched on (options are ORed, none means visible) and a role right. Returns subsystem placements, every functional option over the object, its attributes, commands, tabular sections or section, where each value is stored, and optionally the granting role count. Offline. Call it for \"не вижу документ/реквизит/команду\" before digging into rights, and after adding an object.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in visibilityInput) (*mcp.CallToolResult, source.VisibilityReport, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.VisibilityReport{}, errNoSource
		}
		rep, err := xs.VisibilityAudit(ctx, in.ObjectType, in.Name, in.WithRights)
		if err != nil {
			return nil, source.VisibilityReport{}, err
		}
		return nil, *rep, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "command_visibility",
		Description: "Role-based command visibility from CommandInterface.xml: per command the default and the per-role overrides. role= resolves what one role sees, subsystem= scopes it. Use it when access is limited by hiding commands rather than by rights: rights_audit does not see this file (rights on a section: rights_audit type=Subsystem). Offline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandVisibilityInput) (*mcp.CallToolResult, source.CommandVisibility, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.CommandVisibility{}, errNoSource
		}
		// Unscoped scans return over a thousand commands in a production
		// configuration, so default to the ones carrying role overrides.
		rolesOnly := in.RolesOnly || (in.Subsystem == "" && !in.All)
		rep, err := xs.CommandVisibilityAudit(ctx, in.Subsystem, in.Role, rolesOnly)
		if err != nil {
			return nil, source.CommandVisibility{}, err
		}
		return nil, *rep, nil
	})
}

type commandVisibilityInput struct {
	Subsystem string `json:"subsystem,omitempty" jsonschema:"one subsystem, e.g. CRMИМаркетинг"`
	Role      string `json:"role,omitempty" jsonschema:"what this role sees; Role.X or a bare name"`
	RolesOnly bool   `json:"rolesOnly,omitempty" jsonschema:"only commands with role overrides (default without subsystem)"`
	All       bool   `json:"all,omitempty" jsonschema:"also commands with only a default"`
}
