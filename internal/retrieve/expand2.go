package retrieve

import (
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/effective"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// resolveMetadataAnchor возвращает полную строку metadata_object для anchor
// вида "metadata_object" (anchors несут только type/name/component — этого
// достаточно для отображения, но expansion нужен object_id).
func resolveMetadataAnchor(tx *store.ReadTx, a Anchor) (store.MetadataObjectRow, bool, error) {
	rows, err := tx.MetadataObjectsByNameNormAnyType(a.ObjectName)
	if err != nil {
		return store.MetadataObjectRow{}, false, err
	}
	for _, r := range rows {
		if r.MType == a.ObjectType && (a.Component == "" || r.ComponentID == a.Component) {
			return r, true, nil
		}
	}
	return store.MetadataObjectRow{}, false, nil
}

// --- register --------------------------------------------------------

// expandRegister — «кто пишет в регистр X» (§25 №3): writes_movements
// (register_access mode IN write/movement/clear) + owning_symbols (символы,
// делающие эти доступы). reads — контекст-счётчик, НЕ обязательная категория
// (spec: «не попадёт: reads (упомянуты счётчиком)»).
//
// view=effective (ADR-035) добавляет необязательную writer_intercepts: факты
// перехвата писателей (effective.go:registerWriterIntercepts), а запись,
// сделанная перехватчиком расширения, называет в объяснении, какой метод
// базового слоя она дополняет. raw не меняется: список записей тот же, фактов
// перехвата в нём нет.
func expandRegister(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	if a.Kind != "metadata_object" {
		return nil, nil
	}
	tx := bctx.tx
	obj, ok, err := resolveMetadataAnchor(tx, a)
	if err != nil || !ok {
		return nil, nil
	}
	sc := scoreCtx{depth: 1, anchorStrength: a.Strength, direction: 1.0, anchorComp: obj.ComponentID}

	var out []*candidate
	var warnings []Warning
	writeRows, werr := tx.RegisterAccesses(store.RegisterAccessFilter{
		RegisterNameNorm: obj.NameNorm, Modes: []string{"write", "movement", "clear"}, Limit: 300,
	})
	effectiveView := bctx.view == domain.ViewEffective
	var appliesTo map[string]string
	if effectiveView && werr == nil {
		var aerr error
		if appliesTo, aerr = extensionAppliesTo(tx); aerr != nil {
			warnings = append(warnings, Warning{
				Code:    "register_writer_intercepts_read_failed",
				Message: fmt.Sprintf("не удалось прочитать состав компонентов для наложения расширений на писателей %s: %v", obj.NameDisplay, aerr),
				Hint:    "повторите вызов после reindex",
			})
			effectiveView = false
		}
	}
	var baseWriters []store.SymbolRow
	var extIntercepts []effective.Intercept
	if werr == nil {
		symbolCache := map[int64]store.SymbolRow{}
		ownersDone := map[int64]bool{}
		// extIntercept: факт перехвата у символа расширения, посчитанный
		// один раз на символ (effective): им подписывается каждая его запись.
		extIntercept := map[int64]*effective.Intercept{}
		var sawDynamic bool
		for i, r := range writeRows {
			if !r.Static {
				sawDynamic = true
			}
			ownerDisplay := ""
			if r.SymbolID != 0 {
				sym, cached := symbolCache[r.SymbolID]
				if !cached {
					if s, sok, _ := tx.SymbolByID(r.SymbolID); sok {
						sym = s
						symbolCache[r.SymbolID] = s
					}
				}
				if sym.ID != 0 {
					ownerDisplay = sym.ModulePath + "." + sym.NameDisplay
					if effectiveView && !cached {
						if bctx.extensionComponents[sym.ComponentID] {
							ic, found, ws, ierr := extensionInterceptOf(tx, appliesTo, sym)
							warnings = append(warnings, ws...)
							if ierr != nil {
								warnings = append(warnings, registerInterceptReadFailed(sym, ierr))
							} else if found {
								extIntercept[sym.ID] = &ic
								extIntercepts = append(extIntercepts, ic)
							}
						} else {
							baseWriters = append(baseWriters, sym)
						}
					}
				}
			}
			label := fmt.Sprintf("%s: %s", r.Mode, ownerDisplay)
			why := fmt.Sprintf("register_access mode=%s на %s из %s", r.Mode, obj.NameDisplay, ownerDisplay)
			if ic := extIntercept[r.SymbolID]; ic != nil {
				why += "; " + interceptWhy(*ic)
			}
			rc := &candidate{
				id: fmt.Sprintf("regw:%s:%d", obj.NameNorm, r.ID), bucket: bucketRelation, category: "writes_movements",
				kindLabel: "register_access", fromDisplay: ownerDisplay, display: obj.NameDisplay, detail: label,
				component: r.ComponentID, confidence: domain.Confidence(r.Confidence), span: r.Span,
				whyIncluded: why,
				charCost:    runeLen(label) + runeLen(obj.NameDisplay) + 4,
			}
			out = append(out, bctx.apply(sc, rc))

			if r.SymbolID != 0 && !ownersDone[r.SymbolID] {
				ownersDone[r.SymbolID] = true
				sym := symbolCache[r.SymbolID]
				if sym.ID != 0 {
					params, _ := tx.SymbolParameters(sym.ID)
					out = append(out, makeSignature(bctx, sc, sym, params, "owning_symbols",
						fmt.Sprintf("пишет в %s (register_access, mode=%s)", obj.NameDisplay, r.Mode)))
				}
			}
			_ = i
		}
		if sawDynamic {
			warnings = append(warnings, Warning{
				Code:    "dynamic_register_access",
				Message: fmt.Sprintf("часть доступов к %s определена динамически (RegisterAccess.Static=false) — confidence ниже, адрес регистра мог быть вычислен в рантайме", obj.NameDisplay),
				Hint:    "проверьте вручную места, где регистр адресуется через переменную/параметр",
			})
		}
	}

	if effectiveView {
		icCands, icWarnings := registerWriterIntercepts(bctx, obj, baseWriters, extIntercepts)
		out = append(out, icCands...)
		warnings = append(warnings, icWarnings...)
	}

	readRows, rerr := tx.RegisterAccesses(store.RegisterAccessFilter{
		RegisterNameNorm: obj.NameNorm, Modes: []string{"read"}, Limit: 300,
	})
	if rerr == nil && len(readRows) > 0 {
		f := &candidate{
			id: "regreadcount:" + obj.NameNorm, bucket: bucketFact, category: "register_reads",
			display: obj.NameDisplay, detail: fmt.Sprintf("чтений: %d (не влияет на покрытие — эта категория не обязательна)", len(readRows)),
			component: obj.ComponentID, confidence: 1,
			whyIncluded: fmt.Sprintf("счётчик чтений %s, упомянут per spec §25 №3", obj.NameDisplay),
			charCost:    40,
		}
		out = append(out, bctx.apply(sc, f))
	}

	warnings = append(warnings, Warning{
		Code:    "registrators_not_derived",
		Message: "документы-регистраторы регистра не выведены отдельно: схема register_access не хранит связь регистр -> документ-регистратор из XML движений, только фактические доступы из кода",
		Hint:    "проверьте состав регистраторов регистра через get_object InformationRegister/AccumulationRegister " + obj.NameDisplay,
	})
	return out, warnings
}

// --- form --------------------------------------------------------------

// expandForm — «поменяй обработчик X поля Y на форме документа Z» (§25 №4):
// binding + handler + server_calls + attributes. Форма/элемент находятся по
// упоминанию в тексте задачи (имя элемента, имя события) среди форм anchor'а
// (объекта-владельца); полная структура формы НЕ возвращается (spec:
// «не попадёт: вся структура формы (счётчики + resource link)»).
func expandForm(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	if a.Kind != "metadata_object" {
		return nil, nil
	}
	tx := bctx.tx
	obj, ok, err := resolveMetadataAnchor(tx, a)
	if err != nil || !ok {
		return nil, nil
	}
	forms, ferr := tx.FormsByOwner(obj.ID)
	if ferr != nil || len(forms) == 0 {
		// Тип и компонент — в тексте, не только имя (находка третьего круга
		// ревью, reindex-timings): omonym.NameDisplay совпадает у объектов
		// РАЗНЫХ видов (D03), и без типа/компонента это no_forms читается
		// как противоречие рядом с настоящей находкой по одноимённому
		// объекту другого вида — даже когда suppressFormNoiseWhenMatched
		// корректно НЕ гасит его (это генуинно разные объекты, см. D02/D03).
		return nil, []Warning{{Code: "no_forms", Message: fmt.Sprintf(
			"у объекта %s.%s (%s) не найдено форм в индексе", obj.MType, obj.NameDisplay, obj.ComponentID)}}
	}
	taskNorm := domain.NormalizeName(bctx.req.Task)

	var out []*candidate
	var warnings []Warning
	sc := scoreCtx{depth: 1, anchorStrength: a.Strength, direction: 1.0, anchorComp: obj.ComponentID}
	var matchedAny bool

	for _, f := range forms {
		bindings, berr := tx.HandlerBindingsByForm(f.ID)
		if berr != nil {
			continue
		}
		elements, eerr := tx.FormElementsByForm(f.ID)
		elemByNorm := map[string]store.FormElementRow{}
		if eerr == nil {
			for _, el := range elements {
				elemByNorm[el.NameNorm] = el
			}
		}
		for _, b := range bindings {
			relevant := false
			if b.Source != "" && strings.Contains(taskNorm, b.Source) {
				relevant = true
			}
			if strings.Contains(taskNorm, domain.NormalizeName(b.Event)) {
				relevant = true
			}
			if !relevant {
				continue
			}
			matchedAny = true

			label := fmt.Sprintf("%s.%s -> %s", b.Source, b.Event, b.HandlerNameNorm)
			bc := &candidate{
				id: fmt.Sprintf("binding:%d", b.ID), bucket: bucketRelation, category: "binding",
				kindLabel: "handler_binding", fromDisplay: f.NameDisplay, display: label, detail: b.Resolution,
				component: obj.ComponentID, resolution: b.Resolution, confidence: 1,
				whyIncluded: fmt.Sprintf("привязка обработчика на форме %s (handler_binding)", f.NameDisplay),
				charCost:    runeLen(label) + 10,
			}
			out = append(out, bctx.apply(sc, bc))

			if b.Resolution != string(domain.ResolutionResolved) || b.HandlerSymbolID == 0 {
				warnings = append(warnings, Warning{
					Code: "handler_unresolved", Message: fmt.Sprintf("обработчик %q события %s.%s формы %s не разрешён в символ", b.HandlerNameNorm, b.Source, b.Event, f.NameDisplay),
					Hint: "проверьте модуль формы вручную — имя обработчика могло не совпасть с процедурой",
				})
				continue
			}
			handlerRow, hok, herr := tx.SymbolByID(b.HandlerSymbolID)
			if herr != nil || !hok {
				continue
			}
			params, _ := tx.SymbolParameters(handlerRow.ID)
			out = append(out, makeDefinitionSnippet(bctx, sc, handlerRow, params, "handler",
				fmt.Sprintf("тело обработчика %s.%s (handler)", b.Source, b.Event)))
			serverCalls, serverWarn := walkCallGraphWithWarning(bctx, handlerRow, "callees", 1, "server_calls",
				fmt.Sprintf("серверный вызов из обработчика %s.%s", b.Source, b.Event))
			out = append(out, serverCalls...)
			warnings = append(warnings, serverWarn...)

			// effective (D08, п.4б): модуль формы — обычный заимствуемый
			// модуль, обработчик может быть перехвачен расширением тем же
			// способом, что get_module_structure(view=effective) уже
			// показывает per-символ (internal/app/symbol.go). "handler_intercepts" —
			// НЕ обязательная категория формы (requiredCategoryMap не
			// называет её — "binding"/"handler" остаются как есть) —
			// добавляется контекстно рядом с телом обработчика, тем же
			// приёмом, что query_in_body у bugfix.
			if bctx.view == domain.ViewEffective {
				hIcs, _, hWarn, hErr := effectiveInterceptsForSymbol(tx, handlerRow)
				if hErr == nil {
					scH := scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: handlerRow.ComponentID}
					for _, ic := range hIcs {
						out = append(out, makeInterceptCandidate(bctx, scH, ic, "handler_intercepts",
							fmt.Sprintf("перехватчик расширения обработчика %s.%s (&%s %s, effective)",
								b.Source, b.Event, ic.Kind, ic.Layer.Component)))
					}
					warnings = append(warnings, hWarn...)
					warnings = append(warnings, interceptConflictWarnings(hIcs)...)
				}
			}

			if el, hasElem := elemByNorm[b.Source]; hasElem && el.DataPath != "" {
				attrName := el.DataPath
				if idx := strings.LastIndex(attrName, "."); idx >= 0 {
					attrName = attrName[idx+1:]
				}
				members, merr := tx.MetadataMembers(obj.ID)
				if merr == nil {
					attrNorm := domain.NormalizeName(attrName)
					for _, m := range members {
						if m.NameNorm != attrNorm {
							continue
						}
						fc := &candidate{
							id: fmt.Sprintf("attr:%d", m.ID), bucket: bucketFact, category: "attributes",
							display: m.NameDisplay, detail: el.DataPath, component: obj.ComponentID, confidence: 1,
							whyIncluded: fmt.Sprintf("реквизит %s затронут элементом формы %s (DataPath=%s)", m.NameDisplay, el.NameDisplay, el.DataPath),
							charCost:    runeLen(m.NameDisplay) + runeLen(el.DataPath) + 10,
						}
						out = append(out, bctx.apply(sc, fc))
					}
				}
			}
		}
	}

	if !matchedAny {
		// Имя объекта в тексте — по той же причине, что у no_forms выше:
		// без него предупреждение неотличимо от такого же про совсем другой
		// объект того же имени (D02/D03).
		warnings = append(warnings, Warning{
			Code: "form_binding_not_matched",
			Message: fmt.Sprintf(
				"у объекта %s.%s (%s) ни один обработчик формы не совпал по имени элемента/события с текстом задачи",
				obj.MType, obj.NameDisplay, obj.ComponentID),
			Hint: "уточните задачу точным именем элемента формы и события, либо вызовите get_form_handlers",
		})
	}
	return out, warnings
}

