package index

import (
	"fmt"
	"sort"

	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// publishDerivedModuleFacts вставляет факты, которые resolve.Derive*
// (таск 08) строит из уже готового bsl.Module+Env: доступы к регистрам и
// обработчики формы. Требует прохода 2 (Env те же цели, что и ссылки —
// объекты/члены метаданных уже опубликованы в проходе 1, символы формы
// могут быть в другом файле той же транзакции).
func publishDerivedModuleFacts(tx *store.WriteTx, in publishInput, ts *txState, rel string, st modulePublishState, lp *linkPlan) error {
	if err := publishRegisterAccess(tx, in, ts, rel, st, lp.registerAccess); err != nil {
		return fmt.Errorf("register_access: %w", err)
	}
	if err := publishQueries(tx, ts, rel, st, lp.queries); err != nil {
		return fmt.Errorf("query: %w", err)
	}
	return nil
}

// publishRegisterAccess вставляет register_access. resolve.RegisterAccessResult
// не несёт индекс метода-владельца (в отличие от bsl.RegisterAccess.Method),
// но DeriveRegisterAccess строит результат по mod.RegisterAccesses В ТОМ ЖЕ
// порядке 1:1 (обычный map, без фильтрации/расщепления, в отличие от
// DeriveQueryReference) — i-й результат соответствует i-й записи входа,
// поэтому Method восстанавливается тем же приёмом, что и у ссылок
// (refCallMethodIndexes).
func publishRegisterAccess(tx *store.WriteTx, in publishInput, ts *txState, rel string, st modulePublishState, plans []registerAccessPlan) error {
	for _, p := range plans {
		row := p.row
		row.FileID = st.fileID
		if p.symbolKey != "" {
			row.SymbolID = ts.nodes.byKey[p.symbolKey]
		}
		if p.objectKey != "" {
			if id, ok, err := ts.nodes.lookup(tx, p.objectKey); err != nil {
				return err
			} else if ok {
				row.ObjectID = id
			}
		}
		if err := tx.InsertRegisterAccess(row); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		ts.counts.registerAccess++
		// Та же строка, но в виде читалки: публикация объектного графа
		// берёт вход отсюда, а не вторым SELECT-ом по каждому символу.
		ts.registerAccess = append(ts.registerAccess, store.RegisterAccessRow{
			FileID: row.FileID, ComponentID: string(in.component), SymbolID: row.SymbolID,
			ObjectID: row.ObjectID, RegisterNameNorm: row.RegisterNameNorm, Mode: row.Mode,
			InTransaction: row.InTransaction, Static: row.Static, Confidence: row.Confidence,
			Span: row.Span, Layer: row.Layer,
		})
	}
	return nil
}

// publishQueries вставляет узлы query (текст, span, staticity) для каждого
// литерала bsl.QueryLiteral — не только static, все staticity: сам текст и
// его границы полезны (R28) независимо от того, разобран ли он. Следом, для
// static-литералов, вставляет их query_reference — таск 12 научил
// resolve.DeriveQueryReference группировать результат по литералу
// (QueryLiteralReferences.LiteralIndex), поэтому i-й литерал этого цикла и
// i-й LiteralIndex дериватора теперь один и тот же индекс в mod.Queries, и
// query_id, вставленный здесь строкой выше, находится без второго разбора
// текста запроса (второй резолвер вне internal/resolve запрещён, §6).
func publishQueries(tx *store.WriteTx, ts *txState, rel string, st modulePublishState, plans []queryPlan) error {
	for i := range plans {
		q := &plans[i]
		var symbolID int64
		if q.symbolKey != "" {
			symbolID = ts.nodes.byKey[q.symbolKey]
		}
		if symbolID == 0 {
			// Запрос вне метода (маловероятно для BSL): без symbol_id
			// вставить query нельзя (NOT NULL, §15), пропускаем вместе с его
			// использованиями, им некуда прикрепиться.
			continue
		}
		row := q.row
		row.SymbolID, row.FileID = symbolID, st.fileID
		queryID, err := tx.InsertQuery(row)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		ts.counts.query++
		if err := publishQueryReferences(tx, ts, queryID, q.refs); err != nil {
			return fmt.Errorf("query_reference %s: %w", rel, err)
		}
	}
	return nil
}

// publishQueryReferences вставляет query_reference для static-литералов
// (partial и dynamic текст resolve.DeriveQueryReference честно не отдаёт —
// см. doc-комментарий DeriveQueryReference, это остаётся долгом: сборка
// partial-текста из GapMarker-фрагментов не сделана этим таском, только
// группировка static-результата по литералу).
func publishQueryReferences(tx *store.WriteTx, ts *txState, queryID int64, refs []queryRefPlan) error {
	for _, r := range refs {
		row := r.row
		row.QueryID = queryID
		if r.objectKey != "" {
			if id, ok, err := ts.nodes.lookup(tx, r.objectKey); err != nil {
				return err
			} else if ok {
				row.ObjectID = id
			}
		}
		if r.memberKey != "" {
			if id, ok, err := ts.nodes.lookup(tx, r.memberKey); err != nil {
				return err
			} else if ok {
				row.MemberID = id
			}
		}
		if err := tx.InsertQueryReference(row); err != nil {
			return err
		}
		ts.counts.queryReference++
	}
	return nil
}

// publishHandlerBindingsForForm вставляет handler_binding для формы, чья
// структура (Form.xml) republish-ится этим проходом: обработчики
// (FormStructureFact.Handlers) резолвятся против Env модуля формы —
// DeriveHandlerBinding сам ищет formModulePath в Env, промах — Resolution
// unresolved у КАЖДОГО обработчика (R43.1), не пустая выдача.
func publishHandlerBindingsForForm(tx *store.WriteTx, ts *txState, rel string, hp *handlerBindingsPlan) error {
	formID, ok, err := ts.nodes.lookup(tx, hp.formKey)
	if err != nil {
		return err
	}
	if !ok {
		return nil // форма не опубликована (не должно случаться после publishFormStructure того же файла)
	}
	fileID, ok := ts.fileID[rel]
	if !ok {
		return nil
	}
	for _, b := range hp.bindings {
		row := b.row
		row.FormID, row.OriginFileID = formID, fileID
		if b.handlerKey != "" {
			if id, ok, err := ts.nodes.lookup(tx, b.handlerKey); err != nil {
				return err
			} else if ok {
				row.HandlerSymbolID = id
			}
		}
		if err := tx.InsertHandlerBinding(row); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		ts.counts.handlerBinding++
	}
	return nil
}

// publishDependencyEdges строит field-typed-by рёбра (resolve.
// DeriveDependencyEdges — единственный вид, реализованный резолвером; три
// остальных вида из таска 08 остаются нереализованными, см. doc.go) для
// членов метаданных, republish-нутых этой транзакцией. Остальные (не
// затронутые инкрементом) уже несли бы свои рёбра с прошлого поколения,
// если бы этот код был включён раньше — сейчас он включается впервые, поэтому
// первый full rebuild покрывает все члены разом.
func publishDependencyEdges(tx *store.WriteTx, in publishInput, ts *txState) error {
	results := resolve.DeriveDependencyEdges(in.env)
	if len(results) == 0 {
		return nil
	}
	// memberFile — identity_key члена -> relPath файла, где он объявлен
	// (только среди republish-нутых файлов: остальным чужой origin_file_id
	// не поставить корректно без лишнего чтения, они получат рёбра на
	// следующей полной пересборке).
	memberFile := make(map[string]string)
	for _, rel := range in.republish {
		rec := in.corpus.files[rel]
		if rec == nil || rec.metaFacts.Object == nil {
			continue
		}
		objKey := metadataObjectIdentityKey(in.component, rec.metaFacts.Object.MType, rec.metaFacts.Object.NameNorm)
		for _, m := range rec.metaFacts.Members {
			memberFile[metadataMemberIdentityKey(objKey, m)] = rel
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].FromKey != results[j].FromKey {
			return results[i].FromKey < results[j].FromKey
		}
		return results[i].ToKey < results[j].ToKey
	})
	for _, r := range results {
		rel, ok := memberFile[r.FromKey]
		if !ok {
			continue // член не republish-ится этим проходом
		}
		fromID, ok, err := ts.nodes.lookup(tx, r.FromKey)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		toID, ok, err := ts.nodes.lookup(tx, r.ToKey)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		fileID, ok, err := tx.SourceFileID(string(in.component), rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := tx.InsertDependencyEdge(store.DependencyEdge{
			Kind: string(r.Kind), FromNode: fromID, ToNode: toID,
			OriginFileID: fileID, Confidence: float64(r.Confidence), Layer: layerName(in.layer),
		}); err != nil {
			return fmt.Errorf("dependency_edge %s->%s: %w", r.FromKey, r.ToKey, err)
		}
		ts.counts.dependencyEdge++
	}
	return nil
}
