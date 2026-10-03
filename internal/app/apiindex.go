package app

import (
	"math"
	"sort"
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
	apiFieldCount
)

// apiFieldCover: во сколько слово запроса засчитывается в охват в зависимости
// от того, где оно нашлось (берётся лучшее из мест). Имя метода называет его
// назначение точнее всего; слово из описания параметра говорит лишь, что
// метод имеет к предмету отношение. Без этой разницы метод, у которого слова
// запроса разбросаны по длинному комментарию, обходил метод с теми же словами
// в имени: при равном весе полей на настроечной половине эталона первых мест
// по библиотеке было 54 из 154 против 86.
var apiFieldCover = [apiFieldCount]float64{
	apiFieldName:    1.0,
	apiFieldModule:  0.7,
	apiFieldSummary: 0.8,
	apiFieldDoc:     0.5,
}

// apiFieldPlace: вес места совпадения во втором ключе сортировки: при равном
// охвате выше метод, у которого слова запроса стоят в более значимых местах
// и в большем их числе.
var apiFieldPlace = [apiFieldCount]float64{
	apiFieldName:    3,
	apiFieldModule:  2,
	apiFieldSummary: 1,
	apiFieldDoc:     0.5,
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
}

// buildAPIIndex читает методы программного интерфейса и строит по ним индекс
// слов.
func buildAPIIndex(tx *store.ReadTx, gen domain.Generation) (*apiIndex, error) {
	rows, lib, warn, err := readAPIMethods(tx)
	if err != nil {
		return nil, err
	}
	ix := &apiIndex{
		gen: gen, version: lib.version, warn: warn,
		methods: make([]apiIndexMethod, 0, len(rows)), byPrefix: map[string][]int32{},
	}
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
		nameWords := 0
		for _, w := range apiWords(r.NameDisplay) {
			if len([]rune(w)) >= apiMinCommonPrefix {
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
		// Полный комментарий нужен только словами: текст в памяти не держится.
		r.Doc = ""
		ix.methods = append(ix.methods, apiIndexMethod{
			row: r, call: c.call, execContext: c.execContext, deprecated: c.deprecated,
			library: lib.contains(r), nameWords: nameWords,
		})
	}
	return ix, nil
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
}

// apiStrongFields: поля, слова которых называют назначение метода: имя, имя
// модуля, первая строка комментария.
const apiStrongFields = 1<<uint(apiFieldName) | 1<<uint(apiFieldModule) | 1<<uint(apiFieldSummary)

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
// Редкостей у слова две. Для совпадения в имени, модуле и первой строке
// считается, у скольких методов слово стоит в этих полях; для совпадения в
// остальном комментарии считается, у скольких оно стоит где угодно. Одна
// общая редкость обесценивала слова имени: «строка» как тип параметра есть в
// комментарии каждого третьего метода, и по общему счёту слово «строку» в
// имени РазложитьСтрокуВМассивПодстрок весило впятеро меньше слова
// «разделитель», из-за чего метод уступал ЭтоРазделительСлов.
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
			}
			r.coverage += best
		}
		if n := ix.methods[m].nameWords; n > 0 {
			r.nameFit = math.Min(1, float64(inName)/float64(n))
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// Охват сравнивается с округлением: сумма логарифмов в разном порядке
		// слагаемых расходится в последних знаках, и равные методы менялись
		// бы местами от запуска к запуску.
		if ca, cb := math.Round(a.coverage*1e6), math.Round(b.coverage*1e6); ca != cb {
			return ca > cb
		}
		if a.nameFit != b.nameFit {
			return a.nameFit > b.nameFit
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return ix.methods[a.method].call < ix.methods[b.method].call
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
