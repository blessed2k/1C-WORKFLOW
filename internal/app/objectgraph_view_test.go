package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Фикстура вехи В3 (docs/architecture-graph.md §12): базовая конфигурация и
// расширение, которое перехватывает проведение документа через
// &После("ОбработкаПроведения") и пишет движения в свой регистр. Индекс
// строится настоящим reindex, а не Insert*: проверяется, что режимы карты
// работают на рёбрах, которые положил пайплайн, с его слоями и его строками
// заимствованных объектов.
//
//	cfg: Документ.Отгрузка --writes-register--> РН.ТоварыНаСкладах
//	ext: Документ.Отгрузка --writes-register--> РН.<extRegister>   (перехватчик)
//
// extRegister: мутация фикстуры: другой регистр даёт связь, которой в базе
// нет (diff её показывает), тот же регистр даёт связь, которая в базе уже
// есть (diff пуст).

const viewDocName = "Отгрузка"

func viewAccumRegisterXML(name, uuid string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <AccumulationRegister uuid="` + uuid + `">
    <Properties>
      <Name>` + name + `</Name>
    </Properties>
  </AccumulationRegister>
</MetaDataObject>`
}

func viewDocumentXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Document uuid="88888888-8888-8888-8888-888888888888">
    <Properties>
      <Name>` + viewDocName + `</Name>
    </Properties>
  </Document>
</MetaDataObject>`
}

const viewBasePosting = `
Процедура ОбработкаПроведения(Отказ, Режим)
	Движения.ТоварыНаСкладах.Записать();
КонецПроцедуры
`

func viewInterceptPosting(register string) string {
	return `
&После("ОбработкаПроведения")
Процедура Расш_ОбработкаПроведения(Отказ, Режим)
	Движения.` + register + `.Записать();
КонецПроцедуры
`
}

// buildPostingInterceptProject строит проект и индексирует его. withExt=false
// даёт тот же проект без расширения: так выглядит ut_demo.
func buildPostingInterceptProject(t *testing.T, extRegister string, withExt bool) *ObjectGraphService {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()

	regPath := func(name string) string { return workspace.DumpDeclarationPath("AccumulationRegister", name) }
	common := map[string]string{
		workspace.DumpDeclarationPath("Document", viewDocName): viewDocumentXML(),
		regPath("ТоварыНаСкладах"):                             viewAccumRegisterXML("ТоварыНаСкладах", "aaaaaaaa-0000-0000-0000-000000000001"),
		regPath("ТоварыВРезерве"):                              viewAccumRegisterXML("ТоварыВРезерве", "aaaaaaaa-0000-0000-0000-000000000002"),
	}
	modulePath := workspace.DumpModulePath("Document", viewDocName, workspace.ModuleObject)

	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("БазоваяКонфигурация"))
	for rel, content := range common {
		writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(rel)), content)
	}
	writeFile(t, filepath.Join(projectRoot, "cfg", filepath.FromSlash(modulePath)), viewBasePosting)

	components := []map[string]any{{"id": "cfg", "kind": "configuration", "root": "cfg"}}
	if withExt {
		writeFile(t, filepath.Join(projectRoot, "ext", "Configuration.xml"), extConfigurationXML("РасширениеСклада", "Customization"))
		for rel, content := range common {
			writeFile(t, filepath.Join(projectRoot, "ext", filepath.FromSlash(rel)), content)
		}
		writeFile(t, filepath.Join(projectRoot, "ext", filepath.FromSlash(modulePath)), viewInterceptPosting(extRegister))
		components = append(components, map[string]any{
			"id": "ext", "kind": "extension", "root": "ext", "appliesTo": "cfg", "applyOrder": 1,
		})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "project": "view", "components": components})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: domain.ProjectID("view"), Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject("view"); err != nil {
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
	return NewObjectGraphService(p, 0)
}

