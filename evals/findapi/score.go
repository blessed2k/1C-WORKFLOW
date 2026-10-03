package main

import (
	"fmt"
	"sort"
	"strings"
)

// Глубины выдачи, на которых меряется поиск.
const (
	// scoreFull: сколько методов секции find_api отдаёт полными описаниями,
	// когда limit не задан.
	scoreFull = 10
	// scoreShown: сколько методов секции агент видит без limit: полные
	// описания и короткий список за ними.
	scoreShown = 20
	// scoreDepth: потолок инструмента; глубже агент не видит ни при каком limit.
	scoreDepth = 50
)

// scoreCutoffs: глубины, на которых считается доля найденных. Читает выдачу
// не человек, а модель, которой двадцать строк прочесть нетрудно, поэтому
// главная глубина scoreShown: метод либо попался агенту на глаза, либо нет.
var scoreCutoffs = []int{1, 3, scoreFull, scoreShown, scoreDepth}

// result: исход одного запроса набора.
type result struct {
	ID string `json:"id"`
	// Shown: место первого засчитанного метода в выдаче по умолчанию
	// (полные описания и короткий список, scoreShown методов секции), с
	// единицы; 0: его там нет.
	//
	// Место в выдаче по умолчанию и место в выдаче на scoreDepth это не одно
	// и то же: find_api уводит устаревшие методы в хвост уже после среза, и в
	// длинной выдаче на их место в первой десятке поднимаются другие.
	Shown int `json:"shown"`
	// Deep: то же в выдаче на scoreDepth методов.
	Deep       int     `json:"deep"`
	DurationMS float64 `json:"durationMs"`
	// Top: первые методы секции пары: по ним видно, что поиск предложил
	// вместо ожидаемого.
	Top []string `json:"top,omitempty"`
}

// hit сообщает, найден ли метод в первых k.
func (r result) hit(k int) bool {
	if k <= scoreShown {
		return r.Shown > 0 && r.Shown <= k
	}
	return r.Shown > 0 || (r.Deep > 0 && r.Deep <= k)
}

// rank: место метода для среднего обратного места: в выдаче по умолчанию,
// а если его там нет, в глубокой.
func (r result) rank() int {
	if r.Shown > 0 {
		return r.Shown
	}
	return r.Deep
}

// groupStats: итог по группе запросов.
type groupStats struct {
	N int `json:"n"`
	// Hits: сколько запросов нашли метод в первых k, по scoreCutoffs.
	Hits map[int]int `json:"hits"`
	// MRR: среднее обратное место (0 у ненайденного).
	MRR float64 `json:"mrr"`
}

// catalogInfo: на какой выгрузке снят прогон: версия библиотеки и число
// методов в каталоге.
type catalogInfo struct {
	BSPVersion string `json:"bspVersion"`
	BSP        int    `json:"bsp"`
	Other      int    `json:"other"`
}

// report: итог прогона.
type report struct {
	// Dataset: отпечаток набора: база сравнима только с тем набором, на
	// котором снята.
	Dataset string      `json:"dataset"`
	Catalog catalogInfo `json:"catalog"`
	// Cards: наборы карточек поиска, с которыми шёл прогон (имя и число
	// карточек); пусто: без карточек. База сравнима только с прогоном на тех
	// же карточках.
	Cards  string                `json:"cards,omitempty"`
	Groups map[string]groupStats `json:"groups"`
	// MedianMS: медиана времени вызова find_api при том числе потоков, с
	// которым шёл прогон.
	MedianMS float64 `json:"medianMs"`
	Jobs     int     `json:"jobs"`
}

// groupKeys: в какие группы входит пара. Итог по секции, по её половинам, по
// виду запроса, по слою выборки и по признаку общих слов с именем метода.
func groupKeys(p pair) []string {
	overlap := "без общих слов с именем"
	if p.NameOverlap {
		overlap = "с общими словами с именем"
	}
	variant := "прямой запрос"
	if p.Variant == variantParaphrase {
		variant = "пересказ"
	}
	return []string{
		p.Section,
		p.Section + "/" + p.Split,
		p.Section + "/" + variant,
		p.Section + "/" + p.Split + "/" + variant,
		p.Section + "/" + overlap,
		p.Section + "/слой " + p.Stratum,
	}
}

// positionOf: место первого засчитанного вызова в выдаче, с единицы.
func positionOf(calls, accept []string) int {
	ok := map[string]bool{}
	for _, a := range accept {
		ok[strings.ToLower(a)] = true
	}
	for i, c := range calls {
		if ok[strings.ToLower(c)] {
			return i + 1
		}
	}
	return 0
}

// bestPosition: лучшее из мест в нескольких списках; 0, если метода нет ни в
// одном. Агент читает обе секции ответа, и метод, найденный в соседней
// секции (прикладная обёртка над методом библиотеки), найден.
func bestPosition(accept []string, lists ...[]string) int {
	best := 0
	for _, l := range lists {
		if p := positionOf(l, accept); p > 0 && (best == 0 || p < best) {
			best = p
		}
	}
	return best
}

