package app

import (
	"context"
	"math/bits"
	"regexp"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Обратная проверка черновика: агент уже написал свою процедуру, и вопрос
// «нет ли готовой» задаётся не до письма (его агент пропускает), а после, по
// готовому тексту. Для процедур и функций, объявленных в черновике, ищутся
// готовые методы программного интерфейса: по словам имени и первой строки
// комментария.
//
// Это подсказка, а не находка. Счёт совпадения слов не отличает «функция
// повторяет готовый метод» от «в именах есть общие слова»: на замере
// (docs/find-api-eval.md, «Обратная проверка черновика») у настоящих повторов
// охват запроса ниже, чем у случайных служебных функций форм (повтор назван
// синонимами, УдалитьДублиВМассиве против СвернутьМассив, а служебная функция
// делит с готовым методом ходовые слова), и любой порог по счёту выбрасывал
// как раз синонимы. Поэтому названное надо читать, а не принимать.

const (
	// apiDraftMethodsLimit: потолок методов черновика, по которым идёт поиск.
	// Модуль длиннее проверяется по первым, ответ об этом говорит.
	apiDraftMethodsLimit = 30
	// apiDraftLibraryCandidates, apiDraftOtherCandidates: сколько готовых
	// методов секции называется на один метод черновика.
	apiDraftLibraryCandidates = 3
	apiDraftOtherCandidates   = 2
	// apiDraftScanDepth: сколько первых действующих методов общего
	// ранжирования (обе секции вместе, не порознь) просматривается в поиске
	// кандидатов на один метод черновика. Глубина держит шум ценой полноты:
	// метод библиотеки, первый в своей секции, но одиннадцатый в общем списке,
	// назван не будет. Числа для глубин: docs/find-api-eval.md, «Обратная
	// проверка черновика».
	apiDraftScanDepth = 10
	// apiDraftSummaryRunes: до скольких знаков режется назначение кандидата.
	apiDraftSummaryRunes = 160
)

// reDraftMethodHead: объявление процедуры или функции: имя и параметры до
// закрывающей скобки на той же строке. Директива компиляции и аннотация
// могут стоять на той же строке перед ключевым словом.
//
// упрощение: объявления ищутся регулярным выражением по строкам, а не
// разбором модуля (parse/bsl): объявление с именем на следующей строке не
// находится, параметры читаются только с первой строки. Путь снятия: разбор
// тем же парсером, что строит индекс (он отдаёт аннотации и директивы).
var reDraftMethodHead = regexp.MustCompile(
	`(?i)^\s*((?:&\S+\s+)*)(?:асинх\s+|async\s+)?(?:процедура|функция|procedure|function)\s+([\p{L}_][\p{L}\p{N}_]*)\s*\(([^)]*)`)

// reDraftAnnotation: имя аннотации или директивы в строке, начинающейся с &.
var reDraftAnnotation = regexp.MustCompile(`&\s*([\p{L}_]+)`)

// apiDraftInterceptAnnotations: аннотации перехвата метода в расширении.
// Перехватчик пишут потому, что нужно вмешаться в чужой метод, а не потому,
// что не нашли готового.
var apiDraftInterceptAnnotations = map[string]bool{
	"перед": true, "после": true, "вместо": true, "изменениеиконтроль": true,
	"before": true, "after": true, "around": true, "changeandvalidate": true,
}

// apiDraftEventWords: с этого слова начинается имя обработчика события
// объекта или формы. Сравнивается слово целиком, а не начало строки:
// ПриведениеТипа и ПередатьДанные обработчиками не являются.
var apiDraftEventWords = map[string]bool{
	"при": true, "перед": true, "после": true, "обработка": true,
	"on": true, "before": true, "after": true,
}

// draftMethod: процедура или функция, объявленная в черновике.
type draftMethod struct {
	name string
	// line: строка объявления, с единицы.
	line int
	// comment: первая строка комментария над объявлением.
	comment string
	// params: текст параметров с первой строки объявления.
	params string
	// intercept: над объявлением стоит аннотация перехвата расширения.
	intercept bool
}

// ReadyMethodItem: метод черновика и готовые методы программного интерфейса,
// близкие ему по словам.
type ReadyMethodItem struct {
	// Name: имя процедуры или функции черновика; Line: строка её объявления.
	Name string `json:"name"`
	Line int    `json:"line"`
	// Candidates: готовые методы, лучшие первыми: сначала методы библиотеки,
	// затем прикладные.
	Candidates []APIBriefItem `json:"candidates"`
}

// ReadyMethodsInput: вход обратной проверки. Module: модуль, которому
// принадлежит черновик (имя, как у find_api): его собственные методы
// кандидатами не называются, иначе экспортный метод, уже записанный в файл и
// попавший в индекс, находил бы сам себя.
type ReadyMethodsInput struct {
	Code   string
	Module string
}

// ReadyMethodsReport: итог обратной проверки (items[0]).
type ReadyMethodsReport struct {
	// Declared: сколько процедур и функций объявлено в черновике.
	Declared int `json:"declared"`
	// Skipped: сколько из них не проверялось: обработчики событий,
	// перехватчики расширений, обработчики команд, имена из одного слова без
	// комментария.
	Skipped int `json:"skipped"`
	// Checked: по скольким шёл поиск; NotChecked: сколько не поместилось в
	// потолок apiDraftMethodsLimit.
	Checked    int `json:"checked"`
	NotChecked int `json:"notChecked,omitempty"`
	// Methods: методы черновика, для которых названы готовые методы. Метод,
	// которого здесь нет, ничего не доказывает: поиск лексический.
	Methods []ReadyMethodItem `json:"methods"`
}

// draftMethods находит в тексте объявления процедур и функций с первой
// строкой комментария над каждым.
func draftMethods(code string) []draftMethod {
	lines := strings.Split(code, "\n")
	var out []draftMethod
	for i, line := range lines {
		m := reDraftMethodHead.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		d := draftMethod{name: m[2], line: i + 1, params: strings.TrimSpace(m[3])}
		d.comment, d.intercept = draftHeader(lines, i)
		d.intercept = d.intercept || draftHasIntercept(m[1])
		out = append(out, d)
	}
	return out
}

// draftHasIntercept сообщает, есть ли в строке аннотация перехвата.
func draftHasIntercept(line string) bool {
	for _, m := range reDraftAnnotation.FindAllStringSubmatch(line, -1) {
		if apiDraftInterceptAnnotations[domain.NormalizeName(m[1])] {
			return true
		}
	}
	return false
}

// draftHeader читает то, что стоит вплотную над объявлением: директивы и
// аннотации (среди них ищется аннотация перехвата), над ними комментарий, из
// которого берётся первая содержательная строка.
//
// упрощение: правило первой строки своё, а не то, которым индекс наполняет
// symbol.doc_first_line (internal/index, docComment): пустая строка между
// комментарием и объявлением комментарий отрывает. Маркеры авторства
// («++», «--») пропускаются. Путь снятия: вынести правило индекса в общий
// пакет и звать его здесь.
func draftHeader(lines []string, head int) (comment string, intercept bool) {
	end := head
	for end > 0 && strings.HasPrefix(strings.TrimSpace(lines[end-1]), "&") {
		end--
		intercept = intercept || draftHasIntercept(lines[end])
	}
	start := end
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "//") {
		start--
	}
	for _, l := range lines[start:end] {
		text := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "/"))
		if text == "" || strings.HasPrefix(text, "++") || strings.HasPrefix(text, "--") {
			continue
		}
		return text, intercept
	}
	return "", intercept
}

