// Package app: ObjectGraphService — единственный шов, через который граф
// объектных рёбер (internal/store: object_data_edge/object_badge, таск 03/07)
// читают оба будущих транспорта: HTTP (таск 09) и MCP-инструмент object_graph
// (таск 11). Узел, соседи, транзитивный радиус с потолком, топ перегруженных
// узлов, цепочка атрибуции ребра (тикет 08, spec §6, истории 25/26/31/32/43).
//
// Названо ObjectGraphService, а не GraphService: имя GraphService в этом
// пакете уже занято (graph.go, тикет 11 — find_references/trace_call_graph по
// СИМВОЛЬНОМУ графу вызовов, отдельная фича). Ссылка тикета 08 на «сервис
// GraphService» читается как образец устройства (см. doc-комментарий
// graph.go: «GraphService.TraceCallGraph» — «образец»), а не как требование
// занять то же имя типа в том же пакете: два типа с одинаковым именем в
// одном пакете физически невозможны.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

const (
	defaultNeighborLimit = 50
	maxNeighborLimit     = 200

	defaultRadiusDepth = 2
	maxRadiusDepth     = 6
	// radiusFetchLimit — сколько рёбер одного узла читается ЗА РАЗ во время
	// обхода радиуса. Общий потолок узлов (radiusNodesCap) всё равно не даст
	// обходу вырасти больше него, но одиночный god-node с фан-аутом в
	// десятки тысяч рёбер не должен утащить один SELECT в это же число строк.
	radiusFetchLimit = 2000

	defaultGodNodeLimit = 20
	maxGodNodeLimit     = 100

	defaultSearchLimit = 20
	maxSearchLimit     = 100

	// DefaultRadiusNodesCap — потолок узлов ответа Radius, применяется, когда
	// вызывающий (cmd/mcp1c, таск 09) не передал свой через
	// NewObjectGraphService: тот же дефолт, что у флага -graph-radius-nodes
	// (cmd/mcp1c/options.go:62, таск 06) — совпадение чисел документирует
	// связь, значение задаёт процесс через флаг, не эта константа.
	DefaultRadiusNodesCap = 300
)

// ObjectTarget адресует один узел графа: либо готовый store id (все
// {id}-маршруты HTTP §6.1 — Node тоже приходит уже с числом из предыдущего
// ответа), либо пара вид+имя объекта метаданных (вход MCP-инструмента
// object_graph, таск 11, у которого готового id ещё нет). Ровно один способ,
// как ImpactTarget (impact.go) — та же причина: два транспорта, одно правило
// разрешения.
type ObjectTarget struct {
	ObjectID   int64
	ObjectType string
	ObjectName string
	Component  string
}

// normalizeObjectTarget проверяет, что задан РОВНО один способ адресации.
func normalizeObjectTarget(t ObjectTarget) (ObjectTarget, error) {
	objectType := strings.TrimSpace(t.ObjectType)
	objectName := strings.TrimSpace(t.ObjectName)
	hasID := t.ObjectID > 0
	hasName := objectType != "" || objectName != ""
	switch {
	case hasID && hasName:
		return ObjectTarget{}, fmt.Errorf("object_graph: укажите ЛИБО objectId, ЛИБО objectType+objectName, не оба сразу")
	case hasID:
		return ObjectTarget{ObjectID: t.ObjectID}, nil
	case objectType != "" && objectName != "":
		return ObjectTarget{
			ObjectType: objectType,
			ObjectName: domain.NormalizeName(objectName),
			Component:  strings.TrimSpace(t.Component),
		}, nil
	default:
		return ObjectTarget{}, fmt.Errorf("object_graph: цель не задана — нужен objectId или objectType+objectName")
	}
}

// targetDisplay — как цель выглядит в сообщении об ошибке NotFound.
func targetDisplay(t ObjectTarget) string {
	if t.ObjectID > 0 {
		return strconv.FormatInt(t.ObjectID, 10)
	}
	return t.ObjectType + "." + t.ObjectName
}

// resolveObjectTargets находит ВСЕ строки объекта метаданных, отвечающие цели,
// упорядоченные базовым слоем вперёд (sortObjectRowsByLayer — та же очерёдность,
// что у get_object).
//
// Заимствованный в расширение объект — тот же объект базы, и вопрос «кто пишет
// в этот регистр» обязан отвечать по всем слоям сразу. Прежняя версия брала
// rows[0] и молча теряла остальные: на реальной выгрузке с расширениями запись
// Документ.ВходящиеПлатежи -> РН.ОбщийДенежныеСредстваСотрудников, которую делает
// расширение РасширениеА через &После("ОбработкаПроведения"), не показывалась
// вообще — её ребро висит на строке объекта из компоненты ext-a, а
// ответ строился по строке из cfg. Ни ошибки, ни предупреждения при этом не
// было: неполный ответ выглядел как полный.
//
// Явный component сужает до одного слоя, objectId адресует конкретный узел —
// оба случая по-прежнему дают ровно одну строку.
func resolveObjectTargets(tx *store.ReadTx, manifest workspace.Manifest, t ObjectTarget) ([]store.MetadataObjectRow, error) {
	if t.ObjectID > 0 {
		row, ok, err := tx.MetadataObjectByID(t.ObjectID)
		if err != nil || !ok {
			return nil, err
		}
		return []store.MetadataObjectRow{row}, nil
	}
	rows, err := tx.MetadataObjectsByName(t.ObjectType, t.ObjectName)
	if err != nil {
		return nil, err
	}
	if t.Component != "" {
		var only []store.MetadataObjectRow
		for _, r := range rows {
			if r.ComponentID == t.Component {
				only = append(only, r)
			}
		}
		return only, nil
	}
	return sortObjectRowsByLayer(manifest, rows), nil
}