// --- add-attribute -------------------------------------------------------

// expandAddAttribute — «добавь реквизит X в Документ.Y и оцени impact» (§25
// №5): structure + usages + forms + rights + exchanges. Тела модулей не
// затрагиваются.
//
// view=effective (ADR-035): объект анкера базового слоя дополняется своими
// заимствованиями в применяющихся расширениях (effective.BorrowedObjects):
// структура заимствования с реквизитами расширения, формы, права ролей
// расширения и использования в запросах, каждый факт со своим слоем.
// Категория forms заявляется собранной (declareCollected, ADR-030), когда
// формы всех слоёв прочитаны: пустота тогда честная. raw не меняется.
func expandAddAttribute(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	if a.Kind != "metadata_object" {
		return nil, nil
	}
	tx := bctx.tx
	obj, ok, err := resolveMetadataAnchor(tx, a)
	if err != nil || !ok {
		return nil, nil
	}
	out, formsOK := addAttributeFacts(bctx, a, obj, "")
	var warnings []Warning
	if bctx.view == domain.ViewEffective && !bctx.extensionComponents[obj.ComponentID] {
		borrowed, berr := effective.BorrowedObjects(tx, obj)
		if berr != nil {
			formsOK = false
			warnings = append(warnings, borrowedObjectsReadFailed(obj, berr))
		}
		for _, b := range borrowed {
			cands, bFormsOK := addAttributeFacts(bctx, a, b, b.ComponentID)
			out = append(out, cands...)
			formsOK = formsOK && bFormsOK
		}
		if formsOK {
			bctx.declareCollected("forms")
		} else {
			bctx.declareCollectionFailed("forms")
		}
	}

	warnings = append(warnings, Warning{
		Code:    "exchange_edges_not_built",
		Message: "принадлежность объекта плану обмена не выведена: dependency_edge(kind=exchange-plan-contains) не публикуется индексом (interfaces.md, долг тасков 08/09) — категория exchanges всегда missing",
		Hint:    "проверьте состав плана обмена вручную через get_object ExchangePlan или find_metadata_usages",
	})
	return out, warnings
}

