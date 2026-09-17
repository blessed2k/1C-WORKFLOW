package bsl

import (
	"bytes"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Parse разбирает модуль BSL. Всегда возвращает *Module (возможно, частичный)
// и список диагностик; паника считается дефектом и проверяется fuzz-тестом.
func Parse(src []byte, opts Options) (*Module, []domain.Diagnostic) {
	li := NewLineIndex(src)
	p := &parser{
		lex:  newLexer(src),
		src:  src,
		opts: opts,
		li:   li,
		mod:  &Module{Info: ClassifyModule(opts.File), src: src, lines: li},
	}
	p.run()

	if len(p.lex.diags)+len(p.diags) == 0 {
		return p.mod, nil
	}
	diags := make([]domain.Diagnostic, 0, len(p.lex.diags)+len(p.diags))
	for _, d := range p.lex.diags {
		diags = append(diags, p.diagnostic(d))
	}
	for _, d := range p.diags {
		diags = append(diags, p.diagnostic(d))
	}
	return p.mod, diags
}

type parser struct {
	lex     *lexer
	src     []byte
	opts    Options
	li      *LineIndex
	mod     *Module
	diags   []rawDiag
	toks    []token // все значимые токены (без комментариев)
	open    []int   // стек открытых областей: индексы в regions
	regions []regionRange
	// method — индекс разбираемого метода в Module.Methods либо NoMethod.
	method int
	// binds — локальные переменные метода, за которыми закреплён регистр.
	binds map[string]registerBinding
}

// regionRange — интервал действия одной области препроцессора.
type regionRange struct {
	name   string
	header span // строка #Область Имя
	start  int  // конец заголовка: с этого места область действует
	end    int  // начало #КонецОбласти либо конец файла
	tail   int  // конец #КонецОбласти либо конец файла
	closed bool
}

func (p *parser) errorf(code string, s span, msg string) {
	p.diags = append(p.diags, rawDiag{code: code, msg: msg, sp: s})
}

func (p *parser) diagnostic(d rawDiag) domain.Diagnostic {
	return domain.Diagnostic{
		Code:     d.code,
		Severity: domain.SeverityWarning,
		Message:  d.msg,
		File:     p.opts.File,
		Span:     p.li.Span(d.sp.start, d.sp.end),
	}
}

// run сначала лексирует весь модуль, потом ходит по потоку токенов.
// Разделение держит error recovery простым: восстановление — это переход
// к следующему объявлению в уже готовом потоке, а не откат лексера.
func (p *parser) run() {
	p.method = NoMethod
	for {
		t := p.lex.next()
		if t.kind == tokEOF {
			break
		}
		switch t.kind {
		case tokComment:
			continue
		case tokPreproc:
			p.notePreproc(t)
			continue
		}
		p.toks = append(p.toks, t)
	}
	p.closeRegions()
	p.emitRegions()
	p.walk()
}

var (
	kwRegion      = []string{"#Область", "#Region"}
	kwEndRegion   = []string{"#КонецОбласти", "#EndRegion"}
	kwIf          = []string{"#Если", "#If"}
	kwElsIf       = []string{"#ИначеЕсли", "#ElsIf"}
	kwElse        = []string{"#Иначе", "#Else"}
	kwEndIf       = []string{"#КонецЕсли", "#EndIf"}
	kwInsert      = []string{"#Вставка", "#Insert"}
	kwEndInsert   = []string{"#КонецВставки", "#EndInsert"}
	kwDelete      = []string{"#Удаление", "#Delete"}
	kwEndDelete   = []string{"#КонецУдаления", "#EndDelete"}
	preprocGroups = []struct {
		kind  PreprocKind
		words []string
	}{
		{PreprocRegion, kwRegion},
		{PreprocEndRegion, kwEndRegion},
		{PreprocElsIf, kwElsIf},
		{PreprocEndIf, kwEndIf},
		{PreprocIf, kwIf},
		{PreprocElse, kwElse},
		{PreprocEndInsert, kwEndInsert},
		{PreprocInsert, kwInsert},
		{PreprocEndDelete, kwEndDelete},
		{PreprocDelete, kwDelete},
	}
)

// notePreproc классифицирует строку препроцессора и поддерживает стек областей.
func (p *parser) notePreproc(t token) {
	fields := bytes.Fields(t.lit)
	if len(fields) == 0 {
		p.mod.Preprocs = append(p.mod.Preprocs, Preproc{Kind: PreprocOther, Span: p.li.Span(t.sp.start, t.sp.end)})
		return
	}
	head := fields[0]
	kind := PreprocOther
	for _, g := range preprocGroups {
		if eqAny(head, g.words...) {
			kind = g.kind
			break
		}
	}
	pp := Preproc{Kind: kind, Span: p.li.Span(t.sp.start, t.sp.end)}
	if len(fields) > 1 {
		// Хвост строки без ключевого слова: условие препроцессора или имя области.
		tailStart := t.sp.start + len(head)
		for tailStart < t.sp.end && (p.src[tailStart] == ' ' || p.src[tailStart] == '\t') {
			tailStart++
		}
		tailEnd := t.sp.end
		for tailEnd > tailStart && isSpaceByte(p.src[tailEnd-1]) {
			tailEnd--
		}
		pp.TextSpan = p.li.Span(tailStart, tailEnd)
	}
	p.mod.Preprocs = append(p.mod.Preprocs, pp)

	switch kind {
	case PreprocRegion:
		name := ""
		if len(fields) > 1 {
			name = string(fields[1])
		}
		p.regions = append(p.regions, regionRange{
			name:   name,
			header: t.sp,
			start:  t.sp.end,
			end:    len(p.src),
			tail:   len(p.src),
		})
		p.open = append(p.open, len(p.regions)-1)
	case PreprocEndRegion:
		if len(p.open) == 0 {
			return
		}
		idx := p.open[len(p.open)-1]
		p.regions[idx].end = t.sp.start
		p.regions[idx].tail = t.sp.end
		p.regions[idx].closed = true
		p.open = p.open[:len(p.open)-1]
	}
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// closeRegions сообщает о незакрытых областях: их интервал уже дотянут до конца файла.
func (p *parser) closeRegions() {
	for _, idx := range p.open {
		p.errorf(DiagUnclosedRegion, p.regions[idx].header,
			"область "+p.regions[idx].name+" не закрыта")
	}
	p.open = p.open[:0]
}

func (p *parser) emitRegions() {
	if len(p.regions) == 0 {
		return
	}
	p.mod.Regions = make([]Region, 0, len(p.regions))
	for _, r := range p.regions {
		p.mod.Regions = append(p.mod.Regions, Region{
			Name:       r.name,
			NameNorm:   domain.NormalizeName(r.name),
			Span:       p.li.Span(r.header.start, r.tail),
			HeaderSpan: p.li.Span(r.header.start, r.header.end),
			Closed:     r.closed,
		})
	}
}

// regionAt возвращает индекс самой внутренней области, покрывающей смещение.
// Области ищутся по готовому списку интервалов: стек препроцессора к моменту
// разбора методов уже размотан, и брать имя из него было бы ошибкой.
func (p *parser) regionAt(offset int) int {
	found := NoRegion
	width := -1
	for i, r := range p.regions {
		if offset < r.start || offset >= r.end {
			continue
		}
		if w := r.end - r.start; width < 0 || w < width {
			width, found = w, i
		}
	}
	return found
}

func eqAny(lit []byte, words ...string) bool {
	for _, w := range words {
		if bytes.EqualFold(lit, []byte(w)) {
			return true
		}
	}
	return false
}

func isProcKeyword(t token) bool {
	return t.kind == tokIdent && eqAny(t.lit, "Процедура", "Procedure")
}

func isFuncKeyword(t token) bool {
	return t.kind == tokIdent && eqAny(t.lit, "Функция", "Function")
}

func isEndKeyword(t token) bool {
	return t.kind == tokIdent && eqAny(t.lit, "КонецПроцедуры", "EndProcedure", "КонецФункции", "EndFunction")
}

func isAsyncKeyword(t token) bool {
	return t.kind == tokIdent && eqAny(t.lit, "Асинх", "Async")
}

func isVarKeyword(t token) bool {
	return t.kind == tokIdent && eqAny(t.lit, "Перем", "Var")
}

// keywordsBeforeParen — ключевые слова, за которыми скобка не означает вызов.
var keywordsBeforeParen = []string{
	"Если", "If", "ИначеЕсли", "ElsIf", "Пока", "While", "Для", "For", "Каждого", "Each",
	"Возврат", "Return", "Не", "Not", "И", "And", "Или", "Or", "Новый", "New",
	"Тогда", "Then", "Цикл", "Do", "ВызватьИсключение", "Raise", "Знач", "Val",
	"Экспорт", "Export", "Перем", "Var", "По", "To", "Из", "In",
}

// isDeclarationStart отличает объявление метода от обращения к полю с таким же
// именем: в типовом коде БСП встречается Обработчик.Процедура = "…", и точка
// слева означает свойство, а не начало процедуры.
func (p *parser) isDeclarationStart(i int) bool {
	if !isProcKeyword(p.toks[i]) && !isFuncKeyword(p.toks[i]) {
		return false
	}
	return i == 0 || !isPunct(p.toks[i-1], '.')
}

func (p *parser) walk() {
	i := 0
	for i < len(p.toks) {
		switch {
		case p.isDeclarationStart(i):
			i = p.parseMethod(i)
		case isVarKeyword(p.toks[i]) && (i == 0 || !isPunct(p.toks[i-1], '.')):
			i = p.parseVars(i)
		default:
			p.collectFacts(i)
			i++
		}
	}
}

// parseVars разбирает оператор Перем модуля: Перем А, Б Экспорт;
// Возвращает индекс токена после точки с запятой.
func (p *parser) parseVars(start int) int {
	i := start + 1
	first := len(p.mod.Variables)
	region := p.regionAt(p.toks[start].sp.start)
	end := p.toks[start].sp.end
	for i < len(p.toks) {
		t := p.toks[i]
		if isPunct(t, ';') {
			end = t.sp.end
			i++
			break
		}
		if isPunct(t, ',') {
			i++
			continue
		}
		if t.kind == tokIdent && eqAny(t.lit, "Экспорт", "Export") {
			if n := len(p.mod.Variables); n > first {
				p.mod.Variables[n-1].Export = true
			}
			end = t.sp.end
			i++
			continue
		}
		if t.kind != tokIdent {
			// Не объявление переменных: отдаём токен общему обходу.
			break
		}
		name := string(t.lit)
		p.mod.Variables = append(p.mod.Variables, Variable{
			Name:     name,
			NameNorm: domain.NormalizeName(name),
			NameSpan: p.li.Span(t.sp.start, t.sp.end),
			Region:   region,
		})
		end = t.sp.end
		i++
	}
	if i == start+1 {
		return start + 1
	}
	stmt := p.li.Span(p.toks[start].sp.start, end)
	for j := first; j < len(p.mod.Variables); j++ {
		p.mod.Variables[j].Span = stmt
	}
	return i
}

// parseMethod разбирает объявление метода начиная с индекса ключевого слова
// и возвращает индекс следующего необработанного токена.
func (p *parser) parseMethod(start int) int {
	kwTok := p.toks[start]
	m := Method{Kind: domain.SymbolProcedure, Region: p.regionAt(kwTok.sp.start)}
	if isFuncKeyword(kwTok) {
		m.Kind = domain.SymbolFunction
	}
	declStart := kwTok.sp.start

	// Директивы, аннотации и Асинх стоят перед ключевым словом.
	j := start - 1
	for j >= 0 {
		prev := p.toks[j]
		if isAsyncKeyword(prev) {
			m.Async = true
			declStart = prev.sp.start
			j--
			continue
		}
		if prev.kind == tokPunct && len(prev.lit) == 1 && (prev.lit[0] == ')' || prev.lit[0] == '(') {
			// Хвост аннотации вида &Вместо("Имя") — ищем её начало.
			k := j
			for k >= 0 && p.toks[k].kind != tokDirective {
				k--
			}
			if k < 0 {
				break
			}
			j = k
			continue
		}
		if prev.kind == tokDirective {
			name := string(prev.lit)
			if isExtensionAnnotation(prev.lit) {
				// Обход идёт справа налево, поэтому аннотация встречается в
				// обратном порядке — восстанавливаем порядок исходника.
				arg, hasArg := p.annotationArg(j)
				a := Annotation{Name: name, Arg: arg, HasArg: hasArg}
				m.Annotations = append([]Annotation{a}, m.Annotations...)
			} else if m.Directive == "" {
				m.Directive = name
			}
			declStart = prev.sp.start
			j--
			continue
		}
		if prev.kind == tokString || prev.kind == tokIdent {
			// Аргумент аннотации: пропускаем, если слева действительно директива.
			k := j
			for k >= 0 && p.toks[k].kind != tokDirective {
				if p.toks[k].kind != tokString && p.toks[k].kind != tokIdent && p.toks[k].kind != tokPunct {
					break
				}
				k--
			}
			if k >= 0 && p.toks[k].kind == tokDirective && k >= j-4 {
				j = k
				continue
			}
		}
		break
	}

	i := start + 1
	if i < len(p.toks) && p.toks[i].kind == tokIdent {
		m.Name = string(p.toks[i].lit)
		m.NameNorm = domain.NormalizeName(m.Name)
		m.NameSpan = p.li.Span(p.toks[i].sp.start, p.toks[i].sp.end)
		i++
	} else {
		p.errorf(DiagMethodWithoutName, kwTok.sp, "объявление метода без имени")
	}

	// Список параметров.
	if i < len(p.toks) && isPunct(p.toks[i], '(') {
		i = p.parseParams(i+1, &m)
	} else {
		p.errorf(DiagMethodWithoutParams, kwTok.sp, "объявление метода "+m.Name+" без списка параметров")
	}

	// Экспорт после списка параметров.
	for i < len(p.toks) && p.toks[i].kind == tokIdent && eqAny(p.toks[i].lit, "Экспорт", "Export") {
		m.Export = true
		i++
	}

	bodyStart := len(p.src)
	if i < len(p.toks) {
		bodyStart = p.toks[i].sp.start
	}

	// Метод кладётся в список до разбора тела: факты тела ссылаются на него
	// индексом, и этот индекс должен быть известен заранее.
	p.mod.Methods = append(p.mod.Methods, m)
	idx := len(p.mod.Methods) - 1
	p.method = idx
	p.binds = nil

	// Тело: до закрывающего ключевого слова. Начало следующего объявления —
	// признак того, что закрывающего слова нет; такой метод закрывается там,
	// остальные методы модуля не теряются.
	end := i
	bodyEnd := -1
	for end < len(p.toks) {
		t := p.toks[end]
		if isEndKeyword(t) {
			p.mod.Methods[idx].Complete = true
			p.mod.Methods[idx].Span = p.li.Span(declStart, t.sp.end)
			bodyEnd = t.sp.start
			end++
			break
		}
		if p.isDeclarationStart(end) {
			p.errorf(DiagUnclosedMethod, span{start: declStart, end: t.sp.start},
				"нет закрывающего ключевого слова для метода "+m.Name)
			p.mod.Methods[idx].Span = p.li.Span(declStart, t.sp.start)
			bodyEnd = t.sp.start
			break
		}
		p.collectFacts(end)
		end++
	}
	if bodyEnd < 0 {
		endOff := len(p.src)
		p.errorf(DiagUnclosedMethod, span{start: declStart, end: endOff},
			"модуль обрывается внутри метода "+m.Name)
		p.mod.Methods[idx].Span = p.li.Span(declStart, endOff)
		bodyEnd = endOff
	}
	p.mod.Methods[idx].BodySpan = p.li.Span(bodyStart, bodyEnd)
	p.method = NoMethod
	p.binds = nil
	return end
}

// annotationArg читает аргумент аннотации, стоящей на позиции k: скобки и
// единственный токен в них. Читается вперёд от самой аннотации, а не назад от
// ключевого слова метода: обратный обход директив ищет только начало
// объявления и о содержимом скобок ничего не знает.
//
// hasArg=true означает «скобки были», в том числе у пустых `&Вместо()` и у
// неразобранного аргумента — отказ строить факт перехвата по такому входу
// принимает потребитель (resolve), парсер лишь честно сообщает, что видел.
func (p *parser) annotationArg(k int) (arg string, hasArg bool) {
	if k+1 >= len(p.toks) || !isPunct(p.toks[k+1], '(') {
		return "", false
	}
	if k+2 < len(p.toks) && isPunct(p.toks[k+2], ')') {
		return "", true // `&Вместо()` — скобки есть, аргумента нет.
	}
	if k+3 < len(p.toks) && isPunct(p.toks[k+3], ')') {
		switch t := p.toks[k+2]; t.kind {
		case tokString:
			return unquoteLiteral(t.lit), true
		case tokIdent:
			return string(t.lit), true
		default:
			p.errorf(DiagBadAnnotationArgument, t.sp,
				"аргументом аннотации "+string(p.toks[k].lit)+" стоит не имя метода")
			return "", true
		}
	}
	// Скобка не закрыта либо в ней не один токен: имя цели не выводится.
	p.errorf(DiagBadAnnotationArgument, p.toks[k+1].sp,
		"аргумент аннотации "+string(p.toks[k].lit)+" не разобран")
	return "", true
}

// unquoteLiteral снимает кавычки со строкового литерала и разворачивает
// удвоенную кавычку. Незакрытый литерал лексер уже отметил диагностикой,
// здесь он просто отдаётся без закрывающей кавычки.
func unquoteLiteral(lit []byte) string {
	s := string(lit)
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	return strings.ReplaceAll(s, `""`, `"`)
}

func isExtensionAnnotation(lit []byte) bool {
	return eqAny(lit, "&Перед", "&После", "&Вместо", "&ИзменениеИКонтроль",
		"&Before", "&After", "&Around", "&ChangeAndValidate")
}

func isPunct(t token, c byte) bool {
	return t.kind == tokPunct && len(t.lit) == 1 && t.lit[0] == c
}

// parseParams разбирает список параметров начиная с токена после '('.
// Возвращает индекс токена после ')'.
func (p *parser) parseParams(i int, m *Method) int {
	for i < len(p.toks) {
		if isPunct(p.toks[i], ')') {
			return i + 1
		}
		if isPunct(p.toks[i], ',') {
			i++
			continue
		}
		var par Parameter
		par.Index = len(m.Params)
		parStart := p.toks[i].sp.start
		parEnd := p.toks[i].sp.end
		if p.toks[i].kind == tokIdent && eqAny(p.toks[i].lit, "Знач", "Val") {
			par.ByValue = true
			i++
		}
		if i >= len(p.toks) || p.toks[i].kind != tokIdent {
			// Мусор в списке параметров: пропускаем токен, остальные параметры не теряем.
			if i < len(p.toks) && !isPunct(p.toks[i], ')') {
				p.errorf(DiagBadParameter, p.toks[i].sp,
					"не имя параметра в списке параметров метода "+m.Name)
				i++
			}
			continue
		}
		par.Name = string(p.toks[i].lit)
		par.NameNorm = domain.NormalizeName(par.Name)
		par.NameSpan = p.li.Span(p.toks[i].sp.start, p.toks[i].sp.end)
		parEnd = p.toks[i].sp.end
		i++
		if i < len(p.toks) && isPunct(p.toks[i], '=') {
			i++
			startDef := i
			// Значение по умолчанию — литерал, возможно со знаком.
			if i < len(p.toks) && p.toks[i].kind == tokPunct && len(p.toks[i].lit) == 1 &&
				(p.toks[i].lit[0] == '-' || p.toks[i].lit[0] == '+') {
				i++
			}
			if i < len(p.toks) && p.toks[i].kind != tokPunct {
				i++
			}
			if i > startDef {
				par.HasDefault = true
				par.Default = string(p.src[p.toks[startDef].sp.start:p.toks[i-1].sp.end])
				parEnd = p.toks[i-1].sp.end
			}
		}
		par.Span = p.li.Span(parStart, parEnd)
		m.Params = append(m.Params, par)
	}
	p.errorf(DiagParamListUnclosed, span{start: m.NameSpan.StartByte, end: m.NameSpan.EndByte},
		"список параметров метода "+m.Name+" не закрыт")
	return i
}

// collectFacts распознаёт на позиции i всё, что видно без резолвера:
// вызовы, конструкторы, обращения к менеджерам, тексты запросов, регистры.
func (p *parser) collectFacts(i int) {
	if p.opts.SkipReferences {
		return
	}
	switch p.toks[i].kind {
	case tokIdent:
		p.collectRef(i)
		p.collectManagerRef(i)
		p.collectRegisterAccess(i)
	case tokString:
		p.collectQueryLiteral(i)
	}
}

// collectRef распознаёт вызов по паре «идентификатор + открывающая скобка».
// Строки и комментарии до сюда не доходят: лексер вернул их отдельными токенами,
// а комментарии в поток не попали вовсе.
func (p *parser) collectRef(i int) {
	t := p.toks[i]
	if i+1 >= len(p.toks) || !isPunct(p.toks[i+1], '(') {
		// Новый Тип без скобок — тоже ссылка на тип.
		if i > 0 && p.toks[i-1].kind == tokIdent && eqAny(p.toks[i-1].lit, "Новый", "New") {
			p.mod.References = append(p.mod.References, Reference{
				Span:   p.li.Span(t.sp.start, t.sp.end),
				Kind:   RefNew,
				Method: p.method,
			})
		}
		return
	}
	if eqAny(t.lit, keywordsBeforeParen...) {
		return
	}
	ref := Reference{Span: p.li.Span(t.sp.start, t.sp.end), Kind: RefCall, Method: p.method}
	if i > 0 && p.toks[i-1].kind == tokIdent && eqAny(p.toks[i-1].lit, "Новый", "New") {
		ref.Kind = RefNew
	} else if i >= 2 && isPunct(p.toks[i-1], '.') && p.toks[i-2].kind == tokIdent {
		ref.QualifierSpan = p.li.Span(p.toks[i-2].sp.start, p.toks[i-2].sp.end)
	}
	p.mod.References = append(p.mod.References, ref)
}