// multipleLayersWarning честно называет остальные слои объекта. Без него
// «17 рёбер» у объекта, заимствованного расширением, не отличить от «17 рёбер,
// и это всё, что есть»: сколько слоёв участвовало, из ответа не видно.
// merged различает два случая: радиус склеивает слои, карточка узла — нет.
func multipleLayersWarning(rows []store.MetadataObjectRow, merged bool) []Warning {
	if len(rows) < 2 {
		return nil
	}
	components := make([]string, 0, len(rows))
	for _, r := range rows {
		components = append(components, r.ComponentID)
	}
	итог := fmt.Sprintf("карточка построена по слою %s", rows[0].ComponentID)
	if merged {
		итог = "рёбра собраны по всем слоям сразу"
	}
	return []Warning{{
		Code:    "object_in_multiple_layers",
		Message: fmt.Sprintf("объект есть в компонентах %s (заимствован расширением) — %s", strings.Join(components, ", "), итог),
		Hint:    "передайте component, чтобы работать с одним слоем явно",
	}}
}

// BadgeItem — один бейдж узла (has-dynamic/attribution-truncated/
// attribution-stale, все три — R27/R14/D03 равноправны, фильтр не должен
// терять ни один — interfaces.md, ревью таска 06).
type BadgeItem struct {
	Badge string `json:"badge"`
	Layer string `json:"layer,omitempty"`
	Count int64  `json:"count"`
}

// NodeItem — карточка узла: сам объект метаданных плюс его бейджи.
type NodeItem struct {
	ObjectID    int64       `json:"objectId"`
	Component   string      `json:"component"`
	MType       string      `json:"mtype"`
	NameDisplay string      `json:"nameDisplay"`
	Synonym     string      `json:"synonym,omitempty"`
	Layer       string      `json:"layer"`
	Badges      []BadgeItem `json:"badges,omitempty"`
	// FanIn/FanOut: все рёбра узла без фильтра карты: по ним карта видит,
	// раскрыт ли узел целиком.
	FanIn  int64 `json:"fanIn"`
	FanOut int64 `json:"fanOut"`
}

// EdgeItem — одно ребро object_data_edge, денормализованное для показа: оба
// конца несут вид и отображаемое имя, чтобы SPA/агенту не приходилось
// отдельным вызовом резолвить Node по каждому id.
type EdgeItem struct {
	ID            int64   `json:"id"`
	FromObjectID  int64   `json:"fromObjectId"`
	FromMType     string  `json:"fromMType"`
	FromDisplay   string  `json:"fromDisplay"`
	ToObjectID    int64   `json:"toObjectId"`
	ToMType       string  `json:"toMType"`
	ToDisplay     string  `json:"toDisplay"`
	Kind          string  `json:"kind"`
	Layer         string  `json:"layer"`
	Provenance    string  `json:"provenance"`
	Confidence    float64 `json:"confidence"`
	Mode          string  `json:"mode,omitempty"`
	InTransaction *bool   `json:"inTransaction,omitempty"`
}

// RadiusEdgeItem — EdgeItem плюс глубина, на которой ребро встретил обход
// радиуса (0 — рёбра, оба конца которых уже посещены на этом же уровне не
// бывает нулевой глубины: корень сам по себе рёбер не имеет).
type RadiusEdgeItem struct {
	EdgeItem
	Depth int `json:"depth"`
}

// GodNodeItem — один перегруженный узел панели god-node.
type GodNodeItem struct {
	ObjectID    int64  `json:"objectId"`
	MType       string `json:"mtype"`
	NameDisplay string `json:"nameDisplay"`
	FanIn       int64  `json:"fanIn"`
	FanOut      int64  `json:"fanOut"`
}

// EvidenceChainStepItem — одно звено цепочки атрибуции кодового ребра,
// денормализованное для показа (символ по имени, не только id).
type EvidenceChainStepItem struct {
	SymbolUID  string      `json:"symbolUid,omitempty"`
	SymbolName string      `json:"symbolName,omitempty"`
	Component  string      `json:"component,omitempty"`
	Module     string      `json:"module,omitempty"`
	Span       domain.Span `json:"span,omitempty"`
	Confidence float64     `json:"confidence,omitempty"`
}

// EvidenceFileItem — файл, от которого зависит ребро (BSL-модуль кодовой
// цепочки или XML владельца декларированного ребра).
type EvidenceFileItem struct {
	Component string `json:"component"`
	RelPath   string `json:"relPath"`
}

