package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// graphFixture — фикстура объектного графа поверх общей: второй объект
// метаданных (регистр), в который пишет первый, и три ребра с разными
// файловыми зависимостями.
type graphFixture struct {
	fixture
	registerID int64
	// edgeModule зависит только от Module.bsl, edgeBoth — от него и от XML
	// владельца, edgeXML — только от XML: удаление по файлам обязано различать
	// эти три случая. edgeBack идёт в обратную сторону, лежит в слое расширения
	// и держится за Form.xml: без него фильтры направления и слоя были бы верны
	// при любом коде.
	edgeModule, edgeBoth, edgeXML, edgeBack int64
}

const fxExtLayer = "ext-1"

func seedGraph(t *testing.T, s *Store) graphFixture {
	t.Helper()
	var g graphFixture
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		if err := seedFixture(tx, &g.fixture); err != nil {
			return err
		}
		var err error
		if g.registerID, err = tx.EnsureMetadataObject(MetadataObject{
			IdentityKey: "metadata_object:cfg:AccumulationRegisters/Р", ComponentID: fxComponent,
			UUID: "uuid-reg", MType: "AccumulationRegister", NameNorm: "р", NameDisplay: "Р",
			FileID: g.fileObjectXML, PropsJSON: "{}",
		}); err != nil {
			return err
		}
		inTx := true
		edge := ObjectDataEdge{
			FromObjectID: g.objectID, ToObjectID: g.registerID, Kind: EdgeWritesRegister,
			Provenance: EdgeProvenanceCode, Confidence: 0.9, Mode: "write", InTransaction: &inTx,
			Evidence: `{"chain":["Y.ОбъектМодуль","X.Записать"]}`,
		}
		edge.FileIDs = []int64{g.fileModuleBSL}
		if g.edgeModule, err = tx.InsertObjectDataEdge(edge); err != nil {
			return err
		}
		edge.FileIDs = []int64{g.fileModuleBSL, g.fileObjectXML}
		if g.edgeBoth, err = tx.InsertObjectDataEdge(edge); err != nil {
			return err
		}
		declared := ObjectDataEdge{
			FromObjectID: g.objectID, ToObjectID: g.registerID, Kind: EdgeWritesDeclared,
			Provenance: EdgeProvenanceDeclared, Confidence: 0.5,
			Evidence: `{"xml":"Catalogs/Y.xml"}`, FileIDs: []int64{g.fileObjectXML},
		}
		if g.edgeXML, err = tx.InsertObjectDataEdge(declared); err != nil {
			return err
		}
		back := ObjectDataEdge{
			FromObjectID: g.registerID, ToObjectID: g.objectID, Kind: EdgeReadsQuery,
			Layer: fxExtLayer, Provenance: EdgeProvenanceCode, Confidence: 0.4,
			Evidence: `{"chain":["Р.МодульМенеджера"]}`, FileIDs: []int64{g.fileFormXML},
		}
		if g.edgeBack, err = tx.InsertObjectDataEdge(back); err != nil {
			return err
		}
		return tx.InsertObjectBadge(ObjectBadge{ObjectID: g.objectID, Badge: "has-dynamic", Count: 3})
	}); err != nil {
		t.Fatalf("наполнение графа: %v", err)
	}
	return g
}

// Ребро без файловых зависимостей неудаляемо, поэтому не вставляется вовсе.
func TestInsertObjectDataEdgeRefusesEdgeWithoutFiles(t *testing.T) {
	s, f := seeded(t)
	err := s.Write(context.Background(), func(tx *WriteTx) error {
		_, err := tx.InsertObjectDataEdge(ObjectDataEdge{
			FromObjectID: f.objectID, ToObjectID: f.objectID, Kind: EdgeCreates,
			Provenance: EdgeProvenanceCode, Confidence: 0.5, Evidence: "{}",
		})
		return err
	})
	if !errors.Is(err, ErrEdgeWithoutFiles) {
		t.Fatalf("ребро без файлов принято: %v", err)
	}
	if n := countRows(t, s, "object_data_edge", ""); n != 0 {
		t.Errorf("после отказа в таблице %d рёбер, ожидалось 0", n)
	}
}

