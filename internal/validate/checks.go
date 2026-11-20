package validate

import (
	"regexp"
	"sort"
	"strings"
)

var (
	reModuleCall = regexp.MustCompile(`(?:^|[^\p{L}\d_.\x{E000}])(` + ident + `)\s*\.\s*(` + ident + `)\s*\(`)
	reIdent      = regexp.MustCompile(`(?:^|[^\p{L}\d_.\x{E000}])(` + ident + `)`)
	reLitArg     = regexp.MustCompile(`^"\x{E000}(\d+)\s*"$`)
	reTplMarker  = regexp.MustCompile(`%(?:%|\((\d+)\)|(\d))`)
)

// attributeKinds are module kinds whose context carries attributes by bare
// name. In the others a name outside the module and the global context is
// nothing.
func attributeKind(k ModuleKind) bool {
	return k == KindUnknown || k == KindObject || k == KindForm
}

// contextSure reports that a bare name missing from ContextNames is not an
// attribute of the module's own context.
func (c *checker) contextSure() bool {
	mc := c.env.Module
	if mc == nil {
		return false
	}
	return !attributeKind(mc.Kind) || mc.ContextComplete
}

// contextCaveat says why a finding about name was softened: the module is not
// known at all, or its attributes were not read.
func (c *checker) contextCaveat(name string) string {
	mc := c.env.Module
	if mc == nil || mc.Kind == KindUnknown {
		return " Модуль черновика не известен: если " + name + " это реквизит объекта или формы, находка ложная; передайте path."
	}
	what := "объекта"
	if mc.Kind == KindForm {
		what = "формы"
	}
	return " Реквизиты " + what + " не прочитаны (нет описания на диске): если " + name + " это реквизит, находка ложная."
}

func (c *checker) inContext(lower string) bool {
	return c.env.Module != nil && c.env.Module.ContextNames[lower]
}

// checkCommonModuleCalls: ОбщийМодуль.Метод( where the module exists and has no
// export method of that name. The configuration compiles the call only against
// the module's export methods, the ones extensions add included.
func (c *checker) checkCommonModuleCalls() []Finding {
	p := c.env.Project
	if p == nil {
		return nil
	}
	var out []Finding
	for _, l := range c.m.lines {
		if l.kind != lineCode {
			continue
		}
		for _, mm := range reModuleCall.FindAllStringSubmatch(l.code, -1) {
			modName, methName := mm[1], mm[2]
			lowerMod := strings.ToLower(modName)
			md := c.m.methodAt(l.no)
			// A name the method writes to is reported by checkAssignments
			// (where the module is visible) or is a plain variable (where it
			// is not); its method calls are not a finding of their own.
			if c.m.isLocal(md, lowerMod) || c.inContext(lowerMod) || (md != nil && md.assigned[lowerMod]) {
				continue
			}
			cm, ok := p.CommonModule(modName)
			if !ok || !cm.Complete {
				continue
			}
			// Where the module is not compiled its name is free: on the
			// client Пользователи = Новый Массив makes Пользователи.Добавить
			// a variable's method.
			if avail := cm.Availability; avail != (Availability{}) && !avail.meets(c.execOf(md)) {
				continue
			}
			lowerMeth := strings.ToLower(methName)
			if c.ownCommonModule(cm.Name) {
				if d := c.m.declared[lowerMeth]; d != nil && d.export {
					continue
				}
			}
			info, has := cm.Methods[lowerMeth]
			if has && info.Export {
				continue
			}
			f := Finding{
				Code:     "UnknownCommonModuleMethod",
				Severity: "high",
				Line:     l.no,
				Text:     strings.TrimSpace(l.raw),
			}
			if has {
				f.Message = "Метод " + cm.Name + "." + info.Name + " есть, но не экспортный: из другого модуля он не вызывается, " +
					"вызов падает при выполнении («Метод объекта не обнаружен»). Компиляция этого не ловит."
				f.Suggestion = "Вызвать экспортный метод модуля (find_api module=" + cm.Name + ") или объявить метод с Экспорт."
			} else {
				f.Message = "В общем модуле " + cm.Name + " нет экспортного метода " + methName +
					" ни в конфигурации, ни в расширениях: вызов падает при выполнении («Метод объекта не обнаружен (" + methName +
					")»). Компиляция этого не ловит."
				f.Suggestion = "Проверить имя по find_api (module=" + cm.Name + ")."
				if near := nearNames(methName, exportNames(cm), 3); len(near) > 0 {
					f.Suggestion = "Похожие экспортные методы: " + strings.Join(near, ", ") + ". Сверить по find_api (module=" + cm.Name + ")."
				}
			}
			if !c.contextSure() {
				f.Severity = "medium"
				f.Message += c.contextCaveat(modName)
			}
			out = append(out, f)
		}
	}
	return out
}

