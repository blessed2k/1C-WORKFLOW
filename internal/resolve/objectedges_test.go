package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// graphFixture — граф вызовов и владельцы модулей, собранные в коде: те же
// величины, которые internal/index читает из store (call_edge, module,
// module.owner_object_id), без самого store.
type graphFixture struct {
	callers map[int64][]SymbolCall
	owners  map[int64]SymbolOwner
}

func newGraph() *graphFixture {
	return &graphFixture{callers: map[int64][]SymbolCall{}, owners: map[int64]SymbolOwner{}}
}

// symbolInObject объявляет символ в модуле объекта-владельца ownerObjectID.
func (g *graphFixture) symbolInObject(symbolID, ownerObjectID, fileID int64) *graphFixture {
	g.owners[symbolID] = SymbolOwner{ModuleKind: "object", OwnerObjectID: ownerObjectID, FileID: fileID}
	return g
}

// symbolInCommon объявляет символ в общем модуле: владелец модуля есть
// (объект ОбщийМодуль), но владельцем данных он не считается (D6, docs/architecture-graph.md).
func (g *graphFixture) symbolInCommon(symbolID, commonModuleObjectID, fileID int64) *graphFixture {
	g.owners[symbolID] = SymbolOwner{ModuleKind: "common", OwnerObjectID: commonModuleObjectID, FileID: fileID}
	return g
}

// calledFrom: callerID зовёт calleeID с достоверностью conf.
func (g *graphFixture) calledFrom(calleeID, callerID int64, conf float64) *graphFixture {
	g.callers[calleeID] = append(g.callers[calleeID], SymbolCall{CallerID: callerID, Confidence: conf})
	return g
}

func (g *graphFixture) CallersOf(symbolID int64) []SymbolCall { return g.callers[symbolID] }

func (g *graphFixture) SymbolOwner(symbolID int64) (SymbolOwner, bool) {
	o, ok := g.owners[symbolID]
	return o, ok
}

func boolPtr(v bool) *bool { return &v }

// movement — строка register_access о записи движений: статическая, с
// разрешённым регистром.
func movement(symbolID, fileID, registerObjectID int64, conf float64) store.RegisterAccessRow {
	return store.RegisterAccessRow{
		FileID: fileID, SymbolID: symbolID, ObjectID: registerObjectID,
		RegisterNameNorm: "товарыорганизаций", Mode: "movement", Static: true,
		InTransaction: boolPtr(true), Confidence: conf,
		Span: domain.Span{StartByte: 10, EndByte: 40},
	}
}

// TestDeriveObjectDataEdgesDirectWrite: запись прямо из модуля
// объекта даёт ребро writes-register от документа к регистру, mode и
// in_transaction переносятся из породившей строки register_access.
func TestDeriveObjectDataEdgesDirectWrite(t *testing.T) {
	const обработкаПроведения, документ, регистр, файл = 10, 100, 200, 1000
	g := newGraph().symbolInObject(обработкаПроведения, документ, файл)

	edges, badges := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(обработкаПроведения, файл, регистр, 1.0)},
		Graph:    g,
	})

	if len(badges) != 0 {
		t.Fatalf("бейджи %+v, ожидались пустыми: динамики тут нет", badges)
	}
	if len(edges) != 1 {
		t.Fatalf("рёбра %+v, ожидалось ровно одно", edges)
	}
	e := edges[0]
	if e.FromObjectID != документ || e.ToObjectID != регистр {
		t.Errorf("ребро %d->%d, ожидалось %d->%d", e.FromObjectID, e.ToObjectID, документ, регистр)
	}
	if e.Kind != store.EdgeWritesRegister || e.Provenance != store.EdgeProvenanceCode {
		t.Errorf("kind/provenance = %s/%s, ожидались %s/%s", e.Kind, e.Provenance,
			store.EdgeWritesRegister, store.EdgeProvenanceCode)
	}
	if e.Confidence != 1.0 {
		t.Errorf("confidence = %v, ожидалась 1.0 (цепочка из одного звена)", e.Confidence)
	}
	if e.Mode != "movement" || e.InTransaction == nil || !*e.InTransaction {
		t.Errorf("mode/in_transaction = %q/%v, ожидались movement/true из строки register_access",
			e.Mode, e.InTransaction)
	}
	if len(e.FileIDs) != 1 || e.FileIDs[0] != файл {
		t.Errorf("файловые зависимости %v, ожидался один файл %d", e.FileIDs, файл)
	}
	if len(e.Chain) != 1 || e.Chain[0].SymbolID != обработкаПроведения {
		t.Errorf("цепочка %+v, ожидался один шаг с символом %d", e.Chain, обработкаПроведения)
	}
}

