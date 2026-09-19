// Package retrieve реализует get_context_for_task (тикет 15, architecture-index.md
// §23-25, spec.md «get_context_for_task» и «Бюджет и покрытие
// get_context_for_task») — ключевой пользовательский результат всего
// прогона: агент описывает задачу словами и получает минимальный достаточный
// контекст, начинающийся с определения и места правки, в пределах жёсткого
// бюджета символов, с честным отчётом о покрытии.
//
// Build — единственная точка входа, потребитель уже построенного: store
// (типизированные выборки тасков 03/11/12/13/15), resolve (типы domain,
// косвенно через store), internal/syntax не используется здесь напрямую.
// Контракт stateless: Build не хранит состояние между вызовами, повторяемость
// обеспечивается детерминированным пайплайном над одной read-транзакцией.
package retrieve

import "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// Request — вход get_context_for_task (архитектура §23).
type Request struct {
	// Task — текст задачи словами, источник классификации intent и anchors.
	Task string
	// Project — необязательное имя проекта для сверки (сверяется вызывающим
	// транспортом, Build его не разрешает — активный проект уже дан через tx).
	Project string
	// ProjectID — разрешённый активный проект, заполняется вызывающим
	// (retrieve.Run) ДО Build: нужен для построения resource-ссылок
	// (onec://symbol/..., onec://references/..., onec://src/...), которые
	// несут project в пути. Build сам проект не резолвит (у него нет
	// доступа к workspace/registry — только tx).
	ProjectID domain.ProjectID
	// ComponentHints сужает поиск anchors к перечисленным компонентам, когда
	// задан (пусто — все компоненты активного проекта).
	ComponentHints []string
	// BudgetChars — жёсткий потолок пакинга в символах. 0 -> DefaultBudgetChars.
	BudgetChars int
	// BudgetTokens — alias BudgetChars, конвертируется TokensToChars, когда
	// BudgetChars не задан явно.
	BudgetTokens int
	// FocusHints — явные имена объектов/символов, проверяются anchors ПЕРЕД
	// разбором текста задачи.
	FocusHints []string
	// View — "raw" (умолчание) | "effective" (D08 — effective.go). effective
	// накладывает слои расширений: bugfix/unknown и signature-change получают
	// precise-факты перехватчиков ("interceptors", через
	// internal/resolve.DeriveIntercepts, не эвристику по имени), form:
	// "handler_intercepts", posting: "posting_handler_intercepts" с движениями
	// перехватчиков, register: "writer_intercepts", query: "query_intercepts"
	// и запросы перехватчиков, add-attribute и rights: заимствования объекта
	// в расширениях (ADR-035). Intent вне effectiveAwareIntent получают
	// Warning effective_view_partial_coverage. Неизвестное значение (не
	// "raw"/"effective"/пусто) — ошибка, не молчаливый откат.
	View string
	// MaxDepth — потолок глубины typed expansion (BFS callers/callees и т.п.).
	// 0 -> DefaultMaxDepth.
	MaxDepth int
	// IncludeCode — "signatures" (умолчание) | "bodies" | "none": верхний
	// предел детальности кода в snippets, вне зависимости от score/бюджета.
	IncludeCode string
	// Freshness — "allow-stale" (умолчание) | "require-fresh". Сам precheck
	// (шаг 2 псевдокода §24) выполняется ДО открытия read-транзакции, то есть
	// до вызова Build — вызывающий (cmd/mcp1c через retrieve.Run) заполняет
	// поля ниже результатом этого precheck.
	Freshness string
	// Stale/StaleReason/StaleAgeSeconds — исход freshness-precheck,
	// выполненного вызывающим ДО Build (index.Service.EnsureFresh). Build не
	// решает свежесть сам — у него уже есть открытая транзакция на каком-то
	// поколении, и эти поля лишь объясняют агенту, что это поколение может
	// быть устаревшим.
	Stale           bool
	StaleReason     string
	StaleAgeSeconds float64
}

// Значения по умолчанию входа (spec «Бюджет и покрытие»).
const (
	DefaultBudgetChars = 16000
	// TokensToChars — фиксированная эвристика конверсии budgetTokens, когда
	// budgetChars явно не задан (spec: «chars = tokens * 3», консервативная
	// оценка для кириллицы и BSL).
	TokensToChars   = 3
	DefaultMaxDepth = 2
	MaxMaxDepth     = 6

	IncludeSignatures = "signatures"
	IncludeBodies     = "bodies"
	IncludeNone       = "none"

	FreshnessAllowStale   = "allow-stale"
	FreshnessRequireFresh = "require-fresh"
)

// CoverageStatus — статус покрытия обязательной категории (spec «Бюджет и
// покрытие», ревью №4). Четыре исходных значения (ADR-015) смысла не меняли;
// complete_empty добавлено таском 06 прогона 3 (ADR-030) пятым — «категория
// собрана, фактов нет».
type CoverageStatus string

