package store

import (
	"database/sql"
	"errors"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Файл: типизированные выборки query/query_reference для find_queries_using.
// SQL живёт только здесь (RuleSQLOnlyInStore).

// QueryRow — одна строка query (текст, span В ФАЙЛЕ, staticity, confidence).
type QueryRow struct {
	ID         int64
	SymbolID   int64
	FileID     int64
	Span       domain.Span
	Staticity  string
	Text       string
	Confidence float64
}

// QueryByID читает один текст запроса по его node.id.
func (tx *ReadTx) QueryByID(id int64) (QueryRow, bool, error) {
	if err := tx.check(); err != nil {
		return QueryRow{}, false, err
	}
	var r QueryRow
	err := tx.c.sc.QueryRowContext(tx.ctx, `SELECT id,symbol_id,file_id,byte_start,byte_end,staticity,text,confidence
		FROM query WHERE id=?`, id).
		Scan(&r.ID, &r.SymbolID, &r.FileID, &r.Span.StartByte, &r.Span.EndByte, &r.Staticity, &r.Text, &r.Confidence)
	if errors.Is(err, sql.ErrNoRows) {
		return QueryRow{}, false, nil
	}
	return r, err == nil, err
}

// QueryReferenceRow — одна строка query_reference: таблица/поле/параметр/ВТ,
// использованные текстом запроса.
type QueryReferenceRow struct {
	ID        int64
	QueryID   int64
	Kind      string
	NameNorm  string
	ObjectID  int64
	MemberID  int64
	SpanStart int64
	SpanEnd   int64
}

// QueryReferenceFilter — фильтр find_queries_using: ищет по объекту (ObjectID)
// ИЛИ по имени поля/таблицы (NameNorm, работает и для unresolved:
// staticity=dynamic/partial тексты не резолвятся, но имя в query_reference
// остаётся, если факт вообще опубликован). Нулевые/пустые поля — «любой».
type QueryReferenceFilter struct {
	ObjectID int64
	MemberID int64
	NameNorm string
	Kind     string
	AfterID  int64
	Limit    int
}

// QueryReferences отдаёт строки query_reference по фильтру, отсортированные
// по id (стабильный порядок курсора), не больше Limit+1 (последняя лишняя —
// сигнал, что страница не последняя; убирается вызывающим).
func (tx *ReadTx) QueryReferences(f QueryReferenceFilter) ([]QueryReferenceRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	q := `SELECT id,query_id,kind,name_norm,object_id,member_id,span_start,span_end FROM query_reference WHERE id > ?`
	args := []any{f.AfterID}
	if f.ObjectID != 0 {
		q += ` AND object_id=?`
		args = append(args, f.ObjectID)
	}
	if f.MemberID != 0 {
		q += ` AND member_id=?`
		args = append(args, f.MemberID)
	}
	if f.NameNorm != "" {
		q += ` AND name_norm=?`
		args = append(args, f.NameNorm)
	}
	if f.Kind != "" {
		q += ` AND kind=?`
		args = append(args, f.Kind)
	}
	q += ` ORDER BY id`
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` LIMIT ?`
	args = append(args, limit+1)

	rows, err := tx.c.query(tx.ctx, q, args...)
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