// EdgeEvidenceItem — items[0] ответа EdgeEvidence: цепочка атрибуции для
// кодовых рёбер (Provenance=code), файл+имя регистра для декларированных
// (Provenance=metadata-declared) — «клик по ребру показывает evidence» (§29).
type EdgeEvidenceItem struct {
	EdgeID           int64                   `json:"edgeId"`
	FromObjectID     int64                   `json:"fromObjectId"`
	ToObjectID       int64                   `json:"toObjectId"`
	Kind             string                  `json:"kind"`
	Provenance       string                  `json:"provenance"`
	Confidence       float64                 `json:"confidence"`
	Chain            []EvidenceChainStepItem `json:"chain,omitempty"`
	XMLFile          string                  `json:"xmlFile,omitempty"`
	DeclaredRegister string                  `json:"declaredRegister,omitempty"`
	Files            []EvidenceFileItem      `json:"files,omitempty"`
}

// ObjectGraphService — сервис за Node/Neighbors/Radius/GodNodes/EdgeEvidence.
type ObjectGraphService struct {
	projects       *Projects
	radiusNodesCap int
	// radiusFetchLimit — поле, не константа: продакшен-путь всегда получает
	// пакетную radiusFetchLimit через конструктор, но фикстура на 2000+
	// рёбер одного узла ради теста потолка выборки была бы неоправданно
	// дорогой — тест снижает это поле напрямую на готовом сервисе (см.
	// radiusBFS/doc-комментарий Radius: это ВТОРОЙ, отдельный от
	// radiusNodesCap потолок).
	radiusFetchLimit int
}

// NewObjectGraphService строит сервис поверх общего резолвера проектов.
// radiusNodesCap<=0 берёт DefaultRadiusNodesCap: вызывающий (таск 09) обязан
// передать значение флага -graph-radius-nodes, тест — своё маленькое число,
// чтобы проверить обрезание без фикстуры в сотни узлов.
func NewObjectGraphService(p *Projects, radiusNodesCap int) *ObjectGraphService {
	if radiusNodesCap <= 0 {
		radiusNodesCap = DefaultRadiusNodesCap
	}
	return &ObjectGraphService{projects: p, radiusNodesCap: radiusNodesCap, radiusFetchLimit: radiusFetchLimit}
}

// resolveProject picks which project a call reads from: root, when
// non-empty, is a one-off override resolved BY ROOT (Projects.ByRoot) — the
// active project of the process is left untouched, in contrast to
// index_status's project=, which only compares against Active
// (cmd/mcp1c/idx_status.go). Empty root falls back to the usual
// Projects.Active. Only Radius takes ProjectRoot today (object_graph, тикет
// 11); the other four methods stay on Active — nothing in their tickets asks
// for an override, and adding one unused would be scope no one requested.
func (g *ObjectGraphService) resolveProject(ctx context.Context, root string) (*openProject, error) {
	if strings.TrimSpace(root) == "" {
		return g.projects.Active(ctx)
	}
	return g.projects.ByRoot(ctx, root)
}

// NodeInput — вход Node.
type NodeInput struct {
	Target ObjectTarget
}

// Node отдаёт карточку одного узла: сам объект плюс его бейджи (R27).
func (g *ObjectGraphService) Node(ctx context.Context, in NodeInput) (Response[NodeItem], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[NodeItem]{}, err
	}
	target, terr := normalizeObjectTarget(in.Target)
	if terr != nil {
		return Response[NodeItem]{}, terr
	}

	type txResult struct {
		item     NodeItem
		gen      domain.Generation
		ok       bool
		warnings []Warning
	}
	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		rows, rerr := resolveObjectTargets(tx, op.Manifest, target)
		if rerr != nil {
			return out, rerr
		}
		if len(rows) == 0 {
			return out, nil
		}
		// Карточка узла — про ОДИН узел (её id уходит в Neighbors), поэтому
		// слои здесь не склеиваются: берётся базовый, остальные названы
		// предупреждением.
		row := rows[0]
		badges, berr := tx.ObjectBadges(row.ID)
		if berr != nil {
			return out, berr
		}
		fanIn, fanOut, derr := tx.ObjectDegree(row.ID)
		if derr != nil {
			return out, derr
		}
		out.ok = true
		out.item = nodeItemFrom(row, badges)
		out.item.FanIn, out.item.FanOut = fanIn, fanOut
		out.warnings = multipleLayersWarning(rows, false)
		return out, nil
	})
	if err != nil {
		return Response[NodeItem]{}, err
	}
	if !res.ok {
		return Response[NodeItem]{}, NotFoundError("объект метаданных", targetDisplay(target), nil).
			WithProject(op.Entry.ID).WithGeneration(res.gen)
	}
	return Response[NodeItem]{Generation: res.gen, Warnings: res.warnings, Items: []NodeItem{res.item}, TotalCount: 1}, nil
}

func nodeItemFrom(row store.MetadataObjectRow, badges []store.ObjectBadge) NodeItem {
	item := NodeItem{
		ObjectID: row.ID, Component: row.ComponentID, MType: row.MType,
		NameDisplay: row.NameDisplay, Synonym: row.Synonym, Layer: row.Layer,
	}
	for _, b := range badges {
		item.Badges = append(item.Badges, BadgeItem{Badge: b.Badge, Layer: b.Layer, Count: b.Count})
	}
	return item
}

