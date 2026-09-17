package store

import (
	"database/sql"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Файл — типизированная выборка register_access для find_register_writes
// (таск 12). SQL живёт только здесь (RuleSQLOnlyInStore).

// RegisterAccessRow — один доступ к регистру, с компонентом, в котором лежит
// файл (join source_file: register_access своей колонки component_id не
// несёт, раздел 15).
type RegisterAccessRow struct {
	ID               int64
	FileID           int64
	ComponentID      string
	SymbolID         int64
	ObjectID         int64
	RegisterNameNorm string
	Mode             string
	InTransaction    *bool
	Static           bool
	Confidence       float64
	Span             domain.Span
	Layer            string
}

// RegisterAccessFilter — фильтр find_register_writes: RegisterNameNorm
// обязателен (без него это был бы полный скан таблицы на реальной выгрузке),
// остальные поля — «любой», когда пусты/нулевые.
type RegisterAccessFilter struct {
	RegisterNameNorm string
	Modes            []string // пусто — любой режим
	ComponentID      string
	SymbolID         int64
	MinConfidence    float64
	AfterID          int64
	Limit            int
}

// RegisterAccesses отдаёт строки register_access по фильтру, не больше
// Limit+1 (та же пагинационная идиома, что QueryReferences).
func (tx *ReadTx) RegisterAccesses(f RegisterAccessFilter) ([]RegisterAccessRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	q := `SELECT ra.id, ra.file_id, sf.component_id, ra.symbol_id, ra.object_id, ra.register_name_norm,
		ra.mode, ra.in_transaction, ra.static, ra.confidence, ra.byte_start, ra.byte_end, ra.layer
		FROM register_access ra JOIN source_file sf ON sf.id = ra.file_id
		WHERE ra.id > ? AND ra.register_name_norm = ?`
	args := []any{f.AfterID, f.RegisterNameNorm}
	if len(f.Modes) > 0 {
		q += ` AND ra.mode IN ` + inClause(len(f.Modes))
		for _, m := range f.Modes {
			args = append(args, m)
		}
	}
	if f.ComponentID != "" {
		q += ` AND sf.component_id = ?`
		args = append(args, f.ComponentID)
	}
	if f.SymbolID != 0 {
		q += ` AND ra.symbol_id = ?`
		args = append(args, f.SymbolID)
	}
	if f.MinConfidence > 0 {
		q += ` AND ra.confidence >= ?`
		args = append(args, f.MinConfidence)
	}
	q += ` ORDER BY ra.id`
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
	var out []RegisterAccessRow
	for rows.Next() {
		var r RegisterAccessRow
		var symbolID, objectID sql.NullInt64
		var inTx sql.NullInt64
		var static int64
		if err := rows.Scan(&r.ID, &r.FileID, &r.ComponentID, &symbolID, &objectID, &r.RegisterNameNorm,
			&r.Mode, &inTx, &static, &r.Confidence, &r.Span.StartByte, &r.Span.EndByte, &r.Layer); err != nil {
			return nil, err
		}
		r.SymbolID, r.ObjectID, r.Static = symbolID.Int64, objectID.Int64, static != 0
		if inTx.Valid {
			b := inTx.Int64 != 0
			r.InTransaction = &b
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
