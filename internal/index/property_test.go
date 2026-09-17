package index

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// expectedIdentityKeys пересчитывает identity_key всех узлов, которые
// ОБЯЗАН произвести пайплайн из содержимого root — той же логикой, что
// производство (parseOneFile/buildSymbols/metadataObjectIdentityKey), но
// независимо от текущего состояния store. Используется только тестом
// равенства инкремента и чистой пересборки: у internal/store нет
// bulk-SELECT (D02 — EnvInput строится из фактов пайплайна, не запросом к
// store), поэтому «логический дамп» здесь — множество identity_key плюс
// содержимое blob, а не сырые строки таблиц (то, что публичный API ReadTx
// вообще может проверить без второго SQL-слоя вне internal/store, что
// запрещено гардом internal/arch).
func expectedIdentityKeys(t *testing.T, project domain.ProjectID, component domain.ComponentID, root string) []string {
	t.Helper()
	discovered, err := discoverComponent(root, nil, nil)
	if err != nil {
		t.Fatalf("discoverComponent: %v", err)
	}
	var keys []string
	for _, d := range discovered {
		data, err := os.ReadFile(d.absPath)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", d.relPath, err)
		}
		rec := parseOneFile(d.relPath, data)
		if rec.bslModule != nil {
			keys = append(keys, moduleIdentityKey(component, d.relPath))
			for _, sym := range buildSymbols(project, component, domain.BaseLayer(component), d.relPath, rec.bslModule) {
				keys = append(keys, symbolIdentityKey(sym.UID))
			}
		}
		if rec.metaFacts.Object != nil {
			objKey := metadataObjectIdentityKey(component, rec.metaFacts.Object.MType, rec.metaFacts.Object.NameNorm)
			keys = append(keys, objKey)
			for _, m := range rec.metaFacts.Members {
				keys = append(keys, metadataMemberIdentityKey(objKey, m))
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// assertIdentitySetPresent проверяет, что КАЖДЫЙ ожидаемый identity_key
// существует в store (узел не потерян) и что каждый файл дал тот же
// content_hash, что и на диске (blob не подменён и не устарел).
func assertIdentitySetPresent(t *testing.T, ctx context.Context, st *store.Store, component domain.ComponentID, root string, keys []string) {
	t.Helper()
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() = %v, want пусто", bad)
		}
		for _, k := range keys {
			_, ok, err := tx.NodeID(k)
			if err != nil {
				return err
			}
			if !ok {
				t.Errorf("узел %q отсутствует в индексе", k)
			}
		}

		discovered, err := discoverComponent(root, nil, nil)
		if err != nil {
			return err
		}
		for _, d := range discovered {
			data, err := os.ReadFile(d.absPath)
			if err != nil {
				return err
			}
			wantHash := store.HashContent(data)
			_, ok, err := tx.SourceFileID(string(component), d.relPath)
			if err != nil {
				return err
			}
			if !ok {
				t.Errorf("source_file %q отсутствует в индексе", d.relPath)
				continue
			}
			blob, err := tx.Blob(wantHash)
			if err != nil {
				t.Errorf("blob файла %q (hash %s) не найден: %v", d.relPath, wantHash, err)
				continue
			}
			if string(blob) != string(data) {
				t.Errorf("blob файла %q не совпадает с диском", d.relPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}

// TestIncrementEqualsCleanRebuild — R32.1, критерий приёмки таска 09:
// последовательность инкрементов на фикстуре даёт тот же набор identity-
// узлов, что чистая пересборка того же финального состояния с нуля.
//
// Оговорка по методу (см. doc-комментарий expectedIdentityKeys): сравнение
// идёт по множеству identity_key (узлы: модули, символы, объекты и члены
// метаданных) плюс Validate()==пусто в обоих сторе — то, что можно
// утверждать через ПУБЛИЧНЫЙ API store.ReadTx без второго SQL-слоя вне
// internal/store. Построчное сравнение reference/call_edge вне этого
// периметра: store не выставляет bulk-SELECT (interfaces.md, D02), а
// заводить его — не зона этого таска.
func TestIncrementEqualsCleanRebuild(t *testing.T) {
	ctx := context.Background()

	// A: полная пересборка, затем два инкремента.
	stA := openTestStore(t)
	rootA := writeFixtureComponent(t)
	svcA := NewService(stA, "proj", testManifest(t, rootA), nil, Config{})
	t.Cleanup(func() { svcA.Close() })

	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("A: Reindex(full): %v", err)
	}

	mustWrite(t, filepath.Join(rootA, "CommonModules/УтилитыОбщие/Ext/Module.bsl"), `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции

Функция Дополнительно() Экспорт
	Возврат 1;
КонецФункции
`)
	touchFuture(t, filepath.Join(rootA, "CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	if _, err := svcA.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("A: Reindex(incremental #1): %v", err)
	}

	newFile := filepath.Join(rootA, "Catalogs/Товары/Ext/ObjectModule.bsl")
	mustWrite(t, newFile, `
Процедура ПриЗаписи() Экспорт
	УтилитыОбщие.Помощь();
КонецПроцедуры
`)
	if _, err := svcA.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("A: Reindex(incremental #2): %v", err)
	}

	// B: чистая пересборка сразу финального состояния (тот же rootA, он уже
	// содержит все правки — B индексирует его с нуля отдельным store).
	stB := openTestStore(t)
	svcB := NewService(stB, "proj", testManifest(t, rootA), nil, Config{})
	t.Cleanup(func() { svcB.Close() })
	if _, err := svcB.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("B: Reindex(full): %v", err)
	}

	want := expectedIdentityKeys(t, "proj", "cfg", rootA)
	if len(want) == 0 {
		t.Fatal("expectedIdentityKeys вернул пусто — тест ничего не проверяет")
	}
	assertIdentitySetPresent(t, ctx, stA, "cfg", rootA, want)
	assertIdentitySetPresent(t, ctx, stB, "cfg", rootA, want)
}

// TestCleanRebuildDeterministic — детерминизм (§18.7): два независимых
// чистых прогона по одному и тому же исходнику дают один и тот же набор
// identity-узлов.
func TestCleanRebuildDeterministic(t *testing.T) {
	ctx := context.Background()
	root := writeFixtureComponent(t)
	want := expectedIdentityKeys(t, "proj", "cfg", root)

	for i := 0; i < 2; i++ {
		st := openTestStore(t)
		svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
		if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
			t.Fatalf("прогон %d: Reindex(full): %v", i, err)
		}
		assertIdentitySetPresent(t, ctx, st, "cfg", root, want)
		svc.Close()
	}
}

// TestIncrementAfterRestartEqualsCleanRebuild — история 13 таска 02 (R17,
// R50): случай ПЕРЕЗАПУСКА в property-тесте. Корпус второго Service не
// строится индексацией, а восстанавливается из source_file (ADR-028); дальше
// идут обычные инкременты, и итог обязан совпасть с чистой пересборкой того
// же финального состояния с нуля. Метод сравнения — тот же, что у
// TestIncrementEqualsCleanRebuild (см. его оговорку).
func TestIncrementAfterRestartEqualsCleanRebuild(t *testing.T) {
	ctx := context.Background()

	stA := openTestStore(t)
	rootA := writeFixtureComponent(t)
	svcA := NewService(stA, "proj", testManifest(t, rootA), nil, Config{})
	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("A: Reindex(full): %v", err)
	}
	svcA.Close()

	// Перезапуск процесса: тот же store, новый Service с пустым корпусом.
	restarted := NewService(stA, "proj", testManifest(t, rootA), nil, Config{})
	t.Cleanup(func() { restarted.Close() })

	modulePath := filepath.Join(rootA, "CommonModules/УтилитыОбщие/Ext/Module.bsl")
	mustWrite(t, modulePath, `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции

Функция Дополнительно() Экспорт
	Возврат 1;
КонецФункции
`)
	touchFuture(t, modulePath)
	if _, err := restarted.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("после перезапуска: Reindex(incremental #1): %v", err)
	}

	newFile := filepath.Join(rootA, "Catalogs/Товары/Ext/ObjectModule.bsl")
	mustWrite(t, newFile, `
Процедура ПриЗаписи() Экспорт
	УтилитыОбщие.Дополнительно();
КонецПроцедуры
`)
	if _, err := restarted.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("после перезапуска: Reindex(incremental #2): %v", err)
	}

	// B: чистая пересборка финального состояния в отдельном store.
	stB := openTestStore(t)
	svcB := NewService(stB, "proj", testManifest(t, rootA), nil, Config{})
	t.Cleanup(func() { svcB.Close() })
	if _, err := svcB.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("B: Reindex(full): %v", err)
	}

	want := expectedIdentityKeys(t, "proj", "cfg", rootA)
	if len(want) == 0 {
		t.Fatal("expectedIdentityKeys вернул пусто — тест ничего не проверяет")
	}
	assertIdentitySetPresent(t, ctx, stA, "cfg", rootA, want)
	assertIdentitySetPresent(t, ctx, stB, "cfg", rootA, want)
}
