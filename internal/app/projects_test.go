package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// bom — выгрузки 1С пишутся с BOM (см. internal/workspace/manifest_test.go).
const bom = "\ufeff"

// конфигурацияXML — минимальный Configuration.xml, достаточный для
// workspace.DetectKind: тот же фрагмент, что использует internal/workspace
// в своих тестах.
func конфигурацияXML(name string) string {
	return bom + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0001">
		<Properties>
			<Name>` + name + `</Name>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`
}

// writeFile создаёт файл вместе с недостающими каталогами.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// newFixtureProject создаёт на диске каталог проекта с 1c-project.json и
// одним компонентом конфигурации, регистрирует его в реестре workspaceRoot и
// делает активным. Возвращает id проекта.
func newFixtureProject(t *testing.T, workspaceRoot string, id domain.ProjectID) domain.ProjectID {
	t.Helper()
	projectRoot := t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))

	manifest := map[string]any{
		"version": 1,
		"project": string(id),
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: id, Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject(id); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	return id
}

// newUnregisteredProjectDir создаёт на диске каталог проекта с валидным
// 1c-project.json (project=id), но НЕ трогает никакой registry — ровно то
// состояние, в котором EnsureProjectActive должен уметь зарегистрировать
// проект с нуля (в отличие от newFixtureProject, которая регистрирует и
// активирует сама).
func newUnregisteredProjectDir(t *testing.T, id domain.ProjectID) string {
	t.Helper()
	projectRoot := t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))

	manifest := map[string]any{
		"version": 1,
		"project": string(id),
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))
	return projectRoot
}

// TestProjectsEnsureProjectActiveRegistersFreshProject: критический пробел,
// из-за которого писался этот тест, — до EnsureProjectActive ни один
// production-путь не вызывал Registry.Upsert/SetActiveProject (только
// тестовые хелперы), так что ни один реальный пользователь не мог включить
// хоть один индексный инструмент без ручной правки .mcp1c/registry.json.
func TestProjectsEnsureProjectActiveRegistersFreshProject(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectRoot := newUnregisteredProjectDir(t, "fresh-proj")

	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	registered, err := p.EnsureProjectActive(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("EnsureProjectActive: %v", err)
	}
	if !registered {
		t.Fatal("registered = false, want true — проект был свежим")
	}

	entry, ok := p.registry.ActiveProject()
	if !ok {
		t.Fatal("Registry.ActiveProject() пуст после EnsureProjectActive")
	}
	if entry.ID != "fresh-proj" {
		t.Fatalf("ActiveProject().ID = %s, want fresh-proj", entry.ID)
	}
	real, evalErr := filepath.EvalSymlinks(projectRoot)
	if evalErr != nil {
		t.Fatalf("EvalSymlinks(%s): %v", projectRoot, evalErr)
	}
	if entry.Root != real {
		t.Fatalf("ActiveProject().Root = %s, want %s", entry.Root, real)
	}
}

// TestProjectsEnsureProjectActiveIdempotentOnSameRoot: второй вызов с тем же
// корнем не заводит вторую запись (registered=false на втором вызове) — было
// бы легко случайно задублировать регистрацию при каждом reindex.
func TestProjectsEnsureProjectActiveIdempotentOnSameRoot(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectRoot := newUnregisteredProjectDir(t, "idem-proj")

	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	ctx := context.Background()

	if _, err := p.EnsureProjectActive(ctx, projectRoot); err != nil {
		t.Fatalf("первый EnsureProjectActive: %v", err)
	}
	registered, err := p.EnsureProjectActive(ctx, projectRoot)
	if err != nil {
		t.Fatalf("второй EnsureProjectActive: %v", err)
	}
	if registered {
		t.Fatal("registered = true на втором вызове, want false — проект уже был в registry")
	}
	if len(p.registry.RecentProjects()) != 1 {
		t.Fatalf("проектов в registry = %d, want 1 (не задублировался)", len(p.registry.RecentProjects()))
	}
}

// TestProjectsEnsureProjectActiveMissingManifest: каталог без 1c-project.json
// — честная ошибка (не паника), ничего не зарегистрировано.
func TestProjectsEnsureProjectActiveMissingManifest(t *testing.T) {
	workspaceRoot := t.TempDir()
	emptyDir := t.TempDir() // нет 1c-project.json

	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	if _, err := p.EnsureProjectActive(context.Background(), emptyDir); err == nil {
		t.Fatal("EnsureProjectActive на каталоге без манифеста должен вернуть ошибку")
	}
	if len(p.registry.RecentProjects()) != 0 {
		t.Fatalf("проектов в registry = %d, want 0 после отказа", len(p.registry.RecentProjects()))
	}
}

// TestProjectsEnsureProjectActiveNoWorkspaceRoot: сервер запущен без
// --projects-root — та же no_active_project ошибка, что и у Active.
func TestProjectsEnsureProjectActiveNoWorkspaceRoot(t *testing.T) {
	p, err := NewProjects("", nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects(\"\"): %v", err)
	}
	_, err = p.EnsureProjectActive(context.Background(), t.TempDir())
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNoActiveProject {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNoActiveProject)
	}
}

// TestProjectsActiveNoWorkspaceRoot: сервер запущен без --projects-root
// (workspaceRoot==""). NewProjects не должен трогать диск и не должен падать;
// Active обязан вернуть no_active_project с подсказкой именно про
// --projects-root, а не общей.
func TestProjectsActiveNoWorkspaceRoot(t *testing.T) {
	p, err := NewProjects("", nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects(\"\"): %v", err)
	}
	_, err = p.Active(context.Background())
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNoActiveProject {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNoActiveProject)
	}
	if !strings.Contains(appErr.Hint, "--projects-root") {
		t.Errorf("hint = %q, должен называть --projects-root", appErr.Hint)
	}
}

// TestProjectsActiveNoneRegistered: workspaceRoot задан, но ни один проект не
// зарегистрирован/не активирован — тоже no_active_project, с другой подсказкой
// (зарегистрировать и активировать, а не «задайте --projects-root»).
func TestProjectsActiveNoneRegistered(t *testing.T) {
	p, err := NewProjects(t.TempDir(), nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	_, err = p.Active(context.Background())
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNoActiveProject {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNoActiveProject)
	}
	if strings.Contains(appErr.Hint, "--projects-root") {
		t.Errorf("hint = %q не должен указывать на флаг, когда workspace задан", appErr.Hint)
	}
}

// TestProjectsActiveOpensRealProjectAndCaches: конец в конец — реальный
// манифест на диске, реальный store, реальный index.Service; второй Active
// отдаёт ТОТ ЖЕ *openProject (без повторного store.Open — второй Open на тот
// же каталог эпохи блокировался бы через writeSem/файл, поэтому кэш здесь не
// оптимизация, а необходимость).
func TestProjectsActiveOpensRealProjectAndCaches(t *testing.T) {
	workspaceRoot := t.TempDir()
	id := newFixtureProject(t, workspaceRoot, "ut-main")

	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })

	ctx := context.Background()
	op1, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if op1.Entry.ID != id {
		t.Fatalf("Entry.ID = %s, want %s", op1.Entry.ID, id)
	}
	if op1.Manifest.Project != id {
		t.Fatalf("Manifest.Project = %s, want %s", op1.Manifest.Project, id)
	}
	if op1.Store == nil || op1.Service == nil {
		t.Fatal("Store/Service не открыты")
	}
	if _, ok := op1.Manifest.Component("cfg"); !ok {
		t.Fatal("компонент cfg не разобран из манифеста")
	}

	op2, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active (второй раз): %v", err)
	}
	if op1 != op2 {
		t.Fatal("второй Active открыл проект заново вместо переиспользования кэша")
	}
}
