package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestFindReferencesGroupsByModuleWithResolutionAndConfidence: ссылка
// на Помощь() из ManagerModule приходит resolved, с confidence и
// группировкой по модулю источника (Catalogs/Товары/Ext/ManagerModule.bsl).
func TestFindReferencesGroupsByModuleWithResolutionAndConfidence(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "graph-refs")
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	ctx := context.Background()

	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol setup: %+v %v", found.Items, err)
	}
	uid := found.Items[0].UID

	resp, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: uid})
	if err != nil {
		t.Fatalf("FindReferences: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("Items (группы по модулю) = %d, want 1", len(resp.Items))
	}
	grp := resp.Items[0]
	if grp.Module != "Catalogs/Товары/Ext/ManagerModule.bsl" {
		t.Fatalf("Module = %q, want Catalogs/Товары/Ext/ManagerModule.bsl", grp.Module)
	}
	if len(grp.References) != 1 {
		t.Fatalf("References = %d, want 1", len(grp.References))
	}
	ref := grp.References[0]
	if ref.Resolution != "resolved" {
		t.Fatalf("Resolution = %q, want resolved", ref.Resolution)
	}
	if ref.Confidence != float64(domain.ConfidenceExact) {
		t.Fatalf("Confidence = %v, want %v (parser-bsl, точный вызов)", ref.Confidence, domain.ConfidenceExact)
	}
	if ref.TargetUID != uid {
		t.Fatalf("TargetUID = %q, want %q", ref.TargetUID, uid)
	}

	// Символ без входящих ссылок — пустая, но не ошибочная выдача.
	skrytaya, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "СкрытаяВспомогательная"})
	if err != nil || len(skrytaya.Items) != 1 {
		t.Fatalf("FindSymbol(СкрытаяВспомогательная): %+v %v", skrytaya.Items, err)
	}
	noRefs, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: skrytaya.Items[0].UID})
	if err != nil {
		t.Fatalf("FindReferences(без вызовов): %v", err)
	}
	if len(noRefs.Items) != 0 {
		t.Fatalf("Items = %+v, want пусто для символа без ссылок", noRefs.Items)
	}
}

// TestTraceCallGraphCalleesFindsPlatformNotDeadEnd — trace_call_graph
// направления callees от Тест() находит ребро на Помощь() (общий модуль,
// kind=common-module) И платформенный вызов ТекущаяДата() как отдельный
// узел kind=platform, а не как unresolved-тупик (§19.1).
func TestTraceCallGraphCalleesFindsPlatformNotDeadEnd(t *testing.T) {
	p, op := newFixtureProjectWithPlatformCall(t, "graph-callees")
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	ctx := context.Background()
	_ = op

	tst, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Тест", Component: "cfg"})
	if err != nil || len(tst.Items) != 1 {
		t.Fatalf("FindSymbol(Тест): %+v %v", tst.Items, err)
	}

	resp, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: tst.Items[0].UID, Direction: "callees", Depth: 1})
	if err != nil {
		t.Fatalf("TraceCallGraph: %v", err)
	}
	var sawHelp, sawPlatform bool
	for _, n := range resp.Items {
		if n.Kind == "symbol" && n.Name == "Помощь" {
			sawHelp = true
		}
		if n.Kind == "platform" {
			sawPlatform = true
			if len(n.Path) == 0 || n.Path[len(n.Path)-1].Kind != "platform" {
				t.Errorf("узел platform: Path последний шаг Kind = %+v, want kind=platform", n.Path)
			}
		}
	}
	if !sawHelp {
		t.Errorf("Items = %+v, want узел symbol Помощь среди callees", resp.Items)
	}
	if !sawPlatform {
		t.Errorf("Items = %+v, want узел kind=platform (ТекущаяДата) — платформенный вызов не тупик", resp.Items)
	}
}

// TestTraceCallGraphCallersDirectionIsReverse — direction=callers от Помощь()
// находит Тест() как узел (обратное направление относительно callees).
func TestTraceCallGraphCallersDirectionIsReverse(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "graph-callers")
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	ctx := context.Background()

	help, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil || len(help.Items) != 1 {
		t.Fatalf("FindSymbol(Помощь): %+v %v", help.Items, err)
	}

	resp, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: help.Items[0].UID, Direction: "callers", Depth: 1})
	if err != nil {
		t.Fatalf("TraceCallGraph(callers): %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "Тест" {
		t.Fatalf("Items = %+v, want единственный узел Тест (вызывающий Помощь)", resp.Items)
	}
	step := resp.Items[0].Path[len(resp.Items[0].Path)-1]
	if step.FromUID != resp.Items[0].UID || step.ToUID != help.Items[0].UID {
		t.Errorf("step = %+v, want From=Тест (вызывающий), To=Помощь (цель)", step)
	}
}

