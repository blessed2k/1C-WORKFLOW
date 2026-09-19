package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestRestartSeesNoChanges: после перезапуска
// процесса (новый Service поверх того же store) корпус восстанавливается из
// source_file, precheck видит 0 изменений, фоновая полная пересборка не
// планируется. Раньше карта корпуса была пуста и изменившимися объявлялись
// все файлы компонента.
func TestRestartSeesNoChanges(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)

	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	svc.Close()

	// Перезапуск: тот же store, новый Service — резидентный корпус пуст.
	restarted := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { restarted.Close() })

	changed, err := restarted.precheckChangedCount(ctx)
	if err != nil {
		t.Fatalf("precheckChangedCount: %v", err)
	}
	if changed != 0 {
		t.Errorf("precheckChangedCount = %d, want 0", changed)
	}

	corpus := restarted.corpusFor("cfg")
	// Фикстура — ровно 4 файла (writeFixtureComponent).
	if len(corpus.files) != 4 {
		t.Fatalf("len(corpus.files) = %d, want 4", len(corpus.files))
	}
	for rel, rec := range corpus.files {
		if !rec.hydrated {
			t.Errorf("%s: hydrated = false, want true", rel)
		}
		if rec.bslModule != nil {
			t.Errorf("%s: у восстановленной записи есть bslModule", rel)
		}
		if rec.metaFacts.Object != nil {
			t.Errorf("%s: у восстановленной записи есть metaFacts", rel)
		}
		if rec.contentHash == "" || rec.size == 0 {
			t.Errorf("%s: фингерпринт не восстановлен (size=%d hash=%q)", rel, rec.size, rec.contentHash)
		}
	}

	fr, err := restarted.EnsureFresh(ctx, Policy{Mode: PolicyAllowStale})
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if !fr.Fresh {
		t.Errorf("EnsureFresh = %+v, want Fresh=true", fr)
	}
	restarted.stateMu.Lock()
	reason := restarted.lastRebuildReason
	restarted.stateMu.Unlock()
	if reason != "" {
		t.Errorf("запланирована фоновая пересборка: %q", reason)
	}
}

// seedSourceFiles кладёт в индекс строки source_file по реальному состоянию
// файлов на диске — то, что оставил бы после себя прошлый процесс. staleRel
// получает предыдущую версию парсера, остальные — текущую.
func seedSourceFiles(t *testing.T, st *store.Store, root, staleRel string) {
	t.Helper()
	metas, err := discoverComponentMeta(context.Background(), root, nil, nil)
	if err != nil {
		t.Fatalf("discoverComponentMeta: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("фикстура не дала ни одного файла")
	}
	err = st.Write(context.Background(), func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "."}); err != nil {
			return err
		}
		for _, m := range metas {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.relPath)))
			if err != nil {
				return err
			}
			hash, err := tx.PutBlob(data)
			if err != nil {
				return err
			}
			pv := ParserVersion
			if m.relPath == staleRel {
				pv = ParserVersion - 1
			}
			if _, err := tx.InsertSourceFile(store.SourceFile{
				ComponentID: "cfg", RelPath: m.relPath, Size: m.size, MtimeNS: m.mtimeNS,
				ContentHash: hash, ParserVersion: pv,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("наполнение source_file: %v", err)
	}
}

// TestStaleParserVersionCountsAsChanged — §4: файл, чей parser_version в
// индексе не равен текущему, считается изменённым, даже если size/mtime
// совпали. Без этого после обновления сервера индекс объявил бы себя свежим
// и продолжил отдавать факты старого парсера.
func TestStaleParserVersionCountsAsChanged(t *testing.T) {
	ctx := context.Background()
	const staleRel = "CommonModules/УтилитыОбщие/Ext/Module.bsl"

	root := writeFixtureComponent(t)

	stAll := openTestStore(t)
	seedSourceFiles(t, stAll, root, "")
	svcAll := NewService(stAll, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcAll.Close() })
	changed, err := svcAll.precheckChangedCount(ctx)
	if err != nil {
		t.Fatalf("precheckChangedCount (все версии текущие): %v", err)
	}
	if changed != 0 {
		t.Fatalf("все версии текущие: precheckChangedCount = %d, want 0", changed)
	}

	stStale := openTestStore(t)
	seedSourceFiles(t, stStale, root, staleRel)
	svcStale := NewService(stStale, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcStale.Close() })
	changed, err = svcStale.precheckChangedCount(ctx)
	if err != nil {
		t.Fatalf("precheckChangedCount (одна старая версия): %v", err)
	}
	if changed != 1 {
		t.Errorf("одна строка со старым parser_version: precheckChangedCount = %d, want 1", changed)
	}
}

