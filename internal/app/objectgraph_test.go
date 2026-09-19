package app

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// objectGraphFixtureIDs — id, засеянные buildObjectGraphFixture, нужные тестам
// для построения входов Node/Neighbors/Radius/GodNodes/EdgeEvidence.
//
// Граф:
//
//	O1 Document "ЗаказКлиента"       -- writes-register(1.0) --> O2 AccumulationRegister "ТоварыОрганизаций"
//	O1                               -- writes-register(0.3) --> O3 AccumulationRegister "Остатки"
//	O1                               -- writes-declared(0.5) --> O6 InformationRegister "СтавкиНалогов"
//	O5 Document "ПоступлениеТоваров" -- writes-register(0.9) --> O2
//
// Бейджи: O1 has-dynamic(3), O1 attribution-stale(2), O2 attribution-truncated(1).
type objectGraphFixtureIDs struct {
	o1, o2, o3, o5, o6                              int64
	edgeCode1, edgeCode2, edgeSibling, edgeDeclared int64
	symbolID                                        int64
}

func buildObjectGraphFixture(t *testing.T) (*ObjectGraphService, objectGraphFixtureIDs) {
	t.Helper()
	workspaceRoot := t.TempDir()
	newFixtureProject(t, workspaceRoot, "objectgraph-fixture")
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

	var ids objectGraphFixtureIDs

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

		fO1, err := file("Documents/ЗаказКлиента.xml")
		if err != nil {
			return err
		}
		ids.o1, err = object("Document", "заказклиента", "ЗаказКлиента", fO1)
		if err != nil {
			return err
		}
		fO2, err := file("AccumulationRegisters/ТоварыОрганизаций.xml")
		if err != nil {
			return err
		}
		ids.o2, err = object("AccumulationRegister", "товарыорганизаций", "ТоварыОрганизаций", fO2)
		if err != nil {
			return err
		}
		fO3, err := file("AccumulationRegisters/Остатки.xml")
		if err != nil {
			return err
		}
		ids.o3, err = object("AccumulationRegister", "остатки", "Остатки", fO3)
		if err != nil {
			return err
		}
		fO5, err := file("Documents/ПоступлениеТоваров.xml")
		if err != nil {
			return err
		}
		ids.o5, err = object("Document", "поступлениетоваров", "ПоступлениеТоваров", fO5)
		if err != nil {
			return err
		}
		fO6, err := file("InformationRegisters/СтавкиНалогов.xml")
		if err != nil {
			return err
		}
		ids.o6, err = object("InformationRegister", "ставкиналогов", "СтавкиНалогов", fO6)
		if err != nil {
			return err
		}

		// Модуль и символ для evidence кодового ребра.
		fModule, err := file("Documents/ЗаказКлиента/Ext/ObjectModule.bsl")
		if err != nil {
			return err
		}
		modID, err := tx.EnsureModule(store.Module{
			IdentityKey: "cfg\x00module\x00Documents/ЗаказКлиента/Ext/ObjectModule.bsl", ComponentID: "cfg",
			Kind: "DocumentObject", NameNorm: "заказклиента", NameDisplay: "ЗаказКлиента",
		})
		if err != nil {
			return err
		}
		if err := tx.PutModuleCode(modID, fModule); err != nil {
			return err
		}
		ids.symbolID, err = tx.InsertSymbol(store.Symbol{
			IdentityKey: "sym-записатьдвижения", ComponentID: "cfg", UID: "sym-записатьдвижения",
			ModuleID: modID, OriginFileID: fModule, Kind: "procedure",
			NameNorm: "записатьдвижения", NameDisplay: "ЗаписатьДвижения", IsExport: false, Span: sp(),
		})
		if err != nil {
			return err
		}

		evidenceCode := func(symbolID, fileID int64) string {
			return `{"chain":[{"symbolId":` + strconv.FormatInt(symbolID, 10) + `,"fileId":` + strconv.FormatInt(fileID, 10) + `,"confidence":1}]}`
		}
		ids.edgeCode1, err = tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.o1, ToObjectID: ids.o2, Kind: store.EdgeWritesRegister, Layer: "base",
			Provenance: store.EdgeProvenanceCode, Confidence: 1.0, Mode: "write",
			Evidence: evidenceCode(ids.symbolID, fModule), FileIDs: []int64{fModule},
		})
		if err != nil {
			return err
		}
		ids.edgeCode2, err = tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.o1, ToObjectID: ids.o3, Kind: store.EdgeWritesRegister, Layer: "base",
			Provenance: store.EdgeProvenanceCode, Confidence: 0.3, Mode: "write",
			Evidence: evidenceCode(ids.symbolID, fModule), FileIDs: []int64{fModule},
		})
		if err != nil {
			return err
		}
		ids.edgeSibling, err = tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.o5, ToObjectID: ids.o2, Kind: store.EdgeWritesRegister, Layer: "base",
			Provenance: store.EdgeProvenanceCode, Confidence: 0.9, Mode: "write",
			Evidence: evidenceCode(ids.symbolID, fModule), FileIDs: []int64{fModule},
		})
		if err != nil {
			return err
		}
		ids.edgeDeclared, err = tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.o1, ToObjectID: ids.o6, Kind: store.EdgeWritesDeclared, Layer: "base",
			Provenance: store.EdgeProvenanceDeclared, Confidence: 0.5, Mode: "movement",
			Evidence: `{"xmlFile":"Documents/ЗаказКлиента.xml","declaredRegister":"InformationRegister.СтавкиНалогов"}`,
			FileIDs:  []int64{fO1},
		})
		if err != nil {
			return err
		}

		if err := tx.InsertObjectBadge(store.ObjectBadge{ObjectID: ids.o1, Badge: "has-dynamic", Layer: "base", Count: 3}); err != nil {
			return err
		}
		if err := tx.InsertObjectBadge(store.ObjectBadge{ObjectID: ids.o1, Badge: "attribution-stale", Layer: "base", Count: 2}); err != nil {
			return err
		}
		if err := tx.InsertObjectBadge(store.ObjectBadge{ObjectID: ids.o2, Badge: "attribution-truncated", Layer: "base", Count: 1}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	return NewObjectGraphService(p, 0), ids
}

