// Typed expansion (§24 шаг 4): intent задаёт разрешённые виды рёбер и
// направления. Реализовано как набор функций «один intent — один builder»,
// а не единый generic BFS по абстрактному «виду ребра»: сами виды рёбер
// физически разной формы (call graph — CallEdgesFrom/To, register_access —
// плоский фильтр по регистру, role_right — JOIN по объекту), общий walker
// поверх них означал бы либо новый универсальный граф поверх разнородных
// таблиц (никто из тасков 03/11/12/13 его не завёл — read_symbol.go,
// query_impact.go и readmeta.go/readquery.go/readregister.go читают каждый
// свой срез своим способом), либо генерик, который просто прячет те же
// switch'и на один уровень глубже. builder ниже честно называет, какие виды
// рёбер он проходит — в doc-комментарии над каждым.
package retrieve

import (
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// buildCtx — общее окружение всех builder'ов: транзакция, вход, поколение,
// компоненты-расширения (для интерцепторов), разобранный view (raw/effective —
// D08, effective.go).
type buildCtx struct {
	tx *store.ReadTx
	// symbols — источник символов перехватчиков (effective.go). Всегда та же
	// tx: поле ВЫВОДИТСЯ из неё в newBuildCtx и своей строки заполнения не
	// имеет. Два поля, заполняемые вызывающим по отдельности, значили бы, что
	// инвариант «symbols — это tx» держится соглашением: новый вызывающий
	// выставит одно, забудет второе, и молча разъедется ровно путь
	// перехватчиков. Подмена: привилегия теста (readSeams, build.go).
	symbols symbolFinder
	// objects/queries: те же швы, что symbols (ADR-035): отказ чтения
	// заимствований и запросов анкера на живой транзакции не
	// воспроизводится, а предупреждение и отзыв заявления на этом пути
	// обязаны быть проверены. Выводятся из tx в newBuildCtx.
	objects             borrowedObjectsFinder
	queries             queryReader
	req                 Request
	gen                 domain.Generation
	maxDepth            int
	includeCode         string
	extensionComponents map[string]bool
	view                domain.View

	// collected/collectionFailed — заявление сборщика о том, что обязательная
	// категория БЫЛА собрана (D03, §6 спецификации). Пустая категория имеет
	// право на статус complete_empty только по заявлению: «фактов нет» и
	// «никто не искал» — разные ответы, и вывести первый из счёта
	// (totalCount == 0) нельзя. Сборщик, у которого чтение упало, заявления
	// не делает: сбой чтения — не честная пустота.
	collected        map[string]bool
	collectionFailed map[string]bool
}

// newBuildCtx — единственная точка сборки окружения builder'ов. symbols
// выводится из tx здесь и только здесь: у вызывающего нет строки, в которой
// он мог бы забыть его выставить.
func newBuildCtx(tx *store.ReadTx, req Request, gen domain.Generation, maxDepth int,
	includeCode string, extensionComponents map[string]bool, view domain.View) *buildCtx {
	return &buildCtx{
		tx: tx, symbols: tx, objects: storeBorrowedObjects{tx: tx}, queries: tx, req: req, gen: gen, maxDepth: maxDepth,
		includeCode: includeCode, extensionComponents: extensionComponents, view: view,
	}
}

// declareCollected — сборщик заявляет, что перечисленные обязательные
// категории он собирал: всё, что нашлось, попало в кандидаты, и пустота (если
// она есть) настоящая.
func (bctx *buildCtx) declareCollected(categories ...string) {
	if bctx.collected == nil {
		bctx.collected = map[string]bool{}
	}
	for _, c := range categories {
		bctx.collected[c] = true
	}
}

// declareCollectionFailed отзывает заявление: источник категории прочитать не
// удалось, поэтому её пустота ничего не доказывает. Отзыв сильнее заявления —
// его не перебивает удачное чтение по другому анкеру.
func (bctx *buildCtx) declareCollectionFailed(categories ...string) {
	if bctx.collectionFailed == nil {
		bctx.collectionFailed = map[string]bool{}
	}
	for _, c := range categories {
		bctx.collectionFailed[c] = true
	}
}

// declareCollectedIf: заявление при удачном чтении, отзыв при сбое. Одна
// точка для сборщиков, у которых исход чтения известен целиком.
func (bctx *buildCtx) declareCollectedIf(ok bool, categories ...string) {
	if ok {
		bctx.declareCollected(categories...)
		return
	}
	bctx.declareCollectionFailed(categories...)
}

// anchorNotCollected: анкер, который builder не развернул (не тот вид или
// не разрешился), категорию не собирал. Для полноты это то же, что сбой
// чтения: заявление другого анкера не делает её честно пустой.
func (bctx *buildCtx) anchorNotCollected(categories ...string) {
	bctx.declareCollectionFailed(categories...)
}

// collectedEmptyCategories — итоговое множество категорий, чья пустота честна.
func (bctx *buildCtx) collectedEmptyCategories() map[string]bool {
	out := map[string]bool{}
	for c := range bctx.collected {
		if !bctx.collectionFailed[c] {
			out[c] = true
		}
	}
	return out
}

// scoreCtx — общие входы формулы scoring для одного кандидата (§24 шаг 4/
// spec «Доказанная релевантность»); anchorComponent — компонент anchor'а,
// от которого посчитан componentPriority.
type scoreCtx struct {
	depth          int
	anchorStrength float64
	direction      float64
	anchorComp     string
}

func (bctx *buildCtx) apply(sc scoreCtx, c *candidate) *candidate {
	c.depth = sc.depth
	c.anchorStrength = sc.anchorStrength
	c.direction = sc.direction
	c.componentPriority = componentPriorityFor(c.component, sc.anchorComp, bctx.extensionComponents)
	c.computeScore()
	return c
}

const (
	// bodyInlineCap — потолок инлайна одного snippet тела: суммарный бюджет
	// по умолчанию 16000 символов должен вмещать НЕСКОЛЬКО фактов, не один —
	// то же соображение, что inlineBodyMaxChars в internal/app/symbol.go,
	// но меньше, потому что здесь тело — один из МНОГИХ фактов ответа.
	bodyInlineCap = 1500
	// snippetInlineCap — потолок инлайна текста запроса (обычно короче тела).
	snippetInlineCap = 1200
)

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// formatSignatureText строит читаемую сигнатуру символа: ключевое слово вида,
// параметры со значениями по умолчанию, Экспорт — точнее, чем «голое»
// store.SymbolRow.Signature (index/publish.go:signatureOf режет только имена
// параметров, без директивы/умолчаний/Экспорт), и не требует чтения тела.
func formatSignatureText(row store.SymbolRow, params []store.ParameterRow) string {
	if row.Kind == string(domain.SymbolVariable) {
		return "Перем " + row.NameDisplay
	}
	kw := "Процедура"
	if row.Kind == string(domain.SymbolFunction) {
		kw = "Функция"
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		s := p.Name
		if p.DefaultExpr != "" {
			s += " = " + p.DefaultExpr
		}
		parts = append(parts, s)
	}
	text := kw + " " + row.NameDisplay + "(" + strings.Join(parts, ", ") + ")"
	if row.IsExport {
		text += " Экспорт"
	}
	if row.Directive != "" {
		text = "&" + row.Directive + "\n" + text
	}
	return text
}

// symbolBodyText вырезает тело символа из blob В ТОЙ ЖЕ read-транзакции
// (§18.2: span и текст согласованы автоматически, живой файл не читается).
func symbolBodyText(tx *store.ReadTx, row store.SymbolRow) (text, hash, relPath string, ok bool) {
	sf, sfOK, err := tx.SourceFileByID(row.OriginFileID)
	if err != nil || !sfOK {
		return "", "", "", false
	}
	blob, err := tx.Blob(sf.ContentHash)
	if err != nil {
		return "", "", "", false
	}
	sp := row.Span
	if sp.StartByte < 0 || sp.EndByte > len(blob) || sp.StartByte > sp.EndByte {
		return "", "", "", false
	}
	return string(blob[sp.StartByte:sp.EndByte]), sf.ContentHash, relPath, true
}

func makeSignature(bctx *buildCtx, sc scoreCtx, row store.SymbolRow, params []store.ParameterRow, category, why string) *candidate {
	text := formatSignatureText(row, params)
	c := &candidate{
		id: "sig:" + category + ":" + row.UID, bucket: bucketSignature, category: category,
		refUID: row.UID, component: row.ComponentID, module: row.ModulePath, display: row.NameDisplay,
		kindLabel: row.Kind, text: text, span: row.Span, resolution: string(domain.ResolutionResolved),
		confidence: 1, whyIncluded: why, charCost: runeLen(text),
	}
	return bctx.apply(sc, c)
}

// makeDefinitionSnippet строит категорию category (у bugfix/signature-change/
// query — "definition", у form — "handler", у posting — "posting_handler":
// требуемая категория не всегда буквально "definition", это лишь самый
// частый случай) для символа-анкера/обработчика: тело (usedResource=true и
// resourceURI ВСЕГДА присутствует, чтобы агент мог перечитать точный текст
// даже после усечения) — includeCode="none" отдаёт вместо этого голую
// сигнатуру (см. doc-комментарий Request.IncludeCode: этот builder —
// единственное место, чувствительное к includeCode).
func makeDefinitionSnippet(bctx *buildCtx, sc scoreCtx, row store.SymbolRow, params []store.ParameterRow, category, why string) *candidate {
	if bctx.includeCode == IncludeNone {
		return makeSignature(bctx, sc, row, params, category, why)
	}
	body, _, _, ok := symbolBodyText(bctx.tx, row)
	uri := symbolResourceURI(bctx.req.ProjectID, row.UID, bctx.gen)
	if !ok {
		return makeSignature(bctx, sc, row, params, category, why)
	}
	display := body
	truncated := false
	if runeLen(body) > bodyInlineCap {
		display = truncateRunes(body, bodyInlineCap) + "…"
		truncated = true
	}
	c := &candidate{
		id: "def:" + category + ":" + row.UID, bucket: bucketSnippet, category: category,
		refUID: row.UID, component: row.ComponentID, module: row.ModulePath, kindLabel: "body",
		text: display, truncated: truncated, resourceURI: uri, usedResource: truncated,
		span: row.Span, confidence: 1, whyIncluded: why, charCost: runeLen(display),
	}
	return bctx.apply(sc, c)
}

func makeCallGraphSignature(bctx *buildCtx, anchorComp string, edge store.CallEdgeRow, other store.SymbolRow, category string, depth int, why string) *candidate {
	params, _ := bctx.tx.SymbolParameters(other.ID)
	sc := scoreCtx{depth: depth, anchorStrength: 1.0, direction: 1.0, anchorComp: anchorComp}
	return makeSignature(bctx, sc, other, params, category, why)
}

// --- bugfix / unknown --------------------------------------------------

// expandBugfix — bugfix/unknown (§25 №1): definition + сигнатуры callees
// глубины maxDepth (call_edge, вперёд), сигнатуры callers глубины 1 (call_edge,
// назад — ТОЛЬКО прямые, см. doc-комментарий на вызове ниже), запросы внутри
// тела символа (query, контекст — НЕ обязательная категория). Формы, права,
// остальной модуль НЕ затрагиваются.
func expandBugfix(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	if a.Kind != "symbol" {
		return nil, nil
	}
	tx := bctx.tx
	row, ok, err := symbolByUID(tx, a.UID)
	if err != nil || !ok {
		return nil, nil
	}
	params, _ := tx.SymbolParameters(row.ID)
	var out []*candidate
	var warnings []Warning

	defSC := scoreCtx{depth: 0, anchorStrength: a.Strength, direction: 1.0, anchorComp: row.ComponentID}
	out = append(out, makeDefinitionSnippet(bctx, defSC, row, params, "definition",
		fmt.Sprintf("определение анкера %s (точный символ)", a.Display)))

	calleeCands, calleeWarn := walkCallGraphWithWarning(bctx, row, "callees", bctx.maxDepth, "callees",
		fmt.Sprintf("вызывается из тела %s (call_edge, вперёд)", a.Display))
	out = append(out, calleeCands...)
	warnings = append(warnings, calleeWarn...)
	// depth=1, НЕ bctx.maxDepth (регрессия evaluation-report.md §4.1: с
	// DefaultMaxDepth=2 этот вызов раньше гонял BFS ещё на один уровень вглубь
	// и складывал транзитивных callers (вызывающих вызывающих) в ту же
	// категорию "callers" без пометки уровня — на реальной выгрузке ut_demo
	// это дало 17 вместо 7 прямых, подтверждено grep+Serena независимо и тем
	// же замером до/после этого фикса). expandSignatureChange уже вызывает
	// walkCallGraphWithWarning с литералом 1 для той же категории — это тот
	// же самый контракт «callers = прямые», просто раньше он был согласован
	// не везде. Транзитивные callers здесь сознательно не собираются: у
	// "callers" как категории нет способа нести уровень глубины (candidate.depth
	// используется только для scoring/decay, не публикуется как поле
	// категории) — целый граф глубже 1 отдаёт trace_call_graph.
	callerCands, callerWarn := walkCallGraphWithWarning(bctx, row, "callers", 1, "callers",
		fmt.Sprintf("вызывает %s напрямую (call_edge, назад)", a.Display))
	out = append(out, callerCands...)
	warnings = append(warnings, callerWarn...)

	qCands, qWarn := queriesInBody(bctx, row, "query_in_body",
		fmt.Sprintf("запрос внутри тела %s", a.Display))
	out = append(out, qCands...)
	warnings = append(warnings, qWarn...)

	// effective (D08): "interceptors" не входит в requiredCategoryMap
	// bugfix/unknown (тот же список, что §25 №1: definition/callers/callees) —
	// добавляется контекстно, тем же приёмом, что query_in_body чуть выше:
	// не обязательная категория, но доказавшая релевантность (анкер реально
	// перехвачен). Точный список, не эвристика (см. doc-комментарий
	// effective.go): при view=raw (умолчание) этот блок не выполняется вовсе.
	if bctx.view == domain.ViewEffective {
		icCands, icWarn := effectiveSignatureInterceptors(bctx, row)
		out = append(out, icCands...)
		warnings = append(warnings, icWarn...)
	}

	return out, warnings
}

// symbolByUID — общий хелпер: uid символа -> строка symbol (identity_key
// символа == его uid, тот же приём, что read_symbol.go комментирует у SymbolByID).
func symbolByUID(tx *store.ReadTx, uid string) (store.SymbolRow, bool, error) {
	id, ok, err := tx.NodeID(uid)
	if err != nil || !ok {
		return store.SymbolRow{}, false, err
	}
	return tx.SymbolByID(id)
}

// maxCallGraphWalkNodes/maxCallGraphEdgesPerNode — потолки walkCallGraph, тот
// же приём, что internal/app/graph.go:maxCallGraphNodes/maxEdgesPerNode
// (тикет 11): реальная выгрузка содержит символы с сотнями/тысячами
// callers/callees (общие утилиты), и BFS без потолка на них — не «typed
// expansion, доказавшая релевантность», а полный обход графа, который
// одинаково дорог и для paking (лишняя сортировка), и для самого запроса к
// store (по одному SymbolByID/SymbolParameters на кандидата). Обнаружено
// замером на реальной выгрузке (ut_demo): без потолка p50 get_context_for_task
// на бытовом bugfix-сценарии — 1.56с против бюджета 1с (§28); с потолком
// экспансия остаётся представительной (маркер релевантности — БЛИЖНИЕ рёбра,
// не все существующие), но ограниченной по стоимости.
const (
	maxCallGraphWalkNodes    = 40
	maxCallGraphEdgesPerNode = 40
)

// walkCallGraph — BFS по call_edge вперёд (callees) или назад (callers) до
// depth, cycle-safe, каждый узел — сигнатура (никогда тело — «тело раньше
// модуля» не означает «тело чужого символа», это касается только анкера).
// truncated=true, когда потолок узлов остановил обход раньше исчерпания
// графа — вызывающий обязан отразить это warning'ом, а не молчать.
func walkCallGraph(bctx *buildCtx, root store.SymbolRow, direction string, depth int, category, why string) ([]*candidate, bool) {
	tx := bctx.tx
	visited := map[int64]bool{root.ID: true}
	frontier := []store.SymbolRow{root}
	var out []*candidate
	truncated := false
	for level := 1; level <= depth && len(frontier) > 0 && len(visited) < maxCallGraphWalkNodes; level++ {
		var next []store.SymbolRow
		for _, cur := range frontier {
			var edges []store.CallEdgeRow
			var err error
			if direction == "callees" {
				edges, err = tx.CallEdgesFrom(cur.ID)
			} else {
				edges, err = tx.CallEdgesTo(cur.ID)
			}
			if err != nil {
				continue
			}
			if len(edges) > maxCallGraphEdgesPerNode {
				edges = edges[:maxCallGraphEdgesPerNode]
				truncated = true
			}
			for _, e := range edges {
				if len(visited) >= maxCallGraphWalkNodes {
					truncated = true
					break
				}
				var otherID int64
				if direction == "callees" {
					otherID = e.CalleeID
				} else {
					otherID = e.CallerID
				}
				if otherID == 0 || visited[otherID] {
					continue
				}
				visited[otherID] = true
				other, ok, err := tx.SymbolByID(otherID)
				if err != nil || !ok {
					continue
				}
				out = append(out, makeCallGraphSignature(bctx, root.ComponentID, e, other, category, level, why))
				next = append(next, other)
			}
		}
		frontier = next
	}
	return out, truncated
}

// walkCallGraphWithWarning оборачивает walkCallGraph, добавляя honest warning
// при усечении по потолку узлов/рёбер — используется всеми builder'ами вместо
// голого walkCallGraph, чтобы не дублировать warning-код в каждом из них.
func walkCallGraphWithWarning(bctx *buildCtx, root store.SymbolRow, direction string, depth int, category, why string) ([]*candidate, []Warning) {
	cands, truncated := walkCallGraph(bctx, root, direction, depth, category, why)
	if !truncated {
		return cands, nil
	}
	return cands, []Warning{{
		Code: "call_graph_expansion_truncated",
		Message: fmt.Sprintf("обход call graph (%s, категория %s) от %s остановлен на потолке %d узлов — символ сильно связан",
			direction, category, root.NameDisplay, maxCallGraphWalkNodes),
		Hint: "для полного графа зовите trace_call_graph напрямую с нужным depth/limit",
	}}
}

// queriesInBody — запросы внутри тела символа, с их схемой (таблицы/поля из
// query_reference) — контекст bugfix (§25 №1: «запросы внутри символа с их
// схемами»), НЕ обязательная категория intent bugfix/unknown.
func queriesInBody(bctx *buildCtx, row store.SymbolRow, category, why string) ([]*candidate, []Warning) {
	tx := bctx.tx
	queries, err := tx.QueriesBySymbolID(row.ID)
	if err != nil || len(queries) == 0 {
		return nil, nil
	}
	sc := scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: row.ComponentID}
	var out []*candidate
	var warnings []Warning
	for i, q := range queries {
		text := q.Text
		truncated := false
		if runeLen(text) > snippetInlineCap {
			text = truncateRunes(text, snippetInlineCap) + "…"
			truncated = true
		}
		c := &candidate{
			id: fmt.Sprintf("query:%s:%d", row.UID, i), bucket: bucketSnippet, category: category,
			refUID: row.UID, component: row.ComponentID, module: row.ModulePath, kindLabel: "query_text",
			text: text, truncated: truncated, span: q.Span, confidence: domain.Confidence(q.Confidence),
			whyIncluded: why, charCost: runeLen(text),
		}
		out = append(out, bctx.apply(sc, c))
		if q.Staticity != "static" {
			warnings = append(warnings, Warning{
				Code: "query_not_static", Message: fmt.Sprintf("запрос в %s собран динамически (staticity=%s) — схема из query_reference недоступна", row.NameDisplay, q.Staticity),
				Hint: "прочитайте тело символа целиком, чтобы увидеть сборку текста запроса",
			})
			continue
		}
		refs, rerr := tx.QueryReferencesByQueryID(q.ID)
		if rerr != nil {
			continue
		}
		for j, r := range refs {
			label := r.NameNorm
			if r.Kind == "field" && r.ObjectID != 0 {
				if obj, ok, _ := tx.MetadataObjectByID(r.ObjectID); ok {
					label = obj.NameDisplay + "." + r.NameNorm
				}
			}
			rc := &candidate{
				id: fmt.Sprintf("qref:%s:%d:%d", row.UID, i, j), bucket: bucketRelation, category: category,
				kindLabel: "query_reference", fromDisplay: row.NameDisplay, display: label, detail: r.Kind,
				component: row.ComponentID, confidence: domain.Confidence(q.Confidence),
				whyIncluded: fmt.Sprintf("схема запроса внутри %s: %s %q", row.NameDisplay, r.Kind, r.NameNorm),
				charCost:    runeLen(label) + runeLen(r.Kind) + 4,
			}
			out = append(out, bctx.apply(scoreCtx{depth: 2, anchorStrength: 1.0, direction: 1.0, anchorComp: row.ComponentID}, rc))
		}
	}
	return out, warnings
}

