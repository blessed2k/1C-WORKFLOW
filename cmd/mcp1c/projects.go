package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listProjectsInput struct {
	Root string `json:"root,omitempty" jsonschema:"root to scan; default --projects-root"`
}

type projectInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type listProjectsOutput struct {
	Root     string        `json:"root"`
	Count    int           `json:"count"`
	Projects []projectInfo `json:"projects"`
}

// registerListProjects wires list_projects, which finds 1C export folders to feed
// to set_dump.
func registerListProjects(server *mcp.Server, defaultRoot string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_projects",
		Description: "Lists 1C export folders (with Configuration.xml) under a root. Call it when the export path is unknown, then switch to it with set_dump.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listProjectsInput) (*mcp.CallToolResult, listProjectsOutput, error) {
		root := in.Root
		if root == "" {
			root = defaultRoot
		}
		if root == "" {
			return nil, listProjectsOutput{}, errors.New("no root: pass root, or start the server with --projects-root")
		}
		projects := findProjects(root, 3)
		return nil, listProjectsOutput{Root: root, Count: len(projects), Projects: projects}, nil
	})
}

// findProjects walks root up to maxDepth and returns directories that contain a
// Configuration.xml (a 1C XML export root), not descending into found exports.
func findProjects(root string, maxDepth int) []projectInfo {
	var out []projectInfo
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if rel, e := filepath.Rel(root, path); e == nil && rel != "." {
			if strings.Count(rel, string(os.PathSeparator))+1 > maxDepth {
				return filepath.SkipDir
			}
		}
		if fi, e := os.Stat(filepath.Join(path, "Configuration.xml")); e == nil && !fi.IsDir() {
			out = append(out, projectInfo{Name: d.Name(), Path: path})
			return filepath.SkipDir
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
