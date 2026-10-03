package app

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

const (
	defaultAPILimit = 10
	maxAPILimit     = 50
	// apiMoreCount: сколько методов секции идёт коротким списком вслед за
	// полными описаниями. Читает выдачу модель, и прочесть ещё десять строк ей
	// нетрудно; на эталоне нужный метод стоит на местах с 11 по 20 примерно у
	// каждого шестнадцатого запроса, и без этого списка агент его не увидел бы.
	apiMoreCount = 10
	// apiSubsystemFilesLimit: потолок объявлений подсистем на один вызов.
	// Защита от зацикленного или испорченного дерева, не настройка: у БСП
	// вложенных подсистем десятки.
	apiSubsystemFilesLimit = 2000
)

// Виды модулей (module.kind), чьи экспортные методы зовут снаружи по имени
// модуля: общий модуль и модуль менеджера объекта.
var apiModuleKinds = []string{string(bsl.ModuleCommon), string(bsl.ModuleManager)}

// apiTopRegions: верхняя область, которой стандарт разметки модуля отделяет
// программный интерфейс от служебного. Второе имя из английского варианта
// встроенного языка.
var apiTopRegions = []string{"ПрограммныйИнтерфейс", "Public"}

// apiDeprecatedRegion: область, куда стандарт складывает устаревшие методы
// программного интерфейса.
var apiDeprecatedRegion = domain.NormalizeName("УстаревшиеПроцедурыИФункции")

// apiOverridableMarkers: общий модуль с таким словом в имени — точка
// расширения, а не готовая функция. Его методы реализуют, а не вызывают, и
// отдаёт их bsp_extension_points (те же два написания).
var apiOverridableMarkers = []string{"переопределяемый", "overridable"}

// Библиотека стандартных подсистем в выгрузке: подсистема с её составом и
// метод, в котором записана версия.
const (
	apiLibrarySubsystem    = "СтандартныеПодсистемы"
	apiLibraryUpdateModule = "ОбновлениеИнформационнойБазыБСП"
	apiLibraryUpdateMethod = "ПриДобавленииПодсистемы"
)

// reAPILibraryVersion: версия библиотеки, заданная литералом в методе
// описания подсистемы. Вычисляемую версию (вызов функции) не достаёт.
var reAPILibraryVersion = regexp.MustCompile(`Описание\.Версия\s*=\s*"([^"]+)"`)

// FindAPIInput: вход find_api. Режимов три:
//
//   - Query: поиск метода по описанию задачи (с Module: только в этом модуле);
//   - Module без Query: весь программный интерфейс модуля списком;
//   - ни Query, ни Module: карта библиотеки (подсистемы и их модули).
type FindAPIInput struct {
	Query  string
	Module string
	Limit  int
	// Returns, Accepts: отбор по типу возвращаемого значения и по типу
	// параметра, как они записаны в комментарии метода (ТаблицаЗначений,
	// ДокументСсылка). Уточняют поиск и список модуля; сами по себе, без
	// Query и Module, не ищут.
	Returns string
	Accepts string
}

// APIMapSubsystem: подсистема библиотеки на карте: какие её модули несут
// программный интерфейс.
type APIMapSubsystem struct {
	// Name: имя подсистемы от подсистемы библиотеки (БазоваяФункциональность,
	// РаботаСФайлами); у самой подсистемы библиотеки её имя.
	Name string `json:"name"`
	// Title: представление подсистемы («Работа с файлами»).
	Title   string         `json:"title,omitempty"`
	Modules []APIMapModule `json:"modules"`
}

// APIMapModule: модуль на карте библиотеки. Methods: сколько у него
// действующих методов программного интерфейса; устаревшие в счёт не идут, а в
// списке модуля (MethodsTotal) они есть, поэтому там число может быть больше.
type APIMapModule struct {
	Name    string `json:"name"`
	Methods int    `json:"methods"`
}

