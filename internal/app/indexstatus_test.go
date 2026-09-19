package app

import (
	"context"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/index"
)

// TestIndexStatusServiceNoActiveProject: index_status без активного проекта
// отдаёт no_active_project, а не пустоту.
func TestIndexStatusServiceNoActiveProject(t *testing.T) {
	p, err := NewProjects("", nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	svc := NewIndexStatusService(p)
	_, err = svc.Status(context.Background(), StatusInput{})
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNoActiveProject {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNoActiveProject)
	}
}

// TestIndexStatusServiceReindexNoActiveProject: то же для reindex.
func TestIndexStatusServiceReindexNoActiveProject(t *testing.T) {
	p, err := NewProjects("", nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	svc := NewIndexStatusService(p)
	_, err = svc.Reindex(context.Background(), ReindexInput{Mode: "full"})
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeNoActiveProject {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeNoActiveProject)
	}
}

// newFixtureService строит IndexStatusService над реальным проектом (fixture
// из projects_test.go): манифест на диске, реальный store, реальный
// index.Service — конец в конец, без стабов.
func newFixtureService(t *testing.T) (*IndexStatusService, *Projects) {
	t.Helper()
	workspaceRoot := t.TempDir()
	newFixtureProject(t, workspaceRoot, "ut-main")
	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return NewIndexStatusService(p), p
}

// TestIndexStatusServiceReindexFullThenStatus: критерий приёмки — «reindex
// full на фикстуре создаёт новую эпоху и переключает указатель». Прогоняем
// full reindex через сервис на реальном проекте, затем index_status
// показывает результирующее generation и счётчики этого reindex.
func TestIndexStatusServiceReindexFullThenStatus(t *testing.T) {
	svc, _ := newFixtureService(t)
	ctx := context.Background()

	reindexResp, err := svc.Reindex(ctx, ReindexInput{Mode: "full"})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if len(reindexResp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(reindexResp.Items))
	}
	item := reindexResp.Items[0]
	if item.Mode != "full" {
		t.Fatalf("Mode = %q, want full", item.Mode)
	}
	if item.Generation == "" {
		t.Fatal("Generation пуст после reindex")
	}
	if len(item.Components) != 1 || item.Components[0].Component != "cfg" {
		t.Fatalf("Components = %+v, want один компонент cfg", item.Components)
	}

	statusResp, err := svc.Status(ctx, StatusInput{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statusResp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(statusResp.Items))
	}
	st := statusResp.Items[0]
	if st.Generation != item.Generation {
		t.Fatalf("Status.Generation = %s, want %s (из Reindex)", st.Generation, item.Generation)
	}
	if st.LastReindexCounts == nil {
		t.Fatal("LastReindexCounts == nil после reindex этим сервисом")
	}
	if st.LastReindexCounts.FilesChanged != 1 {
		// Фикстура: components/cfg содержит один файл (Configuration.xml).
		t.Errorf("LastReindexCounts.FilesChanged = %d, want 1", st.LastReindexCounts.FilesChanged)
	}
	if st.Epoch < 1 {
		t.Errorf("Epoch = %d, want >= 1 после reindex full (новая эпоха)", st.Epoch)
	}
}

// TestIndexStatusServiceReindexUnknownComponent: component=<неизвестный
// id> отдаёт component_not_registered ДО обращения к пайплайну, с подсказкой,
// перечисляющей реальные компоненты манифеста.
func TestIndexStatusServiceReindexUnknownComponent(t *testing.T) {
	svc, _ := newFixtureService(t)
	_, err := svc.Reindex(context.Background(), ReindexInput{Mode: "full", Component: "нет-такого"})
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeComponentNotRegistered {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeComponentNotRegistered)
	}
	if appErr.Project != "ut-main" {
		t.Errorf("Project = %s, want ut-main", appErr.Project)
	}
}

// TestIndexStatusServiceReindexInvalidMode: mode за пределами incremental/full
// отклоняется до открытия транзакции store.
func TestIndexStatusServiceReindexInvalidMode(t *testing.T) {
	svc, _ := newFixtureService(t)
	_, err := svc.Reindex(context.Background(), ReindexInput{Mode: "yesterday"})
	if err == nil {
		t.Fatal("Reindex(mode=yesterday) должен вернуть ошибку")
	}
}

