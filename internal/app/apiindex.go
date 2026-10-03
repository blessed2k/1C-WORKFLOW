package app

import (
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// apiField: место в методе, где ищется слово запроса.
type apiField uint8

const (
	apiFieldName apiField = iota
	apiFieldModule
	// apiFieldSummary: первая строка комментария, назначение метода.
	apiFieldSummary
	// apiFieldDoc: остальной комментарий: параметры, возвращаемое значение,
	// примеры. Слова, которые есть и в первой строке, здесь не повторяются.
	apiFieldDoc
	// apiFieldCard: карточка поиска: формулировки задачи словами разработчика
	// (APICard). Есть только у методов, для которых карточки построены.
	apiFieldCard
	apiFieldCount
)

// apiFieldCover: во сколько слово запроса засчитывается в охват в зависимости
// от того, где оно нашлось (берётся лучшее из мест). Имя метода называет его
// назначение точнее всего; слово из описания параметра говорит лишь, что
// метод имеет к предмету отношение. Без этой разницы метод, у которого слова
// запроса разбросаны по длинному комментарию, обходил метод с теми же словами
// в имени: при равном весе полей на настроечной половине эталона первых мест
// по библиотеке было 54 из 154 против 83 (замер без карточек).
var apiFieldCover = [apiFieldCount]float64{
	apiFieldName:    1.0,
	apiFieldModule:  0.7,
	apiFieldSummary: 0.8,
	apiFieldDoc:     0.5,
	// Карточка весит меньше имени и первой строки: её формулировки написаны
	// моделью и шире назначения метода. Подобрано по настроечной половине
	// эталона (score -split dev, все карточки; таблица в
	// docs/find-api-eval.md): при 0,7 лучшие первое место и первая десятка
	// сразу по библиотеке и по конфигурации; при 0,85 и 1,0 карточки соседних
	// методов вытесняют метод с точным именем (первое место по библиотеке 85
	// и 82 против 93 из 154).
	apiFieldCard: 0.7,
}

// apiFieldPlace: вес места совпадения во втором ключе сортировки: при равном
// охвате выше метод, у которого слова запроса стоят в более значимых местах
// и в большем их числе.
var apiFieldPlace = [apiFieldCount]float64{
	apiFieldName:    3,
	apiFieldModule:  2,
	apiFieldSummary: 1,
	apiFieldDoc:     0.5,
	apiFieldCard:    1,
}

// apiIndexMethod: метод программного интерфейса, подготовленный к поиску.
type apiIndexMethod struct {
	row         store.ExportedMethodRow
	call        string
	execContext string
	deprecated  bool
	// library: метод входит в библиотеку стандартных подсистем.
	library bool
	// nameWords: сколько в имени метода слов, годных для сравнения.
	nameWords int
	// owner: модуль метода, как его называют в коде: имя общего модуля или
	// менеджер объекта (Справочники.Товары).
	owner string
	// returns, accepts: типы возвращаемого значения и параметров из
	// комментария метода (apiDocTypes); пусто, когда в комментарии их нет.
	returns []string
	accepts []string
}

// apiIndex: методы программного интерфейса одного поколения индекса и
// обратный индекс их слов.
//
// Слова имён и комментариев десятков тысяч методов разбираются один раз на
// поколение, а не на каждый вызов: с полным комментарием текста втрое больше,
// чем было с одной первой строкой, и разбор на вызов стоил бы секунды.
// Живёт в памяти сервиса, пока поколение индекса не сменится.
type apiIndex struct {
	gen     domain.Generation
	methods []apiIndexMethod
	// words: словарь; byPrefix: слова словаря по первым apiMinCommonPrefix
	// буквам (слова с другим началом одним словом с запросом быть не могут).
	words    []string
	byPrefix map[string][]int32
	// postings: по полю и слову словаря методы, у которых это слово в этом
	// поле есть.
	postings [apiFieldCount][][]int32
	version  string
	warn     []Warning
	// cards: у скольких методов есть карточка поиска.
	cards int
	// byOwner: методы по модулю (apiIndexMethod.owner в нижнем регистре), в
	// порядке объявления.
	byOwner map[string][]int32
	// libraryMap: карта библиотеки: подсистемы и их модули с программным
	// интерфейсом.
	libraryMap []APIMapSubsystem
}

// apiOwnerOf: модуль метода, как его называют в коде. У общего модуля это его
// имя (и у глобального тоже: в выражении вызова имени нет, а модуль есть), у
// модуля менеджера выражение до имени метода.
func apiOwnerOf(r store.ExportedMethodRow, call string) string {
	if r.ModuleKind == string(bsl.ModuleCommon) {
		return r.ModuleName
	}
	if i := strings.LastIndex(call, "."); i >= 0 {
		return call[:i]
	}
	return r.ModuleName
}

// buildAPIIndex читает методы программного интерфейса и строит по ним индекс
// слов.
func buildAPIIndex(tx *store.ReadTx, gen domain.Generation) (*apiIndex, error) {
	rows, lib, warn, err := readAPIMethods(tx)
	if err != nil {
		return nil, err
	}
	// Карточки читаются при постройке индекса слов: новый файл карточек
	// подхватывается после переиндексации или перезапуска сервера. Читаются
	// с диска целиком (мегабайты на конфигурацию) под замком кэша и внутри
	// читающей транзакции вызова, см. упрощения у apiIndexCache.get.
	cards, cerr := LoadAPICards(APICardsDir())
	if cerr != nil {
		warn = append(warn, Warning{
			Code:    "api_cards_unreadable",
			Message: "карточки поиска не прочитаны: " + cerr.Error(),
			Hint:    "поиск идёт без карточек: по имени метода, имени модуля и комментарию",
		})
	}
	ix := &apiIndex{
		gen: gen, version: lib.version, warn: warn,
		methods: make([]apiIndexMethod, 0, len(rows)), byPrefix: map[string][]int32{},
		byOwner: map[string][]int32{},
	}
	// live: сколько действующих методов у объекта состава библиотеки, и как
	// его модуль называют в коде.
	type ownerStat struct {
		owner string
		live  int
	}
	owners := map[string]*ownerStat{}
	ids := map[string]int32{}
	add := func(field apiField, word string, method int32) {
		runes := []rune(word)
		if len(runes) < apiMinCommonPrefix {
			return // короче общего начала: с словом запроса не совпадёт никогда
		}
		id, ok := ids[word]
		if !ok {
			id = int32(len(ix.words))
			ids[word] = id
			ix.words = append(ix.words, word)
			prefix := string(runes[:apiMinCommonPrefix])
			ix.byPrefix[prefix] = append(ix.byPrefix[prefix], id)
			for f := range ix.postings {
				ix.postings[f] = append(ix.postings[f], nil)
			}
		}
		if p := ix.postings[field][id]; len(p) == 0 || p[len(p)-1] != method {
			ix.postings[field][id] = append(p, method)
		}
	}
	for _, r := range rows {
		c := newAPICandidate(r)
		m := int32(len(ix.methods))
		module := r.ModuleName
		if r.ModuleKind == string(bsl.ModuleManager) && r.OwnerName != "" {
			module = r.OwnerName
		}
		// Слова имени, которые запрос способен покрыть: без коротких, без
		// служебных (запрос их не несёт) и без повторов.
		nameWords := 0
		counted := map[string]bool{}
		for _, w := range apiWords(r.NameDisplay) {
			if len([]rune(w)) >= apiMinCommonPrefix && !apiStopWords[w] && !counted[w] {
				counted[w] = true
				nameWords++
			}
			add(apiFieldName, w, m)
		}
		for _, w := range apiWords(module) {
			add(apiFieldModule, w, m)
		}
		inSummary := map[string]bool{}
		for _, w := range apiWords(r.DocFirstLine) {
			inSummary[w] = true
			add(apiFieldSummary, w, m)
		}
		for _, w := range apiWords(r.Doc) {
			if !inSummary[w] {
				add(apiFieldDoc, w, m)
			}
		}
		if card, ok := cards[strings.ToLower(c.call)]; ok {
			ix.cards++
			for _, text := range append(append([]string(nil), card.Tasks...), card.Instead) {
				for _, w := range apiWords(text) {
					add(apiFieldCard, w, m)
				}
			}
		}
		returns, accepts := apiDocTypes(r.Doc)
		if r.Kind != string(domain.SymbolFunction) {
			returns = nil // процедура ничего не возвращает, что бы ни стояло в комментарии
		}
		// Полный комментарий нужен только словами и типами, свойства модуля и
		// путь областей уже разобраны в выражение вызова, контекст и пометку
		// устаревшего: в памяти эти строки не держатся.
		r.Doc, r.ModuleProps, r.Region = "", "", ""
		owner := apiOwnerOf(r, c.call)
		ix.byOwner[strings.ToLower(owner)] = append(ix.byOwner[strings.ToLower(owner)], m)
		if !c.deprecated {
			key := apiObjectKey(r.ComponentID, apiOwnerRef(r))
			if owners[key] == nil {
				owners[key] = &ownerStat{owner: owner}
			}
			owners[key].live++
		}
		ix.methods = append(ix.methods, apiIndexMethod{
			row: r, call: c.call, execContext: c.execContext, deprecated: c.deprecated,
			library: lib.contains(r), nameWords: nameWords, owner: owner,
			returns: returns, accepts: accepts,
		})
	}
	for _, part := range lib.subsystems {
		sub := APIMapSubsystem{Name: part.path, Title: part.synonym}
		if sub.Name == "" {
			sub.Name = apiLibrarySubsystem
		}
		for _, obj := range part.objects {
			if st := owners[apiObjectKey(part.componentID, obj)]; st != nil {
				sub.Modules = append(sub.Modules, APIMapModule{Name: st.owner, Methods: st.live})
			}
		}
		if len(sub.Modules) > 0 {
			sort.Slice(sub.Modules, func(i, j int) bool { return sub.Modules[i].Name < sub.Modules[j].Name })
			ix.libraryMap = append(ix.libraryMap, sub)
		}
	}
	sort.SliceStable(ix.libraryMap, func(i, j int) bool { return ix.libraryMap[i].Name < ix.libraryMap[j].Name })
	// Порядок объявления: по строке в модуле. Методы читаются в порядке id
	// символа, а после инкрементной переиндексации он с порядком в файле не
	// совпадает.
	for _, methods := range ix.byOwner {
		sort.SliceStable(methods, func(i, j int) bool {
			a, b := ix.methods[methods[i]].row, ix.methods[methods[j]].row
			if a.ModulePath != b.ModulePath {
				return a.ModulePath < b.ModulePath
			}
			return a.StartLine < b.StartLine
		})
	}
	return ix, nil
}

// owners: все модули с программным интерфейсом, по имени: из них ошибка
// «модуль не найден» выбирает похожие.
func (ix *apiIndex) owners() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range ix.methods {
		if !seen[m.owner] {
			seen[m.owner] = true
			out = append(out, m.owner)
		}
	}
	sort.Strings(out)
	return out
}

