package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// bigModuleBSL строит текст общего модуля с n экспортными процедурами —
// нужен объём символов (>= 20), чтобы порог §17 п.7 вообще имел смысл
// проверять (на единицах символов «порядок величины» ничего не значит).
func bigModuleBSL(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "Процедура Метод%d() Экспорт\n\tВозврат;\nКонецПроцедуры\n\n", i)
	}
	return b.String()
}

// TestSymbolCountDropAbortsIncrement — §17 п.7: инкремент, роняющий число
// символов компонента на порядок, не публикуется — транзакция отменяется
// (ROLLBACK), а не тихо принимает подозрительно урезанный корпус (характерно
// для недокачанного DumpConfigToFiles).
func TestSymbolCountDropAbortsIncrement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bslPath := filepath.Join(root, "CommonModules/Большой/Ext/Module.bsl")
	if err := os.MkdirAll(filepath.Dir(bslPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(bslPath, []byte(bigModuleBSL(25)), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st := openTestStore(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	genBefore, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Роняем модуль до одной процедуры — 25 -> 1, явно на порядок.
	mustWrite(t, bslPath, bigModuleBSL(1))
	touchFuture(t, bslPath)

	_, err = svc.Reindex(ctx, ModeIncremental, "")
	if err == nil {
		t.Fatal("Reindex(incremental) с падением символов на порядок вернул nil, want ошибку")
	}

	genAfter, statusErr := st.Status(ctx)
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}
	if genAfter.GenerationNumber != genBefore.GenerationNumber {
		t.Errorf("GenerationNumber изменился при отменённой публикации: было %d, стало %d",
			genBefore.GenerationNumber, genAfter.GenerationNumber)
	}
	readErr := st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() после отменённой публикации = %v, want пусто", bad)
		}
		return nil
	})
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
}