// TestDeriveObjectDataEdgesThroughCommonModule: запись
// сделана в общем модуле, а приписывается документу, из которого его позвали.
// Общий модуль узлом графа не становится (решение D6, docs/architecture-graph.md).
func TestDeriveObjectDataEdgesThroughCommonModule(t *testing.T) {
	const обработкаПроведения, записьДвижений = int64(10), int64(20)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлДокумента, файлОбщегоМодуля = int64(1000), int64(2000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(записьДвижений, общийМодуль, файлОбщегоМодуля).
		calledFrom(записьДвижений, обработкаПроведения, 1.0)

	edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(записьДвижений, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
	})

	if len(edges) != 1 {
		t.Fatalf("рёбра %+v, ожидалось ровно одно (от документа)", edges)
	}
	e := edges[0]
	if e.FromObjectID != документ {
		t.Errorf("ребро идёт от %d, ожидался документ %d, а не общий модуль %d",
			e.FromObjectID, документ, общийМодуль)
	}
	if e.ToObjectID != регистр {
		t.Errorf("ребро ведёт в %d, ожидался регистр %d", e.ToObjectID, регистр)
	}
	if len(e.Chain) != 2 || e.Chain[0].SymbolID != обработкаПроведения || e.Chain[1].SymbolID != записьДвижений {
		t.Errorf("цепочка %+v, ожидались два шага: %d (владелец) -> %d (факт)",
			e.Chain, обработкаПроведения, записьДвижений)
	}
	if len(e.FileIDs) != 2 {
		t.Errorf("файловые зависимости %v, ожидались оба файла цепочки (%d и %d)",
			e.FileIDs, файлДокумента, файлОбщегоМодуля)
	}
}

// TestDeriveObjectDataEdgesHubWithRegisterParameter: в
// хаб регистр приходит параметром, статически он не разрешён. Выдуманного
// ребра быть не должно, но дыра обязана быть видна: владелец получает бейдж
// has-dynamic со счётчиком.
func TestDeriveObjectDataEdgesHubWithRegisterParameter(t *testing.T) {
	const обработкаПроведения, записатьДвижения = int64(10), int64(30)
	const документ, общийМодуль = int64(100), int64(300)
	const файлДокумента, файлХаба = int64(1000), int64(3000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(записатьДвижения, общийМодуль, файлХаба).
		calledFrom(записатьДвижения, обработкаПроведения, 1.0)

	динамика := movement(записатьДвижения, файлХаба, 0, 0.5)
	динамика.Static = false
	динамика.RegisterNameNorm = ""

	edges, badges := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{динамика},
		Graph:    g,
	})

	if len(edges) != 0 {
		t.Fatalf("рёбра %+v, ожидались пустыми: регистр приходит параметром", edges)
	}
	if len(badges) != 1 {
		t.Fatalf("бейджи %+v, ожидался ровно один", badges)
	}
	b := badges[0]
	if b.ObjectID != документ || b.Badge != BadgeHasDynamic || b.Count != 1 {
		t.Errorf("бейдж %+v, ожидался %s на документе %d со счётчиком 1",
			b, BadgeHasDynamic, документ)
	}
}

// TestDeriveObjectDataEdgesCycleTerminates: цикл A->B->A в графе
// вызовов не вешает сборку и не удваивает ребро.
func TestDeriveObjectDataEdgesCycleTerminates(t *testing.T) {
	const обработкаПроведения, a, b = int64(10), int64(40), int64(50)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлДокумента, файлОбщегоМодуля = int64(1000), int64(2000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(a, общийМодуль, файлОбщегоМодуля).
		symbolInCommon(b, общийМодуль, файлОбщегоМодуля).
		calledFrom(a, b, 1.0).
		calledFrom(b, a, 1.0).
		calledFrom(b, обработкаПроведения, 1.0)

	edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(a, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
	})

	if len(edges) != 1 {
		t.Fatalf("рёбра %+v, ожидалось ровно одно, несмотря на цикл A->B->A", edges)
	}
	if edges[0].FromObjectID != документ {
		t.Errorf("ребро идёт от %d, ожидался документ %d", edges[0].FromObjectID, документ)
	}
}