// apiOwnerName приводит имя модуля к тому, как модуль называют в коде.
// Остальные инструменты сервера пишут объект в нотации метаданных
// (CommonModule.Имя, Catalog.Имя, Справочник.Имя), а find_api зовёт модуль
// как в выражении вызова: имя общего модуля или Справочники.Имя.
func apiOwnerName(module string) string {
	kind, name, ok := strings.Cut(strings.TrimSpace(module), ".")
	if !ok || name == "" {
		return module
	}
	switch domain.NormalizeName(kind) {
	case "commonmodule", "общиймодуль", "общиемодули", "commonmodules":
		return name
	}
	if mk, found := domain.MetaKindByMType(kind); found && mk.CollectionRu != "" {
		return mk.CollectionRu + "." + name
	}
	if collection, found := apiSingularKinds[domain.NormalizeName(kind)]; found {
		return collection + "." + name
	}
	return module
}

// apiSingularKinds: русское имя вида объекта в единственном числе (так объект
// пишут в запросах и в полном имени метаданных) к коллекции менеджеров.
// Только виды, у которых бывает модуль менеджера с программным интерфейсом.
var apiSingularKinds = map[string]string{
	"справочник":             "Справочники",
	"документ":               "Документы",
	"обработка":              "Обработки",
	"отчет":                  "Отчеты",
	"перечисление":           "Перечисления",
	"регистрсведений":        "РегистрыСведений",
	"регистрнакопления":      "РегистрыНакопления",
	"регистрбухгалтерии":     "РегистрыБухгалтерии",
	"регистррасчета":         "РегистрыРасчета",
	"плансчетов":             "ПланыСчетов",
	"планвидовхарактеристик": "ПланыВидовХарактеристик",
	"планвидоврасчета":       "ПланыВидовРасчета",
	"планобмена":             "ПланыОбмена",
	"бизнеспроцесс":          "БизнесПроцессы",
	"задача":                 "Задачи",
	"журналдокументов":       "ЖурналыДокументов",
	"константа":              "Константы",
	"критерийотбора":         "КритерииОтбора",
	"хранилищенастроек":      "ХранилищаНастроек",
}

