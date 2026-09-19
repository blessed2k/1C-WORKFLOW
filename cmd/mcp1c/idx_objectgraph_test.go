package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// Тонкая обёртка object_graph поверх ObjectGraphService.Radius. Тесты бьют
// ровно по двум швам: депф-потолок 2 (жёсткое отклонение, а не тихое
// обрезание) и project= как настоящий разовый override, не
// путающий данные разных проектов (contract возврата явно требует мутацию
// именно на этих двух местах). Публичный интерфейс — реальный MCP-клиент
// (mcp.NewInMemoryTransports + CallTool), как TestServerInfoTool/
// TestSetDumpTool в server_test.go.
//
// Фикстуры — чистый XML без BSL: одного объявления <RegisterRecords> в
// метаданных документа достаточно, чтобы паблишер (internal/index/
// objectedges.go:publishDeclaredMovementEdges) создал ребро writes-declared —
// разбирать/резолвить BSL для этого не нужно (internal/parse/meta/
// document_test.go подтверждает формат тем же приёмом).

// ogUID — детерминированный псевдо-UUID для фикстур (тот же формат hex-групп,
// что fixtureCatalogXML в idx_meta_test.go, просто с изменяемым хвостом,
// чтобы объекты внутри одной фикстуры не путались по uuid).
func ogUID(seq int) string {
	return fmt.Sprintf("a0000000-0000-0000-0000-%012d", seq)
}

// ogRegisterXML — минимальный InformationRegister: только Properties/Name,
// без Dimension/Resource — publishDeclaredMovementEdges требует лишь, чтобы
// регистр существовал как metadata_object (internal/index/objectedges.go:71
// «регистр, объявленный в XML, но отсутствующий в конфигурации, ребра не
// даёт»), содержимое ChildObjects ему не важно.
func ogRegisterXML(uid, name string) string {
	return metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.20">
	<InformationRegister uuid="` + uid + `">
		<Properties>
			<Name>` + name + `</Name>
		</Properties>
		<ChildObjects/>
	</InformationRegister>
</MetaDataObject>`
}

// ogDocumentXML — документ, декларирующий движения в перечисленные регистры
// (полными ссылками вида "InformationRegister.Имя") через <RegisterRecords>,
// без единой строки BSL — формат скопирован из internal/parse/meta/
// document_test.go:documentWithMovementsFixture (реальная выгрузка УТ).
func ogDocumentXML(uid, name string, registerRefs ...string) string {
	var items strings.Builder
	for _, ref := range registerRefs {
		items.WriteString(`<xr:Item xsi:type="xr:MDObjectRef">` + ref + `</xr:Item>`)
	}
	return metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.20">
	<Document uuid="` + uid + `">
		<Properties>
			<Name>` + name + `</Name>
			<RegisterRecords>` + items.String() + `</RegisterRecords>
		</Properties>
		<ChildObjects/>
	</Document>
</MetaDataObject>`
}

// ogManifest пишет 1c-project.json с единственным компонентом cfg — то, что
// reindex(projectRoot=...) читает через workspace.LoadManifest при первой
// регистрации проекта (internal/app.Projects.EnsureProjectActive). Отдельно
// от metaFixtureProject (idx_meta_test.go): та пишет ЕЩЁ и registry.json
// целиком, перезаписывая его при каждом вызове — для теста с ДВУМЯ
// одновременно зарегистрированными проектами (override) это стёрло бы первый.
// Здесь регистрацию и активацию делает сам инструмент reindex через
// EnsureProjectActive/Upsert (добавляет, не затирает).
func ogManifest(t *testing.T, projectRoot, id string) {
	t.Helper()
	manifest := map[string]any{
		"version": 1,
		"project": id,
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	metaWriteFile(t, filepath.Join(projectRoot, "1c-project.json"), string(data))
}

// ogWriteProject кладёt Configuration.xml + один документ docName,
// декларирующий движения во все регистры из registers — минимальный набор
// файлов, дающий N рёбер writes-declared от одного и того же документа.
func ogWriteProject(t *testing.T, projectRoot, uidSeed string, docName string, registers []string) {
	t.Helper()
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML(uidSeed))
	var refs []string
	for i, r := range registers {
		metaWriteFile(t, filepath.Join(projectRoot, "cfg", "InformationRegisters", r+".xml"),
			ogRegisterXML(ogUID(100+i), r))
		refs = append(refs, "InformationRegister."+r)
	}
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Documents", docName+".xml"),
		ogDocumentXML(ogUID(1), docName, refs...))
}