// TestObjectGraphNodeReturnsBadges — критерий: «Бейджи узла (has-dynamic со
// счётчиком) приезжают в карточке узла», и все ТРИ вида бейджей видны, не
// только has-dynamic (фильтр бейджей не должен терять ни
// один).
func TestObjectGraphNodeReturnsBadges(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	resp, err := svc.Node(context.Background(), NodeInput{Target: ObjectTarget{ObjectID: ids.o1}})
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if len(resp.Items) != 1 || resp.TotalCount != 1 {
		t.Fatalf("items = %+v, totalCount = %d", resp.Items, resp.TotalCount)
	}
	item := resp.Items[0]
	if item.NameDisplay != "ЗаказКлиента" || item.MType != "Document" {
		t.Fatalf("unexpected node: %+v", item)
	}
	badges := map[string]int64{}
	for _, b := range item.Badges {
		badges[b.Badge] = b.Count
	}
	if badges["has-dynamic"] != 3 || badges["attribution-stale"] != 2 {
		t.Fatalf("badges = %+v, want has-dynamic=3 and attribution-stale=2", badges)
	}
}

// TestObjectGraphNodeShowsAttributionTruncatedBadge — Node(O2) обязан
// показать attribution-truncated отдельно от has-dynamic/attribution-stale
// (TestObjectGraphNodeReturnsBadges проверял только O1,
// где этого бейджа нет вовсе — фильтрация по имени бейджа в nodeItemFrom
// оставалась непроверенной).
func TestObjectGraphNodeShowsAttributionTruncatedBadge(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	resp, err := svc.Node(context.Background(), NodeInput{Target: ObjectTarget{ObjectID: ids.o2}})
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %+v", resp.Items)
	}
	badges := map[string]int64{}
	for _, b := range resp.Items[0].Badges {
		badges[b.Badge] = b.Count
	}
	if badges["attribution-truncated"] != 1 {
		t.Fatalf("badges = %+v, want attribution-truncated=1", badges)
	}
}

