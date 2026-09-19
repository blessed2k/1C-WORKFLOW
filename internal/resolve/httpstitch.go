package resolve

import (
	"net/url"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Файл — сшивка HTTP-вызовов с HTTP-сервисами (веха В2, решение D10,
// ADR-039). Правила чистые: ни store, ни диска, вход — факты уже прочитанных
// индексов и функция, отвечающая, какой проект стоит за хостом.
//
// Правила одни на всё:
//  1. путь, который из текста не выводится (HTTPPathDynamic), ребра не даёт
//     никогда: вызов виден бейджем has-dynamic-http у вызывающего (D7);
//  2. хост из литерала сопоставляется конфигом воркспейса; немапленный хост
//     не ищется по путям чужих баз вовсе: это внешний сервис, «внешний HTTP»;
//  3. мапленный хост ищет сервис по пути только в своём проекте: не нашёлся,
//     значит тоже внешний, с причиной «адресат не найден»;
//  4. хост вычисляется (соединение из параметра, настройки): сервис ищется
//     по пути во всех переданных проектах, с меньшей достоверностью;
//  5. путь сопоставляется так, как его публикует платформа:
//     /<публикация>/hs/<RootURL><Template>, параметры {Имя} занимают ровно
//     один сегмент, "*" забирает остаток.

// BadgeHasDynamicHTTP — бейдж «у объекта есть HTTP-вызов, адрес которого
// статически не выводится». Отдельное имя, а не has-dynamic: счётчики
// регистров и HTTP не складываются в одно число.
const BadgeHasDynamicHTTP = "has-dynamic-http"

// Достоверность сшивки. Сшивка по пути — сопоставление текста, эвристика,
// поэтому ни одна величина не достигает 1 (Provenance.Validate).
const (
	// StitchMappedStatic — хост мапится на проект, путь целиком из литерала.
	StitchMappedStatic = 0.9
	// StitchMappedPrefix — хост мапится, известно только начало пути.
	StitchMappedPrefix = 0.7
	// StitchPathOnlyStatic — хост вычисляется, путь целиком из литерала.
	StitchPathOnlyStatic = 0.6
	// StitchPathOnlyPrefix — хост вычисляется, известно только начало пути.
	StitchPathOnlyPrefix = 0.5
)

// HTTPStitchKind — исход сшивки одного вызова.
type HTTPStitchKind string

const (
	// HTTPStitched — найден сервис (и его шаблон) в проекте.
	HTTPStitched HTTPStitchKind = "stitched"
	// HTTPExternal — адресат вне известных проектов: «внешний HTTP».
	HTTPExternal HTTPStitchKind = "external"
	// HTTPDynamic — путь не выводится, ребра нет.
	HTTPDynamic HTTPStitchKind = "dynamic"
)

// Причины исхода, машинные коды для ответа.
const (
	ReasonHostMapped        = "host-mapped"
	ReasonPathOnly          = "path-only"
	ReasonHostUnmapped      = "host-unmapped"
	ReasonProjectNotLoaded  = "project-not-loaded"
	ReasonNoEndpoint        = "no-endpoint"
	ReasonDynamicPath       = "dynamic-path"
	ReasonPrefixBeforeRoot  = "prefix-before-root"
	ReasonVerbNotAllowed    = "verb-not-allowed"
	httpServicePathSegment  = "hs"
	httpTemplateParamPrefix = "{"
)

// HTTPCallFact — вызов, как его видит сшивка.
type HTTPCallFact struct {
	Verb       string
	Host       string
	HostStatic bool
	Path       string
	PathKind   string // static|prefix|dynamic
}

// HTTPEndpointFact — метод сервиса проекта.
type HTTPEndpointFact struct {
	Project      domain.ProjectID
	ID           int64
	ServiceID    int64
	RootURL      string
	Template     string
	TemplateName string
	MethodName   string
	HTTPMethod   string
	Handler      string
}

// HTTPStitch — исход сшивки вызова.
type HTTPStitch struct {
	Kind   HTTPStitchKind
	Reason string
	// Host — нормализованный хост вызова (пусто, если он вычисляется).
	Host string
	// Project — проект, на который указал маппинг хоста (пусто без маппинга).
	Project domain.ProjectID
	// Endpoints — совпавшие методы; у Kind=stitched непусты. Когда путь
	// совпал с шаблоном, а HTTP-метод вызова шаблон не обрабатывает, здесь
	// методы шаблона и Reason=verb-not-allowed.
	Endpoints  []HTTPEndpointFact
	Confidence float64
}

// HostResolver отвечает, какой проект стоит за нормализованным хостом.
type HostResolver func(host string) (domain.ProjectID, bool)

// StitchHTTPCall сшивает один вызов с методами сервисов загруженных
// проектов (endpoints — по проекту).
func StitchHTTPCall(call HTTPCallFact, endpoints map[domain.ProjectID][]HTTPEndpointFact, hosts HostResolver) HTTPStitch {
	host := ""
	if call.HostStatic {
		host = domain.NormalizeHTTPHost(call.Host)
	}
	if call.PathKind == string(pathDynamic) || call.PathKind == "" {
		return HTTPStitch{Kind: HTTPDynamic, Reason: ReasonDynamicPath, Host: host}
	}
	prefix := call.PathKind == string(pathPrefix)
	if host != "" {
		project, ok := domain.ProjectID(""), false
		if hosts != nil {
			project, ok = hosts(host)
		}
		if !ok {
			return HTTPStitch{Kind: HTTPExternal, Reason: ReasonHostUnmapped, Host: host}
		}
		eps, loaded := endpoints[project]
		if !loaded {
			return HTTPStitch{Kind: HTTPExternal, Reason: ReasonProjectNotLoaded, Host: host, Project: project}
		}
		matched, reason := matchEndpoints(call, eps)
		if len(matched) == 0 {
			return HTTPStitch{Kind: HTTPExternal, Reason: reason, Host: host, Project: project}
		}
		conf := StitchMappedStatic
		if prefix {
			conf = StitchMappedPrefix
		}
		if reason == "" {
			reason = ReasonHostMapped
		}
		return HTTPStitch{Kind: HTTPStitched, Reason: reason, Host: host, Project: project,
			Endpoints: matched, Confidence: conf}
	}
	projects := make([]domain.ProjectID, 0, len(endpoints))
	for p := range endpoints {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i] < projects[j] })
	var all []HTTPEndpointFact
	lastReason := ReasonNoEndpoint
	verbMismatch := false
	for _, p := range projects {
		matched, reason := matchEndpoints(call, endpoints[p])
		if len(matched) == 0 {
			if reason == ReasonPrefixBeforeRoot {
				lastReason = reason
			}
			continue
		}
		verbMismatch = verbMismatch || reason == ReasonVerbNotAllowed
		all = append(all, matched...)
	}
	if len(all) == 0 {
		return HTTPStitch{Kind: HTTPExternal, Reason: lastReason}
	}
	conf := StitchPathOnlyStatic
	if prefix {
		conf = StitchPathOnlyPrefix
	}
	reason := ReasonPathOnly
	if verbMismatch {
		reason = ReasonVerbNotAllowed
	}
	return HTTPStitch{Kind: HTTPStitched, Reason: reason, Endpoints: all, Confidence: conf}
}

