package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestDeleteFileNoDanglingFK — R32.2: удаление файла обрабатывается как
// удаление фактов, PRAGMA foreign_key_check (через tx.Validate) остаётся
// пустым, а ссылка на удалённый символ из другого файла переходит в
// unresolved вместо разрыва.
func TestDeleteFileNoDanglingFK(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	// Удаляем сам общий модуль — единственный поставщик символа Помощь,
	// на который ссылается ManagerModule.bsl.
	if err := os.Remove(filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental после удаления): %v", err)
	}

	err := st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() после удаления файла = %v, want пусто", bad)
		}
		if _, ok, err := tx.SourceFileID("cfg", "CommonModules/УтилитыОбщие/Ext/Module.bsl"); err != nil {
			return err
		} else if ok {
			t.Errorf("удалённый файл всё ещё числится в source_file")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}

// TestRenameFileNoDanglingFK — R32.2: переименование = удаление старого пути
// + добавление нового (упрощение архитектуры §17: переиспользование фактов
// не в v1). Инвариантов не нарушено, новый путь проиндексирован.
func TestRenameFileNoDanglingFK(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	oldPath := filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl")
	newRel := "Catalogs/Товары/Ext/ManagerModule2.bsl"
	newPath := filepath.Join(root, filepath.FromSlash(newRel))
	data, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.WriteFile(newPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental после переименования): %v", err)
	}

	err = st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() после переименования = %v, want пусто", bad)
		}
		if _, ok, err := tx.SourceFileID("cfg", "Catalogs/Товары/Ext/ManagerModule.bsl"); err != nil {
			return err
		} else if ok {
			t.Errorf("старый путь всё ещё числится в source_file")
		}
		if _, ok, err := tx.SourceFileID("cfg", newRel); err != nil {
			return err
		} else if !ok {
			t.Errorf("новый путь не проиндексирован")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}
