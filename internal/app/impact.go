// Package app: ImpactService — find_impact (тикет 13, R44/R26.5, spec
// истории 30-31, архитектура §19 «find_impact = обратный BFS от объекта/
// символа по выбранным видам рёбер, ranked»).
package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// impactPageSize — размер страницы ranked-списка find_impact. BFS сам
// ограничен Budget (общий лимит посещённых узлов), пагинация — отдельный,
// более мелкий срез уже собранного результата (R61: лимит на каждый список).
const impactPageSize = 50

// ImpactDefaultDepth/ImpactMaxDepth/ImpactDefaultBudget/ImpactMaxBudget —
// умолчания и потолки входа find_impact. Потолки существуют по той же
// причине, что у deppaths.go (internal/source): глубже нескольких шагов
// граф конфигурации плотный, и ответ становится шумом, а не сигналом.
const (
	ImpactDefaultDepth  = 3
	ImpactMaxDepth      = 8
	ImpactDefaultBudget = 500
	ImpactMaxBudget     = 5000
)

// ImpactTarget — цель find_impact: символ ИЛИ объект метаданных, взаимно
// исключающе.
type ImpactTarget struct {
	SymbolUID  string
	ObjectType string
	ObjectName string
	Component  string
}

// ImpactInput — вход find_impact (архитектура §21: «цель, kinds?, depth,
// budget»; тело тикета 13 добавляет view и cursor).
type ImpactInput struct {
	Target ImpactTarget
	Kinds  []string
	Depth  int
	Budget int
	View   string
	Cursor string
}

// ImpactPathStep — один шаг пути от цели до найденного элемента: вид ребра
// на этом шаге, узел, в который ребро ведёт, и его provenance/confidence
// (критерий приёмки тикета 13: «путь до каждого элемента называет вид ребра
// на каждом шаге»).
type ImpactPathStep struct {
	EdgeKind   string            `json:"edgeKind"`
	Detail     string            `json:"detail,omitempty"`
	NodeKind   string            `json:"nodeKind"`
	Component  string            `json:"component"`
	Display    string            `json:"display"`
	Resolution string            `json:"resolution,omitempty"`
	Confidence domain.Confidence `json:"confidence"`
	Layer      string            `json:"layer,omitempty"`
}

// ImpactItem — один затронутый элемент: узел плюс полный путь от цели до
// него. Поля верхнего уровня дублируют последний шаг Path для удобства
// сортировки/чтения без разбора пути клиентом.
type ImpactItem struct {
	NodeKind   string            `json:"nodeKind"`
	Component  string            `json:"component"`
	Display    string            `json:"display"`
	Depth      int               `json:"depth"`
	EdgeKind   string            `json:"edgeKind"`
	Detail     string            `json:"detail,omitempty"`
	Resolution string            `json:"resolution,omitempty"`
	Confidence domain.Confidence `json:"confidence"`
	Layer      string            `json:"layer,omitempty"`
	Path       []ImpactPathStep  `json:"path"`
}

// visitKey — ключ посещённого узла обхода. Роль (store.ImpactEdge:
// FromNodeKind="role") не является node-сущностью и живёт в отдельном
// id-пространстве (раздел 15 схемы) — без NodeKind в ключе role.id=5 и
// symbol.id=5 схлопнулись бы в один и тот же посещённый узел.
type visitKey struct {
	kind string
	id   int64
}

// ImpactService — сервис за find_impact.
type ImpactService struct {
	projects *Projects
}

// NewImpactService строит сервис поверх общего резолвера проектов.
func NewImpactService(projects *Projects) *ImpactService {
	return &ImpactService{projects: projects}
}

