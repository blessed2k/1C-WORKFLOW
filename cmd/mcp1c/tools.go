package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// errNoSource is returned by data tools when no configuration source is active.
var errNoSource = errors.New(
	"no configuration source: call set_dump <xml-export-dir> for offline mode, or start the server with --base for live mode")

// errNoBase is returned by live tools when no base is selected in multi-base mode.
var errNoBase = errors.New("no live base selected: call set_base with a name from list_bases")

// noBaseError spells out what to choose from. With several bases configured the
// server starts with none selected on purpose, so this is the first thing a
// session sees — it has to name the options rather than send the caller hunting.
func noBaseError(names []string) error {
	if len(names) == 0 {
		return errNoBase
	}
	return fmt.Errorf("no live base selected: call set_base with one of %s, or pass base=<name> in this call",
		strings.Join(names, ", "))
}

// liveProvider is what live tools need from the base registry: the active base,
// a one-shot source for a named base, and the names for error messages.
type liveProvider interface {
	live() source.LiveSource
	liveFor(name string) (source.LiveSource, error)
	currentName() string
	names() []string
}

// resolveLive picks the source for one call. An explicit base wins over server
// state: a call that names its own target cannot be hijacked by a reconnect that
// reset the selection.
func resolveLive(lp liveProvider, base string) (source.LiveSource, string, error) {
	if base != "" {
		live, err := lp.liveFor(base)
		if err != nil {
			return nil, "", err
		}
		return live, base, nil
	}
	live := lp.live()
	if live == nil {
		return nil, "", noBaseError(lp.names())
	}
	return live, lp.currentName(), nil
}

type emptyInput struct{}

// dumpState is a thin view of the process's active project (app.Projects): the
// offline dump directory and the indexed project are one state there, so the
// raw and the indexed tools cannot drift onto different projects. It keeps no
// directory of its own; set_dump switches both halves at once.
type dumpState struct {
	projects *app.Projects
}

func (d *dumpState) get() string {
	return d.projects.DumpDir()
}

func (d *dumpState) set(dir string) app.ActiveProjectState {
	return d.projects.SetDump(dir)
}

// source returns a fresh XMLSource for the current directory, or nil if unset.
// XMLSource holds only a path and reads on demand, so rebuilding per call is cheap.
func (d *dumpState) source() source.ConfigSource {
	if dir := d.get(); dir != "" {
		return source.NewXMLSource(dir)
	}
	return nil
}

type bslSyntaxInput struct {
	Query   string   `json:"query,omitempty" jsonschema:"one name in Russian or English (exact, prefix or substring); Type.Member (ТаблицаЗначений.Свернуть) restricts it to that type"`
	Queries []string `json:"queries,omitempty" jsonschema:"several names in one call"`
	Owner   string   `json:"owner,omitempty" jsonschema:"type (owner) in Russian, e.g. ТаблицаЗначений: restricts matches to its members; without query lists all its members in compact form"`
	Limit   int      `json:"limit,omitempty" jsonschema:"max matches per query (default 20; members listing default 150)"`
}

// bslSyntaxOutput keeps query/matches for a single lookup (unchanged for existing
// callers) and adds results when several names were asked for at once. The
// owner fields appear only when a lookup is restricted to one owner, lists its
// members, or the query is itself a type name.
type bslSyntaxOutput struct {
	Query     string             `json:"query,omitempty"`
	Count     int                `json:"count"`
	Matches   []onec.SyntaxEntry `json:"matches,omitempty"`
	Results   []bslSyntaxResult  `json:"results,omitempty" jsonschema:"one entry per requested name, in the order asked"`
	Owner     string             `json:"owner,omitempty" jsonschema:"the owner (type) the lookup was restricted to"`
	Members   []syntax.Member    `json:"members,omitempty" jsonschema:"compact members of owner, by kind: constructors, methods, properties, events; query+owner gives one member in full"`
	Total     int                `json:"total,omitempty" jsonschema:"members of owner before the limit"`
	Truncated bool               `json:"truncated,omitempty" jsonschema:"members were cut at limit"`
	Type      *syntax.TypeInfo   `json:"type,omitempty" jsonschema:"query is itself a type: member count, constructors and how to list its members"`
	Note      string             `json:"note,omitempty"`
}

// bslSyntaxResult is one name's lookup inside a batch.
type bslSyntaxResult struct {
	Query   string             `json:"query"`
	Count   int                `json:"count"`
	Matches []onec.SyntaxEntry `json:"matches,omitempty"`
	Owner   string             `json:"owner,omitempty"`
	Type    *syntax.TypeInfo   `json:"type,omitempty"`
	Note    string             `json:"note,omitempty"`
}

type objectStructureInput struct {
	Type  string `json:"type" jsonschema:"metadata type, e.g. Catalog, Document, InformationRegister"`
	Name  string `json:"name" jsonschema:"object name without the type prefix, e.g. Контрагенты"`
	Parts string `json:"parts,omitempty" jsonschema:"comma-separated: forms, attributes, tabular, commands, other; default all"`
}

type metadataTreeInput struct {
	Type string `json:"type,omitempty" jsonschema:"only this metadata type"`
	Like string `json:"like,omitempty" jsonschema:"name substring, case-insensitive"`
}

type objectExistsInput struct {
	Type string `json:"type" jsonschema:"metadata type, e.g. Role, Document, Catalog"`
	Name string `json:"name" jsonschema:"object name without the type prefix"`
}