// borrowedObjectsReadFailed: заимствования объекта прочитать не удалось.
// Ответ тогда построен по одному базовому слою, и это обязано быть сказано:
// иначе он неотличим от ответа по объекту, который никто не заимствовал.
func borrowedObjectsReadFailed(obj store.MetadataObjectRow, err error) Warning {
	return Warning{
		Code: "effective_borrowed_objects_read_failed",
		Message: fmt.Sprintf("не удалось прочитать заимствования %s.%s в расширениях: %v; ответ построен только по базовому слою",
			obj.MType, obj.NameDisplay, err),
		Hint: "повторите вызов после reindex",
	}
}

// addAttributeFacts собирает факты add-attribute по ОДНОЙ строке объекта.
// borrowedBy пуст у строки анкера: тогда ключи и тексты кандидатов те же, что
// до effective-вида (raw не меняется). Непустой borrowedBy (компонент
// расширения, effective) метит факты как заимствование и разводит ключ
// структуры: он строится по имени объекта и у двух слоёв иначе совпал бы.
// Второе значение: формы этой строки прочитаны без ошибки.
func addAttributeFacts(bctx *buildCtx, a Anchor, obj store.MetadataObjectRow, borrowedBy string) ([]*candidate, bool) {
	tx := bctx.tx
	sc := scoreCtx{depth: 0, anchorStrength: a.Strength, direction: 1.0, anchorComp: obj.ComponentID}
	structureID := "structure:" + obj.NameNorm
	whyTail := ""
	if borrowedBy != "" {
		structureID += "@" + borrowedBy
		whyTail = fmt.Sprintf(" (заимствован расширением %s, effective)", borrowedBy)
	}
	var out []*candidate

	members, merr := tx.MetadataMembers(obj.ID)
	if merr == nil {
		var summary []MetadataMemberSummary
		for _, m := range members {
			if len(summary) < 30 {
				summary = append(summary, MetadataMemberSummary{Kind: m.Kind, Name: m.NameDisplay})
			}
		}
		text := fmt.Sprintf("%d членов", len(members))
		sm := &candidate{
			id: structureID, bucket: bucketMetadataSummary, category: "structure",
			display: obj.NameDisplay, kindLabel: obj.MType, component: obj.ComponentID, memberCount: len(members),
			members: summary, confidence: 1,
			whyIncluded: fmt.Sprintf("структура объекта %s — точка добавления реквизита", obj.NameDisplay) + whyTail,
			charCost:    runeLen(text) + len(summary)*12,
		}
		out = append(out, bctx.apply(sc, sm))
	}

	usageRefs, uerr := tx.QueryReferences(store.QueryReferenceFilter{ObjectID: obj.ID, Limit: 100})
	if uerr == nil {
		for i, r := range usageRefs {
			label := r.Kind + ":" + r.NameNorm
			uc := &candidate{
				id: fmt.Sprintf("usage:%d:%d", obj.ID, i), bucket: bucketRelation, category: "usages",
				kindLabel: "query_reference", fromDisplay: obj.NameDisplay, display: label,
				component: obj.ComponentID, confidence: 1,
				whyIncluded: fmt.Sprintf("объект %s использован в запросе (query_reference, %s)", obj.NameDisplay, r.Kind) + whyTail,
				charCost:    runeLen(label) + 8,
			}
			out = append(out, bctx.apply(scoreCtx{depth: 1, anchorStrength: a.Strength, direction: 1.0, anchorComp: obj.ComponentID}, uc))
		}
	}

	forms, fferr := tx.FormsByOwner(obj.ID)
	if fferr == nil {
		for _, f := range forms {
			fc := &candidate{
				id: "form:" + itoaInt64(f.ID), bucket: bucketFact, category: "forms",
				display: f.NameDisplay, component: obj.ComponentID, confidence: 1,
				whyIncluded: fmt.Sprintf("форма объекта %s — потенциально требует новый элемент реквизита", obj.NameDisplay) + whyTail,
				charCost:    runeLen(f.NameDisplay) + 10,
			}
			out = append(out, bctx.apply(sc, fc))
		}
	}

	rights, rrerr := tx.RoleRightsByObjectID(obj.ID)
	if rrerr == nil {
		seenRole := map[int64]bool{}
		for _, rr := range rights {
			if seenRole[rr.RoleID] {
				continue
			}
			seenRole[rr.RoleID] = true
			rc := &candidate{
				id: fmt.Sprintf("addattr-role:%d", rr.RoleID), bucket: bucketFact, category: "rights",
				display: rr.RoleNameDisplay, detail: rr.RightName, component: rr.RoleComponentID, confidence: 1,
				whyIncluded: fmt.Sprintf("роль %s уже имеет права на %s — новый реквизит может требовать пересмотра прав", rr.RoleNameDisplay, obj.NameDisplay) + whyTail,
				charCost:    runeLen(rr.RoleNameDisplay) + 10,
			}
			out = append(out, bctx.apply(sc, rc))
		}
	}
	return out, fferr == nil
}

