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

// diffScanLimit: сколько рёбер расширений читается за раз там, где diff
// считается в памяти (соседи узла, god-node, карточка). Рёбер расширений на
// порядки меньше, чем базы; упор в потолок не молчит, а даёт truncated.
const diffScanLimit = 2000

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

// added: ребро из слоя расширения, связи которого (тот же вид, те же объекты
// с обеих сторон, любые их строки) в слое base нет.
func (li *layerIndex) added(e store.ObjectDataEdgeRow) (bool, error) {
	if e.Layer == baseLayer {
		return false, nil
	}
	from, err := li.groupIDs(e.FromObjectID)
	if err != nil {
		return false, err
	}
	to, err := li.groupIDs(e.ToObjectID)
	if err != nil {
		return false, err
	}
	exists, err := li.tx.ObjectDataEdgeExists(from, to, e.Kind, baseLayer)
	return !exists, err
}

// viewEdge: ребро, приведённое к режиму: концы канонические, diff посчитан.
type viewEdge struct {
	row  store.ObjectDataEdgeRow
	diff string
}

// project приводит рёбра к режиму view: в effective и diff концы заменяются
// каноническими узлами и считается пометка added, diff оставляет только
// добавленные расширениями. raw и пустой view отдают рёбра как есть.
func (li *layerIndex) project(view string, rows []store.ObjectDataEdgeRow) ([]viewEdge, error) {
	out := make([]viewEdge, 0, len(rows))
	for _, r := range rows {
		if !graphViewMerges(view) {
			out = append(out, viewEdge{row: r})
			continue
		}
		isAdded, err := li.added(r)
		if err != nil {
			return nil, err
		}
		if view == GraphViewDiff && !isAdded {
			continue
		}
		from, ok, err := li.canonical(r.FromObjectID)
		if err != nil {
			return nil, err
		}
		if ok {
			r.FromObjectID = from.ID
		}
		to, ok, err := li.canonical(r.ToObjectID)
		if err != nil {
			return nil, err
		}
		if ok {
			r.ToObjectID = to.ID
		}
		ve := viewEdge{row: r}
		if isAdded {
			ve.diff = EdgeDiffAdded
		}
		out = append(out, ve)
	}
	return out, nil
}

// edgeFilterFor дополняет фильтр рёбер узла под режим: raw читает только слой
// base, effective и diff читают все строки объекта, diff только слои
// расширений (базовые рёбра добавленными не бывают по определению).
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
	switch view {
	case GraphViewRaw:
		f.Layer = baseLayer
	case GraphViewDiff:
		f.ExcludeLayer = baseLayer
	}
	return f, nil
}

// hasExtensions: в проекте есть хотя бы один компонент-расширение.
func hasExtensions(tx *store.ReadTx) (bool, error) {
	comps, err := tx.Components()
	if err != nil {
		return false, err
	}
	for _, c := range comps {
		if c.Kind == string(domain.KindExtension) {
			return true, nil
		}
	}
	return false, nil
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
	ext, err := hasExtensions(tx)
	if err != nil {
		return nil, err
	}
	if !ext {
		return []Warning{{
			Code:    "no_extensions",
			Message: "в проекте нет расширений: effective совпадает с raw, diff пуст",
		}}, nil
	}
	if view == GraphViewDiff {
		return []Warning{{
			Code:    "diff_only_added",
			Message: "diff показывает связи, которые добавили расширения; пропавшие связи (перехват &Вместо без ПродолжитьВызов) индекс не вычисляет",
			Hint:    "чтобы проверить, не гасит ли расширение движения базы, смотрите перехватчики через get_context_for_task с view=effective",
		}}, nil
	}
	return nil, nil
}

func diffTruncatedWarning() Warning {
	return Warning{
		Code:    "truncated",
		Message: fmt.Sprintf("рёбер расширений больше %d, diff посчитан по первым из них", diffScanLimit),
		Hint:    "сузьте kinds или minConfidence",
	}
}

func rowsOfView(edges []viewEdge) []store.ObjectDataEdgeRow {
	out := make([]store.ObjectDataEdgeRow, len(edges))
	for i, e := range edges {
		out[i] = e.row
	}
	return out
}

// extensionEdgesOf читает рёбра расширений всех строк объекта node (не больше
// diffScanLimit) и приводит их к effective: концы канонические, added
// посчитан. Второе значение: упёрлись ли в потолок.
func (li *layerIndex) extensionEdgesOf(node int64, direction string, kinds []string, minConfidence float64) ([]viewEdge, bool, error) {
	filter, err := li.edgeFilterFor(GraphViewDiff, node, store.ObjectEdgeFilter{
		Direction: direction, Kinds: kinds, MinConfidence: minConfidence, Limit: diffScanLimit,
	})
	if err != nil {
		return nil, false, err
	}
	rows, err := li.tx.ObjectDataEdges(filter)
	if err != nil {
		return nil, false, err
	}
	truncated := len(rows) > diffScanLimit
	if truncated {
		rows = rows[:diffScanLimit]
	}
	edges, err := li.project(GraphViewEffective, rows)
	return edges, truncated, err
}

