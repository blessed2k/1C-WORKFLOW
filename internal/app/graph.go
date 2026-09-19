// GraphService: find_references и trace_call_graph. Делит с
// symbol.go резолвер проекта, componentFromInput, clampLimit, resource URI.
package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

const (
	defaultReferenceLimit = 50
	maxReferenceLimit     = 200

	// Бюджеты trace_call_graph: жёсткий потолок глубины и число узлов за один
	// вызов — bounded traversal (архитектура §19.1: «лимиты глубины/узлов,
	// защита от циклов»). Значения — упрощение: policy-константы этого
	// тикета, не продуктовое решение мейнтейнера (по аналогии с tunables §18.5).
	defaultCallGraphDepth = 2
	maxCallGraphDepth     = 8
	maxCallGraphNodes     = 500
	maxEdgesPerNode       = 200

	defaultCallGraphLimit = 100
	maxCallGraphLimit     = 500
)

// CandidateItem: один кандидат ambiguous-разрешения ссылки.
type CandidateItem struct {
	TargetUID string `json:"targetUid,omitempty"`
	Rank      int    `json:"rank"`
	Reason    string `json:"reason,omitempty"`
}

// ReferenceItem: одна ссылка со своим состоянием разрешения.
type ReferenceItem struct {
	Kind          string          `json:"kind"`
	Span          domain.Span     `json:"span"`
	Resolution    string          `json:"resolution"`
	Confidence    float64         `json:"confidence"`
	TargetClass   string          `json:"targetClass,omitempty"`
	TargetUID     string          `json:"targetUid,omitempty"`
	PlatformKey   string          `json:"platformKey,omitempty"`
	FromSymbolUID string          `json:"fromSymbolUid,omitempty"`
	Candidates    []CandidateItem `json:"candidates,omitempty"`
}

// ReferenceGroup: ссылки одного модуля (группировка по модулю).
type ReferenceGroup struct {
	Module     string             `json:"module"`
	Component  domain.ComponentID `json:"component"`
	References []ReferenceItem    `json:"references"`
}

// FindReferencesInput — вход find_references. View=effective добавляет
// предупреждение о перехватчиках ЦЕЛЕВОГО символа, но не по
// каждому символу в выдаче: разбор модулей расширений на каждую строку
// списка ссылок стоил бы дороже самого поиска (упрощение, см.
// docs/tools-index.md).
type FindReferencesInput struct {
	UID       string
	Kinds     []string
	Component string
	View      string
	Limit     int
	Cursor    string
}

// CallGraphStep — одно ребро на пути от корня trace_call_graph до узла:
// From — вызывающий, To — вызываемый (если разрешён), Kind — вид ребра
// (local/common-module/global-common/manager/platform/dynamic — §19.1),
// провенанс через Module+Span места вызова.
type CallGraphStep struct {
	FromUID     string      `json:"fromUid"`
	FromName    string      `json:"fromName,omitempty"`
	ToUID       string      `json:"toUid,omitempty"`
	ToName      string      `json:"toName,omitempty"`
	Kind        string      `json:"kind"`
	Resolution  string      `json:"resolution"`
	Confidence  float64     `json:"confidence"`
	PlatformKey string      `json:"platformKey,omitempty"`
	Module      string      `json:"module,omitempty"`
	Span        domain.Span `json:"span,omitempty"`
	Ambiguous   bool        `json:"ambiguous,omitempty"`
}

// CallGraphNodeItem — один узел, найденный BFS: символ, либо платформенный/
// ambiguous/dynamic/unresolved лист (§19.1: «platform-вызов не тупик»), со
// своим путём от стартового символа.
type CallGraphNodeItem struct {
	UID   string          `json:"uid,omitempty"`
	Name  string          `json:"name,omitempty"`
	Kind  string          `json:"kind"` // symbol | platform | ambiguous | dynamic | unresolved
	Depth int             `json:"depth"`
	Path  []CallGraphStep `json:"path"`
}