func itoaInt64(n int64) string { return fmt.Sprintf("%d", n) }

// --- query -----------------------------------------------------------

// expandQuery — intent "query": query_text + owner_symbol + schema +
// tables_fields. Anchor-символ (запрос внутри его тела) — основной путь;
// anchor-объект метаданных отдаёт только usages того же вида, что
// find_queries_using (без owner_symbol/query_text одного конкретного текста
// — их несколько, «схема» в этом случае per-usage через find_queries_using,
// не через get_context_for_task).
//
// view=effective (ADR-035) добавляет перехватчики символа-анкера
// (необязательная query_intercepts) и тексты запросов из самих перехватчиков в
// те же query_text/schema/tables_fields, со слоем расширения
// (effective.go:queryInterceptCandidates). Для &ИзменениеИКонтроль исполняется
// именно текст расширения. Запрос, который есть только в перехватчике, не
// даёт effective-ответу сказать no_query_in_symbol. raw не меняется.
func expandQuery(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	tx := bctx.tx
	if a.Kind != "symbol" {
		return nil, nil
	}
	row, ok, err := symbolByUID(tx, a.UID)
	if err != nil || !ok {
		return nil, nil
	}
	queries, qerr := tx.QueriesBySymbolID(row.ID)
	if qerr != nil {
		queries = nil
	}
	var icCands []*candidate
	var icWarnings []Warning
	icQueries := 0
	if bctx.view == domain.ViewEffective {
		icCands, icQueries, icWarnings = queryInterceptCandidates(bctx, row)
	}
	if len(queries) == 0 && icQueries == 0 {
		return icCands, append(icWarnings, Warning{Code: "no_query_in_symbol", Message: fmt.Sprintf("в %s не найдено текстов запросов", row.NameDisplay)})
	}
	sc := scoreCtx{depth: 0, anchorStrength: a.Strength, direction: 1.0, anchorComp: row.ComponentID}
	var out []*candidate

	params, _ := tx.SymbolParameters(row.ID)
	out = append(out, makeSignature(bctx, sc, row, params, "owner_symbol",
		fmt.Sprintf("символ-владелец текста запроса %s", row.NameDisplay)))

	qCands, warnings := queryCandidates(bctx, row, queries, sc, row.ComponentID, "")
	out = append(out, qCands...)
	out = append(out, icCands...)
	return out, append(warnings, icWarnings...)
}

// queryCandidates строит query_text/schema/tables_fields по текстам запросов
// символа owner. Общий для запросов анкера и запросов его перехватчиков
// (effective): у перехватчика component и модуль его собственные, whyTail
// называет факт перехвата. sc: оценка текстов; ссылки схемы идут на шаг
// глубже от anchorComp.
func queryCandidates(bctx *buildCtx, owner store.SymbolRow, queries []store.QueryRow, sc scoreCtx, anchorComp, whyTail string) ([]*candidate, []Warning) {
	tx := bctx.tx
	var out []*candidate
	var warnings []Warning
	for i, q := range queries {
		text := q.Text
		truncated := false
		if runeLen(text) > snippetInlineCap {
			text = truncateRunes(text, snippetInlineCap) + "…"
			truncated = true
		}
		qc := &candidate{
			id: fmt.Sprintf("qtext:%s:%d", owner.UID, i), bucket: bucketSnippet, category: "query_text",
			refUID: owner.UID, component: owner.ComponentID, module: owner.ModulePath, kindLabel: "query_text",
			text: text, truncated: truncated, span: q.Span, confidence: domain.Confidence(q.Confidence),
			whyIncluded: fmt.Sprintf("текст запроса внутри %s", owner.NameDisplay) + whyTail, charCost: runeLen(text),
		}
		out = append(out, bctx.apply(sc, qc))

		if q.Staticity != "static" {
			warnings = append(warnings, Warning{
				Code: "query_not_static", Message: fmt.Sprintf("запрос %d в %s собран динамически (staticity=%s)", i, owner.NameDisplay, q.Staticity),
			})
			continue
		}
		refs, rerr := tx.QueryReferencesByQueryID(q.ID)
		if rerr != nil {
			continue
		}
		seenTable := map[string]bool{}
		for j, r := range refs {
			cat := "tables_fields"
			if r.Kind == "table" && !seenTable[r.NameNorm] {
				seenTable[r.NameNorm] = true
				cat = "schema"
			}
			label := r.NameNorm
			if r.Kind == "field" && r.ObjectID != 0 {
				if obj, oOK, _ := tx.MetadataObjectByID(r.ObjectID); oOK {
					label = obj.NameDisplay + "." + r.NameNorm
				}
			}
			rc := &candidate{
				id: fmt.Sprintf("qs:%s:%d:%d", owner.UID, i, j), bucket: bucketRelation, category: cat,
				kindLabel: "query_reference", fromDisplay: owner.NameDisplay, display: label, detail: r.Kind,
				component: owner.ComponentID, confidence: domain.Confidence(q.Confidence),
				whyIncluded: fmt.Sprintf("%s %q в схеме запроса %s", r.Kind, r.NameNorm, owner.NameDisplay) + whyTail,
				charCost:    runeLen(label) + 8,
			}
			out = append(out, bctx.apply(scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: anchorComp}, rc))
		}
	}
	return out, warnings
}

// --- rights ------------------------------------------------------------