// APIMethodItem: один готовый метод программного интерфейса.
type APIMethodItem struct {
	// Call: выражение вызова без скобок: "ОбщегоНазначения.ЗначениеРеквизитаОбъекта",
	// "Справочники.Номенклатура.Метод"; у глобального модуля одно имя метода.
	Call      string `json:"call"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
	Summary   string `json:"summary,omitempty"`
	// Returns: тип возвращаемого значения из комментария метода («Массив из
	// Строка», «Структура, Неопределено»): в сигнатуре 1С типов нет. Пусто:
	// в комментарии тип не записан (или это процедура).
	Returns string `json:"returns,omitempty"`
	// Context: где метод доступен: сервер, клиент, клиент-сервер, вызов сервера.
	Context string `json:"context,omitempty"`
	// Calls: сколько раз метод зовут из других модулей выгрузки (у метода
	// библиотеки в счёт идут и вызовы из самой библиотеки). Поля нет: таких
	// вызовов индекс не знает; это не значит «не используется», метод могут
	// звать из расширений вне выгрузки или по имени в строке.
	Calls int `json:"calls,omitempty"`
	// Example: как метод зовут на деле: оператор одного вызова из другого
	// модуля (до четырёх строк). Показывает порядок и вид аргументов.
	Example string `json:"example,omitempty"`
	// Deprecated: метод лежит в области устаревших; замену называет Summary.
	Deprecated bool               `json:"deprecated,omitempty"`
	Module     string             `json:"module"`
	Component  domain.ComponentID `json:"component"`
	Line       int                `json:"line"`
	UID        string             `json:"uid"`
	// Doc: полный комментарий метода. Заполняется только в каталоге
	// (APIService.Catalog): в ответе поиска его нет, его отдаёт get_symbol.
	Doc string `json:"doc,omitempty"`
}

// APIBriefItem: метод программного интерфейса одной строкой: вызов и
// назначение. Сигнатуру, параметры и полный комментарий отдаёт get_symbol по
// uid.
type APIBriefItem struct {
	Call       string `json:"call"`
	Summary    string `json:"summary,omitempty"`
	Deprecated bool   `json:"deprecated,omitempty"`
	// Returns, Calls: тип результата из комментария и число вызовов из
	// других модулей (см. APIMethodItem).
	Returns string `json:"returns,omitempty"`
	Calls   int    `json:"calls,omitempty"`
	UID     string `json:"uid"`
}

// APISearchItem: items[0] ответа find_api: две независимо ранжированные
// секции. Секций две, потому что в общем списке методы библиотеки тонут среди
// прикладных с похожими словами в именах.
type APISearchItem struct {
	Query string `json:"query"`
	// BSPVersion: версия библиотеки стандартных подсистем из выгрузки.
	BSPVersion string `json:"bspVersion,omitempty"`
	// BSP: методы общих модулей и модулей менеджеров объектов, входящих в
	// подсистему СтандартныеПодсистемы (со всеми вложенными).
	BSP []APIMethodItem `json:"bsp"`
	// BSPMore: следующие по рангу методы библиотеки коротким списком.
	BSPMore []APIBriefItem `json:"bspMore,omitempty"`
	// Other: остальные общие модули и модули менеджеров конфигурации.
	Other     []APIMethodItem `json:"other"`
	OtherMore []APIBriefItem  `json:"otherMore,omitempty"`
	// BSPMatched/OtherMatched: сколько методов секции совпало с запросом всего.
	BSPMatched   int `json:"bspMatched"`
	OtherMatched int `json:"otherMatched"`
	// Module: модуль, которым ограничен ответ (вход module).
	Module string `json:"module,omitempty"`
	// Methods: весь программный интерфейс модуля в порядке объявления (режим
	// без query); MethodsTotal: сколько их всего.
	Methods      []APIBriefItem `json:"methods,omitempty"`
	MethodsTotal int            `json:"methodsTotal,omitempty"`
	// Map: карта библиотеки (режим без query и без module).
	Map []APIMapSubsystem `json:"map,omitempty"`
	// Cards: у скольких методов программного интерфейса есть карточка задач.
	// Ноль: карточки не подключены или не подходят этой конфигурации, поиск
	// идёт только по имени и комментарию.
	Cards int    `json:"cards"`
	Note  string `json:"note"`
}

// apiModuleListLimit: потолок списка методов одного модуля. Самые большие
// модули программного интерфейса несут около двухсот методов.
const apiModuleListLimit = 300

const apiMapNote = "Карта библиотеки: подсистемы и их модули с числом методов программного интерфейса. " +
	"Весь интерфейс модуля: find_api с module. Поиск по описанию задачи: find_api с query."

const apiNoLibraryMapNote = "Библиотеки стандартных подсистем в выгрузке нет, карты нет. " +
	"Весь интерфейс модуля: find_api с module (имя общего модуля или Справочники.Имя). Поиск по описанию задачи: find_api с query."

const apiModuleNote = "Весь программный интерфейс модуля в порядке объявления: вызов и назначение одной строкой. " +
	"Сигнатуру, параметры и полный комментарий (поле doc) даёт get_symbol по uid; поиск внутри модуля: тот же вызов с query."

const apiSearchNote = "Поиск лексический: слово запроса ищется в имени метода, имени модуля и комментарии метода, словоформы сводятся (удаление = удалить), синонимы не сводятся. " +
	"Списки bspMore и otherMore продолжают секции: вызов и назначение одной строкой. Подходит метод оттуда: get_symbol по uid даст сигнатуру, параметры и полный комментарий (поле doc). " +
	"Нужного нет: повторите запрос, дописав синонимы и термины самой конфигурации (не «сохранить пароль», а «записать данные безопасное хранилище»)."

// APIService: сервис за find_api.
type APIService struct {
	projects *Projects
	// indexes: индексы слов по проектам, общие для всех сервисов одного
	// резолвера (Projects.apiIndexes).
	indexes *apiIndexCache
}

// NewAPIService строит сервис поверх общего резолвера проектов.
func NewAPIService(p *Projects) *APIService {
	return &APIService{projects: p, indexes: &p.apiIndexes}
}

// apiCandidate: метод с выведенными выражением вызова и контекстом.
type apiCandidate struct {
	row         store.ExportedMethodRow
	call        string
	execContext string
	deprecated  bool
}

// FindAPI ищет по описанию задачи готовые экспортные методы программного
// интерфейса: общих модулей и модулей менеджеров.
func (s *APIService) FindAPI(ctx context.Context, in FindAPIInput) (Response[APISearchItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[APISearchItem]{}, err
	}
	query, module := strings.TrimSpace(in.Query), strings.TrimSpace(in.Module)
	terms := apiQueryTerms(query)
	if query != "" && len(terms) == 0 {
		return Response[APISearchItem]{}, NewError(CodeInvalidArgument,
			"в query find_api нет ни одного значимого слова: слова короче трёх букв и служебные (или, для, при) запросом не считаются",
			"опишите задачу словами, например: разбить строку по разделителю").WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultAPILimit, maxAPILimit)
	returns, accepts := strings.TrimSpace(in.Returns), strings.TrimSpace(in.Accepts)
	if (returns != "" || accepts != "") && query == "" && module == "" {
		return Response[APISearchItem]{}, NewError(CodeInvalidArgument,
			"returns и accepts уточняют поиск find_api, сами по себе они не ищут",
			"добавьте query (что должен делать метод) или module (в каком модуле искать)").WithProject(op.Entry.ID)
	}
	// typed: подходит ли метод под отбор по типам.
	typed := func(m apiIndexMethod) bool {
		return apiTypesMatch(m.returns, returns) && apiTypesMatch(m.accepts, accepts)
	}

	type txResult struct {
		item APISearchItem
		gen  domain.Generation
		warn []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		// Секции пусты, а не отсутствуют, в любом режиме: в JSON это [], не null.
		out := txResult{item: APISearchItem{Query: query, Note: apiSearchNote, BSP: []APIMethodItem{}, Other: []APIMethodItem{}}}
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen

		ix, ierr := s.indexes.get(op.Entry.ID, tx, gen)
		if ierr != nil {
			return out, ierr
		}
		out.item.BSPVersion, out.item.Cards = ix.version, ix.cards
		out.warn = append([]Warning(nil), ix.warn...)

		// Без запроса и без модуля: карта библиотеки.
		if query == "" && module == "" {
			out.item.Map, out.item.Note = ix.libraryMap, apiMapNote
			if len(ix.libraryMap) == 0 {
				out.item.Note = apiNoLibraryMapNote
			}
			return out, nil
		}
		var inModule map[int32]bool
		if module != "" {
			methods := ix.byOwner[strings.ToLower(apiOwnerName(module))]
			if len(methods) == 0 {
				// Причин три, и по ответу их не различить: модуля нет, это
				// переопределяемый модуль, либо у модуля нет экспортных
				// методов в области программного интерфейса.
				return out, NotFoundError("модуль с методами программного интерфейса", module, ix.owners()).
					WithProject(op.Entry.ID).WithGeneration(gen)
			}
			out.item.Module = ix.methods[methods[0]].owner
			// Без запроса: весь интерфейс модуля списком.
			if query == "" {
				all := len(methods)
				if returns != "" || accepts != "" {
					var fit []int32
					for _, m := range methods {
						if typed(ix.methods[m]) {
							fit = append(fit, m)
						}
					}
					methods = fit
				}
				out.item.Note, out.item.MethodsTotal = apiModuleNote, len(methods)
				if len(methods) > apiModuleListLimit {
					out.item.Note = "Показаны первые " + strconv.Itoa(apiModuleListLimit) + " методов из " + strconv.Itoa(len(methods)) +
						": остальные ищите тем же вызовом с query. " + apiModuleNote
				}
				if dropped := all - len(methods); dropped > 0 {
					out.item.Note = apiTypeFilterNote(dropped) + " " + out.item.Note
				}
				out.item.Methods = []APIBriefItem{}
				for _, m := range methods[:min(len(methods), apiModuleListLimit)] {
					brief, berr := apiBriefItem(tx, ix.methods[m])
					if berr != nil {
						return out, berr
					}
					out.item.Methods = append(out.item.Methods, brief)
				}
				return out, nil
			}
			inModule = make(map[int32]bool, len(methods))
			for _, m := range methods {
				inModule[m] = true
			}
		}

		var bsp, other []apiRanked
		dropped := 0
		for _, r := range ix.search(terms) {
			if inModule != nil && !inModule[r.method] {
				continue
			}
			if !typed(ix.methods[r.method]) {
				dropped++
				continue
			}
			if ix.methods[r.method].library {
				bsp = append(bsp, r)
			} else {
				other = append(other, r)
			}
		}
		out.item.BSPMatched, out.item.OtherMatched = len(bsp), len(other)
		if dropped > 0 {
			out.item.Note = apiTypeFilterNote(dropped) + " " + out.item.Note
		}
		var serr error
		examples := &apiExamples{}
		if out.item.BSP, out.item.BSPMore, serr = apiSection(tx, ix, bsp, limit, examples); serr != nil {
			return out, serr
		}
		if out.item.Other, out.item.OtherMore, serr = apiSection(tx, ix, other, limit, examples); serr != nil {
			return out, serr
		}
		if examples.failed != nil {
			out.warn = append(out.warn, Warning{
				Code:    "api_example_unavailable",
				Message: "пример вызова показан не у всех методов: текст модуля не прочитан (" + examples.failed.Error() + ")",
				Hint:    "на поиск это не влияет; места вызова метода отдаёт find_references",
			})
		}
		return out, nil
	})
	if err != nil {
		return Response[APISearchItem]{}, err
	}
	resp := Response[APISearchItem]{Generation: res.gen, Items: []APISearchItem{res.item}, TotalCount: 1, Warnings: res.warn}
	return withSnapshot(resp, snap), nil
}

// apiSection превращает ранжированные методы секции в ответ: первые limit
// полными описаниями, следующие apiMoreCount коротким списком.
//
// Устаревшие ранжируются наравне с действующими и только после среза уходят в
// хвост отданного: поставь их в конец всей секции до среза, и при десятках
// совпадений пометка устаревшего с названной заменой не попала бы в ответ
// никогда, а нужна она именно тому, кто помнит старое имя.
//
// Параметры читаются только у отобранных: сигнатура со значениями по
// умолчанию нужна в ответе, а не в ранжировании.
func apiSection(tx *store.ReadTx, ix *apiIndex, ranked []apiRanked, limit int, examples *apiExamples) ([]APIMethodItem, []APIBriefItem, error) {
	full := ranked[:min(limit, len(ranked))]
	rest := ranked[len(full):min(len(full)+apiMoreCount, len(ranked))]
	full = append([]apiRanked(nil), full...)
	sort.SliceStable(full, func(i, j int) bool {
		return !ix.methods[full[i].method].deprecated && ix.methods[full[j].method].deprecated
	})

	items := make([]APIMethodItem, 0, len(full))
	for _, r := range full {
		m := ix.methods[r.method]
		item, err := apiMethodItem(tx, apiCandidate{row: m.row, call: m.call, execContext: m.execContext, deprecated: m.deprecated})
		if err != nil {
			return nil, nil, err
		}
		item.Returns = strings.Join(m.returns, ", ")
		if item.Calls, err = tx.ExternalCallCount(m.row.SymbolID); err != nil {
			return nil, nil, err
		}
		if item.Calls > 0 {
			if item.Example, err = examples.of(tx, m.row.SymbolID); err != nil {
				return nil, nil, err
			}
		}
		items = append(items, item)
	}
	var more []APIBriefItem
	for _, r := range rest {
		brief, err := apiBriefItem(tx, ix.methods[r.method])
		if err != nil {
			return nil, nil, err
		}
		more = append(more, brief)
	}
	return items, more, nil
}

// apiMethodItem превращает кандидата в элемент ответа: дочитывает параметры
// и собирает сигнатуру.
func apiMethodItem(tx *store.ReadTx, c apiCandidate) (APIMethodItem, error) {
	params, err := tx.SymbolParameters(c.row.SymbolID)
	if err != nil {
		return APIMethodItem{}, err
	}
	return APIMethodItem{
		Call: c.call, Kind: c.row.Kind, Signature: apiSignature(c.row.NameDisplay, params),
		Summary: c.row.DocFirstLine, Context: c.execContext, Deprecated: c.deprecated,
		Module: c.row.ModulePath, Component: domain.ComponentID(c.row.ComponentID),
		Line: c.row.StartLine, UID: c.row.UID,
	}, nil
}

// apiBriefItem: метод одной строкой: вызов, назначение, тип результата и
// число вызовов из других модулей.
func apiBriefItem(tx *store.ReadTx, m apiIndexMethod) (APIBriefItem, error) {
	calls, err := tx.ExternalCallCount(m.row.SymbolID)
	if err != nil {
		return APIBriefItem{}, err
	}
	return APIBriefItem{
		Call: m.call, Summary: m.row.DocFirstLine, Deprecated: m.deprecated,
		Returns: strings.Join(m.returns, ", "), Calls: calls, UID: m.row.UID,
	}, nil
}

// apiTypeFilterNote: что сказать, когда отбор по типу отсеял методы,
// подошедшие по словам. Без этого пустая выдача читалась бы как «такого
// метода нет», хотя метод мог быть отсеян потому, что тип в его комментарии
// не записан или записан словами.
func apiTypeFilterNote(dropped int) string {
	return "Отбор по типу (returns, accepts) отсеял " + strconv.Itoa(dropped) +
		" методов, подошедших по остальным условиям: метод, у которого тип в комментарии не записан или записан словами, отбор не пропускает. Нужного нет: повторите без returns и accepts."
}

const (
	// apiExampleMaxRunes, apiExampleMaxLines: потолок примера вызова.
	apiExampleMaxRunes = 200
	apiExampleMaxLines = 4
)

// apiExamples достаёт примеры вызова методов из текстов модулей. Текст модуля
// читается из blob индекса один раз на ответ: несколько методов выдачи часто
// зовут из одного и того же модуля. Пример необязателен: сбой чтения текста
// поиск не роняет, а запоминается в failed (ответ скажет о нём
// предупреждением).
type apiExamples struct {
	blobs  map[int64][]byte
	failed error
}

// of возвращает оператор одного вызова метода из другого модуля; пусто, когда
// такого вызова нет или его текст не достать.
func (e *apiExamples) of(tx *store.ReadTx, symbolID int64) (string, error) {
	fileID, at, ok, err := tx.ExternalCallSite(symbolID)
	if err != nil || !ok {
		return "", err
	}
	blob, cached := e.blobs[fileID]
	if !cached {
		sf, found, err := tx.SourceFileByID(fileID)
		if err != nil {
			return "", err
		}
		if found {
			if blob, err = tx.Blob(sf.ContentHash); err != nil {
				e.failed, blob = err, nil
			}
		}
		if e.blobs == nil {
			e.blobs = map[int64][]byte{}
		}
		e.blobs[fileID] = blob
	}
	return apiCallStatement(blob, at), nil
}

// apiCallStatement вырезает из текста модуля оператор с вызовом: от начала
// строки, где стоит ссылка на метод, до точки с запятой на нулевой глубине
// скобок, но не дальше apiExampleMaxLines строк и apiExampleMaxRunes знаков.
// Перенесённый на несколько строк вызов склеивается в одну строку.
//
// упрощение: скобки и точка с запятой считаются по тексту как есть, без
// разбора строковых литералов: скобка внутри литерала сдвинет счёт, и пример
// оборвётся раньше или позже. Это пример для чтения, а не код для вставки.
func apiCallStatement(text []byte, at int) string {
	if at < 0 || at >= len(text) {
		return ""
	}
	start := at
	for start > 0 && text[start-1] != '\n' {
		start--
	}
	end, depth, lines := start, 0, 1
scan:
	for ; end < len(text); end++ {
		switch text[end] {
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if end >= at && depth <= 0 {
				end++
				break scan
			}
		case '\n':
			// Строка с вызовом закончилась, а скобки закрыты: это вызов
			// внутри условия или выражения без своей точки с запятой.
			if (end >= at && depth <= 0) || lines == apiExampleMaxLines {
				break scan
			}
			lines++
		}
	}
	return apiCutRunes(strings.Join(strings.Fields(string(text[start:end])), " "), apiExampleMaxRunes)
}

// readAPIMethods читает методы программного интерфейса и состав библиотеки:
// общий отбор для поиска (FindAPI) и каталога (Catalog). Методы
// переопределяемых модулей сюда не входят. warn — предупреждения о том, чего
// в индексе не нашлось.
func readAPIMethods(tx *store.ReadTx) (rows []store.ExportedMethodRow, lib apiLibraryInfo, warn []Warning, err error) {
	all, err := tx.ExportedMethodsInRegions(apiModuleKinds, apiTopRegions)
	if err != nil {
		return nil, lib, nil, err
	}
	if lib, err = readAPILibrary(tx); err != nil {
		return nil, lib, nil, err
	}
	warn = lib.warn
	if len(all) == 0 {
		warn = append(warn, Warning{
			Code:    "api_regions_not_indexed",
			Message: "в индексе нет экспортных методов в области ПрограммныйИнтерфейс",
			Hint:    "индекс собран прежней версией сервера: выполните reindex; модули без разметки областями find_api не видит: ищите по имени через find_symbol",
		})
	} else if !lib.found {
		warn = append(warn, Warning{
			Code:    "bsp_library_not_found",
			Message: "подсистема " + apiLibrarySubsystem + " в индексе не найдена: секция bsp пуста, все методы в other",
		})
	}
	rows = all[:0]
	for _, r := range all {
		if r.ModuleKind == string(bsl.ModuleCommon) && apiOverridableModule(r.ModuleName) {
			continue
		}
		rows = append(rows, r)
	}
	return rows, lib, warn, nil
}

// apiSignature: имя и параметры как в объявлении, со «Знач» и значениями по
// умолчанию: порядок и обязательность параметров и есть то, что агент помнит
// хуже всего.
func apiSignature(name string, params []store.ParameterRow) string {
	var b strings.Builder
	b.WriteString(name + "(")
	for i, p := range params {
		if i > 0 {
			b.WriteString(", ")
		}
		if p.ByVal {
			b.WriteString("Знач ")
		}
		b.WriteString(p.Name)
		if p.DefaultExpr != "" {
			b.WriteString(" = " + p.DefaultExpr)
		}
	}
	b.WriteString(")")
	return b.String()
}

// newAPICandidate выводит выражение вызова и контекст исполнения метода.
func newAPICandidate(r store.ExportedMethodRow) apiCandidate {
	c := apiCandidate{row: r, deprecated: apiDeprecated(r.Region)}
	if r.ModuleKind == string(bsl.ModuleManager) {
		// Модуль менеджера исполняется на сервере (свойств контекста у него
		// нет); зовут его через коллекцию менеджеров:
		// Справочники.Номенклатура.Метод().
		c.execContext = "сервер"
		owner := r.ModuleName
		if r.OwnerName != "" {
			owner = r.OwnerName
		}
		if mk, ok := domain.MetaKindByMType(r.OwnerMType); ok && mk.CollectionRu != "" {
			owner = mk.CollectionRu + "." + owner
		} else if r.OwnerMType != "" {
			// Коллекции менеджеров у вида в словаре нет: вид назван как есть,
			// чтобы имя объекта не выглядело именем общего модуля.
			owner = r.OwnerMType + "." + owner
		}
		c.call = owner + "." + r.NameDisplay
		return c
	}
	// Свойства общего модуля индексация пишет как JSON факта парсера
	// метаданных. Нечитаемый JSON оставляет контекст пустым, а не выдуманным.
	var props meta.ModuleRegistryFact
	if r.ModuleProps != "" {
		_ = json.Unmarshal([]byte(r.ModuleProps), &props)
	}
	switch {
	case props.Server && props.ClientManagedApplication:
		c.execContext = "клиент-сервер"
	case props.Server && props.ServerCall:
		c.execContext = "вызов сервера"
	case props.Server:
		c.execContext = "сервер"
	case props.ClientManagedApplication:
		c.execContext = "клиент"
	}
	c.call = r.ModuleName + "." + r.NameDisplay
	if props.Global {
		// Метод глобального модуля зовут без имени модуля.
		c.call = r.NameDisplay
	}
	return c
}

// apiDeprecated: путь областей проходит через область устаревших.
func apiDeprecated(regionPath string) bool {
	for _, seg := range strings.Split(regionPath, domain.RegionPathSeparator) {
		if domain.NormalizeName(seg) == apiDeprecatedRegion {
			return true
		}
	}
	return false
}

func apiOverridableModule(name string) bool {
	lower := strings.ToLower(name)
	for _, m := range apiOverridableMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// apiOwnerRef называет владельца модуля так же, как его называет состав
// подсистемы: "CommonModule.Имя", "Catalog.Имя". Пусто, когда владелец модуля
// менеджера в индексе не определён.
func apiOwnerRef(r store.ExportedMethodRow) string {
	if r.ModuleKind == string(bsl.ModuleCommon) {
		return "CommonModule." + r.ModuleName
	}
	if r.OwnerMType == "" || r.OwnerName == "" {
		return ""
	}
	return r.OwnerMType + "." + r.OwnerName
}

func apiObjectKey(componentID, objectRef string) string {
	return componentID + "\x00" + domain.NormalizeName(objectRef)
}

// apiLibraryInfo — что удалось узнать о библиотеке стандартных подсистем.
type apiLibraryInfo struct {
	// found — объявление подсистемы библиотеки есть хотя бы в одном компоненте.
	found bool
	// objects — объекты состава библиотеки ("Вид.Имя"), ключ apiObjectKey.
	// Библиотека это не только общие модули: метод модуля менеджера её
	// справочника или регистра такой же библиотечный.
	objects map[string]bool
	version string
	warn    []Warning
	// subsystems: подсистемы библиотеки в порядке обхода: по ним строится
	// карта библиотеки.
	subsystems []apiLibraryPart
}

// apiLibraryPart: подсистема библиотеки и объекты её состава.
type apiLibraryPart struct {
	// path: имена от подсистемы библиотеки до этой, через точку; у самой
	// подсистемы библиотеки пусто.
	path        string
	synonym     string
	componentID string
	objects     []string
}

// contains сообщает, входит ли модуль метода в состав библиотеки.
func (info apiLibraryInfo) contains(r store.ExportedMethodRow) bool {
	return info.objects[apiObjectKey(r.ComponentID, apiOwnerRef(r))]
}

// readAPILibrary собирает состав библиотеки и её версию по всем компонентам
// проекта.
//
// упрощение: состав подсистемы читается из её объявлений, сохранённых в
// индексе (десятки небольших XML): раз на поколение индекса при постройке
// индекса слов и на каждый вызов каталога. Индекс рёбер «подсистема содержит
// объект» не хранит; когда начнёт, чтение уходит в один запрос по рёбрам,
// ответ инструмента при этом не меняется.
func readAPILibrary(tx *store.ReadTx) (apiLibraryInfo, error) {
	info := apiLibraryInfo{objects: map[string]bool{}}
	// Строители путей выгрузки паникуют на виде, которого нет в словаре
	// раскладки; виды здесь литеральные, но в ответе инструмента паника
	// кладёт весь сервер, поэтому словарь спрашивается явно.
	for _, mtype := range []string{"Subsystem", "CommonModule"} {
		if _, known := workspace.DumpCollectionDir(mtype); !known {
			return info, nil
		}
	}
	comps, err := tx.Components()
	if err != nil {
		return info, err
	}
	for _, comp := range comps {
		if err := collectSubsystemObjects(tx, comp.ID, &info); err != nil {
			return info, err
		}
		if info.version != "" {
			continue
		}
		if info.version, err = readLibraryVersion(tx, comp.ID); err != nil {
			return info, err
		}
	}
	return info, nil
}

// collectSubsystemObjects обходит подсистему библиотеки и все вложенные и
// складывает объекты их состава в info.objects.
func collectSubsystemObjects(tx *store.ReadTx, componentID string, info *apiLibraryInfo) error {
	type entry struct{ rel, path string }
	queue := []entry{{rel: workspace.DumpDeclarationPath("Subsystem", apiLibrarySubsystem)}}
	seen := map[string]bool{}
	for len(queue) > 0 {
		if len(seen) >= apiSubsystemFilesLimit {
			info.warn = append(info.warn, Warning{
				Code:    "subsystem_tree_truncated",
				Message: "дерево подсистемы " + apiLibrarySubsystem + " обойдено не целиком: прочитано объявлений " + strconv.Itoa(len(seen)),
				Hint:    "часть методов библиотеки могла попасть в секцию other",
			})
			return nil
		}
		cur := queue[0]
		queue = queue[1:]
		if seen[cur.rel] {
			continue
		}
		seen[cur.rel] = true
		fileID, ok, err := tx.SourceFileID(componentID, cur.rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		src, err := readIndexedFile(tx, fileID)
		if err != nil {
			return err
		}
		info.found = true
		content, diags := meta.ParseSubsystemContent(cur.rel, src)
		if len(diags) > 0 {
			info.warn = append(info.warn, Warning{
				Code:    "subsystem_content_unreadable",
				Message: "объявление подсистемы " + cur.rel + " не разобрано: " + diags[0].Message,
				Hint:    "методы её объектов попадут в секцию other",
			})
			continue
		}
		for _, obj := range content.Objects {
			info.objects[apiObjectKey(componentID, obj)] = true
		}
		info.subsystems = append(info.subsystems, apiLibraryPart{
			path: cur.path, synonym: content.Synonym, componentID: componentID, objects: content.Objects,
		})
		for _, child := range content.Children {
			path := child
			if cur.path != "" {
				path = cur.path + "." + child
			}
			queue = append(queue, entry{rel: workspace.DumpChildSubsystemPath(cur.rel, child), path: path})
		}
	}
	return nil
}

// readLibraryVersion достаёт версию библиотеки из тела метода описания
// подсистемы; пусто, если модуля, метода или литерала версии нет.
func readLibraryVersion(tx *store.ReadTx, componentID string) (string, error) {
	rel := workspace.DumpModulePath("CommonModule", apiLibraryUpdateModule, workspace.ModuleCommon)
	fileID, ok, err := tx.SourceFileID(componentID, rel)
	if err != nil || !ok {
		return "", err
	}
	sym, ok, err := tx.SymbolByFileAndName(fileID, domain.NormalizeName(apiLibraryUpdateMethod))
	if err != nil || !ok {
		return "", err
	}
	src, err := readIndexedFile(tx, fileID)
	if err != nil {
		return "", err
	}
	if sym.Span.StartByte < 0 || sym.Span.EndByte > len(src) || sym.Span.StartByte > sym.Span.EndByte {
		return "", nil
	}
	if m := reAPILibraryVersion.FindSubmatch(src[sym.Span.StartByte:sym.Span.EndByte]); m != nil {
		return string(m[1]), nil
	}
	return "", nil
}

// readIndexedFile отдаёт содержимое файла из образа, сохранённого в индексе:
// то же поколение, что и остальные факты ответа, без чтения диска.
func readIndexedFile(tx *store.ReadTx, fileID int64) ([]byte, error) {
	sf, ok, err := tx.SourceFileByID(fileID)
	if err != nil || !ok {
		return nil, err
	}
	return tx.Blob(sf.ContentHash)
}
