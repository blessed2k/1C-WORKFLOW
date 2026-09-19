package bsl

import (
	"bytes"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Файл — исходящие HTTP-вызовы модуля (веха В2, решение D10): кто и куда
// ходит через HTTPСоединение. Факт строится по форме кода внутри одного
// метода, без резолвера:
//
//	Соединение = Новый HTTPСоединение("erp.example.local");
//	Запрос = Новый HTTPЗапрос("/erp/hs/exchange/v1/orders");
//	Ответ = Соединение.ОтправитьДляОбработки(Запрос);
//
// Привязки переменных локальны методу (как у регистров, registerBinding) и
// сбрасываются вместе с p.binds. Адрес, который форма кода не даёт узнать
// (параметр метода, поле структуры, вызов функции), честно помечается
// динамическим: такой вызов рёбер не даёт, а даёт бейдж (§6.3, D7).

// HTTPPathKind — насколько путь запроса известен статически.
type HTTPPathKind string

const (
	// HTTPPathStatic — путь целиком из литерала.
	HTTPPathStatic HTTPPathKind = "static"
	// HTTPPathPrefix — статическое начало и вычисляемый хвост:
	// "/base/hs/svc/" + Номер, СтрШаблон("/base/hs/svc/%1", Номер).
	HTTPPathPrefix HTTPPathKind = "prefix"
	// HTTPPathDynamic — путь не выводится из текста метода.
	HTTPPathDynamic HTTPPathKind = "dynamic"
)

// ConfidenceHTTPCall — достоверность факта HTTP-вызова: связь переменной с
// соединением и запросом выведена по локальным присваиваниям метода, то есть
// эвристикой.
const ConfidenceHTTPCall domain.Confidence = 0.85

// HTTPCall — исходящий HTTP-вызов: метод соединения с известным запросом или
// с соединением, созданным в этом же методе.
type HTTPCall struct {
	Span   domain.Span // Соединение.Метод
	Method int
	// Verb — HTTP-метод вызова (GET, POST, ...); пусто, если не выводится
	// (ВызватьHTTPМетод с вычисляемым именем метода).
	Verb string
	// Host — сервер из литерала конструктора HTTPСоединение как написан;
	// HostStatic=false, если сервер вычисляется или соединение пришло извне.
	Host       string
	HostStatic bool
	// Path — путь запроса (HTTPPathStatic) или его статическое начало
	// (HTTPPathPrefix); пусто у динамического.
	Path       string
	PathKind   HTTPPathKind
	Confidence domain.Confidence
	Provenance domain.Provenance
}

// httpBinds — локальные переменные метода, за которыми закреплены соединение,
// запрос или строка. Ключ — нормализованное имя переменной.
type httpBinds struct {
	conns    map[string]httpHost
	requests map[string]httpPath
	strs     map[string]httpPath
}

type httpHost struct {
	host   string
	static bool
}

type httpPath struct {
	path string
	kind HTTPPathKind
}

var dynamicPath = httpPath{kind: HTTPPathDynamic}

// httpVerbs — методы HTTPСоединение, отправляющие запрос первым аргументом
// (bsl_syntax, тип HTTPСоединение), и их HTTP-метод.
var httpVerbs = map[string]string{
	"получить": "GET", "get": "GET", "получитьасинх": "GET", "getasync": "GET",
	"получитьзаголовки": "HEAD", "head": "HEAD", "получитьзаголовкиасинх": "HEAD", "headasync": "HEAD",
	"отправитьдляобработки": "POST", "post": "POST", "отправитьдляобработкиасинх": "POST", "postasync": "POST",
	"записать": "PUT", "put": "PUT", "записатьасинх": "PUT", "putasync": "PUT",
	"изменить": "PATCH", "patch": "PATCH", "изменитьасинх": "PATCH", "patchasync": "PATCH",
	"удалить": "DELETE", "delete": "DELETE", "удалитьасинх": "DELETE", "deleteasync": "DELETE",
}

// httpCallMethod — ВызватьHTTPМетод(<HTTPМетод>, <HTTPЗапрос>, ...): метод
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
			p.http.requests[name] = p.classifyPath(i+4, end)
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
		path := httpPath{path: "", kind: HTTPPathStatic}
		if len(args) > 0 {
			path = p.classifyPath(args[0][0], args[0][1])
		}
		p.ensureHTTPBinds()
		p.http.requests[varName] = path
	}
}

// bindHTTPString запоминает переменную, которой присвоена строка или склейка
// со строкой в начале: Адрес = "/base/hs/svc/" + Номер. Присваивание другого
// выражения стирает прежнюю привязку: последнее присваивание в тексте метода
// и есть то, что увидит следующий за ним вызов.
func (p *parser) bindHTTPString(i int) {
	name := domain.NormalizeName(string(p.toks[i].lit))
	end := p.statementEnd(i + 2)
	path := p.classifyPath(i+2, end)
	if path.kind == HTTPPathDynamic {
		if p.http.strs != nil {
			delete(p.http.strs, name)
		}
		return
	}
	p.ensureHTTPBinds()
	p.http.strs[name] = path
}