// Impact выполняет обратный BFS от цели по выбранным видам рёбер и
// возвращает ranked список затронутого с путями и provenance.
func (s *ImpactService) Impact(ctx context.Context, in ImpactInput) (Response[ImpactItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[ImpactItem]{}, err
	}

	kinds, err := normalizeImpactKinds(in.Kinds)
	if err != nil {
		return Response[ImpactItem]{}, err
	}
	depth := in.Depth
	if depth <= 0 {
		depth = ImpactDefaultDepth
	}
	if depth > ImpactMaxDepth {
		depth = ImpactMaxDepth
	}
	budget := in.Budget
	if budget <= 0 {
		budget = ImpactDefaultBudget
	}
	if budget > ImpactMaxBudget {
		budget = ImpactMaxBudget
	}
	view, verr := parseView(in.View)
	if verr != nil {
		return Response[ImpactItem]{}, verr.WithProject(op.Entry.ID)
	}
	var warnings []Warning

	target, err := normalizeImpactTarget(in.Target)
	if err != nil {
		return Response[ImpactItem]{}, err
	}

	paramsKey := impactParamsKey(target, kinds, depth, budget)

	type bfsResult struct {
		items      []ImpactItem
		truncated  bool
		rootFound  bool
		generation domain.Generation
		warn       []Warning
	}

	res, err := ReadTx(ctx, op.Store, func(tx *store.ReadTx) (bfsResult, error) {
		gen, gerr := tx.Generation()
		if gerr != nil {
			return bfsResult{}, gerr
		}
		rootKind, rootID, ok, terr := resolveImpactTarget(tx, target)
		if terr != nil {
			return bfsResult{}, terr
		}
		if !ok {
			return bfsResult{rootFound: false, generation: gen}, nil
		}
		items, truncated, berr := runImpactBFS(tx, rootKind, rootID, kinds, depth, budget)
		if berr != nil {
			return bfsResult{}, berr
		}
		var warn []Warning
		if view == domain.ViewEffective && rootKind == "symbol" {
			// упрощение (ADR-4/§20, тикет 14): перехватчики не хранятся как
			// ребро графа (intercepts не в store.ImpactEdgeKinds — таблица
			// dependency_edge их не собирает, см. doc-комментарий
			// internal/app/effective.go) — поэтому BFS их не проходит
			// транзитивно. Здесь добавляются ТОЛЬКО прямые перехватчики
			// КОРНЯ обхода, глубиной 1, а не рекурсивный обход через них.
			extra, ewarn, ierr := rootInterceptItems(tx, rootID)
			if ierr != nil {
				return bfsResult{}, ierr
			}
			items = append(items, extra...)
			warn = ewarn
		}
		return bfsResult{items: items, truncated: truncated, rootFound: true, generation: gen, warn: warn}, nil
	})
	if err != nil {
		return Response[ImpactItem]{}, fmt.Errorf("проект %s: find_impact: %w", op.Entry.ID, err)
	}
	if !res.rootFound {
		what := "символ"
		asked := target.SymbolUID
		if target.SymbolUID == "" {
			what = "объект метаданных"
			asked = target.ObjectType + "." + target.ObjectName
		}
		return Response[ImpactItem]{}, NotFoundError(what, asked, nil).WithProject(op.Entry.ID).WithGeneration(res.generation)
	}

	sortImpactItems(res.items)
	warnings = append(warnings, res.warn...)

	startIdx, decErr := DecodeCursor(in.Cursor, res.generation, paramsKey)
	if decErr != nil {
		return Response[ImpactItem]{}, decErr
	}
	offset := 0
	if startIdx != "" {
		offset, _ = strconv.Atoi(startIdx)
	}
	if offset < 0 || offset > len(res.items) {
		offset = len(res.items)
	}
	end := offset + impactPageSize
	if end > len(res.items) {
		end = len(res.items)
	}
	page := res.items[offset:end]
	if page == nil {
		page = []ImpactItem{}
	}

	resp := Response[ImpactItem]{
		Generation: res.generation,
		Items:      page,
		TotalCount: len(res.items),
		Warnings:   warnings,
	}
	if end < len(res.items) {
		resp.NextCursor = EncodeCursor(res.generation, strconv.Itoa(end), paramsKey)
	}
	if res.truncated {
		resp.Warnings = append(resp.Warnings, Warning{
			Code: "impact_truncated",
			Message: fmt.Sprintf(
				"обход остановлен по лимиту %d узлов; часть графа могла остаться неисследованной", budget),
			Hint: "увеличьте budget или сузьте kinds/depth",
		})
	}
	return resp, nil
}

