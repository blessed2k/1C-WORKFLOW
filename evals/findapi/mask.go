package main

import (
	"regexp"
	"strings"
)

// maskedName: чем во фрагменте заменён вызов метода, который модели
// предстоит описать. Имя метода модель видеть не должна: иначе запрос выйдет
// пересказом имени, и набор мерил бы лексический поиск его же словами.
const maskedName = "ИскомыйМетод"

// Границы фрагмента вокруг места вызова.
const (
	// snippetLeadLines: сколько строк описания вызывающей процедуры (её
	// комментарий и директивы компиляции над заголовком) берётся во фрагмент.
	snippetLeadLines = 12
	// snippetMaxBody: процедура длиннее режется до окна вокруг вызова.
	snippetMaxBody = 60
	// snippetBefore/snippetAfter: окно вокруг вызова в длинной процедуре.
	snippetBefore = 22
	snippetAfter  = 14
	// snippetCut: строка на месте вырезанного кода.
	snippetCut = "\t// ..."
)

var (
	reMethodHeader = regexp.MustCompile(`(?i)^\s*(?:асинх\s+|async\s+)?(?:процедура|функция|procedure|function)\s+([\p{L}\p{N}_]+)\s*\(`)
	reMethodEnd    = regexp.MustCompile(`(?i)^\s*(?:конецпроцедуры|конецфункции|endprocedure|endfunction)(?:[^\p{L}\p{N}_]|$)`)
	// reDottedName: идентификатор или цепочка через точку (Модуль.Метод,
	// Справочники.Товары.Метод). \b в regexp Go понимает только ASCII, поэтому
	// граница слова задана самим классом символов.
	//
	// упрощение: пробелы и перенос строки вокруг точки не допускаются, хотя
	// язык их разрешает: в комментарии точка с пробелом это конец предложения
	// («См. Модуль.Метод»). При вызове «Модуль . Метод» имя метода скрывается,
	// а имя модуля остаётся видимым. Путь снятия: разбирать фрагмент лексером
	// BSL и маскировать по токенам, а не по тексту.
	reDottedName = regexp.MustCompile(`[\p{L}\p{N}_]+(?:\.[\p{L}\p{N}_]+)*`)
)

// compound сообщает, составное ли имя: из двух и более слов CamelCase.
// Составное имя это идентификатор, и скрывать его надо везде; имя из одного
// слова (Заполнить, Сообщить) это обычное слово языка.
func compound(name string) bool { return len(words(name)) > 1 }

// maskMethod заменяет в тексте каждое упоминание метода (вызов через любой
// модуль, вызов без модуля, имя в комментарии или строке) на maskedName.
// Вместе с именем уходит и то, через что метод позван: имя модуля чаще всего
// называет предметную область метода не хуже его собственного имени.
//
// Составное имя скрывается и там, где оно стоит частью чужого идентификатора
// (ЗначениеРеквизитаОбъектаПоСсылке): соседний метод с именем-надстройкой
// выдаёт искомое не хуже прямого упоминания.
func maskMethod(text, method string) string {
	var inside *regexp.Regexp
	if compound(method) {
		inside = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(method))
	}
	return reDottedName.ReplaceAllStringFunc(text, func(chain string) string {
		parts := strings.Split(chain, ".")
		for i, p := range parts {
			if strings.EqualFold(p, method) {
				// Всё до имени метода включительно скрывается; то, что стоит
				// после (обращение к полю результата), остаётся.
				return maskedName + strings.Join(append([]string{""}, parts[i+1:]...), ".")
			}
		}
		if inside != nil {
			return inside.ReplaceAllString(chain, maskedName)
		}
		return chain
	})
}

// mentions сообщает, упомянут ли метод в тексте: отдельным идентификатором
// или, для составного имени, частью другого идентификатора.
func mentions(text, method string) bool {
	lower := strings.ToLower(method)
	for _, chain := range reDottedName.FindAllString(text, -1) {
		for _, p := range strings.Split(chain, ".") {
			if strings.EqualFold(p, method) {
				return true
			}
			if compound(method) && strings.Contains(strings.ToLower(p), lower) {
				return true
			}
		}
	}
	return false
}