// viewNodeID: канонический id документа: карточка по имени в любом view.
func viewNodeID(t *testing.T, svc *ObjectGraphService, mtype, name string) int64 {
	t.Helper()
	resp, err := svc.Node(context.Background(), NodeInput{
		Target: ObjectTarget{ObjectType: mtype, ObjectName: name}, View: GraphViewEffective,
	})
	if err != nil {
		t.Fatalf("Node(%s.%s): %v", mtype, name, err)
	}
	return resp.Items[0].ObjectID
}

// edgeSummary: ребро как строка «откуда->куда|слой|diff» для сравнения наборов.
func edgeSummary(items []EdgeItem) []string {
	out := make([]string, 0, len(items))
	for _, e := range items {
		out = append(out, e.FromDisplay+"->"+e.ToDisplay+"|"+e.Layer+"|"+e.Diff)
	}
	sort.Strings(out)
	return out
}

func neighborsView(t *testing.T, svc *ObjectGraphService, id int64, view string) Response[EdgeItem] {
	t.Helper()
	resp, err := svc.Neighbors(context.Background(), NeighborsInput{ObjectID: id, Direction: "out", View: view, Limit: 50})
	if err != nil {
		t.Fatalf("Neighbors(view=%s): %v", view, err)
	}
	return resp
}