// ogConnect поднимает реальный сервер (той же options{projectsRoot: ...},
// что TestSetDumpElicitsPath в elicit_test.go) и подключает к нему клиента
// через in-memory MCP-транспорт — тот же приём, что TestServerInfoTool в
// server_test.go.
func ogConnect(t *testing.T, ctx context.Context, workspaceRoot string) *mcp.ClientSession {
	t.Helper()
	srv, closer := newServerWithCloser(options{projectsRoot: workspaceRoot})
	// Индекс живёт в t.TempDir(): пока он открыт, Windows не даёт удалить
	// каталог, и уборка теста падает уже после зелёных проверок.
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Errorf("закрытие индекса: %v", err)
		}
	})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-object-graph", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// ogReindex регистрирует (если ещё не зарегистрирован) и активирует проект в
// projectRoot — единственный production-путь сделать это без ручной правки
// .mcp1c/registry.json (idx_status.go: reindexInput.ProjectRoot).
func ogReindex(t *testing.T, ctx context.Context, cs *mcp.ClientSession, projectRoot string) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "reindex",
		Arguments: map[string]any{"projectRoot": projectRoot, "mode": "full"}})
	if err != nil {
		t.Fatalf("reindex(%s): transport error: %v", projectRoot, err)
	}
	if res.IsError {
		t.Fatalf("reindex(%s) вернул ошибку: %s", projectRoot, contentText(res))
	}
}

// ogCall зовёт object_graph и разбирает ответ в типизированный
// app.Response[app.RadiusEdgeItem] (тот же тип, что отдаёт ObjectGraphService.
// Radius) — приём из stdio_live_test.go: StructuredContent через json.Marshal/
// Unmarshal, а не парсинг текстового content. Ошибочный ответ (res.IsError)
// отдаётся вызывающему как есть, с пустым Response — сообщение читается через
// contentText(res).
func ogCall(t *testing.T, ctx context.Context, cs *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, app.Response[app.RadiusEdgeItem]) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "object_graph", Arguments: args})
	if err != nil {
		t.Fatalf("object_graph: transport error: %v", err)
	}
	if res.IsError {
		return res, app.Response[app.RadiusEdgeItem]{}
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("object_graph: marshal structuredContent: %v", err)
	}
	var out app.Response[app.RadiusEdgeItem]
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("object_graph: unmarshal Response: %v (raw=%s)", err, raw)
	}
	return res, out
}