// --- signature-change ----------------------------------------------------

// expandSignatureChange — «измени сигнатуру экспортной X.Y» (§25 №2):
// definition (сигнатура), ВСЕ references (reference, полный список со
// spans — агрегация по модулям + resource link при переполнении бюджета —
// делает pack.go, не эта функция: она честно отдаёт кандидата НА КАЖДУЮ
// ссылку, паковка сама решает, что влезло), callers (call_edge, назад,
// глубина 1 — «кто вызывает» самих символов, не мест вызова), interceptors
// (символы с тем же именем в компонентах вида extension — эвристика, см.
// doc-комментарий ниже).
func expandSignatureChange(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	if a.Kind != "symbol" {
		return nil, nil
	}
	tx := bctx.tx
	row, ok, err := symbolByUID(tx, a.UID)
	if err != nil || !ok {
		return nil, nil
	}
	params, _ := tx.SymbolParameters(row.ID)
	var out []*candidate
	var warnings []Warning

	defSC := scoreCtx{depth: 0, anchorStrength: a.Strength, direction: 1.0, anchorComp: row.ComponentID}
	out = append(out, makeSignature(bctx, defSC, row, params, "definition",
		fmt.Sprintf("определение анкера %s (сигнатура — меняется этой задачей)", a.Display)))

	const referencesSafetyCap = 400
	refs, rerr := tx.FindReferences(store.ReferenceSearch{TargetSymbolID: row.ID, Limit: referencesSafetyCap + 1})
	if rerr == nil {
		truncatedList := len(refs) > referencesSafetyCap
		if truncatedList {
			refs = refs[:referencesSafetyCap]
			warnings = append(warnings, Warning{
				Code: "references_truncated", Message: fmt.Sprintf("references усечены до %d — на выгрузке их больше", referencesSafetyCap),
				Hint: "заберите полный список через find_references " + a.UID,
			})
		}
		resURI := referencesResourceURI(bctx.req.ProjectID, row.UID, bctx.gen)
		for i, r := range refs {
			text := r.ModulePath + ":" + itoa(r.Span.StartLine)
			rc := &candidate{
				id: fmt.Sprintf("ref:%s:%d", row.UID, r.ID), bucket: bucketRelation, category: "references",
				kindLabel: "reference", fromDisplay: r.ModulePath, display: text, detail: r.Resolution,
				component: r.ComponentID, resolution: r.Resolution, confidence: domain.Confidence(r.Confidence),
				span: r.Span, resourceURI: resURI, usedResource: truncatedList && i == len(refs)-1,
				whyIncluded: fmt.Sprintf("ссылка на %s из %s (reference)", a.Display, r.ModulePath),
				charCost:    runeLen(text) + runeLen(r.Resolution) + 4,
			}
			out = append(out, bctx.apply(scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: row.ComponentID}, rc))
		}
	}

	callerCands, callerWarn := walkCallGraphWithWarning(bctx, row, "callers", 1, "callers",
		fmt.Sprintf("вызывает %s напрямую (call_edge, назад)", a.Display))
	out = append(out, callerCands...)
	warnings = append(warnings, callerWarn...)

	// effective (D08): "interceptors" — обязательная категория signature-change
	// (requiredCategoryMap). view=effective меняет ТОЛЬКО источник фактов
	// (точный resolve.DeriveIntercepts вместо эвристики по имени
	// signatureInterceptors) — raw остаётся байт-в-байт прежним поведением,
	// регрессия §7 не допускается (см. doc-комментарий signatureInterceptors
	// и effective.go:effectiveSignatureInterceptors).
	var interceptors []*candidate
	var iwarn []Warning
	if bctx.view == domain.ViewEffective {
		interceptors, iwarn = effectiveSignatureInterceptors(bctx, row)
	} else {
		interceptors, iwarn = signatureInterceptors(bctx, row)
	}
	out = append(out, interceptors...)
	warnings = append(warnings, iwarn...)

	return out, warnings
}