// TestDeriveObjectDataEdgesMinConfidenceAlongChain:
// достоверность ребра это минимум по цепочке. Факт разобран точно (1.0),
// вызов разрешён с 0.6 — ребро получает 0.6.
func TestDeriveObjectDataEdgesMinConfidenceAlongChain(t *testing.T) {
	const обработкаПроведения, записьДвижений = int64(10), int64(20)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлДокумента, файлОбщегоМодуля = int64(1000), int64(2000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(записьДвижений, общийМодуль, файлОбщегоМодуля).
		calledFrom(записьДвижений, обработкаПроведения, 0.6)

	edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(записьДвижений, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
	})

	if len(edges) != 1 {
		t.Fatalf("рёбра %+v, ожидалось ровно одно", edges)
	}
	if edges[0].Confidence != 0.6 {
		t.Errorf("confidence = %v, ожидалась 0.6: минимум по цепочке (1.0 и 0.6)", edges[0].Confidence)
	}
}

// TestDeriveObjectDataEdgesCollapsesDuplicateChains: два
// разных пути к одному факту дают ОДНО ребро с максимальным confidence, а в
// evidence лежит кратчайшая из цепочек.
func TestDeriveObjectDataEdgesCollapsesDuplicateChains(t *testing.T) {
	const короткийПуть, длинныйПуть = int64(10), int64(11)
	const записьДвижений, помощник = int64(60), int64(70)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлДокумента, файлОбщегоМодуля = int64(1000), int64(2000)

	g := newGraph().
		symbolInObject(короткийПуть, документ, файлДокумента).
		symbolInObject(длинныйПуть, документ, файлДокумента).
		symbolInCommon(записьДвижений, общийМодуль, файлОбщегоМодуля).
		symbolInCommon(помощник, общийМодуль, файлОбщегоМодуля).
		calledFrom(записьДвижений, короткийПуть, 0.5).
		calledFrom(записьДвижений, помощник, 1.0).
		calledFrom(помощник, длинныйПуть, 1.0)

	edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(записьДвижений, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
	})

	if len(edges) != 1 {
		t.Fatalf("рёбра %+v, ожидалось одно: два пути ведут к одному факту", edges)
	}
	e := edges[0]
	if e.Confidence != 1.0 {
		t.Errorf("confidence = %v, ожидался максимум по путям (1.0 против 0.5)", e.Confidence)
	}
	if len(e.Chain) != 2 || e.Chain[0].SymbolID != короткийПуть {
		t.Errorf("цепочка %+v, ожидалась кратчайшая: %d -> %d", e.Chain, короткийПуть, записьДвижений)
	}
}

// TestDeriveObjectDataEdgesDepthPenaltyKeepsEdge:
// цепочка длиннее порога глубины СНИЖАЕТ достоверность, но ребро остаётся.
// Порог 2, штраф 0.5, звеньев три: одно звено сверх порога даёт 1.0*0.5.
func TestDeriveObjectDataEdgesDepthPenaltyKeepsEdge(t *testing.T) {
	const обработкаПроведения = int64(10)
	const звено1, звено2, фактныйСимвол = int64(21), int64(22), int64(23)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлДокумента, файлОбщегоМодуля = int64(1000), int64(2000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(звено1, общийМодуль, файлОбщегоМодуля).
		symbolInCommon(звено2, общийМодуль, файлОбщегоМодуля).
		symbolInCommon(фактныйСимвол, общийМодуль, файлОбщегоМодуля).
		calledFrom(фактныйСимвол, звено2, 1.0).
		calledFrom(звено2, звено1, 1.0).
		calledFrom(звено1, обработкаПроведения, 1.0)

	edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(фактныйСимвол, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
		Tunables: ObjectEdgeTunables{ChainDepth: 2, DepthPenalty: 0.5, HubFanIn: 50, HubPenalty: 0.7},
	})

	if len(edges) != 1 {
		t.Fatalf("рёбра %+v, ожидалось одно: порог глубины штрафует, а не режет", edges)
	}
	if edges[0].Confidence != 0.5 {
		t.Errorf("confidence = %v, ожидалась 0.5: одно звено сверх порога глубины", edges[0].Confidence)
	}
}