// TraceCallGraphInput — вход trace_call_graph. View=effective добавляет
// предупреждение о перехватчиках КОРНЕВОГО символа, но обход
// (BFS) сам по себе перехватчики не пересекает, см. doc-комментарий
// FindReferencesInput.View.
type TraceCallGraphInput struct {
	UID             string
	Direction       string // "callers" | "callees"
	Depth           int
	ExpandAmbiguous bool
	View            string
	Limit           int
	Cursor          string
}

// GraphService — сервис за find_references и trace_call_graph.
type GraphService struct{ projects *Projects }

// NewGraphService строит сервис поверх общего резолвера проектов.
func NewGraphService(p *Projects) *GraphService { return &GraphService{projects: p} }

// FindReferences отдаёт все ссылки на символ, сгруппированные по модулю, с
// resolution/confidence и кандидатами ambiguous. При усечении:
// warning + resource link на полный список (onec://references/...).
func (g *GraphService) FindReferences(ctx context.Context, in FindReferencesInput) (Response[ReferenceGroup], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[ReferenceGroup]{}, err
	}
	uid := strings.TrimSpace(in.UID)
	if uid == "" {
		return Response[ReferenceGroup]{}, NewError(CodeNotFound, "find_references требует uid",
			"передайте uid символа из find_symbol/get_symbol").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[ReferenceGroup]{}, cerr
	}
	view, verr := parseView(in.View)
	if verr != nil {
		return Response[ReferenceGroup]{}, verr.WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultReferenceLimit, maxReferenceLimit)
	paramsKey := fmt.Sprintf("uid=%s&kinds=%s&c=%s&l=%d", uid, strings.Join(in.Kinds, ","), componentID, limit)

	type txResult struct {
		refs []store.ReferenceRow
		cand map[int64][]store.ReferenceCandidateRow
		gen  domain.Generation
		warn []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		key, derr := DecodeCursor(in.Cursor, gen, paramsKey)
		if derr != nil {
			return out, derr
		}
		var afterID int64
		if key != "" {
			afterID, _ = strconv.ParseInt(key, 10, 64)
		}

		targetID, ok, nerr := tx.NodeID(uid)
		if nerr != nil {
			return out, nerr
		}
		if !ok {
			return out, NotFoundError("символ", uid, nil).WithProject(op.Entry.ID).WithGeneration(gen)
		}

		if view == domain.ViewEffective {
			if warn := targetInterceptWarnings(tx, targetID); len(warn) > 0 {
				out.warn = warn
			}
		}

		refs, ferr := tx.FindReferences(store.ReferenceSearch{
			TargetSymbolID: targetID, Kinds: in.Kinds, ComponentID: componentID, AfterID: afterID, Limit: limit + 1,
		})
		if ferr != nil {
			return out, ferr
		}
		out.refs = refs
		out.cand = map[int64][]store.ReferenceCandidateRow{}
		for _, r := range refs {
			if r.Resolution == "ambiguous" {
				cs, cerr := tx.ReferenceCandidates(r.ID)
				if cerr != nil {
					return out, cerr
				}
				out.cand[r.ID] = cs
			}
		}
		return out, nil
	})
	if err != nil {
		return Response[ReferenceGroup]{}, err
	}

	hasMore := len(res.refs) > limit
	if hasMore {
		res.refs = res.refs[:limit]
	}

	groups := map[string]*ReferenceGroup{}
	var order []string
	for _, r := range res.refs {
		key := r.ComponentID + "\x00" + r.ModulePath
		grp, ok := groups[key]
		if !ok {
			grp = &ReferenceGroup{Module: r.ModulePath, Component: domain.ComponentID(r.ComponentID)}
			groups[key] = grp
			order = append(order, key)
		}
		item := ReferenceItem{
			Kind: r.Kind, Span: r.Span, Resolution: r.Resolution, Confidence: r.Confidence,
			TargetClass: r.TargetClass, PlatformKey: r.PlatformKey,
		}
		if r.TargetSymbolID != 0 {
			item.TargetUID = uid
		}
		for _, c := range res.cand[r.ID] {
			item.Candidates = append(item.Candidates, CandidateItem{Rank: c.Rank, Reason: c.Reason})
		}
		grp.References = append(grp.References, item)
	}
	items := make([]ReferenceGroup, 0, len(order))
	total := 0
	for _, key := range order {
		items = append(items, *groups[key])
		total += len(groups[key].References)
	}

	resp := Response[ReferenceGroup]{Generation: res.gen, Items: items, TotalCount: total, Warnings: res.warn}
	if hasMore {
		last := res.refs[len(res.refs)-1]
		resp.NextCursor = EncodeCursor(res.gen, strconv.FormatInt(last.ID, 10), paramsKey)
		resp.Warnings = append(resp.Warnings, Warning{
			Code:    "truncated",
			Message: "показана не вся выдача find_references",
			Hint:    "запросите следующую страницу через nextCursor либо заберите полный список по " + referencesResourceURI(op.Entry.ID, uid, res.gen),
		})
	}
	return withSnapshot(resp, snap), nil
}

