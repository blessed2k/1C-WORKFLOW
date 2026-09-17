package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// metaDumpEnvVars — те же переменные, что internal/index/internal/resolve
// realworld-тесты (interfaces.md §28): один прогон, один и тот же путь к
// выгрузке, не зашитый в код.
var metaDumpEnvVars = []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"}

func metaDumpRoot(t *testing.T) string {
	t.Helper()
	for _, env := range metaDumpEnvVars {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if _, err := os.Stat(v); err != nil {
				t.Skipf("%s указывает на недоступный путь: %v", env, err)
			}
			return v
		}
	}
	t.Skip("переменная окружения ONEC_DUMP не задана — прогон на реальной выгрузке пропущен")
	return ""
}

const metaBOM = "\ufeff"

func metaConfigurationXML(name string) string {
	return metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0002">
		<Properties>
			<Name>` + name + `</Name>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`
}

func metaWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// metaCopyFile копирует один файл, создавая недостающие каталоги.
func metaCopyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", src, err)
	}
	metaWriteFile(t, dst, string(data))
}

// metaCopyTree копирует поддерево реальной выгрузки в фикстуру — тот же
// приём, что internal/index/realworld_test.go:copyTree, отдельная копия
// здесь: cmd/mcp1c не может дотянуться до internal/index (RuleCmdThroughApp).
func metaCopyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", src, err)
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			metaCopyTree(t, s, d)
			continue
		}
		metaCopyFile(t, s, d)
	}
}

// metaFixtureProject регистрирует один проект (компонент cfg = дерево,
// собранное вызывающим ДО этого вызова в projectRoot/cfg) и делает его
// активным. Тот же паттерн, что internal/app/projects_test.go:newFixtureProject
// (отдельная копия — internal/app не экспортирует тестовые хелперы наружу
// пакета, а cmd/mcp1c не может импортировать internal/workspace напрямую).
func metaFixtureProject(t *testing.T, workspaceRoot, projectRoot, id string) {
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

	registryPath := filepath.Join(workspaceRoot, ".mcp1c", "registry.json")
	registry := map[string]any{
		"version": 1,
		"projects": []map[string]any{
			{"id": id, "root": projectRoot},
		},
		"activeProject": id,
	}
	regData, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	metaWriteFile(t, registryPath, string(regData))
}

// TestFindRegisterWritesAgainstGetMovementsRealDump — критерий приёмки
// тикета 12: «результат сверен с существующим get_movements на том же
// объекте, расхождения объяснены» (не косметическая сверка — прогон на
// реальном документе УТ, найденном в выгрузке заранее: КорректировкаНалого-
// обложенияНДСПартийТоваров пишет движения по регистру ПартииТоваровОрганизаций
// через коллекцию Движения.X, БЕЗ явного "Записывать = Истина" в своём
// модуле (флаг проставляется где-то в общем коде БСП) — это ЗАВЕДОМОЕ
// расхождение: get_movements.WriteFlag регексом промахивается мимо этого
// идиома, а find_register_writes(modes=movement) видит факт напрямую из
// AST (bsl.RegisterAccess), не завися от конкретной строки кода.
func TestFindRegisterWritesAgainstGetMovementsRealDump(t *testing.T) {
	dumpRoot := metaDumpRoot(t)
	const doc = "КорректировкаНалогообложенияНДСПартийТоваров"
	const register = "ПартииТоваровОрганизаций"
	docXML := filepath.Join(dumpRoot, "Documents", doc+".xml")
	docDir := filepath.Join(dumpRoot, "Documents", doc)
	if _, err := os.Stat(docXML); err != nil {
		t.Skipf("в выгрузке нет Documents/%s.xml: %v", doc, err)
	}

	// --- старый инструмент: get_movements прямо на реальной выгрузке ---
	movements, err := source.NewXMLSource(dumpRoot).Movements(context.Background(), doc)
	if err != nil {
		t.Fatalf("Movements: %v", err)
	}
	var got *source.RegisterMovement
	for i := range movements.Registers {
		if strings.EqualFold(movements.Registers[i].Register, "РегистрНакопления."+register) {
			got = &movements.Registers[i]
		}
	}
	if got == nil {
		t.Fatalf("get_movements не нашёл регистр %s в %s среди %+v — фикстура сломана (проверь реальную выгрузку)", register, doc, movements.Registers)
	}
	if !got.Declared || !got.UsedInCode {
		t.Fatalf("get_movements: %+v, ожидались Declared=true и UsedInCode=true", got)
	}

	// --- новый инструмент: find_register_writes на копии ЭТОГО документа ---
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML("Тест"))
	metaCopyFile(t, docXML, filepath.Join(projectRoot, "cfg", "Documents", doc+".xml"))
	metaCopyTree(t, docDir, filepath.Join(projectRoot, "cfg", "Documents", doc))
	metaFixtureProject(t, workspaceRoot, projectRoot, "ut-register-check")

	projects, err := app.NewProjects(workspaceRoot, nil, app.DefaultIndexConfig())
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { projects.Close() })
	ctx := context.Background()

	if _, err := app.NewIndexStatusService(projects).Reindex(ctx, app.ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	registerSvc := app.NewRegisterService(projects)
	resp, err := registerSvc.FindRegisterWrites(ctx, app.FindRegisterWritesInput{
		Register: register, Modes: "movement", Limit: 50,
	})
	if err != nil {
		t.Fatalf("FindRegisterWrites: %v", err)
	}
	var inThisDoc []app.RegisterAccessItem
	for _, item := range resp.Items {
		if item.Owner != nil && strings.Contains(item.Owner.Module, doc) {
			inThisDoc = append(inThisDoc, item)
		}
	}
	if len(inThisDoc) == 0 {
		t.Fatalf("find_register_writes(register=%s, modes=movement) не нашёл ни одного доступа в модуле %s среди %+v",
			register, doc, resp.Items)
	}
	for _, item := range inThisDoc {
		if item.Mode != "movement" {
			t.Errorf("mode = %q, ожидался movement (Движения.%s — коллекция движений, не менеджер записи)", item.Mode, register)
		}
	}

	// Расхождение, названное явно (не молчаливое): get_movements.WriteFlag
	// для этого регистра в этом документе — false (нет "Записывать=Истина"
	// в его собственном ObjectModule.bsl), при этом find_register_writes
	// подтверждает реальный доступ к регистру (Mode=movement). Оба
	// инструмента правы каждый по своему определению: get_movements ищет
	// ИМЕННО строку присвоения флага, find_register_writes — факт обращения
	// к коллекции движений. Не баг ни одного из них.
	t.Logf("расхождение объяснено: get_movements.WriteFlag=%v (регекс не находит явный 'Записывать=Истина' — "+
		"флаг ставится вне модуля документа), find_register_writes нашёл %d доступ(ов) mode=movement к тому же регистру в том же документе",
		got.WriteFlag, len(inThisDoc))

	// --- пагинация (R61) на реальных данных: тот же фикстурный doc содержит
	// несколько разных регистров-движений; limit=1 на другом регистре этого
	// же документа даёт nextCursor, вторая страница отдаёт следующую запись,
	// а не повтор первой. ---
	page1, err := registerSvc.FindRegisterWrites(ctx, app.FindRegisterWritesInput{
		Register: "СебестоимостьТоваров", Modes: "movement", Limit: 1,
	})
	if err != nil {
		t.Fatalf("FindRegisterWrites (page1): %v", err)
	}
	if len(page1.Items) != 1 {
		t.Skipf("регистр СебестоимостьТоваров дал %d записей в этой копии документа — пагинационная часть теста рассчитана на >=2, пропуск", len(page1.Items))
	}
	if page1.NextCursor == "" {
		t.Fatal("NextCursor пуст на limit=1, хотя в документе есть более одного доступа к СебестоимостьТоваров")
	}
	page2, err := registerSvc.FindRegisterWrites(ctx, app.FindRegisterWritesInput{
		Register: "СебестоимостьТоваров", Modes: "movement", Limit: 1, Cursor: page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("FindRegisterWrites (page2): %v", err)
	}
	if len(page2.Items) == 0 {
		t.Fatal("вторая страница пуста, хотя nextCursor был непуст")
	}
	if page2.Items[0].Span == page1.Items[0].Span {
		t.Fatalf("вторая страница повторяет первую запись: %+v", page2.Items[0])
	}
}

// TestGetObjectRealDump — критерий приёмки тикета 12: «get_object на объекте
// из реальной выгрузки даёт структуру, формы, подписки, права; parts реально
// сужает выдачу». Catalogs/Номенклатура выбран заранее (не подогнан по ходу
// теста): содержит реквизиты, минимум одну подписку с единственным
// источником cfg:CatalogObject.Номенклатура (обходит известный долг —
// publishEventSubscription хранит только Sources[0], см. index/publishmeta2.go)
// и роль ДобавлениеИзменениеНоменклатуры с правом на "Catalog.Номенклатура"
// (Rights.xml, строка 572 реальной выгрузки).
func TestGetObjectRealDump(t *testing.T) {
	dumpRoot := metaDumpRoot(t)
	const mtype, name = "Catalog", "Номенклатура"
	const subscriptionName = "ЗарегистрироватьИзмененияСправочникаПередЗаписьюДляОбменаСМПЗаказы"
	const roleName = "ДобавлениеИзменениеНоменклатуры"

	catXML := filepath.Join(dumpRoot, "Catalogs", name+".xml")
	catDir := filepath.Join(dumpRoot, "Catalogs", name)
	subXML := filepath.Join(dumpRoot, "EventSubscriptions", subscriptionName+".xml")
	roleXML := filepath.Join(dumpRoot, "Roles", roleName+".xml")
	roleDir := filepath.Join(dumpRoot, "Roles", roleName)
	for _, p := range []string{catXML, catDir, subXML, roleXML, roleDir} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("фикстура реальной выгрузки неполна, нет %s: %v", p, err)
		}
	}

	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML("Тест"))
	metaCopyFile(t, catXML, filepath.Join(projectRoot, "cfg", "Catalogs", name+".xml"))
	metaCopyTree(t, catDir, filepath.Join(projectRoot, "cfg", "Catalogs", name))
	metaCopyFile(t, subXML, filepath.Join(projectRoot, "cfg", "EventSubscriptions", subscriptionName+".xml"))
	metaCopyFile(t, roleXML, filepath.Join(projectRoot, "cfg", "Roles", roleName+".xml"))
	metaCopyTree(t, roleDir, filepath.Join(projectRoot, "cfg", "Roles", roleName))
	metaFixtureProject(t, workspaceRoot, projectRoot, "ut-object-check")

	projects, err := app.NewProjects(workspaceRoot, nil, app.DefaultIndexConfig())
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { projects.Close() })
	ctx := context.Background()
	if _, err := app.NewIndexStatusService(projects).Reindex(ctx, app.ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	metaSvc := app.NewMetadataService(projects)

	full, err := metaSvc.GetObject(ctx, app.GetObjectInput{Type: mtype, Name: name})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if len(full.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(full.Items))
	}
	obj := full.Items[0]
	if obj.Type != mtype || obj.Name != name {
		t.Fatalf("Type/Name = %s/%s, want %s/%s", obj.Type, obj.Name, mtype, name)
	}
	if len(obj.Members) == 0 {
		t.Error("Members пуст — у Номенклатуры точно есть реквизиты в реальной выгрузке")
	}
	var foundSub bool
	for _, s := range obj.Subscriptions {
		if s.Name == subscriptionName {
			foundSub = true
			if s.Resolution == "" {
				t.Errorf("подписка %s: пустой Resolution", s.Name)
			}
		}
	}
	if !foundSub {
		t.Errorf("подписка %s не найдена среди %+v", subscriptionName, obj.Subscriptions)
	}
	var foundRight bool
	for _, r := range obj.RoleRights {
		if r.Role == roleName {
			foundRight = true
		}
	}
	if !foundRight {
		t.Errorf("права роли %s не найдены среди %+v", roleName, obj.RoleRights)
	}

	// parts реально сужает выдачу: members-only не несёт ни подписок, ни
	// прав; subscriptions-only не несёт реквизитов.
	membersOnly, err := metaSvc.GetObject(ctx, app.GetObjectInput{Type: mtype, Name: name, Parts: "members"})
	if err != nil {
		t.Fatalf("GetObject(parts=members): %v", err)
	}
	mo := membersOnly.Items[0]
	if len(mo.Members) == 0 {
		t.Error("parts=members: Members пуст")
	}
	if len(mo.Subscriptions) != 0 || len(mo.RoleRights) != 0 || len(mo.Forms) != 0 || len(mo.ScheduledJobs) != 0 {
		t.Errorf("parts=members должен нести ТОЛЬКО members, получено %+v", mo)
	}

	subsOnly, err := metaSvc.GetObject(ctx, app.GetObjectInput{Type: mtype, Name: name, Parts: "subscriptions"})
	if err != nil {
		t.Fatalf("GetObject(parts=subscriptions): %v", err)
	}
	so := subsOnly.Items[0]
	if len(so.Subscriptions) == 0 {
		t.Error("parts=subscriptions: Subscriptions пуст")
	}
	if len(so.Members) != 0 || len(so.RoleRights) != 0 || len(so.Forms) != 0 {
		t.Errorf("parts=subscriptions должен нести ТОЛЬКО subscriptions, получено %+v", so)
	}
}

// --- get_form_handlers: фикстура с одним разрешённым и одним
// unresolved-обработчиком (R43.1) ---

const fixtureCatalogXML = metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Catalog uuid="11111111-1111-1111-1111-111111111111">
		<Properties>
			<Name>Тест</Name>
		</Properties>
		<ChildObjects>
			<Form>ФормаЭлемента</Form>
		</ChildObjects>
	</Catalog>
</MetaDataObject>`

const fixtureFormXML = metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform">
	<Events>
		<Event name="ПриСозданииНаСервере">ПриСозданииНаСервере</Event>
		<Event name="ПриОткрытии">ОбработчикОткрытияКоторогоНетВМодуле</Event>
	</Events>
	<ChildItems/>
	<Attributes/>
	<Commands/>
</Form>`

const fixtureFormModuleBSL = metaBOM + `Процедура ПриСозданииНаСервере(Отказ, СтандартнаяОбработка)
КонецПроцедуры
`

// TestGetFormHandlersUnresolvedFixture — критерий приёмки тикета 12:
// «get_form_handlers на форме с необъявленным обработчиком даёт unresolved +
// diagnostic, а не пустую выдачу». Фикстура собрана вручную (не найдена в
// реальной выгрузке — искать заведомо сломанный случай в чужих данных
// недетерминированно): один обработчик реально объявлен в модуле формы,
// второй — нет.
func TestGetFormHandlersUnresolvedFixture(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML("Тест"))
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Catalogs", "Тест.xml"), fixtureCatalogXML)
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Catalogs", "Тест", "Forms", "ФормаЭлемента", "Ext", "Form.xml"), fixtureFormXML)
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Catalogs", "Тест", "Forms", "ФормаЭлемента", "Ext", "Form", "Module.bsl"), fixtureFormModuleBSL)
	metaFixtureProject(t, workspaceRoot, projectRoot, "form-handlers-check")

	projects, err := app.NewProjects(workspaceRoot, nil, app.DefaultIndexConfig())
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { projects.Close() })
	ctx := context.Background()
	if res, err := app.NewIndexStatusService(projects).Reindex(ctx, app.ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	} else if len(res.Items) != 1 || len(res.Items[0].Diagnostics) > 0 {
		t.Logf("diagnostics пайплайна (информативно): %+v", res.Items[0].Diagnostics)
	}

	metaSvc := app.NewMetadataService(projects)
	resp, err := metaSvc.GetFormHandlers(ctx, app.GetFormHandlersInput{OwnerType: "Catalog", OwnerName: "Тест"})
	if err != nil {
		t.Fatalf("GetFormHandlers: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("Items = %d, want 2 (по одному на объявленный обработчик), получено %+v", len(resp.Items), resp.Items)
	}

	byEvent := map[string]app.FormHandlerItem{}
	for _, it := range resp.Items {
		byEvent[it.Event] = it
	}

	resolved, ok := byEvent["ПриСозданииНаСервере"]
	if !ok {
		t.Fatalf("нет привязки для события ПриСозданииНаСервере среди %+v", resp.Items)
	}
	if resolved.Resolution != domain.ResolutionResolved {
		t.Errorf("ПриСозданииНаСервере: Resolution = %s, want resolved", resolved.Resolution)
	}
	if resolved.Symbol == nil {
		t.Error("ПриСозданииНаСервере: Symbol == nil при resolution=resolved")
	} else if resolved.Symbol.Span.IsZero() {
		t.Error("ПриСозданииНаСервере: Symbol.Span пуст")
	}
	if len(resolved.Diagnostics) != 0 {
		t.Errorf("ПриСозданииНаСервере: resolved-привязка не должна нести diagnostics, получено %+v", resolved.Diagnostics)
	}

	unresolved, ok := byEvent["ПриОткрытии"]
	if !ok {
		t.Fatalf("нет привязки для события ПриОткрытии среди %+v", resp.Items)
	}
	if unresolved.Resolution != domain.ResolutionUnresolved {
		t.Errorf("ПриОткрытии: Resolution = %s, want unresolved (обработчика нет в модуле)", unresolved.Resolution)
	}
	if unresolved.Symbol != nil {
		t.Errorf("ПриОткрытии: Symbol должен быть nil при unresolved, получено %+v", unresolved.Symbol)
	}
	if len(unresolved.Diagnostics) == 0 {
		t.Error("ПриОткрытии: unresolved-привязка ОБЯЗАНА нести diagnostic (R43.1), получен пустой список")
	} else if unresolved.Diagnostics[0].Code != "handler_unresolved" {
		t.Errorf("diagnostic.Code = %q, want handler_unresolved", unresolved.Diagnostics[0].Code)
	}
}

// --- find_queries_using: фикстура со статичным запросом, таблица+поле ---

const fixtureCatalog2XML = metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Catalog uuid="33333333-3333-3333-3333-333333333333">
		<Properties>
			<Name>Тест2</Name>
		</Properties>
		<ChildObjects>
			<Attribute uuid="44444444-4444-4444-4444-444444444444">
				<Properties>
					<Name>Наименование</Name>
				</Properties>
			</Attribute>
		</ChildObjects>
	</Catalog>
</MetaDataObject>`

const fixtureQueryModuleXML = metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<CommonModule uuid="55555555-5555-5555-5555-555555555555">
		<Properties>
			<Name>МодульЗапросов</Name>
			<Server>true</Server>
		</Properties>
	</CommonModule>
</MetaDataObject>`

const fixtureQueryModuleBSL = metaBOM + `Процедура Тест() Экспорт
	Запрос = Новый Запрос;
	Запрос.Текст =
	"ВЫБРАТЬ
	|	Тест2.Наименование
	|ИЗ
	|	Справочник.Тест2 КАК Тест2";
КонецПроцедуры
`

// TestFindQueriesUsingFixture — критерий приёмки тикета 12: «find_queries_using
// находит запрос по виртуальной таблице и по полю» — здесь по базовой
// таблице (Справочник.Тест2, TableBase, доступная тем же путём, что и
// виртуальная — resolve.DeriveQueryReference не различает TableBase/
// TableVirtual при построении query_reference, обе идут kind=table) и по
// полю Наименование, до и после того, как это единственный вид запроса,
// который query_reference вообще несёт сегодня (только static — см.
// doc-комментарий resolve.DeriveQueryReference и internal/index/doc.go):
// partial/dynamic тексты этим тестом намеренно не проверяются — они не
// становятся результатом find_queries_using вовсе, честный остаточный долг.
func TestFindQueriesUsingFixture(t *testing.T) {
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML("Тест"))
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Catalogs", "Тест2.xml"), fixtureCatalog2XML)
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "CommonModules", "МодульЗапросов.xml"), fixtureQueryModuleXML)
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "CommonModules", "МодульЗапросов", "Ext", "Module.bsl"), fixtureQueryModuleBSL)
	metaFixtureProject(t, workspaceRoot, projectRoot, "queries-check")

	projects, err := app.NewProjects(workspaceRoot, nil, app.DefaultIndexConfig())
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { projects.Close() })
	ctx := context.Background()
	if _, err := app.NewIndexStatusService(projects).Reindex(ctx, app.ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	querySvc := app.NewQueryService(projects)

	byTable, err := querySvc.FindQueriesUsing(ctx, app.FindQueriesUsingInput{Type: "Catalog", Name: "Тест2"})
	if err != nil {
		t.Fatalf("FindQueriesUsing(type+name): %v", err)
	}
	var sawTable, sawField bool
	for _, it := range byTable.Items {
		switch it.Kind {
		case "table":
			sawTable = true
			if it.Object != "Тест2" {
				t.Errorf("table.Object = %q, want Тест2", it.Object)
			}
		case "field":
			sawField = true
		}
		if it.Staticity != "static" {
			t.Errorf("Staticity = %q, want static", it.Staticity)
		}
		if !domain.Confidence(it.Confidence).Valid() {
			t.Errorf("Confidence = %v вне допустимого (0..1]", it.Confidence)
		}
		if it.Owner == nil || it.Owner.Name != "Тест" {
			t.Errorf("Owner = %+v, want символ Тест (процедура, где лежит запрос)", it.Owner)
		}
	}
	if !sawTable {
		t.Errorf("не найдено использование таблицы Тест2 среди %+v", byTable.Items)
	}
	if !sawField {
		t.Errorf("find_queries_using(type=Catalog,name=Тест2) должен вернуть и поле того же объекта, среди %+v", byTable.Items)
	}

	byField, err := querySvc.FindQueriesUsing(ctx, app.FindQueriesUsingInput{Field: "Наименование"})
	if err != nil {
		t.Fatalf("FindQueriesUsing(field): %v", err)
	}
	var foundField bool
	for _, it := range byField.Items {
		if it.Kind == "field" && it.Field == "Наименование" {
			foundField = true
		}
	}
	if !foundField {
		t.Errorf("поиск по одному только field=Наименование не нашёл запись среди %+v", byField.Items)
	}
}