// expandRights — intent "rights": roles + rights + rls + profiles.
// Профили групп доступа (AccessGroupProfile XML) не разбираются НИ ОДНИМ
// пакетом parse/* в этой волне — profiles всегда missing, честно.
//
// view=effective (ADR-035): права ролей расширения индекс связывает со
// строкой объекта в компоненте роли, то есть с заимствованием, а не с
// базовой строкой (resolveRoleObjectNode). Поэтому базовый анкер
// дополняется правами на заимствования в применяющихся расширениях
// (effective.BorrowedObjects), каждая роль со своим слоем. RLS без строки
// права не бывает, и когда права всех слоёв прочитаны, категория rls
// заявляется собранной (declareCollected, ADR-030). roles/rights не
// заявляются: отсутствие строки role_right не означает отсутствия доступа
// (дефолты и setForNewObjects). raw не меняется.
func expandRights(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	if a.Kind != "metadata_object" {
		return nil, nil
	}
	tx := bctx.tx
	obj, ok, err := resolveMetadataAnchor(tx, a)
	if err != nil || !ok {
		return nil, nil
	}
	type layerRights struct {
		obj        store.MetadataObjectRow
		rows       []store.RoleRightRow
		borrowedBy string
	}
	rows, rerr := tx.RoleRightsByObjectID(obj.ID)
	layers := []layerRights{{obj: obj, rows: rows}}
	total := len(rows)
	var warnings []Warning
	if bctx.view == domain.ViewEffective && !bctx.extensionComponents[obj.ComponentID] {
		readOK := rerr == nil
		borrowed, berr := effective.BorrowedObjects(tx, obj)
		if berr != nil {
			readOK = false
			warnings = append(warnings, borrowedObjectsReadFailed(obj, berr))
		}
		for _, b := range borrowed {
			bRows, bErr := tx.RoleRightsByObjectID(b.ID)
			if bErr != nil {
				readOK = false
				warnings = append(warnings, Warning{
					Code: "effective_borrowed_rights_read_failed",
					Message: fmt.Sprintf("не удалось прочитать права ролей расширения %s на %s: %v; права этого слоя в ответ не вошли",
						b.ComponentID, obj.NameDisplay, bErr),
					Hint: "повторите вызов после reindex",
				})
				continue
			}
			layers = append(layers, layerRights{obj: b, rows: bRows, borrowedBy: b.ComponentID})
			total += len(bRows)
		}
		if readOK {
			bctx.declareCollected("rls")
		} else {
			bctx.declareCollectionFailed("rls")
		}
	}
	if total == 0 {
		return nil, append(warnings, Warning{Code: "no_role_rights", Message: fmt.Sprintf("для %s нет строк role_right в индексе", obj.NameDisplay)})
	}
	sc := scoreCtx{depth: 1, anchorStrength: a.Strength, direction: 1.0, anchorComp: obj.ComponentID}
	var out []*candidate
	seenRole := map[int64]bool{}
	for _, l := range layers {
		whyTail := ""
		if l.borrowedBy != "" {
			whyTail = fmt.Sprintf(" (на заимствование объекта расширением %s, effective)", l.borrowedBy)
		}
		for _, rr := range l.rows {
			if !seenRole[rr.RoleID] {
				seenRole[rr.RoleID] = true
				rc := &candidate{
					id: fmt.Sprintf("role:%d", rr.RoleID), bucket: bucketFact, category: "roles",
					display: rr.RoleNameDisplay, component: rr.RoleComponentID, confidence: 1,
					whyIncluded: fmt.Sprintf("роль %s имеет право на %s", rr.RoleNameDisplay, obj.NameDisplay) + whyTail,
					charCost:    runeLen(rr.RoleNameDisplay) + 8,
				}
				out = append(out, bctx.apply(sc, rc))
			}
			rightLabel := fmt.Sprintf("%s.%s=%v", rr.RoleNameDisplay, rr.RightName, rr.Value)
			rc := &candidate{
				id: fmt.Sprintf("right:%d:%s", rr.RoleID, rr.RightName), bucket: bucketFact, category: "rights",
				display: rightLabel, component: rr.RoleComponentID, confidence: 1,
				whyIncluded: fmt.Sprintf("право %s роли %s на %s (value=%v, setForNewObjects=%v)", rr.RightName, rr.RoleNameDisplay, obj.NameDisplay, rr.Value, rr.SetForNewObjects) + whyTail,
				charCost:    runeLen(rightLabel) + 8,
			}
			out = append(out, bctx.apply(sc, rc))
			if rr.RLS != "" {
				rlsC := &candidate{
					id: fmt.Sprintf("rls:%d:%s", rr.RoleID, rr.RightName), bucket: bucketFact, category: "rls",
					display: rr.RoleNameDisplay + "." + rr.RightName, detail: rr.RLS, component: rr.RoleComponentID, confidence: 1,
					whyIncluded: fmt.Sprintf("RLS роли %s на %s", rr.RoleNameDisplay, obj.NameDisplay) + whyTail,
					charCost:    runeLen(rr.RLS) + 12,
				}
				out = append(out, bctx.apply(sc, rlsC))
			}
		}
	}
	warnings = append(warnings, Warning{
		Code:    "profiles_not_indexed",
		Message: "профили групп доступа (AccessGroupProfile) не разбираются индексом — категория profiles всегда missing",
		Hint:    "проверьте профили вручную через rights_audit",
	})
	return out, warnings
}

// --- posting -----------------------------------------------------------

// postingHandlerNameNorm — имя обработчика проведения базового слоя. Имя
// платформенное (событие модуля объекта), поэтому в БАЗОВОМ слое оно и есть
// факт; перехватчик расширения зовётся иначе (РасшА_ОбработкаПроведения) и по
// этому имени не ищется НИКОГДА — он находится фактом перехвата, см.
// effectivePostingIntercepts.
const postingHandlerNameNorm = "обработкапроведения"

// postingObjectMType — единственный вид объекта, у которого событие
// ОбработкаПроведения бывает: проводится в 1С только документ. Нужен там, где
// сказанное про ОТСУТСТВИЕ обработчика было бы шумом: у справочника, регистра
// или константы его нет и быть не может, а на реальной выгрузке анкер по
// имени приходит сразу нескольких видов (одно имя может носить и
// документ, и регистр сведений).
const postingObjectMType = "Document"

// findPostingHandler ищет обработчик проведения, принадлежащий МОДУЛЮ САМОГО
// объекта: путь модуля символа лежит внутри каталога МОДУЛЕЙ этого объекта
// (objectModuleDirs по строке source_file его объявления) — факт из индекса,
// а не вхождение имени объекта в путь модуля (П2.2/R19). Прежнее правило
// (strings.Contains(modulePath, obj.NameNorm)) отдавало обработчик ЧУЖОГО
// документа-омонима: путь "Documents/ЗаказКлиента/Ext/ObjectModule.bsl"
// содержит подстроку "заказ", и первый же такой символ выигрывал.
//
// Каталог модулей — НЕ каталог файла объявления: DumpConfigToFiles кладёт
// объявление в "Documents/Штрафы.xml", и каталогом файла оказывается
// "Documents" — префикс, общий ЛЮБОМУ документу конфигурации (регрессия,
// найденная приёмкой на реальной выгрузке: анкер Штрафы отдавал
// обработчик другого документа). Правильный каталог — "Documents/Штрафы", см.
// objectModuleDirs.
//
// Если каталог модулей вывести не из чего (файла объявления в индексе нет
// либо его имя не совпадает с именем объекта), сохраняется прежнее правило
// подстроки как единственный оставшийся сигнал — явный откат, а не основной
// путь.
//
// Третьим значением возвращается ПРИМЕНЁН ЛИ ОТКАТ. Это не деталь реализации:
// откат — то самое правило, которое отдавало обработчик чужого документа
// (D04), и ответ, построенный на нём, стоит слабее ответа по факту владения.
// Вызывающий обязан назвать это предупреждением, иначе исправленный дефект
// возвращается тихо и неотличимо от штатного ответа.
func findPostingHandler(tx *store.ReadTx, obj store.MetadataObjectRow) (store.SymbolRow, bool, bool, error) {
	matches, err := tx.FindSymbols(store.SymbolSearch{NameNorm: postingHandlerNameNorm, ComponentID: obj.ComponentID, Limit: 50})
	if err != nil {
		return store.SymbolRow{}, false, false, err
	}
	ownerDir := ""
	if obj.FileID != 0 {
		sf, sfOK, sfErr := tx.SourceFileByID(obj.FileID)
		if sfErr != nil {
			return store.SymbolRow{}, false, false, sfErr
		}
		if sfOK {
			ownerDir = objectModuleDir(sf.RelPath, obj.NameNorm)
		}
	}
	viaFallback := ownerDir == ""
	for _, m := range matches {
		if m.NameNorm != postingHandlerNameNorm {
			continue
		}
		modulePath := domain.NormalizeName(domain.NormalizeModulePath(m.ModulePath))
		if !viaFallback {
			if strings.HasPrefix(modulePath, ownerDir+"/") {
				return m, true, false, nil
			}
			continue
		}
		if strings.Contains(modulePath, obj.NameNorm) {
			return m, true, true, nil
		}
	}
	return store.SymbolRow{}, false, viaFallback, nil
}