// ResourceReferences отдаёт ПОЛНЫЙ список ссылок на символ, без пагинации
// (в пределах maxCallGraphNodes*4 как защитного потолка) — для
// onec://references/{project}/{uid}?gen=... (§21).
func (g *GraphService) ResourceReferences(ctx context.Context, projectArg, uid, genArg string) (Response[ReferenceGroup], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[ReferenceGroup]{}, err
	}
	if projectArg != "" && string(op.Entry.ID) != projectArg {
		return Response[ReferenceGroup]{}, NewError(CodeNotFound, fmt.Sprintf("проект %q сейчас не активен", projectArg),
			"активный проект: "+string(op.Entry.ID)).WithProject(op.Entry.ID)
	}
	uri := fmt.Sprintf("onec://references/%s/%s", projectArg, uid)
	const fullLimit = 5000
	type txResult struct {
		refs []store.ReferenceRow
		cand map[int64][]store.ReferenceCandidateRow
		gen  domain.Generation
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		if genArg != "" && genArg != string(gen) {
			return out, ResourceExpiredError(uri, fmt.Sprintf("построен для generation %s, текущее — %s", genArg, gen)).
				WithProject(op.Entry.ID).WithGeneration(gen)
		}
		targetID, ok, nerr := tx.NodeID(uid)
		if nerr != nil {
			return out, nerr
		}
		if !ok {
			return out, ResourceExpiredError(uri, "символ больше не найден в индексе").WithProject(op.Entry.ID).WithGeneration(gen)
		}
		refs, ferr := tx.FindReferences(store.ReferenceSearch{TargetSymbolID: targetID, Limit: fullLimit})
		if ferr != nil {
			return out, ferr
		}
		out.refs = refs
		out.cand = map[int64][]store.ReferenceCandidateRow{}
		for _, r := range refs {
			if r.Resolution == "ambiguous" {
				cs, cerr := tx.ReferenceCandidates(r.ID)
				if cerr != nil {
					return out, cerr
				}
				out.cand[r.ID] = cs
			}
		}
		return out, nil
	})
	if err != nil {
		return Response[ReferenceGroup]{}, err
	}

	groups := map[string]*ReferenceGroup{}
	var order []string
	for _, r := range res.refs {
		key := r.ComponentID + "\x00" + r.ModulePath
		grp, ok := groups[key]
		if !ok {
			grp = &ReferenceGroup{Module: r.ModulePath, Component: domain.ComponentID(r.ComponentID)}
			groups[key] = grp
			order = append(order, key)
		}
		item := ReferenceItem{Kind: r.Kind, Span: r.Span, Resolution: r.Resolution, Confidence: r.Confidence,
			TargetClass: r.TargetClass, PlatformKey: r.PlatformKey}
		if r.TargetSymbolID != 0 {
			item.TargetUID = uid
		}
		for _, c := range res.cand[r.ID] {
			item.Candidates = append(item.Candidates, CandidateItem{Rank: c.Rank, Reason: c.Reason})
		}
		grp.References = append(grp.References, item)
	}
	items := make([]ReferenceGroup, 0, len(order))
	total := 0
	for _, key := range order {
		items = append(items, *groups[key])
		total += len(groups[key].References)
	}
	return withSnapshot(Response[ReferenceGroup]{Generation: res.gen, Items: items, TotalCount: total}, snap), nil
}

// bfsNode — рабочее состояние одного фронта BFS.
type bfsNode struct {
	id   int64
	uid  string
	name string
	path []CallGraphStep
}