// TestObjectGraphNodeByTypeAndName — вход MCP-инструмента object_graph
// (адресация объекта видом+именем, без готового id).
func TestObjectGraphNodeByTypeAndName(t *testing.T) {
	svc, _ := buildObjectGraphFixture(t)
	resp, err := svc.Node(context.Background(), NodeInput{Target: ObjectTarget{ObjectType: "Document", ObjectName: "ЗаказКлиента"}})
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].NameDisplay != "ЗаказКлиента" {
		t.Fatalf("items = %+v", resp.Items)
	}
}

// TestObjectGraphNodeNotFound — «объекта нет» обязано быть NotFound, не
// пустой список (§43).
func TestObjectGraphNodeNotFound(t *testing.T) {
	svc, _ := buildObjectGraphFixture(t)
	_, err := svc.Node(context.Background(), NodeInput{Target: ObjectTarget{ObjectID: 999999}})
	if err == nil {
		t.Fatalf("Node: ожидалась ошибка")
	}
	appErr, ok := err.(*Error)
	if !ok || appErr.Code != CodeNotFound {
		t.Fatalf("Node: err = %v, want CodeNotFound", err)
	}
}

// TestObjectGraphNeighborsEmptyVsNotFound — «объект есть, соседей нет»
// (200, пустой список, TotalCount=0) против «объекта нет» (NotFound) — §43,
// прямое требование критериев приёмки.
func TestObjectGraphNeighborsEmptyVsNotFound(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)

	// O3 существует, но у него нет исходящих рёбер (только входящее).
	resp, err := svc.Neighbors(context.Background(), NeighborsInput{ObjectID: ids.o3, Direction: store.EdgeDirectionOut})
	if err != nil {
		t.Fatalf("Neighbors(o3,out): %v", err)
	}
	if len(resp.Items) != 0 || resp.TotalCount != 0 {
		t.Fatalf("Neighbors(o3,out) = %+v, want empty", resp)
	}

	_, err = svc.Neighbors(context.Background(), NeighborsInput{ObjectID: 999999})
	if err == nil {
		t.Fatalf("Neighbors(несуществующий): ожидалась ошибка")
	}
	if appErr, ok := err.(*Error); !ok || appErr.Code != CodeNotFound {
		t.Fatalf("Neighbors(несуществующий): err = %v, want CodeNotFound", err)
	}
}

// TestObjectGraphNeighborsFilters — dir/kinds/minConfidence фильтруют выдачу
// (критерий: «Фильтры: dir, kinds, minConfidence, layer»).
func TestObjectGraphNeighborsFilters(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	ctx := context.Background()

	t.Run("dir=out видит только исходящие", func(t *testing.T) {
		resp, err := svc.Neighbors(ctx, NeighborsInput{ObjectID: ids.o1, Direction: store.EdgeDirectionOut})
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		if resp.TotalCount != 3 { // -> O2, O3, O6
			t.Fatalf("totalCount = %d, want 3", resp.TotalCount)
		}
	})

	t.Run("dir=in видит только входящие", func(t *testing.T) {
		resp, err := svc.Neighbors(ctx, NeighborsInput{ObjectID: ids.o2, Direction: store.EdgeDirectionIn})
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		if resp.TotalCount != 2 { // O1, O5 -> O2
			t.Fatalf("totalCount = %d, want 2", resp.TotalCount)
		}
	})

	t.Run("kinds=writes-declared отделяет декларацию от кода", func(t *testing.T) {
		resp, err := svc.Neighbors(ctx, NeighborsInput{
			ObjectID: ids.o1, Direction: store.EdgeDirectionOut, Kinds: []string{store.EdgeWritesDeclared},
		})
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		if resp.TotalCount != 1 || resp.Items[0].Kind != store.EdgeWritesDeclared {
			t.Fatalf("items = %+v, want ровно writes-declared", resp.Items)
		}
	})

	t.Run("minConfidence отсекает слабые рёбра", func(t *testing.T) {
		resp, err := svc.Neighbors(ctx, NeighborsInput{
			ObjectID: ids.o1, Direction: store.EdgeDirectionOut, MinConfidence: 0.5,
		})
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		// O1->O2 (1.0) и O1->O6 (0.5) проходят порог, O1->O3 (0.3) нет.
		if resp.TotalCount != 2 {
			t.Fatalf("totalCount = %d, want 2 (порог 0.5 отсекает O1->O3)", resp.TotalCount)
		}
		for _, it := range resp.Items {
			if it.ToObjectID == ids.o3 {
				t.Fatalf("edge to o3 (confidence 0.3) не должен пройти minConfidence=0.5")
			}
		}
	})
}

