package app

// Режимы карты «до и после расширений» (веха В3, docs/architecture-graph.md
// §12). Данные для них лежат в индексе с В1: у каждого ребра object_data_edge
// есть колонка layer (base или id компонента-расширения, решение D11). Здесь
// только чтение поверх этих данных, ничего не материализуется.
//
// Главная тонкость не в слое ребра, а в узлах. Объект, заимствованный
// расширением, лежит в metadata_object отдельной строкой на каждый слой, и
// рёбра расширения висят на строке расширения. Карта, которая просто сняла бы
// фильтр по слою, показала бы документ дважды: базовый узел с рёбрами базы и
// узел-двойник из расширения со своими. Поэтому effective и diff склеивают
// строки одного объекта (тот же вид и имя) в один узел, «канонический»: строку
// базы, а если объект есть только в расширениях, то строку первого по
// applyOrder (sortObjectRowsByLayer, та же очерёдность, что у get_object).
// raw работает с теми же каноническими id: рёбра базы и так висят на строках
// базы, и узлы карты не меняют id при переключении режима.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Значения параметра view графа. Пусто: прежнее поведение, рёбра строки узла
// любых слоёв без склейки (контракт до В3 не меняется).
const (
	GraphViewRaw       = string(domain.ViewRaw)
	GraphViewEffective = string(domain.ViewEffective)
	GraphViewDiff      = "diff"
)

// EdgeDiffAdded: пометка ребра расширения, связи которого (тот же вид между
// теми же объектами) в слое base нет: её добавило расширение.
const EdgeDiffAdded = "added"

// baseLayer: значение layer у фактов базовой конфигурации (index.layerName).
const baseLayer = "base"

// viewScanLimit: сколько рёбер читается за раз там, где режим считается в
// памяти (соседи и карточка узла в effective и diff, god-node в diff).
// Склейка связей требует видеть все рёбра узла сразу, поэтому страница
// нарезается после неё. Упор в потолок не молчит: truncated плюс пометка,
// что totalCount только нижняя граница.
const viewScanLimit = 2000

// nodeExtensionEdgeLimit: сколько рёбер расширений показывает карточка узла.
const nodeExtensionEdgeLimit = 50

// normalizeGraphView разбирает view. Неизвестное значение и view вместе с
// layer: ошибки ввода: view=raw уже означает layer=base, а сочетание
// «effective, но только слой ext» противоречит само себе.
func normalizeGraphView(view, layer string) (string, *Error) {
	v := strings.TrimSpace(view)
	switch v {
	case "", GraphViewRaw, GraphViewEffective, GraphViewDiff:
	default:
		return "", NewError(CodeInvalidArgument,
			fmt.Sprintf("view %q неизвестен", view),
			"допустимо raw (только база), effective (с расширениями) или diff (что добавили расширения)")
	}
	if v != "" && strings.TrimSpace(layer) != "" {
		return "", NewError(CodeInvalidArgument,
			"view и layer вместе не задаются",
			"view=raw уже означает layer=base; чтобы смотреть один слой, передайте только layer")
	}
	return v, nil
}

// graphViewMerges: режим склеивает строки объекта по слоям.
func graphViewMerges(view string) bool {
	return view == GraphViewEffective || view == GraphViewDiff
}

// layerIndex: склейка строк объекта по слоям в пределах одной read-
// транзакции: канонический узел, все строки объекта и признак «ребро
// добавлено расширением». Кэширует всё, что прочитал: карта спрашивает про
// одни и те же объекты на каждом ребре страницы.
type layerIndex struct {
	tx       *store.ReadTx
	manifest workspace.Manifest
	rows     map[int64]store.MetadataObjectRow
	groups   map[int64][]store.MetadataObjectRow
}

func newLayerIndex(tx *store.ReadTx, manifest workspace.Manifest) *layerIndex {
	return &layerIndex{
		tx: tx, manifest: manifest,
		rows:   map[int64]store.MetadataObjectRow{},
		groups: map[int64][]store.MetadataObjectRow{},
	}
}

