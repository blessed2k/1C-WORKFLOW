package store

import "fmt"

// Входящие мягкие указатели (ADR-037).
//
// Переопубликование файла удаляет его строки-узлы (symbol, metadata_object,
// metadata_member) и вставляет заново. Id у них прежний: строка-узел делит
// первичный ключ с node, а node — стабильная логическая identity, которая
// переживает файл (§14, раздел 15). Но строки ДРУГИХ файлов, которые на эти
// узлы указывали, о пересоздании не знают: шаг (1b) переводит их ссылки в
// unresolved, а ON DELETE SET NULL обнуляет остальные указатели. Факты этих
// файлов никто не переопубликовывает (их разрешение не изменилось), и индекс
// оставался с оборванными связями до полной пересборки (issue #10).
//
// Пара InboundPointers / RestoreInboundPointers снимает такие указатели ДО
// удаления файлов и возвращает их ПОСЛЕ вставки новых строк-узлов, если узел
// с тем же id снова существует, то есть identity пережила правку. Не пережила
// (символ удалён или переименован) — указатель остаётся пустым, а ссылка
// unresolved: её переразрешение — дело дельты имён §18.4, и файл-владелец
// тогда сам попадает в переопубликование.

// inboundKind — один вид мягкого указателя: столбец строки, живущей в файле,
// на строку-узел, принадлежащую (возможно) другому файлу.
type inboundKind struct {
	name string
	// selectSQL выбирает (id строки, значение указателя) для строк, которые
	// указывают на удаляемые узлы и сами удалению не подлежат. Параметры: ?1 —
	// JSON-массив удаляемых file_id (один и тот же для обоих вхождений).
	selectSQL string
	// restoreSQL возвращает указатель: ?1 — значение, ?2 — id строки. Строка
	// меняется, только если указатель пуст и узел-цель снова существует.
	restoreSQL string
}

// staleFiles — подзапрос множества удаляемых файлов по JSON-параметру ?1.
const staleFiles = `(SELECT value FROM json_each(?1))`

// Множества узлов, которые удаление файлов снесёт (каскадом по file_id или по
// владельцу-объекту). Модуль файлу не принадлежит (§15: identity без file_id),
// поэтому символы уходят только по origin_file_id.
const (
	staleSymbols = `(SELECT id FROM symbol WHERE origin_file_id IN ` + staleFiles + `)`
	staleObjects = `(SELECT id FROM metadata_object WHERE file_id IN ` + staleFiles + `)`
	staleMembers = `(SELECT id FROM metadata_member WHERE origin_file_id IN ` + staleFiles +
		` OR object_id IN ` + staleObjects + `)`
)