const (
	// CompleteInline — категория влезла целиком инлайном.
	CompleteInline CoverageStatus = "complete_inline"
	// CompleteViaResource — категория отдана целиком, но ссылкой на ресурс
	// (полностью, не частично) + suggestedNextAction.
	CompleteViaResource CoverageStatus = "complete_via_resource"
	// Partial — часть категории отдана, returnedCount < totalCount.
	Partial CoverageStatus = "partial"
	// Missing — не отдано ничего при том, что факты БЫЛИ: ни один вариант
	// представления не поместился в бюджет даже минимально (totalCount > 0,
	// returnedCount == 0). Случай «фактов не нашлось вовсе» — CompleteEmpty,
	// а не Missing (ADR-030).
	Missing CoverageStatus = "missing"
	// CompleteEmpty — категория собрана, и в индексе по ней честно нет ни
	// одного факта (totalCount == 0). Это не неполнота ответа: документ без
	// подписок и без чтения регистров полон именно пустотой этих категорий,
	// и deriveSufficiency/missingRequiredFrom такой ответ не роняют (ADR-030,
	// R26–R28).
	CompleteEmpty CoverageStatus = "complete_empty"
)

// Агрегат покрытия (spec «Бюджет и покрытие», правило вывода из requiredCoverage).
const (
	SufficientInline      = "sufficient_inline"
	RequiresResourceFetch = "requires_resource_fetch"
	Insufficient          = "insufficient"
)

// Intent — классификация задачи (§24 шаг 1) с полным распределением весов
// (mixed intent допустим — несколько intent с ненулевым весом).
type Intent struct {
	Primary    string             `json:"primary"`
	Confidence float64            `json:"confidence"`
	Scores     map[string]float64 `json:"scores,omitempty"`
}

// Anchor — точка входа expansion: символ или объект метаданных, найденный по
// явному имени, точным совпадением, подстрочным/синонимным совпадением, FTS
// или структурным совпадением (§24 шаг 2).
type Anchor struct {
	Kind       string  `json:"kind"` // "symbol" | "metadata_object"
	UID        string  `json:"uid,omitempty"`
	ObjectType string  `json:"objectType,omitempty"`
	ObjectName string  `json:"objectName,omitempty"`
	Component  string  `json:"component,omitempty"`
	Display    string  `json:"display"`
	Method     string  `json:"method"` // "exact" | "fuzzy" | "fts" | "structural"
	Strength   float64 `json:"strength"`
}

// Fact — типизированный факт общего вида (списки/структурные факты, не
// подходящие под Signature/Snippet/MetadataSummary/Relation): роли и права,
// подписки, регламентные задания, счётчики.
type Fact struct {
	Category    string            `json:"category"`
	Display     string            `json:"display"`
	Detail      string            `json:"detail,omitempty"`
	Component   string            `json:"component,omitempty"`
	Resolution  string            `json:"resolution,omitempty"`
	Confidence  domain.Confidence `json:"confidence"`
	Provenance  string            `json:"provenance,omitempty"`
	WhyIncluded string            `json:"whyIncluded"`
}

// Signature — сигнатура символа (сигнатура всегда предпочтительнее тела при
// упаковке, §24 шаг 5 «budgeted packing»).
type Signature struct {
	UID         string            `json:"uid,omitempty"`
	Component   string            `json:"component,omitempty"`
	Module      string            `json:"module,omitempty"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind,omitempty"`
	Text        string            `json:"text"`
	Span        domain.Span       `json:"span,omitempty"`
	Resolution  string            `json:"resolution,omitempty"`
	Confidence  domain.Confidence `json:"confidence"`
	WhyIncluded string            `json:"whyIncluded"`
}

// Snippet — точный фрагмент исходника (тело символа, текст запроса), НИКОГДА
// не модуль целиком (§24 шаг 5). Truncated+ResourceURI — большой фрагмент
// отдан частично, с ссылкой на полный текст.
type Snippet struct {
	Category    string            `json:"category"` // "body" | "query_text"
	UID         string            `json:"uid,omitempty"`
	Component   string            `json:"component,omitempty"`
	Module      string            `json:"module,omitempty"`
	Text        string            `json:"text"`
	Truncated   bool              `json:"truncated,omitempty"`
	ResourceURI string            `json:"resourceUri,omitempty"`
	Span        domain.Span       `json:"span,omitempty"`
	Confidence  domain.Confidence `json:"confidence"`
	WhyIncluded string            `json:"whyIncluded"`
}