// NeighborsInput — вход Neighbors: соседи ОДНОГО узла, одна страница.
type NeighborsInput struct {
	ObjectID      int64
	Direction     string // in|out|both; пусто — both
	Kinds         []string
	Layer         string
	MinConfidence float64
	Limit         int
	Cursor        string
}

// Neighbors отдаёт одну страницу рёбер узла (R25: «клик по узлу догружает
// соседей отдельным запросом»). Различает объект-не-найден (NotFound) от
// объекта-без-соседей (200, пустой список, TotalCount=0) — R22.1/§43.
func (g *ObjectGraphService) Neighbors(ctx context.Context, in NeighborsInput) (Response[EdgeItem], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[EdgeItem]{}, err
	}
	direction, derr := normalizeDirection(in.Direction)
	if derr != nil {
		return Response[EdgeItem]{}, derr.WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultNeighborLimit, maxNeighborLimit)
	paramsKey := fmt.Sprintf("id=%d&dir=%s&kinds=%s&layer=%s&minc=%v&l=%d",
		in.ObjectID, direction, strings.Join(in.Kinds, ","), in.Layer, in.MinConfidence, limit)

	type txResult struct {
		rows    []store.ObjectDataEdgeRow
		objects map[int64]store.MetadataObjectRow
		total   int64
		gen     domain.Generation
		found   bool
	}
	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		if _, ok, oerr := tx.MetadataObjectByID(in.ObjectID); oerr != nil {
			return out, oerr
		} else if !ok {
			return out, nil
		}
		out.found = true
		key, derr := DecodeCursor(in.Cursor, gen, paramsKey)
		if derr != nil {
			return out, derr
		}
		var afterID int64
		if key != "" {
			afterID, _ = strconv.ParseInt(key, 10, 64)
		}
		filter := store.ObjectEdgeFilter{
			ObjectID: in.ObjectID, Direction: direction, Kinds: in.Kinds, Layer: in.Layer,
			MinConfidence: in.MinConfidence, AfterID: afterID, Limit: limit,
		}
		rows, rerr := tx.ObjectDataEdges(filter)
		if rerr != nil {
			return out, rerr
		}
		out.rows = rows
		total, cerr := tx.CountObjectDataEdges(filter)
		if cerr != nil {
			return out, cerr
		}
		out.total = total
		objects, oerr := resolveEdgeObjects(tx, rows)
		if oerr != nil {
			return out, oerr
		}
		out.objects = objects
		return out, nil
	})
	if err != nil {
		return Response[EdgeItem]{}, err
	}
	if !res.found {
		return Response[EdgeItem]{}, NotFoundError("объект метаданных", strconv.FormatInt(in.ObjectID, 10), nil).
			WithProject(op.Entry.ID).WithGeneration(res.gen)
	}

	hasMore := len(res.rows) > limit
	if hasMore {
		res.rows = res.rows[:limit]
	}
	items := make([]EdgeItem, 0, len(res.rows))
	for _, r := range res.rows {
		items = append(items, edgeItemFrom(r, res.objects))
	}
	resp := Response[EdgeItem]{Generation: res.gen, Items: items, TotalCount: int(res.total)}
	if hasMore {
		resp.NextCursor = EncodeCursor(res.gen, strconv.FormatInt(res.rows[len(res.rows)-1].ID, 10), paramsKey)
	}
	return resp, nil
}

// resolveEdgeObjects резолвит оба конца каждого ребра одним проходом,
// избегая повторного MetadataObjectByID на дублирующийся id.
func resolveEdgeObjects(tx *store.ReadTx, rows []store.ObjectDataEdgeRow) (map[int64]store.MetadataObjectRow, error) {
	out := make(map[int64]store.MetadataObjectRow, len(rows)*2)
	for _, r := range rows {
		for _, id := range [2]int64{r.FromObjectID, r.ToObjectID} {
			if _, have := out[id]; have {
				continue
			}
			row, ok, err := tx.MetadataObjectByID(id)
			if err != nil {
				return nil, err
			}
			if ok {
				out[id] = row
			}
		}
	}
	return out, nil
}

func edgeItemFrom(r store.ObjectDataEdgeRow, objects map[int64]store.MetadataObjectRow) EdgeItem {
	from := objects[r.FromObjectID]
	to := objects[r.ToObjectID]
	return EdgeItem{
		ID: r.ID, FromObjectID: r.FromObjectID, FromMType: from.MType, FromDisplay: from.NameDisplay,
		ToObjectID: r.ToObjectID, ToMType: to.MType, ToDisplay: to.NameDisplay,
		Kind: r.Kind, Layer: r.Layer, Provenance: r.Provenance, Confidence: r.Confidence,
		Mode: r.Mode, InTransaction: r.InTransaction,
	}
}

// RadiusInput — вход Radius: транзитивный обход от корня, ограниченный
// глубиной И потолком узлов (radiusNodesCap сервиса).
type RadiusInput struct {
	Target        ObjectTarget
	Direction     string
	Depth         int
	Kinds         []string
	Layer         string
	MinConfidence float64
	Limit         int
	Cursor        string
	// ProjectRoot — разовый override активного проекта (тикет 11, spec.md
	// §8, D3): непустой root резолвится через Projects.ByRoot и читает ИМЕННО
	// его данные, не трогая активный проект реестра. Пусто — обычный
	// Projects.Active, как у всех остальных методов сервиса.
	ProjectRoot string
}

