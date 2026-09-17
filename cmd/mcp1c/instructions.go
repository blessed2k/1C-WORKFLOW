package main

import "strings"

// The server instructions are sent to the client at handshake and land in the
// model's system context. Tool descriptions say what one tool does; this map
// says how the tools relate: which phase of a 1C task calls which.
//
// The text is built per session (toolSurface.instructionsMiddleware, surface.go)
// from the tools the server actually registered
// (mode, --tools profile, whether the project registry opened). A line only
// names tools that exist: an offline server does not route to execute_query,
// a server without the index has no INDEXED section, core does not name what
// it left out. A line whose tools are all absent disappears, and so does a
// section left without lines.
//
// HARD LIMIT: the client truncates instructions at 2048 runes, silently and
// mid-sentence. Everything past it never reaches the model, so the sections at
// the bottom are the ones that disappear first. Keep it terse: state WHEN to call
// a tool and let the tool's own description say what it returns. When a tool is
// added, add one short line and keep every variant under instructionsLimit;
// TestServerInstructionsFitClientLimit checks each mode x profile x index
// combination.
//
// Not every tool needs a line here. A tool whose name already says when to reach
// for it (get_configuration_info, get_metadata_tree, search_code) is found
// through its own description; the map exists for the cases where the model's
// default is wrong: writing a query from memory instead of fetching the schema,
// touching a form without checking who else changes it. That is why the WRITE and
// VERIFY sections stay verbose while EXPLORE is one line.
//
// When the limit is hit again, cut in this order: names that are self-evident,
// then the explore/context lines, and only then group the analyse section. Never
// thin out WRITE and VERIFY: those two are what the truncation ate the first
// time, and they are the ones that change the model's behaviour.
//
// INDEXED was added once the indexed layer shipped without a single line here:
// a client connecting fresh had no way to learn those tools exist. Fitting it
// cost real headroom, taken per the cut order above. Full detail for every
// dropped nuance lives in each tool's own description and in docs/tools-index.md.
//
// There is exactly one "first" rule per variant: with the index it is
// get_context_for_task on a task, without it context_pack on an object.

// instrLine is one routing line: the tools it names and the text around them.
// format holds one %s for the present tool names joined with "/".
type instrLine struct {
	tools  []string
	format string
	// onlyWith / onlyWithout make the line conditional on another tool.
	onlyWith    string
	onlyWithout string
}

// instrSection is a heading plus its lines. Inline sections put the lines on
// the heading row separated by "; ", block sections one per indented row.
type instrSection struct {
	head   string
	inline bool
	lines  []instrLine
}

const instructionsPreamble = "1C (BSL) server; prefer these tools over raw XML/BSL."

var instructionSections = []instrSection{
	{head: "CONTEXT:", inline: true, lines: []instrLine{
		{tools: []string{"set_dump", "list_projects"}, format: "%s (offline)"},
		{tools: []string{"set_base", "list_bases"}, format: "%s (live)"},
		{tools: []string{"server_info"}, format: "%s=active"},
	}},
	{head: "START:", inline: true, lines: []instrLine{
		{tools: []string{"get_context_for_task"}, format: "%s first on any task: one call, minimal context"},
	}},
	{head: "EXPLORE:", inline: true, lines: []instrLine{
		{tools: []string{"context_pack"}, onlyWithout: "get_context_for_task",
			format: "%s first on an object: structure+module+usages"},
		{tools: []string{"context_pack"}, onlyWith: "get_context_for_task",
			format: "%s: one object's structure+module+usages"},
	}},
	{head: "ANALYSE BEFORE CHANGING:", inline: true, lines: []instrLine{
		{tools: []string{"find_metadata_usages", "find_dependency_paths"}, format: "%s"},
		{tools: []string{"extension_context"}, format: "%s (.cfe)"},
		{tools: []string{"write_path"}, format: "%s (ПередЗаписью/ПриЗаписи/posting)"},
		{tools: []string{"exchange_audit"}, format: "%s (exchange gap)"},
		{tools: []string{"get_movements"}, format: "%s (+review=true: posting code)"},
		{tools: []string{"rights_audit", "visibility_audit"}, format: "%s (rights/RLS, hidden object)"},
		{tools: []string{"form_impact"}, format: "%s (form change)"},
		{tools: []string{"bsp_extension_points"}, format: "%s (extend)"},
		{tools: []string{"new_object_checklist"}, format: "%s (new object)"},
	}},
	{head: "INDEXED:", lines: []instrLine{
		{tools: []string{"index_status", "reindex"}, format: "%s: freshness; reindex projectRoot registers/rebuilds it (only way)."},
		{tools: []string{"find_symbol", "get_symbol", "get_object", "get_module_structure"}, format: "%s: lookup, not raw XML."},
		{tools: []string{"find_references", "trace_call_graph", "find_impact"}, format: "%s: callers/call graph/blast radius pre-change."},
		{tools: []string{"find_queries_using", "find_register_writes", "get_form_handlers"}, format: "%s: field/register/form use pre-change."},
	}},
	{head: "WRITE CODE, call while writing: recall of the platform is often stale.", lines: []instrLine{
		{tools: []string{"get_query_schema"}, format: "%s: before a query: exact fields, virtual tables, parameters."},
		{tools: []string{"bsl_syntax"}, format: "%s: a platform method/type you are about to call."},
	}},
	{head: "VERIFY WHAT WAS WRITTEN, on the finished draft, before applying it.", lines: []instrLine{
		{tools: []string{"validate_bsl"}, format: "%s: module or procedure: missing metadata, wrong argument counts."},
		{tools: []string{"form_impact"}, format: "%s + draftCode: form code: draft checked against the real sources."},
		{tools: []string{"query_advisor"}, format: "%s: written query: anti-patterns, unindexed filters."},
		{tools: []string{"validate_query", "analyze_query"}, format: "%s: compile-check, heavy-query check."},
	}},
	{head: "LIVE, read-only:", inline: true, lines: []instrLine{
		{tools: []string{"execute_query"}, format: "%s (SELECT)"},
		{tools: []string{"get_event_log", "get_predefined", "check_sync"}, format: "%s"},
	}},
}

// buildInstructions renders the routing map for the tools has reports.
func buildInstructions(has func(string) bool) string {
	var b strings.Builder
	b.WriteString(instructionsPreamble)
	for _, sec := range instructionSections {
		var rendered []string
		for _, line := range sec.lines {
			if line.onlyWith != "" && !has(line.onlyWith) {
				continue
			}
			if line.onlyWithout != "" && has(line.onlyWithout) {
				continue
			}
			var present []string
			for _, name := range line.tools {
				if has(name) {
					present = append(present, name)
				}
			}
			if len(present) == 0 {
				continue
			}
			rendered = append(rendered, strings.Replace(line.format, "%s", strings.Join(present, "/"), 1))
		}
		if len(rendered) == 0 {
			continue
		}
		b.WriteString("\n\n")
		if sec.inline {
			b.WriteString(sec.head + " " + strings.Join(rendered, "; ") + ".")
			continue
		}
		b.WriteString(sec.head)
		for _, r := range rendered {
			b.WriteString("\n  " + r)
		}
	}
	return b.String()
}

// instructionsLimit is where the client cuts the instructions off. Staying under
// it is the difference between the VERIFY section reaching the model and not.
const instructionsLimit = 2048