type objectExistsOutput struct {
	Exists  bool     `json:"exists"`
	Object  string   `json:"object" jsonschema:"the type.name that was looked up"`
	Similar []string `json:"similar,omitempty" jsonschema:"objects of the same type with a similar name, when there is no exact match"`
	Count   int      `json:"count" jsonschema:"objects of this type in the configuration"`
}

// filterTree narrows a metadata tree by type and name substring. The unfiltered
// tree of a production configuration is hundreds of thousands of characters, and
// it was routinely fetched whole to answer a yes/no question.
func filterTree(tree source.MetadataTree, typeName, like string) source.MetadataTree {
	if typeName == "" && like == "" {
		return tree
	}
	wantType := strings.ToLower(strings.TrimSpace(typeName))
	wantName := strings.ToLower(strings.TrimSpace(like))
	out := source.MetadataTree{Configuration: tree.Configuration}
	for _, g := range tree.Groups {
		if wantType != "" && strings.ToLower(g.Type) != wantType {
			continue
		}
		kept := source.MetadataGroup{Type: g.Type}
		for _, n := range g.Objects {
			if wantName != "" && !strings.Contains(strings.ToLower(n), wantName) {
				continue
			}
			kept.Objects = append(kept.Objects, n)
		}
		if len(kept.Objects) > 0 {
			out.Groups = append(out.Groups, kept)
			out.TotalObjects += len(kept.Objects)
		}
	}
	return out
}

// lookupObject answers the existence question and, on a miss, offers the names
// that are close enough to be what the caller meant.
func lookupObject(tree source.MetadataTree, typeName, name string) objectExistsOutput {
	out := objectExistsOutput{Object: typeName + "." + name}
	wantType := strings.ToLower(strings.TrimSpace(typeName))
	for _, g := range tree.Groups {
		if strings.ToLower(g.Type) != wantType {
			continue
		}
		out.Count = len(g.Objects)
		for _, n := range g.Objects {
			if n == name {
				out.Exists = true
				return out
			}
			if similarName(n, name) {
				out.Similar = append(out.Similar, n)
			}
		}
		if len(out.Similar) > 10 {
			out.Similar = out.Similar[:10]
		}
		return out
	}
	return out
}

// similarName decides whether an existing object is close enough to what was
// asked for to be worth offering. Substring matching alone is not enough in
// Russian metadata: Компания and Компании contain neither one another, and that
// pair is exactly the miss a caller needs help with.
func similarName(candidate, asked string) bool {
	a := []rune(strings.ToLower(candidate))
	b := []rune(strings.ToLower(asked))
	if strings.Contains(string(a), string(b)) || strings.Contains(string(b), string(a)) {
		return true
	}
	shared := 0
	for shared < len(a) && shared < len(b) && a[shared] == b[shared] {
		shared++
	}
	shortest := len(a)
	if len(b) < shortest {
		shortest = len(b)
	}
	if shortest == 0 {
		return false
	}
	// Four characters of prefix is noise; most of the shorter name is a lead.
	return shared >= 4 && shared*10 >= shortest*7
}

// applyParts drops the sections of an object structure the caller did not ask
// for. Empty means everything, so existing callers see no change.
func applyParts(obj *source.ObjectStructure, parts string) {
	if strings.TrimSpace(parts) == "" {
		return
	}
	want := map[string]bool{}
	for _, p := range strings.Split(parts, ",") {
		want[strings.ToLower(strings.TrimSpace(p))] = true
	}
	if !want["attributes"] {
		obj.Attributes = nil
	}
	if !want["tabular"] {
		obj.TabularSections = nil
	}
	if !want["forms"] {
		obj.Forms = nil
	}
	if !want["commands"] {
		obj.Commands = nil
	}
	if !want["other"] {
		obj.Other = nil
	}
}

type formStructureInput struct {
	Type string `json:"type" jsonschema:"owner type (Catalog, Document, ...) or CommonForm"`
	Name string `json:"name" jsonschema:"owner name; for CommonForm the form name"`
	Form string `json:"form,omitempty" jsonschema:"form name, e.g. ФормаСписка (omit for CommonForm)"`
}

type setDumpInput struct {
	// Optional on purpose: called with no path, the tool asks the user which
	// export to take (clients that support elicitation), instead of erroring.
	Path string `json:"path,omitempty" jsonschema:"export folder (with Configuration.xml); omit to be asked"`
}

type setDumpOutput struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	Configuration string `json:"configuration,omitempty"`
	IsExtension   bool   `json:"isExtension,omitempty"`
	ExportedAt    string `json:"exportedAt,omitempty" jsonschema:"when this export was written (Configuration.xml mtime)"`
	AgeDays       int    `json:"ageDays,omitempty" jsonschema:"how old the export is, in days"`
	Warning       string `json:"warning,omitempty" jsonschema:"present when the export is old enough that the live base has probably moved on"`
	Message       string `json:"message,omitempty"`
	IndexProject  string `json:"indexProject,omitempty" jsonschema:"indexed project that describes this export and is now active for the indexed tools; empty when no registered project does"`
	IndexHint     string `json:"indexHint,omitempty" jsonschema:"present when no indexed project describes this export: what to do so the indexed tools answer about it"`
}