// TestObjectGraphDepthCapEnforced: критерий приёмки «depth
// потолок 2, больше отвергается понятной ошибкой, не тихо обрезается» — и
// отдельно, что глубина реально ДОХОДИТ до сервиса, а не подменяется
// дефолтом при передаче (главная мутация, названная в тикете явно).
//
// Граф: ЗаказА -> РегА <- ЗаказЕ (оба документа декларируют движения в один
// и тот же регистр). С корнем ЗаказА: depth=1 видит только прямое ребро
// (ЗаказА->РегА), ЗаказЕ появляется ТОЛЬКО на depth=2 — второй хоп обхода
// вскрывает соседей РегА, включая входящее ребро от ЗаказЕ.
func TestObjectGraphDepthCapEnforced(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()

	ogManifest(t, projectRoot, "og-depth")
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML("Тест"))
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "InformationRegisters", "РегА.xml"), ogRegisterXML(ogUID(1), "РегА"))
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Documents", "ЗаказА.xml"),
		ogDocumentXML(ogUID(2), "ЗаказА", "InformationRegister.РегА"))
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Documents", "ЗаказЕ.xml"),
		ogDocumentXML(ogUID(3), "ЗаказЕ", "InformationRegister.РегА"))

	cs := ogConnect(t, ctx, workspaceRoot)
	ogReindex(t, ctx, cs, projectRoot)

	// depth=3 превышает жёсткий потолок 2 — отвергается, ничего не читая.
	res, _ := ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказА", "depth": 3})
	if !res.IsError {
		t.Fatalf("depth=3 должен быть отвергнут (потолок 2), получен успешный ответ")
	}
	if msg := contentText(res); !strings.Contains(msg, "потолок") {
		t.Errorf("сообщение об ошибке должно называть потолок явно, получено: %s", msg)
	}

	// depth=1: только прямой сосед.
	_, d1 := ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказА", "depth": 1})
	if len(d1.Items) != 1 {
		t.Fatalf("depth=1: items = %d, want 1: %+v", len(d1.Items), d1.Items)
	}
	if d1.Items[0].ToDisplay != "РегА" || d1.Items[0].FromDisplay != "ЗаказА" {
		t.Errorf("depth=1: неожиданное ребро %+v", d1.Items[0])
	}
	for _, it := range d1.Items {
		if it.FromDisplay == "ЗаказЕ" || it.ToDisplay == "ЗаказЕ" {
			t.Errorf("depth=1 не должен раскрывать второй хоп, но ЗаказЕ уже виден: %+v", d1.Items)
		}
	}

	// depth=2: второй хоп раскрывает ЗаказЕ -> РегА.
	_, d2 := ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказА", "depth": 2})
	if len(d2.Items) != 2 {
		t.Fatalf("depth=2: items = %d, want 2 (прямое ребро + второй хоп): %+v", len(d2.Items), d2.Items)
	}
	var sawE bool
	for _, it := range d2.Items {
		if it.FromDisplay == "ЗаказЕ" || it.ToDisplay == "ЗаказЕ" {
			sawE = true
		}
	}
	if !sawE {
		t.Errorf("depth=2 обязан раскрыть второй хоп (ЗаказЕ -> РегА), получено %+v", d2.Items)
	}
}

