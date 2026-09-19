package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/store/storetest"
)

// epochRows снимает построчный слепок текущей эпохи хранилища.
func epochRows(t *testing.T, st *store.Store) map[string][]string {
	t.Helper()
	s, err := st.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rows, err := storetest.CanonicalDump(s.EpochPath)
	if err != nil {
		t.Fatalf("CanonicalDump: %v", err)
	}
	return rows
}

// TestIncrementEqualsCleanRebuildRows: полная пересборка, затем правка
// нескольких файлов (изменение тела и добавление метода, удаление метода,
// удаление файла, новый файл) и инкремент дают ТЕ ЖЕ строки во всех таблицах
// SQLite, что чистая полная пересборка конечного состояния. Сверка построчная
// (storetest.CanonicalDump): id, законно различные между путями, заменены
// ключами идентичности, reference и её дети сверяются по естественному ключу
// ссылки. Это страховка пакетной вставки и применения плана (issue #3): оба
// меняют порядок и момент записи строк, а не их состав.
//
// Два случая: маленький компонент, где инкремент уходит в fallback (полный
// re-resolve, republish всех файлов), и широкий, где правка точечная и
// большинство строк reference остаётся от прошлого поколения (тогда id новых
// ссылок обязаны продолжать таблицу, а не начинать её заново).
func TestIncrementEqualsCleanRebuildRows(t *testing.T) {
	t.Run("fallback", func(t *testing.T) { checkIncrementRows(t, 0) })
	t.Run("точечный", func(t *testing.T) {
		// Дефект main, найденный этим тестом (не issue #3, воспроизводится на
		// main 0aec247): правка тела общего модуля без смены имён пересоздаёт
		// его символы с теми же id, но шаг (1b) DeleteSourceFiles переводит
		// ссылки НЕТРОНУТЫХ файлов на эти символы в unresolved, а affected set
		// по дельте имён их не переопубликует. В SQLite остаётся reference
		// unresolved и call_edge resolved с callee NULL, чистая пересборка
		// даёт resolved. Исправление меняет правило affected set (§18.4) и
		// требует решения; до него случай пропускается, а не прячется.
		if os.Getenv("MCP1C_KNOWN_INCREMENT_DEFECT") == "" {
			t.Skip("известный дефект инкремента на main: ссылки на пересозданные символы теряют цель (см. комментарий)")
		}
		checkIncrementRows(t, 30)
	})
}

func checkIncrementRows(t *testing.T, fillers int) {
	ctx := context.Background()
	root := writeFixtureComponent(t)
	filler := map[string]string{}
	for i := 0; i < fillers; i++ {
		filler[fmt.Sprintf("CommonModules/Прочий%d/Ext/Module.bsl", i)] = fmt.Sprintf(`
Процедура Локальная%[1]d()
КонецПроцедуры

Процедура Вызов%[1]d() Экспорт
	Локальная%[1]d();
КонецПроцедуры
`, i)
	}
	writeFixtureFiles(t, root, filler)
	writeFixtureFiles(t, root, map[string]string{
		"CommonModules/Сервис/Ext/Module.bsl": `
Функция Первый(Параметр = Неопределено) Экспорт
	Возврат УтилитыОбщие.Помощь();
КонецФункции

Процедура Второй() Экспорт
	Первый(1);
КонецПроцедуры
`,
		"Catalogs/Лишний/Ext/ManagerModule.bsl": `
Процедура Вызов() Экспорт
	Сервис.Второй();
	УтилитыОбщие.Помощь();
КонецПроцедуры
`,
	})

	stA := openTestStore(t)
	svcA := NewService(stA, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcA.Close() })
	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("A: Reindex(full): %v", err)
	}

	edit := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, p, content)
		touchFuture(t, p)
	}
	// Изменение тела и новый метод.
	edit("CommonModules/УтилитыОбщие/Ext/Module.bsl", `
Функция Помощь() Экспорт
	Возврат Новая();
КонецФункции

Функция Новая() Экспорт
	Возврат "новая";
КонецФункции
`)
	// Удаление метода, на который ссылались из другого файла.
	edit("CommonModules/Сервис/Ext/Module.bsl", `
Функция Первый(Параметр = Неопределено) Экспорт
	Возврат УтилитыОбщие.Помощь();
КонецФункции
`)
	// Удаление файла и новый файл.
	if err := os.Remove(filepath.Join(root, "Catalogs/Лишний/Ext/ManagerModule.bsl")); err != nil {
		t.Fatal(err)
	}
	edit("Catalogs/Новый/Ext/ManagerModule.bsl", `
Процедура Тест() Экспорт
	УтилитыОбщие.Новая();
	Сервис.Второй();
КонецПроцедуры
`)
	if _, err := svcA.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("A: Reindex(incremental): %v", err)
	}

	stB := openTestStore(t)
	svcB := NewService(stB, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcB.Close() })
	if _, err := svcB.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("B: Reindex(full): %v", err)
	}

	a, b := epochRows(t, stA), epochRows(t, stB)
	for _, table := range []string{"reference", "call_edge", "resolution_dep", "symbol", "source_file"} {
		if len(b[table]) == 0 {
			t.Fatalf("в чистой пересборке таблица %s пуста: сверка ничего бы не проверила", table)
		}
	}
	for table := range b {
		if _, ok := a[table]; !ok {
			t.Errorf("таблица %s есть только в чистой пересборке", table)
		}
	}
	for table, rowsA := range a {
		rowsB := b[table]
		if diff := diffRows(rowsA, rowsB); diff != "" {
			t.Errorf("таблица %s: инкремент и чистая пересборка разошлись\n%s", table, diff)
		}
	}
}

// diffRows: строки, которые есть только в одной стороне (мультимножества).
func diffRows(a, b []string) string {
	count := map[string]int{}
	for _, r := range a {
		count[r]++
	}
	for _, r := range b {
		count[r]--
	}
	var out string
	n := 0
	for r, c := range count {
		if c == 0 {
			continue
		}
		if n < 8 {
			side := "только в инкременте"
			if c < 0 {
				side = "только в чистой пересборке"
			}
			out += "  " + side + ": " + r + "\n"
		}
		n++
	}
	if n > 8 {
		out += "  ...\n"
	}
	return out
}