// group отдаёт все строки объекта, которому принадлежит строка id, базой
// вперёд. Пустой ответ: строки id нет.
func (li *layerIndex) group(id int64) ([]store.MetadataObjectRow, error) {
	if g, ok := li.groups[id]; ok {
		return g, nil
	}
	row, ok, err := li.tx.MetadataObjectByID(id)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := li.tx.MetadataObjectsByName(row.MType, row.NameNorm)
	if err != nil {
		return nil, err
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		rows = append(rows, row)
	}
	sorted := sortObjectRowsByLayer(li.manifest, rows)
	for _, r := range sorted {
		li.groups[r.ID] = sorted
		li.rows[r.ID] = r
	}
	return sorted, nil
}

// canonical: канонический узел объекта строки id (строка базы, если есть).
func (li *layerIndex) canonical(id int64) (store.MetadataObjectRow, bool, error) {
	g, err := li.group(id)
	if err != nil || len(g) == 0 {
		return store.MetadataObjectRow{}, false, err
	}
	return g[0], true, nil
}

// groupIDs: id всех строк объекта строки id.
func (li *layerIndex) groupIDs(id int64) ([]int64, error) {
	g, err := li.group(id)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(g))
	for i, r := range g {
		out[i] = r.ID
	}
	return out, nil
}

// relKey: связь без строки и слоя. Концы канонические.
type relKey struct {
	from, to int64
	kind     string
}

// viewEdge: связь, приведённая к режиму. row: представитель (ребро базы,
// если связь есть в базе, иначе первое по id ребро расширения), концы уже
// канонические; layers: все слои, где связь есть, база первой; diff: added,
// если в базе связи нет.
type viewEdge struct {
	row    store.ObjectDataEdgeRow
	layers []string
	diff   string
}

func (e viewEdge) key() relKey { return relKey{e.row.FromObjectID, e.row.ToObjectID, e.row.Kind} }

// baseRelations: какие связи из rows есть в слое base. Один запрос на все
// рёбра (ObjectEdgeKeysInLayer), а не по запросу на ребро: базовый двойник
// ищется среди всех строк обоих объектов, без фильтров карты (порог
// confidence не должен превращать базовую связь в «добавленную»).
func (li *layerIndex) baseRelations(rows []store.ObjectDataEdgeRow) (map[relKey]bool, error) {
	var from, to []int64
	seen := map[int64]bool{}
	add := func(dst *[]int64, id int64) error {
		g, err := li.groupIDs(id)
		if err != nil {
			return err
		}
		for _, x := range g {
			if !seen[x] {
				seen[x] = true
			}
		}
		*dst = append(*dst, g...)
		return nil
	}
	for _, r := range rows {
		if r.Layer == baseLayer {
			continue
		}
		if err := add(&from, r.FromObjectID); err != nil {
			return nil, err
		}
		if err := add(&to, r.ToObjectID); err != nil {
			return nil, err
		}
	}
	out := map[relKey]bool{}
	if len(from) == 0 {
		return out, nil
	}
	keys, err := li.tx.ObjectEdgeKeysInLayer(uniqueIDs(from), uniqueIDs(to), baseLayer)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		f, _, err := li.canonical(k.FromObjectID)
		if err != nil {
			return nil, err
		}
		t, _, err := li.canonical(k.ToObjectID)
		if err != nil {
			return nil, err
		}
		out[relKey{f.ID, t.ID, k.Kind}] = true
	}
	return out, nil
}