// postingOwnerUnknownWarning называет применение отката на правило подстроки
// (см. findPostingHandler). Отдельная функция, а не литерал по месту: текст
// описывает СЛАБОСТЬ ответа и обязан быть один и тот же у всех вызывающих.
//
// Исходов у отката ДВА, и общего текста у них нет. Откат, который отдал
// символ, ставит под сомнение ПРИНАДЛЕЖНОСТЬ найденного обработчика (это и
// есть дефект D04: путь чужого документа-омонима содержит имя нашего).
// Откат, не нашедший ничего, не отдаёт омонима — он оставляет категорию
// posting_handler пустой, и пустота эта ничего не доказывает. Один текст на
// оба исхода отправлял потребителя искать в ответе омонима, которого там
// нет, — поэтому исход называется явно.
func postingOwnerUnknownWarning(obj store.MetadataObjectRow, handler store.SymbolRow, found bool) Warning {
	prefix := fmt.Sprintf(
		"каталог модулей объекта %s из индекса не выводится (строка source_file его объявления отсутствует либо названа не по объекту)",
		obj.NameDisplay)
	outcome := "обработчик проведения искался прежним правилом подстроки и не найден — пустая категория posting_handler здесь не доказывает, что обработчика нет"
	hint := "проверьте, что объявление объекта проиндексировано (reindex), и ищите обработчик вручную: find_symbol name=ОбработкаПроведения"
	if found {
		outcome = fmt.Sprintf(
			"обработчик проведения искался прежним правилом подстроки и найден в модуле %s — это правило отдаёт обработчик документа-омонима, поэтому принадлежность найденного не доказана",
			handler.ModulePath)
		hint = "проверьте, что объявление объекта проиндексировано (reindex), и сверьте модуль найденного обработчика с самим объектом"
	}
	return Warning{
		Code:    "posting_handler_owner_unknown",
		Message: prefix + " — " + outcome,
		Hint:    hint,
	}
}

// objectModuleDir — каталог, внутри которого лежат модули объекта, выведенный
// из пути его объявления. Нормализация та же, в которой сравниваются пути
// модулей (слэши, регистр, составленная кириллица).
//
// Правило: каталог объекта — тот, чьё последнее имя И ЕСТЬ имя объекта.
// Кандидатов ровно два, в порядке проверки: каталог самого файла объявления
// (вложенная раскладка "Documents/Заказ/Заказ.xml" -> "documents/заказ") и
// путь объявления без расширения (раскладка DumpConfigToFiles
// "Documents/Штрафы.xml" -> "documents/штрафы", модули лежат в
// "Documents/Штрафы/Ext/...").
//
// Условие «последнее имя = имя объекта» и отсекает вырожденный "Documents":
// на реальной выгрузке каталог файла объявления — общий префикс ВСЕХ
// документов конфигурации, и по нему подходит любой обработчик проведения
// (регрессия, найденная приёмкой на реальной выгрузке). Пустой результат
// означает «владение вывести не из чего»: вызывающий честно откатывается на
// прежнее правило подстроки, а не считает, что подходит всё.
func objectModuleDir(relPath, objNameNorm string) string {
	p := domain.NormalizeName(domain.NormalizeModulePath(relPath))
	if p == "" {
		return ""
	}
	slash := strings.LastIndex(p, "/")
	withoutExt := p
	if dot := strings.LastIndex(p, "."); dot > slash {
		withoutExt = p[:dot]
	}
	candidates := []string{}
	if slash > 0 {
		candidates = append(candidates, p[:slash])
	}
	candidates = append(candidates, withoutExt)
	for _, dir := range candidates {
		base := dir
		if i := strings.LastIndex(dir, "/"); i >= 0 {
			base = dir[i+1:]
		}
		if base == objNameNorm {
			return dir
		}
	}
	return ""
}

// postingObjectModulePath — путь модуля объекта, ПО КОТОРОМУ расширения
// заимствуют обработчик проведения, когда в базовом слое этого модуля нет
// вовсе (бывает, что у документа каталога Documents/<Имя>/Ext/ не
// существует — свойство конфигурации, не дефект выгрузки). Обычный путь берёт этот путь у НАЙДЕННОГО базового
// символа (row.ModulePath); брать его там нечем, а перехватчик расширения —
// единственный исполняемый код проведения, и молчать о нём нельзя (ADR-034).
//
// Путь СТРОИТСЯ единственным построителем раскладки (workspace.DumpModulePath),
// а не склейкой литералов здесь: второе место, знающее раскладку, — это то
// самое расползание, ради устранения которого заведён dumplayout.go (ADR-033).
//
// Построенное СВЕРЯЕТСЯ с обратным разбором объявления (objectModuleDir):
// каталог построенного пути обязан совпасть с каталогом модулей самого
// объекта. Без сверки объект, объявление которого лежит не по раскладке,
// получил бы выдуманный путь, и перехватчики искались бы по чужому адресу —
// ровно тот класс тихой неправды, против которого ADR-029 п.1. Не совпало
// (или объявления нет в индексе) — false: вызывающий обязан сказать, что
// проверить перехват было негде, а не молча вернуть пустоту.
func postingObjectModulePath(tx *store.ReadTx, obj store.MetadataObjectRow) (string, bool, error) {
	if obj.FileID == 0 {
		return "", false, nil
	}
	sf, ok, err := tx.SourceFileByID(obj.FileID)
	if err != nil || !ok {
		return "", false, err
	}
	ownerDir := objectModuleDir(sf.RelPath, obj.NameNorm)
	if ownerDir == "" {
		return "", false, nil
	}
	// Вид объекта приходит ИЗ ИНДЕКСА (корневой элемент <MetaDataObject>,
	// internal/parse/meta), белым списком там он не ограничен, и карта
	// раскладки заведомо неполна (ExternalDataProcessor, Recalculation, Bot).
	// Строители Dump* на неописанном виде ПАНИКУЮТ — правильно для фикстуры,
	// смертельно здесь: recover в сервере ровно один и он в internal/store,
	// вокруг диспетчеризации инструментов его нет, поэтому паника кладёт весь
	// процесс вместе с индексом. Двухзначная форма — тот же самый словарь,
	// поэтому разъехаться проверке со строителем не с чем.
	if _, known := workspace.DumpCollectionDir(obj.MType); !known {
		return "", false, nil
	}
	built := workspace.DumpModulePath(obj.MType, obj.NameDisplay, workspace.ModuleObject)
	norm := domain.NormalizeName(domain.NormalizeModulePath(built))
	if !strings.HasPrefix(norm, ownerDir+"/") {
		return "", false, nil
	}
	return built, true, nil
}