// TestDeriveObjectDataEdgesHubFanInPenaltyKeepsEdge: проход через
// процедуру-хаб (fan-in выше порога) снижает
// достоверность, ребро при этом не исчезает.
func TestDeriveObjectDataEdgesHubFanInPenaltyKeepsEdge(t *testing.T) {
	const обработкаПроведения = int64(10)
	const хаб, прочийВызов1, прочийВызов2 = int64(80), int64(81), int64(82)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлДокумента, файлОбщегоМодуля = int64(1000), int64(2000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(хаб, общийМодуль, файлОбщегоМодуля).
		symbolInCommon(прочийВызов1, общийМодуль, файлОбщегоМодуля).
		symbolInCommon(прочийВызов2, общийМодуль, файлОбщегоМодуля).
		calledFrom(хаб, обработкаПроведения, 1.0).
		calledFrom(хаб, прочийВызов1, 1.0).
		calledFrom(хаб, прочийВызов2, 1.0)

	edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(хаб, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
		Tunables: ObjectEdgeTunables{ChainDepth: 6, DepthPenalty: 0.9, HubFanIn: 2, HubPenalty: 0.5},
	})

	if len(edges) != 1 {
		t.Fatalf("рёбра %+v, ожидалось одно: хаб штрафует достоверность, а не убирает ребро", edges)
	}
	if edges[0].Confidence != 0.5 {
		t.Errorf("confidence = %v, ожидалась 0.5: проход через хаб с fan-in 3 при пороге 2",
			edges[0].Confidence)
	}
}

// TestDeriveObjectDataEdgesChainWithoutOwnerGivesNothing — решение п.7: цепочка,
// не дошедшая до объекта-владельца, ребра не даёт, и это не ошибка. Ответом на
// такой случай служит ребро writes-declared из метаданных (строит internal/index).
func TestDeriveObjectDataEdgesChainWithoutOwnerGivesNothing(t *testing.T) {
	const записьДвижений, общийМодуль, регистр, файл = int64(20), int64(300), int64(200), int64(2000)

	g := newGraph().symbolInCommon(записьДвижений, общийМодуль, файл)

	edges, badges := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(записьДвижений, файл, регистр, 1.0)},
		Graph:    g,
	})

	if len(edges) != 0 {
		t.Errorf("рёбра %+v, ожидались пустыми: общий модуль владельцем данных не считается", edges)
	}
	if len(badges) != 0 {
		t.Errorf("бейджи %+v, ожидались пустыми: недостроенная статическая цепочка не дыра динамики", badges)
	}
}

// TestDeriveObjectDataEdgesReadAndWriteStaySeparate — критерий приёмки: mode
// переносится из строки register_access, чтение и запись в одно ребро не
// сливаются.
func TestDeriveObjectDataEdgesReadAndWriteStaySeparate(t *testing.T) {
	const метод, документ, регистр, файл = int64(10), int64(100), int64(200), int64(1000)

	g := newGraph().symbolInObject(метод, документ, файл)

	чтение := movement(метод, файл, регистр, 1.0)
	чтение.Mode = "read"
	чтение.InTransaction = nil

	edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(метод, файл, регистр, 1.0), чтение},
		Graph:    g,
	})

	if len(edges) != 2 {
		t.Fatalf("рёбра %+v, ожидалось два: чтение и запись — разные рёбра", edges)
	}
	kinds := map[string]string{}
	for _, e := range edges {
		kinds[e.Kind] = e.Mode
	}
	if kinds[store.EdgeReadsRegister] != "read" || kinds[store.EdgeWritesRegister] != "movement" {
		t.Errorf("виды рёбер и режимы %v, ожидались %s/read и %s/movement",
			kinds, store.EdgeReadsRegister, store.EdgeWritesRegister)
	}
}

