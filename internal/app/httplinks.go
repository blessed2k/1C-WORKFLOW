package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Файл — HTTP-связи между базами (веха В2, решение D10, ADR-039). Факты
// каждого проекта (http_call, http_endpoint) читаются из его индекса одной
// read-транзакцией, вызов приписывается объекту-владельцу обходом графа
// вызовов (resolve.AttributeSymbolFact, те же правила D6/D7, что у записи в
// регистр), затем resolve.StitchHTTPCall сшивает вызовы с сервисами всех
// переданных проектов по пути и маппингу хостов workspace. Индексы баз
// остаются независимыми файлами: кросс-проектный join живёт здесь, при
// чтении, и правка маппинга хостов не требует переиндексации.

// EdgeHTTPCall — вид кросс-базового ребра. В object_data_edge его нет: конец
// ребра лежит в индексе другой базы (ADR-039).
const EdgeHTTPCall = "http-call"

// HTTPNodeRef — узел HTTP-связи: объект метаданных конкретного проекта. Id —
// id строки metadata_object ЭТОГО проекта: у разных баз пространства id
// разные, поэтому узел адресуется парой проект+id.
type HTTPNodeRef struct {
	Project domain.ProjectID `json:"project"`
	ID      int64            `json:"id"`
	Type    string           `json:"type"`
	Name    string           `json:"name"`
}

// HTTPLinkEndpoint — метод сервиса, с которым сшит вызов.
type HTTPLinkEndpoint struct {
	Template     string `json:"template"`
	TemplateName string `json:"templateName,omitempty"`
	Method       string `json:"method,omitempty"`
	HTTPMethod   string `json:"httpMethod,omitempty"`
	Handler      string `json:"handler,omitempty"`
}

// HTTPCallSite — место вызова в коде: «почему ребро существует».
type HTTPCallSite struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Symbol   string `json:"symbol,omitempty"`
	Verb     string `json:"verb,omitempty"`
	Host     string `json:"host,omitempty"`
	Path     string `json:"path,omitempty"`
	PathKind string `json:"pathKind"`
	// Attributed: false — цепочка вызовов до объекта не дошла, и концом
	// ребра стал сам модуль вызова (например, общий модуль).
	Attributed bool `json:"attributed"`
}

// HTTPLink — HTTP-связь объекта одной базы с сервисом другой (или с внешним
// адресатом). Внешняя связь: To пуст, External=true, ExternalHost — хост
// вызова (пусто, если он вычисляется).
type HTTPLink struct {
	ID           string             `json:"id"`
	Kind         string             `json:"kind"`
	From         HTTPNodeRef        `json:"from"`
	To           *HTTPNodeRef       `json:"to,omitempty"`
	External     bool               `json:"external,omitempty"`
	ExternalHost string             `json:"externalHost,omitempty"`
	Reason       string             `json:"reason"`
	Confidence   float64            `json:"confidence"`
	Endpoints    []HTTPLinkEndpoint `json:"endpoints,omitempty"`
	Calls        []HTTPCallSite     `json:"calls"`
}

// HTTPBadge — бейдж узла по HTTP: has-dynamic-http (адрес вызова не
// выводится, ребра нет, D7).
type HTTPBadge struct {
	Node  HTTPNodeRef    `json:"node"`
	Badge string         `json:"badge"`
	Count int            `json:"count"`
	Calls []HTTPCallSite `json:"calls,omitempty"`
}

// CrossLinksItem — HTTP-связи набора проектов.
type CrossLinksItem struct {
	Projects []domain.ProjectID `json:"projects"`
	Links    []HTTPLink         `json:"links"`
	Badges   []HTTPBadge        `json:"badges"`
}

// ProjectHTTPFacts — факты HTTP одного проекта, уже приписанные владельцам.
// Строится ReadHTTPFacts, сшивается StitchCrossLinks.
type ProjectHTTPFacts struct {
	Project   domain.ProjectID
	Calls     []attributedHTTPCall
	Endpoints []resolve.HTTPEndpointFact
	// services: объект-сервис по id, для имени конца ребра.
	services   map[int64]HTTPNodeRef
	Warnings   []Warning
	Generation domain.Generation
}