// Схема допускает только объекты метаданных в качестве узлов (D6): ребро на
// чужой id не вставляется даже когда такой node существует.
func TestInsertObjectDataEdgeRefusesNonObjectNode(t *testing.T) {
	s, f := seeded(t)
	err := s.Write(context.Background(), func(tx *WriteTx) error {
		_, err := tx.InsertObjectDataEdge(ObjectDataEdge{
			FromObjectID: f.symA, ToObjectID: f.objectID, Kind: EdgeWritesRegister,
			Provenance: EdgeProvenanceCode, Confidence: 0.9, Evidence: "{}",
			FileIDs: []int64{f.fileModuleBSL},
		})
		return err
	})
	if err == nil {
		t.Fatal("ребро от символа принято: узлами графа являются только объекты метаданных")
	}
}

// Удаление по файлам сносит каждое ребро, зависящее хотя бы от одного из
// названных файлов, вместе с его строками зависимостей; ребро, ни один файл
// которого не назван, остаётся.
func TestDeleteObjectEdgesForFiles(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	if n := countRows(t, s, "object_data_edge", ""); n != 4 {
		t.Fatalf("до удаления рёбер %d, ожидалось 4", n)
	}
	if n := countRows(t, s, "object_data_edge_dep", ""); n != 5 {
		t.Fatalf("до удаления зависимостей %d, ожидалось 5", n)
	}

	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.DeleteObjectEdgesForFiles(g.fileModuleBSL)
	}); err != nil {
		t.Fatalf("удаление рёбер по файлу: %v", err)
	}

	// Удаляются оба ребра, зависящие от Module.bsl, включая то, второй файл
	// которого не менялся: цепочка атрибуции пересобирается целиком.
	for _, id := range []int64{g.edgeModule, g.edgeBoth} {
		if n := countRows(t, s, "object_data_edge", "id=?", id); n != 0 {
			t.Errorf("ребро %d пережило удаление своего файла", id)
		}
	}
	for _, id := range []int64{g.edgeXML, g.edgeBack} {
		if n := countRows(t, s, "object_data_edge", "id=?", id); n != 1 {
			t.Errorf("ребро %d удалено, хотя его файлы не назывались", id)
		}
	}
	if n := countRows(t, s, "object_data_edge_dep", "edge_id IN (?,?)", g.edgeModule, g.edgeBoth); n != 0 {
		t.Errorf("осталось %d строк зависимостей удалённых рёбер", n)
	}
	if n := countRows(t, s, "object_data_edge_dep", "edge_id=?", g.edgeXML); n != 1 {
		t.Errorf("зависимости уцелевшего ребра потеряны: %d", n)
	}
	assertValid(t, s, "после удаления рёбер по файлам")
}

// Удаление объекта метаданных уносит его рёбра и бейджи: висячих рёбер на
// несуществующий объект в индексе быть не может.
func TestObjectRemovalTakesEdgesAndBadges(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		if err := tx.DeleteObjectEdgesForFiles(g.fileModuleBSL, g.fileObjectXML, g.fileFormXML); err != nil {
			return err
		}
		return tx.DeleteSourceFiles(g.fileObjectXML)
	}); err != nil {
		t.Fatalf("удаление XML владельца: %v", err)
	}
	if n := countRows(t, s, "object_data_edge", ""); n != 0 {
		t.Errorf("после удаления объектов осталось %d рёбер", n)
	}
	if n := countRows(t, s, "object_badge", ""); n != 0 {
		t.Errorf("после удаления объектов осталось %d бейджей", n)
	}
	assertValid(t, s, "после удаления объекта метаданных")
}

// Пересборка, забывшая снять рёбра до удаления файлов, оставляет ребро без
// единой зависимости — неудаляемое навсегда. Инвариант обязан это назвать, а
// не промолчать: foreign_key_check такого не видит.
func TestValidateCatchesEdgeLeftWithoutFiles(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.DeleteSourceFiles(g.fileModuleBSL)
	}); err != nil {
		t.Fatalf("удаление файла модуля: %v", err)
	}
	var bad []string
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		bad, err = tx.Validate()
		return err
	}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(strings.Join(bad, " "), "object_data_edge_without_dep") {
		t.Fatalf("инвариант не заметил ребро без файловых зависимостей: %v", bad)
	}
}