// Виды пути (значения bsl.HTTPPathKind, повторены строками: resolve не
// тащит парсер ради трёх констант сравнения).
type pathKindText string

const (
	pathDynamic pathKindText = "dynamic"
	pathPrefix  pathKindText = "prefix"
)

// matchEndpoints — методы, чей шаблон совпал с путём вызова, с учётом
// HTTP-метода. Второе значение — причина, если совпадений нет, либо
// verb-not-allowed, если путь совпал, а метод вызова шаблон не обрабатывает.
func matchEndpoints(call HTTPCallFact, eps []HTTPEndpointFact) ([]HTTPEndpointFact, string) {
	isPrefix := call.PathKind == string(pathPrefix)
	segs, lastPartial := pathSegments(call.Path, isPrefix)
	known := len(segs) // сегменты, известные целиком
	if lastPartial {
		known--
	}
	hs := -1
	for i := 0; i < known; i++ {
		if strings.EqualFold(segs[i], httpServicePathSegment) {
			hs = i
			break
		}
	}
	if hs < 0 {
		if isPrefix {
			return nil, ReasonPrefixBeforeRoot
		}
		return nil, ReasonNoEndpoint
	}
	rest := segs[hs+1:]
	knownRest := known - hs - 1
	type tplKey struct {
		project  domain.ProjectID
		service  int64
		template string
	}
	byTemplate := map[tplKey][]HTTPEndpointFact{}
	var order []tplKey
	beforeRoot := false
	for _, ep := range eps {
		root, _ := pathSegments(ep.RootURL, false)
		if len(root) == 0 {
			continue
		}
		// Корень обязан быть известен целиком: начало пути, оборванное
		// внутри корня, адресата не называет.
		if knownRest < len(root) {
			if isPrefix && prefixMatches(rest, root, lastPartial) {
				beforeRoot = true
			}
			continue
		}
		if !segsEqualFold(rest[:len(root)], root) {
			continue
		}
		tpl, _ := pathSegments(ep.Template, false)
		if !templateMatches(rest[len(root):], tpl, isPrefix, lastPartial) {
			continue
		}
		k := tplKey{ep.Project, ep.ServiceID, ep.Template}
		if _, seen := byTemplate[k]; !seen {
			order = append(order, k)
		}
		byTemplate[k] = append(byTemplate[k], ep)
	}
	if len(order) == 0 {
		if beforeRoot {
			return nil, ReasonPrefixBeforeRoot
		}
		return nil, ReasonNoEndpoint
	}
	var out, mismatched []HTTPEndpointFact
	for _, k := range order {
		for _, ep := range byTemplate[k] {
			if verbAllowed(call.Verb, ep.HTTPMethod) {
				out = append(out, ep)
			} else {
				mismatched = append(mismatched, ep)
			}
		}
	}
	if len(out) == 0 {
		return mismatched, ReasonVerbNotAllowed
	}
	return out, ""
}