// TestPipelineAfterRestartReadsAllHydrated — §3: любой запуск пайплайна по
// компоненту дочитывает ВСЕ гидратированные записи этого компонента. Проверка
// стоит на самом инварианте: уцелей хоть одна такая запись до buildEnvInput,
// Reindex вернул бы ошибку вместо публикации.
func TestPipelineAfterRestartReadsAllHydrated(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)

	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	svc.Close()

	restarted := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { restarted.Close() })

	changedFile := filepath.Join(root, filepath.FromSlash("CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	mustWrite(t, changedFile, `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции
`)
	touchFuture(t, changedFile)

	res, err := restarted.Reindex(ctx, ModeIncremental, "")
	if err != nil {
		t.Fatalf("Reindex(incremental) после перезапуска: %v", err)
	}
	if len(res.Components) != 1 {
		t.Fatalf("res.Components = %+v, want один компонент", res.Components)
	}
	// Первый инкремент после перезапуска перепубликовывает компонент целиком
	// (4 файла фикстуры) — сознательная консервативность, ADR-028.
	if res.Components[0].FilesChanged != 4 {
		t.Errorf("FilesChanged = %d, want 4", res.Components[0].FilesChanged)
	}

	corpus := restarted.corpusFor("cfg")
	for rel, rec := range corpus.files {
		if rec.hydrated {
			t.Errorf("%s: запись осталась гидратированной после прогона пайплайна", rel)
		}
	}
	if _, err := buildEnvInput("proj", "cfg", domain.BaseLayer("cfg"), corpus); err != nil {
		t.Errorf("buildEnvInput после прогона: %v", err)
	}
}

// TestHydrationFailureFallsBackToFirstTime: пустой индекс,
// недоступная эпоха и нечитаемый source_file дают один исход — компонент
// индексируется как в первый раз. Паники нет, у отказа есть диагностика.
func TestHydrationFailureFallsBackToFirstTime(t *testing.T) {
	ctx := context.Background()
	root := writeFixtureComponent(t)

	// Пустой индекс: восстанавливать нечего, все файлы новые, жаловаться не на что.
	empty := openTestStore(t)
	svcEmpty := NewService(empty, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcEmpty.Close() })
	changed, err := svcEmpty.precheckChangedCount(ctx)
	if err != nil {
		t.Fatalf("пустой индекс: precheckChangedCount: %v", err)
	}
	if changed != 4 {
		t.Errorf("пустой индекс: precheckChangedCount = %d, want 4", changed)
	}
	// Пустой индекс отказом гидратации не считается (ADR-028 п.6), но
	// молчать о том, что корпус не восстановлен, нельзя: нужна
	// диагностика и для этого случая (info, не warning).
	svcEmpty.stateMu.Lock()
	diags := append([]domain.Diagnostic(nil), svcEmpty.lastDiagnostics...)
	svcEmpty.stateMu.Unlock()
	if len(diags) != 1 || diags[0].Code != "index_corpus_not_hydrated" ||
		diags[0].Severity != domain.SeverityInfo {
		t.Errorf("пустой индекс: диагностика = %+v", diags)
	}

	// Индекс не читается: тот же исход плюс диагностика.
	broken := openTestStore(t)
	if err := broken.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	svcBroken := NewService(broken, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcBroken.Close() })
	changed, err = svcBroken.precheckChangedCount(ctx)
	if err != nil {
		t.Fatalf("нечитаемый индекс: precheckChangedCount: %v", err)
	}
	if changed != 4 {
		t.Errorf("нечитаемый индекс: precheckChangedCount = %d, want 4", changed)
	}
	svcBroken.stateMu.Lock()
	got := append([]domain.Diagnostic(nil), svcBroken.lastDiagnostics...)
	svcBroken.stateMu.Unlock()
	if len(got) != 1 || got[0].Code != "index_corpus_hydration_failed" {
		t.Fatalf("диагностика отказа гидратации = %+v", got)
	}
}

// TestRestartWithOneEditDoesNotBlockCaller: после
// перезапуска одна правка на диске не имеет права утащить вызывающего в
// СИНХРОННЫЙ разбор всего проекта. Порог «синхронно или в фон» считает объём
// предстоящей работы (изменения плюс гидратированные записи, которые придётся
// дочитать), а не число изменённых на диске файлов. Здесь на диске изменён
// один файл из четырёх при SmallChangeFileLimit=2: по числу изменений это
// «мало» и путь был бы синхронным, по объёму работы (4 файла) — «много».
func TestRestartWithOneEditDoesNotBlockCaller(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	cfg := Config{SmallChangeFileLimit: 2, DebounceQuiet: time.Hour}

	svc := NewService(st, "proj", testManifest(t, root), nil, cfg)
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	svc.Close()

	restarted := NewService(st, "proj", testManifest(t, root), nil, cfg)
	t.Cleanup(func() { restarted.Close() })

	changedFile := filepath.Join(root, filepath.FromSlash("CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	mustWrite(t, changedFile, `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции
`)
	touchFuture(t, changedFile)

	fr, err := restarted.EnsureFresh(ctx, Policy{Mode: PolicyAllowStale})
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if fr.Fresh {
		t.Fatalf("EnsureFresh = %+v: вызывающий дождался синхронного разбора всего проекта", fr)
	}
	if fr.Reason != "background-rebuild-scheduled" {
		t.Errorf("Reason = %q, want background-rebuild-scheduled", fr.Reason)
	}

	// Синхронного разбора не было: нетронутые записи так и остались
	// восстановленными из индекса.
	corpus := restarted.corpusFor("cfg")
	stillHydrated := 0
	for _, rec := range corpus.files {
		if rec.hydrated {
			stillHydrated++
		}
	}
	if stillHydrated != 4 {
		t.Errorf("гидратированных записей осталось %d, want 4 (пайплайн не должен был запускаться)", stillHydrated)
	}
}
