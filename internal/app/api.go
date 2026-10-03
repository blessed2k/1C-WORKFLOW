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

// FindAPIInput — вход find_api.
type FindAPIInput struct {
	Query string
	Limit int
}

// APIMethodItem — один готовый метод программного интерфейса.
type APIMethodItem struct {
	// Call — выражение вызова без скобок: "ОбщегоНазначения.ЗначениеРеквизитаОбъекта",
	// "Справочники.Номенклатура.Метод"; у глобального модуля одно имя метода.
	Call      string `json:"call"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
	Summary   string `json:"summary,omitempty"`
	// Context — где метод доступен: сервер, клиент, клиент-сервер, вызов сервера.
	Context string `json:"context,omitempty"`
	// Deprecated — метод лежит в области устаревших; замену называет Summary.
	Deprecated bool               `json:"deprecated,omitempty"`
	Module     string             `json:"module"`
	Component  domain.ComponentID `json:"component"`
	Line       int                `json:"line"`
	UID        string             `json:"uid"`
}

// APISearchItem — items[0] ответа find_api: две независимо ранжированные
// секции. Секций две, потому что в общем списке методы библиотеки тонут среди
// прикладных с похожими словами в именах.
type APISearchItem struct {
	Query string `json:"query"`
	// BSPVersion — версия библиотеки стандартных подсистем из выгрузки.
	BSPVersion string `json:"bspVersion,omitempty"`
	// BSP — методы общих модулей и модулей менеджеров объектов, входящих в
	// подсистему СтандартныеПодсистемы (со всеми вложенными).
	BSP []APIMethodItem `json:"bsp"`
	// Other — остальные общие модули и модули менеджеров конфигурации.
	Other []APIMethodItem `json:"other"`
	// BSPMatched/OtherMatched — сколько методов секции совпало с запросом всего.
	BSPMatched   int    `json:"bspMatched"`
	OtherMatched int    `json:"otherMatched"`
	Note         string `json:"note"`
}

const apiSearchNote = "Поиск лексический: слово запроса должно совпасть с началом слова в имени метода, имени модуля или первой строке описания. " +
	"Нужного нет: повторите запрос, дописав синонимы и термины самой конфигурации (не «сохранить пароль», а «записать данные безопасное хранилище»). " +
	"Тело и полное описание параметров: get_symbol по uid."

// APIService — сервис за find_api.
type APIService struct{ projects *Projects }

// NewAPIService строит сервис поверх общего резолвера проектов.
func NewAPIService(p *Projects) *APIService { return &APIService{projects: p} }

// apiCandidate — метод с посчитанным совпадением, до отбора в ответ.
type apiCandidate struct {
	row         store.ExportedMethodRow
	call        string
	execContext string
	deprecated  bool
	coverage    int
	score       int
}

// FindAPI ищет по описанию задачи готовые экспортные методы программного
// интерфейса: общих модулей и модулей менеджеров.
func (s *APIService) FindAPI(ctx context.Context, in FindAPIInput) (Response[APISearchItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[APISearchItem]{}, err
	}
	query := strings.TrimSpace(in.Query)
	terms := apiQueryTerms(query)
	if len(terms) == 0 {
		return Response[APISearchItem]{}, NewError(CodeInvalidArgument,
			"find_api требует query со словами длиннее двух букв",
			"опишите задачу словами, например: разбить строку по разделителю").WithProject(op.Entry.ID)
	}
	limit := clampLimit(in.Limit, defaultAPILimit, maxAPILimit)

	type txResult struct {
		item APISearchItem
		gen  domain.Generation
		warn []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		out := txResult{item: APISearchItem{Query: query, Note: apiSearchNote}}
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen

		rows, rerr := tx.ExportedMethodsInRegions(apiModuleKinds, apiTopRegions)
		if rerr != nil {
			return out, rerr
		}
		lib, lerr := readAPILibrary(tx)
		if lerr != nil {
			return out, lerr
		}
		out.item.BSPVersion = lib.version
		out.warn = lib.warn
		if len(rows) == 0 {
			out.warn = append(out.warn, Warning{
				Code:    "api_regions_not_indexed",
				Message: "в индексе нет экспортных методов в области ПрограммныйИнтерфейс",
				Hint:    "индекс собран прежней версией сервера: выполните reindex; модули без разметки областями find_api не видит: ищите по имени через find_symbol",
			})
		} else if !lib.found {
			out.warn = append(out.warn, Warning{
				Code:    "bsp_library_not_found",
				Message: "подсистема " + apiLibrarySubsystem + " в индексе не найдена: секция bsp пуста, все методы в other",
			})
		}

		var bsp, other []apiCandidate
		for _, r := range rows {
			if r.ModuleKind == string(bsl.ModuleCommon) && apiOverridableModule(r.ModuleName) {
				continue
			}
			coverage, score := apiRank(terms, apiSearchTextOf(r))
			if coverage == 0 {
				continue
			}
			// Выражение вызова и контекст выводятся только у совпавших: разбор
			// свойств модуля на каждый из десятков тысяч методов стоил бы
			// дороже самого сравнения.
			c := newAPICandidate(r)
			c.coverage, c.score = coverage, score
			if lib.objects[apiObjectKey(r.ComponentID, apiOwnerRef(r))] {
				bsp = append(bsp, c)
			} else {
				other = append(other, c)
			}
		}
		out.item.BSPMatched, out.item.OtherMatched = len(bsp), len(other)
		var serr error
		if out.item.BSP, serr = apiSection(tx, bsp, limit); serr != nil {
			return out, serr
		}
		if out.item.Other, serr = apiSection(tx, other, limit); serr != nil {
			return out, serr
		}
		return out, nil
	})
	if err != nil {
		return Response[APISearchItem]{}, err
	}
	resp := Response[APISearchItem]{Generation: res.gen, Items: []APISearchItem{res.item}, TotalCount: 1, Warnings: res.warn}
	return withSnapshot(resp, snap), nil
}

// apiSection отбирает первые limit кандидатов секции и превращает их в
// элементы ответа.
//
// Устаревшие ранжируются наравне с действующими и только после среза уходят в
// хвост отданного: поставь их в конец всей секции до среза, и при десятках
// совпадений пометка устаревшего с названной заменой не попала бы в ответ
// никогда, а нужна она именно тому, кто помнит старое имя.
//
// Параметры читаются только у отобранных: сигнатура со значениями по
// умолчанию нужна в ответе, а не в ранжировании.
func apiSection(tx *store.ReadTx, cands []apiCandidate, limit int) ([]APIMethodItem, error) {
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.coverage != b.coverage {
			return a.coverage > b.coverage
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return a.call < b.call
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	sort.SliceStable(cands, func(i, j int) bool { return !cands[i].deprecated && cands[j].deprecated })

	out := make([]APIMethodItem, 0, len(cands))
	for _, c := range cands {
		params, err := tx.SymbolParameters(c.row.SymbolID)
		if err != nil {
			return nil, err
		}
		out = append(out, APIMethodItem{
			Call: c.call, Kind: c.row.Kind, Signature: apiSignature(c.row.NameDisplay, params),
			Summary: c.row.DocFirstLine, Context: c.execContext, Deprecated: c.deprecated,
			Module: c.row.ModulePath, Component: domain.ComponentID(c.row.ComponentID),
			Line: c.row.StartLine, UID: c.row.UID,
		})
	}
	return out, nil
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

// apiSearchTextOf готовит метод к сравнению с запросом.
func apiSearchTextOf(r store.ExportedMethodRow) apiSearchText {
	module := r.ModuleName
	if r.ModuleKind == string(bsl.ModuleManager) && r.OwnerName != "" {
		module = r.OwnerName
	}
	return apiSearchText{name: apiWords(r.NameDisplay), module: apiWords(module), summary: apiWords(r.DocFirstLine)}
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
}

// readAPILibrary собирает состав библиотеки и её версию по всем компонентам
// проекта.
//
// упрощение: состав подсистемы читается из её объявлений, сохранённых в
// индексе, на каждый вызов (десятки небольших XML). Индекс рёбер
// «подсистема содержит объект» не хранит; когда начнёт, чтение уходит в один
// запрос по рёбрам, ответ инструмента при этом не меняется.
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
	queue := []string{workspace.DumpDeclarationPath("Subsystem", apiLibrarySubsystem)}
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
		rel := queue[0]
		queue = queue[1:]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		fileID, ok, err := tx.SourceFileID(componentID, rel)
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
		content, diags := meta.ParseSubsystemContent(rel, src)
		if len(diags) > 0 {
			info.warn = append(info.warn, Warning{
				Code:    "subsystem_content_unreadable",
				Message: "объявление подсистемы " + rel + " не разобрано: " + diags[0].Message,
				Hint:    "методы её объектов попадут в секцию other",
			})
			continue
		}
		for _, obj := range content.Objects {
			info.objects[apiObjectKey(componentID, obj)] = true
		}
		for _, child := range content.Children {
			queue = append(queue, workspace.DumpChildSubsystemPath(rel, child))
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