// callerNamesMethod сообщает, выдаёт ли имя вызывающей процедуры имя метода:
// обёртка с тем же именем, имя-надстройка (ЗначениеРеквизитаОбъектаНаСервере)
// или имя, повторяющее не меньше двух третей слов метода
// (ВерсииИнтерфейса... зовёт ПолучитьВерсииИнтерфейса...). Заголовок процедуры
// во фрагменте остаётся, и по такому заголовку запрос пишется сам собой.
func callerNamesMethod(caller, method string) bool {
	if strings.EqualFold(caller, method) {
		return true
	}
	mw := words(method)
	if len(mw) < 2 {
		return false
	}
	if strings.Contains(strings.ToLower(caller), strings.ToLower(method)) {
		return true
	}
	have := map[string]bool{}
	for _, w := range words(caller) {
		have[w] = true
	}
	shared := 0
	for _, w := range mw {
		if have[w] {
			shared++
		}
	}
	return shared*3 >= len(mw)*2
}

// buildSnippet вырезает из модуля процедуру, в которой стоит вызов метода, и
// скрывает в ней имя метода. siteLine: номер строки вызова, с единицы.
//
// ok=false, когда фрагмент для оценки не годится: вызов стоит вне процедуры
// (тело модуля), имя вызывающей процедуры выдаёт имя метода
// (callerNamesMethod) или на указанной строке метода нет.
func buildSnippet(src string, siteLine int, method string) (snippet string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	site := siteLine - 1
	if site < 0 || site >= len(lines) || !mentions(lines[site], method) {
		return "", false
	}

	header := -1
	for i := site; i >= 0; i-- {
		if i < site && reMethodEnd.MatchString(lines[i]) {
			return "", false
		}
		if m := reMethodHeader.FindStringSubmatch(lines[i]); m != nil {
			if callerNamesMethod(m[1], method) {
				return "", false
			}
			header = i
			break
		}
	}
	if header < 0 || header == site {
		return "", false
	}
	end := -1
	for i := site; i < len(lines); i++ {
		if reMethodEnd.MatchString(lines[i]) {
			end = i
			break
		}
		if i > site && reMethodHeader.MatchString(lines[i]) {
			return "", false
		}
	}
	if end < 0 {
		return "", false
	}

	lead := header
	for lead > 0 && header-lead < snippetLeadLines {
		prev := strings.TrimSpace(lines[lead-1])
		if !strings.HasPrefix(prev, "//") && !strings.HasPrefix(prev, "&") {
			break
		}
		lead--
	}

	var out []string
	out = append(out, lines[lead:header+1]...)
	bodyFrom, bodyTo := header+1, end // [bodyFrom, bodyTo)
	if end-header-1 > snippetMaxBody {
		from, to := max(bodyFrom, site-snippetBefore), min(bodyTo, site+snippetAfter+1)
		if from > bodyFrom {
			out = append(out, snippetCut)
		}
		out = append(out, lines[from:to]...)
		if to < bodyTo {
			out = append(out, snippetCut)
		}
	} else {
		out = append(out, lines[bodyFrom:bodyTo]...)
	}
	out = append(out, lines[end])

	for i, l := range out {
		out[i] = strings.TrimRight(l, " \t\r")
	}
	return maskMethod(strings.Join(out, "\n"), method), true
}

// methodDoc достаёт полный комментарий метода: блок строк «//» над
// объявлением (директивы компиляции между ними пропускаются). declLine:
// номер строки объявления, с единицы; строка за пределами файла даёт пустое
// описание.
func methodDoc(src string, declLine int) string {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	i := declLine - 2
	if i >= len(lines) {
		return ""
	}
	for i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), "&") {
		i--
	}
	var doc []string
	for ; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "//") {
			break
		}
		doc = append(doc, strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(l, "//"), " "), " \t"))
	}
	for l, r := 0, len(doc)-1; l < r; l, r = l+1, r-1 {
		doc[l], doc[r] = doc[r], doc[l]
	}
	return strings.TrimSpace(strings.Join(doc, "\n"))
}