func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// project приводит рёбра к режиму view. raw и пустой view отдают рёбра как
// есть. effective и diff заменяют концы каноническими узлами и склеивают
// рёбра одной связи (те же объекты, тот же вид) в одно с перечнем слоёв:
// связь базы, повторённая расширением, одна связь, а не две параллельные, и
// степени её не удваивают. diff оставляет только связи, которых нет в base.
func (li *layerIndex) project(view string, rows []store.ObjectDataEdgeRow) ([]viewEdge, error) {
	if !graphViewMerges(view) {
		out := make([]viewEdge, 0, len(rows))
		for _, r := range rows {
			out = append(out, viewEdge{row: r})
		}
		return out, nil
	}
	inBase, err := li.baseRelations(rows)
	if err != nil {
		return nil, err
	}
	var order []relKey
	byKey := map[relKey]*viewEdge{}
	for _, r := range rows {
		for _, end := range []*int64{&r.FromObjectID, &r.ToObjectID} {
			c, ok, err := li.canonical(*end)
			if err != nil {
				return nil, err
			}
			if ok {
				*end = c.ID
			}
		}
		k := relKey{r.FromObjectID, r.ToObjectID, r.Kind}
		e, have := byKey[k]
		if !have {
			e = &viewEdge{row: r}
			byKey[k] = e
			order = append(order, k)
		} else if r.Layer == baseLayer && e.row.Layer != baseLayer {
			e.row = r
		}
		e.layers = addLayer(e.layers, r.Layer)
		if r.Layer == baseLayer {
			inBase[k] = true
		}
	}
	out := make([]viewEdge, 0, len(order))
	for _, k := range order {
		e := byKey[k]
		if !inBase[k] {
			e.diff = EdgeDiffAdded
		}
		if view == GraphViewDiff && e.diff != EdgeDiffAdded {
			continue
		}
		out = append(out, *e)
	}
	return out, nil
}

// addLayer добавляет слой в перечень без повторов: base первой, остальные
// по алфавиту.
func addLayer(layers []string, l string) []string {
	for _, x := range layers {
		if x == l {
			return layers
		}
	}
	layers = append(layers, l)
	sort.SliceStable(layers, func(i, j int) bool {
		if (layers[i] == baseLayer) != (layers[j] == baseLayer) {
			return layers[i] == baseLayer
		}
		return layers[i] < layers[j]
	})
	return layers
}

// edgeFilterFor дополняет фильтр рёбер узла под режим: raw читает только
// слой base, effective и diff читают все строки объекта всех слоёв (diff
// нужна и база: по ней видно, что связь не новая).
func (li *layerIndex) edgeFilterFor(view string, node int64, f store.ObjectEdgeFilter) (store.ObjectEdgeFilter, error) {
	if view == "" {
		f.ObjectID = node
		return f, nil
	}
	ids, err := li.groupIDs(node)
	if err != nil {
		return f, err
	}
	f.ObjectID, f.ObjectIDs = node, ids
	if view == GraphViewRaw {
		f.Layer = baseLayer
	}
	return f, nil
}

// nodeRelations читает рёбра всех строк объекта node (не больше
// viewScanLimit) и склеивает их в связи режима view. Второе значение: упёрлись
// ли в потолок.
func (li *layerIndex) nodeRelations(view string, node int64, direction string, kinds []string, minConfidence float64) ([]viewEdge, bool, error) {
	filter, err := li.edgeFilterFor(view, node, store.ObjectEdgeFilter{
		Direction: direction, Kinds: kinds, MinConfidence: minConfidence, Limit: viewScanLimit,
	})
	if err != nil {
		return nil, false, err
	}
	rows, err := li.tx.ObjectDataEdges(filter)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > viewScanLimit
	if truncated {
		rows = rows[:viewScanLimit]
	}
	edges, err := li.project(view, rows)
	return edges, truncated, err
}

// extensionLayers: id компонентов-расширений проекта, они же значения layer
// рёбер расширений (index.layerName).
func extensionLayers(tx *store.ReadTx) ([]string, error) {
	comps, err := tx.Components()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range comps {
		if c.Kind == string(domain.KindExtension) {
			out = append(out, c.ID)
		}
	}
	return out, nil
}