// MetadataMemberSummary — один член объекта метаданных в сжатом виде.
type MetadataMemberSummary struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// MetadataSummary — сжатая структура объекта метаданных (никогда полная XML
// выгрузка): счётчики + ограниченный список членов.
type MetadataSummary struct {
	Type        string                  `json:"type"`
	Name        string                  `json:"name"`
	Component   string                  `json:"component,omitempty"`
	MemberCount int                     `json:"memberCount"`
	Members     []MetadataMemberSummary `json:"members,omitempty"`
	FormCount   int                     `json:"formCount,omitempty"`
	WhyIncluded string                  `json:"whyIncluded"`
}

// Relation — типизированная связь графа (call_edge/reference/register_access/
// handler_binding/role_right/dependency_edge/query_reference), каждая со
// своим provenance и confidence.
type Relation struct {
	Kind        string            `json:"kind"`
	From        string            `json:"from"`
	To          string            `json:"to,omitempty"`
	Detail      string            `json:"detail,omitempty"`
	Resolution  string            `json:"resolution,omitempty"`
	Confidence  domain.Confidence `json:"confidence"`
	Component   string            `json:"component,omitempty"`
	Span        domain.Span       `json:"span,omitempty"`
	WhyIncluded string            `json:"whyIncluded"`
}

// CoverageEntry — покрытие одной обязательной категории intent.
type CoverageEntry struct {
	Category            string         `json:"category"`
	Status              CoverageStatus `json:"status"`
	ReturnedCount       int            `json:"returnedCount"`
	TotalCount          int            `json:"totalCount"`
	SuggestedNextAction string         `json:"suggestedNextAction,omitempty"`
}

// Ambiguity — неоднозначность, замеченная при разрешении anchors или
// expansion (несколько равно сильных кандидатов, ambiguous-ссылка).
// Truncated/TotalOptions — тот же приём, что Snippet.Truncated: Options
// обрезан потолком (maxAmbiguityOptions, anchors.go) или char budget
// (boundAmbiguities, build.go), TotalOptions несёт реальное число
// найденных кандидатов ДО обрезки — честная отметка, не молчаливое усечение.
type Ambiguity struct {
	Subject      string   `json:"subject"`
	Options      []string `json:"options,omitempty"`
	Note         string   `json:"note,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	TotalOptions int      `json:"totalOptions,omitempty"`
}

// BudgetInfo — бюджет вызова: запрошено, реально использовано, оценка в
// токенах (не обещание точности ни для одной модели, spec §Бюджет).
type BudgetInfo struct {
	RequestedChars  int `json:"requestedChars"`
	UsedChars       int `json:"usedChars"`
	EstimatedTokens int `json:"estimatedTokens"`
}

// ExcludedItem — факт с score>0, не поместившийся в бюджет (R47/R49:
// «excludedHighScoring[] показывает, что не влезло»).
type ExcludedItem struct {
	Category string  `json:"category"`
	Display  string  `json:"display"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason"`
}

// Warning — предупреждение (тот же контракт по форме, что app.Warning —
// retrieve не зависит от internal/app, поэтому объявлен здесь заново,
// формат {code,message,hint} общий для всех индексных инструментов,
// spec §Формат структурированного ответа).
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Result — выход get_context_for_task (архитектура §23 целиком).
type Result struct {
	Intent              Intent            `json:"intent"`
	Anchors             []Anchor          `json:"anchors"`
	Facts               []Fact            `json:"facts,omitempty"`
	Signatures          []Signature       `json:"signatures,omitempty"`
	Snippets            []Snippet         `json:"snippets,omitempty"`
	MetadataSummaries   []MetadataSummary `json:"metadataSummaries,omitempty"`
	Relations           []Relation        `json:"relations,omitempty"`
	RequiredCoverage    []CoverageEntry   `json:"requiredCoverage"`
	SufficiencyStatus   string            `json:"sufficiencyStatus"`
	MissingRequired     []string          `json:"missingRequired,omitempty"`
	Ambiguities         []Ambiguity       `json:"ambiguities,omitempty"`
	Budget              BudgetInfo        `json:"budget"`
	ExcludedHighScoring []ExcludedItem    `json:"excludedHighScoring,omitempty"`
	Warnings            []Warning         `json:"warnings,omitempty"`
	SuggestedNextTools  []string          `json:"suggestedNextTools,omitempty"`
	Generation          domain.Generation `json:"generation"`
}

// normalizeBudgetChars — §24 шаг 1: budget = normalizeBudgetChars(...), один
// раз в начале вызова, дальше жёсткий потолок. budgetChars, если задан,
// побеждает всегда; budgetTokens конвертируется фиксированной эвристикой
// ТОЛЬКО когда budgetChars пуст.
func normalizeBudgetChars(budgetChars, budgetTokens int) int {
	if budgetChars > 0 {
		return budgetChars
	}
	if budgetTokens > 0 {
		return budgetTokens * TokensToChars
	}
	return DefaultBudgetChars
}
