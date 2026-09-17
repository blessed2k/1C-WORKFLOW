package graphweb_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// bom — выгрузки 1С пишутся с BOM (internal/app/projects_test.go делает то
// же самое ради workspace.DetectKind).
const bom = "\ufeff"

func конфигурацияXML(name string) string {
	return bom + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0002">
		<Properties>
			<Name>` + name + `</Name>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// testProject — один --project, как его видит cmd/mcp1c: workspaceRoot
// (куда открывается app.Projects) отдельно от projectRoot (где лежит
// 1c-project.json) — та же пара каталогов, что строит
// internal/app/projects_test.go:newFixtureProject, тем же приёмом (только
// экспортированной поверхностью workspace/app, без доступа к их
// неэкспортированным тестовым хелперам — graphweb не пакет app).
type testProject struct {
	id            domain.ProjectID
	workspaceRoot string
	handle        graphweb.ProjectHandle
}

// newTestProject регистрирует пустой (без узлов) проект и возвращает
// ProjectHandle, готовый к использованию в graphweb.NewHandler. Данные
// сеются отдельно, через seed*, чтобы разные тесты не платили за фикстуру,
// которая им не нужна.
func newTestProject(t *testing.T, id domain.ProjectID) testProject {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))

	manifest := map[string]any{
		"version": 1,
		"project": string(id),
		"components": []map[string]any{
			{"id": "cfg", "kind": "configuration", "root": "cfg"},
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
	if err := reg.Upsert(workspace.ProjectEntry{ID: id, Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject(id); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	open := func(ctx context.Context) (*app.Projects, error) {
		return app.NewProjects(workspaceRoot, nil, app.DefaultIndexConfig())
	}

	return testProject{
		id: id, workspaceRoot: workspaceRoot,
		handle: graphweb.ProjectHandle{ID: id, Root: workspaceRoot, RadiusNodesCap: 0, Open: open},
	}
}

// withRadiusCap возвращает копию ph с другим потолком узлов радиуса — нужно
// TestHandlerRadiusTruncationWarningReachesHTTPResponse, чтобы не заводить
// фикстуру на сотни узлов ради проверки одного предупреждения.
func (tp testProject) withRadiusCap(cap int) graphweb.ProjectHandle {
	h := tp.handle
	h.RadiusNodesCap = cap
	return h
}

// seedTwoNodesWithEdge кладёт два объекта метаданных O1->O2 с одним ребром
// writes-register и объект O3 без единого ребра (нужен различению
// «объекта нет» от «объект есть, соседей нет» — R22.1/§43). Возвращает id
// всех трёх.
func seedTwoNodesWithEdge(t *testing.T, tp testProject) (o1, o2, o3, edgeID int64) {
	return seedTwoNodesWithEdgeNamed(t, tp, "")
}

// seedTwoNodesWithEdgeNamed — то же самое, с суффиксом в NameDisplay: два
// проекта с идентичной фикстурой без суффикса дали бы неотличимые по
// содержимому узлы (совпадающие NameDisplay), а store id у каждого проекта
// — своя автоинкрементная последовательность в СВОЁМ файле SQLite, поэтому
// численно оба O1 могут оказаться, например, id=1 в обоих проектах. Различие
// по содержимому — единственный надёжный способ доказать, что ответ под
// project=A никогда не содержит данные project=B (см.
// TestHandlerProjectSelection), не полагаясь на то, что id-пространства
// двух независимых баз случайно не пересекутся.
func seedTwoNodesWithEdgeNamed(t *testing.T, tp testProject, suffix string) (o1, o2, o3, edgeID int64) {
	t.Helper()
	ctx := context.Background()
	p, err := tp.handle.Open(ctx)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer p.Close()
	op, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}

	fill, ids := twoNodesWithEdgeFill(suffix)
	if err := op.Store.Write(ctx, fill); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return ids.o1, ids.o2, ids.o3, ids.edgeID
}

// rebuildTwoNodesWithEdgeNamed пересобирает индекс проекта ЦЕЛИКОМ через
// store.Rebuild — той же операцией, которой пользуется MCP-инструмент
// reindex mode=full: новая эпоха собирается с нуля и публикуется ПОСЛЕ
// сборки (ADR-023 buildDeliberate), старая перестаёт быть указателем, но
// файл не трогается, пока новая не готова. Нужен
// TestHandlerPicksUpEpochSwapWithoutRestart — единственный способ в этом
// пакете имитировать «другой процесс переиндексировал проект, пока
// graph-сервер уже отвечал на запросы», не трогая internal/store напрямую
// мимо его же публичного API.
func rebuildTwoNodesWithEdgeNamed(t *testing.T, tp testProject, suffix string) (o1, o2, o3, edgeID int64) {
	t.Helper()
	ctx := context.Background()
	p, err := tp.handle.Open(ctx)
	if err != nil {
		t.Fatalf("Open (для rebuild): %v", err)
	}
	defer p.Close()
	op, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active (для rebuild): %v", err)
	}

	fill, ids := twoNodesWithEdgeFill(suffix)
	if err := op.Store.Rebuild(ctx, fill); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return ids.o1, ids.o2, ids.o3, ids.edgeID
}

// seededIDs — id, которые twoNodesWithEdgeFill узнаёт только ВНУТРИ своего
// замыкания (WriteTx-колбэк не возвращает значения, кроме error) — указатель
// на общую структуру снаружи забирает их после того, как fill отработал.
type seededIDs struct{ o1, o2, o3, edgeID int64 }

// twoNodesWithEdgeFill строит fill-колбэк, общий для Store.Write
// (seedTwoNodesWithEdgeNamed) и Store.Rebuild (rebuildTwoNodesWithEdgeNamed):
// одна и та же последовательность вставок (компонент, три файла, три
// объекта, одно ребро) в ОБОИХ случаях — важно для теста смены эпохи, где
// он вызывается ВТОРОЙ раз в заведомо пустой (только что пересобранной)
// эпохе и должен пронумеровать узлы теми же id, что и первый раз в первой
// эпохе (тот же порядок вставки в ОБЕ пустые схемы даёт тот же
// автоинкремент).
func twoNodesWithEdgeFill(suffix string) (func(tx *store.WriteTx) error, *seededIDs) {
	ids := &seededIDs{}
	fill := func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		file := func(rel string) (int64, error) {
			hash, err := tx.PutBlob([]byte(rel))
			if err != nil {
				return 0, err
			}
			return tx.InsertSourceFile(store.SourceFile{
				ComponentID: "cfg", RelPath: rel, Size: int64(len(rel)), ContentHash: hash, ParserVersion: 1,
			})
		}
		object := func(mtype, nameNorm, nameDisplay string, fileID int64) (int64, error) {
			return tx.EnsureMetadataObject(store.MetadataObject{
				IdentityKey: "cfg\x00" + mtype + "\x00" + nameNorm, ComponentID: "cfg",
				MType: mtype, NameNorm: nameNorm, NameDisplay: nameDisplay, FileID: fileID,
			})
		}

		f1, err := file("Documents/O1" + suffix + ".xml")
		if err != nil {
			return err
		}
		ids.o1, err = object("Document", "o1"+suffix, "O1"+suffix, f1)
		if err != nil {
			return err
		}
		f2, err := file("AccumulationRegisters/O2" + suffix + ".xml")
		if err != nil {
			return err
		}
		ids.o2, err = object("AccumulationRegister", "o2"+suffix, "O2"+suffix, f2)
		if err != nil {
			return err
		}
		f3, err := file("Documents/O3" + suffix + ".xml")
		if err != nil {
			return err
		}
		ids.o3, err = object("Document", "o3"+suffix, "O3"+suffix, f3)
		if err != nil {
			return err
		}

		ids.edgeID, err = tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.o1, ToObjectID: ids.o2, Kind: store.EdgeWritesRegister, Layer: "base",
			Provenance: store.EdgeProvenanceCode, Confidence: 1.0, Mode: "write",
			Evidence: `{"chain":[]}`, FileIDs: []int64{f1},
		})
		return err
	}
	return fill, ids
}

