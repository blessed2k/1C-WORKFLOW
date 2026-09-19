package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Фикстура склейки слоёв (веха В3, ревью issue #5): рёбра кладутся Insert*,
// потому что нужен точный набор строк, которого reindex на коротком коде не
// даёт (несколько рёбер у разных строк одного объекта).
//
//	Документ «А»: строка cfg (aBase) и строка ext (aExt).
//	  base: aBase -> Р1, aBase -> Р2
//	  ext:  aExt  -> Р1 (та же связь, что в базе), aExt -> Р3, aExt -> Р4
//	Документ «Б»: только cfg. base: Б -> Р1, Б -> Р2, Б -> Р3
//
// Связей у «А» четыре (Р1..Р4), строк рёбер пять. У «Б» связей три. Строка
// группы «А» в отдельности даёт 2 или 3 ребра, то есть меньше, чем «Б»:
// сортировка по одной строке группы вместо суммы ставит «Б» первым.
type mergeFixture struct {
	aBase, aExt, b int64
	r              [5]int64 // r[1]..r[4]
}

func buildMergeFixture(t *testing.T) (*ObjectGraphService, mergeFixture) {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("База"))
	writeFile(t, filepath.Join(projectRoot, "ext", "Configuration.xml"), расширениеXML("Расширение"))
	data, err := json.Marshal(map[string]any{
		"version": 1, "project": "merge",
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
	if err := reg.Upsert(workspace.ProjectEntry{ID: "merge", Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject("merge"); err != nil {
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

	var f mergeFixture
	err = op.Store.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		if err := tx.UpsertComponent(store.Component{ID: "ext", Kind: "extension", Root: "ext", AppliesTo: "cfg", ApplyOrder: 1}); err != nil {
			return err
		}
		file := func(component, rel string) (int64, error) {
			hash, err := tx.PutBlob([]byte(component + "\x00" + rel))
			if err != nil {
				return 0, err
			}
			return tx.InsertSourceFile(store.SourceFile{
				ComponentID: component, RelPath: rel, Size: int64(len(rel)), ContentHash: hash, ParserVersion: 1,
			})
		}
		object := func(component, layer, mtype, name string) (int64, error) {
			fileID, err := file(component, workspace.DumpDeclarationPath(mtype, name))
			if err != nil {
				return 0, err
			}
			norm := strings.ToLower(name)
			return tx.EnsureMetadataObject(store.MetadataObject{
				IdentityKey: component + "\x00" + mtype + "\x00" + norm, ComponentID: component,
				MType: mtype, NameNorm: norm, NameDisplay: name, FileID: fileID, Layer: layer,
			})
		}
		var err error
		if f.aBase, err = object("cfg", "base", "Document", "А"); err != nil {
			return err
		}
		if f.aExt, err = object("ext", "ext", "Document", "А"); err != nil {
			return err
		}
		if f.b, err = object("cfg", "base", "Document", "Б"); err != nil {
			return err
		}
		for i := 1; i <= 4; i++ {
			if f.r[i], err = object("cfg", "base", "AccumulationRegister", "Р"+string(rune('0'+i))); err != nil {
				return err
			}
		}
		fBase, err := file("cfg", workspace.DumpModulePath("Document", "А", workspace.ModuleObject))
		if err != nil {
			return err
		}
		fExt, err := file("ext", workspace.DumpModulePath("Document", "А", workspace.ModuleObject))
		if err != nil {
			return err
		}
		edge := func(from, to int64, layer string, fileID int64) error {
			_, err := tx.InsertObjectDataEdge(store.ObjectDataEdge{
				FromObjectID: from, ToObjectID: to, Kind: store.EdgeWritesRegister, Layer: layer,
				Provenance: store.EdgeProvenanceCode, Confidence: 0.95, Mode: "movement",
				Evidence: `{"chain":[]}`, FileIDs: []int64{fileID},
			})
			return err
		}
		for _, e := range []struct {
			from, to int64
			layer    string
			file     int64
		}{
			{f.aBase, f.r[1], "base", fBase}, {f.aBase, f.r[2], "base", fBase},
			{f.aExt, f.r[1], "ext", fExt}, {f.aExt, f.r[3], "ext", fExt}, {f.aExt, f.r[4], "ext", fExt},
			{f.b, f.r[1], "base", fBase}, {f.b, f.r[2], "base", fBase}, {f.b, f.r[3], "base", fBase},
		} {
			if err := edge(e.from, e.to, e.layer, e.file); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return NewObjectGraphService(p, 0), f
}

// TestObjectGraphViewGodNodesTotalSumsGroup: god-node в effective по оси
// total сортирует по сумме связей всех строк объекта. Регрессия ревью: в
// ORDER BY (fan_in + fan_out) SQLite брал колонки одной строки группы, а не
// суммы, и «Б» (3 связи) вставал выше «А» (4 связи в двух строках).
func TestObjectGraphViewGodNodesTotalSumsGroup(t *testing.T) {
	svc, f := buildMergeFixture(t)
	for _, by := range []string{"total", "fan-out"} {
		resp, err := svc.GodNodes(context.Background(), GodNodesInput{View: GraphViewEffective, By: by, MTypes: []string{"Document"}})
		if err != nil {
			t.Fatalf("GodNodes(%s): %v", by, err)
		}
		if len(resp.Items) != 2 {
			t.Fatalf("%s: узлов %d, want 2: %+v", by, len(resp.Items), resp.Items)
		}
		if resp.Items[0].ObjectID != f.aBase || resp.Items[0].FanOut != 4 {
			t.Errorf("%s: первый %+v, want документ А (%d) с fanOut=4", by, resp.Items[0], f.aBase)
		}
		if resp.Items[1].ObjectID != f.b || resp.Items[1].FanOut != 3 {
			t.Errorf("%s: второй %+v, want документ Б (%d) с fanOut=3", by, resp.Items[1], f.b)
		}
	}
}

// TestObjectGraphViewEffectiveMergesSameRelation: в effective одна связь
// базы и расширения показывается одним ребром с перечнем слоёв, степени её
// не удваивают; тот же ответ при запросе по id строки расширения (узла-
// двойника нет).
func TestObjectGraphViewEffectiveMergesSameRelation(t *testing.T) {
	svc, f := buildMergeFixture(t)
	ctx := context.Background()
	for _, id := range []int64{f.aBase, f.aExt} {
		resp, err := svc.Neighbors(ctx, NeighborsInput{ObjectID: id, Direction: "out", View: GraphViewEffective})
		if err != nil {
			t.Fatalf("Neighbors(%d): %v", id, err)
		}
		if resp.TotalCount != 4 || len(resp.Items) != 4 {
			t.Fatalf("id=%d: total=%d items=%d, want 4 связи", id, resp.TotalCount, len(resp.Items))
		}
		got := map[string]string{}
		for _, e := range resp.Items {
			if e.FromObjectID != f.aBase {
				t.Errorf("id=%d: ребро %d от узла %d, want канонический %d", id, e.ID, e.FromObjectID, f.aBase)
			}
			got[e.ToDisplay] = strings.Join(e.Layers, ",") + "|" + e.Diff
		}
		want := map[string]string{"Р1": "base,ext|", "Р2": "base|", "Р3": "ext|added", "Р4": "ext|added"}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("id=%d: связь с %s = %q, want %q", id, k, got[k], v)
			}
		}
	}
	card, err := svc.Node(ctx, NodeInput{Target: ObjectTarget{ObjectID: f.aExt}, View: GraphViewEffective})
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if n := card.Items[0]; n.ObjectID != f.aBase || n.FanOut != 4 {
		t.Errorf("карточка effective: id=%d fanOut=%d, want %d и 4", n.ObjectID, n.FanOut, f.aBase)
	}
	reg, err := svc.Node(ctx, NodeInput{Target: ObjectTarget{ObjectID: f.r[1]}, View: GraphViewEffective})
	if err != nil {
		t.Fatalf("Node(Р1): %v", err)
	}
	if reg.Items[0].FanIn != 2 {
		t.Errorf("Р1 fanIn=%d, want 2 (А и Б, связь А не удваивается)", reg.Items[0].FanIn)
	}
	rad, err := svc.Radius(ctx, RadiusInput{Target: ObjectTarget{ObjectID: f.aExt}, Direction: "out", Depth: 1, View: GraphViewEffective})
	if err != nil {
		t.Fatalf("Radius: %v", err)
	}
	if len(rad.Items) != 4 {
		t.Errorf("radius effective: рёбер %d, want 4", len(rad.Items))
	}
	diff, err := svc.Neighbors(ctx, NeighborsInput{ObjectID: f.aExt, Direction: "out", View: GraphViewDiff})
	if err != nil {
		t.Fatalf("Neighbors(diff): %v", err)
	}
	if diff.TotalCount != 2 {
		t.Errorf("diff total=%d, want 2 (Р3, Р4)", diff.TotalCount)
	}
}