// ownCommonModule: the draft is the text of this common module, so its own
// new export methods count before they reach the disk.
func (c *checker) ownCommonModule(name string) bool {
	mc := c.env.Module
	return mc != nil && mc.Kind == KindCommon && strings.EqualFold(mc.Display, name)
}

func exportNames(cm CommonModule) []string {
	var out []string
	for _, m := range cm.Methods {
		if m.Export {
			out = append(out, m.Name)
		}
	}
	return out
}

// checkAssignments: a statement that writes a name owned by the global
// context — a common module (ИнтеграцияCRM, Пользователи) or a global property
// (Документы, Метаданные) — compiles and fails at run time: «Поле объекта
// недоступно для записи». A parameter or a Перем of the same name is a local
// variable and shadows the global one (the configuration itself has
// parameters named Документы), so those are left alone. In an object module a
// loop counter named as an attribute (Для Номер = 1 По ...) writes the
// attribute of the object.
func (c *checker) checkAssignments() []Finding {
	var out []Finding
	for _, s := range c.m.stmts {
		var name string
		loop := false
		if m := reForEach.FindStringSubmatch(s.text); m != nil {
			name, loop = m[1], true
		} else if m := reForTo.FindStringSubmatch(s.text); m != nil {
			name, loop = m[1], true
		} else if m := reAssign.FindStringSubmatch(s.text); m != nil {
			name = m[1]
		} else {
			continue
		}
		lower := strings.ToLower(name)
		if c.m.isLocal(s.m, lower) || c.m.declared[lower] != nil {
			continue
		}
		raw := strings.TrimSpace(c.m.lines[s.line-1].raw)
		if f, ok := c.globalAssign(name, lower, raw, s.line, c.execOf(s.m)); ok {
			out = append(out, f)
			continue
		}
		if loop {
			if f, ok := c.attributeCounter(name, lower, raw, s.line); ok {
				out = append(out, f)
			}
		}
	}
	out = append(out, c.localsShadowingAttributes()...)
	return out
}

// globalAssign judges a write to a name in the context the method runs in: a
// server-only common module or property is no name on the client, and there
// the same assignment declares a variable.
func (c *checker) globalAssign(name, lower, raw string, line int, ctx Availability) (Finding, bool) {
	if c.inContext(lower) {
		return Finding{}, false
	}
	var owner string
	if p := c.env.Project; p != nil {
		if cm, ok := p.CommonModule(name); ok {
			avail := cm.Availability
			if avail == (Availability{}) {
				avail = Availability{Client: true, Server: true}
			}
			if avail.meets(ctx) {
				owner = "общим модулем " + cm.Name
			}
		}
	}
	if owner == "" && c.env.GlobalProperty != nil {
		if avail, ok := c.env.GlobalProperty(name); ok && avail.meets(ctx) {
			owner = "свойством глобального контекста " + name
		}
	}
	if owner == "" {
		return Finding{}, false
	}
	f := Finding{
		Code:     "AssignToGlobalName",
		Severity: "high",
		Line:     line,
		Text:     raw,
		Message: "Имя " + name + " занято " + owner + ": присваивание ему падает при выполнении " +
			"(«Поле объекта недоступно для записи (" + name + ")»). Ни BSL LS, ни компиляция этого не ловят.",
		Suggestion: "Назвать переменную иначе (например, " + name + "Значение) или объявить её через Перем в процедуре.",
	}
	if !c.contextSure() {
		f.Severity = "medium"
		f.Message += c.contextCaveat(name)
	}
	return f, true
}