type attributedHTTPCall struct {
	fact   resolve.HTTPCallFact
	site   HTTPCallSite
	owners []httpOwner
}

type httpOwner struct {
	node       HTTPNodeRef
	confidence float64
}

// httpSiteLimit — сколько мест вызова показывает одна связь или бейдж: у
// общего модуля обмена вызовов бывают десятки, а ответ агенту ограничен.
const httpSiteLimit = 10

// ReadHTTPFacts читает факты HTTP проекта op одной read-транзакцией и
// приписывает вызовы владельцам.
func ReadHTTPFacts(ctx context.Context, op *openProject) (ProjectHTTPFacts, Snapshot, error) {
	tunables := currentGraphTunables()
	return ReadSnapshot(ctx, op, func(tx *store.ReadTx) (ProjectHTTPFacts, error) {
		out := ProjectHTTPFacts{Project: op.Entry.ID, services: map[int64]HTTPNodeRef{}}
		objects := newObjectNames(tx, op.Entry.ID)
		gen, err := tx.Generation()
		if err != nil {
			return out, err
		}
		out.Generation = gen
		eps, err := tx.HTTPEndpoints()
		if err != nil {
			return out, fmt.Errorf("методы HTTP-сервисов: %w", err)
		}
		for _, e := range eps {
			if e.ServiceID == 0 {
				continue // XML без объекта-сервиса: концу ребра нечем быть
			}
			out.Endpoints = append(out.Endpoints, resolve.HTTPEndpointFact{
				Project: op.Entry.ID, ID: e.ID, ServiceID: e.ServiceID, RootURL: e.RootURL,
				Template: e.Template, TemplateName: e.TemplateName, MethodName: e.MethodName,
				HTTPMethod: e.HTTPMethod, Handler: e.Handler,
			})
			out.services[e.ServiceID] = HTTPNodeRef{Project: op.Entry.ID, ID: e.ServiceID,
				Type: "HTTPService", Name: e.ServiceDisplay}
		}
		calls, err := tx.HTTPCalls()
		if err != nil {
			return out, fmt.Errorf("HTTP-вызовы: %w", err)
		}
		graph := newReadEdgeGraph(tx)
		var truncated, orphan int
		for _, c := range calls {
			ac := attributedHTTPCall{
				fact: resolve.HTTPCallFact{Verb: c.Verb, Host: c.Host, HostStatic: c.HostStatic,
					Path: c.Path, PathKind: c.PathKind},
				site: HTTPCallSite{File: c.RelPath, Line: c.Span.StartLine, Symbol: c.SymbolName,
					Verb: c.Verb, Host: c.Host, Path: c.Path, PathKind: c.PathKind},
			}
			attrs, cut := resolve.AttributeSymbolFact(resolve.SymbolFact{
				SymbolID: c.SymbolID, FileID: c.FileID, Span: c.Span, Confidence: c.Confidence,
			}, graph, tunables)
			if err := graph.Err(); err != nil {
				return out, err
			}
			if cut {
				truncated++
			}
			for _, a := range attrs {
				node, ok, err := objects.ref(a.ObjectID)
				if err != nil {
					return out, err
				}
				if ok {
					ac.owners = append(ac.owners, httpOwner{node: node, confidence: a.Confidence})
				}
			}
			if len(ac.owners) > 0 {
				ac.site.Attributed = true
			} else if c.ModuleOwnerID != 0 {
				// Цепочка до объекта не дошла: концом ребра становится модуль
				// вызова (общий модуль, модуль сервиса), а не пустота.
				node, ok, err := objects.ref(c.ModuleOwnerID)
				if err != nil {
					return out, err
				}
				if ok {
					ac.owners = append(ac.owners, httpOwner{node: node, confidence: c.Confidence})
				}
			}
			if len(ac.owners) == 0 {
				orphan++
				continue
			}
			out.Calls = append(out.Calls, ac)
		}
		if truncated > 0 {
			out.Warnings = append(out.Warnings, Warning{Code: "http_attribution_truncated",
				Message: fmt.Sprintf("проект %s: у %d HTTP-вызовов обход графа вызовов упёрся в потолок, часть владельцев могла не найтись", op.Entry.ID, truncated)})
		}
		if orphan > 0 {
			out.Warnings = append(out.Warnings, Warning{Code: "http_call_without_owner",
				Message: fmt.Sprintf("проект %s: %d HTTP-вызовов вне модуля объекта, приписать их некому", op.Entry.ID, orphan)})
		}
		return out, nil
	})
}

