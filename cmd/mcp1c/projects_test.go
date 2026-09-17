package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjects(t *testing.T) {
	root := t.TempDir()

	mkExport := func(rel string) {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Configuration.xml"), []byte("<x/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkExport("ProjA")                                // depth 1
	mkExport("Группа/ProjB")                         // depth 2 (nested export)
	os.MkdirAll(filepath.Join(root, "Empty"), 0o755) // no Configuration.xml

	got := findProjects(root, 3)
	if len(got) != 2 {
		t.Fatalf("found %d: %+v", len(got), got)
	}
	if got[0].Name != "ProjA" || got[1].Name != "ProjB" {
		t.Errorf("projects = %+v, want ProjA, ProjB (sorted)", got)
	}
}
