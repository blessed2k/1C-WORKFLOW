//go:build darwin || windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestProjectsSetDumpBindsOtherCase: на darwin (APFS по умолчанию) и windows
// регистр в пути не различается, выгрузка в другом написании привязывается.
func TestProjectsSetDumpBindsOtherCase(t *testing.T) {
	workspaceRoot := t.TempDir()
	rootA := twoLayerProjectDir(t, "proj-a")
	registerEntries(t, workspaceRoot, "", workspace.ProjectEntry{ID: "proj-a", Root: rootA})
	p := openProjects(t, workspaceRoot)

	dump := canonical(t, filepath.Join(rootA, "cfg"))
	other := strings.ToUpper(dump)
	if other == dump {
		t.Skip("путь не меняется при смене регистра")
	}
	a, errA := os.Stat(dump)
	b, errB := os.Stat(other)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("файловая система различает регистр")
	}
	st := p.SetDump(other)
	if st.Project != "proj-a" || !st.Bound {
		t.Fatalf("SetDump(%s) = %+v, want proj-a bound", other, st)
	}
}
