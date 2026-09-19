package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Фикстура вехи В2: две базы в одном workspace. «shop» вызывает HTTP-сервис
// «erp» из общего модуля, который зовёт документ; имена вымышленные.

func httpServiceXML(name, root string, templates string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<HTTPService uuid="c09df096-f9cc-4b2f-a44e-69147339dc8c">
		<Properties>
			<Name>` + name + `</Name>
			<RootURL>` + root + `</RootURL>
		</Properties>
		<ChildObjects>` + templates + `
		</ChildObjects>
	</HTTPService>
</MetaDataObject>`
}

func httpTemplateXML(name, template, method, verb, handler string) string {
	return `
			<URLTemplate uuid="aa4e4f39-3080-4365-89aa-00b030e26eec">
				<Properties><Name>` + name + `</Name><Template>` + template + `</Template></Properties>
				<ChildObjects>
					<Method uuid="84c1ba1c-e5d6-46ec-98a3-589499162e67">
						<Properties><Name>` + method + `</Name><HTTPMethod>` + verb + `</HTTPMethod><Handler>` + handler + `</Handler></Properties>
					</Method>
				</ChildObjects>
			</URLTemplate>`
}

func commonModuleServerXML(name string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<CommonModule uuid="11111111-2222-3333-4444-555555555555">
		<Properties>
			<Name>` + name + `</Name>
			<Server>true</Server>
		</Properties>
	</CommonModule>
</MetaDataObject>`
}