// graphViewWarnings: честные оговорки режима. Без расширений effective
// совпадает с raw, а diff пуст по построению, и это надо сказать, иначе
// пустой diff читается как «расширения ничего не меняют». С расширениями diff
// видит только добавленные связи: подавление базовых движений перехватом
// &Вместо без ПродолжитьВызов индекс не вычисляет.
func graphViewWarnings(tx *store.ReadTx, view string) ([]Warning, error) {
	if !graphViewMerges(view) {
		return nil, nil
	}
	exts, err := extensionLayers(tx)
	if err != nil {
		return nil, err
	}
	if len(exts) == 0 {
		return []Warning{{
			Code:    "no_extensions",
			Message: "в проекте нет расширений: effective совпадает с raw, diff пуст",
		}}, nil
	}
	if view == GraphViewDiff {
		return []Warning{{
			Code:    "diff_only_added",
			Message: "diff показывает связи, которые добавили расширения; пропавшие связи (перехват &Вместо без ПродолжитьВызов гасит движения базы) индекс не вычисляет",
			Hint:    "перехватчики обработчика проведения видны в get_symbol с view=effective; гасит ли перехватчик движения базы, решает его текст",
		}}, nil
	}
	return nil, nil
}

// scanTruncatedWarnings: упор в viewScanLimit. Два кода: truncated (почему
// ответ неполон) и total_lower_bound (totalCount здесь не точное число, а
// нижняя граница), чтобы клиент не принял обрезанный счёт за полный.
func scanTruncatedWarnings(total int) []Warning {
	return []Warning{{
		Code:    "truncated",
		Message: fmt.Sprintf("у узла больше %d рёбер, режим посчитан по первым из них", viewScanLimit),
		Hint:    "сузьте kinds или minConfidence",
	}, {
		Code:    "total_lower_bound",
		Message: fmt.Sprintf("totalCount и степени здесь нижняя граница: связей не меньше %d", total),
	}}
}

func rowsOfView(edges []viewEdge) []store.ObjectDataEdgeRow {
	out := make([]store.ObjectDataEdgeRow, len(edges))
	for i, e := range edges {
		out[i] = e.row
	}
	return out
}

// edgeItemOf: EdgeItem связи режима, с пометкой diff и перечнем слоёв.
func edgeItemOf(e viewEdge, objects map[int64]store.MetadataObjectRow) EdgeItem {
	item := edgeItemFrom(e.row, objects)
	item.Diff, item.Layers = e.diff, e.layers
	return item
}