// TestObjectGraphNeighborsPagination — вторая страница не повторяет первую,
// курсор привязан к поколению (критерий: «страничность через clampLimit,
// EncodeCursor/DecodeCursor»).
func TestObjectGraphNeighborsPagination(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	ctx := context.Background()

	first, err := svc.Neighbors(ctx, NeighborsInput{ObjectID: ids.o1, Direction: store.EdgeDirectionOut, Limit: 1})
	if err != nil {
		t.Fatalf("Neighbors page1: %v", err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("page1 = %+v, want 1 item and a cursor", first)
	}
	second, err := svc.Neighbors(ctx, NeighborsInput{
		ObjectID: ids.o1, Direction: store.EdgeDirectionOut, Limit: 1, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("Neighbors page2: %v", err)
	}
	if len(second.Items) != 1 {
		t.Fatalf("page2 = %+v, want 1 item", second)
	}
	if second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("page2 повторяет page1: edge id %d", second.Items[0].ID)
	}
	if second.TotalCount != first.TotalCount {
		t.Fatalf("total разошёлся между страницами: %d vs %d", first.TotalCount, second.TotalCount)
	}
}

// TestObjectGraphRadiusTruncation — упор в потолок узлов даёт Warnings с
// кодом truncated, и режет по НАИМЕНЬШЕЙ confidence (§26, критерий: «признак,
// по которому резали — confidence»).
func TestObjectGraphRadiusTruncation(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	svc.radiusNodesCap = 2 // O1 + один сосед: O2(1.0) должен выжить, O6(0.5)/O3(0.3) — нет

	resp, err := svc.Radius(context.Background(), RadiusInput{
		Target: ObjectTarget{ObjectID: ids.o1}, Direction: store.EdgeDirectionOut, Depth: 1,
	})
	if err != nil {
		t.Fatalf("Radius: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ToObjectID != ids.o2 {
		t.Fatalf("items = %+v, want ровно ребро к o2 (confidence 1.0)", resp.Items)
	}
	// Текст предупреждения ЭТОЙ причины (потолок узлов) обязан утверждать
	// отбор по confidence — и НЕ содержать текст ДРУГОЙ причины (потолок
	// выборки, "по id"). Ревью качества (стоп-гейт): radiusTruncationWarnings
	// раньше связывала причину с текстом только позицией двух bool-аргументов
	// подряд — перепутанный порядок компилятор не ловил, а пакет оставался
	// зелёным, потому что ни один тест не сверял ТЕКСТ предупреждения с его
	// причиной.
	truncWarnings := withoutStaleIndex(resp.Warnings)
	if len(truncWarnings) != 1 {
		t.Fatalf("warnings = %+v, want ровно одно (только потолок узлов сработал)", resp.Warnings)
	}
	w := truncWarnings[0]
	if w.Code != "truncated" {
		t.Fatalf("warning.Code = %q, want truncated", w.Code)
	}
	if !strings.Contains(w.Message, "потолке 2 узлов") {
		t.Fatalf("warning.Message = %q, want упоминание потолка узлов (2)", w.Message)
	}
	if !strings.Contains(w.Hint, "confidence") || strings.Contains(w.Hint, "по id") {
		t.Fatalf("warning.Hint = %q, want признак confidence, НЕ текст причины «по id»", w.Hint)
	}
}

// TestObjectGraphTargetBothSetIsRejected — ObjectTarget с ОБОИМИ способами
// адресации сразу (id и вид+имя) обязан быть отклонён на входе, а не молча
// выбрать один из них. Проверяется на Node И Radius — оба принимают
// ObjectTarget; Neighbors адресуется только готовым int64 ObjectID и этой
// неоднозначности физически не имеет (ревью качества, стоп-гейт).
func TestObjectGraphTargetBothSetIsRejected(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	both := ObjectTarget{ObjectID: ids.o1, ObjectType: "Document", ObjectName: "ЗаказКлиента"}
	// Сообщение проверяется дословно, а не просто "err != nil": и id, и
	// type+name существуют в фикстуре по-настоящему, поэтому при снятой
	// проверке normalizeObjectTarget вызов тихо резолвится по id и
	// УСПЕШНО находит объект — err==nil, а не какая-то другая ошибка.
	const wantSubstr = "не оба сразу"

	_, err := svc.Node(context.Background(), NodeInput{Target: both})
	if err == nil || !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("Node(id+type+name): err = %v, want содержит %q", err, wantSubstr)
	}
	_, err = svc.Radius(context.Background(), RadiusInput{Target: both})
	if err == nil || !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("Radius(id+type+name): err = %v, want содержит %q", err, wantSubstr)
	}
}

