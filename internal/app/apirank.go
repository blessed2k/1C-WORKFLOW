package app

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Ранжирование find_api. Поиск лексический: слово запроса ищется среди слов
// имени метода, имени модуля и первой строки описания. Слова сравниваются
// целиком, а не подстрокой: подстрока «цен» находит «Сценарий», «Оценка» и
// «Лицензия», и на общем наборе методов конфигурации такой шум занимает весь
// топ.
//
// Правила ниже подобраны замером на выгрузке УТ 11.5 по 25 запросам с
// известным ответом (7 методов БСП и 18 прикладных, названных в статьях
// разработчиков): в первой тройке своей секции 23 против 19 у прежнего
// правила «начало слова по усечённой основе, счёт совпавших слов». Это
// настроечный набор, а не оценка качества: на 15 запросах другой статьи, в
// настройке не участвовавших, в первой тройке 9 против 6. Оба набора
// закреплены в TestRealDumpFindAPI.

// Веса места совпадения: имя метода самый сильный признак того, для чего
// метод, описание самый слабый.
const (
	apiWeightName    = 3
	apiWeightModule  = 2
	apiWeightSummary = 1
)

// apiMaxTerms: потолок слов запроса. Совпавшие слова метода хранятся битами
// одного числа; запрос длиннее описывает уже не одну задачу.
const apiMaxTerms = 32

// apiStopWords: союзы, предлоги и местоимения, которые в запросе не значат
// ничего, а в описаниях методов есть почти везде. Слова короче трёх букв
// отсеиваются длиной и сюда не входят.
var apiStopWords = map[string]bool{
	"или": true, "для": true, "при": true, "что": true, "это": true, "как": true, "его": true,
	"под": true, "над": true, "без": true, "если": true, "либо": true, "чтобы": true, "также": true,
}

// apiQueryTerms сводит запрос к словам: без слов короче трёх букв («в», «по»,
// «на»), без служебных слов и без повторов. Повтор считается по словоформе:
// «цена цену» это одно слово, иначе оно весило бы в охвате дважды. Слова сверх
// apiMaxTerms отбрасываются.
func apiQueryTerms(query string) []string {
	var out []string
words:
	for _, w := range apiWords(query) {
		if len([]rune(w)) <= 2 || apiStopWords[w] {
			continue
		}
		for _, kept := range out {
			if apiSameWord(kept, w) {
				continue words
			}
		}
		if out = append(out, w); len(out) == apiMaxTerms {
			break
		}
	}
	return out
}