// viewNode строит карточку канонического узла объекта строки rowID в режиме
// view. raw: степени по рёбрам базы (счётчики SQL), рёбра расширений не
// читаются вовсе. effective и diff: степени по связям режима, все компоненты
// объекта и связи, в которых участвует расширение, с его именем.
func viewNode(tx *store.ReadTx, manifest workspace.Manifest, view string, rowID int64) (NodeItem, []Warning, error) {
	li := newLayerIndex(tx, manifest)
	group, err := li.group(rowID)
	if err != nil || len(group) == 0 {
		return NodeItem{}, nil, err
	}
	canon := group[0]
	ids := make([]int64, len(group))
	var badges []store.ObjectBadge
	for i, r := range group {
		ids[i] = r.ID
		// raw показывает только базу, значит и дыры атрибуции только её.
		if view == GraphViewRaw && i > 0 {
			continue
		}
		b, berr := tx.ObjectBadges(r.ID)
		if berr != nil {
			return NodeItem{}, nil, berr
		}
		badges = append(badges, b...)
	}
	item := nodeItemFrom(canon, badges)
	for _, r := range group {
		item.Components = append(item.Components, r.ComponentID)
	}

	if view == GraphViewRaw {
		for _, dir := range []string{store.EdgeDirectionIn, store.EdgeDirectionOut} {
			n, cerr := tx.CountObjectDataEdges(store.ObjectEdgeFilter{ObjectID: canon.ID, ObjectIDs: ids, Direction: dir, Layer: baseLayer})
			if cerr != nil {
				return NodeItem{}, nil, cerr
			}
			if dir == store.EdgeDirectionIn {
				item.FanIn = n
			} else {
				item.FanOut = n
			}
		}
		return item, nil, nil
	}

	rels, truncated, err := li.nodeRelations(view, canon.ID, store.EdgeDirectionBoth, nil, 0)
	if err != nil {
		return NodeItem{}, nil, err
	}
	extTruncated := false
	for _, e := range rels {
		if e.row.ToObjectID == canon.ID {
			item.FanIn++
		}
		if e.row.FromObjectID == canon.ID {
			item.FanOut++
		}
		ext := extensionOnly(e.layers)
		if len(ext) == 0 {
			continue
		}
		if len(item.ExtensionEdges) >= nodeExtensionEdgeLimit {
			extTruncated = true
			continue
		}
		ne := NodeExtensionEdge{EdgeID: e.row.ID, Layer: ext[0], Layers: e.layers, Kind: e.row.Kind, Diff: e.diff,
			Direction: store.EdgeDirectionOut, OtherID: e.row.ToObjectID}
		if e.row.FromObjectID != canon.ID {
			ne.Direction, ne.OtherID = store.EdgeDirectionIn, e.row.FromObjectID
		}
		other, ok, oerr := li.canonical(ne.OtherID)
		if oerr != nil {
			return NodeItem{}, nil, oerr
		}
		if ok {
			ne.OtherMType, ne.OtherDisplay = other.MType, other.NameDisplay
		}
		item.ExtensionEdges = append(item.ExtensionEdges, ne)
	}

	warnings, err := graphViewWarnings(tx, view)
	if err != nil {
		return NodeItem{}, nil, err
	}
	if truncated {
		warnings = append(warnings, scanTruncatedWarnings(int(item.FanIn+item.FanOut))...)
	}
	if extTruncated {
		warnings = append(warnings, Warning{
			Code:    "truncated",
			Message: fmt.Sprintf("у объекта много связей расширений, в карточке первые %d", nodeExtensionEdgeLimit),
			Hint:    "раскройте соседей узла в view=diff, там список полный",
		})
	}
	return item, warnings, nil
}

// extensionOnly: слои расширений из перечня (всё, кроме base).
func extensionOnly(layers []string) []string {
	var out []string
	for _, l := range layers {
		if l != baseLayer {
			out = append(out, l)
		}
	}
	return out
}

// neighborsMerged: соседи узла в effective и diff. Связи склеиваются по всем
// рёбрам узла сразу, поэтому страница нарезается в памяти, а курсор здесь
// накопленный offset, как у Radius, а не id строки. Узел на входе может быть
// любой строкой объекта: ответ всегда про канонический узел, двойника нет.
func (g *ObjectGraphService) neighborsMerged(ctx context.Context, op *openProject, view string, in NeighborsInput, direction string, limit int, paramsKey string) (Response[EdgeItem], error) {
	type txResult struct {
		edges    []viewEdge
		objects  map[int64]store.MetadataObjectRow
		gen      domain.Generation
		found    bool
		afterIdx int
		total    int
		warnings []Warning
	}
	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		li := newLayerIndex(tx, op.Manifest)
		canon, ok, cerr := li.canonical(in.ObjectID)
		if cerr != nil || !ok {
			return out, cerr
		}
		out.found = true
		key, derr := DecodeCursor(in.Cursor, gen, paramsKey)
		if derr != nil {
			return out, derr
		}
		if key != "" {
			out.afterIdx, _ = strconv.Atoi(key)
		}
		rels, truncated, eerr := li.nodeRelations(view, canon.ID, direction, in.Kinds, in.MinConfidence)
		if eerr != nil {
			return out, eerr
		}
		out.total = len(rels)
		if out.afterIdx > len(rels) {
			out.afterIdx = len(rels)
		}
		out.edges = rels[out.afterIdx:]
		if len(out.edges) > limit+1 {
			out.edges = out.edges[:limit+1]
		}
		objects, oerr := resolveEdgeObjects(tx, rowsOfView(out.edges))
		if oerr != nil {
			return out, oerr
		}
		out.objects = objects
		out.warnings, oerr = graphViewWarnings(tx, view)
		if oerr != nil {
			return out, oerr
		}
		if truncated {
			out.warnings = append(out.warnings, scanTruncatedWarnings(out.total)...)
		}
		return out, nil
	})
	if err != nil {
		return Response[EdgeItem]{}, err
	}
	if !res.found {
		return Response[EdgeItem]{}, NotFoundError("объект метаданных", strconv.FormatInt(in.ObjectID, 10), nil).
			WithProject(op.Entry.ID).WithGeneration(res.gen)
	}
	hasMore := len(res.edges) > limit
	if hasMore {
		res.edges = res.edges[:limit]
	}
	items := make([]EdgeItem, 0, len(res.edges))
	for _, e := range res.edges {
		items = append(items, edgeItemOf(e, res.objects))
	}
	resp := Response[EdgeItem]{Generation: res.gen, Warnings: res.warnings, Items: items, TotalCount: res.total}
	if hasMore {
		resp.NextCursor = EncodeCursor(res.gen, strconv.Itoa(res.afterIdx+limit), paramsKey)
	}
	return resp, nil
}

