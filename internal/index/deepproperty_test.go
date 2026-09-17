package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// snapshotOf строит слепок компонента "cfg" из уже открытого Service —
// белый ящик (тест того же пакета): svc.corpora доступен напрямую, второго
// SQL-слоя вне store для этого заводить не нужно (ревью явно просит
// сравнивать ДО публикации, в памяти).
func snapshotOf(svc *Service) map[string]deepFileFacts {
	corpus := svc.corpora["cfg"]
	return snapshotCorpus("proj", "cfg", domain.BaseLayer("cfg"), corpus)
}

// TestIncrementEqualsCleanRebuildDeepFixture — усиленная версия property-
// теста (ревью: прежняя версия сравнивала только множество identity-key и
// hash блобов, не содержимое строк). Путь A: часть файлов индексируется
// full, затем остальные — incremental (реальное добавление новых файлов).
// Путь B: та же итоговая файловая структура — один full с нуля. Сравниваются
// ПОЛНЫЕ слепки в памяти (символы, ссылки с их Resolution/Confidence/Span,
// объекты метаданных) — не только присутствие ключей.
func TestIncrementEqualsCleanRebuildDeepFixture(t *testing.T) {
	ctx := context.Background()
	root := writeFixtureComponent(t)

	stA := openTestStore(t)
	svcA := NewService(stA, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcA.Close() })
	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("A: Reindex(full, batch 1): %v", err)
	}

	// Batch 2: новый каталог, объектный модуль которого зовёт уже
	// проиндексированный общий модуль — упражняет и добавление новых
	// символов, и ссылку на существующие через инкремент.
	writeFixtureFiles(t, root, map[string]string{
		"Catalogs/Заказы.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Catalog uuid="33333333-3333-3333-3333-333333333333">
    <Properties>
      <Name>Заказы</Name>
    </Properties>
  </Catalog>
</MetaDataObject>`,
		"Catalogs/Заказы/Ext/ObjectModule.bsl": `
Процедура ПриЗаписи() Экспорт
	УтилитыОбщие.Помощь();
КонецПроцедуры
`,
	})
	if _, err := svcA.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("A: Reindex(incremental, batch 2): %v", err)
	}

	stB := openTestStore(t)
	svcB := NewService(stB, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcB.Close() })
	if _, err := svcB.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("B: Reindex(full, всё сразу): %v", err)
	}

	snapA, snapB := snapshotOf(svcA), snapshotOf(svcB)
	if len(snapA) == 0 {
		t.Fatal("snapshotOf(svcA) пуст — тест ничего не проверяет")
	}
	if diffs := diffSnapshots(snapA, snapB); len(diffs) > 0 {
		t.Errorf("инкремент != чистая пересборка (%d расхождений):\n%s", len(diffs), strings.Join(diffs, "\n---\n"))
	}
}

// TestIncrementEqualsCleanRebuildDeepRealSample — та же проверка на
// подвыборке реальной выгрузки (Catalogs/Номенклатура — самостоятельный,
// не весь ut_demo: дёшево и быстро, не про 4 минуты полного индекса).
func TestIncrementEqualsCleanRebuildDeepRealSample(t *testing.T) {
	dumpRoot := realDumpRoot(t)
	// Владелец модуля (bsl.ClassifyModule) выводится из ПУТИ: первый
	// сегмент обязан быть коллекцией выгрузки ("Catalogs", ...), поэтому
	// подвыборка копируется С сохранением "Catalogs/Номенклатура/..." —
	// плоская копия одних только внутренностей сломала бы OwnerType/OwnerName
	// молча (bsl.ClassifyModule просто не нашёл бы коллекцию в parts[0]).
	relRoot := filepath.Join("Catalogs", "Номенклатура")
	src := filepath.Join(dumpRoot, relRoot)
	if _, err := os.Stat(src); err != nil {
		t.Skipf("в выгрузке нет Catalogs/Номенклатура: %v", err)
	}

	full := t.TempDir()
	copyTree(t, src, filepath.Join(full, relRoot))
	allFiles, err := discoverComponent(full, nil, nil)
	if err != nil {
		t.Fatalf("discoverComponent: %v", err)
	}
	if len(allFiles) < 4 {
		t.Skipf("подвыборка слишком мала (%d файлов)", len(allFiles))
	}
	half := len(allFiles) / 2

	rootA := t.TempDir()
	for _, f := range allFiles[:half] {
		copyOneFile(t, full, rootA, f.relPath)
	}

	stA := openTestStore(t)
	svcA := NewService(stA, "proj", testManifest(t, rootA), nil, Config{})
	t.Cleanup(func() { svcA.Close() })
	if _, err := svcA.Reindex(context.Background(), ModeFull, ""); err != nil {
		t.Fatalf("A: Reindex(full, batch 1): %v", err)
	}

	for _, f := range allFiles[half:] {
		copyOneFile(t, full, rootA, f.relPath)
	}
	if _, err := svcA.Reindex(context.Background(), ModeIncremental, ""); err != nil {
		t.Fatalf("A: Reindex(incremental, batch 2): %v", err)
	}

	stB := openTestStore(t)
	svcB := NewService(stB, "proj", testManifest(t, full), nil, Config{})
	t.Cleanup(func() { svcB.Close() })
	if _, err := svcB.Reindex(context.Background(), ModeFull, ""); err != nil {
		t.Fatalf("B: Reindex(full, всё сразу): %v", err)
	}

	snapA, snapB := snapshotOf(svcA), snapshotOf(svcB)
	if len(snapA) == 0 {
		t.Fatal("snapshotOf(svcA) пуст — тест ничего не проверяет")
	}
	if diffs := diffSnapshots(snapA, snapB); len(diffs) > 0 {
		t.Errorf("инкремент != чистая пересборка на реальной выгрузке (%d расхождений, показаны первые 5):\n%s",
			len(diffs), strings.Join(firstNStr(diffs, 5), "\n---\n"))
	}
}

func firstNStr(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// copyOneFile копирует один файл rel из srcRoot в dstRoot, создавая
// недостающие каталоги.
func copyOneFile(t *testing.T, srcRoot, dstRoot, rel string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(srcRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("ReadFile %s: %v", rel, err)
	}
	dst := filepath.Join(dstRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", dst, err)
	}
}