// apiRanked: метод с посчитанным совпадением с запросом.
type apiRanked struct {
	method int32
	// coverage: сумма весов найденных слов запроса с поправкой на место.
	coverage float64
	// nameFit: какую долю слов имени метода покрывает запрос. При равном
	// охвате точное имя стоит выше имени с лишними словами: на запрос
	// «значение реквизита объекта» ЗначениеРеквизитаОбъекта выше, чем
	// ЗначениеРеквизитаОбъектаПоУмолчаниюДляФормы.
	nameFit float64
	score   float64
	// nameMask: какие слова запроса (битами, по порядку слов) нашлись в
	// имени метода; matched: сколько слов запроса нашлось у метода где
	// угодно; strong: сколько нашлось в полях, называющих назначение
	// (apiStrongFields). Нужны там, где решают не «кто выше», а «называть ли
	// метод вообще» (обратная проверка черновика, готовые методы по задаче).
	nameMask uint32
	matched  int
	strong   int
}

// apiStrongFields: поля, слова которых называют назначение метода: имя, имя
// модуля, первая строка комментария, карточка поиска.
const apiStrongFields = 1<<uint(apiFieldName) | 1<<uint(apiFieldModule) | 1<<uint(apiFieldSummary) | 1<<uint(apiFieldCard)

