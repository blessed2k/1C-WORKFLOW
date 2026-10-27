package main

import (
	"context"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// Write profile (--tools=write): the server talks to the MCPЗапись extension
// (HTTP service mcp-write) of a TEST base and exposes only typed write actions,
// plus server_info and base switching. It is meant to run as a separate MCP
// server with its own bases file that lists test bases only: read and write
// tools never share one process, so a session cannot mix a production base
// into a write call by switching the active base.

// newWriteServer builds the server for --tools=write. The base registry is the
// same as in live mode, but every URL in it points at /hs/mcp-write.
func newWriteServer(opts options, impl *mcp.Implementation) (*mcp.Server, io.Closer) {
	ls := liveStateFromOptions(opts)
	server := mcp.NewServer(impl, nil)
	surface := newToolSurface()
	surfaces.Store(server, surface)
	describe := func() (string, string) {
		_, name := ls.desc()
		return "write", name
	}
	registerServerInfo(server, describe, &serverInfoState{profile: profileWrite})
	registerBaseSwitching(server, ls)
	registerWriteTools(server, ls)
	server.AddReceivingMiddleware(surface.instructionsMiddleware)
	return server, surfaceCloser{server: server, next: noopCloser{}}
}

// writeProvider is what the write tools need from the base registry: a client
// for a named base or for the active one, and the names for error messages.
type writeProvider interface {
	writeFor(name string) (*source.WriteClient, error)
	currentName() string
	names() []string
}

// resolveWrite picks the client for one call; an explicit base wins over the
// active one, as in resolveLive.
func resolveWrite(wp writeProvider, base string) (*source.WriteClient, string, error) {
	name := base
	if name == "" {
		name = wp.currentName()
	}
	if name == "" {
		return nil, "", noBaseError(wp.names())
	}
	client, err := wp.writeFor(name)
	if err != nil {
		return nil, "", err
	}
	return client, name, nil
}

// writeInput is one write tool's input: the target base and the JSON body sent
// to the extension. The body is the input itself without base: the json tags of
// the input are the contract of the extension, and omitempty there leaves an
// optional field to the extension's default.
type writeInput interface {
	target() string
	body() any
}

type objectWriteInput struct {
	Base            string                      `json:"base,omitempty" jsonschema:"test base to write to (see list_bases); omit for the active one"`
	Type            string                      `json:"type" jsonschema:"full metadata name: Справочник.Контрагенты or Документ.РеализацияТоваровУслуг"`
	Ref             string                      `json:"ref" jsonschema:"UUID of the object; for create generate it yourself and send the same ref on a retry: a repeated create returns the existing object (created=false) instead of a duplicate"`
	Attributes      map[string]any              `json:"attributes,omitempty" jsonschema:"attribute values by name: a date as 2026-10-27 or 2026-10-27T10:00:00, a reference as {type, ref}, an enum value as {type, value}, null for an empty value"`
	TabularSections map[string][]map[string]any `json:"tabular_sections,omitempty" jsonschema:"tabular sections by name, each an array of rows (field values as in attributes); a section that is sent replaces all its rows"`
	Mode            string                      `json:"mode,omitempty" jsonschema:"write or post (documents only); for update of a posted document omit it: it is reposted, as from its form"`
	DryRun          bool                        `json:"dry_run,omitempty" jsonschema:"write and roll back: the answer shows the result and, for a document, its movements, nothing stays in the base; number and code in it are provisional, the real write may get others"`
}

func (in objectWriteInput) target() string { return in.Base }

func (in objectWriteInput) body() any {
	in.Base = ""
	return in
}

type documentStateInput struct {
	Base   string `json:"base,omitempty" jsonschema:"test base to write to (see list_bases); omit for the active one"`
	Type   string `json:"type" jsonschema:"full metadata name of the document: Документ.РеализацияТоваровУслуг"`
	Ref    string `json:"ref" jsonschema:"UUID of the document"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"write and roll back; for post the answer shows the movements"`
}

func (in documentStateInput) target() string { return in.Base }

func (in documentStateInput) body() any {
	in.Base = ""
	return in
}

type deletionMarkInput struct {
	Base   string `json:"base,omitempty" jsonschema:"test base to write to (see list_bases); omit for the active one"`
	Type   string `json:"type" jsonschema:"full metadata name of a catalog or document"`
	Ref    string `json:"ref" jsonschema:"UUID of the object"`
	Value  bool   `json:"value" jsonschema:"true sets the deletion mark, false clears it"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"write and roll back"`
}

func (in deletionMarkInput) target() string { return in.Base }

func (in deletionMarkInput) body() any {
	in.Base = ""
	return in
}

type constantInput struct {
	Base   string `json:"base,omitempty" jsonschema:"test base to write to (see list_bases); omit for the active one"`
	Name   string `json:"name" jsonschema:"constant name: ЗаголовокСистемы or Константа.ЗаголовокСистемы"`
	Value  any    `json:"value" jsonschema:"new value, written as an attribute value (date, {type, ref}, {type, value}); to clear a string constant send an empty string, JSON null gives the empty value of the type (never send the text null)"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"write and roll back"`
}

func (in constantInput) target() string { return in.Base }

func (in constantInput) body() any {
	in.Base = ""
	return in
}

type registerWriteInput struct {
	Base       string         `json:"base,omitempty" jsonschema:"test base to write to (see list_bases); omit for the active one"`
	Type       string         `json:"type" jsonschema:"full name of an independent information register: РегистрСведений.КурсыВалют"`
	Period     string         `json:"period,omitempty" jsonschema:"period date of a periodic register (2026-10-27); it is moved to the start of the register period"`
	Dimensions map[string]any `json:"dimensions,omitempty" jsonschema:"dimension values by name; an omitted dimension is part of the key with an empty value"`
	Resources  map[string]any `json:"resources,omitempty" jsonschema:"resource values by name; the record is replaced whole, an omitted resource becomes empty"`
	Attributes map[string]any `json:"attributes,omitempty" jsonschema:"register attribute values by name"`
	DryRun     bool           `json:"dry_run,omitempty" jsonschema:"write and roll back"`
}

func (in registerWriteInput) target() string { return in.Base }

func (in registerWriteInput) body() any {
	in.Base = ""
	return in
}

type registerDeleteInput struct {
	Base       string         `json:"base,omitempty" jsonschema:"test base to write to (see list_bases); omit for the active one"`
	Type       string         `json:"type" jsonschema:"full name of an independent information register: РегистрСведений.КурсыВалют"`
	Period     string         `json:"period,omitempty" jsonschema:"period date of a periodic register (2026-10-27)"`
	Dimensions map[string]any `json:"dimensions,omitempty" jsonschema:"dimension values of the record key; an omitted dimension means an empty value"`
	DryRun     bool           `json:"dry_run,omitempty" jsonschema:"delete and roll back"`
}

func (in registerDeleteInput) target() string { return in.Base }

func (in registerDeleteInput) body() any {
	in.Base = ""
	return in
}

// writeMessage is one fill-check or write message of the extension.
type writeMessage struct {
	Text  string `json:"text"`
	Field string `json:"field,omitempty"`
}

// writeOutput is the answer of every write tool: the base it went to plus the
// result of the extension. ok=false with messages or write_error is a refused
// write the agent can fix, not a tool error.
type writeOutput struct {
	Base          string                      `json:"base"`
	Type          string                      `json:"type,omitempty"`
	Ref           string                      `json:"ref,omitempty"`
	Created       *bool                       `json:"created,omitempty"`
	OK            bool                        `json:"ok"`
	Written       bool                        `json:"written"`
	DryRun        bool                        `json:"dry_run"`
	Presentation  string                      `json:"presentation,omitempty"`
	Number        any                         `json:"number,omitempty"`
	Date          string                      `json:"date,omitempty"`
	Code          any                         `json:"code,omitempty"`
	Posted        *bool                       `json:"posted,omitempty"`
	DeletionMark  *bool                       `json:"deletion_mark,omitempty"`
	Messages      []writeMessage              `json:"messages,omitempty"`
	WriteError    string                      `json:"write_error,omitempty"`
	Movements     map[string][]map[string]any `json:"movements,omitempty"`
	Value         any                         `json:"value,omitempty"`
	PreviousValue any                         `json:"previous_value,omitempty"`
	Replaced      *bool                       `json:"replaced,omitempty"`
	Record        map[string]any              `json:"record,omitempty"`
	Deleted       *int                        `json:"deleted,omitempty"`
}

// addWriteTool registers one write tool that posts its input to path.
func addWriteTool[In writeInput](server *mcp.Server, wp writeProvider, name, description, path string) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, writeOutput, error) {
			client, base, err := resolveWrite(wp, in.target())
			if err != nil {
				return nil, writeOutput{}, err
			}
			var out writeOutput
			if err := client.Do(ctx, path, in.body(), &out); err != nil {
				return nil, writeOutput{}, fmt.Errorf("base %s: %w", base, err)
			}
			out.Base = base
			return nil, out, nil
		})
}