func (c *checker) attributeCounter(name, lower, raw string, line int) (Finding, bool) {
	mc := c.env.Module
	if mc == nil || mc.Kind != KindObject {
		return Finding{}, false
	}
	attr, ok := mc.ObjectProps[lower]
	if !ok {
		return Finding{}, false
	}
	if attr == "" {
		attr = name
	}
	return Finding{
		Code:     "LoopCounterIsAttribute",
		Severity: "high",
		Line:     line,
		Text:     raw,
		Message: "Счётчик цикла " + name + " в модуле объекта: это реквизит " + attr +
			" самого объекта, цикл пишет в него (номер, дата, ссылка документа).",
		Suggestion: "Назвать счётчик иначе (Индекс, НомерСтроки).",
	}, true
}

// localsShadowingAttributes: Перем in a method of an object module named as an
// attribute hides the attribute inside the method.
func (c *checker) localsShadowingAttributes() []Finding {
	mc := c.env.Module
	if mc == nil || mc.Kind != KindObject || len(mc.ObjectProps) == 0 {
		return nil
	}
	var out []Finding
	for _, md := range c.m.methods {
		for lower, line := range md.localLines {
			attr, ok := mc.ObjectProps[lower]
			if !ok {
				continue
			}
			if attr == "" {
				attr = localName(c.m.lines[line-1].code, lower)
			}
			out = append(out, Finding{
				Code:     "LocalShadowsAttribute",
				Severity: "medium",
				Line:     line,
				Text:     strings.TrimSpace(c.m.lines[line-1].raw),
				Message: "Локальная переменная " + attr + " в модуле объекта перекрывает реквизит " + attr +
					": внутри процедуры реквизит по имени недоступен.",
				Suggestion: "Назвать переменную иначе.",
			})
		}
	}
	return out
}

// localName finds a name in a line as written.
func localName(code, lower string) string {
	for _, m := range reIdent.FindAllStringSubmatch(code, -1) {
		if strings.ToLower(m[1]) == lower {
			return m[1]
		}
	}
	return lower
}

// checkFormDirectives: in a form module &НаКлиентеНаСервере is not a valid
// directive (only &НаКлиентеНаСервереБезКонтекста is): the method is compiled
// nowhere and its callers fail with «Процедура или функция с указанным именем
// не определена».
func (c *checker) checkFormDirectives() []Finding {
	if c.env.Module == nil || c.env.Module.Kind != KindForm {
		return nil
	}
	var out []Finding
	for _, l := range c.m.lines {
		if l.kind != lineAnnotation {
			continue
		}
		d := reDirectiv.FindStringSubmatch(l.code)
		if d == nil {
			continue
		}
		switch strings.ToLower(d[1]) {
		case "наклиентенасервере", "atclientatserver":
			out = append(out, Finding{
				Code:     "FormDirectiveClientServer",
				Severity: "high",
				Line:     l.no,
				Text:     strings.TrimSpace(l.raw),
				Message: "В модуле формы нет директивы &НаКлиентеНаСервере: процедура не компилируется ни на клиенте, ни на сервере, " +
					"вызовы падают с «Процедура или функция с указанным именем не определена».",
				Suggestion: "&НаКлиентеНаСервереБезКонтекста (без обращения к реквизитам формы) или раздельные &НаКлиенте и &НаСервере.",
			})
		}
	}
	return out
}