// verbAllowed: метод шаблона принимает HTTP-метод вызова. Неизвестный метод
// вызова (вычисляется) совместим с любым.
func verbAllowed(verb, method string) bool {
	if verb == "" || method == "" {
		return true
	}
	m := strings.ToUpper(method)
	return m == "ANY" || m == "*" || strings.EqualFold(m, verb)
}

// templateMatches сопоставляет хвост пути после корня с шаблоном URL.
// isPrefix: хвост — известное начало пути, дальше шаблон может продолжаться
// чем угодно; lastPartial: последний сегмент хвоста оборван склейкой.
func templateMatches(tail, tpl []string, isPrefix, lastPartial bool) bool {
	for i, t := range tpl {
		if t == "*" {
			return true
		}
		if i >= len(tail) {
			// Путь кончился раньше шаблона: у полного пути это несовпадение,
			// у начала пути продолжение неизвестно.
			return isPrefix
		}
		param := strings.HasPrefix(t, httpTemplateParamPrefix) && strings.HasSuffix(t, "}")
		if lastPartial && i == len(tail)-1 {
			return param || strings.HasPrefix(t, tail[i])
		}
		if !param && t != tail[i] {
			return false
		}
	}
	return len(tail) == len(tpl)
}

// prefixMatches: оборванное начало пути совпадает с началом корня (для
// причины prefix-before-root, чтобы отличить её от «сервиса нет»).
func prefixMatches(rest, root []string, lastPartial bool) bool {
	if len(rest) > len(root) {
		return false
	}
	for i := range rest {
		if lastPartial && i == len(rest)-1 {
			return strings.HasPrefix(strings.ToLower(root[i]), strings.ToLower(rest[i]))
		}
		if !strings.EqualFold(rest[i], root[i]) {
			return false
		}
	}
	return true
}

func segsEqualFold(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// pathSegments режет путь на сегменты: схема и хост, если путь записан
// полным URL, запрос (?...) и фрагмент отбрасываются, пустые сегменты
// пропускаются. prefix=true: путь — известное начало; второе значение
// отвечает, оборван ли его последний сегмент ("/a/b" + Х — да, "/a/b/" + Х
// — нет).
func pathSegments(p string, prefix bool) ([]string, bool) {
	p = strings.TrimSpace(p)
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
		if j := strings.IndexByte(p, '/'); j >= 0 {
			p = p[j:]
		} else {
			p = ""
		}
	}
	lastPartial := false
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	} else if prefix {
		lastPartial = p != "" && !strings.HasSuffix(p, "/")
	}
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s == "" {
			continue
		}
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		lastPartial = false
	}
	return out, lastPartial
}

// SymbolFact — факт в теле метода, который надо приписать объекту-владельцу
// (HTTP-вызов): символ, файл и место.
type SymbolFact struct {
	SymbolID   int64
	FileID     int64
	Span       domain.Span
	Confidence float64
}

// Attribution — объект-владелец факта и цепочка до него.
type Attribution struct {
	ObjectID   int64
	Confidence float64
	Chain      []ChainStep
}

// AttributeSymbolFact приписывает факт объектам-владельцам по тем же
// правилам, что запись в регистр (§6.3, D6): вверх по статическому графу
// вызовов сквозь общие модули, достоверность — минимум по цепочке, пороги
// штрафуют, потолки обхода дают truncated. Один владелец — одна атрибуция с
// максимальной достоверностью и кратчайшей цепочкой. Пустой ответ без
// truncated — цепочка не дошла ни до одного объекта (например, вызов из
// регламентного задания через общий модуль без вызывающих).
func AttributeSymbolFact(f SymbolFact, g ObjectEdgeGraph, t ObjectEdgeTunables) (owners []Attribution, truncated bool) {
	if g == nil || f.SymbolID == 0 {
		return nil, false
	}
	acc := newEdgeAccumulator(g, t.normalized())
	hits, truncated := acc.walk(store.RegisterAccessRow{
		SymbolID: f.SymbolID, FileID: f.FileID, Span: f.Span, Confidence: f.Confidence, Static: true,
	})
	byObject := map[int64]int{}
	for _, h := range hits {
		i, seen := byObject[h.objectID]
		if !seen {
			byObject[h.objectID] = len(owners)
			owners = append(owners, Attribution{ObjectID: h.objectID, Confidence: h.confidence, Chain: h.chain})
			continue
		}
		if h.confidence > owners[i].Confidence {
			owners[i].Confidence = h.confidence
		}
		if len(h.chain) < len(owners[i].Chain) {
			owners[i].Chain = h.chain
		}
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].ObjectID < owners[j].ObjectID })
	return owners, truncated
}