// TestDeriveObjectDataEdgesTruncationIsVisible: правило «не резать рёбра молча»
// касается и жёстких потолков обхода, а не только мягких порогов. Владелец
// стоит за пределом attributionCallersPerNode: ребра не будет, но след
// усечения обязан остаться, иначе связь исчезает бесследно и исход зависит от
// порядка строк CallEdgesTo.
func TestDeriveObjectDataEdgesTruncationIsVisible(t *testing.T) {
	const записьДвижений, общийМодуль, регистр = int64(20), int64(300), int64(200)
	const обработкаПроведения, документ = int64(999), int64(100)
	const файлОбщегоМодуля, файлДокумента = int64(2000), int64(1000)

	g := newGraph().
		symbolInCommon(записьДвижений, общийМодуль, файлОбщегоМодуля).
		symbolInObject(обработкаПроведения, документ, файлДокумента)
	// 200 вызывающих-пустышек, и владелец 201-м: ровно за краем потолка.
	for i := int64(1); i <= attributionCallersPerNode; i++ {
		g.symbolInCommon(500+i, общийМодуль, файлОбщегоМодуля).calledFrom(записьДвижений, 500+i, 1.0)
	}
	g.calledFrom(записьДвижений, обработкаПроведения, 1.0)

	edges, badges := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(записьДвижений, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
	})

	var усечение *ObjectBadge
	for i := range badges {
		if badges[i].Badge == BadgeAttributionTruncated {
			усечение = &badges[i]
		}
	}
	if len(edges) == 0 && усечение == nil {
		t.Fatalf("ни ребра, ни признака усечения: потолок обхода срезал цепочку молча (бейджи %+v)", badges)
	}
	if усечение == nil {
		t.Fatalf("бейджи %+v, ожидался %s: список вызывающих был обрезан", badges, BadgeAttributionTruncated)
	}
	if усечение.ObjectID != общийМодуль || усечение.Count != 1 {
		t.Errorf("бейдж усечения %+v, ожидался на владельце модуля с фактом (%d) со счётчиком 1",
			*усечение, общийМодуль)
	}
}

