package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

type dumpDiffInput struct {
	DumpA      string `json:"dumpA,omitempty" jsonschema:"first export; default the active one"`
	DumpB      string `json:"dumpB" jsonschema:"second export"`
	WithFormat bool   `json:"withFormat,omitempty" jsonschema:"compare format versions too; reads every XML file"`
}

// registerDumpDiff wires dump_diff, the offline export-to-export comparison.
func registerDumpDiff(server *mcp.Server, ds *dumpState) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "dump_diff",
		Description: "Compares the object composition of two XML exports: only in A, only in B, shared count. Call it before merging or replacing an export, to see which objects would be lost. withFormat=true also compares format versions and finds files left at an older one. dumpA defaults to the active export. Offline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dumpDiffInput) (*mcp.CallToolResult, source.DumpDiff, error) {
		a := in.DumpA
		if a == "" {
			a = ds.get()
		}
		if a == "" {
			return nil, source.DumpDiff{}, errNoSource
		}
		rep, err := source.CompareDumps(ctx, a, in.DumpB, in.WithFormat)
		if err != nil {
			return nil, source.DumpDiff{}, err
		}
		return nil, *rep, nil
	})
}
