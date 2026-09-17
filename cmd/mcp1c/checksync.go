package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// maxSyncDiffNames caps how many object names check_sync returns per type
// difference, so a comparison against the wrong export (same name, unrelated
// history) cannot flood the response with thousands of entries.
const maxSyncDiffNames = 20

type checkSyncInput struct {
	DumpPath string `json:"dumpPath" jsonschema:"absolute path to the XML export directory (containing Configuration.xml) to compare against the live base"`
}

type checkSyncOutput struct {
	InSync           bool               `json:"inSync" jsonschema:"true if the version matches and every COMPARABLE metadata type's object set matches; see notComparedTypes for what was left out"`
	Configuration    string             `json:"configuration"`
	LiveVersion      string             `json:"liveVersion"`
	DumpVersion      string             `json:"dumpVersion"`
	VersionMatch     bool               `json:"versionMatch"`
	TypeDiffs        []metadataTypeDiff `json:"typeDiffs,omitempty" jsonschema:"only metadata types where the object count or object set differs"`
	NotComparedTypes []notComparedType  `json:"notComparedTypes,omitempty" jsonschema:"metadata types present on only one side and therefore NOT compared: the live /metadata endpoint enumerates only the main types, so auxiliary ones (pictures, styles, XDTO packages, web services, ...) are absent on the live side and would otherwise all look like they diverged"`
	Note             string             `json:"note,omitempty"`
}

// notComparedType is one metadata type that could not be compared because only
// one side reports it. It is not a divergence, just outside what the live
// endpoint exposes; reporting it keeps inSync honest instead of screaming about
// thousands of objects the live side simply never lists.
type notComparedType struct {
	Type  string `json:"type"`
	Side  string `json:"side" jsonschema:"which side has this type: live or dump"`
	Count int    `json:"count"`
}

// metadataTypeDiff is one metadata type where the live base and the dump
// disagree on the object count or the exact set of object names.
type metadataTypeDiff struct {
	Type       string   `json:"type"`
	LiveCount  int      `json:"liveCount"`
	DumpCount  int      `json:"dumpCount"`
	OnlyInLive []string `json:"onlyInLive,omitempty" jsonschema:"object names present in the live base but missing from the dump"`
	OnlyInDump []string `json:"onlyInDump,omitempty" jsonschema:"object names present in the dump but missing from the live base"`
	Truncated  bool     `json:"truncated,omitempty" jsonschema:"true if onlyInLive/onlyInDump were capped; the counts above are still exact"`
}

// registerCheckSync wires check_sync: whether an XML export still matches
// what is actually applied on the live base, before loading it in either
// direction. Loading a diverged export is the mistake instructions.go and
// CLAUDE.md both call out ("confirm the source of truth before dump/load"),
// and until now confirming it meant opening the export and the base by hand
// and comparing what you remember.
func registerCheckSync(server *mcp.Server, provideLive func() source.LiveSource) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_sync",
		Description: "Compares the live base against an XML export directory: configuration version and, per metadata type, which objects exist only on one side. Call it before loading an export into the base or dumping the base into files, whenever the two might have diverged (someone changed the base directly, or the export is old) - this is exactly the check CLAUDE.md asks you to make before choosing a source of truth.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in checkSyncInput) (*mcp.CallToolResult, checkSyncOutput, error) {
		live := provideLive()
		if live == nil {
			return nil, checkSyncOutput{}, errNoBase
		}
		dir, err := resolveDumpDir(in.DumpPath)
		if err != nil {
			return nil, checkSyncOutput{}, err
		}
		dump := source.NewXMLSource(dir)
		defer dump.Close()

		liveInfo, err := live.ConfigurationInfo(ctx)
		if err != nil {
			return nil, checkSyncOutput{}, fmt.Errorf("live configuration info: %w", err)
		}
		dumpInfo, err := dump.ConfigurationInfo(ctx)
		if err != nil {
			return nil, checkSyncOutput{}, fmt.Errorf("dump configuration info: %w", err)
		}
		if liveInfo.Name != dumpInfo.Name {
			return nil, checkSyncOutput{}, fmt.Errorf(
				"live base is configuration %q, dump at %q is %q - wrong export path?", liveInfo.Name, dir, dumpInfo.Name)
		}

		liveTree, err := live.MetadataTree(ctx)
		if err != nil {
			return nil, checkSyncOutput{}, fmt.Errorf("live metadata tree: %w", err)
		}
		dumpTree, err := dump.MetadataTree(ctx)
		if err != nil {
			return nil, checkSyncOutput{}, fmt.Errorf("dump metadata tree: %w", err)
		}

		diffs, notCompared := diffMetadataTrees(liveTree, dumpTree)
		out := checkSyncOutput{
			InSync:           liveInfo.Version == dumpInfo.Version && len(diffs) == 0,
			Configuration:    liveInfo.Name,
			LiveVersion:      liveInfo.Version,
			DumpVersion:      dumpInfo.Version,
			VersionMatch:     liveInfo.Version == dumpInfo.Version,
			TypeDiffs:        diffs,
			NotComparedTypes: notCompared,
		}
		if len(notCompared) > 0 {
			out.Note = fmt.Sprintf("Сравнивались только типы, которые отдаёт live-эндпоинт; %d типов присутствуют лишь на одной стороне и не сверялись (см. notComparedTypes). inSync относится только к сопоставимым типам.", len(notCompared))
		}
		return nil, out, nil
	})
}