// checkUnborrowed: an object module of an extension sees only the attributes
// the extension borrowed. A bare name of a base attribute that was not
// borrowed does not compile («Переменная не определена»); ЭтотОбъект.Имя
// reaches it. An assignment to such a name creates a local variable instead
// and leaves the attribute untouched.
func (c *checker) checkUnborrowed() []Finding {
	mc := c.env.Module
	if mc == nil || mc.Kind != KindObject || len(mc.Unborrowed) == 0 {
		return nil
	}
	var out []Finding
	for _, l := range c.m.lines {
		if l.kind != lineCode {
			continue
		}
		md := c.m.methodAt(l.no)
		if md != nil && md.start == l.no {
			continue // the header: parameters, not uses
		}
		seen := map[string]bool{}
		for _, loc := range reIdent.FindAllStringSubmatchIndex(l.code, -1) {
			name := l.code[loc[2]:loc[3]]
			lower := strings.ToLower(name)
			attr, ok := mc.Unborrowed[lower]
			if !ok || seen[lower] {
				continue
			}
			rest := strings.TrimLeft(l.code[loc[3]:], " \t")
			if strings.HasPrefix(rest, "(") || afterNew(l.code[:loc[2]]) || afterDot(l.code[:loc[2]]) {
				continue
			}
			if c.m.isLocal(md, lower) || c.m.declared[lower] != nil {
				continue
			}
			seen[lower] = true
			f := Finding{
				Code:     "UnborrowedAttribute",
				Severity: "high",
				Line:     l.no,
				Text:     strings.TrimSpace(l.raw),
				Message: "Реквизит " + attr + " не заимствован в расширение " + mc.Extension +
					": по имени модуль его не видит, компиляция падает с «Переменная не определена (" + attr + ")».",
				Suggestion: "ЭтотОбъект." + attr + " или заимствовать реквизит в расширение.",
			}
			if md != nil && md.assigned[lower] {
				f.Severity = "medium"
				f.Message = "Реквизит " + attr + " не заимствован в расширение " + mc.Extension +
					": присваивание " + attr + " = ... создаёт локальную переменную, реквизит объекта не меняется."
			}
			out = append(out, f)
		}
	}
	return out
}

// checkStrTemplate: with a literal template, the number of values must match
// the highest %N: СтрШаблон raises «Недостаточно фактических параметров» or
// «Очень много фактических параметров» at run time.
func (c *checker) checkStrTemplate(rest, raw string, lineNo int) []Finding {
	args, ok := splitArgs(rest)
	if !ok || len(args) == 0 {
		return nil
	}
	m := reLitArg.FindStringSubmatch(strings.TrimSpace(args[0]))
	if m == nil {
		return nil // template is not one literal: nothing provable
	}
	id := atoi(m[1])
	if id < 0 || id >= len(c.m.lits) {
		return nil
	}
	tpl := c.m.lits[id].text
	maxN := 0
	for _, loc := range reTplMarker.FindAllStringSubmatchIndex(tpl, -1) {
		var n int
		switch {
		case loc[2] >= 0:
			n = atoi(tpl[loc[2]:loc[3]])
		case loc[4] >= 0:
			if loc[5] < len(tpl) && tpl[loc[5]] >= '0' && tpl[loc[5]] <= '9' {
				return nil // %10 or %1 then a digit: the reading is ambiguous
			}
			n = atoi(tpl[loc[4]:loc[5]])
		default:
			continue // %%
		}
		if n > maxN {
			maxN = n
		}
	}
	values := len(args) - 1
	if values == maxN {
		return nil
	}
	f := Finding{
		Code:       "StrTemplateArgs",
		Severity:   "high",
		Line:       lineNo,
		Text:       raw,
		Suggestion: "Сверить номера %N в шаблоне с переданными значениями.",
	}
	if values < maxN {
		f.Message = "В шаблоне СтрШаблон есть %" + itoa(maxN) + ", а значений передано " + itoa(values) +
			": при выполнении «Недостаточно фактических параметров»."
	} else {
		f.Message = "В шаблоне СтрШаблон наибольший номер %" + itoa(maxN) + ", а значений передано " + itoa(values) +
			": при выполнении «Очень много фактических параметров»."
	}
	return []Finding{f}
}

