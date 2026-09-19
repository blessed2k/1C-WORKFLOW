package store

import "fmt"

// Каскадные строки нетронутых файлов (ADR-038).
//
// Переопубликование файла удаляет его строки-владельцы (metadata_object, role)
// и вставляет заново. У объекта id прежний (node переживает файл, §14), у роли
// новый (роль не node, её identity: имя внутри компонента). Строки ДРУГИХ
// файлов, которые держатся за владельца не мягким указателем, а ON DELETE
// CASCADE, уходят вместе с ним, и возвращать их некому: их файлы не
// переопубликуются. Так пропадали права из нетронутого Rights.xml после
// правки XML роли и рёбра объектного графа после правки XML регистра или
// документа (issue #11). ADR-037 закрыл тот же класс для SET NULL.
//
// Решение то же, что у входящих указателей: снимок до удаления, возврат после
// прохода 1. Строка возвращается как была, с прежним id, если её владелец
// снова существует под той же identity. Не существует (объект или роль
// удалены, переименованы): строка не возвращается, как её не дала бы и чистая
// пересборка.

// staleRoles: роли, которые удаление файлов staleFiles снесёт каскадом по
// role.file_id.
const staleRoles = `(SELECT id FROM role WHERE file_id IN ` + staleFiles + `)`

// cascadeKind: одна таблица, строки которой уходят каскадом от
// строки-владельца удаляемого файла, хотя сами принадлежат другому файлу (или
// никакому). covers: столбцы-FK с ON DELETE CASCADE, которые вид закрывает
// (сверяет TestCascadeKindsCoverCrossFileCascades).
type cascadeKind struct {
	table  string
	covers []string
	// snapshot: SELECT строк, которые каскад снесёт и которые сами удалению не
	// подлежат; параметр ?1: JSON-список удаляемых файлов.
	snapshot string
	// restore: операторы возврата из temp.<saved()>, по порядку.
	restore []string
}

func (k cascadeKind) saved() string { return "cascade_" + k.table }

// cascadeKinds: таблицы, чьи строки держатся за строку-владельца чужого файла
// каскадом. Порядок значим: object_data_edge_dep возвращается после своего
// ребра. Новый такой столбец схемы обязан попасть сюда или в
// sameFileCascades (TestCascadeKindsCoverCrossFileCascades).
//
// Id строк возвращаются прежними: удалила их эта же транзакция, а проход 1,
// который идёт между снимком и возвратом, в эти таблицы не пишет (права роли и
// рёбра графа публикуются после прохода 1), поэтому занять id некому.
var cascadeKinds = []cascadeKind{
	{
		// Роль держит свой XML, права лежат в Rights.xml. Id роли после
		// переопубликации новый, права возвращаются к роли той же identity.
		table:  "role_right",
		covers: []string{"role_right.role_id"},
		snapshot: `SELECT rr.* FROM role_right rr WHERE rr.role_id IN ` + staleRoles +
			` AND rr.origin_file_id NOT IN ` + staleFiles,
		restore: []string{
			`UPDATE temp.cascade_role_right SET
				role_id = (SELECT r.id FROM temp.cascade_role s JOIN role r
					ON r.component_id = s.component_id AND r.name_norm = s.name_norm
					WHERE s.id = cascade_role_right.role_id),
				object_id = (SELECT o.id FROM metadata_object o WHERE o.id = cascade_role_right.object_id)`,
			`INSERT INTO role_right SELECT * FROM temp.cascade_role_right WHERE role_id IS NOT NULL`,
		},
	},
	{
		// Ребро зависит от файлов своей цепочки (object_data_edge_dep), а не от
		// XML своих концов. Рёбра, зависящие от удаляемых файлов, пересобирает
		// публикация (ADR-024), они снимку не принадлежат.
		//
		// Условие NOT IN по зависимостям сегодня избыточно: publishFiles зовёт
		// DeleteObjectEdgesForFiles раньше ReplaceSourceFiles, и таких рёбер к
		// снимку уже нет. Оно здесь как контракт store: ReplaceSourceFiles не
		// полагается на порядок вызовов у вызывающего. Без него другой путь
		// переопубликации (или перестановка шагов) вернул бы ребро, которое
		// публикация тут же построит заново: ребро задвоилось бы, а у
		// возвращённой копии не было бы зависимости от переопубликованного
		// файла, то есть следующая правка этого файла её бы не сняла.
		table:  "object_data_edge",
		covers: []string{"object_data_edge.from_object_id", "object_data_edge.to_object_id"},
		snapshot: `SELECT e.* FROM object_data_edge e
			WHERE (e.from_object_id IN ` + staleNodes[nodeObject] + ` OR e.to_object_id IN ` + staleNodes[nodeObject] + `)
			  AND e.id NOT IN (SELECT edge_id FROM object_data_edge_dep WHERE file_id IN ` + staleFiles + `)`,
		restore: []string{
			`INSERT INTO object_data_edge SELECT * FROM temp.cascade_object_data_edge e
				WHERE EXISTS(SELECT 1 FROM metadata_object o WHERE o.id = e.from_object_id)
				  AND EXISTS(SELECT 1 FROM metadata_object o WHERE o.id = e.to_object_id)`,
		},
	},
	{
		table:  "object_data_edge_dep",
		covers: []string{"object_data_edge_dep.edge_id"},
		// Условие на файл при снятом ребре выполнено всегда; оно здесь, чтобы
		// у каждого снимка был один и тот же параметр ?1.
		snapshot: `SELECT d.* FROM object_data_edge_dep d
			WHERE d.edge_id IN (SELECT id FROM temp.cascade_object_data_edge)
			  AND d.file_id NOT IN ` + staleFiles,
		// Возвращённое ребро узнаётся по id: снимок держит прежние id, а
		// других рёбер с ними после удаления нет.
		restore: []string{
			`INSERT INTO object_data_edge_dep SELECT * FROM temp.cascade_object_data_edge_dep d
				WHERE EXISTS(SELECT 1 FROM object_data_edge e WHERE e.id = d.edge_id)`,
		},
	},
	{
		// Бейдж файла не имеет (контракт §2). Пересчёт владельца
		// (publishCodeObjectEdges) после возврата снимает и пишет его сам.
		table:    "object_badge",
		covers:   []string{"object_badge.object_id"},
		snapshot: `SELECT b.* FROM object_badge b WHERE b.object_id IN ` + staleNodes[nodeObject],
		restore: []string{
			`INSERT INTO object_badge SELECT * FROM temp.cascade_object_badge b
				WHERE EXISTS(SELECT 1 FROM metadata_object o WHERE o.id = b.object_id)`,
		},
	},
}