// draftIsHandler сообщает, что метод черновика написан по обязанности, а не
// вместо готового: обработчик события объекта или формы, обработчик события
// элемента формы (ТоварыПриИзменении), обработчик оповещения
// (ВыборФайлаЗавершение), обработчик команды формы, подключаемый обработчик,
// перехватчик расширения.
//
// упрощение: обработчик опознаётся по имени и параметрам, а не по привязке в
// форме: черновик приходит текстом, формы при нём нет. Обработчик с
// произвольным именем (команда формы с параметрами кроме Команда) пройдёт
// как обычная процедура и получит лишние подсказки.
func draftIsHandler(d draftMethod) bool {
	if d.intercept || strings.HasPrefix(strings.ToLower(d.name), "подключаемый_") {
		return true
	}
	if strings.EqualFold(d.params, "Команда") || strings.EqualFold(d.params, "Command") {
		return true
	}
	words := apiWords(d.name)
	if len(words) == 0 {
		return false
	}
	if apiDraftEventWords[words[0]] || words[len(words)-1] == "завершение" {
		return true
	}
	for _, w := range words[1:] {
		if w == "при" {
			return true
		}
	}
	return false
}

// draftCandidates ищет готовые методы для метода черновика. Просматриваются
// первые apiDraftScanDepth действующих методов общего ранжирования; методы
// модуля skipOwner (нижний регистр) пропускаются. found=false: в имени и
// комментарии меньше двух значимых слов, по одному слову готовый метод не
// ищется.
func (ix *apiIndex) draftCandidates(d draftMethod, skipOwner string) (bsp, other []int32, found bool) {
	nameTerms := apiQueryTerms(d.name)
	terms := apiQueryTerms(d.name + " " + d.comment)
	if len(nameTerms) == 0 || len(terms) < 2 {
		return nil, nil, false
	}
	// Слова имени стоят в запросе первыми: apiQueryTerms сохраняет порядок.
	nameMask := uint32(1)<<uint(len(nameTerms)) - 1
	scanned := 0
	for _, r := range ix.search(terms) {
		im := ix.methods[r.method]
		// Устаревший метод автору черновика не предлагается и в глубину
		// просмотра не идёт; свой модуль тоже.
		if im.deprecated || (skipOwner != "" && strings.ToLower(im.owner) == skipOwner) {
			continue
		}
		switch {
		case im.library:
			if len(bsp) < apiDraftLibraryCandidates && apiDraftAcceptsLibrary(r) {
				bsp = append(bsp, r.method)
			}
		case len(other) < apiDraftOtherCandidates && apiDraftAcceptsOther(r, nameMask, len(nameTerms)):
			other = append(other, r.method)
		}
		if scanned++; scanned == apiDraftScanDepth {
			break
		}
	}
	return bsp, other, true
}

