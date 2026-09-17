package app

import (
	"context"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// NewRealDumpImpactService строит ImpactService над РЕАЛЬНОЙ выгрузкой,
// проиндексированной прямо здесь (полный reindex) — только для сверки
// find_impact с internal/source.XMLSource.DependencyPaths (критерий приёмки
// тикета 13, «сверка на трёх входах»).
//
// Живёт в обычном (не _test.go) файле НАМЕРЕННО, а не как "export for test"
// — Go не даёт _test.go пакета internal/app экспортировать символы в тесты
// ДРУГОГО пакета (cmd/mcp1c): _test.go исключается из сборки, когда пакет
// компилируется как обычная зависимость. Сам вызов internal/source живёт в
// cmd/mcp1c/idx_impact_realdump_test.go, не здесь, — internal/app в списке
// пакетов, которым internal/arch.CheckLegacyIsolation запрещает опираться на
// internal/source, и запрет действует на весь пакет, включая _test.go
// (подробнее в doc-комментарии того теста). internal/app/impact_test.go
// объясняет то же самое короче. Эта функция про internal/source не знает —
// только собирает *Projects над реальным корнем в обход workspace-реестра
// (root — сама выгрузка, писать 1c-project.json в неё нельзя) и гоняет
// ПОЛНЫЙ reindex, тем же приёмом, что internal/index/realworld_test.go
// применяет к internal/index напрямую, только уровнем выше, потому что
// ImpactService требует *Projects, а не голый *index.Service.
//
// Принимает *testing.T и вызывает t.Fatalf/t.Cleanup — использовать вне
// тестов бессмысленно и не предполагается; экспортирована ради компиляции
// cmd/mcp1c/idx_impact_realdump_test.go, других вызывающих в дереве нет.
func NewRealDumpImpactService(t *testing.T, projectID domain.ProjectID, root string) *ImpactService {
	t.Helper()
	ctx := context.Background()
	workspaceRoot := t.TempDir()

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	entry := workspace.ProjectEntry{ID: projectID, Root: root}
	if err := reg.Upsert(entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject(projectID); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	manifest := workspace.Manifest{
		Version: 1, Project: projectID, Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
	builtins := syntaxtest.RealOrSkip(t)
	st, err := store.Open(workspaceRoot, store.Options{ProjectID: projectID, StateDirName: workspace.RegistryDirName})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	svcIdx := index.NewService(st, projectID, manifest, builtins, index.Config{})

	p := &Projects{workspaceRoot: workspaceRoot, registry: reg, builtins: builtins}
	// Кладём готовую пару в кэш ДО первого Active: p.open() при cache-hit не
	// зовёт workspace.LoadManifest(entry.Root) вовсе, поэтому 1c-project.json
	// в реальной выгрузке не нужен (interfaces.md: путь к выгрузке только
	// читается, ничего не пишется).
	p.opened = map[domain.ProjectID]*openProject{
		projectID: {Entry: entry, Manifest: manifest, Store: st, Service: svcIdx},
	}
	t.Cleanup(func() { p.Close() })

	if _, err := svcIdx.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex full: %v", err)
	}
	return NewImpactService(p)
}