// TestIndexStatusServiceReindexProjectRootRegistersAndDefaultsToFull:
// критерий приёмки тикета — reindex с projectRoot на пустом registry
// регистрирует проект, делает его активным (проверено через
// Registry.ActiveProject()) и, раз mode не передан явно, собирает full, а не
// incremental индекса, которого только что не существовало.
func TestIndexStatusServiceReindexProjectRootRegistersAndDefaultsToFull(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectRoot := newUnregisteredProjectDir(t, "boot-proj")

	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	svc := NewIndexStatusService(p)

	resp, err := svc.Reindex(context.Background(), ReindexInput{ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	if resp.Items[0].Mode != "full" {
		t.Fatalf("Mode = %q, want full (проект только что зарегистрирован)", resp.Items[0].Mode)
	}

	entry, ok := p.registry.ActiveProject()
	if !ok {
		t.Fatal("Registry.ActiveProject() пуст после reindex с projectRoot")
	}
	if entry.ID != "boot-proj" {
		t.Fatalf("ActiveProject().ID = %s, want boot-proj", entry.ID)
	}
}

// TestIndexStatusServiceReindexWithoutProjectRootUnaffected: регрессия —
// reindex без projectRoot на уже активном проекте работает точно как до
// этого поля: mode по умолчанию incremental, реестр не тронут.
func TestIndexStatusServiceReindexWithoutProjectRootUnaffected(t *testing.T) {
	svc, p := newFixtureService(t)
	before := len(p.registry.RecentProjects())

	resp, err := svc.Reindex(context.Background(), ReindexInput{})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if resp.Items[0].Mode != "incremental" {
		t.Fatalf("Mode = %q, want incremental (поведение без projectRoot не должно меняться)", resp.Items[0].Mode)
	}
	if len(p.registry.RecentProjects()) != before {
		t.Fatalf("проектов в registry = %d, было %d — reindex без projectRoot не должен трогать реестр",
			len(p.registry.RecentProjects()), before)
	}
}

// TestIndexStatusServiceReindexProjectRootMissingManifest: projectRoot без
// 1c-project.json — честная ошибка (не паника), ничего не зарегистрировано.
func TestIndexStatusServiceReindexProjectRootMissingManifest(t *testing.T) {
	workspaceRoot := t.TempDir()
	emptyDir := t.TempDir()

	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	svc := NewIndexStatusService(p)

	_, err = svc.Reindex(context.Background(), ReindexInput{ProjectRoot: emptyDir})
	if err == nil {
		t.Fatal("Reindex(projectRoot=<каталог без манифеста>) должен вернуть ошибку")
	}
	if len(p.registry.RecentProjects()) != 0 {
		t.Fatalf("проектов в registry = %d, want 0 после отказа", len(p.registry.RecentProjects()))
	}
}

// TestIndexStatusServiceStatusBeforeAnyReindex: до первого reindex
// index_status на СВЕЖЕЗАРЕГИСТРИРОВАННОМ (но ни разу не собранном) проекте
// честно отвечает без LastReindexCounts и без паники — пустой store для
// index_status не особый случай.
func TestIndexStatusServiceStatusBeforeAnyReindex(t *testing.T) {
	svc, _ := newFixtureService(t)
	resp, err := svc.Status(context.Background(), StatusInput{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	if resp.Items[0].LastReindexCounts != nil {
		t.Errorf("LastReindexCounts = %+v, want nil до первого reindex", resp.Items[0].LastReindexCounts)
	}
}

// TestIndexStatusServiceReindexReportsStageTimings:
// reindex отдаёт разбивку
// по этапам конвейера, и сумма этапов сходится с общим DurationMS. "commit"
// определён как остаток общей длительности сверх измеренных этапов
// (internal/index/stagetiming.go), поэтому равенство точное, не приближённое.
func TestIndexStatusServiceReindexReportsStageTimings(t *testing.T) {
	svc, _ := newFixtureService(t)
	ctx := context.Background()

	reindexResp, err := svc.Reindex(ctx, ReindexInput{Mode: "full"})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	item := reindexResp.Items[0]

	if len(item.Stages) == 0 {
		t.Fatal("Stages пуст после reindex — разбивка по этапам не отдана")
	}

	seen := map[string]bool{}
	var sumMS int64
	for _, st := range item.Stages {
		if st.Name == "" {
			t.Errorf("stage с пустым Name: %+v", st)
		}
		if seen[st.Name] {
			t.Errorf("stage %q встречается дважды в одной разбивке", st.Name)
		}
		seen[st.Name] = true
		if st.DurationMS < 0 {
			t.Errorf("stage %q: DurationMS = %d, отрицательная длительность", st.Name, st.DurationMS)
		}
		sumMS += st.DurationMS
	}
	if !seen["commit"] {
		t.Error(`среди этапов нет "commit" — остатка длительности сверх измеренных этапов`)
	}
	if sumMS != item.DurationMS {
		t.Errorf("сумма этапов = %d мс, DurationMS = %d мс — не сходится", sumMS, item.DurationMS)
	}
}

// TestIndexStatusServiceReindexIncrementalStageTimingsReconcile: на
// неизменном дереве инкрементальный reindex отдаёт ту же структуру (набор
// имён этапов, сумма сходится с DurationMS). НЕ проверяет времена на близость
// к нулю: reindex (в отличие от EnsureFresh/precheck) сознательно не заходит
// в precheck и всегда прогоняет весь пайплайн по компоненту целиком —
// filesChanged у incremental и full совпадают (см. CLAUDE.md,
// «Принудительный reindex не проходит через precheck»). Найдено слепой
// приёмкой прогона reindex-timings: первая версия этого теста называлась
// NearZero и заявляла в doc-комментарии то, чего не проверяла и что для
// этого инструмента неверно — исправлено на честное имя и условие.
func TestIndexStatusServiceReindexIncrementalStageTimingsReconcile(t *testing.T) {
	svc, _ := newFixtureService(t)
	ctx := context.Background()

	if _, err := svc.Reindex(ctx, ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	reindexResp, err := svc.Reindex(ctx, ReindexInput{Mode: "incremental"})
	if err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	item := reindexResp.Items[0]

	if len(item.Stages) == 0 {
		t.Fatal("Stages пуст после инкрементального reindex")
	}
	var sumMS int64
	for _, st := range item.Stages {
		sumMS += st.DurationMS
	}
	if sumMS != item.DurationMS {
		t.Errorf("сумма этапов = %d мс, DurationMS = %d мс — не сходится (инкремент)", sumMS, item.DurationMS)
	}
}
