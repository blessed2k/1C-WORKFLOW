package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store/storetest"
)

// testMigrations — шаг схемы для проверки самого механизма миграций. Он живёт в
// тесте, а не в production-списке, намеренно: у схемы версии 1 нет
// предшественников, и выдумывать их значило бы поддерживать историю, которой
// никогда не было. Проверяется механизм: шаг применяется к БД предыдущей
// версии, не теряет данные и идемпотентен.
var testMigrations = []migration{{
	to: SchemaVersion + 1,
	statements: []string{
		`ALTER TABLE symbol ADD COLUMN deprecated INTEGER NOT NULL DEFAULT 0`,
		`UPDATE symbol SET deprecated = 1 WHERE is_export = 0`,
		`CREATE INDEX idx_symbol_deprecated ON symbol(deprecated) WHERE deprecated = 1`,
	},
}}

// Пустая БД: схема создаётся сразу текущей версии, применять к ней нечего.
func TestMigrationOnFreshDatabase(t *testing.T) {
	s := openTestStore(t, Options{})
	ctx := context.Background()
	got, err := readSchemaVersion(ctx, s.writer)
	if err != nil {
		t.Fatal(err)
	}
	if got != SchemaVersion {
		t.Fatalf("schema_version на новой БД %d, ожидалась %d", got, SchemaVersion)
	}
	applied, err := migrate(ctx, s.writer, migrations, SchemaVersion)
	if err != nil {
		t.Fatalf("миграция пустой БД: %v", err)
	}
	if applied != 0 {
		t.Errorf("на актуальной схеме применено шагов %d, ожидалось 0", applied)
	}
}

// БД предыдущей версии: шаг применяется под foreign_keys=ON, данные целы,
// инварианты целы, повторный вызов ничего не делает.
func TestMigrationFromPreviousVersion(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()
	before := countRows(t, s, "symbol", "")

	applied, err := migrate(ctx, s.writer, testMigrations, SchemaVersion+1)
	if err != nil {
		t.Fatalf("миграция: %v", err)
	}
	if applied != 1 {
		t.Fatalf("применено шагов %d, ожидался 1", applied)
	}
	if v, _ := readSchemaVersion(ctx, s.writer); v != SchemaVersion+1 {
		t.Errorf("schema_version=%d, ожидалась %d", v, SchemaVersion+1)
	}
	if n := countRows(t, s, "symbol", ""); n != before {
		t.Errorf("миграция потеряла символы: было %d, стало %d", before, n)
	}
	// Бэкфилл шага помечает не-экспортные символы. Ожидаемое число берётся из
	// самой фикстуры, а не из результата миграции, и в фикстуре такой символ
	// есть: иначе утверждение было бы верно и при полностью пропущенном шаге.
	notExported := countRows(t, s, "symbol", "is_export=0")
	if notExported == 0 {
		t.Fatal("в фикстуре нет ни одного не-экспортного символа: бэкфилл нечем проверить")
	}
	if n := countRows(t, s, "symbol", "deprecated=1"); n != notExported {
		t.Errorf("бэкфилл пометил %d символов, ожидалось %d", n, notExported)
	}
	if n := countRows(t, s, "symbol", "deprecated=0"); n != before-notExported {
		t.Errorf("не помечено %d символов, ожидалось %d", n, before-notExported)
	}
	assertValid(t, s, "после миграции")

	applied, err = migrate(ctx, s.writer, testMigrations, SchemaVersion+1)
	if err != nil || applied != 0 {
		t.Errorf("повторная миграция: applied=%d err=%v", applied, err)
	}
}

// Версия схемы выше известной — несовместимость. По разделу 15 это новая эпоха
// с полным rebuild из XML, а не попытка «как-нибудь открыть» непонятную базу.
func TestMigrationRefusesSchemaFromFuture(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()
	if err := s.writer.exec(ctx, `UPDATE meta SET value='99' WHERE key=?`, metaSchemaVersion); err != nil {
		t.Fatal(err)
	}
	_, err := migrate(ctx, s.writer, migrations, SchemaVersion)
	var future errSchemaFromFuture
	if !errors.As(err, &future) {
		t.Fatalf("миграция не заметила схему из будущего: %v", err)
	}
	if future.found != 99 {
		t.Errorf("в ошибке версия %d, ожидалась 99", future.found)
	}
}