// TestObjectGraphTargetEmptyIsRejected — полностью пустой ObjectTarget (ни
// id, ни вид+имя) — тоже ошибка валидации, не «объект не найден» и не паника
// на пустом идентификаторе.
func TestObjectGraphTargetEmptyIsRejected(t *testing.T) {
	svc, _ := buildObjectGraphFixture(t)
	// Сообщение проверяется дословно: без явного отказа пустой ObjectTarget
	// тихо проваливается в резолв по type+name="" и возвращает NotFound
	// (объект с таким именем не существует) — err тоже != nil, но это
	// СОВСЕМ другая ошибка («не найдено» вместо «цель не задана»), и тест
	// на "err == nil" её не отличил бы.
	const wantSubstr = "цель не задана"

	_, err := svc.Node(context.Background(), NodeInput{Target: ObjectTarget{}})
	if err == nil || !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("Node(пустой target): err = %v, want содержит %q", err, wantSubstr)
	}
	_, err = svc.Radius(context.Background(), RadiusInput{Target: ObjectTarget{}})
	if err == nil || !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("Radius(пустой target): err = %v, want содержит %q", err, wantSubstr)
	}
}

// TestObjectGraphRadiusTotalCountIsFullResultNotPage — TotalCount обязан
// отражать размер ВСЕГО обрезанного потолком узлов результата, а не длину
// одной страницы (стоп-гейт: до фикса TotalCount=len(res.edges) считался
// ПОСЛЕ обрезки по limit, расходясь с Neighbors, где total — честный
// tx.CountObjectDataEdges).
func TestObjectGraphRadiusTotalCountIsFullResultNotPage(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	// O1 --out--> {O2, O3, O6}: 3 ребра при depth=1, dir=out.
	resp, err := svc.Radius(context.Background(), RadiusInput{
		Target: ObjectTarget{ObjectID: ids.o1}, Direction: store.EdgeDirectionOut, Depth: 1, Limit: 1,
	})
	if err != nil {
		t.Fatalf("Radius: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("len(items) = %d, want 1 (страница)", len(resp.Items))
	}
	if resp.TotalCount != 3 {
		t.Fatalf("TotalCount = %d, want 3 (полный обрезанный потолком узлов результат, не страница)", resp.TotalCount)
	}
}

// TestObjectGraphRadiusFetchLimitTruncationIsNotSilent — упор во ВТОРОЙ,
// отдельный от radiusNodesCap потолок (radiusFetchLimit — сколько рёбер
// ОДНОГО узла читается за раз) тоже обязан поднять Warnings с кодом
// truncated, и текст обязан честно не обещать отбор по confidence: на этом
// потолке store.ObjectDataEdges режет по id (её собственная курсорная
// пагинация), не по достоверности (стоп-гейт, пункт 3б).
func TestObjectGraphRadiusFetchLimitTruncationIsNotSilent(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	svc.radiusFetchLimit = 2 // O1 --out--> 3 ребра (id 1,2,4): третье не влезет

	resp, err := svc.Radius(context.Background(), RadiusInput{
		Target: ObjectTarget{ObjectID: ids.o1}, Direction: store.EdgeDirectionOut, Depth: 1,
	})
	if err != nil {
		t.Fatalf("Radius: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items = %+v, want ровно 2 (обрезано по id, не по confidence)", resp.Items)
	}
	for _, it := range resp.Items {
		if it.ToObjectID == ids.o6 {
			t.Fatalf("ребро к o6 (id вставки позже) не должно было влезть при radiusFetchLimit=2")
		}
	}
	// Текст ЭТОЙ причины (потолок выборки одного узла) обязан честно
	// признавать отбор по id, а НЕ обещать confidence — иначе перепутанный
	// текст (см. TestObjectGraphRadiusTruncation) прошёл бы незамеченным.
	truncWarnings := withoutStaleIndex(resp.Warnings)
	if len(truncWarnings) != 1 {
		t.Fatalf("warnings = %+v, want ровно одно (только потолок выборки сработал)", resp.Warnings)
	}
	w := truncWarnings[0]
	if w.Code != "truncated" {
		t.Fatalf("warning.Code = %q, want truncated", w.Code)
	}
	if !strings.Contains(w.Message, "больше 2 за один шаг") {
		t.Fatalf("warning.Message = %q, want упоминание потолка выборки (2)", w.Message)
	}
	if !strings.Contains(w.Hint, "по id") || !strings.Contains(w.Hint, "НЕ по confidence") {
		t.Fatalf("warning.Hint = %q, want честное «по id, НЕ по confidence», а не признак confidence", w.Hint)
	}
}

// TestObjectGraphGodNodesRanksByFanIn — «GodNodes считает fan-in и fan-out по
// рёбрам, фильтруется по типу объекта, отдаёт топ N».
func TestObjectGraphGodNodesRanksByFanIn(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	resp, err := svc.GodNodes(context.Background(), GodNodesInput{
		By: store.GodNodeByFanIn, MTypes: []string{"AccumulationRegister"}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("GodNodes: %v", err)
	}
	if len(resp.Items) == 0 || resp.Items[0].ObjectID != ids.o2 || resp.Items[0].FanIn != 2 {
		t.Fatalf("items = %+v, want o2 первым с fanIn=2", resp.Items)
	}

	// Без фильтра по типу у O1 самый большой fanOut (3) и, соответственно,
	// самый большой total (fanIn+fanOut) — top по умолчанию (By="") был бы
	// o1. Ось fan-in обязана вернуть o2 (fanIn=2) первым несмотря на это:
	// проверяет, что By реально управляет сортировкой, а не просто
	// присутствует в фильтре.
	byFanIn, err := svc.GodNodes(context.Background(), GodNodesInput{By: store.GodNodeByFanIn, Limit: 5})
	if err != nil {
		t.Fatalf("GodNodes(by=fan-in, без фильтра): %v", err)
	}
	if len(byFanIn.Items) == 0 || byFanIn.Items[0].ObjectID != ids.o2 {
		t.Fatalf("items = %+v, want o2 первым по fan-in (o1 первым по total из-за fanOut=3)", byFanIn.Items)
	}
}

// TestObjectGraphGodNodesUnknownMetricIsInputError — опечатка в оси
// сортировки — ошибка ВВОДА, а не внутренний сбой: неизвестное значение
// доезжало до store и возвращалось обычной fmt.Errorf, из-за чего HTTP-карта
// отдавала на него 500 internal. Ось разбирается на шве app — там же, где
// direction (normalizeDirection) и view (parseView).
func TestObjectGraphGodNodesUnknownMetricIsInputError(t *testing.T) {
	svc, _ := buildObjectGraphFixture(t)
	_, err := svc.GodNodes(context.Background(), GodNodesInput{By: "writers", Limit: 5})
	if err == nil {
		t.Fatal(`GodNodes(By: "writers") = nil error, want ошибку про неизвестную ось`)
	}
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %v (%T), want *app.Error", err, err)
	}
	if appErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %s, want %s", appErr.Code, CodeInvalidArgument)
	}
	if !strings.Contains(appErr.Message, `"writers"`) {
		t.Fatalf("Message = %q, want упоминание отвергнутого значения", appErr.Message)
	}
	for _, want := range []string{store.GodNodeByFanIn, store.GodNodeByFanOut, store.GodNodeByTotal} {
		if !strings.Contains(appErr.Hint, want) {
			t.Fatalf("Hint = %q, want перечисление допустимых значений (нет %q)", appErr.Hint, want)
		}
	}
}