// staleExportDays is when an export stops being evidence about the live base.
// "This code is not in the configuration" read off a two-week-old export is a
// guess, and it has already been reported as fact.
const staleExportDays = 7

// exportAge reports when the export was written and how old it is.
func exportAge(dir string) (string, int, bool) {
	fi, err := os.Stat(filepath.Join(dir, "Configuration.xml"))
	if err != nil {
		return "", 0, false
	}
	mod := fi.ModTime()
	return mod.Format("2006-01-02 15:04"), int(time.Since(mod).Hours() / 24), true
}

// closer releases what the index tools opened. Declared here and not in
// indexreg.go: that file is the tool registry and is not edited after task 10.
// Deps built from a failed newIndexToolDeps hold no projects at all — closing
// them is a no-op, not a nil dereference.
func (d indexToolDeps) closer() io.Closer {
	if d.projects == nil {
		return noopCloser{}
	}
	return d.projects
}

// noopCloser stands in when there is nothing to release, so callers never have
// to nil-check the closer they were handed.
type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// newServer builds the MCP server and registers all tools. The index store it
// opens stays open for the life of the process; callers that outlive the server
// — every test — want newServerWithCloser instead.
func newServer(opts options) *mcp.Server {
	server, closer := newServerWithCloser(opts)
	_ = closer
	return server
}

// newServerWithCloser builds the server and hands back the closer for what it
// opened: the per-project SQLite index and the background pipeline behind it.
//
// Without it nothing ever closes them. On unix that goes unnoticed — an open
// file still unlinks — but on Windows the open .sqlite pins its directory, so
// every test that pointed --projects-root at t.TempDir() failed in cleanup
// ("The process cannot access the file because it is being used by another
// process"), long after its own assertions had passed.
//
// The lifecycle newServer → Connect → ss.Wait() is untouched: this only adds
// the release step at the end of it.
func newServerWithCloser(opts options) (*mcp.Server, io.Closer) {
	impl := &mcp.Implementation{Name: serverName, Version: version}

	// The export cache is process-wide, and so are its tunables: resolved once in
	// parseFlags and handed over here. Without this call the cache keeps its
	// off-by-default behaviour — no expiry, no ceiling — whatever the flags say.
	source.ConfigureCache(opts.cacheTTL, opts.cacheLimitBytes)

	// The four attribution thresholds are process-wide too, and they live one
	// layer below: the indexing pipeline reads them from index.Config, which the
	// index-tool registry builds through app.DefaultIndexConfig. Without this
	// call the -graph-* flags parse and go nowhere, and attribution silently
	// runs on its own defaults. -graph-radius-nodes is NOT wired here: it caps
	// the graph service answer, not the pipeline.
	app.ConfigureGraphTunables(opts.graphChainDepth, opts.graphHubFanIn,
		opts.graphDepthPenalty, opts.graphHubPenalty)

	// Search cards of find_api are process-wide for the same reason: the flag
	// is parsed once, the search service reads the directory when it builds
	// its word index.
	app.ConfigureAPICards(opts.apiCardsDir())

	// NewLazy: a session that never asks for platform syntax never pays the
	// corpus parse. The index file lives outside the repository (cmd/syntaxgen
	// builds it from the user's platform); a missing or broken file is visible
	// only through idx.Err(), which the tools that need the corpus ask when they
	// are called. The server starts without it.
	syntaxPath := opts.syntaxIndexPath()
	warnMissingSyntaxIndex(syntaxPath)
	idx := syntax.NewLazy(syntaxPath)

	// Indexed-MCP facade (internal/app): its own workspace/project registry,
	// independent of the offline dump / live base selection above, so it is
	// wired into both branches below the same way. opts.projectsRoot doubles
	// as the workspace root the new registry lives under (docs/architecture-
	// index.md §8: "Workspace: каталог, заданный --projects-root").
	indexDeps, err := newIndexToolDeps(opts.projectsRoot, idx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: индексные инструменты недоступны: %v\n", serverName, err)
		// The active dump still lives in app.Projects even without a registry.
		indexDeps = indexToolDeps{projects: app.NewProjectsUnavailable(opts.projectsRoot, err)}
	}
	// The indexed tools are registered only when the registry is there: without
	// --projects-root, or with a registry that did not open, every one of them
	// could only answer no_active_project, and their definitions would still
	// cost the client several thousand tokens of context.
	indexAvailable := err == nil && strings.TrimSpace(opts.projectsRoot) != ""
	profile := opts.toolsProfile.orDefault()

	// Live mode: a single --base and/or a --bases file (multi-base, switchable).
	if opts.baseURL != "" || opts.basesFile != "" {
		ls := newLiveState()
		if opts.basesFile != "" {
			if err := ls.loadFile(opts.basesFile); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", serverName, err)
			}
		}
		if opts.baseURL != "" {
			ls.add(baseConfig{Name: "default", URL: opts.baseURL, User: opts.user, Password: opts.password})
			ls.setBase("default")
		} else if ls.count() == 1 {
			// One base means no ambiguity. With several, start with none selected:
			// defaulting to the first entry is what made a reconnect answer from
			// an unrelated base while looking perfectly healthy.
			ls.setBase(ls.firstName())
		}
		server := mcp.NewServer(impl, &mcp.ServerOptions{
			CompletionHandler: completionHandler(ls.source),
		})
		surface := newToolSurface()
		surfaces.Store(server, surface)
		registerCoreTools(server, ls.desc,
			&serverInfoState{project: liveProjectInfo(indexDeps.projects), profile: profile}, ls.source, idx)
		registerResources(server, ls.source)
		registerStandardsResources(server)
		registerLiveTools(server, ls)
		registerCheckSync(server, ls.live)
		registerBaseSwitching(server, ls)
		finishSurface(server, surface, indexDeps, indexAvailable, profile)
		return server, surfaceCloser{server: server, next: indexDeps.closer()}
	}

	// Offline / none: the dump directory is switchable via set_dump. A --dump at
	// start binds the indexed project exactly as set_dump does; without it the
	// active project picks the dump (app.Projects start rule).
	ds := &dumpState{projects: indexDeps.projects}
	if opts.dumpDir != "" {
		ds.set(opts.dumpDir)
	}
	describe := func() (string, string) {
		if d := ds.get(); d != "" {
			return "offline", d
		}
		return "none", ""
	}
	server := mcp.NewServer(impl, &mcp.ServerOptions{
		CompletionHandler: completionHandler(ds.source),
	})
	surface := newToolSurface()
	surfaces.Store(server, surface)
	registerCoreTools(server, describe,
		&serverInfoState{project: offlineProjectInfo(indexDeps.projects), profile: profile}, ds.source, idx)
	registerQuerySchema(server, ds.source)
	registerMetadataUsages(server, ds.source)
	registerDependencyPaths(server, ds.source)
	registerWritePath(server, ds.source)
	registerExtensionPoints(server, ds.source)
	registerExchangeAudit(server, ds.source)
	registerVisibilityAudit(server, ds.source)
	registerChecklist(server, ds.source)
	registerQueryAdvisor(server, ds.source)
	registerContextPack(server, ds.source)
	registerInspect(server, ds.source)
	registerFormImpact(server, ds.source)
	registerValidateBSL(server, ds.source, idx)
	registerResources(server, ds.source)
	registerStandardsResources(server)
	registerWorkflowPrompts(server)
	registerSetDump(server, ds, opts.projectsRoot)
	registerListProjects(server, opts.projectsRoot)
	registerDumpDiff(server, ds)
	finishSurface(server, surface, indexDeps, indexAvailable, profile)
	return server, surfaceCloser{server: server, next: indexDeps.closer()}
}

