package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Файл: HTTP-связи между базами (веха В2, решение D10, ADR-039). Факты
// каждого проекта (http_call, http_endpoint) читаются из его индекса одной
// read-транзакцией, вызов приписывается объекту-владельцу обходом графа
// вызовов (resolve.AttributeSymbolFact, те же правила D6/D7, что у записи в
// регистр), затем resolve.StitchHTTPCall сшивает вызовы с сервисами всех
// переданных проектов по пути и маппингу хостов workspace. Индексы баз
// остаются независимыми файлами: кросс-проектный join живёт здесь, при
// чтении, и правка маппинга хостов не требует переиндексации. Сборка ответа
// (маппинг, его предупреждения, слияние снимков) одна: AssembleCrossLinks;
// graphweb и cmd её только зовут.

// EdgeHTTPCall: вид кросс-базового ребра. В object_data_edge его нет: конец
// ребра лежит в индексе другой базы (ADR-039).
const EdgeHTTPCall = "http-call"

// Бейджи узла по HTTP.
const (
	// BadgeHasDynamicHTTP: адрес вызова не выводится (D7), ребра нет.
	BadgeHasDynamicHTTP = resolve.BadgeHasDynamicHTTP
	// BadgeHasUnresolvedHTTP: адрес известен лишь частью и совпадения нет,
	// или проект мапленного хоста не открыт: адресат не определён.
	BadgeHasUnresolvedHTTP = "has-unresolved-http"
)

// HTTPNodeRef: узел HTTP-связи: объект метаданных конкретного проекта. Id:
// id строки metadata_object ЭТОГО проекта: у разных баз пространства id
// разные, поэтому узел адресуется парой проект+id.
type HTTPNodeRef struct {
	Project domain.ProjectID `json:"project"`
	ID      int64            `json:"id"`
	Type    string           `json:"type"`
	Name    string           `json:"name"`
}

// HTTPLinkEndpoint: метод сервиса, с которым сшит вызов.
type HTTPLinkEndpoint struct {
	Template     string `json:"template"`
	TemplateName string `json:"templateName,omitempty"`
	Method       string `json:"method,omitempty"`
	HTTPMethod   string `json:"httpMethod,omitempty"`
	Handler      string `json:"handler,omitempty"`
}

// HTTPCallSite: место вызова в коде: «почему ребро существует».
type HTTPCallSite struct {
	domain.HTTPTarget
	File   string `json:"file"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol,omitempty"`
	// Attributed: false: цепочка вызовов до объекта не дошла, и концом
	// ребра стал сам модуль вызова (например, общий модуль).
	Attributed bool `json:"attributed"`
}

// HTTPLink: HTTP-связь объекта одной базы с сервисом другой (или с внешним
// адресатом). Внешняя связь: To пуст, External=true, ExternalHost: хост
// вызова (пусто, если путь известен целиком, а хост вычисляется).
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

// HTTPBadge: бейдж узла по HTTP: has-dynamic-http (адрес не выводится) или
// has-unresolved-http (адресат не определён). Ставится только владельцам,
// чья цепочка не прошла через хаб и не длиннее порога глубины (ADR-025),
// иначе модулю вызова. Confidence: наибольшая достоверность приписки среди
// учтённых вызовов, Reasons: сколько вызовов по какой причине.
type HTTPBadge struct {
	Node       HTTPNodeRef    `json:"node"`
	Badge      string         `json:"badge"`
	Count      int            `json:"count"`
	Confidence float64        `json:"confidence"`
	Reasons    map[string]int `json:"reasons,omitempty"`
	Calls      []HTTPCallSite `json:"calls,omitempty"`
}

// CrossLinksItem: HTTP-связи набора проектов.
type CrossLinksItem struct {
	Projects []domain.ProjectID `json:"projects"`
	Links    []HTTPLink         `json:"links"`
	Badges   []HTTPBadge        `json:"badges"`
}