// inboundKinds — ВСЕ столбцы схемы с ON DELETE SET NULL на symbol,
// metadata_object и metadata_member. Новый такой столбец обязан попасть сюда,
// иначе правка файла-цели снова молча оборвёт его (закреплено
// TestInboundKindsCoverSetNullColumns).
var inboundKinds = []inboundKind{
	{
		name: "reference.target_symbol_id",
		selectSQL: `SELECT id, target_symbol_id FROM reference
			WHERE resolution='resolved' AND target_symbol_id IN ` + staleSymbols + `
			  AND file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE reference SET resolution='resolved', target_class='symbol', target_symbol_id=?1
			WHERE id=?2 AND resolution='unresolved' AND target_class IS NULL
			  AND EXISTS(SELECT 1 FROM symbol WHERE id=?1)`,
	},
	{
		name: "reference.target_object_id",
		selectSQL: `SELECT id, target_object_id FROM reference
			WHERE resolution='resolved' AND target_object_id IN ` + staleObjects + `
			  AND file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE reference SET resolution='resolved', target_class='metadata', target_object_id=?1
			WHERE id=?2 AND resolution='unresolved' AND target_class IS NULL
			  AND EXISTS(SELECT 1 FROM metadata_object WHERE id=?1)`,
	},
	{
		name: "call_edge.callee_id",
		selectSQL: `SELECT c.id, c.callee_id FROM call_edge c JOIN reference r ON r.id = c.ref_id
			WHERE c.callee_id IN ` + staleSymbols + ` AND r.file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE call_edge SET callee_id=?1
			WHERE id=?2 AND callee_id IS NULL AND EXISTS(SELECT 1 FROM symbol WHERE id=?1)`,
	},
	{
		name: "handler_binding.handler_symbol_id",
		selectSQL: `SELECT id, handler_symbol_id FROM handler_binding
			WHERE handler_symbol_id IN ` + staleSymbols + ` AND origin_file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE handler_binding SET handler_symbol_id=?1
			WHERE id=?2 AND handler_symbol_id IS NULL AND EXISTS(SELECT 1 FROM symbol WHERE id=?1)`,
	},
	{
		name: "event_subscription.handler_symbol_id",
		selectSQL: `SELECT id, handler_symbol_id FROM event_subscription
			WHERE handler_symbol_id IN ` + staleSymbols + ` AND origin_file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE event_subscription SET handler_symbol_id=?1
			WHERE id=?2 AND handler_symbol_id IS NULL AND EXISTS(SELECT 1 FROM symbol WHERE id=?1)`,
	},
	{
		name: "scheduled_job.handler_symbol_id",
		selectSQL: `SELECT id, handler_symbol_id FROM scheduled_job
			WHERE handler_symbol_id IN ` + staleSymbols + ` AND origin_file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE scheduled_job SET handler_symbol_id=?1
			WHERE id=?2 AND handler_symbol_id IS NULL AND EXISTS(SELECT 1 FROM symbol WHERE id=?1)`,
	},
	{
		name: "register_access.object_id",
		selectSQL: `SELECT id, object_id FROM register_access
			WHERE object_id IN ` + staleObjects + ` AND file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE register_access SET object_id=?1
			WHERE id=?2 AND object_id IS NULL AND EXISTS(SELECT 1 FROM metadata_object WHERE id=?1)`,
	},
	{
		name: "query_reference.object_id",
		selectSQL: `SELECT qr.id, qr.object_id FROM query_reference qr JOIN query q ON q.id = qr.query_id
			WHERE qr.object_id IN ` + staleObjects + ` AND q.file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE query_reference SET object_id=?1
			WHERE id=?2 AND object_id IS NULL AND EXISTS(SELECT 1 FROM metadata_object WHERE id=?1)`,
	},
	{
		name: "query_reference.member_id",
		selectSQL: `SELECT qr.id, qr.member_id FROM query_reference qr JOIN query q ON q.id = qr.query_id
			WHERE qr.member_id IN ` + staleMembers + ` AND q.file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE query_reference SET member_id=?1
			WHERE id=?2 AND member_id IS NULL AND EXISTS(SELECT 1 FROM metadata_member WHERE id=?1)`,
	},
	{
		name: "role.object_id",
		selectSQL: `SELECT id, object_id FROM role
			WHERE object_id IN ` + staleObjects + ` AND file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE role SET object_id=?1
			WHERE id=?2 AND object_id IS NULL AND EXISTS(SELECT 1 FROM metadata_object WHERE id=?1)`,
	},
	{
		name: "role_right.object_id",
		selectSQL: `SELECT id, object_id FROM role_right
			WHERE object_id IN ` + staleObjects + ` AND origin_file_id NOT IN ` + staleFiles,
		restoreSQL: `UPDATE role_right SET object_id=?1
			WHERE id=?2 AND object_id IS NULL AND EXISTS(SELECT 1 FROM metadata_object WHERE id=?1)`,
	},
}

// InboundPointer — снятый до удаления файлов мягкий указатель строки, которая
// удаление переживает, на узел удаляемого файла. Поля закрыты: смысл значения
// знает только пара InboundPointers / RestoreInboundPointers.
type InboundPointer struct {
	kind   int
	rowID  int64
	target int64
}

// InboundPointers снимает мягкие указатели на узлы файлов fileIDs из строк
// ДРУГИХ файлов. Вызывать строго до DeleteSourceFiles тех же файлов: после
// удаления указатели уже обнулены. Строки самих fileIDs не снимаются: они
// удаляются, и их id может занять новая строка той же транзакции.
func (tx *WriteTx) InboundPointers(fileIDs ...int64) ([]InboundPointer, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if len(fileIDs) == 0 {
		return nil, nil
	}
	files := int64ListJSON(fileIDs)
	var out []InboundPointer
	for k, spec := range inboundKinds {
		rows, err := tx.c.query(tx.ctx, spec.selectSQL, files)
		if err != nil {
			return nil, fmt.Errorf("входящие указатели %s: %w", spec.name, err)
		}
		for rows.Next() {
			p := InboundPointer{kind: k}
			if err := rows.Scan(&p.rowID, &p.target); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RestoreInboundPointers возвращает указатели, снятые InboundPointers, на узлы,
// которые после вставки новых фактов снова существуют с тем же id. Вызывать
// после того, как вставлены строки-узлы всех переопубликованных файлов (проход
// 1 публикации). Возвращает число восстановленных указателей.
func (tx *WriteTx) RestoreInboundPointers(ptrs []InboundPointer) (int, error) {
	if err := tx.check(); err != nil {
		return 0, err
	}
	restored := 0
	for _, p := range ptrs {
		res, err := tx.c.execResult(tx.ctx, inboundKinds[p.kind].restoreSQL, p.target, p.rowID)
		if err != nil {
			return restored, fmt.Errorf("восстановление указателя %s: %w", inboundKinds[p.kind].name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return restored, err
		}
		restored += int(n)
	}
	return restored, nil
}