// surfaceCloser forgets the server's tool surface, then closes the rest.
type surfaceCloser struct {
	server *mcp.Server
	next   io.Closer
}

func (c surfaceCloser) Close() error {
	surfaces.Delete(c.server)
	return c.next.Close()
}

// coreExcludedTools are left out under --tools=core: rarely needed on a daily
// task and cheap to switch back on with --tools=full. exchange_audit,
// new_object_checklist and object_graph stay: the not-in-exchange and
// new-object prompts and the find_register_writes hint route to them.
var coreExcludedTools = []string{
	"bsp_extension_points", "command_visibility", "dump_diff",
	"extension_context", "get_configuration_info", "list_projects",
}

// finishSurface is the last step of both branches of newServerWithCloser: it
// adds the indexed tools when the registry is available, applies the profile
// and installs the middleware that builds the instructions from whatever ended
// up registered and records that set for the next-step hints (surface.go).
func finishSurface(server *mcp.Server, surface *toolSurface, deps indexToolDeps, indexAvailable bool, profile toolsProfile) {
	if indexAvailable {
		registerAllIndexTools(server, deps)
	}
	if profile == profileCore {
		server.RemoveTools(coreExcludedTools...)
	}
	server.AddReceivingMiddleware(surface.instructionsMiddleware)
}

// syntaxCorpus is what the tools need from the platform syntax reference: the
// lookups, plus the parse error a lazily built corpus reports only when asked.
// An interface and not *syntax.Index, because a corpus that failed to parse is
// otherwise unreachable from this package — NewLazy hands out no broken one on
// demand — and a guard nothing can reach is a guard nothing protects.
type syntaxCorpus interface {
	Err() error
	Search(query string, limit int) []onec.SyntaxEntry
	Lookup(query, owner string, limit int) syntax.Lookup
	GlobalMethod(name string) (onec.SyntaxEntry, bool)
}

// syntaxCorpusErr reports why the platform syntax corpus cannot answer, if it
// cannot. It is asked inside the handler and not at wiring time on purpose: the
// corpus is parsed lazily, and asking while registering the tools would parse it
// in every session, which is the cost the laziness exists to avoid. A parse
// failure has to surface here, because Search and GlobalMethod return "nothing
// found" for a broken corpus just as they do for an unknown name.
func syntaxCorpusErr(idx syntaxCorpus) error {
	if err := idx.Err(); err != nil {
		return fmt.Errorf("syntax index is not available: %w", err)
	}
	return nil
}

// serverInfoState is what server_info reports beyond the data source: the
// active project and the tool profile. nil is valid and reports neither.
type serverInfoState struct {
	project func() *projectInfoOutput
	profile toolsProfile
}