// signatureInterceptors — «перехватчики расширений» (§25 №2, обязательная
// категория). Точного механизма связи символа расширения с перехватываемым
// методом базового слоя (&Вместо/&Перед/&После с ИМЕНЕМ ЦЕЛИ) parse/bsl не
// материализует отдельным фактом (Method.Directive несёт компиляторную
// директиву клиент/сервер, не имя патчимого метода) — эвристика: символы С
// ТЕМ ЖЕ name_norm в компонентах вида extension — кандидаты в перехватчики,
// confidence снижена (0.5, heuristic-provenance), это честно помечено и в
// whyIncluded, и в confidence, а не выдано за точный факт.
//
// Используется ТОЛЬКО при view=raw (умолчание) — D08 добавил точный
// effective-путь (effective.go:effectiveSignatureInterceptors, поверх
// resolve.DeriveIntercepts — тот самый «раздельный merge слоёв», который эта
// эвристика когда-то замещала), эта функция и её поведение при этом не
// меняются ни на символ — регрессия для raw не допускается (см.
// expandSignatureChange).
func signatureInterceptors(bctx *buildCtx, row store.SymbolRow) ([]*candidate, []Warning) {
	tx := bctx.tx
	comps, err := tx.Components()
	if err != nil {
		return nil, nil
	}
	var out []*candidate
	var sawAny bool
	// SymbolsByNameNormExact (name_norm=?, индексирован), не FindSymbols
	// (LIKE '%x%', полный скан) — тот же приём, что anchors.go, и по той же
	// измеренной причине (см. doc-комментарий store.SymbolsByNameNormExact).
	exactMatches, merr := tx.SymbolsByNameNormExact(row.NameNorm)
	if merr != nil {
		return nil, nil
	}
	byComponent := map[string][]store.SymbolRow{}
	for _, m := range exactMatches {
		byComponent[m.ComponentID] = append(byComponent[m.ComponentID], m)
	}
	for _, c := range comps {
		if c.Kind != string(domain.KindExtension) || c.ID == row.ComponentID {
			continue
		}
		sawAny = true
		for _, m := range byComponent[c.ID] {
			params, _ := tx.SymbolParameters(m.ID)
			text := formatSignatureText(m, params)
			cand := &candidate{
				id: "intercept:" + m.UID, bucket: bucketSignature, category: "interceptors",
				refUID: m.UID, component: m.ComponentID, module: m.ModulePath, display: m.NameDisplay,
				kindLabel: m.Kind, text: text, span: m.Span, resolution: "candidate",
				confidence: 0.5, provenance: "heuristic",
				whyIncluded: fmt.Sprintf("символ с тем же именем %q в компоненте-расширении %s — кандидат в перехватчик, не подтверждено &Вместо/&После", row.NameDisplay, m.ComponentID),
				charCost:    runeLen(text),
			}
			out = append(out, bctx.apply(scoreCtx{depth: 1, anchorStrength: 0.7, direction: 1.0, anchorComp: row.ComponentID}, cand))
		}
	}
	var warnings []Warning
	if sawAny && len(out) == 0 {
		warnings = append(warnings, Warning{
			Code:    "no_interceptor_candidates",
			Message: "в расширениях проекта нет символа с тем же именем — либо перехватчика нет, либо он назван иначе",
			Hint:    "проверьте вручную через find_symbol с component=<расширение>",
		})
	}
	return out, warnings
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