// sameFileCascades: каскады на строку-владельца, чьи дочерние строки всегда
// принадлежат тому же файлу, что и владелец, и уходят с ним законно: файл
// переопубликуется целиком. Значение: почему это так.
var sameFileCascades = map[string]string{
	"parameter.symbol_id":           "параметры публикуются вместе с символом",
	"query.symbol_id":               "запрос живёт в теле метода, файл тот же",
	"register_access.symbol_id":     "доступ к регистру в теле метода",
	"reference.from_symbol_id":      "ссылка в теле метода",
	"call_edge.caller_id":           "вызов в теле метода",
	"call_edge.ref_id":              "ребро вызова производно от ссылки",
	"reference_candidate.ref_id":    "кандидаты производны от ссылки",
	"resolution_dep.ref_id":         "зависимости разрешения производны от ссылки",
	"query_reference.query_id":      "ссылки запроса производны от запроса",
	"metadata_member.object_id":     "члены объекта публикуются из его XML",
	"metadata_member.parent_member": "вложенный член из того же XML",
}

// saveCascades снимает каскадные строки нетронутых файлов во временные
// таблицы соединения писателя. Вызывать до DeleteSourceFiles.
//
// упрощение: потолок снимка не ограничен, он равен числу строк, которые
// держатся за владельцев переопубликуемых файлов. Худший случай на ut_demo
// (все XML ролей и объектов, 12 865 файлов, в одном инкременте): права из
// Rights.xml 45 692 строки, рёбра 372, их зависимости 1 391, бейджи 1 973,
// роли 1 087. Замер в sqlite3 на эпохе ut_demo (Memory Used до и после
// снимка, temp_store=MEMORY): 7.8 МиБ, почти всё права (около 160 байт на
// строку). Такой объём правок уходит в фоновую полную пересборку (§18.5), где
// снимать нечего. Путь выше, если понадобится: temp_store=FILE или снимок по
// частям, как у снимка указателей.
func (tx *WriteTx) saveCascades(files string) error {
	if err := tx.c.exec(tx.ctx, `DROP TABLE IF EXISTS temp.cascade_role`); err != nil {
		return err
	}
	if err := tx.c.exec(tx.ctx, `CREATE TEMP TABLE cascade_role AS
		SELECT id, component_id, name_norm FROM role WHERE id IN `+staleRoles, files); err != nil {
		return fmt.Errorf("снимок ролей: %w", err)
	}
	for _, k := range cascadeKinds {
		if err := tx.c.exec(tx.ctx, `DROP TABLE IF EXISTS temp.`+k.saved()); err != nil {
			return err
		}
		if err := tx.c.exec(tx.ctx, `CREATE TEMP TABLE `+k.saved()+` AS `+k.snapshot, files); err != nil {
			return fmt.Errorf("снимок каскада %s: %w", k.table, err)
		}
	}
	return nil
}

// restoreCascades возвращает снятые строки, чей владелец снова существует.
// Вызывать после прохода 1 и сброса буферов: владельцы уже вставлены.
func (tx *WriteTx) restoreCascades() error {
	for _, k := range cascadeKinds {
		for _, q := range k.restore {
			if err := tx.c.exec(tx.ctx, q); err != nil {
				return fmt.Errorf("возврат каскада %s: %w", k.table, err)
			}
		}
	}
	for _, k := range cascadeKinds {
		if err := tx.c.exec(tx.ctx, `DROP TABLE temp.`+k.saved()); err != nil {
			return err
		}
	}
	return tx.c.exec(tx.ctx, `DROP TABLE temp.cascade_role`)
}