// normalizeImpactTarget проверяет вход инструмента: ровно один из (символ
// uid) или (тип+имя объекта) должен быть задан. Нормализация имени
// (domain.NormalizeName) делается здесь, а не в cmd/mcp1c/idx_impact.go —
// транспорт не содержит бизнес-правил.
func normalizeImpactTarget(t ImpactTarget) (ImpactTarget, error) {
	symbolUID := strings.TrimSpace(t.SymbolUID)
	objectType := strings.TrimSpace(t.ObjectType)
	objectName := strings.TrimSpace(t.ObjectName)
	hasSymbol := symbolUID != ""
	hasObject := objectType != "" || objectName != ""
	switch {
	case hasSymbol && hasObject:
		return ImpactTarget{}, fmt.Errorf("find_impact: укажите ЛИБО symbolUID, ЛИБО objectType+objectName, не оба сразу")
	case hasSymbol:
		return ImpactTarget{SymbolUID: symbolUID}, nil
	case objectType != "" && objectName != "":
		return ImpactTarget{
			ObjectType: objectType,
			ObjectName: domain.NormalizeName(objectName),
			Component:  strings.TrimSpace(t.Component),
		}, nil
	default:
		return ImpactTarget{}, fmt.Errorf("find_impact: цель не задана — нужен symbolUID или objectType+objectName")
	}
}

// resolveImpactTarget находит корневой узел обхода. Символ адресуется через
// tx.NodeID напрямую: identity_key символа равен его uid (internal/index/
// identity.go:symbolIdentityKey, тот же приём, которым read_symbol.go
// комментирует SymbolByID) — заводить свой примитив под то же самое незачем.
// Объект метаданных — через tx.MetadataObjectsByName (readmeta.go, таск 12):
// она уже отдаёт все совпадения по виду+имени без фильтра по компоненту
// (raw view умышленно видит все слои); Component входа сужает выбор, когда
// он известен, иначе берётся первое совпадение (сортировка по component_id
// в самом запросе делает выбор детерминированным).
func resolveImpactTarget(tx *store.ReadTx, t ImpactTarget) (kind string, id int64, ok bool, err error) {
	if t.SymbolUID != "" {
		id, ok, err = tx.NodeID(t.SymbolUID)
		return "symbol", id, ok, err
	}
	rows, err := tx.MetadataObjectsByName(t.ObjectType, t.ObjectName)
	if err != nil {
		return "metadata_object", 0, false, err
	}
	for _, r := range rows {
		if t.Component == "" || r.ComponentID == t.Component {
			return "metadata_object", r.ID, true, nil
		}
	}
	return "metadata_object", 0, false, nil
}

// rootInterceptItems строит ImpactItem-ы для ПРЯМЫХ перехватчиков корня
// обхода (тикет 14) — глубина 1, edgeKind="intercepts". Этот вид ребра не
// входит в store.ImpactEdgeKinds: факт не хранится, вычисляется на чтении
// (см. doc-комментарий internal/app/effective.go) — поэтому runImpactBFS не
// может пройти через него транзитивно, только корень.
func rootInterceptItems(tx *store.ReadTx, rootID int64) ([]ImpactItem, []Warning, error) {
	row, ok, err := tx.SymbolByID(rootID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, nil
	}
	ics, blobWarn, err := effectiveIntercepts(tx, domain.ComponentID(row.ComponentID), row.ModulePath)
	if err != nil {
		return nil, nil, err
	}
	var mine []resolve.Intercept
	for _, ic := range ics {
		if ic.TargetNameNorm == row.NameNorm {
			mine = append(mine, ic)
		}
	}
	items := make([]ImpactItem, 0, len(mine))
	for _, ic := range mine {
		layer := string(ic.Layer.Component)
		step := ImpactPathStep{
			EdgeKind: "intercepts", Detail: string(ic.Kind), NodeKind: "symbol",
			Component: layer, Display: ic.InterceptorNameNorm, Confidence: ic.Confidence, Layer: layer,
		}
		items = append(items, ImpactItem{
			NodeKind: "symbol", Component: layer, Display: ic.InterceptorNameNorm, Depth: 1,
			EdgeKind: "intercepts", Detail: string(ic.Kind), Confidence: ic.Confidence, Layer: layer,
			Path: []ImpactPathStep{step},
		})
	}
	warn := append([]Warning{}, blobWarn...)
	for _, c := range resolve.DetectInsteadConflicts(mine) {
		warn = append(warn, interceptConflictWarning(c))
	}
	return items, warn, nil
}

// normalizeImpactKinds проверяет kinds против словаря store.ImpactEdgeKinds
// и возвращает store.ImpactEdgeKinds целиком, когда список пуст.
func normalizeImpactKinds(kinds []string) ([]string, error) {
	if len(kinds) == 0 {
		return nil, nil // nil -> store.IncomingEdges сама подставит все виды
	}
	known := make(map[string]bool, len(store.ImpactEdgeKinds))
	for _, k := range store.ImpactEdgeKinds {
		known[k] = true
	}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		k = strings.TrimSpace(k)
		if !known[k] {
			return nil, fmt.Errorf("find_impact: неизвестный вид ребра %q, допустимы: %s",
				k, strings.Join(store.ImpactEdgeKinds, ", "))
		}
		out = append(out, k)
	}
	return out, nil
}