// apiWords разбивает текст на слова в нижнем регистре. Идентификатор делится
// по границам CamelCase: «РазложитьСтрокуВМассив» даёт «разложить», «строку»,
// «в», «массив»; подряд идущие заглавные считаются одним словом, пока за ними
// не начнётся следующее («URLАдрес» даёт «url», «адрес»).
func apiWords(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, apiFold(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(text)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r):
			if len(cur) > 0 && unicode.IsUpper(r) {
				prev := cur[len(cur)-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if !unicode.IsLetter(prev) || unicode.IsLower(prev) || nextLower {
					flush()
				}
			} else if len(cur) > 0 && unicode.IsDigit(cur[len(cur)-1]) {
				flush()
			}
			cur = append(cur, r)
		case unicode.IsDigit(r):
			if len(cur) > 0 && !unicode.IsDigit(cur[len(cur)-1]) {
				flush()
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// apiFold: нижний регистр, «ё» как «е»: в запросе её пишут как придётся.
func apiFold(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "ё", "е")
}

// Сравнение слов. Имя метода стоит в одной форме («Удалить», «Индексацию»),
// задачу описывают в какой придётся («удаление», «индексировать»), и усечение
// окончания их не сводит: основа запроса выходит длиннее слова в имени. Два
// слова считаются одним, когда у них общее начало, а всё, чем они расходятся,
// складывается из русских словообразующих суффиксов и окончаний:
//
//	удал|ение ~ удал|ить, формат|ирование ~ формат|, индекс|ировать ~ индекс|ацию
//
// Остаток, не похожий на суффикс, значит другое слово: пропи|сью и проп|уск,
// стро|ку и стро|итель, цен|а и цен|тр.
//
// Правило грубое в обе стороны, и это известно. Оно не сводит корни с
// чередованием («выборка» и «выбрать») и синонимы: их в запрос дописывает тот,
// кто ищет. И оно сводит чужие слова, у которых остаток случайно похож на
// суффикс: «права» и «правило», «карта» и «картина», «строку» и «строить».
// Прежнее правило давало те же ложные пары, новых правило не добавило.

// apiInflection: падежные и родовые окончания существительных и
// прилагательных; «s» и «es» для латинских имён (file и files, http и https).
const apiInflection = `(?:ями|ами|ого|ему|ому|ыми|ими|ием|иях|иям|ой|ей|ый|ий|ые|ие|ых|их|ым|им|ую|юю|ая|яя|ое|ее|ия|ию|ем|ом|ам|ям|ах|ях|ов|ев|ью|es|а|я|е|и|й|ю|у|ы|о|ь|s)?`

var (
	// reAPISuffix: остаток слова после общего начала. По порядку: «ир» и
	// глагольный суффикс (-ова-, -ива-, -ыва-, -ева-); тематическая гласная;
	// суффикс отглагольного существительного, инфинитива, причастия или
	// прилагательного; суффикс -к- с беглой гласной (загруз|ка, ошиб|ок,
	// настро|йки, настро|ек); окончание.
	reAPISuffix = regexp.MustCompile(`^(?:ир)?(?:ов|ив|ыв|ев)?(?:а|я|е|и)?(?:ни|ти|ци|ть|нн|н|ск|ем|им|ющ|вш|л)?(?:(?:й|о|е)?к)?` + apiInflection + `$`)
	// reAPIInflection: остаток из одного окончания.
	reAPIInflection = regexp.MustCompile(`^` + apiInflection + `$`)
)

// apiMinCommonPrefix: общее начало короче трёх букв словом не считается, а
// при общем начале ровно в три буквы остатком может быть только окончание:
// «цена» и «цену» одно слово, «вид» и «видимость», «цена» и «ценник» разные.
const apiMinCommonPrefix = 3

// apiSameWord сообщает, одно ли слово записано в a и b разными формами. Слова
// приходят уже свёрнутыми apiFold. Сравнение симметрично.
func apiSameWord(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	common := 0
	for common < len(ra) && common < len(rb) && ra[common] == rb[common] {
		common++
	}
	if common < apiMinCommonPrefix {
		return false
	}
	restA, restB := string(ra[common:]), string(rb[common:])
	if common == apiMinCommonPrefix {
		return reAPIInflection.MatchString(restA) && reAPIInflection.MatchString(restB)
	}
	return reAPISuffix.MatchString(restA) && reAPISuffix.MatchString(restB)
}

// apiSearchText — метод, подготовленный к сравнению с запросом.
type apiSearchText struct {
	name, module, summary []string
}

// apiTermSet — набор слов запроса, бит на слово (слов не больше apiMaxTerms).
type apiTermSet uint64

func (s apiTermSet) has(term int) bool { return s&(1<<uint(term)) != 0 }

func (s apiTermSet) with(term int) apiTermSet { return s | 1<<uint(term) }

// apiMatcher сравнивает методы со словами одного запроса и копит по ходу,
// сколько методов сравнено и у скольких нашлось каждое слово: из этого
// выводится редкость слова. Словарь имён и описаний конфигурации на порядок
// меньше числа слов во всех методах, поэтому исход сравнения пары «слово
// запроса, слово метода» запоминается на вызов.
type apiMatcher struct {
	terms    []string
	seen     []map[string]bool
	compared int
	matched  []int
}

func newAPIMatcher(terms []string) *apiMatcher {
	m := &apiMatcher{terms: terms, seen: make([]map[string]bool, len(terms)), matched: make([]int, len(terms))}
	for i := range m.seen {
		m.seen[i] = map[string]bool{}
	}
	return m
}

func (m *apiMatcher) has(term int, words []string) bool {
	for _, w := range words {
		same, known := m.seen[term][w]
		if !known {
			same = apiSameWord(m.terms[term], w)
			m.seen[term][w] = same
		}
		if same {
			return true
		}
	}
	return false
}

// match сравнивает метод с запросом: covered — какие слова запроса нашлись
// хоть где-то, score — сумма весов мест совпадения.
func (m *apiMatcher) match(d apiSearchText) (covered apiTermSet, score int) {
	m.compared++
	for i := range m.terms {
		hit := 0
		if m.has(i, d.name) {
			hit += apiWeightName
		}
		if m.has(i, d.module) {
			hit += apiWeightModule
		}
		if m.has(i, d.summary) {
			hit += apiWeightSummary
		}
		if hit > 0 {
			covered = covered.with(i)
			m.matched[i]++
			score += hit
		}
	}
	return covered, score
}

// weights: вес каждого слова запроса в охвате по его редкости среди всех
// сравненных методов. Зовётся после того, как сравнены все методы.
//
// Слово, которое есть почти в каждом методе («получить», «данные»,
// «таблица»), о назначении метода не говорит ничего, а редкое («индексация»,
// «факсимиле») называет его почти однозначно. Простой счёт совпавших слов
// ставил метод с двумя общими словами выше метода с одним точным.
func (m *apiMatcher) weights() []float64 {
	out := make([]float64, len(m.matched))
	for i, n := range m.matched {
		if n > 0 {
			out[i] = math.Log(1 + float64(m.compared)/float64(n))
		}
	}
	return out
}

// apiCoverage складывает веса найденных слов запроса.
func apiCoverage(covered apiTermSet, weights []float64) float64 {
	var sum float64
	for i, w := range weights {
		if covered.has(i) {
			sum += w
		}
	}
	return sum
}
