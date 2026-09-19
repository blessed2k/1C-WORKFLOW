package store

import (
	"fmt"
	"strings"
)

// Входящие мягкие указатели (ADR-037).
//
// Переопубликование файла удаляет его строки-узлы (symbol, metadata_object,
// metadata_member) и вставляет заново. Id у них прежний: строка-узел делит
// первичный ключ с node, а node (стабильная логическая identity) переживает
// файл (§14, раздел 15). Строки ДРУГИХ файлов, которые на эти узлы указывали,
// о пересоздании не знают: шаг (1b) переводит их ссылки в unresolved, а ON
// DELETE SET NULL обнуляет остальные указатели. Факты этих файлов никто не
// переопубликовывает (их разрешение не изменилось), и индекс оставался с
// оборванными связями до полной пересборки (issue #10).
//
// ReplaceSourceFiles закрывает весь порядок одним методом: снять указатели,
// удалить файлы, вставить новые факты, вернуть указатели узлам, которые снова
// существуют с тем же id. Не существуют (символ удалён или переименован):
// указатель остаётся пустым, а ссылка unresolved. Её переразрешение делает
// дельта имён §18.4, и файл-владелец ссылки тогда сам в переопубликовании.

// Виды строк-узлов, которые удаление файла пересоздаёт.
const (
	nodeSymbol = "symbol"
	nodeObject = "metadata_object"
	nodeMember = "metadata_member"
)

// staleFiles: подзапрос множества удаляемых файлов по JSON-параметру ?1.
const staleFiles = `(SELECT value FROM json_each(?1))`

// staleNodes: строки-узлы, которые удаление файлов staleFiles снесёт (каскадом
// по file_id или по владельцу-объекту). Единственное определение: им пользуются
// и шаг (1b) в DeleteSourceFiles, и снятие входящих указателей. Модуль файлу не
// принадлежит (§15: identity без file_id), поэтому символы уходят только по
// origin_file_id.
var staleNodes = map[string]string{
	nodeSymbol: `(SELECT id FROM symbol WHERE origin_file_id IN ` + staleFiles + `)`,
	nodeObject: `(SELECT id FROM metadata_object WHERE file_id IN ` + staleFiles + `)`,
	nodeMember: `(SELECT id FROM metadata_member WHERE origin_file_id IN ` + staleFiles +
		` OR object_id IN (SELECT id FROM metadata_object WHERE file_id IN ` + staleFiles + `))`,
}

// inboundKind: один вид мягкого указателя, столбец строки, живущей в файле, на
// строку-узел, принадлежащую (возможно) другому файлу.
type inboundKind struct {
	table, column string
	target        string // nodeSymbol | nodeObject | nodeMember
	// owner: выражение файла-владельца строки (строка таблицы видна как t).
	owner string
	// targetClass: только у reference. Указатель там входит в XOR-автомат, и
	// вместе с целью возвращаются resolution и target_class.
	targetClass string
}

func (k inboundKind) name() string { return k.table + "." + k.column }

// inboundKinds: ВСЕ столбцы схемы с ON DELETE SET NULL на symbol,
// metadata_object и metadata_member. Новый такой столбец обязан попасть сюда,
// иначе правка файла-цели снова молча оборвёт его (закреплено
// TestInboundKindsCoverSetNullColumns).
var inboundKinds = []inboundKind{
	{table: "reference", column: "target_symbol_id", target: nodeSymbol, owner: "t.file_id", targetClass: "symbol"},
	{table: "reference", column: "target_object_id", target: nodeObject, owner: "t.file_id", targetClass: "metadata"},
	{table: "call_edge", column: "callee_id", target: nodeSymbol,
		owner: "(SELECT r.file_id FROM reference r WHERE r.id = t.ref_id)"},
	{table: "handler_binding", column: "handler_symbol_id", target: nodeSymbol, owner: "t.origin_file_id"},
	{table: "event_subscription", column: "handler_symbol_id", target: nodeSymbol, owner: "t.origin_file_id"},
	{table: "scheduled_job", column: "handler_symbol_id", target: nodeSymbol, owner: "t.origin_file_id"},
	{table: "register_access", column: "object_id", target: nodeObject, owner: "t.file_id"},
	{table: "query_reference", column: "object_id", target: nodeObject,
		owner: "(SELECT q.file_id FROM query q WHERE q.id = t.query_id)"},
	{table: "query_reference", column: "member_id", target: nodeMember,
		owner: "(SELECT q.file_id FROM query q WHERE q.id = t.query_id)"},
	{table: "role", column: "object_id", target: nodeObject, owner: "t.file_id"},
	{table: "role_right", column: "object_id", target: nodeObject, owner: "t.origin_file_id"},
}

