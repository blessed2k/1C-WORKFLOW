package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// C0 (docs/architecture-graph.md §4.1, решение D2): одно состояние процесса
// «активный проект» = каталог выгрузки для raw + индексный проект. Тесты идут
// через публичные методы Projects и IndexStatusService.

// twoLayerProjectDir создаёт каталог проекта с конфигурацией cfg и
// расширением ext, не регистрируя его.
func twoLayerProjectDir(t *testing.T, id domain.ProjectID) string {
	t.Helper()
	projectRoot := t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("База"))
	writeFile(t, filepath.Join(projectRoot, "ext", "Configuration.xml"), расширениеXML("Доработка"))
	manifest := map[string]any{
		"version": 1,
		"project": string(id),
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
			{"id": "ext", "kind": "extension", "root": "ext", "appliesTo": "cfg", "applyOrder": 1},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))
	return projectRoot
}

// registerEntries пишет записи в реестр workspaceRoot и, если active не пуст,
// делает его активным.
func registerEntries(t *testing.T, workspaceRoot string, active domain.ProjectID, entries ...workspace.ProjectEntry) {
	t.Helper()
	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	for _, e := range entries {
		if err := reg.Upsert(e); err != nil {
			t.Fatalf("Upsert %s: %v", e.ID, err)
		}
	}
	if active != "" {
		if err := reg.SetActiveProject(active); err != nil {
			t.Fatalf("SetActiveProject: %v", err)
		}
	}
}

func openProjects(t *testing.T, workspaceRoot string) *Projects {
	t.Helper()
	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return real
}

func activeID(t *testing.T, p *Projects) (domain.ProjectID, error) {
	t.Helper()
	op, err := p.Active(context.Background())
	if err != nil {
		return "", err
	}
	return op.Entry.ID, nil
}

// TestProjectsSetDumpBindsComponents: выгрузка, совпавшая с компонентом
// (конфигурация или расширение) зарегистрированного проекта, делает этот
// проект активным в процессе, даже если в реестре активен другой.
func TestProjectsSetDumpBindsComponents(t *testing.T) {
	cases := []struct {
		name string
		comp string
	}{
		{"конфигурация", "cfg"},
		{"расширение", "ext"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspaceRoot := t.TempDir()
			rootA := twoLayerProjectDir(t, "proj-a")
			rootB := twoLayerProjectDir(t, "proj-b")
			registerEntries(t, workspaceRoot, "proj-b",
				workspace.ProjectEntry{ID: "proj-a", Root: rootA},
				workspace.ProjectEntry{ID: "proj-b", Root: rootB})
			p := openProjects(t, workspaceRoot)

			dump := filepath.Join(rootA, tc.comp)
			st := p.SetDump(dump)
			if st.Project != "proj-a" || !st.Bound || st.Hint != "" {
				t.Fatalf("SetDump(%s) = %+v, want proj-a bound без подсказки", dump, st)
			}
			if st.ProjectRoot != canonical(t, rootA) {
				t.Errorf("ProjectRoot = %s, want %s", st.ProjectRoot, canonical(t, rootA))
			}
			if got := p.DumpDir(); got != dump {
				t.Errorf("DumpDir() = %s, want %s (путь хранится как задан)", got, dump)
			}
			id, err := activeID(t, p)
			if err != nil || id != "proj-a" {
				t.Fatalf("Active = %s, %v; want proj-a", id, err)
			}
		})
	}
}