// TestTraceCallGraphCycleSafe — взаимно рекурсивные А<->Б не вешают обход:
// число узлов конечно и совпадает с ожидаемым (каждый встречается один раз
// на глубину, а не растёт экспоненциально/бесконечно).
func TestTraceCallGraphCycleSafe(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	files := map[string]string{
		"CommonModules/Цикл.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="33333333-3333-3333-3333-333333333333">
    <Properties>
      <Name>Цикл</Name>
      <Global>true</Global>
      <ClientManagedApplication>true</ClientManagedApplication>
      <Server>true</Server>
      <ServerCall>true</ServerCall>
    </Properties>
  </CommonModule>
</MetaDataObject>`,
		"CommonModules/Цикл/Ext/Module.bsl": `
Процедура А() Экспорт
	Цикл.Б();
КонецПроцедуры

Процедура Б() Экспорт
	Цикл.А();
КонецПроцедуры
`,
	}
	for rel, content := range files {
		writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(rel)), content)
	}
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName),
		`{"version":1,"project":"graph-cycle","components":[{"id":"cfg","kind":"configuration","root":"cfg"}]}`)

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: "graph-cycle", Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject("graph-cycle"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	ctx := context.Background()
	op, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "А", Component: "cfg"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol(А): %+v %v", found.Items, err)
	}

	resp, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: found.Items[0].UID, Direction: "callees", Depth: 5})
	if err != nil {
		t.Fatalf("TraceCallGraph(depth=5, цикл А<->Б): %v", err)
	}
	// Цикл-safe обход А->Б->(А уже посещён, не повторяется) — ровно один узел Б.
	if len(resp.Items) != 1 || resp.Items[0].Name != "Б" {
		t.Fatalf("Items = %+v, want единственный узел Б (А уже посещён как корень)", resp.Items)
	}
}

// TestTraceCallGraphCursorPaginatesWithoutRepeats — регрессия на баг из
// независимого ревью: NextCursor кодировался буквальным limit
// вместо накопленного смещения, из-за чего вторая и все следующие страницы
// декодировали один и тот же offset=limit и повторяли узлы первой страницы,
// а хвост графа никогда не отдавался. Цепочка Ф1->Ф2->Ф3->Ф4->Ф5 (4 ребра,
// 4 узла callees), Limit=1, обход курсором по всем страницам подряд:
// собранные узлы обязаны быть все разные и покрывать весь граф без повторов
// и без пропусков.
func TestTraceCallGraphCursorPaginatesWithoutRepeats(t *testing.T) {
	p, _ := newFixtureProjectWithCallChain(t, "graph-cursor-chain")
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	ctx := context.Background()

	root, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Ф1", Component: "cfg"})
	if err != nil || len(root.Items) != 1 {
		t.Fatalf("FindSymbol(Ф1): %+v %v", root.Items, err)
	}

	var (
		seen   []string
		cursor string
	)
	for page := 1; page <= 10; page++ {
		resp, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{
			UID: root.Items[0].UID, Direction: "callees", Depth: 4, Limit: 1, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("TraceCallGraph(page %d): %v", page, err)
		}
		if len(resp.Items) != 1 {
			t.Fatalf("страница %d: Items = %+v, want ровно 1 узел (Limit=1)", page, resp.Items)
		}
		seen = append(seen, resp.Items[0].Name)
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
		if page == 10 {
			t.Fatalf("курсор не завершился за 10 страниц: seen=%v (похоже на бесконечное повторение)", seen)
		}
	}

	want := []string{"Ф2", "Ф3", "Ф4", "Ф5"}
	if len(seen) != len(want) {
		t.Fatalf("seen = %v (len %d), want %v (len %d) — без повторов и без пропусков", seen, len(seen), want, len(want))
	}
	byName := map[string]int{}
	for _, name := range seen {
		byName[name]++
	}
	for _, name := range want {
		if byName[name] != 1 {
			t.Errorf("узел %q встречен %d раз(а) в seen=%v, want ровно 1", name, byName[name], seen)
		}
	}
}

// newFixtureProjectWithCallChain — компонент с общим модулем, где Ф1 зовёт
// Ф2, Ф2 зовёт Ф3, Ф3 зовёт Ф4, Ф4 зовёт Ф5 — линейная цепочка на 4 ребра
// без циклов и без ветвления, чтобы BFS-порядок узлов callees был предсказуем
// (ровно один узел на страницу при Limit=1).
func newFixtureProjectWithCallChain(t *testing.T, id domain.ProjectID) (*Projects, *openProject) {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	files := map[string]string{
		"CommonModules/Цепочка.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="22222222-2222-2222-2222-222222222222">
    <Properties>
      <Name>Цепочка</Name>
      <Global>true</Global>
      <ClientManagedApplication>true</ClientManagedApplication>
      <Server>true</Server>
      <ServerCall>true</ServerCall>
    </Properties>
  </CommonModule>
</MetaDataObject>`,
		"CommonModules/Цепочка/Ext/Module.bsl": `
Процедура Ф1() Экспорт
	Цепочка.Ф2();
КонецПроцедуры

Процедура Ф2() Экспорт
	Цепочка.Ф3();
КонецПроцедуры

Процедура Ф3() Экспорт
	Цепочка.Ф4();
КонецПроцедуры

Процедура Ф4() Экспорт
	Цепочка.Ф5();
КонецПроцедуры

Процедура Ф5() Экспорт
	// лист цепочки, без дальнейших вызовов
КонецПроцедуры
`,
	}
	for rel, content := range files {
		writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(rel)), content)
	}
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName),
		`{"version":1,"project":"`+string(id)+`","components":[{"id":"cfg","kind":"configuration","root":"cfg"}]}`)

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
	builtins := syntaxtest.Fixture(t)
	p, err := NewProjects(workspaceRoot, builtins, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	ctx := context.Background()
	op, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	return p, op
}