// TestObjectGraphProjectOverrideDoesNotMixData: критерий приёмки
// «project= работает как разовый override: проект выбирается из реестра по
// корню, активный проект процесса НЕ меняется» (D3, docs/architecture-graph.md) и «несуще­
// ствующий проект даёт понятную ошибку». Contract возврата отдельно требует
// мутацию именно здесь: «project= действительно переключает проект, не
// путая данные разных проектов».
//
// Два независимых проекта, Alpha и Beta, каждый со своим документом и своим
// регистром (имена НЕ пересекаются намеренно — совпадение имён замаскировало
// бы утечку данных между проектами). Beta регистрируется вторым и остаётся
// активным весь тест.
func TestObjectGraphProjectOverrideDoesNotMixData(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	rootAlpha := t.TempDir()
	rootBeta := t.TempDir()

	ogManifest(t, rootAlpha, "og-alpha")
	ogWriteProject(t, rootAlpha, "Alpha", "ЗаказAlpha", []string{"РегAlpha"})
	ogManifest(t, rootBeta, "og-beta")
	ogWriteProject(t, rootBeta, "Beta", "ЗаказBeta", []string{"РегBeta"})

	cs := ogConnect(t, ctx, workspaceRoot)
	ogReindex(t, ctx, cs, rootAlpha) // регистрирует и активирует Alpha
	ogReindex(t, ctx, cs, rootBeta)  // регистрирует и активирует Beta -> активный теперь Beta

	// без project=: активный (Beta) отвечает про ЗаказBeta.
	res, resp := ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказBeta"})
	if res.IsError {
		t.Fatalf("ЗаказBeta на активном Beta должен найтись: %s", contentText(res))
	}
	if len(resp.Items) != 1 || resp.Items[0].ToDisplay != "РегBeta" {
		t.Fatalf("неожиданный ответ по ЗаказBeta на активном Beta: %+v", resp.Items)
	}

	// без project=: активный (Beta) НЕ видит ЗаказAlpha — подтверждает, что
	// активный проект действительно Beta, а не Alpha.
	res, _ = ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказAlpha"})
	if !res.IsError {
		t.Fatalf("ЗаказAlpha не должен находиться на активном Beta")
	}

	// project=rootAlpha: override реально читает данные Alpha.
	res, resp = ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказAlpha", "project": rootAlpha})
	if res.IsError {
		t.Fatalf("project=rootAlpha должен найти ЗаказAlpha: %s", contentText(res))
	}
	if len(resp.Items) != 1 || resp.Items[0].ToDisplay != "РегAlpha" {
		t.Fatalf("override на Alpha дал не Alpha-данные: %+v", resp.Items)
	}

	// project=rootAlpha ищет ЗаказBeta — ДОЛЖНО быть not_found: если бы
	// override молча падал обратно на активный (Beta) вместо реального
	// переключения, этот вызов ошибочно бы нашёл ЗаказBeta через "чужой"
	// project=. Это и есть проверка «не путает данные разных проектов».
	res, _ = ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказBeta", "project": rootAlpha})
	if !res.IsError {
		t.Fatalf("project=rootAlpha не должен видеть ЗаказBeta (данные Beta просочились через override)")
	}

	// активный проект процесса НЕ изменился override-вызовами: тот же запрос
	// без project= снова находит ЗаказBeta.
	res, resp = ogCall(t, ctx, cs, map[string]any{"objectType": "Document", "objectName": "ЗаказBeta"})
	if res.IsError {
		t.Fatalf("после override активный проект должен остаться Beta, но ЗаказBeta больше не находится: %s", contentText(res))
	}
	if len(resp.Items) != 1 {
		t.Fatalf("активный проект после override повреждён: %+v", resp.Items)
	}

	// «Понятная ошибка на несуществующий project=» — две РАЗНЫЕ ветки
	// Projects.ByRoot, проверены отдельными тестами ниже
	// (TestObjectGraphProjectNonexistentPathRejected,
	// TestObjectGraphProjectUnregisteredExistingDirRejected): путь, которого
	// физически нет на диске (отсекается filepath.EvalSymlinks ДО сравнения
	// с реестром), и каталог, который реально существует, но никогда не
	// регистрировался через reindex (отсекается findByRoot). Раньше здесь
	// проверялся только первый случай — ревью нашло, что второй, куда более
	// вероятный в реальном использовании (опечатка в уже существующем пути
	// вместо буквально несуществующего), не был покрыт вовсе, и мутация,
	// подменяющая отказ findByRoot на молчаливый откат к Active, оставляла
	// весь пакет зелёным.
}

// TestObjectGraphProjectNonexistentPathRejected — «project=» на путь,
// которого физически нет на диске: Projects.ByRoot отказывает на
// filepath.EvalSymlinks, ДО того, как сравнение с реестром (findByRoot)
// вообще начинается. Активного проекта нарочно нет вовсе (workspaceRoot
// создан, reindex ни разу не звался) — эта ветка ByRoot не читает Active,
// так что её проверка не нуждается ни в каком зарегистрированном проекте.
func TestObjectGraphProjectNonexistentPathRejected(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	cs := ogConnect(t, ctx, workspaceRoot)

	res, _ := ogCall(t, ctx, cs, map[string]any{
		"objectType": "Document", "objectName": "ЧтоУгодно",
		"project": filepath.Join(workspaceRoot, "нет-такого-каталога-на-диске"),
	})
	if !res.IsError {
		t.Fatalf("project= на путь, которого нет на диске, должен дать ошибку")
	}
}

