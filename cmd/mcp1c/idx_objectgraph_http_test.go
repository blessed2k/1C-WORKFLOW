package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Milestone V2 (issue #6) over the real MCP client: object_graph with
// crossProjects stitches an HTTP call of one base with the HTTP service of
// another. Fixture names are made up.

const ogHTTPCallerModule = `
Процедура ОтправитьЗаказ(Номер) Экспорт
	Соединение = Новый HTTPСоединение("erp.example.local");
	Запрос = Новый HTTPЗапрос("/erp/hs/exchange/v1/orders/" + Номер);
	Ответ = Соединение.ОтправитьДляОбработки(Запрос);
КонецПроцедуры
`

const ogHTTPService = `<?xml version="1.0" encoding="UTF-8"?>
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

const ogHTTPCommonModule = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<CommonModule uuid="11111111-2222-3333-4444-555555555555">
		<Properties><Name>ОбменСЕРП</Name><Server>true</Server></Properties>
	</CommonModule>
</MetaDataObject>`

type ogHTTPOutput struct {
	app.Response[app.RadiusEdgeItem]
	HTTPLinks *app.CrossLinksItem `json:"httpLinks"`
}

func TestObjectGraphCrossProjectsHTTPLinks(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	rootShop, rootERP := t.TempDir(), t.TempDir()

	ogManifest(t, rootShop, "og-shop")
	metaWriteFile(t, filepath.Join(rootShop, "cfg", "Configuration.xml"), metaConfigurationXML("Shop"))
	metaWriteFile(t, filepath.Join(rootShop, "cfg", filepath.FromSlash(workspace.DumpDeclarationPath("CommonModule", "ОбменСЕРП"))), ogHTTPCommonModule)
	metaWriteFile(t, filepath.Join(rootShop, "cfg", filepath.FromSlash(workspace.DumpModulePath("CommonModule", "ОбменСЕРП", workspace.ModuleCommon))), ogHTTPCallerModule)

	ogManifest(t, rootERP, "og-erp")
	metaWriteFile(t, filepath.Join(rootERP, "cfg", "Configuration.xml"), metaConfigurationXML("ERP"))
	metaWriteFile(t, filepath.Join(rootERP, "cfg", filepath.FromSlash(workspace.DumpDeclarationPath("HTTPService", "ОбменЗаказами"))), ogHTTPService)

	metaWriteFile(t, workspace.HTTPHostsPath(workspaceRoot),
		`{"version":1,"hosts":[{"host":"erp.example.local","project":"og-erp"}]}`)

	cs := ogConnect(t, ctx, workspaceRoot)
	ogReindex(t, ctx, cs, rootERP)
	ogReindex(t, ctx, cs, rootShop)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "object_graph", Arguments: map[string]any{
		"objectType": "CommonModule", "objectName": "ОбменСЕРП", "crossProjects": []string{rootERP},
	}})
	if err != nil {
		t.Fatalf("object_graph: %v", err)
	}
	if res.IsError {
		t.Fatalf("object_graph вернул ошибку: %s", contentText(res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out ogHTTPOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, raw)
	}
	if out.Items == nil {
		t.Errorf("items обязаны остаться списком: %s", raw)
	}
	if out.HTTPLinks == nil || len(out.HTTPLinks.Links) != 1 {
		t.Fatalf("httpLinks: %s", raw)
	}
	l := out.HTTPLinks.Links[0]
	if l.Kind != "http-call" || l.To == nil || l.To.Project != "og-erp" || l.To.Name != "ОбменЗаказами" ||
		len(l.Endpoints) != 1 || l.Endpoints[0].Handler != "ЗаказPost" {
		t.Errorf("ребро http-call: %+v", l)
	}

	// Без crossProjects и без kinds=http-call блока нет: прежний ответ.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "object_graph", Arguments: map[string]any{
		"objectType": "CommonModule", "objectName": "ОбменСЕРП",
	}})
	if err != nil || res.IsError {
		t.Fatalf("object_graph без crossProjects: %v %s", err, contentText(res))
	}
	raw, _ = json.Marshal(res.StructuredContent)
	var plain map[string]any
	_ = json.Unmarshal(raw, &plain)
	if _, has := plain["httpLinks"]; has {
		t.Errorf("httpLinks без запроса: %s", raw)
	}
}
