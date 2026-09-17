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

// расширениеXML — выгрузка расширения: то же Configuration.xml, но с
// непустым ConfigurationExtensionPurpose (по нему workspace.DetectKind
// отличает расширение от конфигурации).
func расширениеXML(name string) string {
	return bom + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0002">
		<Properties>
			<Name>` + name + `</Name>
			<ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`
}

// borrowedFixtureIDs — id фикстуры «регистр заимствован расширением».
//
// Граф:
//
//	D1 Document "Штрафы"      (cfg) -- writes-register(0.95) --> R_base (cfg)
//	D2 Document "ВходящиеПлатежи" (ext) -- writes-register(0.95) --> R_ext (ext)
//
// R_base и R_ext — ДВЕ строки одного и того же регистра
// «ОбщийДенежныеСредстваСотрудников»: базовая и заимствованная расширением.
// Для разработчика это один регистр, и «кто в него пишет» обязано отвечать
// про обе записи сразу.
type borrowedFixtureIDs struct {
	rBase, rExt, d1, d2 int64
}

func buildBorrowedRegisterFixture(t *testing.T) (*ObjectGraphService, borrowedFixtureIDs) {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()

	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("БазоваяКонфигурация"))
	writeFile(t, filepath.Join(projectRoot, "ext", "Configuration.xml"), расширениеXML("РасширениеА"))

	manifest := map[string]any{
		"version": 1,
		"project": "borrowed",
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
			{"id": "ext", "kind": "extension", "root": "ext", "appliesTo": "cfg", "applyOrder": 1},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: "borrowed", Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject("borrowed"); err != nil {
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

	var ids borrowedFixtureIDs
	err = op.Store.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		if err := tx.UpsertComponent(store.Component{ID: "ext", Kind: "extension", Root: "ext"}); err != nil {
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
		object := func(component, layer, mtype, nameNorm, nameDisplay string) (int64, error) {
			fileID, err := file(component, mtype+"/"+nameDisplay+".xml")
			if err != nil {
				return 0, err
			}
			return tx.EnsureMetadataObject(store.MetadataObject{
				IdentityKey: component + "\x00" + mtype + "\x00" + nameNorm, ComponentID: component,
				MType: mtype, NameNorm: nameNorm, NameDisplay: nameDisplay, FileID: fileID, Layer: layer,
			})
		}

		if ids.rBase, err = object("cfg", "base", "AccumulationRegister",
			"общийденежныесредствасотрудников", "ОбщийДенежныеСредстваСотрудников"); err != nil {
			return err
		}
		if ids.rExt, err = object("ext", "ext", "AccumulationRegister",
			"общийденежныесредствасотрудников", "ОбщийДенежныеСредстваСотрудников"); err != nil {
			return err
		}
		if ids.d1, err = object("cfg", "base", "Document", "штрафы", "Штрафы"); err != nil {
			return err
		}
		if ids.d2, err = object("ext", "ext", "Document", "входящиеплатежи", "ВходящиеПлатежи"); err != nil {
			return err
		}

		// Ребро обязано нести файловые зависимости (ADR-024): по ним
		// инкремент понимает, какие рёбра снести при правке файла.
		fBase, err := file("cfg", "Documents/Штрафы/Ext/ObjectModule.bsl")
		if err != nil {
			return err
		}
		fExt, err := file("ext", "Documents/ВходящиеПлатежи/Ext/ObjectModule.bsl")
		if err != nil {
			return err
		}
		if _, err := tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.d1, ToObjectID: ids.rBase, Kind: store.EdgeWritesRegister, Layer: "base",
			Provenance: store.EdgeProvenanceCode, Confidence: 0.95, Mode: "movement",
			Evidence: `{"chain":[]}`, FileIDs: []int64{fBase},
		}); err != nil {
			return err
		}
		_, err = tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.d2, ToObjectID: ids.rExt, Kind: store.EdgeWritesRegister, Layer: "ext",
			Provenance: store.EdgeProvenanceCode, Confidence: 0.95, Mode: "movement",
			Evidence: `{"chain":[]}`, FileIDs: []int64{fExt},
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	return NewObjectGraphService(p, 0), ids
}

// TestObjectGraphRadiusMergesBorrowedLayers — регрессия с реальной выгрузки
// с расширениями: запись в регистр, сделанную расширением, было не видно вообще.
// Ответ на «кто пишет в ОбщийДенежныеСредстваСотрудников» строился по строке
// объекта из базовой конфигурации, а ребро расширения висит на СВОЕЙ строке
// того же регистра — и терялось молча, без ошибки и предупреждения.
func TestObjectGraphRadiusMergesBorrowedLayers(t *testing.T) {
	svc, _ := buildBorrowedRegisterFixture(t)

	resp, err := svc.Radius(context.Background(), RadiusInput{
		Target:    ObjectTarget{ObjectType: "AccumulationRegister", ObjectName: "ОбщийДенежныеСредстваСотрудников"},
		Direction: "in", Depth: 1, Limit: 50,
	})
	if err != nil {
		t.Fatalf("Radius: %v", err)
	}

	got := map[string]string{}
	for _, e := range resp.Items {
		got[e.FromDisplay] = e.Layer
	}
	if len(got) != 2 {
		t.Fatalf("рёбер %d (%v), ожидалось 2 — база и расширение", len(got), got)
	}
	if got["Штрафы"] != "base" {
		t.Errorf("ребро базы: layer=%q, ожидался base", got["Штрафы"])
	}
	if got["ВходящиеПлатежи"] != "ext" {
		t.Errorf("ребро расширения потеряно или в чужом слое: %v", got)
	}

	var предупреждение bool
	for _, w := range resp.Warnings {
		if w.Code == "object_in_multiple_layers" {
			предупреждение = true
			if !strings.Contains(w.Message, "ext") {
				t.Errorf("предупреждение не называет компоненту расширения: %s", w.Message)
			}
		}
	}
	if !предупреждение {
		t.Error("склейка слоёв прошла молча: нет предупреждения object_in_multiple_layers")
	}
}

// TestObjectGraphRadiusComponentNarrowsToOneLayer — явный component остаётся
// способом посмотреть один слой отдельно: склейка не отнимает эту
// возможность, иначе разницу «база против расширения» стало бы не увидеть.
func TestObjectGraphRadiusComponentNarrowsToOneLayer(t *testing.T) {
	svc, _ := buildBorrowedRegisterFixture(t)

	tests := []struct {
		component string
		wantFrom  string
	}{
		{component: "cfg", wantFrom: "Штрафы"},
		{component: "ext", wantFrom: "ВходящиеПлатежи"},
	}
	for _, tt := range tests {
		t.Run(tt.component, func(t *testing.T) {
			resp, err := svc.Radius(context.Background(), RadiusInput{
				Target: ObjectTarget{
					ObjectType: "AccumulationRegister", ObjectName: "ОбщийДенежныеСредстваСотрудников",
					Component: tt.component,
				},
				Direction: "in", Depth: 1, Limit: 50,
			})
			if err != nil {
				t.Fatalf("Radius: %v", err)
			}
			if len(resp.Items) != 1 {
				t.Fatalf("рёбер %d, ожидалось 1", len(resp.Items))
			}
			if resp.Items[0].FromDisplay != tt.wantFrom {
				t.Errorf("ребро от %q, ожидалось от %q", resp.Items[0].FromDisplay, tt.wantFrom)
			}
			for _, w := range resp.Warnings {
				if w.Code == "object_in_multiple_layers" {
					t.Errorf("сужение до одного слоя не должно давать предупреждение о склейке: %s", w.Message)
				}
			}
		})
	}
}

// TestObjectGraphNodeNamesOtherLayers — карточка узла склейкой не занимается
// (её objectId уходит дальше в Neighbors), но обязана сказать, что объект
// живёт ещё и в расширении.
func TestObjectGraphNodeNamesOtherLayers(t *testing.T) {
	svc, ids := buildBorrowedRegisterFixture(t)

	resp, err := svc.Node(context.Background(), NodeInput{
		Target: ObjectTarget{ObjectType: "AccumulationRegister", ObjectName: "ОбщийДенежныеСредстваСотрудников"},
	})
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("узлов %d, ожидался 1", len(resp.Items))
	}
	if resp.Items[0].ObjectID != ids.rBase {
		t.Errorf("карточка построена по узлу %d, ожидался базовый %d", resp.Items[0].ObjectID, ids.rBase)
	}
	var предупреждение bool
	for _, w := range resp.Warnings {
		if w.Code == "object_in_multiple_layers" {
			предупреждение = true
		}
	}
	if !предупреждение {
		t.Error("карточка молчит о том, что объект заимствован расширением")
	}
}