// postingBaseHandlerMissingWarning — пункт 3 таска 10: у объекта нет
// СОБСТВЕННОГО ОбработкаПроведения, и это обязано быть сказано вслух.
// Молчание здесь хуже пустоты: агент, увидев posting_handler=missing и
// movements=missing без единого слова, заключает, что проведение не
// дорабатывалось, — тогда как перехватчик расширения его переписывает.
//
// Исходов три, и общего текста у них нет, поэтому исход называется явно
// (тот же приём, что postingOwnerUnknownWarning):
//   - перехватчики есть — они и есть единственный исполняемый код;
//   - перехватчиков нет — проведения у объекта нет ни в одном слое;
//   - проверить было негде (модуль объекта из индекса не выводится) — про
//     расширения ответ не знает ничего и не притворяется, что знает.
func postingBaseHandlerMissingWarning(obj store.MetadataObjectRow, interceptors []string, checked bool) Warning {
	prefix := fmt.Sprintf("у объекта %s нет собственного обработчика проведения: модуля объекта в базовом слое нет",
		obj.NameDisplay)
	switch {
	case !checked:
		return Warning{
			Code:    "posting_base_handler_missing",
			Message: prefix + " — и путь модуля объекта из индекса не выводится, поэтому перехватчики расширений по нему не проверялись",
			Hint:    "убедитесь, что объявление объекта проиндексировано (reindex), и посмотрите модуль объекта в расширениях вручную: get_module_structure view=effective",
		}
	case len(interceptors) == 0:
		return Warning{
			Code:    "posting_base_handler_missing",
			Message: prefix + ", и ни одно применяющееся расширение не перехватывает ОбработкаПроведения — проведение этого объекта не описано кодом ни в одном слое",
			Hint:    "движения такого документа обычно пишет подписка на событие (категория subscriptions ответа) либо внешний код",
		}
	default:
		return Warning{
			Code: "posting_base_handler_missing",
			Message: prefix + fmt.Sprintf(
				", проведение целиком описано перехватчиками расширений (%s) — они единственный исполняемый здесь код",
				strings.Join(interceptors, ", ")),
			Hint: "исходник перехватчика: get_module_structure component=<расширение> view=effective",
		}
	}
}

// expandPosting — intent "posting": posting_handler + movements +
// register_access + subscriptions. Anchor-символ трактуется как обработчик
// проведения напрямую; anchor-объект (документ) ищет обработчик по факту
// принадлежности своему модулю (findPostingHandler), а при view=effective
// добавляет к нему перехватчики расширений и ИХ обращения к регистрам
// (effectivePostingIntercepts).
func expandPosting(bctx *buildCtx, a Anchor) ([]*candidate, []Warning) {
	tx := bctx.tx
	var handler store.SymbolRow
	var ownerComponent string
	var ok bool
	var err error
	var out []*candidate
	var warnings []Warning

	switch a.Kind {
	case "symbol":
		handler, ok, err = symbolByUID(tx, a.UID)
		ownerComponent = handler.ComponentID
	case "metadata_object":
		var obj store.MetadataObjectRow
		obj, ok, err = resolveMetadataAnchor(tx, a)
		if err == nil && ok {
			ownerComponent = obj.ComponentID
			// Подписки собираются ДО поиска обработчика и независимо от того,
			// нашёлся ли он: подписка дописывает движения мимо модуля объекта,
			// поэтому документ без собственного ОбработкаПроведения — не повод
			// промолчать про неё (П3.3/R29).
			subCands, subWarnings := postingSubscriptions(bctx, a, obj)
			out = append(out, subCands...)
			warnings = append(warnings, subWarnings...)
			var viaFallback bool
			handler, ok, viaFallback, err = findPostingHandler(tx, obj)
			if viaFallback {
				warnings = append(warnings, postingOwnerUnknownWarning(obj, handler, err == nil && ok))
			}
			if err == nil && !ok && !viaFallback && obj.MType == postingObjectMType &&
				!bctx.extensionComponents[obj.ComponentID] {
				// Базового обработчика нет, а каталог модулей объекта
				// известен: единственным исполняемым кодом проведения могут
				// быть перехватчики расширений. Раньше здесь возвращалась
				// пустота без единого предупреждения (ADR-034).
				//
				// Условие !viaFallback узкое намеренно: при откате каталог
				// модулей вывести не из чего, и postingOwnerUnknownWarning уже
				// сказал ровно это — второе предупреждение о том же добавило бы
				// шума, а не правды.
				//
				// Условие «компонент объекта не расширение» — находка живого
				// вызова: заимствованный документ имеет строку metadata_object
				// в ОБОИХ компонентах, анкеров по имени два, и путь
				// высказывался дважды. Второй раз — про компонент, к которому
				// расширений не применяется, то есть «ни одно расширение не
				// перехватывает», прямо против первого. Вопрос «перекрывает ли
				// расширение недостающий базовый метод» осмыслен только для
				// объекта БАЗОВОЙ конфигурации; про заимствованную копию на
				// него отвечает анкер базового слоя.
				icCands, icWarnings := postingInterceptsWithoutBaseHandler(bctx, obj)
				out = append(out, icCands...)
				warnings = append(warnings, icWarnings...)
			}
		}
	}
	if err != nil || !ok {
		return out, warnings
	}

	sc := scoreCtx{depth: 0, anchorStrength: a.Strength, direction: 1.0, anchorComp: ownerComponent}
	params, _ := tx.SymbolParameters(handler.ID)
	out = append(out, makeDefinitionSnippet(bctx, sc, handler, params, "posting_handler",
		fmt.Sprintf("обработчик проведения %s (posting_handler)", handler.NameDisplay)))
	accCands, accWarnings := postingRegisterAccesses(bctx, ownerComponent, handler.ID, handler.NameDisplay, "")
	out = append(out, accCands...)
	warnings = append(warnings, accWarnings...)
	// effective (П2.3/П2.4): модуль объекта — обычный заимствуемый модуль,
	// обработчик проведения перехватывается расширением тем же способом, что
	// уже показывает форма (handler_intercepts выше в этом файле).
	// "posting_handler_intercepts" — НЕ обязательная категория posting
	// (requiredCategoryMap её не называет): обязательная объявила бы
	// недостаточным любой ответ по документу без расширений. Факты
	// перехватчиков добавляются ТОЛЬКО при view=effective — raw остаётся
	// прежним (R25).
	if bctx.view == domain.ViewEffective {
		icCands, icWarnings := effectivePostingIntercepts(bctx, handler, ownerComponent)
		out = append(out, icCands...)
		warnings = append(warnings, icWarnings...)
	}

	return out, warnings
}