// TestObjectGraphGodNodesKnownMetricsStayValid — разбор оси не ломает ни одно
// из трёх допустимых значений и оставляет пустое умолчанием total.
func TestObjectGraphGodNodesKnownMetricsStayValid(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	for _, by := range []string{"", store.GodNodeByTotal, store.GodNodeByFanIn, store.GodNodeByFanOut} {
		resp, err := svc.GodNodes(context.Background(), GodNodesInput{By: by, Limit: 5})
		if err != nil {
			t.Fatalf("GodNodes(By: %q): %v", by, err)
		}
		if len(resp.Items) == 0 {
			t.Fatalf("GodNodes(By: %q): items пуст", by)
		}
	}
	// Пустая ось обязана остаться сортировкой по сумме: у O1 самый большой
	// total (fanOut=3), тогда как по fan-in первым идёт O2.
	byDefault, err := svc.GodNodes(context.Background(), GodNodesInput{Limit: 5})
	if err != nil {
		t.Fatalf(`GodNodes(By: ""): %v`, err)
	}
	if len(byDefault.Items) == 0 || byDefault.Items[0].ObjectID != ids.o1 {
		t.Fatalf("items = %+v, want o1 первым по умолчанию (total)", byDefault.Items)
	}
}

// TestObjectGraphEdgeEvidenceCode — «EdgeEvidence отдаёт цепочку атрибуции
// для кодовых рёбер» (§32).
func TestObjectGraphEdgeEvidenceCode(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	resp, err := svc.EdgeEvidence(context.Background(), EdgeEvidenceInput{EdgeID: ids.edgeCode1})
	if err != nil {
		t.Fatalf("EdgeEvidence: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %+v", resp.Items)
	}
	item := resp.Items[0]
	if len(item.Chain) != 1 || item.Chain[0].SymbolName != "ЗаписатьДвижения" {
		t.Fatalf("chain = %+v, want звено с именем ЗаписатьДвижения", item.Chain)
	}
}