// ProjectHTTPFacts: факты HTTP одного проекта, уже приписанные владельцам.
// Строится ReadHTTPFacts, сшивается AssembleCrossLinks. Не меняется после
// построения: кэш отдаёт один и тот же экземпляр нескольким запросам.
type ProjectHTTPFacts struct {
	Project    domain.ProjectID
	Generation domain.Generation
	Calls      []attributedHTTPCall
	Endpoints  []resolve.HTTPEndpointFact
	// services: объект-сервис по id, для имени конца ребра.
	services map[int64]HTTPNodeRef
	Warnings []Warning
}

type attributedHTTPCall struct {
	site HTTPCallSite
	// owners: концы рёбер (все приписанные владельцы, иначе модуль вызова).
	owners []httpOwner
	// module: модуль вызова, конец бейджа, когда прямых владельцев нет.
	module     *HTTPNodeRef
	confidence float64
}

type httpOwner struct {
	node       HTTPNodeRef
	confidence float64
	// direct: цепочка без хаба и не длиннее порога глубины.
	direct bool
}

// httpSiteLimit: сколько мест вызова показывает одна связь или бейдж: у
// общего модуля обмена вызовов бывают десятки, а ответ агенту ограничен.
const httpSiteLimit = 10

// httpFactsCacheSize: сколько поколений проектов держит кэш фактов. Ключ
// включает поколение: новая публикация индекса промахивается сама.
const httpFactsCacheSize = 8

type httpFactsKey struct {
	root       string
	project    domain.ProjectID
	generation domain.Generation
	tunables   resolve.ObjectEdgeTunables
}

// httpFactsCache: факты HTTP с атрибуцией по (проект, поколение). Живёт на
// процесс: graph-режим открывает индекс заново на каждый запрос (graphweb,
// doc.go), и без кэша каждый клик по «HTTP-связи» повторял бы обход графа
// вызовов по всем вызовам всех проектов.
var httpFactsCache = struct {
	mu      sync.Mutex
	entries map[httpFactsKey]ProjectHTTPFacts
	order   []httpFactsKey
}{entries: map[httpFactsKey]ProjectHTTPFacts{}}

func cachedHTTPFacts(k httpFactsKey) (ProjectHTTPFacts, bool) {
	httpFactsCache.mu.Lock()
	defer httpFactsCache.mu.Unlock()
	f, ok := httpFactsCache.entries[k]
	return f, ok
}

func storeHTTPFacts(k httpFactsKey, f ProjectHTTPFacts) {
	httpFactsCache.mu.Lock()
	defer httpFactsCache.mu.Unlock()
	if _, ok := httpFactsCache.entries[k]; ok {
		return
	}
	httpFactsCache.entries[k] = f
	httpFactsCache.order = append(httpFactsCache.order, k)
	for len(httpFactsCache.order) > httpFactsCacheSize {
		delete(httpFactsCache.entries, httpFactsCache.order[0])
		httpFactsCache.order = httpFactsCache.order[1:]
	}
}

// ReadHTTPFacts читает факты HTTP проекта op одной read-транзакцией и
// приписывает вызовы владельцам (с кэшем по поколению).
func ReadHTTPFacts(ctx context.Context, op *openProject) (ProjectHTTPFacts, Snapshot, error) {
	return ReadSnapshot(ctx, op, func(tx *store.ReadTx) (ProjectHTTPFacts, error) {
		return readHTTPFactsTx(tx, op)
	})
}

// readHTTPFactsTx: то же внутри уже открытой транзакции (ObjectHTTPLinks
// читает цель и факты своего проекта одной транзакцией).
func readHTTPFactsTx(tx *store.ReadTx, op *openProject) (ProjectHTTPFacts, error) {
	gen, err := tx.Generation()
	if err != nil {
		return ProjectHTTPFacts{}, err
	}
	tunables := currentGraphTunables()
	key := httpFactsKey{root: op.Entry.Root, project: op.Entry.ID, generation: gen, tunables: tunables}
	if f, ok := cachedHTTPFacts(key); ok {
		return f, nil
	}
	f, err := buildHTTPFacts(tx, op.Entry.ID, gen, tunables)
	if err != nil {
		return ProjectHTTPFacts{}, err
	}
	storeHTTPFacts(key, f)
	return f, nil
}

