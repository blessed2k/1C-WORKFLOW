package index

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestReindexCancellationLeavesStoreIntact — отмена по context.Context
// посреди индексации (§17/§28): транзакция откатывается силами SQLite
// (18.1 «падение в любой точке — обычный откат»), индекс остаётся на
// прежнем поколении, а сервис продолжает нормально работать со свежим
// контекстом.
func TestReindexCancellationLeavesStoreIntact(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	genBefore, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	mustWrite(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"), `
Функция Помощь() Экспорт
	Возврат "v2";
КонецФункции
`)
	touchFuture(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"))

	cctx, cancel := context.WithCancel(ctx)
	testMidRunHook = func() { cancel() }
	t.Cleanup(func() { testMidRunHook = nil })

	_, err = svc.Reindex(cctx, ModeIncremental, "")
	if err == nil {
		t.Fatal("Reindex с отменённым контекстом вернул nil, want ошибку отмены")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}

	genAfter, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status после отмены: %v", err)
	}
	if genAfter.GenerationNumber != genBefore.GenerationNumber {
		t.Errorf("GenerationNumber изменился на отменённом инкременте: было %d, стало %d",
			genBefore.GenerationNumber, genAfter.GenerationNumber)
	}
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() после отмены = %v, want пусто", bad)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Сервис остаётся рабочим: обычный инкремент со свежим контекстом
	// проходит без следов отменённой попытки.
	testMidRunHook = nil
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex после отмены: %v", err)
	}
	genFinal, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if genFinal.GenerationNumber != genBefore.GenerationNumber+1 {
		t.Errorf("GenerationNumber = %d, want %d (один успешный инкремент после отменённого)",
			genFinal.GenerationNumber, genBefore.GenerationNumber+1)
	}
}