// цепочкаИзЗвеньев собирает граф, где от факта в общем модуле до метода
// объекта ровно links звеньев вызова, и отдаёт строку register_access факта.
func цепочкаИзЗвеньев(links int) (*graphFixture, store.RegisterAccessRow) {
	const фактныйСимвол, обработкаПроведения = int64(20), int64(10)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлОбщегоМодуля, файлДокумента = int64(2000), int64(1000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(фактныйСимвол, общийМодуль, файлОбщегоМодуля)
	текущий := фактныйСимвол
	for i := 1; i < links; i++ {
		промежуточный := int64(50 + i)
		g.symbolInCommon(промежуточный, общийМодуль, файлОбщегоМодуля).
			calledFrom(текущий, промежуточный, 1.0)
		текущий = промежуточный
	}
	g.calledFrom(текущий, обработкаПроведения, 1.0)
	return g, movement(фактныйСимвол, файлОбщегоМодуля, регистр, 1.0)
}

// хабСВызывающими собирает граф, где факт лежит в процедуре с fanIn
// вызывающими, из которых один — метод объекта.
func хабСВызывающими(fanIn int) (*graphFixture, store.RegisterAccessRow) {
	const хаб, обработкаПроведения = int64(20), int64(10)
	const документ, общийМодуль, регистр = int64(100), int64(300), int64(200)
	const файлОбщегоМодуля, файлДокумента = int64(2000), int64(1000)

	g := newGraph().
		symbolInObject(обработкаПроведения, документ, файлДокумента).
		symbolInCommon(хаб, общийМодуль, файлОбщегоМодуля).
		calledFrom(хаб, обработкаПроведения, 1.0)
	for i := 1; i < fanIn; i++ {
		прочий := int64(500 + i)
		g.symbolInCommon(прочий, общийМодуль, файлОбщегоМодуля).calledFrom(хаб, прочий, 1.0)
	}
	return g, movement(хаб, файлОбщегоМодуля, регистр, 1.0)
}

// TestObjectEdgeDefaultTunables закрепляет четыре дефолта resolve поведением:
// глубина 6, штраф за глубину 0.9, хаб от 50 вызывающих, штраф за хаб 0.7.
// Те же числа стоят дефолтами флагов -graph-* в cmd/mcp1c/options.go, и
// сверить их одним тестом нельзя: cmd не имеет права импортировать resolve
// (правило cmd-through-app в internal/arch). Односторонняя правка обязана
// красить один из двух тестов, поэтому этот существует отдельно.
func TestObjectEdgeDefaultTunables(t *testing.T) {
	type случай struct {
		имя        string
		граф       *graphFixture
		строка     store.RegisterAccessRow
		confidence float64
	}
	g6, r6 := цепочкаИзЗвеньев(6)
	g7, r7 := цепочкаИзЗвеньев(7)
	g50, r50 := хабСВызывающими(50)
	g51, r51 := хабСВызывающими(51)

	for _, c := range []случай{
		{"цепочка ровно в порог глубины 6", g6, r6, 1.0},
		{"седьмое звено штрафуется на 0.9", g7, r7, 0.9},
		{"fan-in 50 хабом ещё не считается", g50, r50, 1.0},
		{"fan-in 51 штрафуется на 0.7", g51, r51, 0.7},
	} {
		edges, _ := DeriveObjectDataEdges(ObjectEdgeInput{
			Accesses: []store.RegisterAccessRow{c.строка},
			Graph:    c.граф,
		})
		if len(edges) != 1 {
			t.Fatalf("%s: рёбра %+v, ожидалось одно", c.имя, edges)
		}
		if edges[0].Confidence != c.confidence {
			t.Errorf("%s: confidence = %v, ожидалась %v", c.имя, edges[0].Confidence, c.confidence)
		}
	}
}

// TestDeriveObjectDataEdgesNodeBudgetTruncationIsVisible — вторая половина
// того же правила для жёстких потолков: обход упирается не в число вызывающих одной
// вершины (их везде ровно attributionCallersPerNode, то есть потолок не
// превышен), а в бюджет узлов attributionNodeBudget. Владелец остаётся в
// неразвёрнутом фронте, ребра нет — след усечения обязан быть.
func TestDeriveObjectDataEdgesNodeBudgetTruncationIsVisible(t *testing.T) {
	const записьДвижений, общийМодуль, регистр = int64(20), int64(300), int64(200)
	const обработкаПроведения, документ = int64(10), int64(100)
	const файлОбщегоМодуля, файлДокумента = int64(2000), int64(1000)

	g := newGraph().
		symbolInCommon(записьДвижений, общийМодуль, файлОбщегоМодуля).
		symbolInObject(обработкаПроведения, документ, файлДокумента)

	// Три уровня по attributionCallersPerNode вершин: 1+200+200+200 больше
	// бюджета узлов, и на четвёртом шаге обход обрывается. Ветвится каждый
	// раз ровно одна вершина уровня, остальные — тупики.
	ветвь := func(вершина int64, первыйID int64, последний int64) {
		for i := int64(0); i < attributionCallersPerNode-1; i++ {
			g.symbolInCommon(первыйID+i, общийМодуль, файлОбщегоМодуля).
				calledFrom(вершина, первыйID+i, 1.0)
		}
		g.calledFrom(вершина, последний, 1.0)
	}
	ветвь(записьДвижений, 10000, 11000)
	g.symbolInCommon(11000, общийМодуль, файлОбщегоМодуля)
	ветвь(11000, 20000, 21000)
	g.symbolInCommon(21000, общийМодуль, файлОбщегоМодуля)
	ветвь(21000, 30000, обработкаПроведения)

	edges, badges := DeriveObjectDataEdges(ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{movement(записьДвижений, файлОбщегоМодуля, регистр, 1.0)},
		Graph:    g,
	})

	var усечение *ObjectBadge
	for i := range badges {
		if badges[i].Badge == BadgeAttributionTruncated {
			усечение = &badges[i]
		}
	}
	if len(edges) == 0 && усечение == nil {
		t.Fatalf("ни ребра, ни признака усечения: бюджет узлов оборвал обход молча (бейджи %+v)", badges)
	}
	if усечение == nil {
		t.Fatalf("бейджи %+v, ожидался %s: обход упёрся в бюджет узлов", badges, BadgeAttributionTruncated)
	}
	if усечение.ObjectID != общийМодуль || усечение.Count != 1 {
		t.Errorf("бейдж усечения %+v, ожидался на владельце модуля с фактом (%d) со счётчиком 1",
			*усечение, общийМодуль)
	}
}