// collectHTTPVerb разбирает Соединение.Метод(...) и даёт факт, если
// соединение создано в этом методе или запрос известен.
func (p *parser) collectHTTPVerb(i int) {
	methodTok := p.toks[i+2]
	verb, isVerb := httpVerbs[strings.ToLower(string(methodTok.lit))]
	callMethod := isHTTPCallMethod(methodTok.lit)
	if !isVerb && !callMethod {
		return
	}
	conn, connBound := p.http.conns[domain.NormalizeName(string(p.toks[i].lit))]
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
	path, reqKnown := httpPath{}, false
	if reqArg < len(args) {
		path, reqKnown = p.requestArg(args[reqArg][0], args[reqArg][1])
	}
	if !connBound && !reqKnown {
		return // не отличить от Соответствие.Получить(Ключ)
	}
	if !reqKnown {
		path = dynamicPath
	}
	end := methodTok.sp.end
	if closeIdx > 0 && closeIdx < len(p.toks) {
		end = p.toks[closeIdx].sp.end
	}
	p.mod.HTTPCalls = append(p.mod.HTTPCalls, HTTPCall{
		Span:       p.li.Span(p.toks[i].sp.start, end),
		Method:     p.method,
		Verb:       verb,
		Host:       conn.host,
		HostStatic: connBound && conn.static,
		Path:       path.path,
		PathKind:   path.kind,
		Confidence: ConfidenceHTTPCall,
		Provenance: domain.Provenance{Source: domain.SourceHeuristic, File: p.opts.File, Detail: "http-call-" + string(methodTok.lit)},
	})
}

// requestArg — запрос в аргументе метода соединения: переменная, за которой
// закреплён запрос, или прямо Новый HTTPЗапрос(...).
func (p *parser) requestArg(start, end int) (httpPath, bool) {
	if end-start == 1 && p.toks[start].kind == tokIdent {
		r, ok := p.http.requests[domain.NormalizeName(string(p.toks[start].lit))]
		return r, ok
	}
	if end-start >= 3 && eqAny(p.toks[start].lit, "Новый", "New") &&
		eqAny(p.toks[start+1].lit, "HTTPЗапрос", "HTTPRequest") && isPunct(p.toks[start+2], '(') {
		args, _ := p.argRanges(start + 2)
		if len(args) == 0 {
			return httpPath{kind: HTTPPathStatic}, true
		}
		return p.classifyPath(args[0][0], args[0][1]), true
	}
	return httpPath{}, false
}

// classifyHost — сервер соединения: литерал (или переменная со статической
// строкой) либо ничего.
func (p *parser) classifyHost(start, end int) httpHost {
	path := p.classifyPath(start, end)
	if path.kind != HTTPPathStatic || strings.TrimSpace(path.path) == "" {
		return httpHost{}
	}
	return httpHost{host: path.path, static: true}
}

// classifyPath — выражение адреса [start,end): литерал, склейка с литералом
// в начале, СтрШаблон с литералом шаблона или переменная со строкой.
func (p *parser) classifyPath(start, end int) httpPath {
	if start >= end || end > len(p.toks) {
		return dynamicPath
	}
	first := p.toks[start]
	var head httpPath
	next := start + 1
	switch {
	case first.kind == tokString:
		head = httpPath{path: unquoteBSL(first.lit), kind: HTTPPathStatic}
	case first.kind == tokIdent && eqAny(first.lit, "СтрШаблон", "StrTemplate") &&
		next+1 < end && isPunct(p.toks[next], '(') && p.toks[next+1].kind == tokString:
		tmpl := unquoteBSL(p.toks[next+1].lit)
		if k := strings.IndexByte(tmpl, '%'); k >= 0 {
			return httpPath{path: tmpl[:k], kind: HTTPPathPrefix}
		}
		return httpPath{path: tmpl, kind: HTTPPathStatic}
	case first.kind == tokIdent && (start == 0 || !isPunct(p.toks[start-1], '.')):
		if next < end && !isPunct(p.toks[next], '+') {
			return dynamicPath // Структура.Поле, Функция(...)
		}
		s, ok := p.http.strs[domain.NormalizeName(string(first.lit))]
		if !ok {
			return dynamicPath
		}
		head = s
	default:
		return dynamicPath
	}
	if next >= end {
		return head
	}
	if !isPunct(p.toks[next], '+') {
		return dynamicPath
	}
	// Склейка: статическим остаётся только начало, хвост вычисляется. Два
	// литерала подряд ("/a" + "/b") склеиваются в начало целиком.
	path := head.path
	j := next
	for head.kind == HTTPPathStatic && j+1 < end && isPunct(p.toks[j], '+') && p.toks[j+1].kind == tokString {
		path += unquoteBSL(p.toks[j+1].lit)
		j += 2
	}
	if j >= end && head.kind == HTTPPathStatic {
		return httpPath{path: path, kind: HTTPPathStatic}
	}
	return httpPath{path: path, kind: HTTPPathPrefix}
}

// stringLiteral — выражение ровно из одного строкового литерала.
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

// statementEnd — индекс ';' (или конца тела) после start на нулевой глубине
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
		p.http.requests = make(map[string]httpPath)
	}
	if p.http.strs == nil {
		p.http.strs = make(map[string]httpPath)
	}
}

// unquoteBSL снимает кавычки строкового литерала BSL: "" внутри — одна
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