// writeToolNames lists the tools of the write profile, in registration order.
var writeToolNames = []string{
	"create_object", "update_object", "post_document", "unpost_document",
	"set_deletion_mark", "set_constant", "write_register_record", "delete_register_record",
}

// registerWriteTools wires the write actions of the MCPЗапись extension.
func registerWriteTools(server *mcp.Server, wp writeProvider) {
	const dryRunHint = " Call it with dry_run=true first, then without it; a number or code in a dry_run answer is provisional, take the one from the real write. Pass base=<name> to target one base for this call."
	addWriteTool[objectWriteInput](server, wp, "create_object",
		"Creates a catalog item or a document in a TEST base, as a user would from its form: filling defaults, fill check, write (mode=post also posts a document). Fill-check messages and a refused write come back as ok=false with messages/write_error, nothing is written then; fix the data and repeat."+dryRunHint,
		source.WritePathCreate)
	addWriteTool[objectWriteInput](server, wp, "update_object",
		"Changes attributes and tabular sections of an existing catalog item or document in a TEST base; a tabular section that is sent replaces all its rows, the others stay. The object is locked while it is written, so an object open in a user's form is not overwritten (locked error)."+dryRunHint,
		source.WritePathUpdate)
	addWriteTool[documentStateInput](server, wp, "post_document",
		"Posts a document in a TEST base, as the Post button does. With dry_run=true the answer lists the movements the posting would make (registers and their records) and nothing stays: use it to check posting without leaving data."+dryRunHint,
		source.WritePathPost)
	addWriteTool[documentStateInput](server, wp, "unpost_document",
		"Cancels the posting of a document in a TEST base: its movements are removed, the document stays."+dryRunHint,
		source.WritePathUnpost)
	addWriteTool[deletionMarkInput](server, wp, "set_deletion_mark",
		"Sets (value=true) or clears (value=false) the deletion mark of a catalog item or document in a TEST base; marking a posted document cancels its posting, as in its form. Nothing is deleted physically."+dryRunHint,
		source.WritePathDeletionMark)
	addWriteTool[constantInput](server, wp, "set_constant",
		"Writes the value of a constant in a TEST base; the answer has the previous value. To clear a string constant send value \"\" (an empty string), not the text null."+dryRunHint,
		source.WritePathConstant)
	addWriteTool[registerWriteInput](server, wp, "write_register_record",
		"Writes one record of an independent information register in a TEST base, keyed by period and dimensions; a record with the same key is replaced whole. Registers written by document posting are refused: post the document instead."+dryRunHint,
		source.WritePathRegisterWrite)
	addWriteTool[registerDeleteInput](server, wp, "delete_register_record",
		"Deletes the record of an independent information register in a TEST base with the given period and dimensions; deleted tells how many records there were."+dryRunHint,
		source.WritePathRegisterDelete)
}
