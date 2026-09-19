package store

import (
	"database/sql"
	"strings"
)

// Файл: типизированные выборки, нужные get_context_for_task и
// которых не было у других инструментов: список компонентов проекта
// (кто из них extension — нужно для поиска перехватчиков в expansion),
// поиск объекта метаданных по имени БЕЗ известного mtype (anchors по
// свободному тексту задачи не называют вид объекта явно), подстрочный поиск
// метаданных (fuzzy-anchor стадия §24: «нормализация и синонимы»,
// используется как приближение синонимов метаданных без отдельного
// словаря) и полнотекстовый поиск по символам через fts_symbols (стадия
// «FTS по именам/докам»). SQL живёт только здесь (RuleSQLOnlyInStore).

// Components перечисляет все компоненты активного проекта, как они
// зарегистрированы в store (id, kind, apply_order, display) — retrieve не
// зависит от internal/workspace (Build получает только *store.ReadTx),
// поэтому вид компонента (configuration/extension/...) для «перехватчиков в
// расширениях» (сценарий §25 №2) берётся отсюда, не из манифеста.
func (tx *ReadTx) Components() ([]Component, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,kind,root,applies_to,apply_order,display FROM component ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Component
	for rows.Next() {
		var c Component
		var appliesTo, display sql.NullString
		var applyOrder sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Kind, &c.Root, &appliesTo, &applyOrder, &display); err != nil {
			return nil, err
		}
		c.AppliesTo, c.Display, c.ApplyOrder = appliesTo.String, display.String, int(applyOrder.Int64)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SymbolsByNameNormExact ищет символы РОВНО по нормализованному имени —
// `name_norm = ?`, использует idx_symbol_name (раздел 15 схемы). Отдельная
// функция от FindSymbols (read_symbol.go) намеренно: FindSymbols
// строит `name_norm LIKE '%x%'` (задача инструмента find_symbol — подстрочный
// поиск), а LIKE с ведущим wildcard не может использовать B-tree индекс —
// SQLite обязан пройти ВСЮ таблицу symbol. Стадия «точный lookup» anchors
// (§24 шаг 2) вызывается на КАЖДОЕ слово-кандидат текста задачи; на реальной
// выгрузке (десятки тысяч символов) многократный полный скан через
// FindSymbols держал p50 get_context_for_task на 1.6с против бюджета 1с —
// измерено на ut_demo, честно, не гипотеза. Равенство с индексом снимает
// это на порядок.
func (tx *ReadTx) SymbolsByNameNormExact(nameNorm string) ([]SymbolRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT `+symbolSelectColumns+` FROM `+symbolFromJoin+` WHERE s.name_norm=?`, nameNorm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolRow
	for rows.Next() {
		r, err := scanSymbolRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MetadataObjectsByNameNormAnyType ищет объект метаданных по нормализованному
// имени БЕЗ фильтра по виду (mtype) — свободный текст задачи agenta называет
// имя объекта («ТоварыНаСкладах»), но не его MType (InformationRegister?
// AccumulationRegister? Document?). Возвращает все совпадения по всем видам и
// компонентам, выбор среди них — дело вызывающего (тот же принцип, что
// MetadataObjectsByName).
func (tx *ReadTx) MetadataObjectsByNameNormAnyType(nameNorm string) ([]MetadataObjectRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,component_id,uuid,mtype,name_norm,name_display,synonym,file_id,props,layer
		FROM metadata_object WHERE name_norm=? ORDER BY mtype, component_id`, nameNorm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetadataObjectRows(rows)
}

// SearchMetadataObjects ищет объекты метаданных по подстроке нормализованного
// имени ИЛИ синонима (регистронезависимо, synonym хранится как введено в
// XML — сравнение через LOWER) — приближение стадии «нормализация и синонимы
// метаданных» §24: словаря синонимов в v1 нет, но синоним самого объекта
// (Synonym в Rights.xml/MetaDataObject) уже несёт то же самое человекочитаемое
// имя, которым задачу мог описать агент («документ отгрузки» -> synonym
// «Отгрузка товаров»). limit<=0 -> 20.
func (tx *ReadTx) SearchMetadataObjects(substrNorm string, limit int) ([]MetadataObjectRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	like := "%" + substrNorm + "%"
	rows, err := tx.c.query(tx.ctx, `SELECT id,component_id,uuid,mtype,name_norm,name_display,synonym,file_id,props,layer
		FROM metadata_object WHERE name_norm LIKE ? OR LOWER(synonym) LIKE ?
		ORDER BY mtype, component_id LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetadataObjectRows(rows)
}

func scanMetadataObjectRows(rows *sql.Rows) ([]MetadataObjectRow, error) {
	var out []MetadataObjectRow
	for rows.Next() {
		var r MetadataObjectRow
		var uuid, synonym, props sql.NullString
		if err := rows.Scan(&r.ID, &r.ComponentID, &uuid, &r.MType, &r.NameNorm, &r.NameDisplay,
			&synonym, &r.FileID, &props, &r.Layer); err != nil {
			return nil, err
		}
		r.UUID, r.Synonym, r.PropsJSON = uuid.String, synonym.String, props.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// SearchSymbolsFTS ищет символы по fts_symbols (name, signature, doc —
// tokenize='unicode61', rowid = symbol.id, §schema раздел 15) — стадия «FTS
// по именам/докам» §24, третья после точного lookup и подстрочного/
// синонимного совпадения. terms — уже токенизированные слова задачи (не
// сырой текст с пунктуацией: FTS5 MATCH синтаксис толкует часть символов
// как операторы). Каждый термин оборачивается в кавычки как phrase-запрос —
// это делает MATCH нечувствительным к FTS5-операторам ВНУТРИ самого слова
// (не может случиться при токенизации identifier-подобных слов, но защищает
// от синтаксической ошибки, если задача содержит операторные символы) и
// связывается через OR — «любое из слов задачи упомянуто в имени/сигнатуре/
// доке символа». Пустой terms или ошибка синтаксиса MATCH -> nil, не ошибка:
// FTS — лучшее усилие, а не обязательный источник anchors.
func (tx *ReadTx) SearchSymbolsFTS(terms []string, limit int) ([]int64, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	q := ftsMatchQuery(terms)
	if q == "" {
		return nil, nil
	}
	rows, err := tx.c.query(tx.ctx,
		`SELECT rowid FROM fts_symbols WHERE fts_symbols MATCH ? ORDER BY rank LIMIT ?`, q, limit)
	if err != nil {
		// Синтаксически некорректный MATCH (термин целиком состоит из
		// FTS5-пунктуации после экранирования и всё равно не разбирается) —
		// FTS честно отдаёт «ничего не нашли», не валит весь вызов.
		return nil, nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ftsMatchQuery строит "col1" OR "col2" OR ... из terms, экранируя кавычки
// внутри каждого термина удвоением (FTS5 quoting rule), отбрасывая пустые.
func ftsMatchQuery(terms []string) string {
	var parts []string
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " OR ")
}

// FormElementRow — один элемент структуры формы (form_element).
type FormElementRow struct {
	ID           int64
	FormID       int64
	OriginFileID int64
	NameNorm     string
	NameDisplay  string
	EType        string
	DataPath     string
}

// FormElementsByForm перечисляет элементы формы — нужно сценарию «поменяй
// обработчик ПриИзменении поля X на форме Y» (§25 №4): элемент даёт
// DataPath, которым get_context_for_task находит затронутый реквизит объекта.
func (tx *ReadTx) FormElementsByForm(formID int64) ([]FormElementRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,form_id,origin_file_id,name_norm,name_display,etype,data_path
		FROM form_element WHERE form_id=? ORDER BY name_norm`, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FormElementRow
	for rows.Next() {
		var r FormElementRow
		var etype, dataPath sql.NullString
		if err := rows.Scan(&r.ID, &r.FormID, &r.OriginFileID, &r.NameNorm, &r.NameDisplay, &etype, &dataPath); err != nil {
			return nil, err
		}
		r.EType, r.DataPath = etype.String, dataPath.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueriesBySymbolID перечисляет тексты запросов, лежащие внутри символа —
// нужно и bugfix-сценарию («запросы внутри тела с их схемами», §25 №1), и
// query-intent (owner_symbol).
func (tx *ReadTx) QueriesBySymbolID(symbolID int64) ([]QueryRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT id,symbol_id,file_id,byte_start,byte_end,staticity,text,confidence
		FROM query WHERE symbol_id=? ORDER BY byte_start`, symbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryRow
	for rows.Next() {
		var r QueryRow
		if err := rows.Scan(&r.ID, &r.SymbolID, &r.FileID, &r.Span.StartByte, &r.Span.EndByte,
			&r.Staticity, &r.Text, &r.Confidence); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RegisterAccessesBySymbol перечисляет ВСЕ доступы к регистрам одного
// символа-владельца, любого регистра и режима — обратный разрез
// register_access к RegisterAccessFilter (readregister.go), который
// требует конкретное имя регистра (без него это был бы «любой режим у любого
// регистра», а не «всё, что делает этот символ»). Нужен posting-intent
// (§Конкретика: «обработчик + движения + register_access + подписки»).
func (tx *ReadTx) RegisterAccessesBySymbol(symbolID int64) ([]RegisterAccessRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT ra.id, ra.file_id, sf.component_id, ra.symbol_id, ra.object_id,
		ra.register_name_norm, ra.mode, ra.in_transaction, ra.static, ra.confidence, ra.byte_start,
		ra.byte_end, ra.layer
		FROM register_access ra JOIN source_file sf ON sf.id = ra.file_id
		WHERE ra.symbol_id = ? ORDER BY ra.id`, symbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegisterAccessRow
	for rows.Next() {
		var r RegisterAccessRow
		var symbolID2, objectID sql.NullInt64
		var inTx sql.NullInt64
		var static int64
		if err := rows.Scan(&r.ID, &r.FileID, &r.ComponentID, &symbolID2, &objectID, &r.RegisterNameNorm,
			&r.Mode, &inTx, &static, &r.Confidence, &r.Span.StartByte, &r.Span.EndByte, &r.Layer); err != nil {
			return nil, err
		}
		r.SymbolID, r.ObjectID, r.Static = symbolID2.Int64, objectID.Int64, static != 0
		if inTx.Valid {
			b := inTx.Int64 != 0
			r.InTransaction = &b
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QueryReferencesByQueryID перечисляет ВСЕ таблицы/поля/параметры/ВТ ОДНОГО
// текста запроса (query.id): «схема» query-intent («используемые
// таблицы и поля»). QueryReferenceFilter (readquery.go) не несёт
// query_id — там фильтр строился под find_queries_using (по object/field, а
// не по конкретному запросу), эта функция — обратный разрез той же таблицы.
func (tx *ReadTx) QueryReferencesByQueryID(queryID int64) ([]QueryReferenceRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx,
		`SELECT id,query_id,kind,name_norm,object_id,member_id,span_start,span_end
		 FROM query_reference WHERE query_id=? ORDER BY span_start`, queryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryReferenceRow
	for rows.Next() {
		var r QueryReferenceRow
		var objectID, memberID, spanStart, spanEnd sql.NullInt64
		if err := rows.Scan(&r.ID, &r.QueryID, &r.Kind, &r.NameNorm, &objectID, &memberID, &spanStart, &spanEnd); err != nil {
			return nil, err
		}
		r.ObjectID, r.MemberID, r.SpanStart, r.SpanEnd = objectID.Int64, memberID.Int64, spanStart.Int64, spanEnd.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}