func documentXML(name string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<Document uuid="99999999-8888-7777-6666-555555555555">
		<Properties><Name>` + name + `</Name></Properties>
	</Document>
</MetaDataObject>`
}

const shopExchangeModule = `
Процедура ОтправитьЗаказ(Номер) Экспорт
	Соединение = Новый HTTPСоединение("erp.example.local", 443);
	Запрос = Новый HTTPЗапрос("/erp/hs/exchange/v1/orders/" + Номер);
	Ответ = Соединение.ОтправитьДляОбработки(Запрос);
КонецПроцедуры

Процедура ОтправитьПоНастройке(Настройки) Экспорт
	Соединение = Новый HTTPСоединение(Настройки.Сервер);
	Запрос = Новый HTTPЗапрос(Настройки.Путь);
	Ответ = Соединение.Получить(Запрос);
КонецПроцедуры

Процедура ПроверитьКурсы() Экспорт
	Соединение = Новый HTTPСоединение("api.partner.example");
	Ответ = Соединение.Получить(Новый HTTPЗапрос("/erp/hs/exchange/version"));
КонецПроцедуры
`

const shopOrderObjectModule = `
Процедура ОбработкаПроведения(Отказ, Режим)
	ОбменСЕРП.ОтправитьЗаказ(Номер);
	ОбменСЕРП.ОтправитьПоНастройке(Неопределено);
КонецПроцедуры
`

// newHTTPWorkspace регистрирует проекты в одном workspace, индексирует их
// и кладёт маппинг хостов (пусто: файла нет).
func newHTTPWorkspace(t *testing.T, hostsJSON string, projects map[domain.ProjectID]map[string]string) (*Projects, map[domain.ProjectID]string) {
	t.Helper()
	workspaceRoot := t.TempDir()
	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	roots := map[domain.ProjectID]string{}
	for id, files := range projects {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "cfg", "Configuration.xml"), конфигурацияXML(string(id)))
		for rel, content := range files {
			writeFile(t, filepath.Join(root, "cfg", filepath.FromSlash(rel)), content)
		}
		manifest, _ := json.Marshal(map[string]any{"version": 1, "project": string(id),
			"components": []map[string]any{{"id": "cfg", "kind": "configuration", "root": "cfg"}}})
		writeFile(t, filepath.Join(root, workspace.ManifestFileName), string(manifest))
		if err := reg.Upsert(workspace.ProjectEntry{ID: id, Root: root}); err != nil {
			t.Fatal(err)
		}
		roots[id] = root
	}
	if hostsJSON != "" {
		writeFile(t, workspace.HTTPHostsPath(workspaceRoot), hostsJSON)
	}
	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	ctx := context.Background()
	for id, root := range roots {
		op, err := p.ByRoot(ctx, root)
		if err != nil {
			t.Fatalf("ByRoot %s: %v", id, err)
		}
		if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
			t.Fatalf("Reindex %s: %v", id, err)
		}
	}
	return p, roots
}

func twoBases() map[domain.ProjectID]map[string]string {
	return map[domain.ProjectID]map[string]string{
		"shop": {
			workspace.DumpDeclarationPath("CommonModule", "ОбменСЕРП"):                    commonModuleServerXML("ОбменСЕРП"),
			workspace.DumpModulePath("CommonModule", "ОбменСЕРП", workspace.ModuleCommon): shopExchangeModule,
			workspace.DumpDeclarationPath("Document", "ЗаказКлиента"):                     documentXML("ЗаказКлиента"),
			workspace.DumpModulePath("Document", "ЗаказКлиента", workspace.ModuleObject):  shopOrderObjectModule,
		},
		"erp": {
			workspace.DumpDeclarationPath("HTTPService", "ОбменЗаказами"): httpServiceXML("ОбменЗаказами", "exchange",
				httpTemplateXML("Заказ", "/v1/orders/{Номер}", "post", "POST", "ЗаказPost")+
					httpTemplateXML("Версия", "/version", "get", "GET", "ВерсияGet")),
			workspace.DumpModulePath("HTTPService", "ОбменЗаказами", workspace.ModuleCommon): "Функция ЗаказPost(Запрос)\nКонецФункции\n",
		},
	}
}

const erpHostsJSON = `{"version":1,"hosts":[{"host":"erp.example.local","project":"erp"}]}`

func crossLinks(t *testing.T, p *Projects, roots map[domain.ProjectID]string) CrossLinksItem {
	t.Helper()
	p.activateInProcess(mustEntry(t, p, "shop"), nil)
	svc := NewObjectGraphService(p, 0)
	resp, err := svc.CrossLinks(context.Background(), CrossLinksInput{ProjectRoots: []string{roots["erp"]}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items %d", len(resp.Items))
	}
	return resp.Items[0]
}

func mustEntry(t *testing.T, p *Projects, id domain.ProjectID) workspace.ProjectEntry {
	t.Helper()
	e, ok := p.registry.Project(id)
	if !ok {
		t.Fatalf("проекта %s нет в реестре", id)
	}
	return e
}

func linkByTo(item CrossLinksItem, toType string) *HTTPLink {
	for i, l := range item.Links {
		if l.To != nil && l.To.Type == toType {
			return &item.Links[i]
		}
	}
	return nil
}

// TestCrossLinksStitchByPath (критерий 1 issue #6): вызов из общего модуля
// через мапленный хост и путь сшивается с обработчиком сервиса другой базы,
// концом ребра стал документ, который зовёт общий модуль (D6).
func TestCrossLinksStitchByPath(t *testing.T) {
	p, roots := newHTTPWorkspace(t, erpHostsJSON, twoBases())
	item := crossLinks(t, p, roots)
	l := linkByTo(item, "HTTPService")
	if l == nil {
		t.Fatalf("ребра к HTTP-сервису нет: %+v", item.Links)
	}
	if l.Kind != EdgeHTTPCall || l.From.Project != "shop" || l.From.Type != "Document" || l.From.Name != "ЗаказКлиента" {
		t.Errorf("начало ребра %+v, ожидался документ ЗаказКлиента базы shop", l.From)
	}
	if l.To.Project != "erp" || l.To.Name != "ОбменЗаказами" {
		t.Errorf("конец ребра %+v, ожидался сервис ОбменЗаказами базы erp", l.To)
	}
	if len(l.Endpoints) != 1 || l.Endpoints[0].Handler != "ЗаказPost" || l.Endpoints[0].Template != "/v1/orders/{Номер}" {
		t.Errorf("обработчик: %+v, ожидался ЗаказPost шаблона /v1/orders/{Номер}", l.Endpoints)
	}
	if l.Confidence <= 0 || l.Confidence >= 1 {
		t.Errorf("confidence %v: эвристика, строго между 0 и 1", l.Confidence)
	}
	if len(l.Calls) != 1 || !l.Calls[0].Attributed || !strings.HasSuffix(l.Calls[0].File, "Module.bsl") || l.Calls[0].Line == 0 {
		t.Errorf("место вызова: %+v", l.Calls)
	}
}

// TestCrossLinksUnknownHostIsExternal (критерий 1): литеральный хост без
// маппинга виден как внешний, даже если путь совпал бы с сервисом.
func TestCrossLinksUnknownHostIsExternal(t *testing.T) {
	p, roots := newHTTPWorkspace(t, erpHostsJSON, twoBases())
	item := crossLinks(t, p, roots)
	var ext *HTTPLink
	for i, l := range item.Links {
		if l.External {
			ext = &item.Links[i]
		}
	}
	if ext == nil {
		t.Fatalf("внешней связи нет: %+v", item.Links)
	}
	if ext.To != nil || ext.ExternalHost != "api.partner.example" || ext.Reason != "host-unmapped" {
		t.Errorf("внешняя связь: %+v", ext)
	}
	// Вызов без вызывающих: концом стал сам общий модуль.
	if ext.From.Type != "CommonModule" || ext.From.Name != "ОбменСЕРП" || ext.Calls[0].Attributed {
		t.Errorf("начало внешней связи %+v, место %+v", ext.From, ext.Calls)
	}
	if len(item.Links) != 2 {
		t.Errorf("связей %d, ожидалось 2 (сервис и внешний хост): %+v", len(item.Links), item.Links)
	}
}

// TestCrossLinksWithoutHostMappingAllExternal: без файла маппинга тот же
// вызов с литеральным хостом внешний (мутация «сшивать немапленный хост по
// пути» красит этот тест).
func TestCrossLinksWithoutHostMappingAllExternal(t *testing.T) {
	p, roots := newHTTPWorkspace(t, "", twoBases())
	item := crossLinks(t, p, roots)
	if l := linkByTo(item, "HTTPService"); l != nil {
		t.Fatalf("без маппинга хоста ребра к сервису быть не должно: %+v", l)
	}
	for _, l := range item.Links {
		if !l.External || l.Reason != "host-unmapped" {
			t.Errorf("связь %+v: ожидалась внешняя host-unmapped", l)
		}
	}
}

// TestCrossLinksDynamicURLGivesBadgeNotEdge (критерий 4): путь из поля
// структуры ребра не даёт, у документа бейдж has-dynamic-http.
func TestCrossLinksDynamicURLGivesBadgeNotEdge(t *testing.T) {
	p, roots := newHTTPWorkspace(t, erpHostsJSON, twoBases())
	item := crossLinks(t, p, roots)
	if len(item.Badges) != 1 {
		t.Fatalf("бейджей %d, ожидался 1: %+v", len(item.Badges), item.Badges)
	}
	b := item.Badges[0]
	if b.Badge != "has-dynamic-http" || b.Count != 1 || b.Node.Name != "ЗаказКлиента" || b.Calls[0].PathKind != "dynamic" {
		t.Errorf("бейдж %+v", b)
	}
	for _, l := range item.Links {
		for _, c := range l.Calls {
			if c.PathKind == "dynamic" {
				t.Errorf("динамический вызов дал ребро: %+v", l)
			}
		}
	}
}

// TestObjectHTTPLinksBothEnds: object_graph видит связь и со стороны
// вызывающего документа, и со стороны сервиса.
func TestObjectHTTPLinksBothEnds(t *testing.T) {
	p, roots := newHTTPWorkspace(t, erpHostsJSON, twoBases())
	svc := NewObjectGraphService(p, 0)
	ctx := context.Background()
	from, err := svc.ObjectHTTPLinks(ctx, ObjectHTTPLinksInput{
		Target:      ObjectTarget{ObjectType: "Document", ObjectName: "ЗаказКлиента"},
		ProjectRoot: roots["shop"], CrossProjectRoots: []string{roots["erp"]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := from.Items[0]; len(got.Links) != 1 || got.Links[0].To == nil || len(got.Badges) != 1 {
		t.Errorf("со стороны документа: %+v", got)
	}
	to, err := svc.ObjectHTTPLinks(ctx, ObjectHTTPLinksInput{
		Target:      ObjectTarget{ObjectType: "HTTPService", ObjectName: "ОбменЗаказами"},
		ProjectRoot: roots["erp"], CrossProjectRoots: []string{roots["shop"]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := to.Items[0]; len(got.Links) != 1 || got.Links[0].From.Name != "ЗаказКлиента" {
		t.Errorf("со стороны сервиса: %+v", got)
	}
}

// Фикстура шума бейджа (п.4 ревью) и потерянных вызовов (п.5): вызов с
// вычисляемым адресом в общем модуле зовут документ напрямую и три документа
// через процедуру-хаб; вторая процедура получает соединение параметром.
const noiseExchangeModule = `
Процедура ОтправитьПоНастройке(Настройки) Экспорт
	Соединение = Новый HTTPСоединение(Настройки.Сервер);
	Ответ = Соединение.Получить(Новый HTTPЗапрос(Настройки.Путь));
КонецПроцедуры

Процедура ОтправитьЧерез(Соединение, Запрос) Экспорт
	Ответ = Соединение.ОтправитьДляОбработки(Запрос);
КонецПроцедуры
`

const noiseHubModule = `
Процедура Отправить() Экспорт
	ОбменСЕРП.ОтправитьПоНастройке(Неопределено);
КонецПроцедуры
`

func noiseDocModule(viaHub bool) string {
	if viaHub {
		return "\nПроцедура ОбработкаПроведения(Отказ, Режим)\n\tХабОбмена.Отправить();\nКонецПроцедуры\n"
	}
	return "\nПроцедура ОбработкаПроведения(Отказ, Режим)\n\tОбменСЕРП.ОтправитьПоНастройке(Неопределено);\n\tОбменСЕРП.ОтправитьЧерез(Неопределено, Неопределено);\nКонецПроцедуры\n"
}

func noiseBase() map[domain.ProjectID]map[string]string {
	files := map[string]string{
		workspace.DumpDeclarationPath("CommonModule", "ОбменСЕРП"):                    commonModuleServerXML("ОбменСЕРП"),
		workspace.DumpModulePath("CommonModule", "ОбменСЕРП", workspace.ModuleCommon): noiseExchangeModule,
		workspace.DumpDeclarationPath("CommonModule", "ХабОбмена"):                    strings.ReplaceAll(commonModuleServerXML("ХабОбмена"), "555555555555", "555555555556"),
		workspace.DumpModulePath("CommonModule", "ХабОбмена", workspace.ModuleCommon): noiseHubModule,
		workspace.DumpDeclarationPath("Document", "ЗаказКлиента"):                     documentXML("ЗаказКлиента"),
		workspace.DumpModulePath("Document", "ЗаказКлиента", workspace.ModuleObject):  noiseDocModule(false),
	}
	for _, name := range []string{"Возврат", "Поступление", "Перемещение"} {
		files[workspace.DumpDeclarationPath("Document", name)] = strings.ReplaceAll(documentXML(name), "555555555555", "55555555555"+string(rune('0'+len(name)%10)))
		files[workspace.DumpModulePath("Document", name, workspace.ModuleObject)] = noiseDocModule(true)
	}
	return map[domain.ProjectID]map[string]string{"shop": files}
}

func badgesByNode(item CrossLinksItem) map[string]HTTPBadge {
	out := map[string]HTTPBadge{}
	for _, b := range item.Badges {
		out[b.Node.Type+"."+b.Node.Name+"/"+b.Badge] = b
	}
	return out
}

// TestDynamicBadgeOnlyOnDirectOwners (п.4): бейдж динамического вызова
// получает документ, зовущий общий модуль напрямую; документы за хабом его
// не получают, вместо них бейдж у модуля вызова.
func TestDynamicBadgeOnlyOnDirectOwners(t *testing.T) {
	ConfigureGraphTunables(0, 2, 0, 0) // хаб: больше двух вызывающих
	t.Cleanup(func() { ConfigureGraphTunables(0, 0, 0, 0) })
	p, _ := newHTTPWorkspace(t, "", noiseBase())
	p.activateInProcess(mustEntry(t, p, "shop"), nil)
	resp, err := NewObjectGraphService(p, 0).CrossLinks(context.Background(), CrossLinksInput{})
	if err != nil {
		t.Fatal(err)
	}
	got := badgesByNode(resp.Items[0])
	direct, ok := got["Document.ЗаказКлиента/has-dynamic-http"]
	if !ok || direct.Reasons["dynamic-path"] != 1 || direct.Confidence <= 0 || direct.Confidence >= 1 {
		t.Errorf("прямой владелец: %+v (все: %+v)", direct, got)
	}
	for _, name := range []string{"Возврат", "Поступление", "Перемещение"} {
		if b, ok := got["Document."+name+"/has-dynamic-http"]; ok {
			t.Errorf("документ за хабом получил бейдж: %+v", b)
		}
	}
	module, ok := got["CommonModule.ОбменСЕРП/has-dynamic-http"]
	if !ok || module.Reasons["dynamic-path"] != 1 {
		t.Errorf("бейдж модуля вызова за хабом: %+v (все: %+v)", module, got)
	}
}

// TestConnectionFromParameterBadge (п.5): вызов, где соединение и запрос
// пришли параметрами, не пропадает: бейдж на модуле вызова с причиной
// connection-from-parameter, даже когда у процедуры есть вызывающий документ.
func TestConnectionFromParameterBadge(t *testing.T) {
	p, _ := newHTTPWorkspace(t, "", noiseBase())
	p.activateInProcess(mustEntry(t, p, "shop"), nil)
	resp, err := NewObjectGraphService(p, 0).CrossLinks(context.Background(), CrossLinksInput{})
	if err != nil {
		t.Fatal(err)
	}
	got := badgesByNode(resp.Items[0])
	module, ok := got["CommonModule.ОбменСЕРП/has-dynamic-http"]
	if !ok || module.Reasons["connection-from-parameter"] != 1 {
		t.Fatalf("бейдж модуля с причиной connection-from-parameter: %+v (все: %+v)", module, got)
	}
	if d := got["Document.ЗаказКлиента/has-dynamic-http"]; d.Reasons["connection-from-parameter"] != 0 {
		t.Errorf("соединение из параметра не приписывается вызывающим: %+v", d)
	}
}

// TestHTTPFactsCachedByGeneration (п.7): повторное чтение того же поколения
// берёт факты из кэша, новое поколение строит их заново.
func TestHTTPFactsCachedByGeneration(t *testing.T) {
	p, roots := newHTTPWorkspace(t, erpHostsJSON, twoBases())
	svc := NewObjectGraphService(p, 0)
	p.activateInProcess(mustEntry(t, p, "shop"), nil)
	ctx := context.Background()
	before := httpFactsBuilds.Load()
	if _, err := svc.CrossLinks(ctx, CrossLinksInput{ProjectRoots: []string{roots["erp"]}}); err != nil {
		t.Fatal(err)
	}
	if got := httpFactsBuilds.Load() - before; got != 2 {
		t.Fatalf("первое чтение: построений %d, ожидалось 2 (по проекту)", got)
	}
	if _, err := svc.CrossLinks(ctx, CrossLinksInput{ProjectRoots: []string{roots["erp"]}}); err != nil {
		t.Fatal(err)
	}
	if got := httpFactsBuilds.Load() - before; got != 2 {
		t.Fatalf("повторное чтение того же поколения: построений %d, ожидалось 2", got)
	}
	op, err := p.ByRoot(ctx, roots["shop"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Service.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CrossLinks(ctx, CrossLinksInput{ProjectRoots: []string{roots["erp"]}}); err != nil {
		t.Fatal(err)
	}
	if got := httpFactsBuilds.Load() - before; got != 3 {
		t.Fatalf("после пересборки shop: построений %d, ожидалось 3", got)
	}
}

// TestRadiusHTTPOnlyKindsWarns (п.6): kinds только из http-call не дают
// молчаливой пустоты радиуса.
func TestRadiusHTTPOnlyKindsWarns(t *testing.T) {
	p, roots := newHTTPWorkspace(t, "", noiseBase())
	resp, err := NewObjectGraphService(p, 0).Radius(context.Background(), RadiusInput{
		Target:      ObjectTarget{ObjectType: "Document", ObjectName: "ЗаказКлиента"},
		Kinds:       []string{EdgeHTTPCall},
		ProjectRoot: roots["shop"],
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range resp.Warnings {
		found = found || w.Code == "http_call_not_in_radius"
	}
	if !found {
		t.Errorf("нет предупреждения http_call_not_in_radius: %+v", resp.Warnings)
	}
}