// radiusCandidate — одно ребро, найденное на текущем уровне обхода, прежде
// чем решено, влезает ли его новый конец в потолок узлов.
type radiusCandidate struct {
	row    store.ObjectDataEdgeRow
	other  int64
	isFrom bool // true, если other — from_object_id (значит текущий узел — to)
}

// radiusTruncation — ПОЧЕМУ Radius не гарантированно вернул полный подграф:
// именованные поля, не два позиционных bool подряд. Ревью качества (стоп-
// гейт) нашло, что radiusTruncationWarnings(radiusNodesCap, radiusFetchLim,
// byCap, byFetch bool) держит связь причины и текста предупреждения только
// на ПОЗИЦИИ параметров — компилятор не ловит перепутанный порядок, а
// смысл при перестановке byCap/byFetch переворачивается на противоположный
// (потолок confidence выдаёт себя за потолок id, и наоборот). Структура с
// именованными полями делает эту ошибку невозможной для компилятора, а не
// только отловленной тестом.
type radiusTruncation struct {
	ByCap   bool // потолок узлов radiusNodesCap — резали по confidence, осмысленно
	ByFetch bool // потолок выборки одного узла radiusFetchLimit — резали по id, НЕ по confidence
}

// Radius — транзитивный обход от корня на глубину Depth, ответ ограничен
// потолком узлов сервиса (R22, §26): упор в потолок ДАЁТ Warnings с кодом
// truncated и признаком confidence — при нехватке бюджета сначала обрезаются
// рёбра/узлы с НАИМЕНЬШЕЙ достоверностью (candidates сортируются по
// Confidence по убыванию перед распределением бюджета), молчаливого
// обрезания нет.
//
// Второй, отдельный потолок — radiusFetchLimit, сколько рёбер ОДНОГО узла
// читается за один SELECT. store.ObjectDataEdges сортирует по id (это
// требование её же курсорной пагинации — AfterID/ORDER BY id, менять здесь
// нельзя, иначе сломается Neighbors), НЕ по confidence. Если у узла рёбер
// больше radiusFetchLimit, SQL уже отбросил лишнее по id ДО того, как этот
// метод дойдёт до сортировки по confidence: для такого узла отбор НЕ
// гарантированно по достоверности. Это тоже упор в потолок и тоже не должно
// молчать — TotalCount отдаёт реальный размер обрезанного потолком узлов
// результата (СОГЛАСОВАНО с Neighbors: там же — tx.CountObjectDataEdges,
// здесь — размер items ДО постраничной нарезки, а не длина одной страницы).
func (g *ObjectGraphService) Radius(ctx context.Context, in RadiusInput) (Response[RadiusEdgeItem], error) {
	op, err := g.resolveProject(ctx, in.ProjectRoot)
	if err != nil {
		return Response[RadiusEdgeItem]{}, err
	}
	target, terr := normalizeObjectTarget(in.Target)
	if terr != nil {
		return Response[RadiusEdgeItem]{}, terr
	}
	direction, derr := normalizeDirection(in.Direction)
	if derr != nil {
		return Response[RadiusEdgeItem]{}, derr.WithProject(op.Entry.ID)
	}
	depth := in.Depth
	if depth <= 0 {
		depth = defaultRadiusDepth
	}
	if depth > maxRadiusDepth {
		depth = maxRadiusDepth
	}
	limit := clampLimit(in.Limit, defaultNeighborLimit, maxNeighborLimit)
	paramsKey := fmt.Sprintf("t=%s&dir=%s&depth=%d&kinds=%s&layer=%s&minc=%v&l=%d",
		targetDisplay(target), direction, depth, strings.Join(in.Kinds, ","), in.Layer, in.MinConfidence, limit)

	type txResult struct {
		edges      []RadiusEdgeItem
		total      int
		gen        domain.Generation
		found      bool
		truncation radiusTruncation
		afterIdx   int
		warnings   []Warning
	}
	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		rows, rerr := resolveObjectTargets(tx, op.Manifest, target)
		if rerr != nil {
			return out, rerr
		}
		if len(rows) == 0 {
			return out, nil
		}
		out.found = true
		out.warnings = multipleLayersWarning(rows, true)
		roots := make([]int64, 0, len(rows))
		for _, r := range rows {
			roots = append(roots, r.ID)
		}
		key, derr := DecodeCursor(in.Cursor, gen, paramsKey)
		if derr != nil {
			return out, derr
		}
		var afterIdx int
		if key != "" {
			afterIdx, _ = strconv.Atoi(key)
		}

		edges, objects, truncation, berr := g.radiusBFS(tx, roots, direction, in.Kinds, in.Layer, in.MinConfidence, depth)
		if berr != nil {
			return out, berr
		}
		out.truncation = truncation
		items := make([]RadiusEdgeItem, 0, len(edges))
		for _, e := range edges {
			items = append(items, RadiusEdgeItem{EdgeItem: edgeItemFrom(e.row, objects), Depth: e.depth})
		}
		// total — размер ВСЕГО обрезанного потолком узлов результата, ДО
		// постраничной нарезки ниже: тот же смысл, что у Neighbors
		// (tx.CountObjectDataEdges независимо от AfterID), не длина страницы.
		out.total = len(items)
		if afterIdx > len(items) {
			afterIdx = len(items)
		}
		out.afterIdx = afterIdx
		out.edges = items[afterIdx:]
		return out, nil
	})
	if err != nil {
		return Response[RadiusEdgeItem]{}, err
	}
	if !res.found {
		return Response[RadiusEdgeItem]{}, NotFoundError("объект метаданных", targetDisplay(target), nil).
			WithProject(op.Entry.ID).WithGeneration(res.gen)
	}

	hasMore := len(res.edges) > limit
	if hasMore {
		res.edges = res.edges[:limit]
	}
	resp := Response[RadiusEdgeItem]{Generation: res.gen, Warnings: res.warnings, Items: res.edges, TotalCount: res.total}
	if hasMore {
		resp.NextCursor = EncodeCursor(res.gen, strconv.Itoa(res.afterIdx+limit), paramsKey)
	}
	resp.Warnings = append(resp.Warnings, radiusTruncationWarnings(g.radiusNodesCap, g.radiusFetchLimit, res.truncation)...)
	return resp, nil
}