// StitchCrossLinks сшивает факты нескольких проектов в связи. Связь одна на
// пару «владелец вызова, сервис» (или «владелец, внешний хост»): несколько
// вызовов одного объекта к одному сервису дают одну связь с перечнем мест и
// методов, достоверность — максимум по вызовам.
func StitchCrossLinks(hosts workspace.HTTPHosts, facts []ProjectHTTPFacts) CrossLinksItem {
	endpoints := make(map[domain.ProjectID][]resolve.HTTPEndpointFact, len(facts))
	services := map[domain.ProjectID]map[int64]HTTPNodeRef{}
	item := CrossLinksItem{Links: []HTTPLink{}, Badges: []HTTPBadge{}}
	for _, f := range facts {
		endpoints[f.Project] = f.Endpoints
		services[f.Project] = f.services
		item.Projects = append(item.Projects, f.Project)
	}
	links := map[string]*HTTPLink{}
	badges := map[string]*HTTPBadge{}
	for _, f := range facts {
		for _, c := range f.Calls {
			st := resolve.StitchHTTPCall(c.fact, endpoints, hosts.Lookup)
			for _, owner := range c.owners {
				switch st.Kind {
				case resolve.HTTPDynamic:
					key := fmt.Sprintf("%s:%d", owner.node.Project, owner.node.ID)
					b := badges[key]
					if b == nil {
						b = &HTTPBadge{Node: owner.node, Badge: resolve.BadgeHasDynamicHTTP}
						badges[key] = b
					}
					b.Count++
					if len(b.Calls) < httpSiteLimit {
						b.Calls = append(b.Calls, c.site)
					}
				case resolve.HTTPExternal:
					id := fmt.Sprintf("http-ext:%s:%d:%s", owner.node.Project, owner.node.ID, st.Host)
					l := links[id]
					if l == nil {
						l = &HTTPLink{ID: id, Kind: EdgeHTTPCall, From: owner.node, External: true,
							ExternalHost: st.Host, Reason: st.Reason}
						links[id] = l
					}
					addCallSite(l, c.site, 0)
				case resolve.HTTPStitched:
					byService := map[domain.ProjectID]map[int64][]resolve.HTTPEndpointFact{}
					for _, ep := range st.Endpoints {
						if byService[ep.Project] == nil {
							byService[ep.Project] = map[int64][]resolve.HTTPEndpointFact{}
						}
						byService[ep.Project][ep.ServiceID] = append(byService[ep.Project][ep.ServiceID], ep)
					}
					for project, svcs := range byService {
						for serviceID, eps := range svcs {
							to := services[project][serviceID]
							id := fmt.Sprintf("http:%s:%d:%s:%d", owner.node.Project, owner.node.ID, project, serviceID)
							l := links[id]
							if l == nil {
								l = &HTTPLink{ID: id, Kind: EdgeHTTPCall, From: owner.node, To: &to, Reason: st.Reason}
								links[id] = l
							}
							conf := st.Confidence
							if owner.confidence < conf {
								conf = owner.confidence
							}
							addCallSite(l, c.site, conf)
							for _, ep := range eps {
								addEndpoint(l, HTTPLinkEndpoint{Template: ep.Template, TemplateName: ep.TemplateName,
									Method: ep.MethodName, HTTPMethod: ep.HTTPMethod, Handler: ep.Handler})
							}
							if st.Reason == resolve.ReasonVerbNotAllowed {
								l.Reason = st.Reason
							}
						}
					}
				}
			}
		}
	}
	for _, l := range links {
		item.Links = append(item.Links, *l)
	}
	sort.Slice(item.Links, func(i, j int) bool { return item.Links[i].ID < item.Links[j].ID })
	for _, b := range badges {
		item.Badges = append(item.Badges, *b)
	}
	sort.Slice(item.Badges, func(i, j int) bool {
		a, b := item.Badges[i].Node, item.Badges[j].Node
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.ID < b.ID
	})
	return item
}