// httpFactsBuilds: сколько раз факты строились заново (промахи кэша);
// читают тесты кэша.
var httpFactsBuilds atomic.Int64

func buildHTTPFacts(tx *store.ReadTx, project domain.ProjectID, gen domain.Generation, tunables resolve.ObjectEdgeTunables) (ProjectHTTPFacts, error) {
	httpFactsBuilds.Add(1)
	out := ProjectHTTPFacts{Project: project, Generation: gen, services: map[int64]HTTPNodeRef{}}
	objects := newObjectNames(tx, project)
	eps, err := tx.HTTPEndpoints()
	if err != nil {
		return out, fmt.Errorf("методы HTTP-сервисов: %w", err)
	}
	for _, e := range eps {
		if e.ServiceID == 0 {
			continue // XML без объекта-сервиса: концу ребра нечем быть
		}
		out.Endpoints = append(out.Endpoints, resolve.HTTPEndpointFact{
			Project: project, ID: e.ID, ServiceID: e.ServiceID, RootURL: e.RootURL,
			Template: e.Template, TemplateName: e.TemplateName, MethodName: e.MethodName,
			HTTPMethod: e.HTTPMethod, Handler: e.Handler,
		})
		out.services[e.ServiceID] = HTTPNodeRef{Project: project, ID: e.ServiceID,
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
			site: HTTPCallSite{HTTPTarget: c.HTTPTarget, File: c.RelPath, Line: c.Span.StartLine,
				Symbol: c.SymbolName},
			confidence: c.Confidence,
		}
		if c.ModuleOwnerID != 0 {
			node, ok, err := objects.ref(c.ModuleOwnerID)
			if err != nil {
				return out, err
			}
			if ok {
				ac.module = &node
			}
		}
		// Соединение из параметра: адреса нет, вызывающих не ищем, бейдж
		// получает модуль вызова.
		if c.DynamicReason != domain.HTTPDynamicConnectionParam {
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
					ac.owners = append(ac.owners, httpOwner{node: node, confidence: a.Confidence, direct: a.Direct})
				}
			}
		}
		if len(ac.owners) > 0 {
			ac.site.Attributed = true
		} else if ac.module != nil {
			// Цепочка до объекта не дошла: концом ребра становится модуль
			// вызова (общий модуль, модуль сервиса), а не пустота.
			ac.owners = []httpOwner{{node: *ac.module, confidence: c.Confidence, direct: true}}
		}
		if len(ac.owners) == 0 {
			orphan++
			continue
		}
		out.Calls = append(out.Calls, ac)
	}
	if truncated > 0 {
		out.Warnings = append(out.Warnings, Warning{Code: "http_attribution_truncated",
			Message: fmt.Sprintf("проект %s: у %d HTTP-вызовов обход графа вызовов упёрся в потолок, часть владельцев могла не найтись", project, truncated)})
	}
	if orphan > 0 {
		out.Warnings = append(out.Warnings, Warning{Code: "http_call_without_owner",
			Message: fmt.Sprintf("проект %s: %d HTTP-вызовов вне модуля объекта, приписать их некому", project, orphan)})
	}
	return out, nil
}

