package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// extensionCommonModulePath: общий модуль расширения фикстуры.
var extensionCommonModulePath = workspace.DumpModulePath("CommonModule", "РасшА_Сервис", workspace.ModuleCommon)

const extensionCommonModuleText = `
Функция РасшА_Посчитать() Экспорт
	Возврат 1;
КонецФункции
`

// writeManifest переписывает 1c-project.json проекта: конфигурация cfg и
// перечисленные расширения, применяющиеся к ней.
func writeManifest(t *testing.T, projectRoot string, id domain.ProjectID, extensions ...string) {
	t.Helper()
	components := []map[string]any{{"id": "cfg", "kind": "configuration", "root": "cfg"}}
	for i, ext := range extensions {
		components = append(components, map[string]any{
			"id": ext, "kind": "extension", "root": ext, "appliesTo": "cfg", "applyOrder": i + 1,
		})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "project": string(id), "components": components})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))
}

// newReloadFixture: зарегистрированный проект с одной конфигурацией и каталогом
// расширения exta на диске, которого в манифесте пока нет.
func newReloadFixture(t *testing.T) (*Projects, string) {
	t.Helper()
	workspaceRoot := t.TempDir()
	newFixtureProject(t, workspaceRoot, "reload")
	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	op, err := p.Active(context.Background())
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	root := op.Entry.Root
	writeFile(t, filepath.Join(root, "exta", "Configuration.xml"), extConfigurationXML("РасшА", "Customization"))
	writeFile(t, filepath.Join(root, "exta", filepath.FromSlash(extensionCommonModulePath)), extensionCommonModuleText)
	return p, root
}

func reindexComponents(t *testing.T, p *Projects, in ReindexInput) ReindexResultItem {
	t.Helper()
	resp, err := NewIndexStatusService(p).Reindex(context.Background(), in)
	if err != nil {
		t.Fatalf("Reindex(%+v): %v", in, err)
	}
	return resp.Items[0]
}

func symbolCount(t *testing.T, p *Projects, name string) int {
	t.Helper()
	resp, err := NewSymbolService(p).FindSymbol(context.Background(), FindSymbolInput{Name: name})
	if err != nil {
		t.Fatalf("FindSymbol(%s): %v", name, err)
	}
	return len(resp.Items)
}

// TestReindexSeesComponentAddedToManifest: расширение дописали в манифест при
// работающем сервере. Раньше reindex шёл по манифесту, снятому при открытии
// проекта, и нового компонента не видел до перезапуска процесса.
func TestReindexSeesComponentAddedToManifest(t *testing.T) {
	p, root := newReloadFixture(t)

	first := reindexComponents(t, p, ReindexInput{Mode: "full"})
	if len(first.Components) != 1 {
		t.Fatalf("до правки манифеста компонентов %d, want 1", len(first.Components))
	}
	if got := symbolCount(t, p, "РасшА_Посчитать"); got != 0 {
		t.Fatalf("метод расширения найден до добавления компонента: %d", got)
	}

	writeManifest(t, root, "reload", "exta")

	second := reindexComponents(t, p, ReindexInput{})
	if len(second.Components) != 2 {
		t.Fatalf("после правки манифеста компонентов %d, want 2: %+v", len(second.Components), second.Components)
	}
	if got := symbolCount(t, p, "РасшА_Посчитать"); got != 1 {
		t.Fatalf("метод расширения: найдено %d, want 1", got)
	}
	if _, err := NewSymbolService(p).FindSymbol(context.Background(), FindSymbolInput{Name: "РасшА_Посчитать", Component: "exta"}); err != nil {
		t.Fatalf("отбор по новому компоненту: %v", err)
	}
}

// TestReindexDropsComponentRemovedFromManifest: компонент убрали из манифеста.
// Инкремент его факты не снимает, поэтому прогон идёт полной пересборкой, и
// ответ это называет.
func TestReindexDropsComponentRemovedFromManifest(t *testing.T) {
	p, root := newReloadFixture(t)
	writeManifest(t, root, "reload", "exta")
	reindexComponents(t, p, ReindexInput{Mode: "full"})
	if got := symbolCount(t, p, "РасшА_Посчитать"); got != 1 {
		t.Fatalf("метод расширения до удаления компонента: найдено %d, want 1", got)
	}

	writeManifest(t, root, "reload")

	resp, err := NewIndexStatusService(p).Reindex(context.Background(), ReindexInput{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	item := resp.Items[0]
	if item.Mode != "full" {
		t.Errorf("mode = %q, want full: факты убранного компонента снимает только полная пересборка", item.Mode)
	}
	if len(item.Components) != 1 {
		t.Errorf("компонентов %d, want 1", len(item.Components))
	}
	if !hasWarning(resp.Warnings, "manifest_reloaded") {
		t.Errorf("нет предупреждения manifest_reloaded: %+v", resp.Warnings)
	}
	if got := symbolCount(t, p, "РасшА_Посчитать"); got != 0 {
		t.Errorf("метод убранного расширения всё ещё в индексе: %d", got)
	}
}

// TestReindexUnchangedManifestKeepsOpenProject: без правки манифеста открытый
// проект остаётся тем же (корпус индексации не выбрасывается), предупреждения нет.
func TestReindexUnchangedManifestKeepsOpenProject(t *testing.T) {
	p, _ := newReloadFixture(t)
	ctx := context.Background()
	before, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	resp, err := NewIndexStatusService(p).Reindex(ctx, ReindexInput{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	after, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if before != after {
		t.Error("открытый проект пересоздан без изменения манифеста")
	}
	if hasWarning(resp.Warnings, "manifest_reloaded") {
		t.Errorf("лишнее предупреждение manifest_reloaded: %+v", resp.Warnings)
	}
}

// TestReindexBrokenManifestIsAnError: манифест сломали. Молча индексировать по
// прежнему составу нельзя: человек ждёт, что правка применилась.
func TestReindexBrokenManifestIsAnError(t *testing.T) {
	p, root := newReloadFixture(t)
	reindexComponents(t, p, ReindexInput{Mode: "full"})

	writeManifest(t, root, "reload", "нет-такого-каталога")

	if _, err := NewIndexStatusService(p).Reindex(context.Background(), ReindexInput{}); err == nil {
		t.Fatal("Reindex по сломанному манифесту прошёл без ошибки")
	}
}
