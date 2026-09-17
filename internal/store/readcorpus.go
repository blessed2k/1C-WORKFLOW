package store

// Файл — выборка source_file компонента целиком: то, из чего internal/index
// восстанавливает резидентный корпус при первом обращении к компоненту после
// перезапуска процесса (ADR-028). Разобранных фактов store не отдаёт и не
// хранит (D02) — только фингерпринт файла, которого хватает precheck-у
// свежести и отбору toRead.

// SourceFileState — строка source_file в объёме, нужном гидратации корпуса:
// путь плюс фингерпринт (size/mtime_ns/content_hash) плюс версия парсера,
// которым файл был разобран.
type SourceFileState struct {
	RelPath       string
	Size          int64
	MtimeNS       int64
	ContentHash   string
	ParserVersion int
}

// SourceFilesByComponent отдаёт все файлы компонента активной эпохи,
// отсортированные по rel_path (детерминизм входа, §18.7). Неизвестный
// компонент — пустой срез без ошибки: индекс просто ещё не видел его.
func (tx *ReadTx) SourceFilesByComponent(componentID string) ([]SourceFileState, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT rel_path,size,mtime_ns,content_hash,parser_version
		FROM source_file WHERE component_id=? ORDER BY rel_path`, componentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceFileState
	for rows.Next() {
		var r SourceFileState
		if err := rows.Scan(&r.RelPath, &r.Size, &r.MtimeNS, &r.ContentHash, &r.ParserVersion); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