func addCallSite(l *HTTPLink, site HTTPCallSite, conf float64) {
	if conf > l.Confidence {
		l.Confidence = conf
	}
	for _, s := range l.Calls {
		if s == site {
			return
		}
	}
	if len(l.Calls) < httpSiteLimit {
		l.Calls = append(l.Calls, site)
	}
}

func addEndpoint(l *HTTPLink, ep HTTPLinkEndpoint) {
	for _, e := range l.Endpoints {
		if e == ep {
			return
		}
	}
	l.Endpoints = append(l.Endpoints, ep)
}

// FilterCrossLinks оставляет связи и бейджи, касающиеся узлов nodes одного
// проекта (object_graph по одному объекту).
func FilterCrossLinks(item CrossLinksItem, project domain.ProjectID, nodes map[int64]bool) CrossLinksItem {
	out := CrossLinksItem{Projects: item.Projects, Links: []HTTPLink{}, Badges: []HTTPBadge{}}
	touches := func(n *HTTPNodeRef) bool { return n != nil && n.Project == project && nodes[n.ID] }
	for _, l := range item.Links {
		if touches(&l.From) || touches(l.To) {
			out.Links = append(out.Links, l)
		}
	}
	for _, b := range item.Badges {
		if touches(&b.Node) {
			out.Badges = append(out.Badges, b)
		}
	}
	return out
}

// objectNames — узлы-объекты по id с кэшем на транзакцию.
type objectNames struct {
	tx      *store.ReadTx
	project domain.ProjectID
	cache   map[int64]*HTTPNodeRef
}

func newObjectNames(tx *store.ReadTx, project domain.ProjectID) *objectNames {
	return &objectNames{tx: tx, project: project, cache: map[int64]*HTTPNodeRef{}}
}

func (o *objectNames) ref(id int64) (HTTPNodeRef, bool, error) {
	if r, ok := o.cache[id]; ok {
		if r == nil {
			return HTTPNodeRef{}, false, nil
		}
		return *r, true, nil
	}
	row, ok, err := o.tx.MetadataObjectByID(id)
	if err != nil {
		return HTTPNodeRef{}, false, fmt.Errorf("объект %d: %w", id, err)
	}
	if !ok {
		o.cache[id] = nil
		return HTTPNodeRef{}, false, nil
	}
	r := HTTPNodeRef{Project: o.project, ID: row.ID, Type: row.MType, Name: row.NameDisplay}
	o.cache[id] = &r
	return r, true, nil
}

// readEdgeGraph — resolve.ObjectEdgeGraph поверх read-транзакции: граф
// вызовов и владельцы модулей. Ошибка транспорта копится и спрашивается
// после обхода (идиома Err(), как у txEdgeGraph в internal/index): пока она
// не снята, ответы пусты, и недостроенная цепочка даёт отсутствие владельца,
// а не выдуманного.
type readEdgeGraph struct {
	tx      *store.ReadTx
	callers map[int64][]resolve.SymbolCall
	owners  map[int64]*resolve.SymbolOwner
	err     error
}

func newReadEdgeGraph(tx *store.ReadTx) *readEdgeGraph {
	return &readEdgeGraph{tx: tx, callers: map[int64][]resolve.SymbolCall{}, owners: map[int64]*resolve.SymbolOwner{}}
}

func (g *readEdgeGraph) Err() error { return g.err }

func (g *readEdgeGraph) CallersOf(symbolID int64) []resolve.SymbolCall {
	if g.err != nil {
		return nil
	}
	if c, ok := g.callers[symbolID]; ok {
		return c
	}
	rows, err := g.tx.CallEdgesTo(symbolID)
	if err != nil {
		g.err = fmt.Errorf("вызывающие символа %d: %w", symbolID, err)
		return nil
	}
	out := make([]resolve.SymbolCall, 0, len(rows))
	for _, r := range rows {
		if r.CallerID != 0 {
			out = append(out, resolve.SymbolCall{CallerID: r.CallerID, Confidence: r.Confidence})
		}
	}
	g.callers[symbolID] = out
	return out
}

