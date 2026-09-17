package retrieve

// Бакеты выходных списков Result (§24 шаг 5 «budgeted packing»).
const (
	bucketFact            = "fact"
	bucketSignature       = "signature"
	bucketSnippet         = "snippet"
	bucketMetadataSummary = "metadataSummary"
	bucketRelation        = "relation"
)

func (c *candidate) toFact() Fact {
	return Fact{
		Category: c.category, Display: c.display, Detail: c.detail, Component: c.component,
		Resolution: c.resolution, Confidence: c.confidence, Provenance: c.provenance, WhyIncluded: c.whyIncluded,
	}
}

func (c *candidate) toSignature() Signature {
	return Signature{
		UID: c.refUID, Component: c.component, Module: c.module, Name: c.display, Kind: c.kindLabel,
		Text: c.text, Span: c.span, Resolution: c.resolution, Confidence: c.confidence, WhyIncluded: c.whyIncluded,
	}
}

func (c *candidate) toSnippet() Snippet {
	return Snippet{
		Category: c.kindLabel, UID: c.refUID, Component: c.component, Module: c.module, Text: c.text,
		Truncated: c.truncated, ResourceURI: c.resourceURI, Span: c.span, Confidence: c.confidence,
		WhyIncluded: c.whyIncluded,
	}
}

func (c *candidate) toMetadataSummary() MetadataSummary {
	return MetadataSummary{
		Type: c.kindLabel, Name: c.display, Component: c.component, MemberCount: c.memberCount,
		Members: c.members, FormCount: c.formCount, WhyIncluded: c.whyIncluded,
	}
}

func (c *candidate) toRelation() Relation {
	return Relation{
		Kind: c.kindLabel, From: c.fromDisplay, To: c.display, Detail: c.detail, Resolution: c.resolution,
		Confidence: c.confidence, Component: c.component, Span: c.span, WhyIncluded: c.whyIncluded,
	}
}

// packedOutput — типизированные списки, ужe разложенные из упакованных
// кандидатов по bucket.
type packedOutput struct {
	facts             []Fact
	signatures        []Signature
	snippets          []Snippet
	metadataSummaries []MetadataSummary
	relations         []Relation
}

func appendPacked(out *packedOutput, c *candidate) {
	switch c.bucket {
	case bucketSignature:
		out.signatures = append(out.signatures, c.toSignature())
	case bucketSnippet:
		out.snippets = append(out.snippets, c.toSnippet())
	case bucketMetadataSummary:
		out.metadataSummaries = append(out.metadataSummaries, c.toMetadataSummary())
	case bucketRelation:
		out.relations = append(out.relations, c.toRelation())
	default:
		out.facts = append(out.facts, c.toFact())
	}
}

// maxExcludedHighScoring — потолок excludedHighScoring[] (R47/R49: «показывает,
// что не влезло», не «показывает вообще всё, что не влезло»).
const maxExcludedHighScoring = 20

// packBudget — §24 шаг 5+6: обязательные категории первыми (в фиксированном
// порядке requiredCategories(intent)), затем контекстные кандидаты в общем
// порядке score; дедупликация уже сделана на этапе сборки кандидатов (id
// уникален по построению — addCandidate в expand.go отбрасывает повторы);
// usedChars никогда не превышает budget — каждый кандидат admits только если
// он ЦЕЛИКОМ помещается в остаток бюджета (инвариант «partial/missing, а не
// превышение» держится тем, что кандидат либо влезает целиком в уже
// минимальном представлении c.charCost, либо не влезает вовсе — c.text уже
// построен build-стороной как «минимальное представление», см. expand.go).
func packBudget(cands []*candidate, required []string, budget int, collected map[string]bool) (packedOutput, []CoverageEntry, []ExcludedItem, int) {
	byCategory := map[string][]*candidate{}
	var optional []*candidate
	requiredSet := map[string]bool{}
	for _, r := range required {
		requiredSet[r] = true
	}
	for _, c := range cands {
		if c.category != "" && requiredSet[c.category] {
			byCategory[c.category] = append(byCategory[c.category], c)
		} else {
			optional = append(optional, c)
		}
	}

	var out packedOutput
	var coverage []CoverageEntry
	var excluded []*candidate
	used := 0

	admit := func(c *candidate) bool {
		if used+c.charCost > budget {
			return false
		}
		used += c.charCost
		appendPacked(&out, c)
		return true
	}

	for _, cat := range required {
		items := byCategory[cat]
		sortCandidates(items)
		total := len(items)
		returned := 0
		usedResourceInCat := false
		var resourceURIForCat string
		for _, it := range items {
			if admit(it) {
				returned++
				if it.usedResource {
					usedResourceInCat = true
					if resourceURIForCat == "" {
						resourceURIForCat = it.resourceURI
					}
				}
			} else {
				excluded = append(excluded, it)
			}
		}
		entry := CoverageEntry{Category: cat, ReturnedCount: returned, TotalCount: total}
		switch {
		case total == 0 && collected[cat]:
			// «Категория собрана, фактов нет» (ADR-030, R26/R28) — и только по
			// ЗАЯВЛЕНИЮ сборщика (D03): документ, который ничего не читает из
			// регистров и не имеет подписок, полон именно этой пустотой.
			// Категория, которую никто не собирал (нет владельца-анкера, не
			// совпало ничего, механизм не построен), остаётся missing: иначе
			// ответ, не нашедший ничего, объявляет себя полным.
			entry.Status = CompleteEmpty
		case returned == 0:
			// totalCount > 0 и не влезло ничего — настоящая неполнота, статус
			// прежний.
			entry.Status = Missing
		case returned < total:
			entry.Status = Partial
		case usedResourceInCat:
			entry.Status = CompleteViaResource
			entry.SuggestedNextAction = "прочитайте ресурс " + resourceURIForCat + " за полным содержимым категории " + cat
		default:
			entry.Status = CompleteInline
		}
		coverage = append(coverage, entry)
	}

	sortCandidates(optional)
	for _, it := range optional {
		if !admit(it) {
			excluded = append(excluded, it)
		}
	}

	sortCandidates(excluded)
	var excludedItems []ExcludedItem
	for i, c := range excluded {
		if i >= maxExcludedHighScoring {
			break
		}
		excludedItems = append(excludedItems, ExcludedItem{
			Category: c.category, Display: c.display, Score: c.score,
			Reason: "не поместилось в бюджет символов",
		})
	}

	return out, coverage, excludedItems, used
}