// Открытие базы со схемой из будущего заканчивается новой эпохой и требованием
// полной сборки: старая эпоха не публикуется и не читается наугад.
func TestOpenOnSchemaFromFutureStartsNewEpoch(t *testing.T) {
	root := t.TempDir()
	opts := Options{ProjectID: "project", StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.SetMeta(metaSchemaVersion, "99")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	defer s2.Close()
	st, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Epoch != 2 {
		t.Errorf("эпоха %d, ожидалась 2: несовместимая схема требует новой эпохи", st.Epoch)
	}
	if !st.NeedsFullRebuild {
		t.Error("после несовместимой схемы не запрошена полная сборка индекса")
	}
	if st.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version новой эпохи %d, ожидалась %d", st.SchemaVersion, SchemaVersion)
	}
}

// downgradeToV1 приводит открытую базу к виду схемы версии 1: сносит таблицы
// объектного графа и колонку слоя. Это единственный способ получить базу
// предыдущей версии, не храня её копию: createScript теперь создаёт сразу v2.
func downgradeToV1(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		for _, q := range storetest.DowngradeToSchema1Statements {
			if err := tx.c.exec(tx.ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("сведение базы к схеме 1: %v", err)
	}
}

// tableColumns — имена колонок таблицы в порядке объявления.
func tableColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	var out []string
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		rows, err := tx.c.query(tx.ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	return out
}

// indexNames — имена индексов таблицы (без автоматических sqlite_*).
func indexNames(t *testing.T, s *Store, table string) []string {
	t.Helper()
	var out []string
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		rows, err := tx.c.query(tx.ctx,
			`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=? AND name NOT LIKE 'sqlite_%' ORDER BY name`,
			table)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("индексы %s: %v", table, err)
	}
	return out
}

// Шаг до схемы 2 на базе версии 1: таблицы графа появляются с полями §2
// спецификации, register_access получает слой со значением по умолчанию,
// данные целы, повторный прогон ничего не делает.
//
// Ожидаемые списки колонок и индексов выписаны из §2 спецификации руками:
// вывести их из objectGraphTables значило бы сверять схему саму с собой.
func TestMigrationToSchema2(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()
	accessesBefore := countRows(t, s, "register_access", "")
	if accessesBefore == 0 {
		t.Fatal("в фикстуре нет ни одного register_access: слой нечем проверить")
	}
	downgradeToV1(t, s)

	applied, err := migrate(ctx, s.writer, migrations, SchemaVersion)
	if err != nil {
		t.Fatalf("миграция до %d: %v", SchemaVersion, err)
	}
	if applied != 1 {
		t.Fatalf("применено шагов %d, ожидался 1", applied)
	}
	if v, _ := readSchemaVersion(ctx, s.writer); v != 2 {
		t.Errorf("schema_version=%d, ожидалась 2", v)
	}

	if n := countRows(t, s, "register_access", ""); n != accessesBefore {
		t.Errorf("миграция потеряла доступы к регистрам: было %d, стало %d", accessesBefore, n)
	}
	if n := countRows(t, s, "register_access", "layer='base'"); n != accessesBefore {
		t.Errorf("слой 'base' проставлен у %d строк из %d", n, accessesBefore)
	}

	want := map[string][]string{
		"object_data_edge": {"id", "from_object_id", "to_object_id", "kind", "layer",
			"provenance", "confidence", "mode", "in_transaction", "evidence"},
		"object_data_edge_dep": {"edge_id", "file_id"},
		"object_badge":         {"object_id", "badge", "layer", "count"},
	}
	for table, cols := range want {
		got := tableColumns(t, s, table)
		if strings.Join(got, ",") != strings.Join(cols, ",") {
			t.Errorf("колонки %s: %v, ожидались %v", table, got, cols)
		}
	}
	for table, cols := range map[string][]string{
		"object_data_edge":     {"idx_ode_from", "idx_ode_to", "idx_ode_kind_layer"},
		"object_data_edge_dep": {"idx_odedep_file", "idx_odedep_edge"},
	} {
		got := strings.Join(indexNames(t, s, table), ",")
		for _, want := range cols {
			if !strings.Contains(got, want) {
				t.Errorf("индекса %s нет: %s", want, got)
			}
		}
	}
	assertValid(t, s, "после миграции до схемы 2")

	applied, err = migrate(ctx, s.writer, migrations, SchemaVersion)
	if err != nil || applied != 0 {
		t.Errorf("повторная миграция: applied=%d err=%v", applied, err)
	}
}