func copyPath(p []CallGraphStep) []CallGraphStep {
	out := make([]CallGraphStep, len(p))
	copy(out, p)
	return out
}

// leafKind сообщает вид узла-листа, в который упёрлось ребро без разрешённого
// символа-цели: platform — отдельный класс, не тупик (§19.1); иначе —
// resolution ребра как есть (ambiguous/dynamic/unresolved).
func leafKind(e store.CallEdgeRow) string {
	if e.Kind == "platform" {
		return "platform"
	}
	return e.Resolution
}

// TraceCallGraph — BFS по callers/callees на глубину depth, cycle-safe,
// с объяснением пути на каждый узел. ambiguous по умолчанию не
// расширяется; expandAmbiguous=true продолжает BFS по кандидатам,
// помечая шаг как ambiguous-переход.
func (g *GraphService) TraceCallGraph(ctx context.Context, in TraceCallGraphInput) (Response[CallGraphNodeItem], error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return Response[CallGraphNodeItem]{}, err
	}
	uid := strings.TrimSpace(in.UID)
	if uid == "" {
		return Response[CallGraphNodeItem]{}, NewError(CodeNotFound, "trace_call_graph требует uid",
			"передайте uid символа из find_symbol/get_symbol").WithProject(op.Entry.ID)
	}
	direction, derr := normalizeCallDirection(in.Direction)
	if derr != nil {
		return Response[CallGraphNodeItem]{}, derr.WithProject(op.Entry.ID)
	}
	depth := in.Depth
	if depth <= 0 {
		depth = defaultCallGraphDepth
	}
	if depth > maxCallGraphDepth {
		depth = maxCallGraphDepth
	}
	view, verr := parseView(in.View)
	if verr != nil {
		return Response[CallGraphNodeItem]{}, verr.WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultCallGraphLimit, maxCallGraphLimit)
	paramsKey := fmt.Sprintf("uid=%s&dir=%s&depth=%d&expand=%v&l=%d", uid, direction, depth, in.ExpandAmbiguous, limit)

	type txResult struct {
		nodes          []CallGraphNodeItem
		gen            domain.Generation
		budgetExceeded bool
		afterIdx       int
		warn           []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		key, derr := DecodeCursor(in.Cursor, gen, paramsKey)
		if derr != nil {
			return out, derr
		}
		var afterIdx int
		if key != "" {
			afterIdx, _ = strconv.Atoi(key)
		}

		rootID, ok, nerr := tx.NodeID(uid)
		if nerr != nil {
			return out, nerr
		}
		if !ok {
			return out, NotFoundError("символ", uid, nil).WithProject(op.Entry.ID).WithGeneration(gen)
		}
		root, sok, serr := tx.SymbolByID(rootID)
		if serr != nil {
			return out, serr
		}
		if !sok {
			return out, NotFoundError("символ", uid, nil).WithProject(op.Entry.ID).WithGeneration(gen)
		}
		if view == domain.ViewEffective {
			out.warn = targetInterceptWarnings(tx, rootID)
		}

		visited := map[int64]bool{rootID: true}
		frontier := []bfsNode{{id: rootID, uid: root.UID, name: root.NameDisplay}}
		var nodes []CallGraphNodeItem

		for level := 1; level <= depth && len(visited) < maxCallGraphNodes; level++ {
			var next []bfsNode
			for _, cur := range frontier {
				var edges []store.CallEdgeRow
				var eerr error
				if direction == "callees" {
					edges, eerr = tx.CallEdgesFrom(cur.id)
				} else {
					edges, eerr = tx.CallEdgesTo(cur.id)
				}
				if eerr != nil {
					return out, eerr
				}
				if len(edges) > maxEdgesPerNode {
					edges = edges[:maxEdgesPerNode]
				}
				for _, e := range edges {
					if len(visited) >= maxCallGraphNodes {
						out.budgetExceeded = true
						break
					}
					step := CallGraphStep{Kind: e.Kind, Resolution: e.Resolution, Confidence: e.Confidence,
						Ambiguous: e.Resolution == "ambiguous"}
					if ref, rok, rerr := tx.ReferenceByID(e.RefID); rerr == nil && rok {
						step.Module, step.Span, step.PlatformKey = ref.ModulePath, ref.Span, ref.PlatformKey
					}
					var nextID int64
					if direction == "callees" {
						step.FromUID, step.FromName, nextID = cur.uid, cur.name, e.CalleeID
					} else {
						step.ToUID, step.ToName, nextID = cur.uid, cur.name, e.CallerID
					}
					if nextID == 0 {
						// Нет разрешённого символа-цели: platform/ambiguous/
						// dynamic/unresolved — лист, а не тупик (§19.1).
						nodes = append(nodes, CallGraphNodeItem{Kind: leafKind(e), Depth: level, Path: append(copyPath(cur.path), step)})
						if step.Ambiguous && in.ExpandAmbiguous {
							cands, cerr := tx.ReferenceCandidates(e.RefID)
							if cerr != nil {
								return out, cerr
							}
							for _, c := range cands {
								if visited[c.TargetNodeID] {
									continue
								}
								csym, csok, cserr := tx.SymbolByID(c.TargetNodeID)
								if cserr != nil {
									return out, cserr
								}
								if !csok {
									continue
								}
								visited[c.TargetNodeID] = true
								cstep := step
								if direction == "callees" {
									cstep.ToUID, cstep.ToName = csym.UID, csym.NameDisplay
								} else {
									cstep.FromUID, cstep.FromName = csym.UID, csym.NameDisplay
								}
								path := append(copyPath(cur.path), cstep)
								nodes = append(nodes, CallGraphNodeItem{UID: csym.UID, Name: csym.NameDisplay, Kind: "symbol", Depth: level, Path: path})
								next = append(next, bfsNode{id: c.TargetNodeID, uid: csym.UID, name: csym.NameDisplay, path: path})
							}
						}
						continue
					}
					if visited[nextID] {
						continue // cycle-safe: узел уже посещён этим обходом
					}
					visited[nextID] = true
					nsym, nsok, nserr := tx.SymbolByID(nextID)
					if nserr != nil {
						return out, nserr
					}
					if !nsok {
						continue
					}
					if direction == "callees" {
						step.ToUID, step.ToName = nsym.UID, nsym.NameDisplay
					} else {
						step.FromUID, step.FromName = nsym.UID, nsym.NameDisplay
					}
					path := append(copyPath(cur.path), step)
					nodes = append(nodes, CallGraphNodeItem{UID: nsym.UID, Name: nsym.NameDisplay, Kind: "symbol", Depth: level, Path: path})
					next = append(next, bfsNode{id: nextID, uid: nsym.UID, name: nsym.NameDisplay, path: path})
				}
				if out.budgetExceeded {
					break
				}
			}
			frontier = next
			if len(frontier) == 0 || out.budgetExceeded {
				break
			}
		}

		if afterIdx > len(nodes) {
			afterIdx = len(nodes)
		}
		out.afterIdx = afterIdx
		out.nodes = nodes[afterIdx:]
		return out, nil
	})
	if err != nil {
		return Response[CallGraphNodeItem]{}, err
	}

	hasMore := len(res.nodes) > limit
	if hasMore {
		res.nodes = res.nodes[:limit]
	}
	resp := Response[CallGraphNodeItem]{Generation: res.gen, Items: res.nodes, TotalCount: len(res.nodes), Warnings: res.warn}
	if hasMore {
		// Курсор несёт НАКОПЛЕННОЕ смещение (afterIdx текущей страницы + limit),
		// не константный limit — иначе каждая следующая страница декодирует тот
		// же offset и отдаёт те же узлы повторно (регрессия, найдена ревью).
		resp.NextCursor = EncodeCursor(res.gen, strconv.Itoa(res.afterIdx+limit), paramsKey)
	}
	if res.budgetExceeded {
		resp.Warnings = append(resp.Warnings, Warning{
			Code:    "call_graph_budget_exceeded",
			Message: fmt.Sprintf("обход остановлен на потолке %d узлов", maxCallGraphNodes),
			Hint:    "сузьте depth или начните обход от более конкретного символа",
		})
	}
	return withSnapshot(resp, snap), nil
}