// diffMetadataTrees compares two metadata trees by type. It returns the
// comparable types whose object set differs, and separately the types present
// on only one side. The split matters: the live /metadata endpoint enumerates
// only the main metadata types, so auxiliary ones (CommonPicture, StyleItem,
// XDTOPackage, WebService, ...) are simply absent on the live side. Treating
// those as "everything is only in the dump" turned an in-sync export into a
// wall of thousands of false differences. A type that one side does not list at
// all is not comparable — say so, do not call it a divergence.
func diffMetadataTrees(live, dump *source.MetadataTree) ([]metadataTypeDiff, []notComparedType) {
	liveByType := groupObjectsByType(live)
	dumpByType := groupObjectsByType(dump)

	types := map[string]bool{}
	for t := range liveByType {
		types[t] = true
	}
	for t := range dumpByType {
		types[t] = true
	}

	var diffs []metadataTypeDiff
	var notCompared []notComparedType
	for t := range types {
		liveSet, liveHas := liveByType[t]
		dumpSet, dumpHas := dumpByType[t]

		// One side does not enumerate this type at all: not comparable.
		if !liveHas || !dumpHas {
			nc := notComparedType{Type: t, Side: "dump", Count: len(dumpSet)}
			if liveHas {
				nc.Side, nc.Count = "live", len(liveSet)
			}
			notCompared = append(notCompared, nc)
			continue
		}

		onlyInLive := setDifference(liveSet, dumpSet)
		onlyInDump := setDifference(dumpSet, liveSet)
		if len(onlyInLive) == 0 && len(onlyInDump) == 0 {
			continue
		}
		d := metadataTypeDiff{
			Type:      t,
			LiveCount: len(liveSet),
			DumpCount: len(dumpSet),
		}
		d.OnlyInLive, d.Truncated = capNames(onlyInLive, maxSyncDiffNames)
		var truncatedDump bool
		d.OnlyInDump, truncatedDump = capNames(onlyInDump, maxSyncDiffNames)
		d.Truncated = d.Truncated || truncatedDump
		diffs = append(diffs, d)
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Type < diffs[j].Type })
	sort.Slice(notCompared, func(i, j int) bool { return notCompared[i].Type < notCompared[j].Type })
	return diffs, notCompared
}

// groupObjectsByType turns a MetadataTree into type -> set(object names).
func groupObjectsByType(tree *source.MetadataTree) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, g := range tree.Groups {
		set := make(map[string]bool, len(g.Objects))
		for _, name := range g.Objects {
			set[name] = true
		}
		out[g.Type] = set
	}
	return out
}

// setDifference returns the sorted names present in a but not in b.
func setDifference(a, b map[string]bool) []string {
	var out []string
	for name := range a {
		if !b[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// capNames truncates names to at most limit entries, reporting whether it did.
func capNames(names []string, limit int) ([]string, bool) {
	if len(names) <= limit {
		return names, false
	}
	return names[:limit], true
}