// snapshotSQL снимает указатели вида k во временную таблицу: строки, которые
// указывают на удаляемые узлы и сами удалению не подлежат. Строки удаляемых
// файлов не снимаются: их id может занять новая строка той же транзакции.
func (k inboundKind) snapshotSQL(kind int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `INSERT INTO temp.inbound_ptr(kind, row_id, target) SELECT %d, t.id, t.%s FROM %s t WHERE t.%s IN %s AND %s NOT IN %s`,
		kind, k.column, k.table, k.column, staleNodes[k.target], k.owner, staleFiles)
	if k.targetClass != "" {
		b.WriteString(` AND t.resolution='resolved'`)
	}
	return b.String()
}

// restoreSQL возвращает указатели вида k одним UPDATE: только пустые и только
// тем, чей узел снова существует с тем же id.
func (k inboundKind) restoreSQL(kind int) string {
	set := k.column + " = p.target"
	guard := ""
	if k.targetClass != "" {
		set = fmt.Sprintf("resolution = 'resolved', target_class = '%s', %s", k.targetClass, set)
		guard = fmt.Sprintf(" AND %s.resolution = 'unresolved' AND %s.target_class IS NULL", k.table, k.table)
	}
	return fmt.Sprintf(`UPDATE %s SET %s FROM temp.inbound_ptr p
		WHERE p.kind = %d AND %s.id = p.row_id AND %s.%s IS NULL%s
		  AND EXISTS(SELECT 1 FROM %s n WHERE n.id = p.target)`,
		k.table, set, kind, k.table, k.table, k.column, guard, k.target)
}

// ReplaceSourceFiles переопубликует файлы fileIDs: удаляет их вместе с фактами
// (DeleteSourceFiles), зовёт insert, который вставляет новые строки-узлы, и
// возвращает указатели строк ДРУГИХ файлов на узлы, пережившие правку.
//
// insert обязан вставить все строки-узлы переопубликуемых файлов (проход 1
// публикации): узел, вставленный позже, указатель уже не получит. Ссылки и
// производные факты прохода 2 вставляются после возврата и видят
// согласованную базу.
//
// Снимок живёт во временной таблице соединения писателя, а не в памяти Go:
// потолок и оценка у createInboundTable.
func (tx *WriteTx) ReplaceSourceFiles(fileIDs []int64, insert func() error) error {
	if len(fileIDs) == 0 {
		return insert()
	}
	if err := tx.check(); err != nil {
		return err
	}
	if err := tx.c.exec(tx.ctx, createInboundTable); err != nil {
		return fmt.Errorf("таблица входящих указателей: %w", err)
	}
	if err := tx.c.exec(tx.ctx, `DELETE FROM temp.inbound_ptr`); err != nil {
		return err
	}
	files := int64ListJSON(fileIDs)
	for i, k := range inboundKinds {
		if err := tx.c.exec(tx.ctx, k.snapshotSQL(i), files); err != nil {
			return fmt.Errorf("входящие указатели %s: %w", k.name(), err)
		}
	}
	if err := tx.saveCascades(files); err != nil {
		return err
	}
	if err := tx.DeleteSourceFiles(fileIDs...); err != nil {
		return err
	}
	if err := insert(); err != nil {
		return err
	}
	if err := tx.check(); err != nil {
		return err
	}
	// Каскадные строки возвращаются раньше указателей: у них прежние id, и
	// возврат указателя по row_id находит уже вернувшуюся строку.
	if err := tx.restoreCascades(); err != nil {
		return err
	}
	for i, k := range inboundKinds {
		if err := tx.c.exec(tx.ctx, k.restoreSQL(i)); err != nil {
			return fmt.Errorf("возврат указателей %s: %w", k.name(), err)
		}
	}
	return tx.c.exec(tx.ctx, `DELETE FROM temp.inbound_ptr`)
}

// createInboundTable: временная таблица снимка, своя у соединения писателя
// (temp_store=MEMORY).
//
// упрощение: потолок снимка не ограничен, он равен числу указателей из
// нетронутых файлов на узлы переопубликуемых. Худший случай на ut_demo (все XML
// объектов в одном инкременте): role_right, query_reference и register_access
// целиком, 475 362 строки, около 16 МиБ памяти SQLite (около 34 байт на
// строку). Все мягкие указатели индекса (около 1.6 млн) дали бы порядка 55 МиБ.
// Такой объём правок уходит в фоновую полную пересборку (§18.5), где снимать
// нечего. Путь выше, если понадобится: temp_store=FILE или снимок по частям.
const createInboundTable = `CREATE TEMP TABLE IF NOT EXISTS inbound_ptr(
	kind INTEGER NOT NULL, row_id INTEGER NOT NULL, target INTEGER NOT NULL)`