// AssembleCrossLinks: единственная сборка ответа HTTP-связей. Маппинги
// хостов читаются из http-hosts.json каждого workspace (по одному разу,
// в порядке списка) и сливаются, сломанный файл и расхождение идут
// предупреждениями, свежесть снимков объединяется (stale любого проекта
// делает ответ stale). Поколение ответа: поколение первого проекта.
func AssembleCrossLinks(workspaceRoots []string, facts []ProjectHTTPFacts, snaps []Snapshot) Response[CrossLinksItem] {
	var warnings []Warning
	var maps []workspace.HTTPHosts
	seen := map[string]bool{}
	for _, root := range workspaceRoots {
		if seen[root] {
			continue
		}
		seen[root] = true
		m, err := workspace.LoadHTTPHosts(root)
		if err != nil {
			// Сломанный маппинг не валит ответ, но и не молчит: без него все
			// вызовы с литеральным хостом видны внешними.
			warnings = append(warnings, Warning{Code: "http_hosts_invalid", Message: err.Error(),
				Hint: "исправьте " + workspace.HTTPHostsFileName + ", формат в README"})
			continue
		}
		maps = append(maps, m)
	}
	hosts, conflicts := workspace.MergeHTTPHosts(maps...)
	for _, c := range conflicts {
		warnings = append(warnings, Warning{Code: "http_hosts_conflict", Message: c})
	}
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

// StitchCrossLinks сшивает факты нескольких проектов в связи. Связь одна на
// пару «владелец вызова, сервис» (или «владелец, внешний хост»): несколько
// вызовов одного объекта к одному сервису дают одну связь с перечнем мест и
// методов, достоверность: максимум по вызовам.
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
			st := resolve.StitchHTTPCall(c.site.HTTPTarget, endpoints, hosts.Lookup)
			switch st.Kind {
			case resolve.HTTPDynamic:
				addBadges(badges, c, BadgeHasDynamicHTTP, st.Reason)
			case resolve.HTTPUnknown:
				addBadges(badges, c, BadgeHasUnresolvedHTTP, st.Reason)
			case resolve.HTTPExternal:
				for _, owner := range c.owners {
					id := fmt.Sprintf("http-ext:%s:%d:%s", owner.node.Project, owner.node.ID, st.Host)
					l := links[id]
					if l == nil {
						l = &HTTPLink{ID: id, Kind: EdgeHTTPCall, From: owner.node, External: true,
							ExternalHost: st.Host, Reason: st.Reason}
						links[id] = l
					}
					addCallSite(l, c.site, owner.confidence)
				}
			case resolve.HTTPStitched:
				for _, owner := range c.owners {
					addStitched(links, services, owner, c.site, st)
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
		a, b := item.Badges[i], item.Badges[j]
		if a.Node.Project != b.Node.Project {
			return a.Node.Project < b.Node.Project
		}
		if a.Node.ID != b.Node.ID {
			return a.Node.ID < b.Node.ID
		}
		return a.Badge < b.Badge
	})
	return item
}