// TestProjectsSetDumpUnboundGivesReindexHint: выгрузка без проекта снимает
// индексный активный проект процесса, а не оставляет сохранённый в реестре.
func TestProjectsSetDumpUnboundGivesReindexHint(t *testing.T) {
	workspaceRoot := t.TempDir()
	rootB := twoLayerProjectDir(t, "proj-b")
	registerEntries(t, workspaceRoot, "proj-b", workspace.ProjectEntry{ID: "proj-b", Root: rootB})
	p := openProjects(t, workspaceRoot)

	stray := t.TempDir()
	st := p.SetDump(stray)
	if st.Project != "" || st.Bound {
		t.Fatalf("SetDump(stray) = %+v, want без проекта", st)
	}
	for _, want := range []string{"reindex projectRoot=", stray, "1c-project.json"} {
		if !strings.Contains(st.Hint, want) {
			t.Errorf("Hint = %q, должен содержать %q", st.Hint, want)
		}
	}
	_, err := activeID(t, p)
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != CodeNoActiveProject {
		t.Fatalf("Active err = %v, want no_active_project", err)
	}
	if !strings.Contains(appErr.Hint, "reindex projectRoot=") || !strings.Contains(appErr.Hint, stray) {
		t.Errorf("Active hint = %q, должен называть reindex projectRoot и выгрузку", appErr.Hint)
	}
	if got := p.DumpDir(); got != stray {
		t.Errorf("DumpDir() = %s, want %s", got, stray)
	}
}

// TestProjectsSetDumpBindsTemporaryEntry: запись без манифеста (временная)
// привязывается по своему Root.
func TestProjectsSetDumpBindsTemporaryEntry(t *testing.T) {
	workspaceRoot := t.TempDir()
	dump := t.TempDir()
	writeFile(t, filepath.Join(dump, "Configuration.xml"), конфигурацияXML("Врем"))
	registerEntries(t, workspaceRoot, "", workspace.ProjectEntry{ID: "tmp-dump", Root: canonical(t, dump), Temporary: true})
	p := openProjects(t, workspaceRoot)

	st := p.SetDump(dump)
	if st.Project != "tmp-dump" || !st.Bound {
		t.Fatalf("SetDump(temp) = %+v, want tmp-dump bound", st)
	}
}

// TestProjectsSetDumpKeepsRegistryBytes: реестр общий для процессов,
// set_dump его не переписывает.
func TestProjectsSetDumpKeepsRegistryBytes(t *testing.T) {
	workspaceRoot := t.TempDir()
	rootA := twoLayerProjectDir(t, "proj-a")
	rootB := twoLayerProjectDir(t, "proj-b")
	registerEntries(t, workspaceRoot, "proj-b",
		workspace.ProjectEntry{ID: "proj-a", Root: rootA},
		workspace.ProjectEntry{ID: "proj-b", Root: rootB})
	regFile := filepath.Join(workspaceRoot, workspace.RegistryDirName, workspace.RegistryFileName)
	before, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	p := openProjects(t, workspaceRoot)
	p.SetDump(filepath.Join(rootA, "cfg"))
	if _, err := p.Active(context.Background()); err != nil {
		t.Fatalf("Active: %v", err)
	}
	p.SetDump(t.TempDir())

	after, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("registry.json изменился после set_dump:\nбыло  %s\nстало %s", before, after)
	}
}