// registerCoreTools wires server_info, bsl_syntax and the ConfigSource tools.
// provide returns the active source for each call (nil when none is configured).
func registerCoreTools(server *mcp.Server, describe func() (string, string), info *serverInfoState, provide func() source.ConfigSource, idx syntaxCorpus) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_info",
		Description: "Server version, active data source (offline dump, live base or none), active project (which indexed project the indexed tools read and whether it matches the dump) and tool profile. Call it when a tool reports no source or no active project.",
	}, serverInfoHandler(describe, info))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bsl_syntax",
		Description: "Look up 1C BSL / platform syntax by Russian or English name (function, method, property, type, constructor). Returns signature, parameters, return value and description. Consult it WHILE writing BSL, before calling a platform method you are recalling from memory. Pass queries=[...] to look up everything a block of code needs in one call. owner=<type> (e.g. ТаблицаЗначений) restricts matches to that type, and alone lists all its members compactly; query=Type.Member is the same as owner+query. A query that names a type also reports its constructors.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in bslSyntaxInput) (*mcp.CallToolResult, bslSyntaxOutput, error) {
		if err := syntaxCorpusErr(idx); err != nil {
			return nil, bslSyntaxOutput{}, err
		}
		// A batch is what makes the tool worth reaching for while writing: one
		// call for the whole block beats one call per name, which is the round
		// the model skips in favour of its own recall.
		if len(in.Queries) > 0 {
			out := bslSyntaxOutput{}
			for _, q := range in.Queries {
				r := idx.Lookup(q, in.Owner, in.Limit)
				out.Results = append(out.Results, bslSyntaxResult{Query: q, Count: len(r.Matches), Matches: r.Matches, Owner: r.Owner, Type: r.Type, Note: r.Note})
				out.Count += len(r.Matches)
			}
			return nil, out, nil
		}
		if strings.TrimSpace(in.Query) == "" && strings.TrimSpace(in.Owner) == "" {
			return nil, bslSyntaxOutput{}, errors.New("pass query (one name), queries (several names) or owner (a type's members)")
		}
		r := idx.Lookup(in.Query, in.Owner, in.Limit)
		out := bslSyntaxOutput{Query: in.Query, Count: len(r.Matches), Matches: r.Matches,
			Owner: r.Owner, Type: r.Type, Note: r.Note,
			Members: r.Members, Total: r.Total, Truncated: r.Truncated}
		if r.Members != nil {
			out.Count = len(r.Members)
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_configuration_info",
		Description: "Returns the configuration's name, synonym, vendor, version and per-type object counts. Use it to get your bearings in an unfamiliar configuration, or to confirm the right export is active.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, source.ConfigurationInfo, error) {
		src := provide()
		if src == nil {
			return nil, source.ConfigurationInfo{}, errNoSource
		}
		info, err := src.ConfigurationInfo(ctx)
		if err != nil {
			return nil, source.ConfigurationInfo{}, err
		}
		return nil, *info, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_metadata_tree",
		Description: "Configuration objects grouped by type (Catalog, Document, CommonModule, ...). Use it to find the exact name of an object you know approximately; narrow it with type= and like=, a full tree runs to hundreds of thousands of characters. For a yes/no question call object_exists.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in metadataTreeInput) (*mcp.CallToolResult, source.MetadataTree, error) {
		src := provide()
		if src == nil {
			return nil, source.MetadataTree{}, errNoSource
		}
		tree, err := src.MetadataTree(ctx)
		if err != nil {
			return nil, source.MetadataTree{}, err
		}
		return nil, filterTree(*tree, in.Type, in.Like), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "object_exists",
		Description: "Whether an object of this type and name is in the active configuration, with near matches when it is not. Use it for the yes/no question instead of the metadata tree.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in objectExistsInput) (*mcp.CallToolResult, objectExistsOutput, error) {
		src := provide()
		if src == nil {
			return nil, objectExistsOutput{}, errNoSource
		}
		tree, err := src.MetadataTree(ctx)
		if err != nil {
			return nil, objectExistsOutput{}, err
		}
		return nil, lookupObject(*tree, in.Type, in.Name), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_object_structure",
		Description: "Attributes (with types), tabular sections, forms and commands of one object (type=Catalog name=Контрагенты), from the export or the live base. parts=forms (or attributes, tabular, commands, other) returns one section. Use it when you need an object's fields; when the index is available, get_object adds subscriptions, jobs and rights, and context_pack adds modules and usages.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in objectStructureInput) (*mcp.CallToolResult, objectStructureOutput, error) {
		src := provide()
		if src == nil {
			return nil, objectStructureOutput{}, errNoSource
		}
		obj, err := src.ObjectStructure(ctx, in.Type, in.Name)
		if err != nil {
			return nil, objectStructureOutput{}, err
		}
		steps := nextStepsFor(*obj, surfaceOf(server).has)
		applyParts(obj, in.Parts)
		return nil, objectStructureOutput{ObjectStructure: *obj, NextSteps: steps}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_form_structure",
		Description: "Returns a managed form's attributes, UI items (flattened), commands and event handlers (type=Catalog name=Контрагенты form=ФормаСписка, or type=CommonForm name=<form>). Use it before changing a form or writing its module code.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in formStructureInput) (*mcp.CallToolResult, source.FormStructure, error) {
		src := provide()
		if src == nil {
			return nil, source.FormStructure{}, errNoSource
		}
		form, err := src.FormStructure(ctx, in.Type, in.Name, in.Form)
		if err != nil {
			return nil, source.FormStructure{}, err
		}
		return nil, *form, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_code",
		Description: "Searches BSL module text (substring or regex) and names the enclosing procedure of each hit. Use it to find where something is implemented; narrow with scope=<path fragment> in a standard configuration, total=true gives the exact count. For metadata usage call find_metadata_usages. Regex is RE2; for Cyrillic letters use \\p{L} (\\w is ASCII-only).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in source.SearchParams) (*mcp.CallToolResult, source.SearchResult, error) {
		src := provide()
		if src == nil {
			return nil, source.SearchResult{}, errNoSource
		}
		res, err := src.SearchCode(ctx, in)
		if err != nil {
			return nil, source.SearchResult{}, err
		}
		return nil, *res, nil
	})
}

