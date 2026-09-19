package retrieve

import (
	"math"
	"sort"
	"unicode/utf8"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// candidate — единая внутренняя форма факта до упаковки: несёт достаточно
// полей, чтобы материализоваться в ЛЮБОЙ из типизированных выходных списков
// (Fact/Signature/Snippet/MetadataSummary/Relation) через to*() в pack.go —
// одна scoring/packing реализация на все виды фактов, конкретный тип решает
// bucket.
type candidate struct {
	id       string // ключ дедупликации, уникален в пределах одного вызова Build
	bucket   string // "fact" | "signature" | "snippet" | "metadataSummary" | "relation"
	category string // тег обязательной категории (см. requiredCategoryMap); "" — контекстный факт, не входит в requiredCoverage

	refUID      string
	component   string
	module      string
	display     string
	fromDisplay string // для relation: отображаемое имя источника ребра
	kindLabel   string // Signature.Kind / Snippet.Category / Relation.Kind / MetadataSummary.Type
	detail      string
	provenance  string

	text        string
	truncated   bool
	resourceURI string
	// usedResource — true, если пакованное представление ЭТОГО кандидата
	// требует похода за resourceURI, чтобы получить полный текст (усечённый
	// snippet, агрегированный relations-список). Управляет complete_inline
	// vs complete_via_resource для категории, в которую попал этот кандидат.
	usedResource bool

	span       domain.Span
	resolution string
	confidence domain.Confidence

	members     []MetadataMemberSummary
	memberCount int
	formCount   int

	whyIncluded string

	// Входы формулы scoring (spec «Доказанная релевантность»): score =
	// w_intent(kind) * anchorStrength * direction * distanceDecay(d) *
	// confidence * freshness * uniqueness * componentPriority / charCost.
	depth             int
	anchorStrength    float64
	direction         float64
	componentPriority float64
	score             float64 // вычисляется computeScore, читается при сортировке/паковке
	charCost          int     // рун в text (или display+detail, когда text пуст) — стоимость паковки
}

// runeLen — длина строки в символах (rune), не байтах: бюджет считается «в
// символах, не в токенах» (spec §Бюджет), и русский/BSL текст многобайтовый.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// categoryWeight — единая таблица весов вида факта (w_intent(kind) в формуле,
// spec: «веса живут в коде одной таблицей»). Ключ — category кандидата.
// Definition/binding/structure/query_text — максимум (1.0): это то самое
// «начинать с определения и непосредственного места задачи» (R45/R48).
// Контекстные категории без официального требования (queries внутри тела
// bugfix, member-счётчики) — ниже 0.6, чтобы обязательные категории почти
// всегда побеждали их за место в бюджете при прочих равных.
var categoryWeight = map[string]float64{
	"definition":      1.00,
	"binding":         1.00,
	"structure":       1.00,
	"query_text":      1.00,
	"posting_handler": 1.00,

	"body":               0.90,
	"handler":            0.90,
	"handler_body":       0.90,
	"writes_movements":   0.90,
	"movements":          0.90,
	"schema":             0.85,
	"owner_symbol":       0.80,
	"owning_symbols":     0.80,
	"register_access":    0.80,
	"references":         0.85,
	"callers":            0.85,
	"callees":            0.85,
	"caller":             0.85,
	"callee":             0.85,
	"server_calls":       0.80,
	"usages":             0.75,
	"tables_fields":      0.70,
	"interceptors":       0.70,
	"handler_intercepts": 0.55,
	// posting_handler_intercepts — необязательная категория posting (П2.3):
	// вес тот же, что у handler_intercepts формы, природа факта одна.
	"posting_handler_intercepts": 0.55,
	"attributes":                 0.65,
	"subscriptions":              0.70,
	"roles":                      0.85,
	"rights":                     0.85,
	"rls":                        0.70,
	"profiles":                   0.55,
	"forms":                      0.55,
	"exchanges":                  0.55,

	// writer_intercepts (register) и query_intercepts (query): необязательные
	// категории effective-вида (ADR-035): вес тот же, что у
	// posting_handler_intercepts, природа факта одна.
	"writer_intercepts": 0.55,
	"query_intercepts":  0.55,

	// Контекстные (не входят ни в одну requiredCategoryMap запись, но
	// доказали релевантность через anchor+edge — см. §Доказанная релевантность).
	"query_in_body":  0.55,
	"register_reads": 0.45,
	"member_summary": 0.60,
	"scheduled_job":  0.40,
}

// weightFor возвращает вес категории, 0.5 для неизвестной (не должно
// случаться в реальном пайплайне — все конструкторы кандидатов используют
// категории из этой таблицы; конформность проверена unit-тестом).
func weightFor(category string) float64 {
	if w, ok := categoryWeight[category]; ok {
		return w
	}
	return 0.5
}

// distanceDecay(d) = 0.6^d — прямой anchor (d=0, собственное определение)
// весит максимально, каждый следующий хоп expansion теряет вес геометрически
// (архитектура §24 шаг 4 «distanceDecay(d)»).
func distanceDecay(d int) float64 {
	if d < 0 {
		d = 0
	}
	return math.Pow(0.6, float64(d))
}

// componentPriorityFor — 1.0 в своём компоненте (том же, что anchor), 0.8 в
// компоненте вида extension (перехватчики/расширения всё ещё релевантны, но
// не основной слой), 0.65 в прочем стороннем компоненте. Реальный, не
// декоративный множитель формулы: он снижает шум из дальних слоёв.
func componentPriorityFor(component, anchorComponent string, extensionComponents map[string]bool) float64 {
	if component == anchorComponent {
		return 1.0
	}
	if extensionComponents[component] {
		return 0.8
	}
	return 0.65
}

// computeScore заполняет c.score по формуле §24 шага 4/spec «Доказанная
// релевантность». freshness и uniqueness — константы 1.0 в этой волне
// (упрощение, названо явно): freshness факта внутри одной read-транзакции
// совпадает с freshness всего ответа (уже несётся в Result.Warnings/Stale
// отдельно, per-факт различий нет — весь снапшот одного поколения);
// uniqueness=1.0, потому что дедупликация по id уже устраняет повторы ДО
// scoring — отдельный штраф за «похожесть» без второго кандидата на то же
// место не за что применять. Множители тем не менее присутствуют в формуле
// буквально (a не опущены «для простоты»), чтобы код читался как формула
// спецификации, а не как её пересказ.
func (c *candidate) computeScore() {
	const freshness = 1.0
	const uniqueness = 1.0
	cost := float64(c.charCost)
	if cost < 1 {
		cost = 1
	}
	c.score = weightFor(c.category) * c.anchorStrength * c.direction * distanceDecay(c.depth) *
		float64(c.confidence) * freshness * uniqueness * c.componentPriority / cost
}

// sortCandidates — детерминированный порядок: score по убыванию, затем id по
// возрастанию (стабильный tie-break — два одинаковых вызова обязаны давать
// идентичный порядок паковки, R «детерминизм»).
func sortCandidates(cs []*candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		return cs[i].id < cs[j].id
	})
}