// addStitched добавляет связь владельца с каждым сервисом, чьи методы
// совпали.
func addStitched(links map[string]*HTTPLink, services map[domain.ProjectID]map[int64]HTTPNodeRef, owner httpOwner, site HTTPCallSite, st resolve.HTTPStitch) {
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
			addCallSite(l, site, min(st.Confidence, owner.confidence))
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

// addBadges ставит бейдж прямым владельцам вызова (без хаба, не длиннее
// порога глубины); за каждого непрямого владельца, за отсутствие владельцев и
// за соединение из параметра бейдж получает модуль вызова (один раз на
// вызов). Вызов с неизвестным адресом в общем модуле-хабе иначе ложился бы
// бейджем на сотни объектов (ADR-039).
func addBadges(badges map[string]*HTTPBadge, c attributedHTTPCall, badge, reason string) {
	var targets []httpOwner
	toModule := c.site.DynamicReason == domain.HTTPDynamicConnectionParam || len(c.owners) == 0
	if !toModule {
		for _, o := range c.owners {
			if o.direct {
				targets = append(targets, o)
			} else {
				toModule = true
			}
		}
	}
	if toModule && c.module != nil {
		already := false
		for _, o := range targets {
			already = already || o.node == *c.module
		}
		if !already {
			targets = append(targets, httpOwner{node: *c.module, confidence: c.confidence})
		}
	}
	for _, o := range targets {
		key := fmt.Sprintf("%s:%d:%s", o.node.Project, o.node.ID, badge)
		b := badges[key]
		if b == nil {
			b = &HTTPBadge{Node: o.node, Badge: badge, Reasons: map[string]int{}}
			badges[key] = b
		}
		b.Count++
		b.Reasons[reason]++
		if o.confidence > b.Confidence {
			b.Confidence = o.confidence
		}
		if len(b.Calls) < httpSiteLimit {
			b.Calls = append(b.Calls, c.site)
		}
	}
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

// objectNames: узлы-объекты по id с кэшем на транзакцию.
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

// readEdgeGraph: resolve.ObjectEdgeGraph поверх read-транзакции: граф
// вызовов и владельцы модулей. Фронт обхода поднимается одним запросом на
// уровень (resolve.CallerPrefetcher): вызывающие всего фронта и владельцы их
// модулей, а не три точечных запроса на символ. Ошибка транспорта копится и
// спрашивается после обхода (идиома Err(), как у txEdgeGraph в
// internal/index): пока она не снята, ответы пусты, и недостроенная цепочка
// даёт отсутствие владельца, а не выдуманного.
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

// PrefetchCallers поднимает вызывающих символов фронта и владельцев модулей
// самих символов и их вызывающих: два запроса на уровень обхода.
func (g *readEdgeGraph) PrefetchCallers(symbolIDs []int64) {
	if g.err != nil {
		return
	}
	var missing []int64
	for _, id := range symbolIDs {
		if _, ok := g.callers[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		rows, err := g.tx.CallersOfSymbols(missing)
		if err != nil {
			g.err = fmt.Errorf("вызывающие фронта обхода: %w", err)
			return
		}
		for _, id := range missing {
			out := make([]resolve.SymbolCall, 0, len(rows[id]))
			for _, r := range rows[id] {
				if r.CallerID != 0 {
					out = append(out, resolve.SymbolCall{CallerID: r.CallerID, Confidence: r.Confidence})
				}
			}
			g.callers[id] = out
		}
	}
	var ownerIDs []int64
	want := func(id int64) {
		if _, ok := g.owners[id]; !ok {
			ownerIDs = append(ownerIDs, id)
			g.owners[id] = nil // занято: не просить дважды
		}
	}
	for _, id := range symbolIDs {
		want(id)
		for _, c := range g.callers[id] {
			want(c.CallerID)
		}
	}
	g.loadOwners(ownerIDs)
}

func (g *readEdgeGraph) loadOwners(ids []int64) {
	if len(ids) == 0 || g.err != nil {
		return
	}
	rows, err := g.tx.SymbolModuleOwners(ids)
	if err != nil {
		g.err = fmt.Errorf("владельцы символов: %w", err)
		return
	}
	for _, id := range ids {
		r, ok := rows[id]
		if !ok || r.ModuleKind == "" {
			g.owners[id] = nil
			continue
		}
		o := resolve.SymbolOwner{ModuleKind: r.ModuleKind, OwnerObjectID: r.OwnerObjectID, FileID: r.FileID}
		g.owners[id] = &o
	}
}

func (g *readEdgeGraph) CallersOf(symbolID int64) []resolve.SymbolCall {
	if g.err != nil {
		return nil
	}
	if c, ok := g.callers[symbolID]; ok {
		return c
	}
	g.PrefetchCallers([]int64{symbolID})
	return g.callers[symbolID]
}

func (g *readEdgeGraph) SymbolOwner(symbolID int64) (resolve.SymbolOwner, bool) {
	if g.err != nil {
		return resolve.SymbolOwner{}, false
	}
	o, ok := g.owners[symbolID]
	if !ok {
		g.owners[symbolID] = nil
		g.loadOwners([]int64{symbolID})
		o = g.owners[symbolID]
	}
	if o == nil {
		return resolve.SymbolOwner{}, false
	}
	return *o, true
}

// CrossLinksInput: вход CrossLinks: корни проектов, зарегистрированных в
// реестре workspace. Пусто: только активный проект.
type CrossLinksInput struct {
	ProjectRoots []string
}

// CrossLinks: HTTP-связи активного проекта и перечисленных (MCP-сторона;
// карта собирает то же через HTTPFacts по своим проектам).
func (g *ObjectGraphService) CrossLinks(ctx context.Context, in CrossLinksInput) (Response[CrossLinksItem], error) {
	ops, err := g.crossProjects(ctx, "", in.ProjectRoots)
	if err != nil {
		return Response[CrossLinksItem]{}, err
	}
	facts := make([]ProjectHTTPFacts, 0, len(ops))
	snaps := make([]Snapshot, 0, len(ops))
	for _, op := range ops {
		f, s, err := ReadHTTPFacts(ctx, op)
		if err != nil {
			return Response[CrossLinksItem]{}, err
		}
		facts, snaps = append(facts, f), append(snaps, s)
	}
	return AssembleCrossLinks([]string{g.projects.workspaceRoot}, facts, snaps), nil
}

// ObjectHTTPLinksInput: HTTP-связи одного объекта (object_graph).
type ObjectHTTPLinksInput struct {
	Target            ObjectTarget
	ProjectRoot       string
	CrossProjectRoots []string
}

// ObjectHTTPLinks: HTTP-связи объекта с сервисами перечисленных проектов
// (и входящие вызовы, если объект сам HTTP-сервис). Цель и факты своего
// проекта читаются одной транзакцией.
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
	type ownRead struct {
		rows  []store.MetadataObjectRow
		facts ProjectHTTPFacts
	}
	res, snap, err := ReadSnapshot(ctx, own, func(tx *store.ReadTx) (ownRead, error) {
		rows, err := resolveObjectTargets(tx, own.Manifest, target)
		if err != nil || len(rows) == 0 {
			return ownRead{rows: rows}, err
		}
		f, err := readHTTPFactsTx(tx, own)
		return ownRead{rows: rows, facts: f}, err
	})
	if err != nil {
		return Response[CrossLinksItem]{}, err
	}
	if len(res.rows) == 0 {
		return Response[CrossLinksItem]{}, NewError(CodeNotFound,
			fmt.Sprintf("объект %s не найден в проекте %s", targetDisplay(target), own.Entry.ID), "")
	}
	nodes := make(map[int64]bool, len(res.rows))
	for _, r := range res.rows {
		nodes[r.ID] = true
	}
	facts, snaps := []ProjectHTTPFacts{res.facts}, []Snapshot{snap}
	for _, op := range ops[1:] {
		f, s, err := ReadHTTPFacts(ctx, op)
		if err != nil {
			return Response[CrossLinksItem]{}, err
		}
		facts, snaps = append(facts, f), append(snaps, s)
	}
	resp := AssembleCrossLinks([]string{g.projects.workspaceRoot}, facts, snaps)
	resp.Items[0] = FilterCrossLinks(resp.Items[0], own.Entry.ID, nodes)
	resp.TotalCount = len(resp.Items[0].Links)
	return resp, nil
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

// HTTPFacts: факты HTTP активного проекта сервиса (graph-режим: каждый
// --project открыт своим сервисом, собирает ответ AssembleCrossLinks).
func (g *ObjectGraphService) HTTPFacts(ctx context.Context) (ProjectHTTPFacts, Snapshot, error) {
	op, err := g.projects.Active(ctx)
	if err != nil {
		return ProjectHTTPFacts{}, Snapshot{}, err
	}
	return ReadHTTPFacts(ctx, op)
}

// WithHTTPLinks переносит свежесть и предупреждения HTTP-связей в ответ
// object_graph (без повторов одинаковых предупреждений двух чтений).
func WithHTTPLinks(radius Response[RadiusEdgeItem], links Response[CrossLinksItem]) (Response[RadiusEdgeItem], *CrossLinksItem) {
	radius.Stale = radius.Stale || links.Stale
	for _, w := range links.Warnings {
		if !containsWarning(radius.Warnings, w) {
			radius.Warnings = append(radius.Warnings, w)
		}
	}
	if len(links.Items) == 0 {
		return radius, nil
	}
	return radius, &links.Items[0]
}

func containsWarning(ws []Warning, w Warning) bool {
	for _, x := range ws {
		if x == w {
			return true
		}
	}
	return false
}