// subscriptionSourceCandidates — нормализованные варианты source_name_norm,
// под которыми подписка на этот объект могла быть записана платформой:
// конкретный тип ("<mtype>object.<имя>") и голый вид, покрывающий все объекты
// этого MType ("<mtype>object"). Правило то же, что у internal/app (get_object,
// meta.go): retrieve не импортирует app (граница internal/arch), поэтому
// строит кандидатов сам — не переиспользует чужую функцию через слой выше.
// Ref/Manager-формы источника не перечислены — тот же предел, что у app.
func subscriptionSourceCandidates(mtype, nameDisplay string) []string {
	return []string{
		domain.NormalizeName(mtype + "Object." + nameDisplay),
		domain.NormalizeName(mtype + "Object"),
	}
}

// postingSubscriptions наполняет обязательную категорию subscriptions
// (П3.3/R29, R30). Источник — event_subscription (store.
// EventSubscriptionsBySourceNames), отбор по источнику подписки: объект-анкер
// в конкретной и в голой форме.
//
// «Подписки расширений — как перехватчики» (ADR-030) прочитано буквально:
// опрашивается компонент самого объекта ПЛЮС все расширения, применяющиеся к
// нему, тем же effective.ApplyingTo, что отбирает компоненты для
// перехватчиков (internal/effective). Второй версии этого отбора здесь нет.
// component/layer факта — компонент, ОБЪЯВИВШИЙ подписку, а не компонент
// документа: подписка расширения на базовый документ обязана быть видна и
// обязана нести свой слой. Дополнительной фильтрации по слою нет.
//
// Подписки наполняются в ОБОИХ view: подписка — опубликованный факт
// метаданных, а не вычисляемое на чтении слияние перехватчиков, поэтому
// правило «raw не несёт фактов расширений» (R25) на неё не распространяется —
// иначе raw объявлял бы категорию честно пустой, зная обратное.
func postingSubscriptions(bctx *buildCtx, a Anchor, obj store.MetadataObjectRow) ([]*candidate, []Warning) {
	readFailed := func(what string, err error) []Warning {
		bctx.declareCollectionFailed("subscriptions")
		return []Warning{{
			Code:    "posting_subscriptions_read_failed",
			Message: fmt.Sprintf("не удалось прочитать %s для %s: %v", what, obj.NameDisplay, err),
			Hint:    "категория subscriptions не собрана — повторите вызов после reindex",
		}}
	}
	rows, err := bctx.tx.EventSubscriptionsBySourceNames(subscriptionSourceCandidates(obj.MType, obj.NameDisplay))
	if err != nil {
		return nil, readFailed("event_subscription", err)
	}
	comps, cerr := bctx.tx.Components()
	if cerr != nil {
		return nil, readFailed("состав компонентов", cerr)
	}
	// Заявление делается ПОСЛЕ обоих удачных чтений и не зависит от того,
	// нашлась ли хоть одна подписка: пустота здесь честная (D03).
	bctx.declareCollected("subscriptions")
	if len(rows) == 0 {
		return nil, nil
	}
	allowed := map[string]bool{obj.ComponentID: true}
	for _, ext := range effective.ApplyingTo(effective.ExtensionsFromStore(comps), obj.ComponentID) {
		allowed[ext.ID] = true
	}
	sc := scoreCtx{depth: 1, anchorStrength: a.Strength, direction: 1.0, anchorComp: obj.ComponentID}
	var out []*candidate
	for _, r := range rows {
		if !allowed[r.ComponentID] {
			continue
		}
		detail := fmt.Sprintf("событие %s, обработчик %s", r.Event, r.HandlerNameNorm)
		c := &candidate{
			id: fmt.Sprintf("subscription:%d", r.ID), bucket: bucketFact, category: "subscriptions",
			display: r.NameDisplay, detail: detail, component: r.ComponentID, confidence: 1,
			resolution: r.Resolution,
			whyIncluded: fmt.Sprintf("подписка %s: источник %s, событие %s (объявлена компонентом %s)",
				r.NameDisplay, r.SourceNameNorm, r.Event, r.ComponentID),
			charCost: runeLen(r.NameDisplay) + runeLen(detail) + 8,
		}
		out = append(out, bctx.apply(sc, c))
	}
	return out, nil
}

// postingRegisterAccesses строит movements/register_access по обращениям
// ОДНОГО символа. component факта берётся из самой строки register_access
// (source_file.component_id), а не у документа-анкера: у перехватчика
// расширения это его собственный слой (П2.4/R21).
// Обе категории — movements и register_access — заявляются собранными
// (D03) только по УДАЧНОМУ чтению register_access: сбой чтения отзывает
// заявление и уходит предупреждением, иначе пустота категории соврала бы
// ровно в том случае, ради честности которого complete_empty и введён.
func postingRegisterAccesses(bctx *buildCtx, ownerComponent string, symbolID int64, fromDisplay, whySuffix string) ([]*candidate, []Warning) {
	accesses, err := bctx.tx.RegisterAccessesBySymbol(symbolID)
	if err != nil {
		bctx.declareCollectionFailed("movements", "register_access")
		return nil, []Warning{{
			Code:    "posting_register_access_read_failed",
			Message: fmt.Sprintf("не удалось прочитать обращения к регистрам символа %s: %v", fromDisplay, err),
			Hint:    "категории movements/register_access не собраны — повторите вызов после reindex",
		}}
	}
	bctx.declareCollected("movements", "register_access")
	var out []*candidate
	for _, r := range accesses {
		cat := "register_access"
		if r.Mode == "movement" {
			cat = "movements"
		}
		label := fmt.Sprintf("%s (%s)", r.RegisterNameNorm, r.Mode)
		why := fmt.Sprintf("register_access mode=%s внутри %s", r.Mode, fromDisplay)
		if whySuffix != "" {
			why += " " + whySuffix
		}
		rc := &candidate{
			id: fmt.Sprintf("posting-access:%d", r.ID), bucket: bucketRelation, category: cat,
			kindLabel: "register_access", fromDisplay: fromDisplay, display: label,
			component: r.ComponentID, confidence: domain.Confidence(r.Confidence), span: r.Span,
			whyIncluded: why,
			charCost:    runeLen(label) + 8,
		}
		out = append(out, bctx.apply(scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: ownerComponent}, rc))
	}
	return out, nil
}