// registerSetDump wires the tool that switches the offline dump directory.
// projectsRoot is only used to offer a choice when the path is missing and the
// client can ask the user for it.
func registerSetDump(server *mcp.Server, ds *dumpState, projectsRoot string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_dump",
		Description: "Switches the offline source to a 1C XML export (folder with Configuration.xml) and activates the indexed project that describes it (indexProject); otherwise indexHint says how to register one. Returns the export date and warns when it is old. Call it when starting work on a project; without path, a client with elicitation asks the user.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in setDumpInput) (*mcp.CallToolResult, setDumpOutput, error) {
		if in.Path == "" && req != nil {
			// Ask instead of failing: the user knows the path, the model does not.
			picked, err := elicitDumpDir(ctx, req.Session, projectsRoot)
			if err != nil {
				return nil, setDumpOutput{Message: err.Error()}, nil
			}
			in.Path = picked
		}
		dir, err := resolveDumpDir(in.Path)
		if err != nil {
			msg := err.Error()
			if errors.Is(err, errDumpPathRequired) && surfaceOf(server).has("list_projects") {
				msg += "; найденные выгрузки перечисляет list_projects"
			}
			return nil, setDumpOutput{Path: in.Path, Message: msg}, nil
		}
		info, err := source.NewXMLSource(dir).ConfigurationInfo(ctx)
		if err != nil {
			return nil, setDumpOutput{Path: dir, Message: "не удалось разобрать Configuration.xml: " + err.Error()}, nil
		}
		st := ds.set(dir)
		out := setDumpOutput{OK: true, Path: dir, Configuration: info.Name, IsExtension: info.IsExtension,
			IndexProject: string(st.Project)}
		if st.Project == "" {
			out.IndexHint = st.Hint
		}
		if when, days, ok := exportAge(dir); ok {
			out.ExportedAt, out.AgeDays = when, days
			if days >= staleExportDays {
				out.Warning = fmt.Sprintf(
					"выгрузке %d дн. (от %s): рабочая база могла уйти вперёд. Вывод «в коде этого нет» по такой выгрузке — предположение; сверьтесь через check_sync или перевыгрузите",
					days, when)
			}
		}
		return nil, out, nil
	})
}

// errDumpPathRequired: no path and no way to ask for one. The caller adds the
// list_projects pointer only when that tool is registered.
var errDumpPathRequired = errors.New("path is required: клиент не поддерживает elicitation, поэтому спросить путь не получилось; передайте path явно: каталог, в котором лежит Configuration.xml")

// resolveDumpDir validates that path is a 1C XML export directory.
func resolveDumpDir(path string) (string, error) {
	if path == "" {
		return "", errDumpPathRequired
	}
	cfg := filepath.Join(path, "Configuration.xml")
	if fi, err := os.Stat(cfg); err == nil && !fi.IsDir() {
		return path, nil
	}
	return "", fmt.Errorf("Configuration.xml не найден в %q — укажите папку XML-выгрузки", path)
}

// Live inputs and outputs carry the base explicitly. Every live answer names the
// database it came from, because a plausible answer from the wrong base is the
// costliest failure this server can produce: it reads as a finding, not an error.
type queryInput struct {
	source.QueryParams
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type queryOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.QueryResult
}

type validateQueryLiveInput struct {
	Text string `json:"text" jsonschema:"1C query text to compile-check"`
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type validateQueryOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.ValidateResult
}

type eventLogInput struct {
	source.EventLogParams
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type eventLogOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.EventLogResult
}

type subsystemLiveInput struct {
	Name string `json:"name" jsonschema:"subsystem name (nested subsystems are found recursively)"`
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type subsystemOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.SubsystemInfo
}

type predefinedLiveInput struct {
	Type string `json:"type" jsonschema:"Catalog, ChartOfAccounts, ChartOfCharacteristicTypes or ChartOfCalculationTypes"`
	Name string `json:"name" jsonschema:"object name"`
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type predefinedOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.PredefinedList
}

type dataHealthInput struct {
	Objects []string `json:"objects" jsonschema:"query table names to measure and compare, e.g. [\"РегистрСведений.СотрудникНаСмене\", \"РегистрСведений.СтатусыСотрудников\"]"`
	Base    string   `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type dataHealthOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.DataHealthReport
}

type accessProfilesInput struct {
	Profile      string   `json:"profile,omitempty" jsonschema:"show one profile in detail: roles, access kinds, groups and members"`
	User         string   `json:"user,omitempty" jsonschema:"name in the Пользователи catalog: which profiles and roles this user actually gets"`
	Diff         []string `json:"diff,omitempty" jsonschema:"exactly two profile names to compare by roles"`
	IncludeRoles bool     `json:"includeRoles,omitempty" jsonschema:"return the full role list instead of the first 40"`
	Base         string   `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type accessProfilesOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.AccessProfileReport
}

