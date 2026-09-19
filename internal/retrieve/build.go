package retrieve

import (
	"context"
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Build реализует get_context_for_task целиком поверх ОДНОЙ уже открытой
// read-транзакции (§18.2: «одна read-транзакция на весь MCP-вызов» — вызов
// приходит сюда с tx, уже открытой вызывающим через store.Store.Read).
// Псевдокод §24 реализован буквально, шаг за шагом:
//
//  1. normalizeBudgetChars — один раз, дальше жёсткий потолок.
//  2. freshness/precheck — уже выполнены ДО вызова Build (см. Run,
//     freshness.go): Build читает исход через req.Stale/StaleReason.
//  3. одна read-транзакция — это и есть tx.
//  4. classifyIntent — rule-based, таблица правил в intent.go.
//  5. anchors — точный/структурный/подстрочный/FTS lookup, anchors.go.
//  6. typed expansion — по одному builder'у на intent, expand.go/expand2.go.
//  7. scoring — candidate.computeScore, единая таблица весов.
//  8. budgeted packing — pack.go: обязательные категории первыми,
//     usedChars<=budget всегда.
//  9. sufficiency check — requiredCoverage[] + sufficiencyStatus.
func Build(ctx context.Context, tx *store.ReadTx, req Request) (Result, error) {
	return buildWithSymbols(ctx, tx, nil, req)
}

// buildWithSymbols — то же самое с ПОДМЕНЁННЫМ источником символов
// перехватчиков (symbolFinder, effective.go). nil означает «как в
// production»: источник выводится из транзакции конструктором newBuildCtx.
// Непустое значение передаёт только тест, которому нужен отказ чтения,
// недостижимый на живой транзакции.
func buildWithSymbols(ctx context.Context, tx *store.ReadTx, symbols symbolFinder, req Request) (Result, error) {
	budget := normalizeBudgetChars(req.BudgetChars, req.BudgetTokens)
	maxDepth := req.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	if maxDepth > MaxMaxDepth {
		maxDepth = MaxMaxDepth
	}
	includeCode := req.IncludeCode
	if includeCode == "" {
		includeCode = IncludeSignatures
	}

	gen, err := tx.Generation()
	if err != nil {
		return Result{}, fmt.Errorf("get_context_for_task: generation: %w", err)
	}

	task := strings.TrimSpace(req.Task)
	if task == "" {
		return Result{}, fmt.Errorf("get_context_for_task: task не может быть пустым")
	}

	intent := classifyIntent(task, req.FocusHints)
	required := requiredCategories(intent.Primary)

	// D08 (закрытие долга таска 15): view раньше принимался схемой input'а
	// (см. jsonschema в cmd/mcp1c/idx_context.go) и молча откатывался на raw
	// для ЛЮБОГО значения != "raw" — включая опечатку. Теперь effective
	// частично реализован (effective.go — definition/interceptors у
	// bugfix/signature-change, handler_intercepts у form; см.
	// effectiveAwareIntent ниже), и опечатка в view обязана быть замечена как
	// ошибка, а не тихо стать raw — тот же принцип, что internal/app/effective.go:parseView
	// уже применяет к get_symbol/get_object/get_module_structure (тикет 14).
	view := domain.View(strings.TrimSpace(req.View))
	if view == "" {
		view = domain.ViewRaw
	}
	if !view.Valid() {
		return Result{}, fmt.Errorf("get_context_for_task: view %q неизвестен (допустимые значения: raw, effective)", req.View)
	}

	var warnings []Warning
	if view == domain.ViewEffective && !effectiveAwareIntent(intent.Primary) {
		// Честная граница покрытия (D08 п.4): effective сегодня учитывает
		// перехватчики расширений только там, где typed expansion строит
		// definition-подобный факт по конкретному символу/обработчику
		// (bugfix/unknown, signature-change, form, posting) — register/query/
		// rights/add-attribute остаются построены как raw, это НЕ забытый
		// случай, а честно названный предел этой волны (см. handoff/CLAUDE
		// правку JSON-схемы cmd/mcp1c/idx_context.go).
		warnings = append(warnings, Warning{
			Code: "effective_view_partial_coverage",
			Message: fmt.Sprintf(
				"view=effective учитывает перехватчики расширений для intent bugfix/unknown/signature-change/form/posting — для классифицированного intent %q этот вызов по-прежнему построен как raw",
				intent.Primary),
			Hint: "перехватчики конкретного модуля/объекта смотрите отдельно: get_symbol/get_object/get_module_structure с view=effective",
		})
	}
	if req.Stale {
		reason := req.StaleReason
		if reason == "" {
			reason = "неизвестна"
		}
		warnings = append(warnings, Warning{
			Code: "stale_index", Message: fmt.Sprintf("ответ построен по устаревшему поколению %s (возраст %.0fс, причина: %s)", gen, req.StaleAgeSeconds, reason),
			Hint: "передайте freshness=require-fresh, если критична точная синхронность с диском",
		})
	}

	anchors, ambiguities, err := findAnchors(tx, req)
	if err != nil {
		return Result{}, fmt.Errorf("get_context_for_task: anchors: %w", err)
	}
	// П4/R32: единственный вид шума, который снимается по классифицированному
	// intent'у, — омонимия по имени обработчика проведения при заданном
	// объекте (см. suppressPostingHandlerAmbiguity в anchors.go).
	ambiguities = suppressPostingHandlerAmbiguity(intent.Primary, anchors, ambiguities)
	if len(anchors) == 0 {
		return Result{
			Intent: intent, Anchors: []Anchor{},
			RequiredCoverage:  emptyCoverage(required),
			SufficiencyStatus: Insufficient,
			MissingRequired:   required,
			Budget:            BudgetInfo{RequestedChars: budget, UsedChars: 0, EstimatedTokens: 0},
			Warnings: append(warnings, Warning{
				Code:    "no_anchors",
				Message: "ни один anchor не найден точным, структурным, подстрочным или FTS lookup по тексту задачи",
				Hint:    "назовите точное имя символа/объекта в focusHints, либо исследуйте вручную",
			}),
			SuggestedNextTools: []string{"find_symbol", "get_metadata_tree"},
			Generation:         gen,
		}, nil
	}

	comps, cerr := tx.Components()
	extensionComponents := map[string]bool{}
	if cerr == nil {
		for _, c := range comps {
			if c.Kind == string(domain.KindExtension) {
				extensionComponents[c.ID] = true
			}
		}
	}

	bctx := newBuildCtx(tx, req, gen, maxDepth, includeCode, extensionComponents, view)
	if symbols != nil {
		bctx.symbols = symbols // привилегия теста, см. buildWithSymbols
	}

	expansions := make([]anchorExpansion, 0, len(anchors))
	for _, a := range anchors {
		cs, ws := expandForAnchor(bctx, intent.Primary, a)
		expansions = append(expansions, anchorExpansion{anchor: a, candidates: cs, warnings: ws})
	}

	if intent.Primary == IntentForm {
		suppressFormNoiseWhenMatched(expansions)
	}

	var allCandidates []*candidate
	seen := map[string]bool{}
	for _, e := range expansions {
		warnings = append(warnings, e.warnings...)
		for _, c := range e.candidates {
			if c == nil || seen[c.id] {
				continue
			}
			seen[c.id] = true
			allCandidates = append(allCandidates, c)
		}
	}

	warnings = dedupWarnings(warnings)

	// collectedEmptyCategories — заявления сборщиков (D03): только они дают
	// пустой категории право на complete_empty.
	packed, coverage, excluded, used := packBudget(allCandidates, required, budget, bctx.collectedEmptyCategories())

	// Ambiguities (находка доводки P6) раньше писались в ответ мимо этой
	// проверки — здесь тот же жёсткий budget<=budget, что и у packed-контента,
	// плюс потолок числа записей maxAmbiguities (anchors.go).
	remaining := budget - used
	if remaining < 0 {
		remaining = 0
	}
	ambiguities, ambigUsed, ambigDropped := boundAmbiguities(ambiguities, remaining)
	used += ambigUsed
	if ambigDropped > 0 {
		warnings = append(warnings, Warning{
			Code: "ambiguities_truncated",
			Message: fmt.Sprintf("показаны не все найденные неоднозначности — %d из %d записей не поместились (потолок %d записей и/или бюджет символов)",
				ambigDropped, ambigDropped+len(ambiguities), maxAmbiguities),
			Hint: "назовите точное имя/модуль в focusHints, чтобы убрать омонимию, либо увеличьте budgetChars",
		})
	}

	if used > budget {
		// Инвариант, а не ожидаемый путь: packBudget/boundAmbiguities уже
		// гарантируют used<=budget на уровне admit(); эта проверка — защита от
		// будущей регрессии.
		return Result{}, fmt.Errorf("get_context_for_task: usedChars(%d) > budget(%d) — инвариант нарушен", used, budget)
	}

	sufficiency := deriveSufficiency(coverage)
	missing := missingRequiredFrom(coverage)

	return Result{
		Intent:              intent,
		Anchors:             anchors,
		Facts:               packed.facts,
		Signatures:          packed.signatures,
		Snippets:            packed.snippets,
		MetadataSummaries:   packed.metadataSummaries,
		Relations:           packed.relations,
		RequiredCoverage:    coverage,
		SufficiencyStatus:   sufficiency,
		MissingRequired:     missing,
		Ambiguities:         ambiguities,
		Budget:              BudgetInfo{RequestedChars: budget, UsedChars: used, EstimatedTokens: used / TokensToChars},
		ExcludedHighScoring: excluded,
		Warnings:            warnings,
		SuggestedNextTools:  nextToolsFor(intent.Primary, missing),
		Generation:          gen,
	}, nil
}

// expandForAnchor маршрутизирует typed expansion на builder конкретного
// intent (§24 шаг 4). exchange/extension делят builder с bugfix — у них нет
// собственной карты обязательных категорий (см. doc-комментарий intent.go).
func expandForAnchor(bctx *buildCtx, intent string, a Anchor) ([]*candidate, []Warning) {
	switch intent {
	case IntentSignatureChange:
		return expandSignatureChange(bctx, a)
	case IntentRegister:
		return expandRegister(bctx, a)
	case IntentForm:
		return expandForm(bctx, a)
	case IntentAddAttribute:
		return expandAddAttribute(bctx, a)
	case IntentQuery:
		return expandQuery(bctx, a)
	case IntentRights:
		return expandRights(bctx, a)
	case IntentPosting:
		return expandPosting(bctx, a)
	default:
		return expandBugfix(bctx, a)
	}
}

// effectiveAwareIntent — интенты, у которых typed expansion (expand.go/
// expand2.go) реально консультируется с internal/resolve при view=effective
// (D08 п.4а/4б): bugfix/unknown и signature-change — "interceptors" на
// definition-анкере (effective.go:effectiveSignatureInterceptors), form —
// "handler_intercepts" на обработчике формы, posting —
// "posting_handler_intercepts" плюс движения самих перехватчиков на
// обработчике проведения (effective.go:effectivePostingIntercepts, П2.1/R22).
// register/query/rights/add-attribute намеренно не входят — честно объявлено
// эффективным пределом этой волны, не забытым случаем (см. Warning
// effective_view_partial_coverage в Build).
func effectiveAwareIntent(intent string) bool {
	switch intent {
	case IntentBugfix, IntentUnknown, IntentSignatureChange, IntentForm, IntentPosting, IntentRegister, IntentQuery, IntentAddAttribute:
		return true
	default:
		return false
	}
}

// anchorExpansion — результат typed expansion одного анкера (§24 шаг 4),
// ДО того как candidates/warnings слиты в общие срезы Build. Нужен как
// самостоятельный тип, потому что suppressFormNoiseWhenMatched обязана
// видеть, какой анкер (и с каким ObjectName) дал каждую находку — после
// слияния в плоские срезы эта связь теряется безвозвратно.
type anchorExpansion struct {
	anchor     Anchor
	candidates []*candidate
	warnings   []Warning
}

// suppressFormNoiseWhenMatched — находка F3 прогона reindex-timings, дважды
// исправленная после ревью (Codex stop-gate, тот же прогон): у intent=form
// несколько анкеров могут называть один и тот же объект-омоним в разных
// компонентах (findAnchors идёт по MetadataObjectsByNameNormAnyType без учёта
// типа/компонента). Если анкер-омоним ОДНОГО с ним имени И ТИПА дал
// настоящую находку (форму, привязку обработчика), предупреждения
// no_forms/form_binding_not_matched этого анкера — шум про чужой объект под
// тем же именем: их текст несёт только общее для омонимов Display-имя
// (см. комментарий dedupWarnings ниже про ту же причину), поэтому рядом с
// настоящей находкой они читаются как противоречие, а не как честная
// информация о запросе.
//
// ПЕРВАЯ версия гасила эти предупреждения при ЛЮБОЙ настоящей находке во
// всём ответе — не только у анкеров того же имени. Это неверно: если задача
// упоминает два РАЗНЫХ объекта (не омонима один другого), у одного есть
// форма и обработчик, а у другого форм действительно нет, честное no_forms
// про второй объект пряталось за находкой по первому.
//
// ВТОРАЯ версия группировала только по ObjectName — тоже неверно: одно имя
// резолвится MetadataObjectsByNameNormAnyType сразу в НЕСКОЛЬКО объектов
// РАЗНЫХ ВИДОВ (тот же факт, что описан ниже у dedupWarnings — например
// Catalog и CommonPicture с именем «Номенклатура»), и это генуинно разные
// объекты, а не один и тот же объект в разных компонентах. Справочник с
// формой не имеет отношения к картинке без форм под тем же именем.
//
// Мутировать warnings анкера позволительно ТОЛЬКО когда сосед по ГРУППЕ
// ОДНОГО (ObjectType, ObjectName) дал находку — компонент в ключ группировки
// сознательно НЕ входит: это и есть исходный сценарий F3 (тот же тип объекта
// в base и в расширении). Анкеры разных имён или разных типов друг на друга
// не влияют никогда.
//
// Область — только intent=form (R19 брифа reindex-timings: точечный фикс,
// не переписывание intent form целиком); для остальных builder'ов то же
// явление возможно, но решать его для каждого — отдельная задача.
func suppressFormNoiseWhenMatched(expansions []anchorExpansion) {
	groupKey := func(a Anchor) string {
		return a.ObjectType + "\x00" + a.ObjectName
	}
	matchedGroups := make(map[string]bool, len(expansions))
	for _, e := range expansions {
		if len(e.candidates) > 0 {
			matchedGroups[groupKey(e.anchor)] = true
		}
	}
	for i := range expansions {
		if !matchedGroups[groupKey(expansions[i].anchor)] {
			continue
		}
		ws := expansions[i].warnings
		if len(ws) == 0 {
			continue
		}
		out := make([]Warning, 0, len(ws))
		for _, w := range ws {
			if w.Code == "no_forms" || w.Code == "form_binding_not_matched" {
				continue
			}
			out = append(out, w)
		}
		expansions[i].warnings = out
	}
}

// dedupWarnings убирает буквально повторяющиеся предупреждения (совпадают и
// Code, и Message) — регрессия P5 доводки круга 2: частое имя объекта
// метаданных (например «Номенклатура») резолвится exactMetadataLookup сразу
// в НЕСКОЛЬКО объектов разных видов с одинаковым именем (Catalog +
// CommonPicture + DefinedType — findAnchors уже честно помечает это как
// Ambiguity), каждый становится собственным anchor, и typed expansion
// (например expandForm) идёт по anchors ОДИН ПРОХОД НА КАЖДЫЙ — для видов,
// которые структурно не могут иметь форм (картинка, определяемый тип),
// builder корректно не находит форм и честно возвращает warning "no_forms" —
// но текст предупреждения несёт только имя объекта (Display), общее для всех
// омонимов, поэтому одно и то же сообщение дублируется по числу
// нерелевантных omonym-anchor'ов. Дедупликация здесь, на границе агрегации
// warnings в Build (а не точечно внутри expandForm/expandRegister/...),
// потому что тот же паттерн («несколько anchor одного builder'а формируют
// warning с одинаковым текстом») в принципе воспроизводим любым typed
// expansion builder'ом, не только expandForm — общая проблема агрегации, не
// частный случай одного builder'а. Первое вхождение сохраняет порядок и Hint;
// Ambiguities (где перечислены ВСЕ омонимы) дублировать не нужно — там это не
// баг, а сознательно полный список.
func dedupWarnings(in []Warning) []Warning {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]Warning, 0, len(in))
	for _, w := range in {
		key := w.Code + "\x00" + w.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, w)
	}
	return out
}

func emptyCoverage(required []string) []CoverageEntry {
	out := make([]CoverageEntry, len(required))
	for i, r := range required {
		out[i] = CoverageEntry{Category: r, Status: Missing}
	}
	return out
}

// nextToolsFor — §25 «Next» каждого сценария, обобщено по intent + missing.
func nextToolsFor(intent string, missing []string) []string {
	var out []string
	add := func(tools ...string) {
		for _, t := range tools {
			for _, existing := range out {
				if existing == t {
					goto next
				}
			}
			out = append(out, t)
		next:
		}
	}
	switch intent {
	case IntentSignatureChange:
		add("trace_call_graph", "find_references")
	case IntentRegister:
		add("find_register_writes", "get_movements")
	case IntentForm:
		add("form_impact", "get_form_handlers")
	case IntentAddAttribute:
		add("new_object_checklist", "rights_audit", "find_metadata_usages")
	case IntentQuery:
		add("get_query_schema", "query_advisor")
	case IntentPosting:
		add("get_movements")
	case IntentRights:
		add("rights_audit", "visibility_audit")
	default:
		add("find_references", "validate_bsl")
	}
	if len(missing) > 0 {
		add("find_symbol", "get_object")
	}
	return out
}
