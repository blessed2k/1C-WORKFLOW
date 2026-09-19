package graphweb_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Режимы карты «до и после расширений» (веха В3) на HTTP-шве: параметр view
// у /api/node, /api/neighbors и /api/godnodes. Фикстура: расширение
// перехватывает проведение документа (&После) и пишет в регистр, которого
// база не пишет; индекс строит настоящий reindex через app, как это делает
// MCP-инструмент.

func viewRegisterXML(name, uuid string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
  <AccumulationRegister uuid="` + uuid + `">
    <Properties>
      <Name>` + name + `</Name>
    </Properties>
  </AccumulationRegister>
</MetaDataObject>`
}

const viewDocumentXML = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
  <Document uuid="88888888-8888-8888-8888-888888888888">
    <Properties>
      <Name>Отгрузка</Name>
    </Properties>
  </Document>
</MetaDataObject>`

func расширениеXML(name string) string {
	return bom + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0003">
		<Properties>
			<Name>` + name + `</Name>
			<ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`
}

// newInterceptProject пишет на диск проект cfg+ext, индексирует его и отдаёт
// ProjectHandle для graphweb.NewHandler.
func newInterceptProject(t *testing.T) graphweb.ProjectHandle {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()

	common := map[string]string{
		workspace.DumpDeclarationPath("Document", "Отгрузка"):                    viewDocumentXML,
		workspace.DumpDeclarationPath("AccumulationRegister", "ТоварыНаСкладах"): viewRegisterXML("ТоварыНаСкладах", "aaaaaaaa-0000-0000-0000-000000000001"),
		workspace.DumpDeclarationPath("AccumulationRegister", "ТоварыВРезерве"):  viewRegisterXML("ТоварыВРезерве", "aaaaaaaa-0000-0000-0000-000000000002"),
	}
	module := filepath.FromSlash(workspace.DumpModulePath("Document", "Отгрузка", workspace.ModuleObject))
	for _, comp := range []string{"cfg", "ext"} {
		for rel, content := range common {
			writeFile(t, filepath.Join(projectRoot, comp, filepath.FromSlash(rel)), content)
		}
	}
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))
	writeFile(t, filepath.Join(projectRoot, "ext", "Configuration.xml"), расширениеXML("РасширениеСклада"))
	writeFile(t, filepath.Join(projectRoot, "cfg", module),
		"\nПроцедура ОбработкаПроведения(Отказ, Режим)\n\tДвижения.ТоварыНаСкладах.Записать();\nКонецПроцедуры\n")
	writeFile(t, filepath.Join(projectRoot, "ext", module),
		"\n&После(\"ОбработкаПроведения\")\nПроцедура Расш_ОбработкаПроведения(Отказ, Режим)\n\tДвижения.ТоварыВРезерве.Записать();\nКонецПроцедуры\n")

	data, err := json.Marshal(map[string]any{
		"version": 1, "project": "intercept",
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
			{"id": "ext", "kind": "extension", "root": "ext", "appliesTo": "cfg", "applyOrder": 1},
		},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: "intercept", Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject("intercept"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	open := func(ctx context.Context) (*app.Projects, error) {
		return app.NewProjects(workspaceRoot, nil, app.DefaultIndexConfig())
	}
	p, err := open(context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := app.NewIndexStatusService(p).Reindex(context.Background(), app.ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	p.Close()
	return graphweb.ProjectHandle{ID: domain.ProjectID("intercept"), Root: workspaceRoot, Open: open}
}

type viewEdge struct {
	ID           int64  `json:"id"`
	FromObjectID int64  `json:"fromObjectId"`
	FromDisplay  string `json:"fromDisplay"`
	ToObjectID   int64  `json:"toObjectId"`
	ToDisplay    string `json:"toDisplay"`
	Layer        string `json:"layer"`
	Diff         string `json:"diff"`
}

func decodeViewEdges(t *testing.T, env envelope) []string {
	t.Helper()
	out := make([]string, 0, len(env.Items))
	for _, raw := range env.Items {
		var e viewEdge
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("decode edge: %v", err)
		}
		out = append(out, e.FromDisplay+"->"+e.ToDisplay+"|"+e.Layer+"|"+e.Diff)
	}
	sort.Strings(out)
	return out
}

func warningCodes(t *testing.T, env envelope) []string {
	t.Helper()
	var out []string
	for _, raw := range env.Warnings {
		var w struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			t.Fatalf("decode warning: %v", err)
		}
		out = append(out, w.Code)
	}
	return out
}

// documentID находит узел документа базового слоя через поиск.
func documentID(t *testing.T, h http.Handler) int64 {
	t.Helper()
	rr, env := doGET(t, h, "/api/search?q="+url.QueryEscape("Отгрузка"))
	if rr.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rr.Code, rr.Body.String())
	}
	for _, it := range decodeSearchItems(t, env) {
		if it.MType == "Document" && it.Component == "cfg" {
			return it.ObjectID
		}
	}
	t.Fatalf("документ Отгрузка слоя cfg не найден поиском")
	return 0
}

// TestHandlerViewDiffShowsInterceptEdge: критерий issue #5 на HTTP-шве.
// raw не видит ребра перехватчика, effective видит его на том же узле
// документа с пометкой added, diff отдаёт только его; god-node и карточка
// узла отвечают тем же.
func TestHandlerViewDiffShowsInterceptEdge(t *testing.T) {
	h := graphweb.NewHandler([]graphweb.ProjectHandle{newInterceptProject(t)})
	doc := documentID(t, h)

	neighbors := func(view string) envelope {
		t.Helper()
		rr, env := doGET(t, h, fmt.Sprintf("/api/neighbors/%d?dir=out&view=%s", doc, view))
		if rr.Code != http.StatusOK {
			t.Fatalf("neighbors view=%s: %d %s", view, rr.Code, rr.Body.String())
		}
		return env
	}
	raw := decodeViewEdges(t, neighbors("raw"))
	if got := strings.Join(raw, ";"); got != "Отгрузка->ТоварыНаСкладах|base|" {
		t.Errorf("raw = %s", got)
	}
	eff := decodeViewEdges(t, neighbors("effective"))
	if got := strings.Join(eff, ";"); got != "Отгрузка->ТоварыВРезерве|ext|added;Отгрузка->ТоварыНаСкладах|base|" {
		t.Errorf("effective = %s", got)
	}
	diffEnv := neighbors("diff")
	diff := decodeViewEdges(t, diffEnv)
	if got := strings.Join(diff, ";"); got != "Отгрузка->ТоварыВРезерве|ext|added" {
		t.Errorf("diff = %s, want одно ребро перехватчика", got)
	}
	if diffEnv.TotalCount != 1 {
		t.Errorf("diff totalCount = %d, want 1", diffEnv.TotalCount)
	}
	for _, e := range raw {
		if strings.Contains(e, "ТоварыВРезерве") {
			t.Errorf("ребро перехватчика попало в raw: %s", e)
		}
	}

	rr, env := doGET(t, h, "/api/godnodes?metric=fan-out&view=diff")
	if rr.Code != http.StatusOK || len(env.Items) == 0 {
		t.Fatalf("godnodes diff: %d %s", rr.Code, rr.Body.String())
	}
	var top struct {
		ObjectID int64 `json:"objectId"`
		FanOut   int64 `json:"fanOut"`
	}
	if err := json.Unmarshal(env.Items[0], &top); err != nil {
		t.Fatalf("decode godnode: %v", err)
	}
	if top.ObjectID != doc || top.FanOut != 1 {
		t.Errorf("godnodes diff: первый %+v, want документ %d с fanOut=1", top, doc)
	}

	rr, env = doGET(t, h, fmt.Sprintf("/api/node/%d?view=diff", doc))
	if rr.Code != http.StatusOK {
		t.Fatalf("node diff: %d %s", rr.Code, rr.Body.String())
	}
	var card struct {
		Components     []string `json:"components"`
		ExtensionEdges []struct {
			Layer        string `json:"layer"`
			OtherDisplay string `json:"otherDisplay"`
			Diff         string `json:"diff"`
		} `json:"extensionEdges"`
	}
	if err := json.Unmarshal(env.Items[0], &card); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if len(card.ExtensionEdges) != 1 || card.ExtensionEdges[0].Layer != "ext" ||
		card.ExtensionEdges[0].OtherDisplay != "ТоварыВРезерве" || card.ExtensionEdges[0].Diff != "added" {
		t.Errorf("карточка не называет расширение, добавившее связь: %+v", card.ExtensionEdges)
	}
}

// TestHandlerViewWithoutExtensions: проект без расширений (как ut_demo).
// raw и effective совпадают, diff пуст, effective и diff предупреждают.
func TestHandlerViewWithoutExtensions(t *testing.T) {
	tp := newTestProject(t, "noext")
	o1, _, _, _ := seedTwoNodesWithEdge(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	get := func(path string) envelope {
		t.Helper()
		rr, env := doGET(t, h, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		return env
	}
	raw := get(fmt.Sprintf("/api/neighbors/%d?view=raw", o1))
	eff := get(fmt.Sprintf("/api/neighbors/%d?view=effective", o1))
	diff := get(fmt.Sprintf("/api/neighbors/%d?view=diff", o1))
	if r, e := strings.Join(decodeViewEdges(t, raw), ";"), strings.Join(decodeViewEdges(t, eff), ";"); r != e || r == "" {
		t.Errorf("raw (%s) и effective (%s) обязаны совпасть и быть непустыми", r, e)
	}
	if len(diff.Items) != 0 || diff.TotalCount != 0 {
		t.Errorf("diff не пуст: %v", decodeViewEdges(t, diff))
	}
	if codes := strings.Join(warningCodes(t, diff), ","); !strings.Contains(codes, "no_extensions") {
		t.Errorf("diff без предупреждения no_extensions: %s", codes)
	}
	godRaw := get("/api/godnodes?view=raw")
	godEff := get("/api/godnodes?view=effective")
	if len(godRaw.Items) != len(godEff.Items) {
		t.Errorf("godnodes raw (%d) и effective (%d) разошлись", len(godRaw.Items), len(godEff.Items))
	}
	if godDiff := get("/api/godnodes?view=diff"); len(godDiff.Items) != 0 {
		t.Errorf("godnodes diff не пуст: %d", len(godDiff.Items))
	}
}

// TestHandlerViewUnknownIsBadRequest: опечатка в view даёт 400, а не 500 и
// не молчаливый откат на прежнее поведение.
func TestHandlerViewUnknownIsBadRequest(t *testing.T) {
	tp := newTestProject(t, "badview")
	o1, _, _, _ := seedTwoNodesWithEdge(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})
	for _, path := range []string{
		fmt.Sprintf("/api/neighbors/%d?view=effectiv", o1),
		fmt.Sprintf("/api/node/%d?view=dif", o1),
		"/api/godnodes?view=rw",
		fmt.Sprintf("/api/neighbors/%d?view=raw&layer=base", o1),
	} {
		rr, aerr := doGETErr(t, h, path)
		if rr.Code != http.StatusBadRequest || aerr.Code != "invalid_argument" {
			t.Errorf("%s: %d %s, want 400 invalid_argument", path, rr.Code, aerr.Code)
		}
	}
}