// TestObjectGraphProjectUnregisteredExistingDirRejected — критическая ветка,
// которую соседний TestObjectGraphProjectNonexistentPathRejected НЕ
// проверяет: каталог РЕАЛЬНО существует на диске (значит EvalSymlinks его
// пропускает), но никогда не проходил через reindex(projectRoot=...) и
// поэтому отсутствует в реестре — отказать обязан findByRoot внутри
// Projects.ByRoot.
//
// Найдено ревью качества: мутация «подменить ветку отказа findByRoot на
// молчаливый return p.projects.Active(ctx)» оставляла весь пакет зелёным,
// потому что единственный существовавший до этого теста кейс
// («project=<путь, которого нет на диске>») отсекается раньше, в
// EvalSymlinks, и до findByRoot не доходит вовсе.
//
// Здесь есть РЕАЛЬНЫЙ активный проект (Beta) с объектом того же имени,
// которое запрашивается через unregisteredDir: если бы override молча
// падал на Active вместо not_found, вызов ошибочно вернул бы 200 с данными
// Beta — тест ловит именно это, а не просто "код ошибки не тот".
func TestObjectGraphProjectUnregisteredExistingDirRejected(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	rootBeta := t.TempDir()
	unregisteredDir := t.TempDir() // существует на диске, но НИКОГДА не звался reindex(projectRoot=...)

	ogManifest(t, rootBeta, "og-beta-unreg")
	ogWriteProject(t, rootBeta, "Beta", "ЗаказBeta", []string{"РегBeta"})

	cs := ogConnect(t, ctx, workspaceRoot)
	ogReindex(t, ctx, cs, rootBeta) // регистрирует и активирует Beta — единственный активный проект

	res, _ := ogCall(t, ctx, cs, map[string]any{
		"objectType": "Document", "objectName": "ЗаказBeta", // существует именно в АКТИВНОМ (Beta)
		"project": unregisteredDir,
	})
	if !res.IsError {
		t.Fatalf("project=<существующий, но незарегистрированный каталог> должен дать not_found, " +
			"а не молча ответить данными активного проекта")
	}
}