// newFixtureProjectWithPlatformCall — компонент с общим модулем, зовущим
// сам себя и платформенную функцию ТекущаяДата() — для проверки, что
// платформенный вызов виден как узел kind=platform.
func newFixtureProjectWithPlatformCall(t *testing.T, id domain.ProjectID) (*Projects, *openProject) {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	files := map[string]string{
		"CommonModules/УтилитыОбщие.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="11111111-1111-1111-1111-111111111111">
    <Properties>
      <Name>УтилитыОбщие</Name>
      <Global>true</Global>
      <ClientManagedApplication>true</ClientManagedApplication>
      <Server>true</Server>
      <ServerCall>true</ServerCall>
    </Properties>
  </CommonModule>
</MetaDataObject>`,
		"CommonModules/УтилитыОбщие/Ext/Module.bsl": `
Функция Помощь() Экспорт
	Возврат "ok";
КонецФункции

Процедура Тест() Экспорт
	Помощь();
	ТекущаяДата();
КонецПроцедуры
`,
	}
	for rel, content := range files {
		writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(rel)), content)
	}
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName),
		`{"version":1,"project":"`+string(id)+`","components":[{"id":"cfg","kind":"configuration","root":"cfg"}]}`)

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
	builtins := syntaxtest.Fixture(t)
	p, err := NewProjects(workspaceRoot, builtins, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	ctx := context.Background()
	op, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	return p, op
}

// TestResourceSymbolBodyMatchesSpanAndExpiresOnGenMismatch — §21/§18.2:
// resource link отдаёт РОВНО тот текст, по которому построен span, а
// несовпадающий gen -> resource_expired.
func TestResourceSymbolBodyMatchesSpanAndExpiresOnGenMismatch(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "res-symbol")
	symSvc := NewSymbolService(p)
	ctx := context.Background()

	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol: %+v %v", found.Items, err)
	}
	uid := found.Items[0].UID

	detail, err := symSvc.GetSymbol(ctx, GetSymbolInput{UID: uid, IncludeBody: true})
	if err != nil {
		t.Fatalf("GetSymbol: %v", err)
	}

	full, err := symSvc.ResourceSymbolBody(ctx, "res-symbol", uid, string(detail.Generation))
	if err != nil {
		t.Fatalf("ResourceSymbolBody(текущий gen): %v", err)
	}
	if full != detail.Items[0].Body {
		t.Fatalf("ResourceSymbolBody = %q, want ровно тот текст, что в get_symbol.body (тело не усечено в фикстуре): %q",
			full, detail.Items[0].Body)
	}
	if !strings.Contains(full, `Возврат "ok"`) {
		t.Fatalf("full = %q, want текст функции Помощь", full)
	}

	_, err = symSvc.ResourceSymbolBody(ctx, "res-symbol", uid, "e999.g999")
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != CodeResourceExpired {
		t.Fatalf("ResourceSymbolBody(чужой gen) err=%v, want *Error{Code: resource_expired}", err)
	}
}
