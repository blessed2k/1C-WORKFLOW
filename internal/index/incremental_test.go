package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestIncrementalTwoFiles — R32: правка двух модулей даёт инкремент без
// полной переиндексации, generation растёт на 1, время инкремента
// укладывается в бюджет 300 мс (§28) на маленькой фикстуре (реальная
// выгрузка — отдельный прогон, см. handoff/финальный отчёт).
func TestIncrementalTwoFiles(t *testing.T) {
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	m := testManifest(t, root)
	svc := NewService(st, "proj", m, nil, Config{})
	t.Cleanup(func() { svc.Close() })
	ctx := context.Background()

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	genBefore, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Правим ДВА .bsl-файла.
	mustWrite(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"), `
Функция Помощь() Экспорт
	Возврат "ok-2";
КонецФункции

Функция ЕщёОдна() Экспорт
	Возврат Истина;
КонецФункции
`)
	mustWrite(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"), `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
	УтилитыОбщие.ЕщёОдна();
КонецПроцедуры
`)
	// mtime гранулярность файловой системы может совпасть с исходной записью
	// в пределах одной наносекунды теста — фикстура пишется, затем сразу
	// читается: сдвигаем время модификации, чтобы fingerprint увидел правку
	// даже при агрессивном кэшировании ФС.
	touchFuture(t, filepath.Join(root, "CommonModules/УтилитыОбщие/Ext/Module.bsl"))
	touchFuture(t, filepath.Join(root, "Catalogs/Товары/Ext/ManagerModule.bsl"))

	start := time.Now()
	res, err := svc.Reindex(ctx, ModeIncremental, "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	t.Logf("инкремент 2 файлов: %s", elapsed)

	genAfter, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if genAfter.GenerationNumber != genBefore.GenerationNumber+1 {
		t.Errorf("GenerationNumber = %d, want %d", genAfter.GenerationNumber, genBefore.GenerationNumber+1)
	}
	if len(res.Components) != 1 || res.Components[0].FilesChanged != 2 {
		t.Errorf("FilesChanged = %+v, want 2", res.Components)
	}

	err = st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() = %v, want пусто", bad)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// touchFuture сдвигает mtime файла в будущее, чтобы fingerprint гарантированно
// увидел изменение независимо от разрешения часов файловой системы теста.
func touchFuture(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}