var (
	reOrderBy       = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(упорядочить\s+по|order\s+by)(?:$|[^\p{L}\d_])`)
	reOrderEnd      = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])(итоги|totals|автоупорядочивание|autoorder|объединить|union|для\s+изменения|for\s+update|индексировать|index|поместить|into)(?:$|[^\p{L}\d_])`)
	rePresentation  = regexp.MustCompile(`(?i)^(представление|представлениессылки|presentation|refpresentation)\s*\(`)
	rePresentAlias  = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_.])(?:представление|представлениессылки|presentation|refpresentation)\s*\(`)
	reAliasAfter    = regexp.MustCompile(`(?i)^\s*(?:как|as)\s+(` + ident + `)`)
	reOrderDirTrail = regexp.MustCompile(`(?i)\s+(убыв|возр|desc|asc|иерархия|hierarchy)(\s+(убыв|возр|desc|asc))?\s*$`)
)

// checkQueries: a query may not order by ПРЕДСТАВЛЕНИЕ(...) nor by the alias
// of a column computed with it: «Операция не разрешена в предложении
// УПОРЯДОЧИТЬ» at run time. Neither compilation nor BSL LS sees query text.
func (c *checker) checkQueries() []Finding {
	var out []Finding
	for _, lit := range c.m.lits {
		if !reOrderBy.MatchString(lit.text) {
			continue
		}
		offset := 0
		for _, q := range strings.Split(lit.text, ";") {
			out = append(out, c.orderByPresentation(q, lit, offset)...)
			offset += len(q) + 1
		}
	}
	return out
}

// orderByPresentation checks one query of a batch: q starts at byte offset base
// of the literal's text.
func (c *checker) orderByPresentation(q string, lit literal, base int) []Finding {
	aliases := map[string]bool{}
	for _, loc := range rePresentAlias.FindAllStringIndex(q, -1) {
		end, ok := closeParen(q, loc[1])
		if !ok {
			continue
		}
		if a := reAliasAfter.FindStringSubmatch(q[end:]); a != nil {
			aliases[strings.ToLower(a[1])] = true
		}
	}
	var out []Finding
	for _, loc := range reOrderBy.FindAllStringSubmatchIndex(q, -1) {
		clause := q[loc[3]:]
		if e := reOrderEnd.FindStringIndex(clause); e != nil {
			clause = clause[:e[0]]
		}
		if e, ok := closeParenOrEnd(clause); ok {
			clause = clause[:e]
		}
		line := lit.lineOf(base + loc[2])
		for _, item := range splitTop(clause) {
			item = strings.TrimSpace(reOrderDirTrail.ReplaceAllString(strings.TrimSpace(item), ""))
			if item == "" {
				continue
			}
			bad := rePresentation.MatchString(item)
			if !bad && aliases[strings.ToLower(item)] {
				bad = true
			}
			if !bad {
				continue
			}
			out = append(out, Finding{
				Code:     "OrderByPresentation",
				Severity: "high",
				Line:     line,
				Text:     strings.TrimSpace(c.m.lines[min(line, len(c.m.lines))-1].raw),
				Message: "УПОРЯДОЧИТЬ ПО " + item + ": упорядочивать по ПРЕДСТАВЛЕНИЕ() и по псевдониму колонки с ним нельзя, " +
					"запрос падает при выполнении («Операция не разрешена в предложении УПОРЯДОЧИТЬ»).",
				Suggestion: "Упорядочить по ссылке или реквизиту таблицы (Контрагенты.Наименование), проверить запрос execute_query на тестовой базе.",
			})
		}
	}
	return out
}

// closeParen finds the end of a parenthesised expression whose opening
// parenthesis ends at from.
func closeParen(s string, from int) (int, bool) {
	depth := 1
	for i := from; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// closeParenOrEnd cuts an ORDER BY clause of a nested query at the
// parenthesis that closes the nested query.
func closeParenOrEnd(s string) (int, bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// nearNames picks up to limit names close to name: a small edit distance or a
// long common beginning.
func nearNames(name string, pool []string, limit int) []string {
	target := []rune(strings.ToLower(name))
	type cand struct {
		name string
		d    int
	}
	var cs []cand
	for _, p := range pool {
		pr := []rune(strings.ToLower(p))
		d := editDistance(target, pr)
		max := len(target) / 4
		if max < 2 {
			max = 2
		}
		if d > max {
			// ДеньМесяца -> День, ДеньГода: a shared first word is worth naming.
			if pre := commonPrefix(target, pr); pre >= 4 {
				d = max + 1 + absInt(len(pr)-pre) + absInt(len(target)-pre)
			} else {
				continue
			}
		}
		cs = append(cs, cand{p, d})
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].d != cs[j].d {
			return cs[i].d < cs[j].d
		}
		return cs[i].name < cs[j].name
	})
	var out []string
	for _, c := range cs {
		if len(out) == limit {
			break
		}
		out = append(out, c.name)
	}
	return out
}

func commonPrefix(a, b []rune) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func editDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func minInt(v ...int) int {
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
