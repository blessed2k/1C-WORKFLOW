package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	t.Run("fallback", func(t *testing.T) { checkIncrementRows(t, 0, moduleEdits) })
	t.Run("точечный", func(t *testing.T) {
		// Правка тела общего модуля без смены имён переопубликует только сам
		// модуль: символы пересоздаются с прежними id (identity стабильна), а
		// ссылки НЕТРОНУТЫХ файлов на них обязаны остаться resolved и с тем же
		// callee (issue #10, ADR-037).
		checkIncrementRows(t, 30, moduleEdits)
	})
	t.Run("узлы XML", func(t *testing.T) {
		// Тот же дефект не только у ссылок: на пересоздаваемые узлы указывают
		// подписка на событие (обработчик в общем модуле) и запрос к
		// справочнику. Правка XML справочника и тела модуля, все указывающие
		// файлы нетронуты (ADR-037).
		checkIncrementRows(t, 30, nodeEdits)
	})
}

// incrementScenario — исходные файлы сверх базовой фикстуры и правки перед
// инкрементом.
type incrementScenario struct {
	seed map[string]string
	// edit применяет правки: write пишет файл со сдвигом mtime, remove удаляет.
	edit func(write func(rel, content string), remove func(rel string))
	// mustHave — подстроки, каждая из которых обязана встретиться в строках
	// чистой пересборки: иначе сценарий не создал то, что проверяет.
	mustHave []string
}

var moduleEdits = incrementScenario{
	seed: map[string]string{
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
	},
	edit: func(write func(rel, content string), remove func(rel string)) {
		// Изменение тела и новый метод.
		write("CommonModules/УтилитыОбщие/Ext/Module.bsl", `
Функция Помощь() Экспорт
	Возврат Новая();
КонецФункции

Функция Новая() Экспорт
	Возврат "новая";
КонецФункции
`)
		// Удаление метода, на который ссылались из другого файла.
		write("CommonModules/Сервис/Ext/Module.bsl", `
Функция Первый(Параметр = Неопределено) Экспорт
	Возврат УтилитыОбщие.Помощь();
КонецФункции
`)
		// Удаление файла и новый файл.
		remove("Catalogs/Лишний/Ext/ManagerModule.bsl")
		write("Catalogs/Новый/Ext/ManagerModule.bsl", `
Процедура Тест() Экспорт
	УтилитыОбщие.Новая();
	Сервис.Второй();
КонецПроцедуры
`)
	},
	mustHave: []string{"rel_path=Catalogs/Товары/Ext/ManagerModule.bsl"},
}

var nodeEdits = incrementScenario{
	seed: map[string]string{
		"EventSubscriptions/ПроверкаТоваров.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<EventSubscription uuid="33333333-3333-3333-3333-333333333333">
		<Properties>
			<Name>ПроверкаТоваров</Name>
			<Source>
				<v8:Type>cfg:CatalogObject.Товары</v8:Type>
			</Source>
			<Event>BeforeWrite</Event>
			<Handler>CommonModule.УтилитыОбщие.Помощь</Handler>
		</Properties>
	</EventSubscription>
</MetaDataObject>`,
		"CommonModules/Отчеты/Ext/Module.bsl": `
Функция Товары() Экспорт
	Запрос = Новый Запрос;
	Запрос.Текст = "ВЫБРАТЬ Товары.Ссылка ИЗ Справочник.Товары КАК Товары";
	Возврат Запрос.Выполнить();
КонецФункции
`,
	},
	edit: func(write func(rel, content string), remove func(rel string)) {
		write("Catalogs/Товары.xml", `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Catalog uuid="22222222-2222-2222-2222-222222222222">
    <Properties>
      <Name>Товары</Name>
      <Comment>правка свойства</Comment>
    </Properties>
  </Catalog>
</MetaDataObject>`)
		write("CommonModules/УтилитыОбщие/Ext/Module.bsl", `
Функция Помощь() Экспорт
	Возврат "ok, но иначе";
КонецФункции
`)
	},
	mustHave: []string{
		"object_id=node:" + metadataObjectIdentityKey("cfg", "Catalog", "товары"),
		"handler_symbol_id=node:",
	},
}

func checkIncrementRows(t *testing.T, fillers int, sc incrementScenario) {
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
	writeFixtureFiles(t, root, sc.seed)

	stA := openTestStore(t)
	svcA := NewService(stA, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcA.Close() })
	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("A: Reindex(full): %v", err)
	}

	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, p, content)
		touchFuture(t, p)
	}
	remove := func(rel string) {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	sc.edit(write, remove)
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
	for _, sub := range sc.mustHave {
		found := false
		for _, rows := range b {
			for _, r := range rows {
				if strings.Contains(r, sub) {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("в чистой пересборке нет строки с %q: сценарий не создал проверяемый факт", sub)
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
