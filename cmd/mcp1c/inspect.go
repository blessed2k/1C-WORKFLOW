package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type extensionContextInput struct {
	BaseDump string `json:"baseDump,omitempty" jsonschema:"base configuration export; adds original method text"`
}

type movementsInput struct {
	Name   string `json:"name" jsonschema:"document name without the type prefix, e.g. РеализацияТоваровУслуг"`
	Review bool   `json:"review,omitempty" jsonschema:"also review the posting code: inline or delegated, and its defects"`
}

// movementsOutput is the movements report plus, on review=true, the posting
// review that used to be the separate posting_review tool, unchanged.
type movementsOutput struct {
	source.MovementsReport
	Review *source.PostingReport `json:"review,omitempty" jsonschema:"posting code review (review=true): style inline/delegated/none/absent, handlers, findings"`
}

type rightsAuditInput struct {
	Type               string   `json:"type" jsonschema:"metadata type, e.g. Catalog, Document"`
	Name               string   `json:"name" jsonschema:"object name without the type prefix"`
	Roles              []string `json:"roles,omitempty" jsonschema:"what a user with exactly these roles gets"`
	Profile            string   `json:"profile,omitempty" jsonschema:"БСП access-group profile (Имя or Наименование)"`
	IncludeNotGranting bool     `json:"includeNotGranting,omitempty" jsonschema:"list every non-granting role, not a count and a sample"`
}

// rightsAuditOutput is the object-centric audit plus, when a role set or a
// profile was asked for, what that set actually resolves to.
type rightsAuditOutput struct {
	source.RightsAudit
	NotGrantingCount  int                     `json:"notGrantingCount,omitempty" jsonschema:"how many roles grant nothing here (the list itself is sampled unless includeNotGranting is set)"`
	UndeterminedCount int                     `json:"undeterminedCount,omitempty" jsonschema:"how many roles do not list the object but have setForNewObjects"`
	Effective         *source.EffectiveRights `json:"effective,omitempty" jsonschema:"rights as the platform resolves them over the requested role set"`
}

// notGrantingSample is how many "grants nothing" role names come back by default.
// The signal in that list is its length: a typical configuration has hundreds of
// roles, and printing every name once cost a session most of its context window
// for two useful lines (granting: [] and totalRoles: 642).
const notGrantingSample = 10

// sampleRoleLists trims the long "did not match" lists to a count plus a sample,
// unless the caller explicitly asked for everything.
func sampleRoleLists(out *rightsAuditOutput, full bool) {
	out.NotGrantingCount = len(out.NotGranting)
	out.UndeterminedCount = len(out.Undetermined)
	if full {
		return
	}
	if len(out.NotGranting) > notGrantingSample {
		out.NotGranting = append([]string(nil), out.NotGranting[:notGrantingSample]...)
		out.Note = appendNote(out.Note, fmt.Sprintf(
			"notGranting: показаны %d из %d ролей, полный список — includeNotGranting=true",
			notGrantingSample, out.NotGrantingCount))
	}
	if len(out.Undetermined) > notGrantingSample {
		out.Undetermined = append([]string(nil), out.Undetermined[:notGrantingSample]...)
		out.Note = appendNote(out.Note, fmt.Sprintf(
			"undetermined: показаны %d из %d ролей", notGrantingSample, out.UndeterminedCount))
	}
}

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// registerInspect wires the offline extension/movements/rights inspection tools.
// All three need the XML source directly (assert *XMLSource, error in live mode).
func registerInspect(server *mcp.Server, provide func() source.ConfigSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "extension_context",
		Description: "Reads the active dump as an extension (.cfe export): own vs borrowed objects and every interceptor (&Перед/&После/&Вместо/&ИзменениеИКонтроль); baseDump adds the original method text to each. Use it when the task touches an extension, before writing an interceptor. Offline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in extensionContextInput) (*mcp.CallToolResult, source.ExtensionContext, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, source.ExtensionContext{}, errNoSource
		}
		ec, err := xs.ExtensionContext(ctx, in.BaseDump)
		if err != nil {
			return nil, source.ExtensionContext{}, err
		}
		return nil, *ec, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_movements",
		Description: "Which registers a document posts to: declared RegisterRecords cross-checked with the object module (Движения.<X>, Записывать, fields set). review=true adds the posting code review: movements formed inline or delegated to a mechanism, a balance read without a lock, a query in a loop, a record set never written. Offline. Use it before changing posting, and with review=true after writing ОбработкаПроведения.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movementsInput) (*mcp.CallToolResult, movementsOutput, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, movementsOutput{}, errNoSource
		}
		rep, err := xs.Movements(ctx, in.Name)
		if err != nil {
			return nil, movementsOutput{}, err
		}
		out := movementsOutput{MovementsReport: *rep}
		if in.Review {
			review, err := xs.PostingReview(ctx, in.Name)
			if err != nil {
				return nil, movementsOutput{}, err
			}
			out.Review = review
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rights_audit",
		Description: "Rights on one object: which roles grant what (with RLS conditions), how many grant nothing, which БСП access-group profiles reach it. roles=[...] and/or profile= give the EFFECTIVE rights of that set: roles are ORed, so one role without RLS cancels the others' restrictions. profile= knows SUPPLIED profiles only (built in code); profiles created by users live in the base, query them in live mode. Offline. Use it for 'works as admin, fails as user' and 'RLS limits nothing' before release.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in rightsAuditInput) (*mcp.CallToolResult, rightsAuditOutput, error) {
		xs, ok := provide().(*source.XMLSource)
		if !ok {
			return nil, rightsAuditOutput{}, errNoSource
		}
		ra, err := xs.RightsAudit(ctx, in.Type, in.Name)
		if err != nil {
			return nil, rightsAuditOutput{}, err
		}
		out := rightsAuditOutput{RightsAudit: *ra}
		sampleRoleLists(&out, in.IncludeNotGranting)
		if len(in.Roles) > 0 || in.Profile != "" {
			er, err := xs.EffectiveRights(ctx, in.Type, in.Name, in.Roles, in.Profile)
			if err != nil {
				return nil, rightsAuditOutput{}, err
			}
			out.Effective = er
		}
		return nil, out, nil
	})
}