// TestProjectsStartWithoutDumpActiveRule: правило старта без выгрузки.
func TestProjectsStartWithoutDumpActiveRule(t *testing.T) {
	t.Run("сохранённый активный", func(t *testing.T) {
		workspaceRoot := t.TempDir()
		rootA := twoLayerProjectDir(t, "proj-a")
		rootB := twoLayerProjectDir(t, "proj-b")
		registerEntries(t, workspaceRoot, "proj-b",
			workspace.ProjectEntry{ID: "proj-a", Root: rootA},
			workspace.ProjectEntry{ID: "proj-b", Root: rootB})
		p := openProjects(t, workspaceRoot)

		st := p.ActiveState()
		if st.Project != "proj-b" || !st.Bound {
			t.Fatalf("ActiveState = %+v, want proj-b bound", st)
		}
		if want := filepath.Join(canonical(t, rootB), "cfg"); p.DumpDir() != want {
			t.Errorf("DumpDir() = %s, want каталог конфигурации %s", p.DumpDir(), want)
		}
	})

	t.Run("единственный проект", func(t *testing.T) {
		workspaceRoot := t.TempDir()
		rootA := twoLayerProjectDir(t, "proj-a")
		registerEntries(t, workspaceRoot, "", workspace.ProjectEntry{ID: "proj-a", Root: rootA})
		p := openProjects(t, workspaceRoot)

		id, err := activeID(t, p)
		if err != nil || id != "proj-a" {
			t.Fatalf("Active = %s, %v; want proj-a", id, err)
		}
		if want := filepath.Join(canonical(t, rootA), "cfg"); p.DumpDir() != want {
			t.Errorf("DumpDir() = %s, want %s", p.DumpDir(), want)
		}
	})

	t.Run("несколько без активного", func(t *testing.T) {
		workspaceRoot := t.TempDir()
		registerEntries(t, workspaceRoot, "",
			workspace.ProjectEntry{ID: "proj-a", Root: twoLayerProjectDir(t, "proj-a")},
			workspace.ProjectEntry{ID: "proj-b", Root: twoLayerProjectDir(t, "proj-b")})
		p := openProjects(t, workspaceRoot)

		if _, err := activeID(t, p); err == nil {
			t.Fatal("Active без выбора среди двух проектов должен дать no_active_project")
		}
		st := p.ActiveState()
		if st.Project != "" || st.Bound || st.Hint == "" || p.DumpDir() != "" {
			t.Fatalf("ActiveState = %+v, DumpDir = %q; want пусто с подсказкой", st, p.DumpDir())
		}
	})

	t.Run("ни одного", func(t *testing.T) {
		p := openProjects(t, t.TempDir())
		st := p.ActiveState()
		if st.Project != "" || st.Bound || st.Hint == "" || p.DumpDir() != "" {
			t.Fatalf("ActiveState = %+v; want пусто с подсказкой", st)
		}
	})
}

// TestProjectsWithoutWorkspaceKeepsDump: без --projects-root выгрузка
// переключается как раньше, индексного проекта нет.
func TestProjectsWithoutWorkspaceKeepsDump(t *testing.T) {
	p := openProjects(t, "")
	st := p.SetDump("testdata/export")
	if p.DumpDir() != "testdata/export" || st.Project != "" || st.Bound {
		t.Fatalf("SetDump без workspace = %+v, DumpDir = %q", st, p.DumpDir())
	}
	if !strings.Contains(st.Hint, "--projects-root") {
		t.Errorf("Hint = %q, должен называть --projects-root", st.Hint)
	}
}

// TestReindexProjectRootSwitchesActiveDump: reindex projectRoot делает проект
// активным в процессе и переключает raw на его конфигурацию.
func TestReindexProjectRootSwitchesActiveDump(t *testing.T) {
	workspaceRoot := t.TempDir()
	p := openProjects(t, workspaceRoot)
	p.SetDump(t.TempDir())

	projectRoot := twoLayerProjectDir(t, "proj-new")
	svc := NewIndexStatusService(p)
	if _, err := svc.Reindex(context.Background(), ReindexInput{ProjectRoot: projectRoot}); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	st := p.ActiveState()
	if st.Project != "proj-new" || !st.Bound {
		t.Fatalf("ActiveState после reindex = %+v, want proj-new bound", st)
	}
	if want := filepath.Join(canonical(t, projectRoot), "cfg"); p.DumpDir() != want {
		t.Errorf("DumpDir() = %s, want %s", p.DumpDir(), want)
	}
}

