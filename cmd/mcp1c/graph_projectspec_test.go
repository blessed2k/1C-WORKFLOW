package main

import (
	"context"
	"path/filepath"
	"testing"
)

// TestSplitProjectSpec: --project <workspace>#<id> selects one project of a
// workspace registry; an existing path with '#' stays a path.
func TestSplitProjectSpec(t *testing.T) {
	dir := t.TempDir()
	hashDir := filepath.Join(dir, "a#b")
	metaWriteFile(t, filepath.Join(hashDir, "x"), "")
	for spec, want := range map[string][2]string{
		dir:          {dir, ""},
		dir + "#erp": {dir, "erp"},
		hashDir:      {hashDir, ""},
		dir + "#":    {dir + "#", ""},
		"#erp":       {"#erp", ""},
	} {
		root, id := splitProjectSpec(spec)
		if root != want[0] || id != want[1] {
			t.Errorf("splitProjectSpec(%q) = %q,%q; ожидалось %q,%q", spec, root, id, want[0], want[1])
		}
	}
	if got := dedupeRoots([]string{dir + "#a", dir + "#b", dir + "#a"}); len(got) != 2 {
		t.Errorf("два проекта одного workspace — два handle: %v", got)
	}
}

// TestOpenGraphProjectsOneWorkspaceTwoProjects: two projects registered in
// one workspace open as two handles with their own ids (V2: both bases on
// one map).
func TestOpenGraphProjectsOneWorkspaceTwoProjects(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	rootA, rootB := t.TempDir(), t.TempDir()
	ogManifest(t, rootA, "og-a")
	ogWriteProject(t, rootA, "A", "ЗаказA", []string{"РегA"})
	ogManifest(t, rootB, "og-b")
	ogWriteProject(t, rootB, "B", "ЗаказB", []string{"РегB"})
	cs := ogConnect(t, ctx, workspaceRoot)
	ogReindex(t, ctx, cs, rootA)
	ogReindex(t, ctx, cs, rootB)
	cs.Close()

	handles, code := openGraphProjects(ctx, []string{workspaceRoot + "#og-a", workspaceRoot + "#og-b"},
		newProjectsFactory(nil), 0)
	if code != 0 || len(handles) != 2 {
		t.Fatalf("code %d, handles %+v", code, handles)
	}
	if handles[0].ID != "og-a" || handles[1].ID != "og-b" {
		t.Errorf("ids %s, %s", handles[0].ID, handles[1].ID)
	}
	if _, code := openGraphProjects(ctx, []string{workspaceRoot + "#нет-такого"}, newProjectsFactory(nil), 0); code == 0 {
		t.Error("неизвестный id проекта обязан дать ненулевой код")
	}
}