// apiDraftAcceptsLibrary: называть ли автору черновика метод библиотеки: у
// метода должно найтись не меньше двух слов запроса. Одно слово чаще всего
// совпадает случайно, со словом из имени модуля.
func apiDraftAcceptsLibrary(r apiRanked) bool {
	return r.matched >= 2
}

// apiDraftAcceptsOther: называть ли автору черновика прикладной метод.
// Прикладных методов вдесятеро больше библиотечных, и почти у каждой
// служебной функции находится прикладной метод с двумя общими словами.
// Поэтому имена должны быть близки с обеих сторон: в имени метода не меньше
// двух слов запроса и не меньше двух третей его слов покрыты запросом, а из
// слов имени функции черновика (именно имени, не комментария) в имени метода
// стоит не меньше половины.
func apiDraftAcceptsOther(r apiRanked, nameMask uint32, nameTerms int) bool {
	inName := bits.OnesCount32(r.nameMask)
	fromDraftName := bits.OnesCount32(r.nameMask & nameMask)
	return inName >= 2 && r.nameFit >= 2.0/3 && 2*fromDraftName >= nameTerms
}

// ReadyMethods называет для процедур и функций черновика готовые методы
// программного интерфейса, близкие им по словам имени и комментария: до
// apiDraftLibraryCandidates методов библиотеки и до apiDraftOtherCandidates
// прикладных с близким именем.
func (s *APIService) ReadyMethods(ctx context.Context, in ReadyMethodsInput) (Response[ReadyMethodsReport], error) {
	report := ReadyMethodsReport{Methods: []ReadyMethodItem{}}
	var drafts []draftMethod
	for _, d := range draftMethods(in.Code) {
		report.Declared++
		if draftIsHandler(d) {
			report.Skipped++
			continue
		}
		drafts = append(drafts, d)
	}
	// Искать нечего: проект и индекс слов ради пустого ответа не открываются.
	if len(drafts) == 0 {
		return Response[ReadyMethodsReport]{Items: []ReadyMethodsReport{report}, TotalCount: 1}, nil
	}
	if len(drafts) > apiDraftMethodsLimit {
		report.NotChecked = len(drafts) - apiDraftMethodsLimit
		drafts = drafts[:apiDraftMethodsLimit]
	}

	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[ReadyMethodsReport]{}, err
	}
	skipOwner := strings.ToLower(apiOwnerName(strings.TrimSpace(in.Module)))
	type txResult struct {
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
		ix, ierr := s.indexes.get(op.Entry.ID, tx, gen)
		if ierr != nil {
			return out, ierr
		}
		out.warn = append([]Warning(nil), ix.warn...)
		for _, d := range drafts {
			bsp, other, found := ix.draftCandidates(d, skipOwner)
			if !found {
				report.Skipped++
				continue
			}
			report.Checked++
			item := ReadyMethodItem{Name: d.name, Line: d.line}
			for _, m := range append(bsp, other...) {
				im := ix.methods[m]
				item.Candidates = append(item.Candidates, APIBriefItem{
					Call: im.call, Summary: apiCutRunes(im.row.DocFirstLine, apiDraftSummaryRunes), UID: im.row.UID,
				})
			}
			if len(item.Candidates) > 0 {
				report.Methods = append(report.Methods, item)
			}
		}
		return out, nil
	})
	if err != nil {
		return Response[ReadyMethodsReport]{}, err
	}
	resp := Response[ReadyMethodsReport]{Generation: res.gen, Items: []ReadyMethodsReport{report}, TotalCount: 1, Warnings: res.warn}
	return withSnapshot(resp, snap), nil
}