// TestProjectsSetDumpDoesNotBindUnreadableManifest: обычная запись с
// нечитаемым манифестом по своему Root не привязывается (это право только
// временных записей), а подсказка называет ошибку манифеста.
func TestProjectsSetDumpDoesNotBindUnreadableManifest(t *testing.T) {
	workspaceRoot := t.TempDir()
	dump := t.TempDir()
	writeFile(t, filepath.Join(dump, "Configuration.xml"), конфигурацияXML("БезМанифеста"))
	registerEntries(t, workspaceRoot, "", workspace.ProjectEntry{ID: "no-manifest", Root: canonical(t, dump)})
	p := openProjects(t, workspaceRoot)

	st := p.SetDump(dump)
	if st.Project != "" || st.Bound {
		t.Fatalf("SetDump = %+v, want без проекта", st)
	}
	for _, want := range []string{"no-manifest", workspace.ManifestFileName, "reindex projectRoot="} {
		if !strings.Contains(st.Hint, want) {
			t.Errorf("Hint = %q, должен содержать %q", st.Hint, want)
		}
	}
}

// TestReindexProjectRootWithoutConfigurationUsesFirstComponent: у проекта нет
// компонента-конфигурации, raw переключается на корень первого компонента.
func TestReindexProjectRootWithoutConfigurationUsesFirstComponent(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "src", "Модуль.bsl"), bom+"Процедура Тест()\nКонецПроцедуры\n")
	data, err := json.Marshal(map[string]any{
		"version": 1,
		"project": "bsl-only",
		"components": []map[string]any{
			{"id": "src", "kind": "standalone-bsl", "root": "src"},
		},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))

	p := openProjects(t, workspaceRoot)
	if _, err := NewIndexStatusService(p).Reindex(context.Background(), ReindexInput{ProjectRoot: projectRoot}); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	st := p.ActiveState()
	want := filepath.Join(canonical(t, projectRoot), "src")
	if st.Project != "bsl-only" || !st.Bound || st.DumpDir != want {
		t.Fatalf("ActiveState = %+v, want bsl-only bound на %s", st, want)
	}
}

// TestActivateWithoutComponentsKeepsPreviousDump: манифест без компонентов
// с диска не читается (validate), поэтому ветка проверяется внутренним
// вызовом: прежняя выгрузка остаётся, bound=false, подсказка объясняет.
func TestActivateWithoutComponentsKeepsPreviousDump(t *testing.T) {
	p := openProjects(t, t.TempDir())
	prev := t.TempDir()
	p.SetDump(prev)

	root := t.TempDir()
	p.activateInProcess(workspace.ProjectEntry{ID: "empty", Root: root}, &workspace.Manifest{Root: root})
	st := p.ActiveState()
	if st.Project != "empty" || st.DumpDir != prev || st.Bound {
		t.Fatalf("ActiveState = %+v, want empty c прежней выгрузкой %s и bound=false", st, prev)
	}
	if !strings.Contains(st.Hint, "компонент") {
		t.Errorf("Hint = %q, должен объяснять отсутствие компонентов", st.Hint)
	}
}

// TestProjectsActiveStateDoesNotWaitForIndexOpen: open() держит mu на всё
// время store.Open (секунды на большом индексе). Привязка выгрузки и чтение
// состояния mu не берут, поэтому отвечают, пока mu занят.
func TestProjectsActiveStateDoesNotWaitForIndexOpen(t *testing.T) {
	workspaceRoot := t.TempDir()
	rootA := twoLayerProjectDir(t, "proj-a")
	registerEntries(t, workspaceRoot, "proj-a", workspace.ProjectEntry{ID: "proj-a", Root: rootA})
	p := openProjects(t, workspaceRoot)

	p.mu.Lock() // имитация open(), застрявшего в store.Open
	done := make(chan ActiveProjectState, 1)
	go func() {
		p.DumpDir()
		p.SetDump(filepath.Join(rootA, "ext"))
		done <- p.ActiveState()
	}()
	select {
	case st := <-done:
		p.mu.Unlock()
		if st.Project != "proj-a" || !st.Bound {
			t.Fatalf("ActiveState = %+v, want proj-a bound", st)
		}
	case <-time.After(2 * time.Second):
		p.mu.Unlock()
		t.Fatal("DumpDir/SetDump/ActiveState ждут mu, то есть открытия индекса")
	}
}
