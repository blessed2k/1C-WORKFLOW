package resolve

import (
	"sort"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Файл — атрибуция код-фактов (§5 спецификации В1): кому принадлежит запись в
// регистр, сделанная в коде. Правило одно: факт приписывается ОБЪЕКТУ, а не
// модулю, где он физически написан. Общий модуль владельцем данных не
// считается (решение D6, общие модули свёрнуты) — обход идёт сквозь него
// вверх по графу вызовов до объекта-владельца.
//
// Пакет ничего не пишет в store: рёбра и бейджи возвращаются значениями, их
// публикует internal/index (таск 07).

// BadgeHasDynamic — бейдж «у объекта есть запись, которую статически
// приписать регистру нельзя». Дыра атрибуции обязана быть видна со
// счётчиком, а не замолчана (история 13).
const BadgeHasDynamic = "has-dynamic"

// BadgeAttributionTruncated — бейдж «обход атрибуции упёрся в жёсткий потолок».
// Потолки обхода (attributionNodeBudget, attributionCallersPerNode) в отличие
// от порогов ObjectEdgeTunables ребро действительно теряют: цепочка может не
// успеть дойти до владельца. R16 запрещает резать рёбра МОЛЧА, поэтому усечение
// оставляет след на объекте по тому же правилу подвешивания, что и
// has-dynamic, включая случай, когда владелец не найден.
const BadgeAttributionTruncated = "attribution-truncated"

// SymbolCall — обратное ребро графа вызовов: кто зовёт символ и с какой
// достоверностью разрешён вызов (store.CallEdgeRow.CallerID/Confidence).
type SymbolCall struct {
	CallerID   int64
	Confidence float64
}

// SymbolOwner — модуль символа и объект-владелец этого модуля
// (module.kind + module.owner_object_id, заполненная таском 04).
// ModuleKind сравнивается с bsl.ModuleCommon: у общего модуля владелец тоже
// есть (объект ОбщийМодуль), но владельцем ДАННЫХ он не считается.
type SymbolOwner struct {
	ModuleKind    string
	OwnerObjectID int64
	FileID        int64
}

// ownsData отвечает, годится ли модуль символа концом цепочки атрибуции.
func (o SymbolOwner) ownsData() bool {
	return o.OwnerObjectID != 0 && o.ModuleKind != string(bsl.ModuleCommon)
}

// ObjectEdgeGraph — всё, что атрибуции нужно знать о графе вызовов и о
// владельцах модулей. Реализует вызывающий (internal/index) поверх
// store.ReadTx: сам resolve в store не ходит (решение D02).
//
// Методы ошибок не возвращают намеренно: обход зовёт их тысячи раз, и
// протаскивать error через каждую вершину значит подменить алгоритм
// обработкой ошибок. Реализация копит ошибку транспорта у себя и проверяет
// её ПОСЛЕ вызова DeriveObjectDataEdges (идиома Err(), как у bufio.Scanner);
// пустой ответ на ошибке даёт недостроенную цепочку, то есть отсутствие
// ребра, а не выдуманное ребро.
type ObjectEdgeGraph interface {
	// CallersOf — кто зовёт символ (обратные рёбра call_edge, CallEdgesTo).
	CallersOf(symbolID int64) []SymbolCall
	// SymbolOwner — модуль символа и владелец модуля.
	SymbolOwner(symbolID int64) (SymbolOwner, bool)
}

// CallerPrefetcher: необязательная возможность графа поднять вызывающих
// целого фронта обхода (и владельцев их модулей) одним запросом, а не по
// запросу на символ. Обход зовёт её перед каждым уровнем; реализация без неё
// отвечает на CallersOf/SymbolOwner по одному, как раньше.
type CallerPrefetcher interface {
	PrefetchCallers(symbolIDs []int64)
}

// ObjectEdgeTunables — пороги атрибуции (§5). Порог не режет ребро: его
// превышение снижает confidence, ребро остаётся (история 15).
type ObjectEdgeTunables struct {
	ChainDepth   int     // длина цепочки, после которой каждое звено штрафуется
	HubFanIn     int     // fan-in, выше которого процедура считается хабом
	DepthPenalty float64 // множитель confidence за звено сверх ChainDepth
	HubPenalty   float64 // множитель confidence за проход через хаб
}

// Дефолты порогов. Дублируют дефолты флагов -graph-* (cmd/mcp1c/options.go):
// cmd не может импортировать internal/resolve (RuleCmdThroughApp), поэтому
// одного места для этих чисел не существует. Меняются парой.
const (
	defaultChainDepth   = 6
	defaultHubFanIn     = 50
	defaultDepthPenalty = 0.9
	defaultHubPenalty   = 0.7
)

// normalized подставляет дефолты вместо нулей: вызывающий, которому пороги
// не важны, передаёт пустую структуру и получает работающие значения, а не
// «глубина 0, штраф 0».
func (t ObjectEdgeTunables) normalized() ObjectEdgeTunables {
	if t.ChainDepth <= 0 {
		t.ChainDepth = defaultChainDepth
	}
	if t.HubFanIn <= 0 {
		t.HubFanIn = defaultHubFanIn
	}
	if t.DepthPenalty <= 0 || t.DepthPenalty > 1 {
		t.DepthPenalty = defaultDepthPenalty
	}
	if t.HubPenalty <= 0 || t.HubPenalty > 1 {
		t.HubPenalty = defaultHubPenalty
	}
	return t
}

// ObjectEdgeInput — вход атрибуции: строки register_access как они лежат в
// индексе, доступ к графу вызовов и пороги.
type ObjectEdgeInput struct {
	Accesses []store.RegisterAccessRow
	Graph    ObjectEdgeGraph
	Tunables ObjectEdgeTunables
}

// ChainStep — одно звено цепочки атрибуции: от объекта-владельца к месту
// факта. Сериализуется вызывающим в object_data_edge.evidence.
type ChainStep struct {
	SymbolID   int64       `json:"symbolId"`
	FileID     int64       `json:"fileId"`
	Confidence float64     `json:"confidence,omitempty"`
	Span       domain.Span `json:"span,omitempty"`
}

// ObjectDataEdge — ребро объектного графа до записи в store: то же, что
// store.ObjectDataEdge, но с ЦЕПОЧКОЙ вместо сериализованного evidence.
// Сериализацию делает вызывающий (store.ObjectDataEdge.Evidence).
type ObjectDataEdge struct {
	FromObjectID  int64
	ToObjectID    int64
	Kind          string
	Layer         string
	Provenance    string
	Confidence    float64
	Mode          string
	InTransaction *bool
	Chain         []ChainStep
	FileIDs       []int64
}

// ObjectBadge — бейдж узла со счётчиком (store.ObjectBadge без слоя-умолчания).
//
// Count — ПОЛНЫЙ пересчёт по всем строкам объекта, попавшим в этот вызов, а не
// приращение. store.InsertObjectBadge счётчик ЗАМЕЩАЕТ, поэтому вызывающий
// обязан подавать строки владельца ЦЕЛИКОМ: инкрементальный вызов с частью
// строк запишет частичный счётчик поверх полного, и дыра будет выглядеть
// уменьшившейся, а не недосчитанной. Пересобирается владелец — пересобираются
// все его строки register_access.
type ObjectBadge struct {
	ObjectID int64
	Badge    string
	Layer    string
	Count    int64
}

// DeriveObjectDataEdges приписывает строки register_access объектам-владельцам.
//
// Правила (§5, раздел 6.3 контракта):
//  1. вход — статические строки с разрешённым регистром; нестатические ребра
//     не дают, но дают бейдж has-dynamic владельцу;
//  2. обход идёт ВВЕРХ по графу вызовов до символа, чей модуль принадлежит
//     объекту; общий модуль владельцем не считается, сквозь него идут дальше;
//  3. confidence ребра — минимум по цепочке, пороги её снижают, но не режут;
//  4. цикл терминируется, ребро строится один раз;
//  5. несколько цепочек к одному факту — одно ребро с максимальным
//     confidence, цепочка в evidence кратчайшая;
//  6. цепочка, не дошедшая до объекта, ребра не даёт и ошибкой не является
//     (ответ на этот случай — writes-declared из метаданных, таск 07).
func DeriveObjectDataEdges(in ObjectEdgeInput) ([]ObjectDataEdge, []ObjectBadge) {
	if in.Graph == nil || len(in.Accesses) == 0 {
		return nil, nil
	}
	acc := newEdgeAccumulator(in.Graph, in.Tunables.normalized())
	for _, row := range in.Accesses {
		acc.add(row)
	}
	return acc.result()
}

// edgeAccumulator собирает рёбра и бейджи по всем строкам разом: схлопывание
// дублей возможно только поверх всего входа, не построчно.
type edgeAccumulator struct {
	graph    ObjectEdgeGraph
	tunables ObjectEdgeTunables
	edges    map[edgeKey]*edgeAgg
	badges   map[badgeKey]int64
}

// edgeKey — то, что делает ребро тем же самым ребром. Mode входит в ключ
// намеренно: чтение и запись одного объекта в одно ребро не сливаются.
type edgeKey struct {
	from, to                int64
	kind, layer, mode, inTx string
}

// edgeAgg — накопленное состояние одного ребра: максимальный confidence,
// кратчайшая цепочка и объединение файлов ВСЕХ цепочек (по ним ребро
// удаляется при инкрементальной пересборке, таск 07).
type edgeAgg struct {
	confidence    float64
	chain         []ChainStep
	files         map[int64]struct{}
	inTransaction *bool
}

type badgeKey struct {
	objectID int64
	badge    string
	layer    string
}

func newEdgeAccumulator(g ObjectEdgeGraph, t ObjectEdgeTunables) *edgeAccumulator {
	return &edgeAccumulator{
		graph: g, tunables: t,
		edges:  map[edgeKey]*edgeAgg{},
		badges: map[badgeKey]int64{},
	}
}

// ownerHit — цепочка, дошедшая до объекта-владельца.
type ownerHit struct {
	objectID   int64
	confidence float64
	chain      []ChainStep
	files      []int64
	// penalized: цепочка прошла через хаб или длиннее порога глубины
	// (штрафы ObjectEdgeTunables). Рёбрам не важно, бейджам HTTP важно.
	penalized bool
}

// add приписывает одну строку register_access: статическую с разрешённым
// регистром — рёбрами, динамическую — бейджем.
func (a *edgeAccumulator) add(row store.RegisterAccessRow) {
	if row.Static && row.ObjectID == 0 {
		// Имя регистра статично, но такого объекта в индексе нет (мягкая
		// цель, §15): висячее ребро хуже отсутствующего, а динамикой это
		// тоже не является — бейдж тут был бы ложной дырой. Обход не
		// запускаем вовсе: на реальной выгрузке такие строки массовые
		// (ложные срабатывания вида «Движения.Вставить»).
		return
	}
	hits, truncated := a.walk(row)
	if truncated {
		// Обход не был доведён до конца: рёбер могло не получиться вовсе.
		// Молчать об этом нельзя, поэтому след остаётся на объекте.
		a.bumpBadge(BadgeAttributionTruncated, row, hits)
	}
	if !row.Static {
		a.bumpBadge(BadgeHasDynamic, row, hits)
		return
	}
	for _, hit := range hits {
		a.mergeEdge(row, hit)
	}
}

// mergeEdge схлопывает цепочку в ребро: confidence максимальный, цепочка
// кратчайшая, файлы объединяются.
func (a *edgeAccumulator) mergeEdge(row store.RegisterAccessRow, hit ownerHit) {
	key := edgeKey{
		from: hit.objectID, to: row.ObjectID, kind: edgeKindForMode(row.Mode),
		layer: row.Layer, mode: row.Mode, inTx: inTransactionKey(row.InTransaction),
	}
	agg, ok := a.edges[key]
	if !ok {
		agg = &edgeAgg{confidence: hit.confidence, chain: hit.chain,
			files: map[int64]struct{}{}, inTransaction: row.InTransaction}
		a.edges[key] = agg
	}
	if hit.confidence > agg.confidence {
		agg.confidence = hit.confidence
	}
	if len(hit.chain) < len(agg.chain) {
		agg.chain = hit.chain
	}
	for _, f := range hit.files {
		agg.files[f] = struct{}{}
	}
}

// bumpBadge вешает бейдж на владельцев, до которых дошла цепочка. Если не
// дошла ни одна — на владельца модуля, где найден факт (§5 п.6), в том числе
// на сам общий модуль: дыра обязана быть видна хоть где-то. Правило
// подвешивания одно на все бейджи, чтобы «не видно ничего» не зависело от
// того, какая именно причина сорвала атрибуцию.
func (a *edgeAccumulator) bumpBadge(badge string, row store.RegisterAccessRow, hits []ownerHit) {
	if len(hits) == 0 {
		owner, ok := a.graph.SymbolOwner(row.SymbolID)
		if !ok || owner.OwnerObjectID == 0 {
			return
		}
		a.badges[badgeKey{objectID: owner.OwnerObjectID, badge: badge, layer: row.Layer}]++
		return
	}
	seen := make(map[int64]struct{}, len(hits))
	for _, hit := range hits {
		if _, dup := seen[hit.objectID]; dup {
			continue // один факт — один пункт счётчика, сколько бы цепочек к нему ни вело
		}
		seen[hit.objectID] = struct{}{}
		a.badges[badgeKey{objectID: hit.objectID, badge: badge, layer: row.Layer}]++
	}
}

func (a *edgeAccumulator) result() ([]ObjectDataEdge, []ObjectBadge) {
	edges := make([]ObjectDataEdge, 0, len(a.edges))
	for key, agg := range a.edges {
		edges = append(edges, ObjectDataEdge{
			FromObjectID: key.from, ToObjectID: key.to, Kind: key.kind, Layer: key.layer,
			Provenance: store.EdgeProvenanceCode, Confidence: agg.confidence,
			Mode: key.mode, InTransaction: agg.inTransaction,
			Chain: agg.chain, FileIDs: sortedIDs(agg.files),
		})
	}
	sort.Slice(edges, func(i, j int) bool { return lessEdge(edges[i], edges[j]) })

	badges := make([]ObjectBadge, 0, len(a.badges))
	for key, count := range a.badges {
		badges = append(badges, ObjectBadge{
			ObjectID: key.objectID, Badge: key.badge, Layer: key.layer, Count: count,
		})
	}
	sort.Slice(badges, func(i, j int) bool {
		switch {
		case badges[i].ObjectID != badges[j].ObjectID:
			return badges[i].ObjectID < badges[j].ObjectID
		case badges[i].Badge != badges[j].Badge:
			return badges[i].Badge < badges[j].Badge
		default:
			return badges[i].Layer < badges[j].Layer
		}
	})
	return edges, badges
}

// lessEdge задаёт устойчивый порядок выдачи: карта в Go неупорядочена, а
// вызывающий пишет рёбра в store и сравнивает их в тестах.
func lessEdge(x, y ObjectDataEdge) bool {
	switch {
	case x.FromObjectID != y.FromObjectID:
		return x.FromObjectID < y.FromObjectID
	case x.ToObjectID != y.ToObjectID:
		return x.ToObjectID < y.ToObjectID
	case x.Kind != y.Kind:
		return x.Kind < y.Kind
	case x.Mode != y.Mode:
		return x.Mode < y.Mode
	case x.Layer != y.Layer:
		return x.Layer < y.Layer
	default:
		// in_transaction входит в ключ ребра, значит обязан участвовать и в
		// порядке: иначе два ребра, различающиеся только им, встают в
		// случайном порядке (sort.Slice неустойчив).
		return inTransactionKey(x.InTransaction) < inTransactionKey(y.InTransaction)
	}
}

func sortedIDs(set map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// inTransactionKey переводит трёхзначное in_transaction в часть ключа:
// «неизвестно» и «нет» — разные рёбра, как и в схеме.
func inTransactionKey(v *bool) string {
	switch {
	case v == nil:
		return ""
	case *v:
		return "1"
	default:
		return "0"
	}
}

// Жёсткие потолки обхода. Пороги из ObjectEdgeTunables только штрафуют
// confidence, поэтому обход обязан иметь СВОЙ предел: на реальной выгрузке
// общий модуль зовут из тысяч мест. Числа — те же, что у bounded traversal
// trace_call_graph (internal/app/graph.go): упрощение, не продуктовое решение.
//
// В отличие от порогов потолок ребро действительно теряет: владелец может
// оказаться за обрезанным краем. Поэтому упор в потолок НЕ молчаливый — он
// поднимает флаг усечения, а тот становится бейджем BadgeAttributionTruncated
// на объекте (R16: не резать рёбра молча). Граница записана в ADR-025.
const (
	attributionNodeBudget     = 500
	attributionCallersPerNode = 200
)

// walkState — вершина обхода: символ, накопленная достоверность и цепочка от
// места факта к нему (в обратном порядке, разворачивается на выходе).
type walkState struct {
	symbolID   int64
	confidence float64
	chain      []ChainStep
	penalized  bool
}

// walk идёт ВВЕРХ по статическим рёбрам графа вызовов от места факта до
// символов, чьи модули принадлежат объектам. Обход пошаговый (BFS), поэтому
// первая найденная цепочка до владельца — кратчайшая. Уже посещённый символ
// разворачивается заново, только если новая цепочка даёт СТРОГО больший
// confidence: так цикл A->B->A терминируется (по цепочке confidence не
// растёт), а лучший путь не теряется из-за того, что худший пришёл раньше.
func (a *edgeAccumulator) walk(row store.RegisterAccessRow) (hits []ownerHit, truncated bool) {
	if row.SymbolID == 0 {
		return nil, false // факт вне метода: подниматься не от чего
	}
	start := ChainStep{SymbolID: row.SymbolID, FileID: row.FileID,
		Confidence: row.Confidence, Span: row.Span}
	frontier := []walkState{{symbolID: row.SymbolID, confidence: row.Confidence, chain: []ChainStep{start}}}
	best := map[int64]float64{row.SymbolID: row.Confidence}

	for len(frontier) > 0 {
		if len(best) >= attributionNodeBudget {
			truncated = true // остались неразвёрнутые вершины: обход не полон
			break
		}
		if pf, ok := a.graph.(CallerPrefetcher); ok {
			ids := make([]int64, len(frontier))
			for i, st := range frontier {
				ids[i] = st.symbolID
			}
			pf.PrefetchCallers(ids)
		}
		var next []walkState
		for _, cur := range frontier {
			owner, known := a.graph.SymbolOwner(cur.symbolID)
			if !known {
				continue
			}
			if owner.ownsData() {
				hits = append(hits, ownerHit{objectID: owner.OwnerObjectID, confidence: cur.confidence,
					chain: reverseChain(cur.chain), files: chainFiles(cur.chain), penalized: cur.penalized})
				continue // дальше владельца цепочка не идёт: он и есть ответ
			}
			states, cut := a.expand(cur, best)
			truncated = truncated || cut
			next = append(next, states...)
		}
		frontier = next
	}
	return hits, truncated
}

// expand поднимается на один уровень вверх: от символа к тем, кто его зовёт.
// Второе значение — «список вызывающих обрезан потолком»: за краем мог
// остаться владелец, и это обязано стать видимым, а не потеряться.
func (a *edgeAccumulator) expand(cur walkState, best map[int64]float64) ([]walkState, bool) {
	callers := a.graph.CallersOf(cur.symbolID)
	// Хаб считается по ПОЛНОМУ fan-in, до обрезания по потолку обхода:
	// потолок — защита обхода, а не признак хаба.
	hub := len(callers) > a.tunables.HubFanIn
	truncated := len(callers) > attributionCallersPerNode
	if truncated {
		callers = callers[:attributionCallersPerNode]
	}
	var out []walkState
	for _, call := range callers {
		if call.CallerID == 0 {
			continue // вызов без разрешённого символа-источника цепочку не продолжает
		}
		ownerInfo, known := a.graph.SymbolOwner(call.CallerID)
		if !known {
			continue // символ без модуля: ни файла для зависимости, ни владельца
		}
		conf := minConfidence(cur.confidence, call.Confidence)
		penalized := cur.penalized || hub || len(cur.chain) > a.tunables.ChainDepth
		if hub {
			// Процедуру зовут слишком многие, чтобы цепочка через неё что-то
			// объясняла: достоверность падает, ребро остаётся (R16).
			conf *= a.tunables.HubPenalty
		}
		if len(cur.chain) > a.tunables.ChainDepth {
			// Звено сверх порога глубины: чем длиннее цепочка, тем слабее
			// объяснение — но ребро остаётся, порог не режет молча (R16).
			conf *= a.tunables.DepthPenalty
		}
		if prev, seen := best[call.CallerID]; seen && conf <= prev {
			continue
		}
		best[call.CallerID] = conf
		step := ChainStep{SymbolID: call.CallerID, FileID: ownerInfo.FileID, Confidence: call.Confidence}
		out = append(out, walkState{symbolID: call.CallerID, confidence: conf,
			chain: append(copyChain(cur.chain), step), penalized: penalized})
	}
	return out, truncated
}

// reverseChain разворачивает цепочку в порядок «владелец -> место факта»:
// обход идёт снизу вверх, а читается она сверху вниз.
func reverseChain(chain []ChainStep) []ChainStep {
	out := make([]ChainStep, len(chain))
	for i, step := range chain {
		out[len(chain)-1-i] = step
	}
	return out
}

func copyChain(chain []ChainStep) []ChainStep {
	out := make([]ChainStep, len(chain), len(chain)+1)
	copy(out, chain)
	return out
}

// minConfidence — достоверность цепочки это её слабейшее звено (R13): одно
// сомнительное разрешение вызова обесценивает весь путь.
func minConfidence(a, b float64) float64 {
	if b < a {
		return b
	}
	return a
}

func chainFiles(chain []ChainStep) []int64 {
	out := make([]int64, 0, len(chain))
	for _, step := range chain {
		if step.FileID != 0 {
			out = append(out, step.FileID)
		}
	}
	return out
}

// edgeKindForMode переводит режим доступа в вид ребра: чтение и запись живут
// разными рёбрами и в одно не сливаются.
func edgeKindForMode(mode string) string {
	if mode == "read" {
		return store.EdgeReadsRegister
	}
	return store.EdgeWritesRegister
}