// search сравнивает запрос со всеми методами и возвращает совпавшие, лучшие
// первыми.
//
// Вес слова запроса в охвате задаёт его редкость: слово, которое есть почти в
// каждом методе («получить», «данные», «таблица»), о назначении метода не
// говорит ничего, а редкое («индексация», «факсимиле») называет его почти
// однозначно. Редкость считается по всем методам сразу, а не по секции: слово
// не становится редким оттого, что в библиотеке его меньше, чем в
// конфигурации.
//
// Редкостей у слова две. Для совпадения в имени, модуле, первой строке и
// карточке считается, у скольких методов слово стоит в этих полях; для
// совпадения в остальном комментарии считается, у скольких оно стоит где
// угодно. Одна общая редкость обесценивала слова имени: «строка» как тип
// параметра есть в комментарии каждого третьего метода, и по общему счёту
// слово «строку» в имени РазложитьСтрокуВМассивПодстрок почти ничего не
// весило: на запрос «разбить строку по разделителю» метод стоял пятым, с
// двумя редкостями стал третьим.
func (ix *apiIndex) search(terms []string) []apiRanked {
	// fields[t][m]: в каких полях метода m нашлось слово запроса t, битами.
	fields := make([][]uint8, len(terms))
	strong := make([]float64, len(terms))
	anywhere := make([]float64, len(terms))
	touched := map[int32]bool{}
	for t, term := range terms {
		runes := []rune(term)
		if len(runes) < apiMinCommonPrefix {
			continue
		}
		mask := make([]uint8, len(ix.methods))
		inAny, inStrong := 0, 0
		for _, id := range ix.byPrefix[string(runes[:apiMinCommonPrefix])] {
			if !apiSameWord(term, ix.words[id]) {
				continue
			}
			for f := range ix.postings {
				bit := uint8(1) << uint(f)
				for _, m := range ix.postings[f][id] {
					if mask[m] == 0 {
						inAny++
						touched[m] = true
					}
					if bit&apiStrongFields != 0 && mask[m]&apiStrongFields == 0 {
						inStrong++
					}
					mask[m] |= bit
				}
			}
		}
		fields[t] = mask
		n := float64(len(ix.methods))
		if inAny > 0 {
			anywhere[t] = math.Log(1 + n/float64(inAny))
		}
		if inStrong > 0 {
			strong[t] = math.Log(1 + n/float64(inStrong))
		}
	}

	out := make([]apiRanked, 0, len(touched))
	for m := range touched {
		r := apiRanked{method: m}
		inName := 0
		for t := range terms {
			if fields[t] == nil || fields[t][m] == 0 {
				continue
			}
			best := 0.0
			for f := apiField(0); f < apiFieldCount; f++ {
				if fields[t][m]&(1<<uint(f)) == 0 {
					continue
				}
				weight := strong[t]
				if f == apiFieldDoc {
					weight = anywhere[t]
				}
				best = math.Max(best, apiFieldCover[f]*weight)
				r.score += apiFieldPlace[f]
			}
			if fields[t][m]&(1<<uint(apiFieldName)) != 0 {
				inName++
				// Слов запроса не больше apiMaxTerms, маска их вмещает.
				r.nameMask |= 1 << uint(t)
			}
			if fields[t][m]&apiStrongFields != 0 {
				r.strong++
			}
			r.matched++
			r.coverage += best
		}
		if n := ix.methods[m].nameWords; n > 0 {
			r.nameFit = math.Min(1, float64(inName)/float64(n))
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// Охват сравнивается с округлением: разница в седьмом знаке суммы
		// логарифмов это не разница в качестве совпадения, и решать должен
		// следующий ключ.
		if ca, cb := math.Round(a.coverage*1e6), math.Round(b.coverage*1e6); ca != cb {
			return ca > cb
		}
		if a.nameFit != b.nameFit {
			return a.nameFit > b.nameFit
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if ca, cb := ix.methods[a.method].call, ix.methods[b.method].call; ca != cb {
			return ca < cb
		}
		// Одноимённые методы разных компонентов: порядок чтения из индекса.
		return a.method < b.method
	})
	return out
}

// apiIndexCache держит индекс слов по одному на проект. Поколение индекса
// сменилось: индекс строится заново.
type apiIndexCache struct {
	mu      sync.Mutex
	indexes map[domain.ProjectID]*apiIndex
}

// get отдаёт индекс слов поколения gen, строя его при первом обращении.
// Строится под замком: параллельные вызовы ждут одну постройку, а не делают
// каждый свою.
//
// упрощение: ждущие постройку держат читающую транзакцию и отмену своего
// контекста не слушают. На типовой конфигурации постройка вместе с чтением
// файлов карточек занимает одну-две секунды раз на поколение индекса, и пул читателей на это время может быть
// занят вызовами find_api целиком. Путь снятия: строить вне транзакции
// вызова, а ждать через канал с выбором по ctx.Done().
//
// упрощение: индекс слов лежит по одному на проект и не вытесняется: сколько
// проектов открывали за жизнь процесса, столько индексов и занято (десятки
// мегабайт на типовую конфигурацию). Путь снятия: держать индекс только
// активного проекта.
func (c *apiIndexCache) get(project domain.ProjectID, tx *store.ReadTx, gen domain.Generation) (*apiIndex, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ix := c.indexes[project]; ix != nil && ix.gen == gen {
		return ix, nil
	}
	ix, err := buildAPIIndex(tx, gen)
	if err != nil {
		return nil, err
	}
	if c.indexes == nil {
		c.indexes = map[domain.ProjectID]*apiIndex{}
	}
	c.indexes[project] = ix
	return ix, nil
}
