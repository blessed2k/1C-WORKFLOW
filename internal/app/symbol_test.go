package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// bslFixtureFiles — компонент с одним общим модулем (Global, модульная
// переменная, экспортная функция Помощь и неэкспортная процедура
// СкрытаяВспомогательная) и модулем менеджера справочника, зовущим Помощь() —
// тот же срез, что internal/index/pipeline_test.go использует для пайплайна,
// расширенный неэкспортным символом и переменной (фильтры kind/export,
// get_module_structure.variables).
func bslFixtureFiles() map[string]string {
	return map[string]string{
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
Перем ВнутреннийКэш;

Функция Помощь() Экспорт
	Возврат "ok";
КонецФункции

Процедура СкрытаяВспомогательная()
КонецПроцедуры
`,
		"Catalogs/Товары.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Catalog uuid="22222222-2222-2222-2222-222222222222">
    <Properties>
      <Name>Товары</Name>
    </Properties>
  </Catalog>
</MetaDataObject>`,
		"Catalogs/Товары/Ext/ManagerModule.bsl": `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
КонецПроцедуры
`,
	}
}

// newBSLFixtureProject создаёт проект с компонентом cfg (bslFixtureFiles),
// делает его активным и индексирует full reindex — конец в конец, реальный
// store, реальный резолвер, без стабов.
func newBSLFixtureProject(t *testing.T, id domain.ProjectID) (*Projects, *openProject) {
	t.Helper()
	return newBSLFixtureProjectCfg(t, id, index.Config{})
}

// newBSLFixtureProjectCfg: то же с явными tunables index.Service (часы и
// TTL свежести для тестов снимка, snapshot_test.go).
func newBSLFixtureProjectCfg(t *testing.T, id domain.ProjectID, cfg index.Config) (*Projects, *openProject) {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	for rel, content := range bslFixtureFiles() {
		writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(rel)), content)
	}
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))

	manifest := `{"version":1,"project":"` + string(id) + `","components":[{"id":"cfg","kind":"configuration","root":"cfg"}]}`
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), manifest)

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

	p, err := NewProjects(workspaceRoot, nil, cfg)
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

const fixtureModulePath = "CommonModules/УтилитыОбщие/Ext/Module.bsl"

// TestFindSymbolExactAndSubstring — R25: find_symbol находит символ и по
// точному имени, и по подстроке, без чтения модуля (тело в ответе нет полей
// вообще).
func TestFindSymbolExactAndSubstring(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "sym-find")
	svc := NewSymbolService(p)

	ctx := context.Background()
	exact, err := svc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil {
		t.Fatalf("FindSymbol(exact): %v", err)
	}
	if len(exact.Items) != 1 || exact.Items[0].Name != "Помощь" {
		t.Fatalf("FindSymbol(exact).Items = %+v, want ровно Помощь", exact.Items)
	}
	if exact.Items[0].UID == "" || exact.Items[0].Module != fixtureModulePath {
		t.Errorf("Items[0] = %+v, want непустой uid и module=%s", exact.Items[0], fixtureModulePath)
	}
	if !exact.Items[0].Export {
		t.Errorf("Помощь помечена Экспорт в фикстуре, Export=false")
	}

	sub, err := svc.FindSymbol(ctx, FindSymbolInput{Name: "омощ"})
	if err != nil {
		t.Fatalf("FindSymbol(substring): %v", err)
	}
	if len(sub.Items) != 1 || sub.Items[0].UID != exact.Items[0].UID {
		t.Fatalf("FindSymbol(substring).Items = %+v, want тот же символ, что exact", sub.Items)
	}
}

// TestFindSymbolKindFilterAndUnknownComponent — kind=procedure отсекает
// функцию; component, не описанный в манифесте, -> component_not_registered
// ДО обращения к store.
func TestFindSymbolKindFilterAndUnknownComponent(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "sym-kind")
	svc := &SymbolService{projects: p}
	ctx := context.Background()

	procOnly, err := svc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь", Kind: "procedure"})
	if err != nil {
		t.Fatalf("FindSymbol(kind=procedure): %v", err)
	}
	if len(procOnly.Items) != 0 {
		t.Fatalf("Помощь — функция, kind=procedure не должен её найти: %+v", procOnly.Items)
	}

	_, err = svc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь", Component: "нет-такого"})
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %T, want *app.Error", err)
	}
	if appErr.Code != CodeComponentNotRegistered {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeComponentNotRegistered)
	}
}

// TestGetSymbolByUIDAndByModuleName — get_symbol находит один и тот же
// символ обеими формами адресации (R25) и отдаёт его параметры.
func TestGetSymbolByUIDAndByModuleName(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "sym-get")
	svc := &SymbolService{projects: p}
	ctx := context.Background()

	found, err := svc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol setup: items=%+v err=%v", found.Items, err)
	}
	uid := found.Items[0].UID

	byUID, err := svc.GetSymbol(ctx, GetSymbolInput{UID: uid})
	if err != nil {
		t.Fatalf("GetSymbol(uid): %v", err)
	}
	byName, err := svc.GetSymbol(ctx, GetSymbolInput{Module: fixtureModulePath, Name: "Помощь", Component: "cfg"})
	if err != nil {
		t.Fatalf("GetSymbol(module+name): %v", err)
	}
	if len(byUID.Items) != 1 || len(byName.Items) != 1 {
		t.Fatalf("ожидался ровно один item в обоих ответах")
	}
	if byUID.Items[0].UID != byName.Items[0].UID {
		t.Fatalf("byUID.UID=%s != byName.UID=%s — два способа адресации должны находить один символ",
			byUID.Items[0].UID, byName.Items[0].UID)
	}

	notFound, err := svc.GetSymbol(ctx, GetSymbolInput{UID: "нет-такого-uid"})
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != CodeNotFound {
		t.Fatalf("GetSymbol(неизвестный uid) = %+v, err=%v, want *Error{Code: not_found}", notFound, err)
	}
}

