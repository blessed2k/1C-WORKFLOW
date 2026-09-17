package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// testStateDir — имя каталога состояния сервера в тестах. В бою его задаёт
// вызывающий (workspace.RegistryDirName): store не знает это имя сам.
const testStateDir = ".mcp1c"

// openTestStore открывает хранилище в новом временном корне.
func openTestStore(t *testing.T, opts Options) *Store {
	t.Helper()
	if opts.ProjectID == "" {
		opts.ProjectID = "project"
	}
	if opts.StateDirName == "" {
		opts.StateDirName = testStateDir
	}
	s, err := Open(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Пустой каталог: хранилище обязано опубликовать эпоху и честно сказать, что
// индекс надо собрать из XML. Молчаливое «всё хорошо» на пустой базе — это
// ответ «ничего не найдено» на каждый запрос вместо признания, что индекса нет.
func TestOpenPublishesEpochAndAsksForRebuild(t *testing.T) {
	s := openTestStore(t, Options{})
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Epoch != 1 {
		t.Errorf("эпоха %d, ожидалась 1", st.Epoch)
	}
	if st.Generation != "e1.g1" {
		t.Errorf("поколение %q, ожидалось e1.g1", st.Generation)
	}
	if !st.NeedsFullRebuild {
		t.Error("пустая эпоха не потребовала полной сборки индекса")
	}
	if st.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version=%d, ожидалась %d", st.SchemaVersion, SchemaVersion)
	}
	if st.ValidatedAt.IsZero() {
		t.Error("validated_at не проставлен: эпоха опубликована без отметки о проверке")
	}
	if _, err := os.Stat(filepath.Join(s.dir, pointerName)); err != nil {
		t.Errorf("указатель не создан: %v", err)
	}
	if st.DBBytes == 0 {
		t.Error("файл эпохи пуст")
	}
	// Указатель публикуется только на эпоху, рядом с которой не осталось WAL.
	if st.WALBytes != 0 {
		t.Errorf("рядом с опубликованной эпохой остался WAL %d Б", st.WALBytes)
	}
}

// Состав схемы сверяется со списком раздела 15 архитектуры плюс семь таблиц,
// названных в таске (form_command, handler_binding, query_reference,
// event_subscription, scheduled_job, role, role_right) и три таблицы объектного
// графа из §2 спецификации В1 (object_data_edge, object_data_edge_dep,
// object_badge). Список записан здесь руками из документа, а не выведен из
// createScript: иначе тест сверял бы схему сам с собой и никогда бы не заметил
// пропажи таблицы.
func TestSchemaContainsEveryTableOfSection15(t *testing.T) {
	want := []string{
		"blob", "call_edge", "component", "dependency_edge", "diagnostic",
		"event_subscription", "form", "form_command", "form_declaration",
		"form_element", "form_structure", "fts_symbols", "generation_log",
		"handler_binding", "meta", "metadata_member", "metadata_object", "module",
		"module_code", "module_context", "node", "object_badge",
		"object_data_edge", "object_data_edge_dep", "parameter", "query",
		"query_reference", "reference", "reference_candidate", "register_access",
		"resolution_dep", "role", "role_right", "scheduled_job", "source_file",
		"symbol",
	}
	s := openTestStore(t, Options{})
	var got []string
	err := s.Read(context.Background(), func(tx *ReadTx) error {
		rows, err := tx.c.query(tx.ctx, `SELECT name FROM sqlite_master WHERE type='table'
			AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'fts_symbols_%' ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			got = append(got, n)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	missing := diffSets(want, got)
	if len(missing) > 0 {
		t.Errorf("в схеме нет таблиц раздела 15: %v", missing)
	}
	if extra := diffSets(got, want); len(extra) > 0 {
		t.Errorf("в схеме есть таблицы, которых нет в разделе 15: %v", extra)
	}
}

// diffSets возвращает элементы a, которых нет в b.
func diffSets(a, b []string) []string {
	have := make(map[string]bool, len(b))
	for _, v := range b {
		have[v] = true
	}
	var out []string
	for _, v := range a {
		if !have[v] {
			out = append(out, v)
		}
	}
	return out
}

// Заполненная база обязана проходить foreign_key_check и инварианты раздела 15.
func TestFilledIndexPassesInvariants(t *testing.T) {
	s := openTestStore(t, Options{})
	ctx := context.Background()
	var f fixture
	if err := s.Write(ctx, func(tx *WriteTx) error { return seedFixture(tx, &f) }); err != nil {
		t.Fatalf("наполнение: %v", err)
	}
	assertValid(t, s, "после наполнения")

	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// COMMIT — единственная точка публикации: одна write-транзакция = +1.
	if st.Generation != "e1.g2" {
		t.Errorf("поколение после одной записи %q, ожидалось e1.g2", st.Generation)
	}
}

// Полная пересборка: новая эпоха собирается отдельным файлом, публикуется
// строго после validate и checkpoint, и только после этого старая уходит.
// До момента публикации старая эпоха остаётся last known good.
func TestRebuildSwitchesEpochAndDropsOld(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()
	oldPath := s.currentPath()

	if err := s.Rebuild(ctx, func(tx *WriteTx) error {
		return tx.UpsertComponent(Component{ID: "ext", Kind: "extension", Root: "e", ApplyOrder: 1})
	}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Epoch != 2 {
		t.Errorf("эпоха %d, ожидалась 2", st.Epoch)
	}
	if st.NeedsFullRebuild {
		t.Error("после полной сборки индекс всё ещё считается неготовым")
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("старая эпоха не удалена после переключения: %v", err)
	}
	if n := countRows(t, s, "symbol", ""); n != 0 {
		t.Errorf("в новой эпохе остались факты старой: символов %d", n)
	}
	if n := countRows(t, s, "component", "id='ext'"); n != 1 {
		t.Error("данные новой сборки не опубликованы")
	}
	assertValid(t, s, "после полной пересборки")
}

// Во время полной пересборки сервер обязан продолжать отвечать из last known
// good (истории 8 и 33): чтение идёт на СТАРОЙ эпохе и не ждёт конца сборки.
// Тест краснеет, если состояние хранилища снова закрыть мьютексом на всё время
// сборки: чтение тогда встаёт в очередь за ней.
func TestReadDuringRebuildAnswersFromOldEpoch(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()
	before, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	symbolsBefore := countRows(t, s, "symbol", "")
	if symbolsBefore == 0 {
		t.Fatal("подготовка: в старой эпохе нет фактов")
	}

	hold := make(chan struct{})
	var release sync.Once
	letGo := func() { release.Do(func() { close(hold) }) }
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Rebuild(ctx, func(tx *WriteTx) error {
			close(started)
			<-hold
			return tx.UpsertComponent(Component{ID: "cfg", Kind: "configuration", Root: "."})
		})
	}()
	<-started
	// Страховка: если чтение всё-таки заблокировано сборкой, отпускаем её и
	// падаем с внятным сообщением вместо зависания до конца прогона.
	time.AfterFunc(2*time.Second, letGo)

	start := time.Now()
	var seen int64
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var err error
		seen, err = tx.c.queryInt(tx.ctx, `SELECT COUNT(*) FROM symbol`)
		return err
	}); err != nil {
		t.Fatalf("чтение во время пересборки: %v", err)
	}
	elapsed := time.Since(start)
	during, err := s.Status(ctx)
	if err != nil {
		t.Fatalf("статус во время пересборки: %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("чтение ждало пересборку %s: сервер не отвечает из last known good", elapsed)
	}
	if seen != symbolsBefore {
		t.Errorf("во время пересборки видно %d символов, ожидалось %d из старой эпохи", seen, symbolsBefore)
	}
	if during.Epoch != before.Epoch || during.Generation != before.Generation {
		t.Errorf("во время пересборки отдано поколение %s (эпоха %d), ожидалось прежнее %s (эпоха %d)",
			during.Generation, during.Epoch, before.Generation, before.Epoch)
	}

	letGo()
	if err := <-done; err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	after, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Epoch != before.Epoch+1 {
		t.Errorf("после пересборки эпоха %d, ожидалась %d", after.Epoch, before.Epoch+1)
	}
}

// Диагностика исчерпанного пула обязана быть доступна именно тогда, когда пул
// исчерпан: Status не встаёт в очередь за читателем. Поля из БД в этом случае
// не заполняются, и это сказано явно, а не подменено нулями.
func TestStatusDoesNotWaitForReader(t *testing.T) {
	s := openTestStore(t, Options{ReaderPoolSize: 1})
	ctx := context.Background()
	hold := make(chan struct{})
	inside := make(chan struct{})
	go func() {
		s.Read(ctx, func(tx *ReadTx) error { //nolint:errcheck // результат проверяет основная горутина
			close(inside)
			<-hold
			return nil
		})
	}()
	<-inside

	start := time.Now()
	st, err := s.Status(ctx)
	elapsed := time.Since(start)
	close(hold)
	if err != nil {
		t.Fatalf("Status при исчерпанном пуле: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Status ждал свободного читателя %s", elapsed)
	}
	if !st.DBUnavailable {
		t.Error("Status не сообщил, что данные из БД не читались")
	}
	if st.Epoch != 1 || st.PointerSeq == 0 {
		t.Errorf("эпоха %d и seq указателя %d недоступны без аренды соединения", st.Epoch, st.PointerSeq)
	}
	if st.ReaderPool.Size != 1 || st.ReaderPool.Acquired == 0 {
		t.Errorf("диагностика пула недоступна при его исчерпании: %+v", st.ReaderPool)
	}
}

// assertValid проверяет инварианты через публичный интерфейс.
func assertValid(t *testing.T, s *Store, where string) {
	t.Helper()
	var bad []string
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		bad, err = tx.Validate()
		return err
	}); err != nil {
		t.Fatalf("%s: validate: %v", where, err)
	}
	if len(bad) > 0 {
		t.Fatalf("%s: нарушены инварианты: %v", where, bad)
	}
}
