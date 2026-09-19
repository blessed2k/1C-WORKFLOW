package bsl

import (
	"bytes"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Файл: исходящие HTTP-вызовы модуля (веха В2, решение D10 в
// docs/architecture-graph.md, ADR-039): кто и куда ходит через
// HTTPСоединение. Факт строится по форме кода внутри одного метода, без
// резолвера:
//
//	Соединение = Новый HTTPСоединение("erp.example.local");
//	Запрос = Новый HTTPЗапрос("/erp/hs/exchange/v1/orders");
//	Ответ = Соединение.ОтправитьДляОбработки(Запрос);
//
// Привязки переменных локальны методу (как у регистров, registerBinding) и
// сбрасываются вместе с p.binds. Адрес собирается как последовательность
// частей: литералы и вычисляемые куски (склейка через '+', подстановки
// СтрШаблон, строковые переменные метода). Из частей выводится, что о пути
// известно: целиком, начало с концом или путь от сегмента /hs/ при
// вычисляемом начале (СтруктураURI.ПутьНаСервере + "/hs/..."). Остальное
// честно динамическое: ребра не даёт, даёт бейдж (D7, docs/architecture-graph.md §6.3).

// HTTPPathKind и его значения живут в domain (общий тип вызова).
type HTTPPathKind = domain.HTTPPathKind

const (
	HTTPPathStatic  = domain.HTTPPathStatic
	HTTPPathPrefix  = domain.HTTPPathPrefix
	HTTPPathDynamic = domain.HTTPPathDynamic
)

// ConfidenceHTTPCall: достоверность факта HTTP-вызова: связь переменной с
// соединением и запросом выведена по локальным присваиваниям метода, то есть
// эвристикой.
const ConfidenceHTTPCall domain.Confidence = 0.85

// HTTPCall: исходящий HTTP-вызов: метод соединения с известным запросом, с
// соединением, созданным в этом же методе, или с соединением из параметра.
type HTTPCall struct {
	domain.HTTPTarget
	Span       domain.Span // Соединение.Метод(...)
	Method     int
	Confidence domain.Confidence
	Provenance domain.Provenance
}

// httpBinds: локальные переменные метода, за которыми закреплены соединение,
// запрос или строка. Ключ: нормализованное имя переменной.
type httpBinds struct {
	conns    map[string]httpHost
	requests map[string][]pathPart
	strs     map[string][]pathPart
}

type httpHost struct {
	host   string
	static bool
}

// pathPart: часть адреса: литерал (dyn=false) или вычисляемый кусок.
type pathPart struct {
	lit string
	dyn bool
}

var dynParts = []pathPart{{dyn: true}}

// httpVerbs: методы HTTPСоединение, отправляющие запрос первым аргументом
// (bsl_syntax, тип HTTPСоединение), и их HTTP-метод.
var httpVerbs = map[string]string{
	"получить": "GET", "get": "GET", "получитьасинх": "GET", "getasync": "GET",
	"получитьзаголовки": "HEAD", "head": "HEAD", "получитьзаголовкиасинх": "HEAD", "headasync": "HEAD",
	"отправитьдляобработки": "POST", "post": "POST", "отправитьдляобработкиасинх": "POST", "postasync": "POST",
	"записать": "PUT", "put": "PUT", "записатьасинх": "PUT", "putasync": "PUT",
	"изменить": "PATCH", "patch": "PATCH", "изменитьасинх": "PATCH", "patchasync": "PATCH",
	"удалить": "DELETE", "delete": "DELETE", "удалитьасинх": "DELETE", "deleteasync": "DELETE",
}

// httpOnlyVerbs: методы, которых нет у коллекций (Соответствие.Получить,
// Структура.Удалить): по ним соединение из параметра узнаётся без привязки.
var httpOnlyVerbs = map[string]bool{
	"получитьзаголовки": true, "head": true, "отправитьдляобработки": true, "post": true,
	"получитьасинх": true, "getasync": true, "получитьзаголовкиасинх": true, "headasync": true,
	"отправитьдляобработкиасинх": true, "postasync": true, "записатьасинх": true, "putasync": true,
	"изменитьасинх": true, "patchasync": true, "удалитьасинх": true, "deleteasync": true,
}

// isHTTPCallMethod: ВызватьHTTPМетод(<HTTPМетод>, <HTTPЗапрос>, ...): метод
// первым аргументом, запрос вторым.
func isHTTPCallMethod(lit []byte) bool {
	return eqAny(lit, "ВызватьHTTPМетод", "CallHTTPMethod", "ВызватьHTTPМетодАсинх", "CallHTTPMethodAsync")
}

// collectHTTP распознаёт на позиции i конструкторы соединения и запроса,
// присваивания строк и адреса ресурса и сам вызов метода соединения.
func (p *parser) collectHTTP(i int) {
	t := p.toks[i]
	if t.kind != tokIdent {
		return
	}
	if eqAny(t.lit, "Новый", "New") {
		p.collectHTTPConstructor(i)
		return
	}
	if i > 0 && isPunct(p.toks[i-1], '.') {
		return // сегмент чужой цепочки
	}
	if i+1 >= len(p.toks) {
		return
	}
	// Переменная = "строка" ...;
	if isPunct(p.toks[i+1], '=') {
		p.bindHTTPString(i)
		return
	}
	if i+3 >= len(p.toks) || !isPunct(p.toks[i+1], '.') || p.toks[i+2].kind != tokIdent {
		return
	}
	// Запрос.АдресРесурса = ...;
	if isPunct(p.toks[i+3], '=') && eqAny(p.toks[i+2].lit, "АдресРесурса", "ResourceAddress") {
		name := domain.NormalizeName(string(t.lit))
		if _, ok := p.http.requests[name]; ok {
			end := p.statementEnd(i + 4)
			p.http.requests[name] = p.exprParts(i+4, end)
		}
		return
	}
	if isPunct(p.toks[i+3], '(') {
		p.collectHTTPVerb(i)
	}
}

// collectHTTPConstructor разбирает Новый HTTPСоединение(...) и
// Новый HTTPЗапрос(...), и если результат присвоен переменной (Х = Новый ...),
// закрепляет его за ней.
func (p *parser) collectHTTPConstructor(i int) {
	if i+2 >= len(p.toks) || p.toks[i+1].kind != tokIdent || !isPunct(p.toks[i+2], '(') {
		return
	}
	varName := ""
	if i >= 2 && isPunct(p.toks[i-1], '=') && p.toks[i-2].kind == tokIdent &&
		(i == 2 || !isPunct(p.toks[i-3], '.')) {
		varName = domain.NormalizeName(string(p.toks[i-2].lit))
	}
	if varName == "" {
		return
	}
	typeTok := p.toks[i+1]
	args, _ := p.argRanges(i + 2)
	switch {
	case eqAny(typeTok.lit, "HTTPСоединение", "HTTPConnection"):
		host := httpHost{}
		if len(args) > 0 {
			host = p.classifyHost(args[0][0], args[0][1])
		}
		p.ensureHTTPBinds()
		p.http.conns[varName] = host
	case eqAny(typeTok.lit, "HTTPЗапрос", "HTTPRequest"):
		parts := []pathPart{{lit: ""}}
		if len(args) > 0 {
			parts = p.exprParts(args[0][0], args[0][1])
		}
		p.ensureHTTPBinds()
		p.http.requests[varName] = parts
	}
}

// bindHTTPString запоминает переменную, которой присвоено выражение с хотя
// бы одним литералом: Адрес = СтруктураURI.ПутьНаСервере + "/hs/svc/version",
// Шаблон = "/%1/hs/svc/v1/%2". Присваивание выражения без литералов стирает
// прежнюю привязку: последнее присваивание в тексте метода и есть то, что
// увидит следующий за ним вызов.
func (p *parser) bindHTTPString(i int) {
	name := domain.NormalizeName(string(p.toks[i].lit))
	end := p.statementEnd(i + 2)
	parts := p.exprParts(i+2, end)
	if !hasLiteral(parts) {
		if p.http.strs != nil {
			delete(p.http.strs, name)
		}
		return
	}
	p.ensureHTTPBinds()
	p.http.strs[name] = parts
}

// collectHTTPVerb разбирает Соединение.Метод(...) и даёт факт, если
// соединение создано в этом методе, запрос известен или соединение пришло
// параметром метода (тогда адрес честно неизвестен).
func (p *parser) collectHTTPVerb(i int) {
	methodTok := p.toks[i+2]
	methodLower := strings.ToLower(string(methodTok.lit))
	verb, isVerb := httpVerbs[methodLower]
	callMethod := isHTTPCallMethod(methodTok.lit)
	if !isVerb && !callMethod {
		return
	}
	receiver := domain.NormalizeName(string(p.toks[i].lit))
	conn, connBound := p.http.conns[receiver]
	args, closeIdx := p.argRanges(i + 3)
	reqArg := 0
	if callMethod {
		reqArg = 1
		verb = ""
		if len(args) > 0 {
			if v, ok := p.stringLiteral(args[0][0], args[0][1]); ok {
				verb = strings.ToUpper(strings.TrimSpace(v))
			}
		}
	}
	var parts []pathPart
	reqKnown, reqParam := false, false
	if reqArg < len(args) {
		parts, reqKnown = p.requestArg(args[reqArg][0], args[reqArg][1])
		reqParam = !reqKnown && args[reqArg][1]-args[reqArg][0] == 1 && p.isParam(p.toks[args[reqArg][0]].lit)
	}
	target := domain.HTTPTarget{Verb: verb, Host: conn.host, HostStatic: connBound && conn.static}
	switch {
	case reqKnown:
		target = withParts(target, parts)
	case connBound:
		target.PathKind = HTTPPathDynamic
		if reqParam {
			target.DynamicReason = domain.HTTPDynamicRequestParam
		}
	case p.isParam(p.toks[i].lit) && (httpOnlyVerbs[methodLower] || callMethod || looksLikeConnection(receiver)):
		// Соединение пришло параметром, запрос тоже не собран здесь: адреса
		// нет, но вызов не должен пропасть молча (бейдж на модуле вызова).
		target.PathKind = HTTPPathDynamic
		target.DynamicReason = domain.HTTPDynamicConnectionParam
	default:
		return // не отличить от Соответствие.Получить(Ключ)
	}
	end := methodTok.sp.end
	if closeIdx > 0 && closeIdx < len(p.toks) {
		end = p.toks[closeIdx].sp.end
	}
	p.mod.HTTPCalls = append(p.mod.HTTPCalls, HTTPCall{
		HTTPTarget: target,
		Span:       p.li.Span(p.toks[i].sp.start, end),
		Method:     p.method,
		Confidence: ConfidenceHTTPCall,
		Provenance: domain.Provenance{Source: domain.SourceHeuristic, File: p.opts.File, Detail: "http-call-" + string(methodTok.lit)},
	})
}

// looksLikeConnection: имя переменной говорит о соединении (HTTPСоединение,
// Соединение, Connection): так соединение из параметра отличается от
// коллекции с методом Получить.
func looksLikeConnection(nameNorm string) bool {
	return strings.Contains(nameNorm, "соединени") || strings.Contains(nameNorm, "connection") ||
		strings.Contains(nameNorm, "http")
}

// isParam: имя является параметром разбираемого метода.
func (p *parser) isParam(lit []byte) bool {
	if p.method < 0 || p.method >= len(p.mod.Methods) {
		return false
	}
	name := domain.NormalizeName(string(lit))
	for _, prm := range p.mod.Methods[p.method].Params {
		if prm.NameNorm == name {
			return true
		}
	}
	return false
}

// requestArg: запрос в аргументе метода соединения: переменная, за которой
// закреплён запрос, или прямо Новый HTTPЗапрос(...).
func (p *parser) requestArg(start, end int) ([]pathPart, bool) {
	if end-start == 1 && p.toks[start].kind == tokIdent {
		r, ok := p.http.requests[domain.NormalizeName(string(p.toks[start].lit))]
		return r, ok
	}
	if end-start >= 3 && eqAny(p.toks[start].lit, "Новый", "New") &&
		eqAny(p.toks[start+1].lit, "HTTPЗапрос", "HTTPRequest") && isPunct(p.toks[start+2], '(') {
		args, _ := p.argRanges(start + 2)
		if len(args) == 0 {
			return []pathPart{{lit: ""}}, true
		}
		return p.exprParts(args[0][0], args[0][1]), true
	}
	return nil, false
}

// classifyHost: сервер соединения: литерал (или переменная с литералом)
// либо ничего.
func (p *parser) classifyHost(start, end int) httpHost {
	parts := p.exprParts(start, end)
	if len(parts) != 1 || parts[0].dyn || strings.TrimSpace(parts[0].lit) == "" {
		return httpHost{}
	}
	return httpHost{host: parts[0].lit, static: true}
}

// exprParts раскладывает выражение [start,end) на части по '+' верхнего
// уровня: литерал, СтрШаблон с известным шаблоном (литерал или строковая
// переменная метода), строковая переменная метода; остальное вычисляется.
func (p *parser) exprParts(start, end int) []pathPart {
	if start >= end || end > len(p.toks) {
		return dynParts
	}
	var parts []pathPart
	depth, opStart := 0, start
	for j := start; j <= end; j++ {
		if j < end {
			t := p.toks[j]
			switch {
			case isPunct(t, '(') || isPunct(t, '['):
				depth++
				continue
			case isPunct(t, ')') || isPunct(t, ']'):
				depth--
				continue
			case !(isPunct(t, '+') && depth == 0):
				continue
			}
		}
		parts = append(parts, p.operandParts(opStart, j)...)
		opStart = j + 1
	}
	return mergeParts(parts)
}

// operandParts: части одного операнда склейки [start,end).
func (p *parser) operandParts(start, end int) []pathPart {
	if start >= end {
		return dynParts
	}
	first := p.toks[start]
	switch {
	case end-start == 1 && first.kind == tokString:
		return []pathPart{{lit: unquoteBSL(first.lit)}}
	case end-start == 1 && first.kind == tokIdent && (start == 0 || !isPunct(p.toks[start-1], '.')):
		if s, ok := p.http.strs[domain.NormalizeName(string(first.lit))]; ok {
			return s
		}
	case first.kind == tokIdent && eqAny(first.lit, "СтрШаблон", "StrTemplate") &&
		start+1 < end && isPunct(p.toks[start+1], '('):
		args, closeIdx := p.argRanges(start + 1)
		if closeIdx != end-1 || len(args) == 0 {
			return dynParts
		}
		tmpl := p.exprParts(args[0][0], args[0][1])
		if len(tmpl) != 1 || tmpl[0].dyn {
			return dynParts // шаблон вычисляется: подставлять некуда
		}
		return templateParts(tmpl[0].lit)
	}
	return dynParts
}

// templateParts раскладывает шаблон СтрШаблон: подстановки %1...%10 это
// вычисляемые части, %% это знак процента.
func templateParts(tmpl string) []pathPart {
	var parts []pathPart
	var lit strings.Builder
	for k := 0; k < len(tmpl); k++ {
		if tmpl[k] != '%' || k+1 >= len(tmpl) {
			lit.WriteByte(tmpl[k])
			continue
		}
		if tmpl[k+1] == '%' {
			lit.WriteByte('%')
			k++
			continue
		}
		if tmpl[k+1] < '0' || tmpl[k+1] > '9' {
			lit.WriteByte(tmpl[k])
			continue
		}
		for k+1 < len(tmpl) && tmpl[k+1] >= '0' && tmpl[k+1] <= '9' {
			k++
		}
		parts = append(parts, pathPart{lit: lit.String()}, pathPart{dyn: true})
		lit.Reset()
	}
	parts = append(parts, pathPart{lit: lit.String()})
	return mergeParts(parts)
}

// mergeParts склеивает соседние литералы и соседние вычисляемые части,
// пустые литералы выбрасывает (но выражение из одного пустого литерала
// остаётся литералом).
func mergeParts(in []pathPart) []pathPart {
	var out []pathPart
	for _, pt := range in {
		if !pt.dyn && pt.lit == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].dyn == pt.dyn {
			if !pt.dyn {
				out[n-1].lit += pt.lit
			}
			continue
		}
		out = append(out, pt)
	}
	if len(out) == 0 && len(in) > 0 && !in[0].dyn {
		return []pathPart{{lit: ""}}
	}
	return out
}

func hasLiteral(parts []pathPart) bool {
	for _, pt := range parts {
		if !pt.dyn {
			return true
		}
	}
	return false
}

// withParts выводит из частей адреса, что о пути известно:
//   - только литералы: путь целиком;
//   - известное начало с сегментом /hs/: начало и статический конец после
//     последней вычисляемой части;
//   - вычисляемое начало, но дальше литерал с /hs/: путь от /hs/ (имя
//     публикации неизвестно, сшивка его и не сверяет), целиком или с концом;
//   - остальное: путь динамический.
func withParts(t domain.HTTPTarget, parts []pathPart) domain.HTTPTarget {
	if len(parts) == 0 {
		t.PathKind = HTTPPathDynamic
		return t
	}
	if !parts[0].dyn && len(parts) == 1 {
		t.Path, t.PathKind = parts[0].lit, HTTPPathStatic
		return t
	}
	suffix := ""
	if last := parts[len(parts)-1]; !last.dyn {
		suffix = last.lit
	}
	if !parts[0].dyn && hsIndex(parts[0].lit) >= 0 {
		t.Path, t.PathKind, t.PathSuffix = parts[0].lit, HTTPPathPrefix, suffix
		return t
	}
	for k, pt := range parts {
		if pt.dyn {
			continue
		}
		at := hsIndex(pt.lit)
		if at < 0 {
			continue
		}
		t.PathAnchored = true
		t.Path = pt.lit[at:]
		if k == len(parts)-1 {
			t.PathKind = HTTPPathStatic
			return t
		}
		t.PathKind, t.PathSuffix = HTTPPathPrefix, suffix
		return t
	}
	t.PathKind = HTTPPathDynamic
	return t
}

// hsIndex: позиция сегмента "/hs/" (без учёта регистра) в литерале.
func hsIndex(lit string) int {
	lower := strings.ToLower(lit)
	if i := strings.Index(lower, "/hs/"); i >= 0 {
		return i
	}
	return -1
}

// stringLiteral: выражение ровно из одного строкового литерала.
func (p *parser) stringLiteral(start, end int) (string, bool) {
	if end-start != 1 || p.toks[start].kind != tokString {
		return "", false
	}
	return unquoteBSL(p.toks[start].lit), true
}

// argRanges разбирает список аргументов вызова от открывающей скобки open:
// полуинтервалы токенов каждого аргумента и индекс закрывающей скобки
// (0, если скобка не закрыта до конца потока).
func (p *parser) argRanges(open int) ([][2]int, int) {
	var out [][2]int
	depth := 0
	start := open + 1
	for j := open + 1; j < len(p.toks); j++ {
		t := p.toks[j]
		switch {
		case isPunct(t, '(') || isPunct(t, '['):
			depth++
		case isPunct(t, ')') || isPunct(t, ']'):
			if depth == 0 {
				out = append(out, [2]int{start, j})
				return out, j
			}
			depth--
		case isPunct(t, ',') && depth == 0:
			out = append(out, [2]int{start, j})
			start = j + 1
		case isPunct(t, ';') || p.isDeclarationStart(j):
			return out, 0
		}
	}
	return out, 0
}

// statementEnd: индекс ';' (или конца тела) после start на нулевой глубине
// скобок.
func (p *parser) statementEnd(start int) int {
	depth := 0
	for j := start; j < len(p.toks); j++ {
		t := p.toks[j]
		switch {
		case isPunct(t, '(') || isPunct(t, '['):
			depth++
		case isPunct(t, ')') || isPunct(t, ']'):
			depth--
		case isPunct(t, ';') && depth <= 0:
			return j
		case isEndKeyword(t) || p.isDeclarationStart(j):
			return j
		}
	}
	return len(p.toks)
}

func (p *parser) ensureHTTPBinds() {
	if p.http.conns == nil {
		p.http.conns = make(map[string]httpHost)
	}
	if p.http.requests == nil {
		p.http.requests = make(map[string][]pathPart)
	}
	if p.http.strs == nil {
		p.http.strs = make(map[string][]pathPart)
	}
}

// unquoteBSL снимает кавычки строкового литерала BSL: "" внутри: одна
// кавычка, продолжение многострочного литерала (перевод строки и '|')
// склеивается в перевод строки.
func unquoteBSL(lit []byte) string {
	inner := stringLiteralInner(lit)
	if len(inner) == 0 {
		return ""
	}
	s := string(bytes.ReplaceAll(inner, []byte(`""`), []byte(`"`)))
	if !strings.Contains(s, "\n") {
		return s
	}
	lines := strings.Split(s, "\n")
	for k := 1; k < len(lines); k++ {
		l := strings.TrimLeft(lines[k], " \t\r")
		lines[k] = strings.TrimPrefix(l, "|")
	}
	return strings.Join(lines, "\n")
}