// godNodesDiff: топ узлов по числу связей, добавленных расширениями: какие
// объекты расширения меняют сильнее всего. Читаются только рёбра слоёв
// расширений (ObjectDataEdgesInLayers, по индексу), узлы канонические.
func godNodesDiff(tx *store.ReadTx, li *layerIndex, in GodNodesInput, by string, limit int) ([]store.GodNodeRow, []Warning, error) {
	warnings, err := graphViewWarnings(tx, GraphViewDiff)
	if err != nil {
		return nil, nil, err
	}
	exts, err := extensionLayers(tx)
	if err != nil || len(exts) == 0 {
		return nil, warnings, err
	}
	rows, err := tx.ObjectDataEdgesInLayers(exts, in.Kinds, in.MinConfidence, viewScanLimit)
	if err != nil {
		return nil, nil, err
	}
	truncated := len(rows) > viewScanLimit
	if truncated {
		rows = rows[:viewScanLimit]
	}
	edges, err := li.project(GraphViewDiff, rows)
	if err != nil {
		return nil, nil, err
	}
	agg := map[int64]*store.GodNodeRow{}
	node := func(id int64) *store.GodNodeRow {
		if n, ok := agg[id]; ok {
			return n
		}
		r := li.rows[id]
		n := &store.GodNodeRow{ObjectID: id, MType: r.MType, NameDisplay: r.NameDisplay}
		agg[id] = n
		return n
	}
	for _, e := range edges {
		node(e.row.FromObjectID).FanOut++
		node(e.row.ToObjectID).FanIn++
	}
	allowed := map[string]bool{}
	for _, m := range in.MTypes {
		allowed[m] = true
	}
	out := make([]store.GodNodeRow, 0, len(agg))
	for _, n := range agg {
		if len(allowed) > 0 && !allowed[n.MType] {
			continue
		}
		out = append(out, *n)
	}
	metric := func(n store.GodNodeRow) (int64, int64) {
		switch by {
		case store.GodNodeByFanIn:
			return n.FanIn, n.FanOut
		case store.GodNodeByFanOut:
			return n.FanOut, n.FanIn
		default:
			return n.FanIn + n.FanOut, 0
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ai, bi := metric(out[i])
		aj, bj := metric(out[j])
		if ai != aj {
			return ai > aj
		}
		if bi != bj {
			return bi > bj
		}
		return out[i].ObjectID < out[j].ObjectID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	if truncated {
		warnings = append(warnings, Warning{
			Code:    "truncated",
			Message: fmt.Sprintf("рёбер расширений больше %d, топ посчитан по первым из них", viewScanLimit),
			Hint:    "сузьте kinds или minConfidence",
		})
	}
	return out, warnings, nil
}