type accessDiagnoseInput struct {
	User   string `json:"user" jsonschema:"name in the Пользователи catalog, e.g. Тест РукСТО"`
	Object string `json:"object,omitempty" jsonschema:"metadata object the access is about, e.g. Документ.ЗаказНаряд"`
	Base   string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type accessDiagnoseOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.AccessDiagnosis
}

type analyzeQueryLiveInput struct {
	Text string `json:"text" jsonschema:"1C query text to analyze for anti-patterns"`
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type analyzeQueryOutput struct {
	Base string `json:"base" jsonschema:"live base this answer came from"`
	source.QueryAnalysis
}

// registerLiveTools wires tools that require a running base. lp resolves which
// base a call targets: the one named in the call, else the active selection.
func registerLiveTools(server *mcp.Server, lp liveProvider) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "execute_query",
		Description: "Runs a read-only 1C query (SELECT / ВЫБРАТЬ only) against the live base and returns rows, plus the base the rows came from. Use it to check real data; build the query text with get_query_schema first. Non-SELECT queries are rejected. Pass base=<name> to target one base for this call without switching the active one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, queryOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, queryOutput{}, err
		}
		res, err := live.ExecuteQuery(ctx, in.QueryParams)
		if err != nil {
			return nil, queryOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, queryOutput{Base: name, QueryResult: *res}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_query",
		Description: "Compiles a 1C query without executing it and reports whether it is valid, plus the base it was compiled against. Use it after writing a query in live mode; offline use query_advisor. Pass base=<name> to target one base for this call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in validateQueryLiveInput) (*mcp.CallToolResult, validateQueryOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, validateQueryOutput{}, err
		}
		res, err := live.ValidateQuery(ctx, in.Text)
		if err != nil {
			return nil, validateQueryOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, validateQueryOutput{Base: name, ValidateResult: *res}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_event_log",
		Description: "Reads the registration log of the live base, filtered by date range, level (Error/Warning/Information/Note) and user, and names the base it read. Use it to investigate an error that actually happened in the base. Pass base=<name> to target one base for this call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in eventLogInput) (*mcp.CallToolResult, eventLogOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, eventLogOutput{}, err
		}
		res, err := live.EventLog(ctx, in.EventLogParams)
		if err != nil {
			return nil, eventLogOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, eventLogOutput{Base: name, EventLogResult: *res}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_subsystem",
		Description: "Returns a subsystem's composition (member objects and child subsystems) from the live base. Use it to see what belongs to a functional area. Offline, the same question is answered by get_metadata_tree and the subsystem-info skill. Pass base=<name> to target one base for this call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in subsystemLiveInput) (*mcp.CallToolResult, subsystemOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, subsystemOutput{}, err
		}
		res, err := live.Subsystem(ctx, in.Name)
		if err != nil {
			return nil, subsystemOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, subsystemOutput{Base: name, SubsystemInfo: *res}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_predefined",
		Description: "Returns the predefined items of a catalog or chart (type=Catalog/ChartOfAccounts/ChartOfCharacteristicTypes/ChartOfCalculationTypes), plus the base they came from. Use it when code or a query references predefined data. Pass base=<name> to target one base for this call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in predefinedLiveInput) (*mcp.CallToolResult, predefinedOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, predefinedOutput{}, err
		}
		res, err := live.Predefined(ctx, in.Type, in.Name)
		if err != nil {
			return nil, predefinedOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, predefinedOutput{Base: name, PredefinedList: *res}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "data_health",
		Description: "Measures whether objects are actually maintained: row count, first and last write, records added in the last 30 and 90 days, and a verdict (живой / затухает / заброшен / пустой). Call it BEFORE building logic on a register or catalog you picked from the metadata tree, especially when several similar objects exist: metadata cannot tell a register written daily from one abandoned two years ago, and choosing the dead one produces logic that is wrong only in production. Pass several objects at once to compare them. Read-only, live mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dataHealthInput) (*mcp.CallToolResult, dataHealthOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, dataHealthOutput{}, err
		}
		rep, err := source.DataHealth(ctx, live, in.Objects, time.Now())
		if err != nil {
			return nil, dataHealthOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, dataHealthOutput{Base: name, DataHealthReport: *rep}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "access_profiles",
		Description: "Access group profiles as data, read from the base. Without arguments lists every profile with its role count; profile=<name> returns roles, access kinds, access groups and their members; user=<name> answers which profiles and roles a user actually gets, counting all three ways of joining a group (personal attribute, member row, user group); diff=[A,B] compares two profiles by roles. Use it when rights_audit is not enough: rights_audit reads roles and SUPPLIED profiles from the configuration, while profiles created by hand exist only in the database. The answer also reports whether record-level access restriction is switched on at all — hunting an RLS condition in a base where it is off costs hours. Read-only, live mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in accessProfilesInput) (*mcp.CallToolResult, accessProfilesOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, accessProfilesOutput{}, err
		}
		rep, err := source.AccessProfiles(ctx, live, source.AccessProfileOptions{
			Profile:      in.Profile,
			User:         in.User,
			Diff:         in.Diff,
			IncludeRoles: in.IncludeRoles,
		})
		if err != nil {
			return nil, accessProfilesOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, accessProfilesOutput{Base: name, AccessProfileReport: *rep}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "access_diagnose",
		Description: "Walks the chain behind \"user X cannot see Y\" and stops at the FIRST break: is the user in the catalog, is the account active, is it linked to an infobase user at all, which access groups and profiles it lands in, which roles those profiles carry, and whether record-level restriction is even switched on. Call it INSTEAD of hand-writing queries over ПрофилиГруппДоступа and ГруппыДоступа: the stop is the point, since hunting an RLS condition in a base where the restriction is off, or a role for a catalog item never linked to an infobase user, is where whole sessions go. The last link (does a role grant the right on the object) lives in the configuration, not the base, so it is handed back as a concrete rights_audit step instead of being guessed. Read-only, live mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in accessDiagnoseInput) (*mcp.CallToolResult, accessDiagnoseOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, accessDiagnoseOutput{}, err
		}
		rep, err := source.AccessDiagnose(ctx, live, in.User, in.Object)
		if err != nil {
			return nil, accessDiagnoseOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, accessDiagnoseOutput{Base: name, AccessDiagnosis: *rep}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "analyze_query",
		Description: "Reports heavy-query anti-patterns (join subquery, leading-wildcard LIKE, virtual table without params, ...) for a 1C query in live mode. Offline prefer query_advisor: it also checks indexes and suggests concrete rewrites. Pass base=<name> to target one base for this call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in analyzeQueryLiveInput) (*mcp.CallToolResult, analyzeQueryOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, analyzeQueryOutput{}, err
		}
		res, err := live.AnalyzeQuery(ctx, in.Text)
		if err != nil {
			return nil, analyzeQueryOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, analyzeQueryOutput{Base: name, QueryAnalysis: *res}, nil
	})
}