func (g *readEdgeGraph) SymbolOwner(symbolID int64) (resolve.SymbolOwner, bool) {
	if g.err != nil {
		return resolve.SymbolOwner{}, false
	}
	if o, ok := g.owners[symbolID]; ok {
		if o == nil {
			return resolve.SymbolOwner{}, false
		}
		return *o, true
	}
	sym, ok, err := g.tx.SymbolByID(symbolID)
	if err != nil {
		g.err = fmt.Errorf("символ %d: %w", symbolID, err)
		return resolve.SymbolOwner{}, false
	}
	if !ok {
		g.owners[symbolID] = nil
		return resolve.SymbolOwner{}, false
	}
	mod, found, err := g.tx.ModuleByFile(sym.OriginFileID)
	if err != nil {
		g.err = fmt.Errorf("модуль символа %d: %w", symbolID, err)
		return resolve.SymbolOwner{}, false
	}
	if !found {
		g.owners[symbolID] = nil
		return resolve.SymbolOwner{}, false
	}
	o := resolve.SymbolOwner{ModuleKind: mod.Kind, OwnerObjectID: mod.OwnerObjectID, FileID: sym.OriginFileID}
	g.owners[symbolID] = &o
	return o, true
}

// CrossLinksInput — вход CrossLinks: корни проектов, зарегистрированных в
// реестре workspace. Пусто — только активный проект.
type CrossLinksInput struct {
	ProjectRoots []string
}

// CrossLinks — HTTP-связи активного проекта и перечисленных (MCP-сторона;
// карта собирает то же через ReadHTTPFacts по своим проектам). Маппинг
// хостов читается из workspace сервера.
func (g *ObjectGraphService) CrossLinks(ctx context.Context, in CrossLinksInput) (Response[CrossLinksItem], error) {
	ops, err := g.crossProjects(ctx, "", in.ProjectRoots)
	if err != nil {
		return Response[CrossLinksItem]{}, err
	}
	item, gen, snap, warnings, err := g.stitchProjects(ctx, ops)
	if err != nil {
		return Response[CrossLinksItem]{}, err
	}
	resp := Response[CrossLinksItem]{Generation: gen, Items: []CrossLinksItem{item}, Warnings: warnings, TotalCount: len(item.Links)}
	return withSnapshot(resp, snap), nil
}

// ObjectHTTPLinksInput — HTTP-связи одного объекта (object_graph).
type ObjectHTTPLinksInput struct {
	Target            ObjectTarget
	ProjectRoot       string
	CrossProjectRoots []string
}

// ObjectHTTPLinks — HTTP-связи объекта с сервисами перечисленных проектов
// (и входящие вызовы, если объект сам HTTP-сервис).
func (g *ObjectGraphService) ObjectHTTPLinks(ctx context.Context, in ObjectHTTPLinksInput) (Response[CrossLinksItem], error) {
	target, terr := normalizeObjectTarget(in.Target)
	if terr != nil {
		return Response[CrossLinksItem]{}, terr
	}
	ops, err := g.crossProjects(ctx, in.ProjectRoot, in.CrossProjectRoots)
	if err != nil {
		return Response[CrossLinksItem]{}, err
	}
	own := ops[0]
	rows, _, err := ReadSnapshot(ctx, own, func(tx *store.ReadTx) ([]store.MetadataObjectRow, error) {
		return resolveObjectTargets(tx, own.Manifest, target)
	})
	if err != nil {
		return Response[CrossLinksItem]{}, err
	}
	if len(rows) == 0 {
		return Response[CrossLinksItem]{}, NewError(CodeNotFound,
			fmt.Sprintf("объект %s не найден в проекте %s", targetDisplay(target), own.Entry.ID), "")
	}
	nodes := make(map[int64]bool, len(rows))
	for _, r := range rows {
		nodes[r.ID] = true
	}
	item, gen, snap, warnings, err := g.stitchProjects(ctx, ops)
	if err != nil {
		return Response[CrossLinksItem]{}, err
	}
	item = FilterCrossLinks(item, own.Entry.ID, nodes)
	resp := Response[CrossLinksItem]{Generation: gen, Items: []CrossLinksItem{item}, Warnings: warnings, TotalCount: len(item.Links)}
	return withSnapshot(resp, snap), nil
}