func hasWarning(ws []Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

// TestObjectGraphViewDiffShowsInterceptEdge: критерий issue #5: расширение
// перехватывает проведение и пишет в регистр, которого база не пишет; raw
// этой связи не видит, effective видит её на ТОМ ЖЕ узле документа, diff
// показывает только её.
func TestObjectGraphViewDiffShowsInterceptEdge(t *testing.T) {
	svc := buildPostingInterceptProject(t, "ТоварыВРезерве", true)
	ctx := context.Background()
	doc := viewNodeID(t, svc, "Document", viewDocName)
	reserve := viewNodeID(t, svc, "AccumulationRegister", "ТоварыВРезерве")

	raw := neighborsView(t, svc, doc, GraphViewRaw)
	if got, want := strings.Join(edgeSummary(raw.Items), ";"), "Отгрузка->ТоварыНаСкладах|base|"; got != want {
		t.Errorf("raw = %s, want %s", got, want)
	}

	eff := neighborsView(t, svc, doc, GraphViewEffective)
	if got, want := strings.Join(edgeSummary(eff.Items), ";"),
		"Отгрузка->ТоварыВРезерве|ext|added;Отгрузка->ТоварыНаСкладах|base|"; got != want {
		t.Errorf("effective = %s, want %s", got, want)
	}
	for _, e := range eff.Items {
		if e.FromObjectID != doc {
			t.Errorf("effective: ребро %d висит на узле %d, а не на каноническом узле документа %d", e.ID, e.FromObjectID, doc)
		}
	}

	diff := neighborsView(t, svc, doc, GraphViewDiff)
	if len(diff.Items) != 1 || diff.TotalCount != 1 {
		t.Fatalf("diff: items=%d total=%d, want 1/1: %v", len(diff.Items), diff.TotalCount, edgeSummary(diff.Items))
	}
	e := diff.Items[0]
	if e.ToObjectID != reserve || e.Layer != "ext" || e.Diff != EdgeDiffAdded {
		t.Errorf("diff: ребро %+v, want к %d, layer=ext, diff=added", e, reserve)
	}
	for _, r := range raw.Items {
		if r.ToObjectID == e.ToObjectID {
			t.Errorf("ребро diff есть и в raw: %+v", r)
		}
	}
	if !hasWarning(diff.Warnings, "diff_only_added") {
		t.Errorf("diff молчит о том, что пропавшие связи не считаются: %+v", diff.Warnings)
	}

	// Та же связь со стороны регистра: входящее ребро в diff.
	in, err := svc.Neighbors(ctx, NeighborsInput{ObjectID: reserve, Direction: "in", View: GraphViewDiff})
	if err != nil {
		t.Fatalf("Neighbors(reserve, diff): %v", err)
	}
	if len(in.Items) != 1 || in.Items[0].FromObjectID != doc {
		t.Errorf("diff у регистра: %v, want одно ребро от документа %d", edgeSummary(in.Items), doc)
	}

	// Карточка узла называет расширение, добавившее связь.
	card, err := svc.Node(ctx, NodeInput{Target: ObjectTarget{ObjectID: doc}, View: GraphViewDiff})
	if err != nil {
		t.Fatalf("Node(diff): %v", err)
	}
	node := card.Items[0]
	if node.FanOut != 1 || node.FanIn != 0 {
		t.Errorf("карточка diff: fanIn=%d fanOut=%d, want 0/1", node.FanIn, node.FanOut)
	}
	if got := strings.Join(node.Components, ","); got != "cfg,ext" {
		t.Errorf("карточка: components=%s, want cfg,ext", got)
	}
	if len(node.ExtensionEdges) != 1 || node.ExtensionEdges[0].Layer != "ext" ||
		node.ExtensionEdges[0].OtherDisplay != "ТоварыВРезерве" || node.ExtensionEdges[0].Direction != "out" {
		t.Errorf("карточка не называет связь расширения: %+v", node.ExtensionEdges)
	}
	effCard, err := svc.Node(ctx, NodeInput{Target: ObjectTarget{ObjectID: doc}, View: GraphViewEffective})
	if err != nil {
		t.Fatalf("Node(effective): %v", err)
	}
	if effCard.Items[0].FanOut != 2 {
		t.Errorf("карточка effective: fanOut=%d, want 2", effCard.Items[0].FanOut)
	}
	rawCard, err := svc.Node(ctx, NodeInput{Target: ObjectTarget{ObjectID: doc}, View: GraphViewRaw})
	if err != nil {
		t.Fatalf("Node(raw): %v", err)
	}
	if rawCard.Items[0].FanOut != 1 || len(rawCard.Items[0].ExtensionEdges) != 0 {
		t.Errorf("карточка raw: fanOut=%d ext=%v, want 1 и без рёбер расширений", rawCard.Items[0].FanOut, rawCard.Items[0].ExtensionEdges)
	}

	// Радиус (object_graph) в diff: только добавленное ребро.
	rad, err := svc.Radius(ctx, RadiusInput{
		Target: ObjectTarget{ObjectType: "Document", ObjectName: viewDocName}, Depth: 2, View: GraphViewDiff,
	})
	if err != nil {
		t.Fatalf("Radius(diff): %v", err)
	}
	if len(rad.Items) != 1 || rad.Items[0].ToDisplay != "ТоварыВРезерве" || rad.Items[0].Diff != EdgeDiffAdded {
		t.Errorf("radius diff: %+v, want одно ребро к ТоварыВРезерве", rad.Items)
	}
	radRaw, err := svc.Radius(ctx, RadiusInput{
		Target: ObjectTarget{ObjectType: "Document", ObjectName: viewDocName}, Depth: 2, View: GraphViewRaw,
	})
	if err != nil {
		t.Fatalf("Radius(raw): %v", err)
	}
	if len(radRaw.Items) != 1 || radRaw.Items[0].ToDisplay != "ТоварыНаСкладах" {
		t.Errorf("radius raw: %+v, want одно ребро к ТоварыНаСкладах", radRaw.Items)
	}

	// God-node: effective склеивает строки документа в один узел, diff
	// считает только добавленное.
	godEff, err := svc.GodNodes(ctx, GodNodesInput{View: GraphViewEffective, By: "fan-out"})
	if err != nil {
		t.Fatalf("GodNodes(effective): %v", err)
	}
	var docRows int
	for _, it := range godEff.Items {
		if it.NameDisplay == viewDocName {
			docRows++
			if it.ObjectID != doc || it.FanOut != 2 {
				t.Errorf("god-node effective: %+v, want id=%d fanOut=2", it, doc)
			}
		}
	}
	if docRows != 1 {
		t.Errorf("god-node effective: документ встречается %d раз, want 1", docRows)
	}
	godDiff, err := svc.GodNodes(ctx, GodNodesInput{View: GraphViewDiff, By: "fan-out"})
	if err != nil {
		t.Fatalf("GodNodes(diff): %v", err)
	}
	if len(godDiff.Items) == 0 || godDiff.Items[0].ObjectID != doc || godDiff.Items[0].FanOut != 1 {
		t.Errorf("god-node diff: %+v, want первым документ %d с fanOut=1", godDiff.Items, doc)
	}
	godRaw, err := svc.GodNodes(ctx, GodNodesInput{View: GraphViewRaw, By: "fan-out"})
	if err != nil {
		t.Fatalf("GodNodes(raw): %v", err)
	}
	for _, it := range godRaw.Items {
		if it.NameDisplay == viewDocName && it.FanOut != 1 {
			t.Errorf("god-node raw: документ fanOut=%d, want 1", it.FanOut)
		}
	}
}

// TestObjectGraphViewSameRegisterIsNotDiff: мутация фикстуры: расширение
// пишет в тот же регистр, что и база. Связь не новая, diff пуст, а effective
// честно показывает оба ребра, у ребра расширения нет пометки added.
func TestObjectGraphViewSameRegisterIsNotDiff(t *testing.T) {
	svc := buildPostingInterceptProject(t, "ТоварыНаСкладах", true)
	doc := viewNodeID(t, svc, "Document", viewDocName)

	if diff := neighborsView(t, svc, doc, GraphViewDiff); len(diff.Items) != 0 {
		t.Errorf("diff = %v, want пусто: связь уже есть в базе", edgeSummary(diff.Items))
	}
	eff := neighborsView(t, svc, doc, GraphViewEffective)
	if got, want := strings.Join(edgeSummary(eff.Items), ";"),
		"Отгрузка->ТоварыНаСкладах|base|;Отгрузка->ТоварыНаСкладах|ext|"; got != want {
		t.Errorf("effective = %s, want %s", got, want)
	}
}

// TestObjectGraphViewWithoutExtensions: так выглядит ut_demo: расширений
// нет, raw и effective совпадают, diff пуст, и об этом сказано.
func TestObjectGraphViewWithoutExtensions(t *testing.T) {
	svc := buildPostingInterceptProject(t, "", false)
	doc := viewNodeID(t, svc, "Document", viewDocName)

	raw := neighborsView(t, svc, doc, GraphViewRaw)
	eff := neighborsView(t, svc, doc, GraphViewEffective)
	if r, e := strings.Join(edgeSummary(raw.Items), ";"), strings.Join(edgeSummary(eff.Items), ";"); r != e || r == "" {
		t.Errorf("raw (%s) и effective (%s) обязаны совпасть и быть непустыми", r, e)
	}
	diff := neighborsView(t, svc, doc, GraphViewDiff)
	if len(diff.Items) != 0 || diff.TotalCount != 0 {
		t.Errorf("diff = %v, want пусто", edgeSummary(diff.Items))
	}
	if !hasWarning(diff.Warnings, "no_extensions") || !hasWarning(eff.Warnings, "no_extensions") {
		t.Errorf("нет предупреждения no_extensions: diff=%+v effective=%+v", diff.Warnings, eff.Warnings)
	}
	if hasWarning(raw.Warnings, "no_extensions") {
		t.Errorf("raw не должен предупреждать о расширениях: %+v", raw.Warnings)
	}
}

// TestObjectGraphViewInvalidArgument: опечатка в view и view вместе с layer
// дают invalid_argument, а не молчаливый откат на прежнее поведение.
func TestObjectGraphViewInvalidArgument(t *testing.T) {
	svc := buildPostingInterceptProject(t, "", false)
	doc := viewNodeID(t, svc, "Document", viewDocName)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		in   NeighborsInput
	}{
		{"опечатка", NeighborsInput{ObjectID: doc, View: "effectiv"}},
		{"view и layer", NeighborsInput{ObjectID: doc, View: GraphViewRaw, Layer: "base"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Neighbors(ctx, tc.in)
			appErr, ok := err.(*Error)
			if !ok || appErr.Code != CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
		})
	}
	if _, err := svc.GodNodes(ctx, GodNodesInput{View: "dif"}); err == nil {
		t.Error("GodNodes(view=dif) без ошибки")
	}
}