// TestObjectGraphEdgeEvidenceDeclared — «...ссылку на XML для декларированных».
func TestObjectGraphEdgeEvidenceDeclared(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	resp, err := svc.EdgeEvidence(context.Background(), EdgeEvidenceInput{EdgeID: ids.edgeDeclared})
	if err != nil {
		t.Fatalf("EdgeEvidence: %v", err)
	}
	item := resp.Items[0]
	if item.XMLFile != "Documents/ЗаказКлиента.xml" || item.DeclaredRegister != "InformationRegister.СтавкиНалогов" {
		t.Fatalf("item = %+v, want заполненные xmlFile/declaredRegister", item)
	}
	if len(item.Files) != 1 || item.Files[0].RelPath != "Documents/ЗаказКлиента.xml" {
		t.Fatalf("files = %+v", item.Files)
	}
}

// TestObjectGraphEdgeEvidenceNotFound — несуществующее ребро — NotFound, не
// пустой ответ.
func TestObjectGraphEdgeEvidenceNotFound(t *testing.T) {
	svc, _ := buildObjectGraphFixture(t)
	_, err := svc.EdgeEvidence(context.Background(), EdgeEvidenceInput{EdgeID: 999999})
	if err == nil {
		t.Fatalf("EdgeEvidence: ожидалась ошибка")
	}
	if appErr, ok := err.(*Error); !ok || appErr.Code != CodeNotFound {
		t.Fatalf("EdgeEvidence: err = %v, want CodeNotFound", err)
	}
}

// withoutStaleIndex убирает предупреждение о свежести: сид этого файла пишет
// store мимо пайплайна, эпоха честно несёт признак полной пересборки, и
// stale_index в ответе штатен (ADR-036). Тесты усечения считают только свои
// предупреждения.
func withoutStaleIndex(ws []Warning) []Warning {
	var out []Warning
	for _, w := range ws {
		if w.Code != "stale_index" {
			out = append(out, w)
		}
	}
	return out
}