// TestGetSymbolIncludeBodyCutFromBlobIgnoresDiskEdit — критерий приёмки:
// «правка файла на диске не меняет ответ, но даёт staleAgainstDisk». Тело
// режется из blob В ТОЙ ЖЕ read-транзакции, живой файл для тела не читается.
func TestGetSymbolIncludeBodyCutFromBlobIgnoresDiskEdit(t *testing.T) {
	p, op := newBSLFixtureProject(t, "sym-stale")
	svc := &SymbolService{projects: p}
	ctx := context.Background()

	found, err := svc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol setup: %+v %v", found.Items, err)
	}
	uid := found.Items[0].UID

	before, err := svc.GetSymbol(ctx, GetSymbolInput{UID: uid, IncludeBody: true})
	if err != nil {
		t.Fatalf("GetSymbol(before edit): %v", err)
	}
	if !strings.Contains(before.Items[0].Body, `Возврат "ok"`) {
		t.Fatalf("Body = %q, want содержит текст функции Помощь", before.Items[0].Body)
	}
	if before.Items[0].StaleAgainstDisk {
		t.Errorf("StaleAgainstDisk=true до правки диска, want false")
	}

	// Правим ФАЙЛ на диске напрямую, не через reindex: индекс об этом не знает.
	comp, _ := op.Manifest.Component("cfg")
	abs := filepath.Join(comp.AbsRoot, filepath.FromSlash(fixtureModulePath))
	if err := os.WriteFile(abs, []byte("\nФункция Помощь() Экспорт\n\tВозврат \"ИЗМЕНЕНО НА ДИСКЕ\";\nКонецФункции\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	after, err := svc.GetSymbol(ctx, GetSymbolInput{UID: uid, IncludeBody: true})
	if err != nil {
		t.Fatalf("GetSymbol(after edit): %v", err)
	}
	if after.Items[0].Body != before.Items[0].Body {
		t.Fatalf("Body изменился после правки диска БЕЗ reindex: %q != %q — тело обязано читаться из blob",
			after.Items[0].Body, before.Items[0].Body)
	}
	if !after.Items[0].StaleAgainstDisk {
		t.Errorf("StaleAgainstDisk=false после правки диска, want true")
	}
	if !after.Stale {
		t.Errorf("Response.Stale=false после правки диска, want true")
	}
	foundWarning := false
	for _, w := range after.Warnings {
		if w.Code == "staleAgainstDisk" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("Warnings = %+v, want код staleAgainstDisk", after.Warnings)
	}
}

// TestGetModuleStructureListsSymbolsWithoutModuleText — критерий приёмки:
// на самом модуле фикстуры ответ содержит символы (включая переменную из
// неэкспортной процедуры) и НЕ содержит текста модуля — структура ответа
// физически не несёт поля с телом.
func TestGetModuleStructureListsSymbolsWithoutModuleText(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "sym-structure")
	svc := &SymbolService{projects: p}
	ctx := context.Background()

	resp, err := svc.GetModuleStructure(ctx, GetModuleStructureInput{Module: fixtureModulePath, Component: "cfg"})
	if err != nil {
		t.Fatalf("GetModuleStructure: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	item := resp.Items[0]
	if item.SymbolCount != 2 {
		t.Fatalf("SymbolCount = %d, want 2 (Помощь + СкрытаяВспомогательная)", item.SymbolCount)
	}
	var names []string
	for _, s := range item.Symbols {
		names = append(names, s.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "Помощь") || !strings.Contains(strings.Join(names, ","), "СкрытаяВспомогательная") {
		t.Fatalf("Symbols = %+v, want Помощь и СкрытаяВспомогательная", names)
	}
	if item.VariableCount != 1 {
		t.Fatalf("VariableCount = %d, want 1 (Перем ВнутреннийКэш на уровне модуля)", item.VariableCount)
	}

	// Гард: модуль не должен вернуть свой текст — ищем сигнатуру функции по
	// сериализованному представлению, годной для этого свойства.
	for _, s := range item.Symbols {
		if strings.Contains(s.Signature, `Возврат "ok"`) {
			t.Fatalf("Signature = %q содержит тело — модуль не должен утекать через сигнатуру", s.Signature)
		}
	}
}

// TestFindSymbolCursorExpiresAfterIncrement — R61: страница 2 после
// инкремента (generation вырос) отдаёт cursor_expired.
func TestFindSymbolCursorExpiresAfterIncrement(t *testing.T) {
	p, op := newBSLFixtureProject(t, "sym-cursor")
	svc := &SymbolService{projects: p}
	ctx := context.Background()

	// Подстрока "о" покрывает оба символа фикстуры (Помощь, СкрытаяВспомогательная).
	page1, err := svc.FindSymbol(ctx, FindSymbolInput{Name: "о", Limit: 1, Component: "cfg"})
	if err != nil {
		t.Fatalf("FindSymbol page1: %v", err)
	}
	if page1.NextCursor == "" {
		t.Fatalf("NextCursor пуст на первой странице limit=1 при >1 совпадении, want непустой курсор")
	}

	if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) второй раз: %v", err)
	}

	_, err = svc.FindSymbol(ctx, FindSymbolInput{Name: "о", Limit: 1, Component: "cfg", Cursor: page1.NextCursor})
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != CodeCursorExpired {
		t.Fatalf("FindSymbol(page2 после reindex) err=%v, want *Error{Code: cursor_expired}", err)
	}
}
