package store

import (
	"database/sql"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// DiagnosticRow — одна строка diagnostic с восстановленным rel_path файла
// (LEFT JOIN source_file: file_id nullable — диагностика может быть не
// привязана ни к одному файлу, напр. манифеста).
type DiagnosticRow struct {
	ComponentID string
	Severity    string
	Code        string
	Message     string
	File        string // rel_path; пусто, если file_id IS NULL
	Span        domain.Span
}

// Diagnostics читает диагностики ТЕКУЩЕЙ эпохи, упорядоченно по id (порядок
// вставки — детерминированный, republish файла удаляет его старые диагностики
// раньше вставки новых, см. publishFiles/DeleteSourceFiles). Отдельного
// фильтра по generation/epoch здесь нет и не нужен: store хранит одну эпоху
// на файл SQLite (epoch.go), и ReadTx открывается строго над файлом ТЕКУЩЕЙ
// опубликованной эпохи — предыдущие эпохи физически удаляются сразу после
// публикации следующей (store.go, buildEpoch/removeEpoch), так что этой
// таблице просто неоткуда взять строки устаревшей эпохи.
// limit — верхняя граница выборки (упрощение: без keyset-пагинации). Целиком
// этот список наружу больше НЕ уходит: index_status/reindex дозируют его
// (app.doseDiagnostics, diagnosticsSample = 10) и кладут рядом
// diagnosticsDigest с полным счётом по кодам; весь список отдаётся только по
// явному includeAllDiagnostics. То есть limit здесь — потолок ЧТЕНИЯ, а не
// размер ответа инструмента.
func (tx *ReadTx) Diagnostics(limit int) ([]DiagnosticRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT d.component_id, d.severity, d.code, d.message, sf.rel_path,
		d.byte_start, d.byte_end, d.start_line, d.start_col
		FROM diagnostic d LEFT JOIN source_file sf ON sf.id = d.file_id
		ORDER BY d.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiagnosticRow
	for rows.Next() {
		var r DiagnosticRow
		var relPath sql.NullString
		var byteStart, byteEnd, startLine, startCol sql.NullInt64
		if err := rows.Scan(&r.ComponentID, &r.Severity, &r.Code, &r.Message, &relPath,
			&byteStart, &byteEnd, &startLine, &startCol); err != nil {
			return nil, err
		}
		r.File = relPath.String
		r.Span = domain.Span{
			StartByte: int(byteStart.Int64), EndByte: int(byteEnd.Int64),
			StartLine: int(startLine.Int64), StartCol: int(startCol.Int64),
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