// radiusTruncationWarnings строит предупреждения об обрезании радиуса.
// Два РАЗНЫХ потолка дают два РАЗНЫХ по смыслу предупреждения (общий код
// truncated, разный текст — см. doc-комментарий Radius): потолок узлов режет
// по confidence осмысленно, потолок выборки одного узла (radiusFetchLimit)
// режет по id по причинам, не связанным с достоверностью, и обещать
// «резали по confidence» в этом случае значило бы соврать.
func radiusTruncationWarnings(radiusNodesCap, radiusFetchLim int, t radiusTruncation) []Warning {
	var warns []Warning
	if t.ByCap {
		warns = append(warns, Warning{
			Code: "truncated",
			Message: fmt.Sprintf("радиус остановлен на потолке %d узлов; часть графа могла остаться неисследованной",
				radiusNodesCap),
			Hint: "резались узлы/рёбра с наименьшей достоверностью (confidence); сузьте depth/kinds/minConfidence или начните с более конкретного узла",
		})
	}
	if t.ByFetch {
		warns = append(warns, Warning{
			Code:    "truncated",
			Message: fmt.Sprintf("у одного или нескольких узлов рёбер по фильтру оказалось больше %d за один шаг обхода; часть графа могла остаться неисследованной", radiusFetchLim),
			Hint:    "для таких узлов отбор шёл по id (порядок вставки), а НЕ по confidence — признак «резали по достоверности» здесь не гарантирован; сузьте kinds/minConfidence или начните с менее перегруженного узла",
		})
	}
	return warns
}

type radiusEdge struct {
	row   store.ObjectDataEdgeRow
	depth int
}

// radiusBFS — сам обход: по уровням, на каждом уровне рёбра-кандидаты
// сортируются по confidence по убыванию, и бюджет (radiusNodesCap) отдаётся
// сначала самым достоверным. Ребро между уже посещёнными узлами включается
// в результат всегда (бюджет узлов не тратит), даже если оно не открывает
// новый узел, — иначе локальная петля внутри уже раскрытого радиуса
// пропала бы с карты.
// radiusBFS обходит граф от узлов roots. Корней больше одного, когда объект
// заимствован расширением: строки объекта в базе и в расширении — разные узлы
// одного и того же объекта конфигурации, и рёбра у них свои (см.
// resolveObjectTargets). Все корни лежат на нулевом уровне: ребро из слоя
// расширения имеет ту же глубину, что и ребро из базы.
func (g *ObjectGraphService) radiusBFS(tx *store.ReadTx, roots []int64, direction string, kinds []string, layer string, minConfidence float64, depth int) ([]radiusEdge, map[int64]store.MetadataObjectRow, radiusTruncation, error) {
	visited := make(map[int64]bool, len(roots))
	frontier := make([]int64, 0, len(roots))
	for _, id := range roots {
		if visited[id] {
			continue
		}
		visited[id] = true
		frontier = append(frontier, id)
	}
	seenEdge := map[int64]bool{}
	var result []radiusEdge
	var truncation radiusTruncation

	for level := 1; level <= depth && len(frontier) > 0; level++ {
		var candidates []radiusCandidate
		for _, node := range frontier {
			filter := store.ObjectEdgeFilter{
				ObjectID: node, Direction: direction, Kinds: kinds, Layer: layer,
				MinConfidence: minConfidence, Limit: g.radiusFetchLimit,
			}
			rows, err := tx.ObjectDataEdges(filter)
			if err != nil {
				return nil, nil, radiusTruncation{}, err
			}
			if len(rows) > g.radiusFetchLimit {
				// ObjectDataEdges сортирует по id (Limit+1 идиома её
				// собственной пагинации), не по confidence: у узла нашлось
				// больше radiusFetchLimit рёбер под фильтром, и лишнее
				// отброшено ДО того, как этот метод дойдёт до сортировки по
				// confidence ниже. Отбор для этого узла НЕ гарантированно по
				// достоверности — молчать об этом нельзя (§26).
				truncation.ByFetch = true
				rows = rows[:g.radiusFetchLimit]
			}
			for _, r := range rows {
				if seenEdge[r.ID] {
					continue
				}
				seenEdge[r.ID] = true
				other, isFrom := r.ToObjectID, false
				if r.FromObjectID != node {
					other, isFrom = r.FromObjectID, true
				}
				candidates = append(candidates, radiusCandidate{row: r, other: other, isFrom: isFrom})
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].row.Confidence > candidates[j].row.Confidence
		})
		var next []int64
		for _, c := range candidates {
			isNew := !visited[c.other]
			if isNew {
				if len(visited) >= g.radiusNodesCap {
					truncation.ByCap = true
					continue
				}
				visited[c.other] = true
				next = append(next, c.other)
			}
			result = append(result, radiusEdge{row: c.row, depth: level})
		}
		frontier = next
	}

	objects, err := resolveEdgeObjects(tx, rowsOf(result))
	if err != nil {
		return nil, nil, radiusTruncation{}, err
	}
	return result, objects, truncation, nil
}