// crossProjects открывает свой проект (root или активный) первым и затем
// перечисленные, без повторов.
func (g *ObjectGraphService) crossProjects(ctx context.Context, root string, others []string) ([]*openProject, error) {
	own, err := g.resolveProject(ctx, root)
	if err != nil {
		return nil, err
	}
	ops := []*openProject{own}
	seen := map[domain.ProjectID]bool{own.Entry.ID: true}
	for _, r := range others {
		if strings.TrimSpace(r) == "" {
			continue
		}
		op, err := g.projects.ByRoot(ctx, r)
		if err != nil {
			return nil, err
		}
		if seen[op.Entry.ID] {
			continue
		}
		seen[op.Entry.ID] = true
		ops = append(ops, op)
	}
	return ops, nil
}

// stitchProjects читает факты каждого проекта (по транзакции на проект) и
// сшивает их. Поколение ответа — поколение первого, своего проекта.
func (g *ObjectGraphService) stitchProjects(ctx context.Context, ops []*openProject) (CrossLinksItem, domain.Generation, Snapshot, []Warning, error) {
	hosts, herr := workspace.LoadHTTPHosts(g.projects.workspaceRoot)
	var warnings []Warning
	if herr != nil {
		// Сломанный маппинг не валит ответ, но и не молчит: без него все
		// вызовы с литеральным хостом видны внешними.
		warnings = append(warnings, Warning{Code: "http_hosts_invalid", Message: herr.Error(),
			Hint: "исправьте " + workspace.HTTPHostsFileName + ", формат в README"})
	}
	var snap Snapshot
	facts := make([]ProjectHTTPFacts, 0, len(ops))
	for _, op := range ops {
		f, s, err := ReadHTTPFacts(ctx, op)
		if err != nil {
			return CrossLinksItem{}, "", Snapshot{}, nil, err
		}
		snap.Stale = snap.Stale || s.Stale
		snap.Warnings = append(snap.Warnings, s.Warnings...)
		warnings = append(warnings, f.Warnings...)
		facts = append(facts, f)
	}
	return StitchCrossLinks(hosts, facts), facts[0].Generation, snap, warnings, nil
}

// HTTPFacts — факты HTTP активного проекта сервиса (graph-режим: каждый
// --project открыт своим сервисом, сшивку делает CrossLinksResponse).
func (g *ObjectGraphService) HTTPFacts(ctx context.Context) (ProjectHTTPFacts, Snapshot, error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return ProjectHTTPFacts{}, Snapshot{}, err
	}
	return ReadHTTPFacts(ctx, op)
}

// CrossLinksResponse собирает ответ /api/crosslinks из фактов проектов,
// прочитанных по отдельности: свежесть объединяется (stale любого проекта
// делает ответ stale), предупреждения фактов и маппинга хостов идут в ответ.
func CrossLinksResponse(hosts workspace.HTTPHosts, facts []ProjectHTTPFacts, snaps []Snapshot, warnings []Warning) Response[CrossLinksItem] {
	var snap Snapshot
	for _, s := range snaps {
		snap.Stale = snap.Stale || s.Stale
		for _, w := range s.Warnings {
			if !containsWarning(snap.Warnings, w) {
				snap.Warnings = append(snap.Warnings, w)
			}
		}
	}
	for _, f := range facts {
		warnings = append(warnings, f.Warnings...)
	}
	item := StitchCrossLinks(hosts, facts)
	resp := Response[CrossLinksItem]{Items: []CrossLinksItem{item}, Warnings: warnings, TotalCount: len(item.Links)}
	if len(facts) > 0 {
		resp.Generation = facts[0].Generation
	}
	return withSnapshot(resp, snap)
}

func containsWarning(ws []Warning, w Warning) bool {
	for _, x := range ws {
		if x == w {
			return true
		}
	}
	return false
}
