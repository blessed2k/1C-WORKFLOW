package resolve

import (
	"net/url"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Файл: сшивка HTTP-вызовов с HTTP-сервисами (веха В2, решение D10,
// ADR-039, docs/architecture-graph.md). Правила чистые: ни store, ни диска,
// вход: факты уже прочитанных индексов и функция, отвечающая, какой проект
// стоит за хостом.
//
// Правила одни на всё:
//  1. литеральный хост сопоставляется конфигом воркспейса; немапленный хост
//     это внешний сервис («внешний HTTP», external), что бы ни было в пути;
//  2. путь, который из текста не выводится (dynamic), ребра не даёт никогда:
//     вызов виден бейджем has-dynamic-http (D7, docs/architecture-graph.md);
//  3. мапленный хост ищет сервис по пути только в своём проекте, вычисляемый
//     хост ищет во всех переданных проектах с меньшей достоверностью;
//  4. external только в двух случаях: хост известен и не в маппинге, либо
//     путь известен целиком (от /hs/) и сервиса по нему нет. Путь, известный
//     лишь частью, без совпадения, и мапленный проект, который не открыт,
//     дают «адресат не определён» (unknown), а не внешний HTTP;
//  5. путь сопоставляется так, как его публикует платформа:
//     /<публикация>/hs/<RootURL><Template>, имя публикации не сверяется,
//     параметры {Имя} занимают ровно один сегмент, "*" забирает остаток,
//     сегменты сравниваются без учёта регистра.

// BadgeHasDynamicHTTP: бейдж «у объекта есть HTTP-вызов, адрес которого
// статически не выводится». Отдельное имя, а не has-dynamic: счётчики
// регистров и HTTP не складываются в одно число.
const BadgeHasDynamicHTTP = "has-dynamic-http"

// Достоверность сшивки. Сшивка по пути: сопоставление текста, эвристика,
// поэтому ни одна величина не достигает 1 (Provenance.Validate).
const (
	// StitchMappedStatic: хост мапится на проект, путь целиком из литерала.
	StitchMappedStatic = 0.9
	// StitchMappedPrefix: хост мапится, известно только начало пути.
	StitchMappedPrefix = 0.7
	// StitchPathOnlyStatic: хост вычисляется, путь целиком из литерала.
	StitchPathOnlyStatic = 0.6
	// StitchPathOnlyPrefix: хост вычисляется, известно только начало пути.
	StitchPathOnlyPrefix = 0.5
)

// HTTPStitchKind: исход сшивки одного вызова.
type HTTPStitchKind string

const (
	// HTTPStitched: найден сервис (и его шаблон) в проекте.
	HTTPStitched HTTPStitchKind = "stitched"
	// HTTPExternal: адресат вне известных проектов: «внешний HTTP».
	HTTPExternal HTTPStitchKind = "external"
	// HTTPDynamic: путь не выводится, ребра нет.
	HTTPDynamic HTTPStitchKind = "dynamic"
	// HTTPUnknown: адресат не определён: путь известен лишь частью и
	// совпадения нет, либо проект мапленного хоста не открыт.
	HTTPUnknown HTTPStitchKind = "unknown"
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
	ReasonPrefixWithoutHS   = "prefix-without-hs"
	ReasonVerbNotAllowed    = "verb-not-allowed"
	httpServicePathSegment  = "hs"
	httpTemplateParamPrefix = "{"
)

// HTTPCallFact: вызов, как его видит сшивка (общий тип адреса вызова).
type HTTPCallFact = domain.HTTPTarget

// HTTPEndpointFact: метод сервиса проекта.
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

// HTTPStitch: исход сшивки вызова.
type HTTPStitch struct {
	Kind   HTTPStitchKind
	Reason string
	// Host: нормализованный хост вызова (пусто, если он вычисляется).
	Host string
	// Project: проект, на который указал маппинг хоста (пусто без маппинга).
	Project domain.ProjectID
	// Endpoints: совпавшие методы; у Kind=stitched непусты. Когда путь
	// совпал с шаблоном, а HTTP-метод вызова шаблон не обрабатывает, здесь
	// методы шаблона и Reason=verb-not-allowed.
	Endpoints  []HTTPEndpointFact
	Confidence float64
}

// HostResolver отвечает, какой проект стоит за нормализованным хостом.
type HostResolver func(host string) (domain.ProjectID, bool)

// StitchHTTPCall сшивает один вызов с методами сервисов загруженных
// проектов (endpoints: по проекту).
func StitchHTTPCall(call HTTPCallFact, endpoints map[domain.ProjectID][]HTTPEndpointFact, hosts HostResolver) HTTPStitch {
	host := ""
	if call.HostStatic {
		host = domain.NormalizeHTTPHost(call.Host)
	}
	var project domain.ProjectID
	if host != "" {
		ok := false
		if hosts != nil {
			project, ok = hosts(host)
		}
		if !ok {
			return HTTPStitch{Kind: HTTPExternal, Reason: ReasonHostUnmapped, Host: host}
		}
	}
	if call.PathKind == domain.HTTPPathDynamic || call.PathKind == "" {
		reason := ReasonDynamicPath
		if call.DynamicReason != "" {
			reason = call.DynamicReason
		}
		return HTTPStitch{Kind: HTTPDynamic, Reason: reason, Host: host, Project: project}
	}
	prefix := call.PathKind == domain.HTTPPathPrefix
	// notFound: сервиса по пути нет. Внешний HTTP только при пути, известном
	// целиком; у известного лишь частью пути адресат не определён.
	notFound := func(reason string) HTTPStitch {
		kind := HTTPExternal
		if prefix || reason == ReasonPrefixBeforeRoot || reason == ReasonPrefixWithoutHS {
			kind = HTTPUnknown
		}
		return HTTPStitch{Kind: kind, Reason: reason, Host: host, Project: project}
	}
	if host != "" {
		eps, loaded := endpoints[project]
		if !loaded {
			return HTTPStitch{Kind: HTTPUnknown, Reason: ReasonProjectNotLoaded, Host: host, Project: project}
		}
		matched, reason := matchEndpoints(call, eps)
		if len(matched) == 0 {
			return notFound(reason)
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
			if reason != ReasonNoEndpoint {
				lastReason = reason
			}
			continue
		}
		verbMismatch = verbMismatch || reason == ReasonVerbNotAllowed
		all = append(all, matched...)
	}
	if len(all) == 0 {
		if len(projects) == 0 {
			// Ни одного проекта для поиска: сказать «не наш» не по чему.
			return HTTPStitch{Kind: HTTPUnknown, Reason: ReasonProjectNotLoaded}
		}
		return notFound(lastReason)
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

// matchEndpoints: методы, чей шаблон совпал с путём вызова, с учётом
// HTTP-метода. Второе значение: причина, если совпадений нет, либо
// verb-not-allowed, если путь совпал, а метод вызова шаблон не обрабатывает.
func matchEndpoints(call HTTPCallFact, eps []HTTPEndpointFact) ([]HTTPEndpointFact, string) {
	isPrefix := call.PathKind == domain.HTTPPathPrefix
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
		// Без сегмента hs это не адрес HTTP-сервиса 1С (сторонний API,
		// веб-сервис, OData). У пути, известного лишь началом, сегмент hs
		// мог оказаться в вычисляемой части: адресат не определён.
		if isPrefix {
			return nil, ReasonPrefixWithoutHS
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
		if isPrefix && call.PathSuffix != "" && !templateEndsWith(tpl, len(rest)-len(root), lastPartial, call.PathSuffix) {
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
// isPrefix: хвост: известное начало пути, дальше шаблон может продолжаться
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
			return param || hasPrefixFold(t, tail[i])
		}
		if !param && !strings.EqualFold(t, tail[i]) {
			return false
		}
	}
	return len(tail) == len(tpl)
}

// templateEndsWith: шаблон кончается статическим концом пути вызова. known
// сегментов шаблона уже заняты известным началом (последний из них оборван,
// если lastPartial); конец не может на них налезать. Вычисляемая часть между
// ними непуста: при целых сегментах с обеих сторон она занимает хотя бы
// один сегмент. Конец, начатый не с '/', дописывает вычисляемый сегмент: его
// первый сегмент сравнивается как окончание сегмента шаблона. Параметр
// {Имя} совпадает с любым сегментом, "*" в шаблоне принимает любой конец.
func templateEndsWith(tpl []string, known int, lastPartial bool, suffix string) bool {
	if i := strings.IndexAny(suffix, "?#"); i >= 0 {
		suffix = suffix[:i]
	}
	firstPartial := !strings.HasPrefix(suffix, "/")
	segs, _ := pathSegments(suffix, false)
	if len(segs) == 0 {
		return true
	}
	for _, t := range tpl {
		if t == "*" {
			return true
		}
	}
	off := len(tpl) - len(segs)
	minOff := known
	switch {
	case !lastPartial && !firstPartial:
		minOff = known + 1 // вычисляемая часть: целый сегмент между началом и концом
	case lastPartial && firstPartial:
		minOff = known - 1 // вычисляемая часть внутри одного сегмента
	}
	if off < minOff {
		return false
	}
	for i, s := range segs {
		t := tpl[off+i]
		if strings.HasPrefix(t, httpTemplateParamPrefix) && strings.HasSuffix(t, "}") {
			continue
		}
		if i == 0 && firstPartial {
			if !hasSuffixFold(t, s) {
				return false
			}
			continue
		}
		if !strings.EqualFold(t, s) {
			return false
		}
	}
	return true
}

// prefixMatches: оборванное начало пути совпадает с началом корня (для
// причины prefix-before-root, чтобы отличить её от «сервиса нет»).
func prefixMatches(rest, root []string, lastPartial bool) bool {
	if len(rest) > len(root) {
		return false
	}
	for i := range rest {
		if lastPartial && i == len(rest)-1 {
			return hasPrefixFold(root[i], rest[i])
		}
		if !strings.EqualFold(rest[i], root[i]) {
			return false
		}
	}
	return true
}

func hasPrefixFold(s, prefix string) bool {
	return strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix))
}

func hasSuffixFold(s, suffix string) bool {
	return strings.HasSuffix(strings.ToLower(s), strings.ToLower(suffix))
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
// пропускаются. prefix=true: путь: известное начало; второе значение
// отвечает, оборван ли его последний сегмент ("/a/b" + Х: да, "/a/b/" + Х
// : нет).
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

// SymbolFact: факт в теле метода, который надо приписать объекту-владельцу
// (HTTP-вызов): символ, файл и место.
type SymbolFact struct {
	SymbolID   int64
	FileID     int64
	Span       domain.Span
	Confidence float64
}

// Attribution: объект-владелец факта и цепочка до него.
type Attribution struct {
	ObjectID   int64
	Confidence float64
	Chain      []ChainStep
	// Direct: хотя бы одна цепочка до владельца не прошла через хаб и не
	// длиннее порога глубины (пороги ADR-025, ObjectEdgeTunables). Бейдж
	// динамического вызова ставится только таким владельцам.
	Direct bool
}

// AttributeSymbolFact приписывает факт объектам-владельцам по тем же
// правилам, что запись в регистр (§6.3 и D6 в architecture-graph.md): вверх по статическому графу
// вызовов сквозь общие модули, достоверность: минимум по цепочке, пороги
// штрафуют, потолки обхода дают truncated. Один владелец: одна атрибуция с
// максимальной достоверностью и кратчайшей цепочкой. Пустой ответ без
// truncated: цепочка не дошла ни до одного объекта (например, вызов из
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
			owners = append(owners, Attribution{ObjectID: h.objectID, Confidence: h.confidence, Chain: h.chain, Direct: !h.penalized})
			continue
		}
		owners[i].Direct = owners[i].Direct || !h.penalized
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