// Бейдж повторной вставкой замещается, а не складывается: пересборка владельца
// считает счётчик заново.
func TestInsertObjectBadgeReplacesCount(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.InsertObjectBadge(ObjectBadge{ObjectID: g.objectID, Badge: "has-dynamic", Count: 5})
	}); err != nil {
		t.Fatalf("повторная вставка бейджа: %v", err)
	}
	if n := countRows(t, s, "object_badge", "object_id=? AND badge='has-dynamic'", g.objectID); n != 1 {
		t.Errorf("строк бейджа %d, ожидалась 1", n)
	}
	if n := countRows(t, s, "object_badge", "object_id=? AND count=5", g.objectID); n != 1 {
		t.Error("счётчик бейджа не замещён последней вставкой")
	}
}

// Фильтры соседей: направление, вид ребра, слой и порог достоверности. Числа
// взяты из фикстуры выше, а не из выдачи: каждый случай отличает выборку от
// «отдать всё».
func TestObjectDataEdgesFilters(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	cases := []struct {
		name   string
		filter ObjectEdgeFilter
		want   int
	}{
		{"оба направления", ObjectEdgeFilter{ObjectID: g.objectID}, 4},
		{"исходящие", ObjectEdgeFilter{ObjectID: g.objectID, Direction: EdgeDirectionOut}, 3},
		{"входящие", ObjectEdgeFilter{ObjectID: g.objectID, Direction: EdgeDirectionIn}, 1},
		{"вид ребра", ObjectEdgeFilter{ObjectID: g.objectID, Kinds: []string{EdgeWritesRegister}}, 2},
		{"два вида", ObjectEdgeFilter{ObjectID: g.objectID,
			Kinds: []string{EdgeWritesRegister, EdgeWritesDeclared}}, 3},
		{"слой расширения", ObjectEdgeFilter{ObjectID: g.objectID, Layer: fxExtLayer}, 1},
		{"базовый слой", ObjectEdgeFilter{ObjectID: g.objectID, Layer: "base"}, 3},
		{"порог достоверности", ObjectEdgeFilter{ObjectID: g.objectID, MinConfidence: 0.6}, 2},
		{"порог выше всех", ObjectEdgeFilter{ObjectID: g.objectID, MinConfidence: 0.95}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []ObjectDataEdgeRow
			var total int64
			if err := s.Read(context.Background(), func(tx *ReadTx) error {
				var err error
				if got, err = tx.ObjectDataEdges(tc.filter); err != nil {
					return err
				}
				total, err = tx.CountObjectDataEdges(tc.filter)
				return err
			}); err != nil {
				t.Fatalf("чтение рёбер: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("рёбер %d, ожидалось %d", len(got), tc.want)
			}
			if total != int64(tc.want) {
				t.Errorf("total %d, ожидалось %d: счётчик обязан считать по тому же условию", total, tc.want)
			}
		})
	}
}

// Страничность: вторая страница продолжает первую, а не повторяет её. Лишняя
// строка сверх Limit — признак продолжения, курсор считает вызывающий.
func TestObjectDataEdgesPaging(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	f := ObjectEdgeFilter{ObjectID: g.objectID, Limit: 2}
	var first, second []ObjectDataEdgeRow
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		if first, err = tx.ObjectDataEdges(f); err != nil {
			return err
		}
		f.AfterID = first[len(first)-2].ID
		second, err = tx.ObjectDataEdges(f)
		return err
	}); err != nil {
		t.Fatalf("чтение страниц: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("на первой странице %d строк, ожидалось 3 (Limit+1 как признак продолжения)", len(first))
	}
	seen := map[int64]bool{first[0].ID: true, first[1].ID: true}
	for _, r := range second {
		if seen[r.ID] {
			t.Errorf("вторая страница повторяет ребро %d", r.ID)
		}
	}
	if len(second) != 2 {
		t.Errorf("на второй странице %d строк, ожидалось 2", len(second))
	}
}

