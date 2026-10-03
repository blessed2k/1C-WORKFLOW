package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Ключи meta. Читаются и пишутся только внутри пакета: формат служебных полей
// наружу не выходит.
const (
	metaSchemaVersion     = "schema_version"
	metaEpoch             = "epoch"
	metaCurrentGeneration = "current_generation"
	metaValidatedAt       = "validated_at"
	// metaNeedsFullRebuild — индекс структурно обновлён, но не наполнен.
	// Живёт в meta, а не в поле процесса: устаревание СОДЕРЖИМОГО переживает
	// закрытие хранилища, и после перезапуска индекс обязан требовать
	// переиндексации так же громко, как в тот раз, когда его мигрировали.
	metaNeedsFullRebuild = "needs_full_rebuild"
)

// migration — один шаг встроенной последовательной миграции (раздел 15).
type migration struct {
	to         int
	statements []string
	// needsFullRebuild — шаг структурный: он создаёт колонки и таблицы, но
	// наполнить их не может, потому что содержательные значения знает только
	// парсер. Такой шаг ставит metaNeedsFullRebuild в ТОЙ ЖЕ транзакции, что и
	// новую версию схемы: иначе падение между двумя записями оставило бы базу
	// с версией 2 и без признака, то есть молча устаревшей.
	needsFullRebuild bool
}

// migrations — шаги обновления схемы. Новая версия добавляет сюда свой шаг и
// поднимает SchemaVersion.
//
// Шаг до 2 исполняет ТОТ ЖЕ текст DDL, что и createScript на новой эпохе:
// иначе мигрированная база тихо расходится со свежесозданной. Наполнить новые
// колонки шаг не может и не пытается: layer проставляется дефолтом 'base' всем
// строкам подряд, а таблицы графа создаются пустыми. Поэтому шаг помечен
// needsFullRebuild — признак ложится в meta и снимается только завершённой
// полной переиндексацией.
//
// Шаг до 3 DDL не меняет вовсе: он объявляет СОДЕРЖИМОЕ индексов версии 2
// ненадёжным. До ADR-037 инкремент после правки файла-цели (тело общего
// модуля, XML объекта) обрывал указатели нетронутых файлов на пересозданные
// узлы: ссылки оставались unresolved, call_edge resolved без callee,
// обработчики подписок и объекты запросов пустыми. Какие строки задеты,
// по базе не восстановить (unresolved неотличим от честного), и починить их
// может только полная пересборка, её шаг и требует.
//
// Шаг до 4 тоже без DDL, по тому же основанию (ADR-038): до него инкремент
// после правки XML роли, регистра или документа сносил каскадом права из
// нетронутого Rights.xml, рёбра объектного графа и бейджи, а чистая
// пересборка оставляла пустым role.object_id. Потерю строки по базе не
// увидеть.
//
// Шаг до 6 без DDL (issue #14): до него инкремент после удаления XML объекта
// при живом модуле оставлял module.owner_object_id на удалённом узле. По базе
// такой указатель не отличить: id узла без AUTOINCREMENT мог достаться новому
// объекту, и указатель стал бы не висячим, а чужим.
//
// Шаг до 7 без DDL (issue #15): колонки symbol.region и symbol.doc_first_line
// объявлены с первой версии, но индексация их не заполняла. Наполнить их
// может только разбор модулей, выход парсера при этом прежний, поэтому
// пересборку требует шаг схемы, а не ParserVersion.
var migrations = []migration{{
	to:               2,
	needsFullRebuild: true,
	statements: append([]string{
		`ALTER TABLE register_access ADD COLUMN layer TEXT NOT NULL DEFAULT 'base'`,
	}, append(splitStatements(objectGraphTables), splitStatements(objectGraphIndexes)...)...),
}, {
	to:               3,
	needsFullRebuild: true,
}, {
	to:               4,
	needsFullRebuild: true,
}, {
	// Шаг до 5: таблицы фактов HTTP (веха В2, ADR-039) создаются тем же
	// текстом, что и на новой эпохе; наполнить их может только парсер.
	to:               5,
	needsFullRebuild: true,
	statements:       append(splitStatements(httpTables), splitStatements(httpIndexes)...),
}, {
	to:               6,
	needsFullRebuild: true,
}, {
	to:               7,
	needsFullRebuild: true,
}}

// errSchemaFromFuture — БД собрана более новой версией пакета. По разделу 15
// это повод для новой эпохи с полным rebuild из XML, а не для угадывания.
type errSchemaFromFuture struct{ found, known int }

func (e errSchemaFromFuture) Error() string {
	return fmt.Sprintf("schema_version=%d выше известной %d: нужна новая эпоха с полным rebuild из XML",
		e.found, e.known)
}

func readSchemaVersion(ctx context.Context, c *conn) (int, error) {
	s, err := c.queryText(ctx, `SELECT value FROM meta WHERE key=?`, metaSchemaVersion)
	if err != nil {
		return 0, fmt.Errorf("meta.schema_version не прочитан: %w", err)
	}
	return strconv.Atoi(s)
}

// migrate применяет шаги строго по возрастанию версии, каждый в своей
// write-транзакции: незавершённый шаг откатывается целиком, а не оставляет
// схему на полпути.
func migrate(ctx context.Context, c *conn, steps []migration, known int) (applied int, err error) {
	cur, err := readSchemaVersion(ctx, c)
	if err != nil {
		return 0, err
	}
	if cur > known {
		return 0, errSchemaFromFuture{found: cur, known: known}
	}
	for _, m := range steps {
		if m.to <= cur {
			continue
		}
		if err := c.exec(ctx, "BEGIN IMMEDIATE"); err != nil {
			return applied, err
		}
		for _, s := range m.statements {
			if err := c.exec(ctx, s); err != nil {
				c.exec(ctx, "ROLLBACK")
				return applied, fmt.Errorf("миграция до %d (%.50s): %w", m.to, s, err)
			}
		}
		if err := c.exec(ctx, `UPDATE meta SET value=? WHERE key=?`, itoa(m.to), metaSchemaVersion); err != nil {
			c.exec(ctx, "ROLLBACK")
			return applied, err
		}
		if m.needsFullRebuild {
			if err := c.exec(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`,
				metaNeedsFullRebuild, "1"); err != nil {
				c.exec(ctx, "ROLLBACK")
				return applied, err
			}
		}
		if err := c.exec(ctx, "COMMIT"); err != nil {
			return applied, err
		}
		cur = m.to
		applied++
	}
	return applied, nil
}

func itoa(v int) string { return strconv.Itoa(v) }

// readNeedsFullRebuild — стоит ли на эпохе признак «структура новая, данных
// нет». Читается при КАЖДОМ открытии: снять его может только полная
// переиндексация, а не перезапуск процесса.
func readNeedsFullRebuild(ctx context.Context, c *conn) (bool, error) {
	v, err := c.queryText(ctx, `SELECT value FROM meta WHERE key=?`, metaNeedsFullRebuild)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// clearNeedsFullRebuild снимает признак. Единственный законный вызывающий —
// завершённая полная переиндексация (Store.Rebuild): частичная или
// инкрементальная публикация наполняет не всё и права снимать его не имеет.
func clearNeedsFullRebuild(ctx context.Context, c *conn) error {
	return c.exec(ctx, `DELETE FROM meta WHERE key=?`, metaNeedsFullRebuild)
}