// Схема после миграции обязана совпадать со схемой новой эпохи: расхождение
// значило бы, что мигрированный индекс отвечает не то же самое, что собранный
// заново. Эталон берётся из свежей базы, а не из текста миграции.
func TestMigratedSchemaMatchesFresh(t *testing.T) {
	fresh := openTestStore(t, Options{})
	migrated, _ := seeded(t)
	downgradeToV1(t, migrated)
	if _, err := migrate(context.Background(), migrated.writer, migrations, SchemaVersion); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	for _, table := range []string{"register_access", "object_data_edge", "object_data_edge_dep", "object_badge"} {
		a := strings.Join(tableColumns(t, fresh, table), ",")
		b := strings.Join(tableColumns(t, migrated, table), ",")
		if a != b {
			t.Errorf("колонки %s расходятся: свежая [%s], мигрированная [%s]", table, a, b)
		}
		ia := strings.Join(indexNames(t, fresh, table), ",")
		ib := strings.Join(indexNames(t, migrated, table), ",")
		if ia != ib {
			t.Errorf("индексы %s расходятся: свежая [%s], мигрированная [%s]", table, ia, ib)
		}
	}
}

// Полный жизненный цикл требования перестроить индекс: миграция его ставит,
// перезапуск не снимает, полная переиндексация снимает, и после неё оно не
// возвращается. Второй шаг здесь главный: признак в поле процесса пережил бы
// первое открытие и молча исчез бы на втором, оставив индекс с враньём в
// слоях и пустым графом под видом исправного.
func TestNeedsFullRebuildSurvivesRestartUntilRebuild(t *testing.T) {
	root := t.TempDir()
	opts := Options{ProjectID: "project", StateDirName: testStateDir}
	ctx := context.Background()
	fill := func(tx *WriteTx) error {
		var f fixture
		return seedFixture(tx, &f)
	}

	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, fill); err != nil {
		t.Fatalf("наполнение фикстуры: %v", err)
	}
	symbols := countRows(t, s, "symbol", "")
	epochBefore := statusOf(t, s).Epoch
	downgradeToV1(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// (1) Открытие индекса предыдущей версии: миграция прошла, переиндексация
	// затребована, данные на месте и эпоха прежняя — миграция не заводит новой.
	s1, err := Open(root, opts)
	if err != nil {
		t.Fatalf("открытие индекса предыдущей версии: %v", err)
	}
	st := statusOf(t, s1)
	if !st.NeedsFullRebuild {
		t.Error("после миграции старого индекса не запрошена переиндексация")
	}
	if st.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version=%d, ожидалась %d", st.SchemaVersion, SchemaVersion)
	}
	if st.Epoch != epochBefore {
		t.Errorf("эпоха %d, ожидалась прежняя %d: миграция не заводит новую эпоху", st.Epoch, epochBefore)
	}
	if n := countRows(t, s1, "symbol", ""); n != symbols {
		t.Errorf("после миграции символов %d, было %d", n, symbols)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// (2) Перезапуск ничего не лечит: схема уже 2, миграции не применяются, а
	// содержимое по-прежнему устарело.
	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	if !statusOf(t, s2).NeedsFullRebuild {
		t.Error("после перезапуска требование переиндексации потеряно, хотя индекс не наполняли")
	}

	// (3) Полная переиндексация снимает признак...
	if err := s2.Rebuild(ctx, fill); err != nil {
		t.Fatalf("полная переиндексация: %v", err)
	}
	if statusOf(t, s2).NeedsFullRebuild {
		t.Error("после полной переиндексации требование не снято")
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	// (4) ...и не возвращается следующим открытием.
	s3, err := Open(root, opts)
	if err != nil {
		t.Fatalf("открытие после переиндексации: %v", err)
	}
	defer s3.Close()
	if statusOf(t, s3).NeedsFullRebuild {
		t.Error("требование переиндексации вернулось после перезапуска")
	}
}

// Инкрементальная публикация признак НЕ снимает: она наполняет то, что
// изменилось, а мигрированные таблицы пусты целиком.
func TestNeedsFullRebuildSurvivesIncrementalWrite(t *testing.T) {
	s, _ := seeded(t)
	downgradeToV1(t, s)
	if _, err := migrate(context.Background(), s.writer, migrations, SchemaVersion); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.SetMeta("проба", "1")
	}); err != nil {
		t.Fatalf("инкрементальная запись: %v", err)
	}
	pending, err := readNeedsFullRebuild(context.Background(), s.writer)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Error("обычная запись сняла требование полной переиндексации")
	}
}

func statusOf(t *testing.T, s *Store) StoreStatus {
	t.Helper()
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return st
}