func rowsOf(edges []radiusEdge) []store.ObjectDataEdgeRow {
	out := make([]store.ObjectDataEdgeRow, len(edges))
	for i, e := range edges {
		out[i] = e.row
	}
	return out
}

// GodNodesInput — вход GodNodes: топ N перегруженных узлов, без страничности
// (§6: «отдаёт топ N» — не полное перечисление, курсор здесь не нужен).
type GodNodesInput struct {
	Kinds         []string
	Layer         string
	MinConfidence float64
	MTypes        []string
	By            string // fan-in|fan-out|total; пусто — total
	Limit         int
}

// GodNodes отдаёт топ узлов по fan-in/fan-out, посчитанный по тем же рёбрам,
// что видит карта (R28).
func (g *ObjectGraphService) GodNodes(ctx context.Context, in GodNodesInput) (Response[GodNodeItem], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[GodNodeItem]{}, err
	}
	by, berr := normalizeGodNodeMetric(in.By)
	if berr != nil {
		return Response[GodNodeItem]{}, berr.WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultGodNodeLimit, maxGodNodeLimit)

	type txResult struct {
		rows []store.GodNodeRow
		gen  domain.Generation
	}
	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		rows, rerr := tx.GodNodes(store.GodNodeFilter{
			Kinds: in.Kinds, Layer: in.Layer, MinConfidence: in.MinConfidence,
			MTypes: in.MTypes, By: by, Limit: limit,
		})
		if rerr != nil {
			return out, rerr
		}
		out.rows = rows
		return out, nil
	})
	if err != nil {
		return Response[GodNodeItem]{}, err
	}
	items := make([]GodNodeItem, 0, len(res.rows))
	for _, r := range res.rows {
		items = append(items, GodNodeItem{ObjectID: r.ObjectID, MType: r.MType, NameDisplay: r.NameDisplay,
			FanIn: r.FanIn, FanOut: r.FanOut})
	}
	return Response[GodNodeItem]{Generation: res.gen, Items: items, TotalCount: len(items)}, nil
}

// normalizeGodNodeMetric разбирает ось сортировки god-node — тем же способом,
// каким normalizeDirection разбирает направление: закрытый перечень, пусто —
// умолчание (total), неизвестное значение — ошибка ВВОДА. Разбора здесь не
// было вовсе, и опечатка доезжала до store, откуда возвращалась обычной
// ошибкой и выглядела снаружи внутренним сбоем (HTTP 500 internal).
func normalizeGodNodeMetric(raw string) (string, *Error) {
	if raw == "" {
		return store.GodNodeByTotal, nil
	}
	switch raw {
	case store.GodNodeByFanIn, store.GodNodeByFanOut, store.GodNodeByTotal:
		return raw, nil
	default:
		return "", NewError(CodeInvalidArgument,
			fmt.Sprintf("ось god-node %q неизвестна", raw),
			fmt.Sprintf("допустимо %s, %s или %s", store.GodNodeByFanIn, store.GodNodeByFanOut, store.GodNodeByTotal))
	}
}

// evidencePayload — зеркало internal/index.edgeEvidence (internal/index/
// objectedges.go:53) по JSON-контракту, а не по типу: edgeEvidence
// неэкспортирован, и это данные, записанные в object_data_edge.evidence
// прошлой публикацией, а не структура в памяти — зеркалить контракт полей
// здесь корректно, тянуть internal/index ради непубличного типа было бы
// нельзя (Go этого и не позволил бы). Смена набора полей в edgeEvidence
// обязана поменять и это зеркало.
type evidencePayload struct {
	Chain []struct {
		SymbolID   int64       `json:"symbolId"`
		FileID     int64       `json:"fileId"`
		Confidence float64     `json:"confidence,omitempty"`
		Span       domain.Span `json:"span,omitempty"`
	} `json:"chain,omitempty"`
	XMLFile          string `json:"xmlFile,omitempty"`
	DeclaredRegister string `json:"declaredRegister,omitempty"`
}

// EdgeEvidenceInput — вход EdgeEvidence.
type EdgeEvidenceInput struct {
	EdgeID int64
}