// runImpactBFS — обратный BFS: от корня (rootKind, rootID) наружу по
// IncomingEdges, cycle-safe за счёт visitKey, с лимитом узлов budget и
// глубины depth. Возвращает найденные элементы (без корня) и признак
// усечения по budget (тикет 13: «цикл не вешает обход; лимит узлов
// соблюдается, усечение честно помечено»).
func runImpactBFS(tx *store.ReadTx, rootKind string, rootID int64, kinds []string, depth, budget int) ([]ImpactItem, bool, error) {
	type queueItem struct {
		kind, comp, display string
		id                  int64
		depth               int
		path                []ImpactPathStep
	}
	visited := map[visitKey]bool{{rootKind, rootID}: true}
	queue := []queueItem{{kind: rootKind, id: rootID, depth: 0}}
	var items []ImpactItem
	truncated := false

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= depth {
			continue
		}
		edges, err := tx.IncomingEdges(cur.kind, cur.id, kinds)
		if err != nil {
			return nil, false, err
		}
		// Детерминированный порядок обхода одного уровня: без сортировки
		// порядок строк SQL не гарантирован между вызовами (нет ORDER BY
		// по соображениям производительности на плотном графе), а BFS
		// обязан быть воспроизводим для стабильной пагинации курсором.
		sort.SliceStable(edges, func(i, j int) bool {
			if edges[i].FromNodeKind != edges[j].FromNodeKind {
				return edges[i].FromNodeKind < edges[j].FromNodeKind
			}
			return edges[i].FromNodeID < edges[j].FromNodeID
		})
		for _, e := range edges {
			ck := visitKey{e.FromNodeKind, e.FromNodeID}
			if visited[ck] {
				continue
			}
			if len(visited)-1 >= budget {
				truncated = true
				continue
			}
			visited[ck] = true
			step := ImpactPathStep{
				EdgeKind:   e.Kind,
				Detail:     e.Detail,
				NodeKind:   e.FromNodeKind,
				Component:  e.FromComponent,
				Display:    e.FromDisplay,
				Resolution: e.Resolution,
				Confidence: domain.Confidence(e.Confidence),
				Layer:      e.Layer,
			}
			path := make([]ImpactPathStep, len(cur.path)+1)
			copy(path, cur.path)
			path[len(cur.path)] = step
			items = append(items, ImpactItem{
				NodeKind:   e.FromNodeKind,
				Component:  e.FromComponent,
				Display:    e.FromDisplay,
				Depth:      cur.depth + 1,
				EdgeKind:   e.Kind,
				Detail:     e.Detail,
				Resolution: e.Resolution,
				Confidence: domain.Confidence(e.Confidence),
				Layer:      e.Layer,
				Path:       path,
			})
			queue = append(queue, queueItem{
				kind: e.FromNodeKind, id: e.FromNodeID, depth: cur.depth + 1, path: path,
			})
		}
		if len(visited)-1 >= budget && len(queue) > 0 {
			truncated = true
			break
		}
	}
	return items, truncated, nil
}

// sortImpactItems — ранжирование (тикет 13): ближе -> выше (Depth), точный
// факт выше эвристики (Confidence по убыванию), затем стабильный
// детерминированный tie-break по отображаемому имени.
func sortImpactItems(items []ImpactItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Depth != items[j].Depth {
			return items[i].Depth < items[j].Depth
		}
		if items[i].Confidence != items[j].Confidence {
			return items[i].Confidence > items[j].Confidence
		}
		if items[i].NodeKind != items[j].NodeKind {
			return items[i].NodeKind < items[j].NodeKind
		}
		return items[i].Display < items[j].Display
	})
}

func impactParamsKey(t ImpactTarget, kinds []string, depth, budget int) string {
	sorted := append([]string(nil), kinds...)
	sort.Strings(sorted)
	return fmt.Sprintf("%s|%s|%s|%s|%s|%d|%d",
		t.SymbolUID, t.ObjectType, t.ObjectName, t.Component, strings.Join(sorted, ","), depth, budget)
}
