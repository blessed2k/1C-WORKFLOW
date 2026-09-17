package index

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestIncrementalFallbackToFullReResolve — §18.4: превышение порога fallback
// (здесь — принудительно занижен до 0, чтобы гарантированно сработать) уводит
// инкремент на полный re-resolve компонента вместо точечного affected set;
// результат обязан остаться корректным (Validate пуст, generation +1) — сама
// ветка иначе никаким другим тестом не задета.
func TestIncrementalFallbackToFullReResolve(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	// Config.fill() (вызванный NewService) не даёт задать буквальный 0 —
	// 0 читается как «умолчание» (тот же приём, что store.Options), поэтому
	// порог занижается уже ПОСЛЕ конструктора: любой affected set > 0 уйдёт
	// в fallback.
	svc.cfg.FallbackMinKeys = 0
	svc.cfg.FallbackPct = 0

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

	res, err := svc.Reindex(ctx, ModeIncremental, "")
	if err != nil {
		t.Fatalf("Reindex(incremental, fallback): %v", err)
	}
	if len(res.Components) != 1 || res.Components[0].FilesChanged == 0 {
		t.Errorf("FilesChanged = %+v, want > 0", res.Components)
	}

	genAfter, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if genAfter.GenerationNumber != genBefore.GenerationNumber+1 {
		t.Errorf("GenerationNumber = %d, want %d", genAfter.GenerationNumber, genBefore.GenerationNumber+1)
	}
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() после fallback = %v, want пусто", bad)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}