// EdgeEvidence отдаёт «почему это ребро существует» (§32): цепочку
// атрибуции для кодовых рёбер (символ по имени, файл, span), файл+имя
// регистра для декларированных.
func (g *ObjectGraphService) EdgeEvidence(ctx context.Context, in EdgeEvidenceInput) (Response[EdgeEvidenceItem], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[EdgeEvidenceItem]{}, err
	}

	type txResult struct {
		item EdgeEvidenceItem
		gen  domain.Generation
		ok   bool
	}
	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		row, ok, rerr := tx.ObjectDataEdgeByID(in.EdgeID)
		if rerr != nil {
			return out, rerr
		}
		if !ok {
			return out, nil
		}
		out.ok = true
		item := EdgeEvidenceItem{
			EdgeID: row.ID, FromObjectID: row.FromObjectID, ToObjectID: row.ToObjectID,
			Kind: row.Kind, Provenance: row.Provenance, Confidence: row.Confidence,
		}
		var payload evidencePayload
		if row.Evidence != "" {
			if jerr := json.Unmarshal([]byte(row.Evidence), &payload); jerr != nil {
				return out, fmt.Errorf("ребро %d: разбор evidence: %w", row.ID, jerr)
			}
		}
		item.XMLFile = payload.XMLFile
		item.DeclaredRegister = payload.DeclaredRegister
		for _, step := range payload.Chain {
			cs := EvidenceChainStepItem{Confidence: step.Confidence, Span: step.Span}
			if sym, sok, serr := tx.SymbolByID(step.SymbolID); serr != nil {
				return out, serr
			} else if sok {
				cs.SymbolUID, cs.SymbolName, cs.Component, cs.Module = sym.UID, sym.NameDisplay, sym.ComponentID, sym.ModulePath
			}
			item.Chain = append(item.Chain, cs)
		}
		files, ferr := tx.ObjectDataEdgeFiles(row.ID)
		if ferr != nil {
			return out, ferr
		}
		for _, f := range files {
			item.Files = append(item.Files, EvidenceFileItem{Component: f.ComponentID, RelPath: f.RelPath})
		}
		out.item = item
		return out, nil
	})
	if err != nil {
		return Response[EdgeEvidenceItem]{}, err
	}
	if !res.ok {
		return Response[EdgeEvidenceItem]{}, NotFoundError("ребро объектного графа", strconv.FormatInt(in.EdgeID, 10), nil).
			WithProject(op.Entry.ID).WithGeneration(res.gen)
	}
	return Response[EdgeEvidenceItem]{Generation: res.gen, Items: []EdgeEvidenceItem{res.item}, TotalCount: 1}, nil
}

// SearchItem: объект метаданных, найденный по части имени: точка входа на
// карту для человека, который не знает числового id узла.
type SearchItem struct {
	ObjectID    int64  `json:"objectId"`
	Component   string `json:"component"`
	MType       string `json:"mtype"`
	NameDisplay string `json:"nameDisplay"`
	Synonym     string `json:"synonym,omitempty"`
	Layer       string `json:"layer"`
	// Match: ярус совпадения: exact, prefix или contains.
	Match  string `json:"match"`
	FanIn  int64  `json:"fanIn"`
	FanOut int64  `json:"fanOut"`
}

// SearchInput: вход Search.
type SearchInput struct {
	Query string
	Limit int
}

var searchMatchNames = map[int]string{
	store.ObjectSearchExact:    "exact",
	store.ObjectSearchPrefix:   "prefix",
	store.ObjectSearchContains: "contains",
}

// Search ищет объекты метаданных по подстроке имени без учёта регистра:
// сначала точные совпадения, затем по префиксу, затем по вхождению. Синоним
// не участвует: LOWER в SQLite не понижает кириллицу, а name_norm уже
// хранится в нижнем регистре.
func (g *ObjectGraphService) Search(ctx context.Context, in SearchInput) (Response[SearchItem], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[SearchItem]{}, err
	}
	q := domain.NormalizeName(strings.TrimSpace(in.Query))
	if q == "" {
		return Response[SearchItem]{}, NewError(CodeInvalidArgument,
			"пустая строка поиска", "передайте часть имени объекта, например q=Заказ").WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultSearchLimit, maxSearchLimit)

	type txResult struct {
		rows []store.ObjectSearchRow
		gen  domain.Generation
	}
	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		// Строка сверх лимита: признак «есть ещё», как у страничных выборок.
		rows, rerr := tx.SearchObjectsByName(q, limit+1)
		if rerr != nil {
			return out, rerr
		}
		out.rows = rows
		return out, nil
	})
	if err != nil {
		return Response[SearchItem]{}, err
	}
	var warnings []Warning
	if len(res.rows) > limit {
		res.rows = res.rows[:limit]
		warnings = append(warnings, Warning{
			Code:    "search_truncated",
			Message: fmt.Sprintf("показаны первые %d совпадений", limit),
			Hint:    "уточните строку поиска",
		})
	}
	items := make([]SearchItem, 0, len(res.rows))
	for _, r := range res.rows {
		items = append(items, SearchItem{
			ObjectID: r.ID, Component: r.ComponentID, MType: r.MType, NameDisplay: r.NameDisplay,
			Synonym: r.Synonym, Layer: r.Layer, Match: searchMatchNames[r.Tier], FanIn: r.FanIn, FanOut: r.FanOut,
		})
	}
	return Response[SearchItem]{Generation: res.gen, Warnings: warnings, Items: items, TotalCount: len(items)}, nil
}