// TestObjectGraphResponsePagination: критерий приёмки «лимиты и
// обрезание тем же способом, что у соседних индексных инструментов ...
// обрезание сообщается через Warnings и NextCursor», «ответ укладывается в
// бюджет: дозировка проверена тестом, как у соседей» — тот же приём, что
// TestFindRegisterWritesAgainstGetMovementsRealDump (idx_meta_test.go):
// limit=2 на трёх рёбрах даёт непустой NextCursor, вторая страница не
// повторяет первую и в сумме покрывает все три ребра.
func TestObjectGraphResponsePagination(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()

	ogManifest(t, projectRoot, "og-paging")
	ogWriteProject(t, projectRoot, "Paging", "ЗаказПагинация", []string{"Рег1", "Рег2", "Рег3"})

	cs := ogConnect(t, ctx, workspaceRoot)
	ogReindex(t, ctx, cs, projectRoot)

	baseArgs := map[string]any{"objectType": "Document", "objectName": "ЗаказПагинация", "limit": 2}
	res, page1 := ogCall(t, ctx, cs, baseArgs)
	if res.IsError {
		t.Fatalf("page1: %s", contentText(res))
	}
	if len(page1.Items) != 2 {
		t.Fatalf("page1: items = %d, want 2 (limit=2 из 3 рёбер): %+v", len(page1.Items), page1.Items)
	}
	if page1.TotalCount != 3 {
		t.Errorf("page1: TotalCount = %d, want 3 (полный размер под фильтром, не размер страницы)", page1.TotalCount)
	}
	if page1.NextCursor == "" {
		t.Fatal("page1: NextCursor пуст, хотя рёбер больше, чем limit")
	}

	page2Args := map[string]any{"objectType": "Document", "objectName": "ЗаказПагинация", "limit": 2, "cursor": page1.NextCursor}
	res, page2 := ogCall(t, ctx, cs, page2Args)
	if res.IsError {
		t.Fatalf("page2: %s", contentText(res))
	}
	if len(page2.Items) != 1 {
		t.Fatalf("page2: items = %d, want 1 (последнее из трёх рёбер): %+v", len(page2.Items), page2.Items)
	}

	seen := map[int64]bool{}
	for _, it := range append(append([]app.RadiusEdgeItem{}, page1.Items...), page2.Items...) {
		if seen[it.ID] {
			t.Fatalf("ребро id=%d повторилось между страницами", it.ID)
		}
		seen[it.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("вместе страницы должны покрыть все 3 ребра, получено %d", len(seen))
	}
}

// ogAccumRegisterXML: минимальный регистр накопления для фикстуры с кодом
// проведения (Движения.<Имя>.Записать()).
func ogAccumRegisterXML(uid, name string) string {
	return metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<AccumulationRegister uuid="` + uid + `">
		<Properties>
			<Name>` + name + `</Name>
		</Properties>
		<ChildObjects/>
	</AccumulationRegister>
</MetaDataObject>`
}

// TestObjectGraphToolViewDiff: object_graph принимает view (веха В3, issue
// #5). Расширение перехватывает проведение документа (&После) и пишет в
// регистр, которого база не пишет: view=raw этого ребра не видит,
// view=diff отдаёт только его, с именем расширения в layer и diff=added.
func TestObjectGraphToolViewDiff(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()

	manifest, err := json.Marshal(map[string]any{
		"version": 1, "project": "og-view",
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
			{"id": "ext", "kind": "extension", "root": "ext", "appliesTo": "cfg", "applyOrder": 1},
		},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	metaWriteFile(t, filepath.Join(projectRoot, "1c-project.json"), string(manifest))
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML("Тест"))
	metaWriteFile(t, filepath.Join(projectRoot, "ext", "Configuration.xml"), strings.Replace(metaConfigurationXML("Расширение"),
		"</Name>", "</Name>\n\t\t\t<ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose>", 1))
	for _, comp := range []string{"cfg", "ext"} {
		metaWriteFile(t, filepath.Join(projectRoot, comp, "Documents", "Отгрузка.xml"), ogDocumentXML(ogUID(1), "Отгрузка"))
		metaWriteFile(t, filepath.Join(projectRoot, comp, "AccumulationRegisters", "Остатки.xml"), ogAccumRegisterXML(ogUID(2), "Остатки"))
		metaWriteFile(t, filepath.Join(projectRoot, comp, "AccumulationRegisters", "Резервы.xml"), ogAccumRegisterXML(ogUID(3), "Резервы"))
	}
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Documents", "Отгрузка", "Ext", "ObjectModule.bsl"),
		"\nПроцедура ОбработкаПроведения(Отказ, Режим)\n\tДвижения.Остатки.Записать();\nКонецПроцедуры\n")
	metaWriteFile(t, filepath.Join(projectRoot, "ext", "Documents", "Отгрузка", "Ext", "ObjectModule.bsl"),
		"\n&После(\"ОбработкаПроведения\")\nПроцедура Расш_ОбработкаПроведения(Отказ, Режим)\n\tДвижения.Резервы.Записать();\nКонецПроцедуры\n")

	cs := ogConnect(t, ctx, workspaceRoot)
	ogReindex(t, ctx, cs, projectRoot)

	args := func(view string) map[string]any {
		return map[string]any{"objectType": "Document", "objectName": "Отгрузка", "depth": 1, "view": view}
	}
	res, raw := ogCall(t, ctx, cs, args("raw"))
	if res.IsError {
		t.Fatalf("view=raw: %s", contentText(res))
	}
	if len(raw.Items) != 1 || raw.Items[0].ToDisplay != "Остатки" || raw.Items[0].Layer != "base" {
		t.Errorf("view=raw: %+v, want одно ребро базы к Остатки", raw.Items)
	}
	res, diff := ogCall(t, ctx, cs, args("diff"))
	if res.IsError {
		t.Fatalf("view=diff: %s", contentText(res))
	}
	if len(diff.Items) != 1 || diff.Items[0].ToDisplay != "Резервы" || diff.Items[0].Layer != "ext" || diff.Items[0].Diff != app.EdgeDiffAdded {
		t.Errorf("view=diff: %+v, want одно ребро расширения ext к Резервы с diff=added", diff.Items)
	}
	res, _ = ogCall(t, ctx, cs, args("effectiv"))
	if !res.IsError || !strings.Contains(contentText(res), "view") {
		t.Errorf("опечатка в view обязана быть ошибкой, получено IsError=%v %s", res.IsError, contentText(res))
	}
}