// ambiguityCharCost — оценка веса одной Ambiguity в итоговом ответе:
// Subject+Note+все Options, в рунах (тот же критерий, что charCost
// кандидатов, spec «бюджет в символах, не в токенах»).
func ambiguityCharCost(a Ambiguity) int {
	cost := runeLen(a.Subject) + runeLen(a.Note)
	for _, o := range a.Options {
		cost += runeLen(o)
	}
	return cost
}

// boundAmbiguities — находка доводки P6, вторая половина: ambiguities
// писались в ответ ЦЕЛИКОМ, вне проверки char budget (используется только
// packBudget'ом, отдельно от списка ambiguities) — реальные ответы весили
// 74312/40405 байт при budgetChars=16000 по умолчанию. Здесь тот же приём,
// что packBudget.admit: entry принимается только если целиком помещается и в
// потолок числа записей (maxAmbiguities), и в остаток бюджета символов;
// первая же непомещающаяся запись останавливает упаковку (детерминированный
// порядок — как пришли из findAnchors, without re-sorting), а не пытается
// найти место для более поздних меньших записей — та же простая семантика
// «влезло целиком по порядку», что уже принята для excludedHighScoring и
// packBudget.admit, не более сложная best-fit укладка. Дропнутые записи не
// молчаливо теряются — droppedCount уходит в honest warning в Build, тем же
// приёмом, что call_graph_expansion_truncated/references_truncated.
func boundAmbiguities(in []Ambiguity, budgetRemaining int) (out []Ambiguity, usedChars int, droppedCount int) {
	for i, a := range in {
		if len(out) >= maxAmbiguities {
			droppedCount += len(in) - i
			break
		}
		cost := ambiguityCharCost(a)
		if usedChars+cost > budgetRemaining {
			droppedCount += len(in) - i
			break
		}
		usedChars += cost
		out = append(out, a)
	}
	return out, usedChars, droppedCount
}

// deriveSufficiency — spec «Бюджет и покрытие»: все complete_inline ->
// sufficient_inline; нет partial/missing и есть хотя бы один
// complete_via_resource -> requires_resource_fetch; иначе insufficient.
// complete_empty (ADR-030) считается наравне с complete_inline: категория
// собрана и пуста честно, ответ от этого неполным не становится.
func deriveSufficiency(coverage []CoverageEntry) string {
	allInline := true
	anyPartialOrMissing := false
	anyResource := false
	for _, c := range coverage {
		switch c.Status {
		case CompleteInline, CompleteEmpty:
		case CompleteViaResource:
			allInline = false
			anyResource = true
		case Partial, Missing:
			allInline = false
			anyPartialOrMissing = true
		}
	}
	switch {
	case allInline:
		return SufficientInline
	case !anyPartialOrMissing && anyResource:
		return RequiresResourceFetch
	default:
		return Insufficient
	}
}

// missingRequiredFrom собирает названия категорий со статусом partial/missing.
// complete_empty в список не попадает (ADR-030): назвать отсутствующей
// категорию, по которой в индексе честно нет фактов, — это ложная подсказка
// «поищите ещё», а не диагноз.
func missingRequiredFrom(coverage []CoverageEntry) []string {
	var out []string
	for _, c := range coverage {
		if c.Status == Partial || c.Status == Missing {
			out = append(out, c.Category)
		}
	}
	return out
}
