package graphweb_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Веха В2 (issue #6): карта с двумя --project отдаёт кросс-базовые рёбра
// http-call через GET /api/crosslinks. Имена в фикстуре вымышленные.

const clCallerModule = `
Процедура ОтправитьЗаказ(Номер) Экспорт
	Соединение = Новый HTTPСоединение("erp.example.local");
	Запрос = Новый HTTPЗапрос("/erp/hs/exchange/v1/orders/" + Номер);
	Ответ = Соединение.ОтправитьДляОбработки(Запрос);
КонецПроцедуры

Процедура ОтправитьПоНастройке(Настройки) Экспорт
	Соединение = Новый HTTPСоединение(Настройки.Сервер);
	Ответ = Соединение.Получить(Новый HTTPЗапрос(Настройки.Путь));
КонецПроцедуры
`

const clCommonModule = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<CommonModule uuid="11111111-2222-3333-4444-555555555555">
		<Properties><Name>ОбменСЕРП</Name><Server>true</Server></Properties>
	</CommonModule>
</MetaDataObject>`

const clService = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<HTTPService uuid="c09df096-f9cc-4b2f-a44e-69147339dc8c">
		<Properties><Name>ОбменЗаказами</Name><RootURL>exchange</RootURL></Properties>
		<ChildObjects>
			<URLTemplate uuid="aa4e4f39-3080-4365-89aa-00b030e26eec">
				<Properties><Name>Заказ</Name><Template>/v1/orders/{Номер}</Template></Properties>
				<ChildObjects>
					<Method uuid="84c1ba1c-e5d6-46ec-98a3-589499162e67">
						<Properties><Name>post</Name><HTTPMethod>POST</HTTPMethod><Handler>ЗаказPost</Handler></Properties>
					</Method>
				</ChildObjects>
			</URLTemplate>
		</ChildObjects>
	</HTTPService>
</MetaDataObject>`

// newIndexedProject — отдельный workspace с одним проектом из files,
// полностью проиндексированный через app (тот же путь, что reindex).
func newIndexedProject(t *testing.T, id domain.ProjectID, files map[string]string, hostsJSON string) graphweb.ProjectHandle {
	t.Helper()
	workspaceRoot, projectRoot := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML(string(id)))
	for rel, content := range files {
		writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(rel)), content)
	}
	manifest, _ := json.Marshal(map[string]any{"version": 1, "project": string(id),
		"components": []map[string]any{{"id": "cfg", "kind": "configuration", "root": "cfg"}}})
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(manifest))
	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: id, Root: projectRoot}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetActiveProject(id); err != nil {
		t.Fatal(err)
	}
	if hostsJSON != "" {
		writeFile(t, workspace.HTTPHostsPath(workspaceRoot), hostsJSON)
	}
	open := func(ctx context.Context) (*app.Projects, error) {
		return app.NewProjects(workspaceRoot, nil, app.DefaultIndexConfig())
	}
	p, err := open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.NewIndexStatusService(p).Reindex(context.Background(), app.ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("reindex %s: %v", id, err)
	}
	p.Close()
	return graphweb.ProjectHandle{ID: id, Root: workspaceRoot, Open: open}
}

func twoProjectHandler(t *testing.T, hostsJSON string) http.Handler {
	t.Helper()
	shop := newIndexedProject(t, "shop", map[string]string{
		workspace.DumpDeclarationPath("CommonModule", "ОбменСЕРП"):                    clCommonModule,
		workspace.DumpModulePath("CommonModule", "ОбменСЕРП", workspace.ModuleCommon): clCallerModule,
	}, hostsJSON)
	erp := newIndexedProject(t, "erp", map[string]string{
		workspace.DumpDeclarationPath("HTTPService", "ОбменЗаказами"): clService,
	}, "")
	return graphweb.NewHandler([]graphweb.ProjectHandle{shop, erp})
}

type clItem struct {
	Projects []string        `json:"projects"`
	Links    []app.HTTPLink  `json:"links"`
	Badges   []app.HTTPBadge `json:"badges"`
}

func decodeCrossLinks(t *testing.T, env envelope) clItem {
	t.Helper()
	if len(env.Items) != 1 {
		t.Fatalf("items %d", len(env.Items))
	}
	var it clItem
	if err := json.Unmarshal(env.Items[0], &it); err != nil {
		t.Fatal(err)
	}
	return it
}

// TestHandlerCrossLinksTwoProjects (критерий 3): карта с двумя --project
// отдаёт ребро http-call к сервису другой базы и бейдж динамического вызова.
func TestHandlerCrossLinksTwoProjects(t *testing.T) {
	h := twoProjectHandler(t, `{"version":1,"hosts":[{"host":"erp.example.local","project":"erp"}]}`)
	rr, env := doGET(t, h, "/api/crosslinks?projects=shop,erp")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	it := decodeCrossLinks(t, env)
	if len(it.Links) != 1 {
		t.Fatalf("связей %d: %+v", len(it.Links), it.Links)
	}
	l := it.Links[0]
	if l.Kind != "http-call" || l.From.Project != "shop" || l.To == nil || l.To.Project != "erp" ||
		l.To.Type != "HTTPService" || len(l.Endpoints) != 1 || l.Endpoints[0].Handler != "ЗаказPost" {
		t.Errorf("ребро: %+v", l)
	}
	if len(it.Badges) != 1 || it.Badges[0].Badge != "has-dynamic-http" {
		t.Errorf("бейджи: %+v", it.Badges)
	}
	// Без projects= берутся все открытые проекты: тот же ответ.
	_, env = doGET(t, h, "/api/crosslinks")
	if all := decodeCrossLinks(t, env); len(all.Links) != 1 || len(all.Projects) != 2 {
		t.Errorf("без projects=: %+v", all)
	}
	// Только вызывающий проект: сервис не загружен, связь внешняя с причиной.
	_, env = doGET(t, h, "/api/crosslinks?projects=shop")
	if only := decodeCrossLinks(t, env); len(only.Links) != 1 || !only.Links[0].External || only.Links[0].Reason != "project-not-loaded" {
		t.Errorf("один проект: %+v", only.Links)
	}
}

func TestHandlerCrossLinksUnknownProject(t *testing.T) {
	h := twoProjectHandler(t, "")
	rr, aerr := doGETErr(t, h, "/api/crosslinks?projects=shop,nope")
	if rr.Code != http.StatusBadRequest || aerr.Code != "unknown_project" {
		t.Errorf("status %d, %+v", rr.Code, aerr)
	}
}

func TestHandlerCrossLinksBrokenHostsFileIsWarning(t *testing.T) {
	h := twoProjectHandler(t, `{"version":7}`)
	rr, env := doGET(t, h, "/api/crosslinks")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if !hasWarningCode(t, env.Warnings, "http_hosts_invalid") {
		t.Errorf("сломанный маппинг обязан дать предупреждение: %s", rr.Body.String())
	}
}