// Ребро по id со своими файлами и бейджи узла: то, из чего собирается ответ
// evidence и карточка узла.
func TestObjectDataEdgeEvidenceAndBadges(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	var edge ObjectDataEdgeRow
	var ok bool
	var files []SourceFileRow
	var badges []ObjectBadge
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		if edge, ok, err = tx.ObjectDataEdgeByID(g.edgeBoth); err != nil {
			return err
		}
		if files, err = tx.ObjectDataEdgeFiles(g.edgeBoth); err != nil {
			return err
		}
		badges, err = tx.ObjectBadges(g.objectID)
		return err
	}); err != nil {
		t.Fatalf("чтение evidence: %v", err)
	}
	if !ok {
		t.Fatal("ребро не найдено по своему id")
	}
	if edge.Evidence != `{"chain":["Y.ОбъектМодуль","X.Записать"]}` {
		t.Errorf("evidence %q не то, что было записано", edge.Evidence)
	}
	if edge.Mode != "write" || edge.InTransaction == nil || !*edge.InTransaction {
		t.Errorf("mode=%q in_transaction=%v: значения register_access не сохранились",
			edge.Mode, edge.InTransaction)
	}
	if edge.Layer != "base" {
		t.Errorf("слой %q, ожидался base по умолчанию", edge.Layer)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.RelPath)
	}
	if len(paths) != 2 || !strings.Contains(strings.Join(paths, " "), "Module.bsl") ||
		!strings.Contains(strings.Join(paths, " "), "Catalogs/Y.xml") {
		t.Errorf("файлы ребра %v, ожидались Module.bsl и Catalogs/Y.xml", paths)
	}
	if len(badges) != 1 || badges[0].Badge != "has-dynamic" || badges[0].Count != 3 {
		t.Errorf("бейджи узла %+v, ожидался has-dynamic со счётчиком 3", badges)
	}
	// Ребро с NULL-режимом отдаётся пустым режимом, а не выдуманным значением.
	var declared ObjectDataEdgeRow
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		declared, _, err = tx.ObjectDataEdgeByID(g.edgeXML)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if declared.Mode != "" || declared.InTransaction != nil {
		t.Errorf("у декларированного ребра mode=%q in_transaction=%v, ожидались пустые",
			declared.Mode, declared.InTransaction)
	}
}

// Топ god-node: fan-in и fan-out считаются по рёбрам и различаются по оси
// сортировки, фильтр по виду объекта сужает выдачу.
func TestGodNodes(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)
	var byIn, byOut, catalogs []GodNodeRow
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		if byIn, err = tx.GodNodes(GodNodeFilter{By: GodNodeByFanIn}); err != nil {
			return err
		}
		if byOut, err = tx.GodNodes(GodNodeFilter{By: GodNodeByFanOut}); err != nil {
			return err
		}
		catalogs, err = tx.GodNodes(GodNodeFilter{MTypes: []string{"Catalog"}})
		return err
	}); err != nil {
		t.Fatalf("god-node: %v", err)
	}
	// В фикстуре у регистра три входящих ребра и одно исходящее, у справочника
	// наоборот: топы по двум осям обязаны начинаться с разных узлов.
	if len(byIn) == 0 || byIn[0].ObjectID != g.registerID || byIn[0].FanIn != 3 || byIn[0].FanOut != 1 {
		t.Errorf("топ по fan-in %+v, ожидался регистр с 3 входящими и 1 исходящим", byIn)
	}
	if len(byOut) == 0 || byOut[0].ObjectID != g.objectID || byOut[0].FanOut != 3 {
		t.Errorf("топ по fan-out %+v, ожидался справочник с 3 исходящими", byOut)
	}
	if len(catalogs) != 1 || catalogs[0].MType != "Catalog" {
		t.Errorf("фильтр по виду объекта дал %+v, ожидался один Catalog", catalogs)
	}

	// Отбор рёбер у god-node тот же, что у соседей: панель обязана считать по
	// тем рёбрам, которые показывает карта. Числа взяты из фикстуры: два ребра
	// writes-register и одно единственное в слое расширения.
	for _, tc := range []struct {
		name       string
		filter     GodNodeFilter
		wantFanIn  int64
		wantFanOut int64
	}{
		{"только writes-register", GodNodeFilter{Kinds: []string{EdgeWritesRegister}, By: GodNodeByFanIn}, 2, 0},
		{"только слой расширения", GodNodeFilter{Layer: fxExtLayer, By: GodNodeByFanIn}, 0, 1},
		{"порог отсекает всё", GodNodeFilter{MinConfidence: 0.95, By: GodNodeByFanIn}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []GodNodeRow
			if err := s.Read(context.Background(), func(tx *ReadTx) error {
				var err error
				got, err = tx.GodNodes(tc.filter)
				return err
			}); err != nil {
				t.Fatalf("god-node: %v", err)
			}
			if tc.wantFanIn == 0 && tc.wantFanOut == 0 {
				if len(got) != 0 {
					t.Fatalf("под фильтр не попадает ни одно ребро, а узлов %d", len(got))
				}
				return
			}
			var reg GodNodeRow
			for _, r := range got {
				if r.ObjectID == g.registerID {
					reg = r
				}
			}
			if reg.FanIn != tc.wantFanIn || reg.FanOut != tc.wantFanOut {
				t.Errorf("у регистра fan-in=%d fan-out=%d, ожидались %d и %d",
					reg.FanIn, reg.FanOut, tc.wantFanIn, tc.wantFanOut)
			}
		})
	}
}