type serverInfoOutput struct {
	Name    string             `json:"name" jsonschema:"server name"`
	Version string             `json:"version" jsonschema:"server version"`
	Mode    string             `json:"mode" jsonschema:"active data source: offline, live or none"`
	Source  string             `json:"source" jsonschema:"data source location (dump dir or base URL, password redacted)"`
	Client  clientInfoOutput   `json:"client" jsonschema:"the connected client and the optional capabilities it declared"`
	Memory  memoryOutput       `json:"memory" jsonschema:"what this process holds: RSS, uptime and the export cache"`
	Project *projectInfoOutput `json:"project,omitempty" jsonschema:"the process's active project: the indexed project and the dump the raw tools read"`
	Profile string             `json:"profile,omitempty" jsonschema:"tool profile: full or core (--tools)"`
}

// projectInfoOutput is the one active-project state of the process as the
// caller sees it: before it existed the raw and the indexed tools could answer
// about two different projects with nothing saying so.
type projectInfoOutput struct {
	IndexProject string `json:"indexProject,omitempty" jsonschema:"indexed project the indexed tools read; empty when there is none"`
	ProjectRoot  string `json:"projectRoot,omitempty" jsonschema:"directory holding that project's 1c-project.json"`
	DumpDir      string `json:"dumpDir,omitempty" jsonschema:"export the raw tools read"`
	Bound        bool   `json:"bound" jsonschema:"true when the raw dump and the index belong to the same project"`
	Hint         string `json:"hint,omitempty" jsonschema:"what to do when bound is false"`
}

// offlineProjectInfo reports the active project of an offline server.
func offlineProjectInfo(projects *app.Projects) func() *projectInfoOutput {
	return func() *projectInfoOutput {
		st := projects.ActiveState()
		return &projectInfoOutput{
			IndexProject: string(st.Project), ProjectRoot: st.ProjectRoot,
			DumpDir: st.DumpDir, Bound: st.Bound, Hint: st.Hint,
		}
	}
}

// liveProjectInfo reports the indexed project of a live server. The raw tools
// read the base there, so there is no dump to bind and bound stays false.
func liveProjectInfo(projects *app.Projects) func() *projectInfoOutput {
	return func() *projectInfoOutput {
		st := projects.ActiveState()
		hint := "live-режим: raw-инструменты читают базу, а не выгрузку, привязка к индексу не проверяется"
		if st.Project == "" {
			hint += "; " + st.Hint
		}
		return &projectInfoOutput{IndexProject: string(st.Project), ProjectRoot: st.ProjectRoot, Hint: hint}
	}
}

// clientInfoOutput reports who is on the other end and what it can do. The
// elicitation flag is the answer to a question that cannot be looked up: whether
// asking the user mid-call works with THIS client.
type clientInfoOutput struct {
	Name        string        `json:"name,omitempty"`
	Version     string        `json:"version,omitempty"`
	Elicitation elicitSupport `json:"elicitation"`
}

func serverInfoHandler(describe func() (string, string), info *serverInfoState) func(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, serverInfoOutput, error) {
	return func(_ context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, serverInfoOutput, error) {
		mode, src := describe()
		rssBytes, rssKind, rssOK := processRSS()
		out := serverInfoOutput{
			Name: serverName, Version: version, Mode: mode, Source: src,
			Memory: buildMemory(rssBytes, rssKind, rssOK, time.Since(processStart), source.CacheSnapshot()),
		}
		if info != nil {
			out.Profile = string(info.profile.orDefault())
			if info.project != nil {
				out.Project = info.project()
			}
		}
		if req != nil && req.Session != nil {
			out.Client.Elicitation = clientElicitation(req.Session)
			if params := req.Session.InitializeParams(); params != nil && params.ClientInfo != nil {
				out.Client.Name = params.ClientInfo.Name
				out.Client.Version = params.ClientInfo.Version
			}
		}
		return nil, out, nil
	}
}