// Готовые методы по тексту задачи: блок readyMethods ответа
// get_context_for_task.
const (
	// APITaskLibraryLimit, APITaskOtherLimit: сколько методов секции
	// называется по тексту задачи.
	APITaskLibraryLimit = 5
	APITaskOtherLimit   = 3
	// apiTaskScanDepth: сколько первых методов ранжирования просматривается.
	apiTaskScanDepth = 100
	// apiTaskSummaryRunes: до скольких знаков режется назначение метода.
	apiTaskSummaryRunes = 120
)

// APITaskMethod: готовый метод, близкий задаче по словам. Section: "bsp" или
// "other".
type APITaskMethod struct {
	Section string `json:"section"`
	Call    string `json:"call"`
	Summary string `json:"summary,omitempty"`
	UID     string `json:"uid"`
}

// ReadyForTask называет готовые методы программного интерфейса, близкие
// тексту задачи: действующие методы, у которых не меньше двух слов задачи
// стоят в имени, имени модуля, первой строке описания или карточке. Задача
// уровня постановки длинна, и метод, совпавший одним словом или словами в
// описании параметров, к ней почти никогда не относится. Пустой ответ не
// ошибка: в задаче нет слов, по которым что-то нашлось.
func (s *APIService) ReadyForTask(ctx context.Context, task string) (Response[APITaskMethod], error) {
	terms := apiQueryTerms(task)
	if len(terms) < 2 {
		return Response[APITaskMethod]{Items: []APITaskMethod{}}, nil
	}
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[APITaskMethod]{}, err
	}
	type txResult struct {
		items []APITaskMethod
		gen   domain.Generation
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		out := txResult{items: []APITaskMethod{}}
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		ix, ierr := s.indexes.get(op.Entry.ID, tx, gen)
		if ierr != nil {
			return out, ierr
		}
		var bsp, other []APITaskMethod
		for i, r := range ix.search(terms) {
			if i == apiTaskScanDepth || (len(bsp) == APITaskLibraryLimit && len(other) == APITaskOtherLimit) {
				break
			}
			im := ix.methods[r.method]
			if im.deprecated || r.strong < 2 {
				continue
			}
			item := APITaskMethod{Call: im.call, Summary: apiCutRunes(im.row.DocFirstLine, apiTaskSummaryRunes), UID: im.row.UID}
			switch {
			case im.library && len(bsp) < APITaskLibraryLimit:
				item.Section = "bsp"
				bsp = append(bsp, item)
			case !im.library && len(other) < APITaskOtherLimit:
				item.Section = "other"
				other = append(other, item)
			}
		}
		out.items = append(append(out.items, bsp...), other...)
		return out, nil
	})
	if err != nil {
		return Response[APITaskMethod]{}, err
	}
	resp := Response[APITaskMethod]{Generation: res.gen, Items: res.items, TotalCount: len(res.items)}
	return withSnapshot(resp, snap), nil
}

// apiCutRunes режет строку до limit знаков, помечая обрезку многоточием.
func apiCutRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