// Число файлов в одной пересборке ничем не ограничено: на реальной выгрузке
// ut_demo их 48 699, и инкрементальный прогон после смены ParserVersion несёт
// в отбор весь этот список. Отбор с одним плейсхолдером на файл упирается в
// лимит SQLite на число переменных в запросе (32 766 в текущих сборках, 999 в
// старых) и падает «too many SQL variables». Проверяются обе стороны ОДНОГО
// отбора — «что снесём» и сам снос: они обязаны выдерживать любой список.
func TestObjectEdgesByFilesSurviveHugeFileList(t *testing.T) {
	s := openTestStore(t, Options{})
	g := seedGraph(t, s)

	// Заведомо выше лимита. Несуществующие id ничего не отбирают, но в
	// запросе участвуют наравне с настоящим: проверяется размер списка, а не
	// состав выборки.
	const padding = 40000
	huge := make([]int64, 0, padding+1)
	huge = append(huge, g.fileModuleBSL)
	for i := int64(1); i <= padding; i++ {
		huge = append(huge, 1_000_000+i)
	}

	deps := func(fileIDs []int64) []ObjectDataEdgeDeps {
		t.Helper()
		var out []ObjectDataEdgeDeps
		if err := s.Read(context.Background(), func(tx *ReadTx) error {
			var err error
			out, err = tx.ObjectDataEdgesDependingOnFiles(fileIDs...)
			return err
		}); err != nil {
			t.Fatalf("состав сносимых рёбер на %d файлах: %v", len(fileIDs), err)
		}
		return out
	}

	// Длинный список обязан дать ровно то же, что короткий: лишние id пустые.
	want := deps([]int64{g.fileModuleBSL})
	if len(want) != 2 {
		t.Fatalf("на коротком списке рёбер %d, ожидалось 2", len(want))
	}
	if got := deps(huge); !reflect.DeepEqual(got, want) {
		t.Errorf("состав на %d файлах разошёлся с коротким списком:\n%+v\n%+v", len(huge), got, want)
	}

	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.DeleteObjectEdgesForFiles(huge...)
	}); err != nil {
		t.Fatalf("удаление рёбер по %d файлам: %v", len(huge), err)
	}
	for _, id := range []int64{g.edgeModule, g.edgeBoth} {
		if n := countRows(t, s, "object_data_edge", "id=?", id); n != 0 {
			t.Errorf("ребро %d пережило удаление своего файла в длинном списке", id)
		}
	}
	for _, id := range []int64{g.edgeXML, g.edgeBack} {
		if n := countRows(t, s, "object_data_edge", "id=?", id); n != 1 {
			t.Errorf("ребро %d удалено, хотя его файлы не назывались", id)
		}
	}
	assertValid(t, s, "после удаления рёбер по длинному списку файлов")
}
