package index

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/store/storetest"
)

// TestEnsureFreshSmallChangeSync — мало изменений (в пределах
// SmallChangeFileLimit): EnsureFresh делает синхронный инкремент и отдаёт
// fresh без предупреждения.
func TestEnsureFreshSmallChangeSync(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{SmallChangeFileLimit: 50})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	mustWrite(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"), `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции
`)
	touchFuture(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	mustWrite(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"), `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
	УтилитыОбщие.Помощь();
КонецПроцедуры
`)
	touchFuture(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"))

	fr, err := svc.EnsureFresh(ctx, Policy{Mode: PolicyAllowStale})
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if !fr.Fresh {
		t.Errorf("Fresh = false, want true (изменение внутри SmallChangeFileLimit)")
	}
}

// TestEnsureFreshAllowStaleDuringBackgroundRebuild — много изменений:
// allow-stale отдаёт ответ сразу с warning вместо ожидания фоновой
// пересборки.
func TestEnsureFreshAllowStaleDuringBackgroundRebuild(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	cfg := Config{SmallChangeFileLimit: 1, DebounceQuiet: time.Hour} // debounce намеренно долгий: rebuild не должен успеть до проверки
	svc := NewService(st, "proj", testManifest(t, root), nil, cfg)
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	mustWrite(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"), `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции
`)
	touchFuture(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	mustWrite(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"), `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
	УтилитыОбщие.Помощь();
КонецПроцедуры
`)
	touchFuture(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"))

	fr, err := svc.EnsureFresh(ctx, Policy{Mode: PolicyAllowStale})
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if fr.Fresh {
		t.Errorf("Fresh = true, want false (много изменений, debounce не истёк)")
	}
	if fr.Reason == "" {
		t.Errorf("Reason пуст — allow-stale обязан назвать причину")
	}
}

// TestEnsureFreshRequireFreshTimesOut — require-fresh не дожидается
// зависшей (искусственно заблокированной) пересборки в пределах короткого
// deadline и возвращает ErrIndexNotFresh — устаревшее под видом свежего не
// отдаётся никогда.
func TestEnsureFreshRequireFreshTimesOut(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	cfg := Config{SmallChangeFileLimit: 1, DebounceQuiet: time.Millisecond, RequireFreshDeadline: 150 * time.Millisecond}
	svc := NewService(st, "proj", testManifest(t, root), nil, cfg)
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	mustWrite(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"), `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции
`)
	touchFuture(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	mustWrite(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"), `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
	УтилитыОбщие.Помощь();
КонецПроцедуры
`)
	touchFuture(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"))

	release := make(chan struct{})
	testMidRunHook = func() { <-release }

	_, err := svc.EnsureFresh(ctx, Policy{Mode: PolicyRequireFresh})
	if err == nil {
		t.Fatal("EnsureFresh(require-fresh) = nil error, want ErrIndexNotFresh")
	}
	if _, ok := err.(*ErrIndexNotFresh); !ok {
		t.Errorf("err = %T(%v), want *ErrIndexNotFresh", err, err)
	}

	// Отпускаем фоновую пересборку и дожидаемся её конца (svc.Close ждёт
	// debouncer) ДО того, как снять хук — иначе снятие хука гонится с его
	// чтением в ещё выполняющейся горутине (поймано go test -race).
	close(release)
	svc.Close()
	testMidRunHook = nil
}

// TestEnsureFreshRequireFreshWaits — require-fresh дожидается фоновой
// пересборки (короткий debounce) и возвращает fresh.
func TestEnsureFreshRequireFreshWaits(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	cfg := Config{SmallChangeFileLimit: 1, DebounceQuiet: 5 * time.Millisecond, RequireFreshDeadline: 5 * time.Second}
	svc := NewService(st, "proj", testManifest(t, root), nil, cfg)
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	mustWrite(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"), `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции
`)
	touchFuture(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	mustWrite(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"), `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
	УтилитыОбщие.Помощь();
КонецПроцедуры
`)
	touchFuture(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"))

	fr, err := svc.EnsureFresh(ctx, Policy{Mode: PolicyRequireFresh})
	if err != nil {
		t.Fatalf("EnsureFresh(require-fresh): %v", err)
	}
	if !fr.Fresh {
		t.Errorf("Fresh = false, want true — require-fresh обязан дождаться")
	}
}

// TestEnsureFreshRefusesMigratedIndex — индекс предыдущей версии схемы
// мигрирован, ни один файл выгрузки при этом не менялся. Проверка свежести
// обязана это заметить: миграция создаёт колонки и таблицы, но не наполняет
// их, поэтому ответ по такому индексу шёл бы по пустым таблицам графа и по
// слою 'base' у всех фактов подряд (R53i — существующие индексы обязаны
// перестроиться, а не просто пережить смену версии).
//
// Проверяется последствие для инструмента, а не возврат внутренней функции:
// allow-stale получает явно устаревший ответ с причиной, require-fresh —
// честный отказ ErrIndexNotFresh вместо данных старой схемы. Без учёта
// признака оба режима отвечали бы «свежо»: файлы совпадают по size и mtime,
// а неизвестные корпусу файлы укладываются в SmallChangeFileLimit и лечатся
// синхронным инкрементом, который признак не снимает.
func TestEnsureFreshRefusesMigratedIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := writeFixtureComponent(t)
	opts := store.Options{ProjectID: "proj", StateDirName: testStateDir}
	// DebounceQuiet длинный: фоновая пересборка не должна успеть починить
	// индекс до проверки, иначе тест верен при любом коде.
	cfg := Config{SmallChangeFileLimit: 50, DebounceQuiet: time.Hour,
		RequireFreshDeadline: 150 * time.Millisecond}

	st1, err := store.Open(dir, opts)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	svc1 := NewService(st1, "proj", testManifest(t, root), nil, cfg)
	if _, err := svc1.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	st, err := st1.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	epochPath := st.EpochPath
	svc1.Close()
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}

	if err := storetest.DowngradeEpochToSchema1(epochPath); err != nil {
		t.Fatalf("сведение эпохи к схеме 1: %v", err)
	}

	st2, err := store.Open(dir, opts) // здесь применяется миграция
	if err != nil {
		t.Fatalf("открытие мигрируемого индекса: %v", err)
	}
	t.Cleanup(func() { st2.Close() })
	svc2 := NewService(st2, "proj", testManifest(t, root), nil, cfg)
	t.Cleanup(func() { svc2.Close() })

	fr, err := svc2.EnsureFresh(ctx, Policy{Mode: PolicyAllowStale})
	if err != nil {
		t.Fatalf("EnsureFresh(allow-stale): %v", err)
	}
	if fr.Fresh {
		t.Error("Fresh = true на мигрированном индексе: ответ пошёл бы по пустым таблицам графа")
	}
	if fr.Reason == "" {
		t.Error("Reason пуст — allow-stale обязан назвать причину устаревания")
	}

	if _, err := svc2.EnsureFresh(ctx, Policy{Mode: PolicyRequireFresh}); err == nil {
		t.Error("require-fresh ответил по мигрированному индексу вместо отказа")
	} else {
		var notFresh *ErrIndexNotFresh
		if !errors.As(err, &notFresh) {
			t.Errorf("require-fresh вернул %v, ожидался ErrIndexNotFresh", err)
		}
	}

	// Полная переиндексация — единственное, что снимает признак.
	if _, err := svc2.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) после миграции: %v", err)
	}
	fr, err = svc2.EnsureFresh(ctx, Policy{Mode: PolicyRequireFresh})
	if err != nil {
		t.Fatalf("EnsureFresh после полной пересборки: %v", err)
	}
	if !fr.Fresh {
		t.Error("после полной пересборки индекс всё ещё не свежий")
	}
}