// summarize сводит исходы запросов в итог по группам.
func summarize(pairs []pair, results []result) (map[string]groupStats, float64, error) {
	byID := map[string]result{}
	for _, r := range results {
		byID[r.ID] = r
	}
	groups := map[string]groupStats{}
	rr := map[string]float64{}
	var durations []float64
	for _, p := range pairs {
		r, ok := byID[p.ID]
		if !ok {
			return nil, 0, fmt.Errorf("пара %s: нет исхода", p.ID)
		}
		durations = append(durations, r.DurationMS)
		for _, key := range groupKeys(p) {
			g := groups[key]
			if g.Hits == nil {
				g.Hits = map[int]int{}
				for _, k := range scoreCutoffs {
					g.Hits[k] = 0
				}
			}
			g.N++
			if rank := r.rank(); rank > 0 {
				rr[key] += 1 / float64(rank)
			}
			for _, k := range scoreCutoffs {
				if r.hit(k) {
					g.Hits[k]++
				}
			}
			groups[key] = g
		}
	}
	for key, g := range groups {
		g.MRR = rr[key] / float64(g.N)
		groups[key] = g
	}
	median := 0.0
	if len(durations) > 0 {
		sort.Float64s(durations)
		median = durations[len(durations)/2]
	}
	return groups, median, nil
}

// ratchetGroups: группы и глубины, по которым прогон сравнивается с базой.
// Сравниваются секции по половинам на глубинах scoreFull, scoreShown и
// scoreDepth: что агент получит полным описанием, что увидит по умолчанию и
// что способен увидеть вообще. Первая тройка и первое место печатаются, но
// прогон не валят: перестановка внутри первой десятки для читающей модели не
// потеря.
//
// Проверочная половина сравнивается наравне с настроечной: это страховка от
// регрессии, а не настройка. Правила подбираются по половине dev, качество
// называется по test; кто подбирает правила, глядя на test, оценки не имеет.
var (
	ratchetGroups  = []string{sectionBSP + "/" + splitDev, sectionBSP + "/" + splitTest, sectionOther + "/" + splitDev, sectionOther + "/" + splitTest}
	ratchetCutoffs = []int{scoreFull, scoreShown, scoreDepth}
)

// ratchetFirstSlack: на сколько пар первое место группы может упасть против
// базы с карточками. Карточки почти не меняют, виден ли метод вообще, они
// поднимают его к началу списка: это их главный измеримый эффект, и без
// сравнения первого места его потеря прошла бы как «не ниже базы». Допуск
// нужен потому, что перестановка двух-трёх пар на первом месте случается от
// любой правки рядом.
const ratchetFirstSlack = 3

// compare сверяет прогон с базой и возвращает список падений; пустой список
// значит «не ниже базы». Ошибка: база снята на другом наборе, на другой
// выгрузке или с другими карточками поиска.
func compare(cur, base report) ([]string, error) {
	if cur.Dataset != base.Dataset {
		return nil, fmt.Errorf("база снята на другом наборе (%s против %s): сравнивать нечего, снимите базу заново", short(base.Dataset), short(cur.Dataset))
	}
	if cur.Catalog != base.Catalog {
		return nil, fmt.Errorf("база снята на другой выгрузке (%+v против %+v): сравнивать нечего, снимите базу заново", base.Catalog, cur.Catalog)
	}
	if cur.Cards != base.Cards {
		return nil, fmt.Errorf("база снята с другими карточками поиска (%q против %q): сравнивать нечего, задайте те же карточки флагом -cards или снимите базу заново", base.Cards, cur.Cards)
	}
	var drops []string
	for _, key := range ratchetGroups {
		b, ok := base.Groups[key]
		if !ok {
			continue
		}
		c := cur.Groups[key]
		for _, k := range ratchetCutoffs {
			if c.Hits[k] < b.Hits[k] {
				drops = append(drops, fmt.Sprintf("%s: в первых %d найдено %d из %d, в базе %d", key, k, c.Hits[k], c.N, b.Hits[k]))
			}
		}
		if base.Cards != "" && c.Hits[1] < b.Hits[1]-ratchetFirstSlack {
			drops = append(drops, fmt.Sprintf("%s: на первом месте %d из %d, в базе с карточками %d (допуск %d)", key, c.Hits[1], c.N, b.Hits[1], ratchetFirstSlack))
		}
	}
	return drops, nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// formatReport печатает итог таблицей: группа, число запросов, доля найденных
// на каждой глубине.
func formatReport(rep report) string {
	keys := make([]string, 0, len(rep.Groups))
	for k := range rep.Groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%-36s %5s", "группа", "пар")
	for _, k := range scoreCutoffs {
		fmt.Fprintf(&b, " %11s", fmt.Sprintf("в первых %d", k))
	}
	fmt.Fprintf(&b, " %6s\n", "MRR")
	for _, key := range keys {
		g := rep.Groups[key]
		fmt.Fprintf(&b, "%-36s %5d", key, g.N)
		for _, k := range scoreCutoffs {
			fmt.Fprintf(&b, " %4d (%3.0f%%)", g.Hits[k], 100*float64(g.Hits[k])/float64(g.N))
		}
		fmt.Fprintf(&b, " %6.3f\n", g.MRR)
	}
	if rep.Cards != "" {
		fmt.Fprintf(&b, "карточки поиска: %s\n", rep.Cards)
	} else {
		fmt.Fprintf(&b, "карточки поиска: нет\n")
	}
	fmt.Fprintf(&b, "медиана вызова find_api: %.0f мс при %d одновременных запросах\n", rep.MedianMS, rep.Jobs)
	return b.String()
}