// viewNode строит карточку канонического узла объекта строки rowID в режиме
// view: степени по рёбрам режима, все компоненты объекта и, в effective и
// diff, рёбра расширений с именем расширения.
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

	ext, truncated, err := li.extensionEdgesOf(canon.ID, store.EdgeDirectionBoth, nil, 0)
	if err != nil {
		return NodeItem{}, nil, err
	}
	switch view {
	case GraphViewDiff:
		for _, e := range ext {
			if e.diff != EdgeDiffAdded {
				continue
			}
			if e.row.ToObjectID == canon.ID {
				item.FanIn++
			}
			if e.row.FromObjectID == canon.ID {
				item.FanOut++
			}
		}
	default:
		layer := ""
		if view == GraphViewRaw {
			layer = baseLayer
		}
		for _, dir := range []string{store.EdgeDirectionIn, store.EdgeDirectionOut} {
			n, cerr := tx.CountObjectDataEdges(store.ObjectEdgeFilter{ObjectID: canon.ID, ObjectIDs: ids, Direction: dir, Layer: layer})
			if cerr != nil {
				return NodeItem{}, nil, cerr
			}
			if dir == store.EdgeDirectionIn {
				item.FanIn = n
			} else {
				item.FanOut = n
			}
		}
	}

	if view != GraphViewRaw {
		for _, e := range ext {
			if view == GraphViewDiff && e.diff != EdgeDiffAdded {
				continue
			}
			if len(item.ExtensionEdges) >= nodeExtensionEdgeLimit {
				truncated = true
				break
			}
			ne := NodeExtensionEdge{EdgeID: e.row.ID, Layer: e.row.Layer, Kind: e.row.Kind, Diff: e.diff,
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
	}

	warnings, err := graphViewWarnings(tx, view)
	if err != nil {
		return NodeItem{}, nil, err
	}
	if truncated {
		warnings = append(warnings, Warning{
			Code:    "truncated",
			Message: fmt.Sprintf("у объекта много рёбер расширений, в карточке первые %d", nodeExtensionEdgeLimit),
			Hint:    "раскройте соседей узла в view=diff, там список полный",
		})
	}
	return item, warnings, nil
}

// neighborsDiff: соседи узла в view=diff: только рёбра, добавленные
// расширениями. Считается в памяти по рёбрам расширений (их мало), поэтому
// курсор здесь накопленный offset, как у Radius, а не id строки.
func (g *ObjectGraphService) neighborsDiff(ctx context.Context, op *openProject, in NeighborsInput, direction string, limit int, paramsKey string) (Response[EdgeItem], error) {
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
		ext, truncated, eerr := li.extensionEdgesOf(canon.ID, direction, in.Kinds, in.MinConfidence)
		if eerr != nil {
			return out, eerr
		}
		var added []viewEdge
		for _, e := range ext {
			if e.diff == EdgeDiffAdded {
				added = append(added, e)
			}
		}
		out.total = len(added)
		if out.afterIdx > len(added) {
			out.afterIdx = len(added)
		}
		out.edges = added[out.afterIdx:]
		objects, oerr := resolveEdgeObjects(tx, rowsOfView(out.edges))
		if oerr != nil {
			return out, oerr
		}
		out.objects = objects
		out.warnings, oerr = graphViewWarnings(tx, GraphViewDiff)
		if oerr != nil {
			return out, oerr
		}
		if truncated {
			out.warnings = append(out.warnings, diffTruncatedWarning())
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
		item := edgeItemFrom(e.row, res.objects)
		item.Diff = e.diff
		items = append(items, item)
	}
	resp := Response[EdgeItem]{Generation: res.gen, Warnings: res.warnings, Items: items, TotalCount: res.total}
	if hasMore {
		resp.NextCursor = EncodeCursor(res.gen, strconv.Itoa(res.afterIdx+limit), paramsKey)
	}
	return resp, nil
}

// godNodesDiff: топ узлов по числу связей, добавленных расширениями: какие
// объекты расширения меняют сильнее всего. Считается в памяти по рёбрам
// расширений всей таблицы, узлы канонические.
func godNodesDiff(tx *store.ReadTx, li *layerIndex, in GodNodesInput, by string, limit int) ([]store.GodNodeRow, []Warning, error) {
	rows, err := tx.ObjectDataEdgesOutsideLayer(baseLayer, in.Kinds, in.MinConfidence, diffScanLimit)
	if err != nil {
		return nil, nil, err
	}
	truncated := len(rows) > diffScanLimit
	if truncated {
		rows = rows[:diffScanLimit]
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
	warnings, err := graphViewWarnings(tx, GraphViewDiff)
	if err != nil {
		return nil, nil, err
	}
	if truncated {
		warnings = append(warnings, diffTruncatedWarning())
	}
	return out, warnings, nil
}