// seedNodeWithManyEdges кладёт один узел-источник (Document) с тремя
// исходящими рёбрами writes-register к трём разным регистрам — нужен
// TestHandlerNeighborsCursorPagesThroughDistinctResults (курсор не
// проверяется контрактом раздела 8.1 никаким другим тестом: без него
// обнулённый проброс cursor красит только соседний манифест, не тесты
// пакета, что и обнаружила мутация ревью).
func seedNodeWithManyEdges(t *testing.T, tp testProject) (root int64, edgeIDs []int64) {
	t.Helper()
	ctx := context.Background()
	p, err := tp.handle.Open(ctx)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer p.Close()
	op, err := p.Active(ctx)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}

	err = op.Store.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		file := func(rel string) (int64, error) {
			hash, err := tx.PutBlob([]byte(rel))
			if err != nil {
				return 0, err
			}
			return tx.InsertSourceFile(store.SourceFile{
				ComponentID: "cfg", RelPath: rel, Size: int64(len(rel)), ContentHash: hash, ParserVersion: 1,
			})
		}
		object := func(mtype, nameNorm, nameDisplay string, fileID int64) (int64, error) {
			return tx.EnsureMetadataObject(store.MetadataObject{
				IdentityKey: "cfg\x00" + mtype + "\x00" + nameNorm, ComponentID: "cfg",
				MType: mtype, NameNorm: nameNorm, NameDisplay: nameDisplay, FileID: fileID,
			})
		}

		fRoot, err := file("Documents/Root.xml")
		if err != nil {
			return err
		}
		root, err = object("Document", "root", "Root", fRoot)
		if err != nil {
			return err
		}

		for i := 0; i < 3; i++ {
			suffix := string(rune('A' + i))
			f, err := file("AccumulationRegisters/Target" + suffix + ".xml")
			if err != nil {
				return err
			}
			target, err := object("AccumulationRegister", "target"+suffix, "Target"+suffix, f)
			if err != nil {
				return err
			}
			edgeID, err := tx.InsertObjectDataEdge(store.ObjectDataEdge{
				FromObjectID: root, ToObjectID: target, Kind: store.EdgeWritesRegister, Layer: "base",
				Provenance: store.EdgeProvenanceCode, Confidence: 1.0, Mode: "write",
				Evidence: `{"chain":[]}`, FileIDs: []int64{fRoot},
			})
			if err != nil {
				return err
			}
			edgeIDs = append(edgeIDs, edgeID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return root, edgeIDs
}
