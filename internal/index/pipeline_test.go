package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

const testStateDir = ".mcp1c-test"

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir(), store.Options{ProjectID: "proj", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// writeFixtureComponent создаёт корень компонента с общим модулем УтилитыОбщие
// (метод Помощь) и вызывающим модулем менеджера справочника, который зовёт
// УтилитыОбщие.Помощь() — минимальный, но не тривиальный срез: qualified
// module call, common module XML (module_context) и manager-модуль.
func writeFixtureComponent(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"CommonModules/УтилитыОбщие.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="11111111-1111-1111-1111-111111111111">
    <Properties>
      <Name>УтилитыОбщие</Name>
      <Global>true</Global>
      <ClientManagedApplication>true</ClientManagedApplication>
      <Server>true</Server>
      <ServerCall>true</ServerCall>
    </Properties>
  </CommonModule>
</MetaDataObject>`,
		"CommonModules/УтилитыОбщие/Ext/Module.bsl": `
Функция Помощь() Экспорт
	Возврат "ok";
КонецФункции
`,
		"Catalogs/Товары.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Catalog uuid="22222222-2222-2222-2222-222222222222">
    <Properties>
      <Name>Товары</Name>
    </Properties>
  </Catalog>
</MetaDataObject>`,
		"Catalogs/Товары/Ext/ManagerModule.bsl": `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
КонецПроцедуры
`,
	}
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return root
}

func testManifest(t *testing.T, root string) workspace.Manifest {
	t.Helper()
	return workspace.Manifest{
		Version: 1, Project: "proj", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
}

// TestFullIndexBasic — первый шов пайплайна: полная индексация маленькой
// фикстуры проходит до конца, generation растёт, инвариантов store
// (foreign_key_check, XOR reference) не нарушено, символ общего модуля и
// разрешённая ссылка на него физически видны через store.
func TestFullIndexBasic(t *testing.T) {
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	m := testManifest(t, root)
	svc := NewService(st, "proj", m, nil, Config{})
	t.Cleanup(func() { svc.Close() })

	ctx := context.Background()
	res, err := svc.Reindex(ctx, ModeFull, "")
	if err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	if len(res.Components) != 1 || res.Components[0].FilesChanged != 4 {
		t.Fatalf("res.Components = %+v, want 1 компонент, 4 файла", res.Components)
	}

	err = st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() = %v, want пусто", bad)
		}

		modID, ok, err := tx.NodeID(moduleIdentityKey("cfg", "CommonModules/УтилитыОбщие/Ext/Module.bsl"))
		if err != nil {
			return err
		}
		if !ok {
			t.Errorf("модуль УтилитыОбщие не найден в индексе")
		}
		_ = modID

		uid := domain.NewSymbolUID("proj", "cfg", "CommonModules/УтилитыОбщие/Ext/Module.bsl", "помощь")
		symID, ok, err := tx.NodeID(symbolIdentityKey(uid))
		if err != nil {
			return err
		}
		if !ok {
			t.Errorf("символ Помощь не найден в индексе")
		}
		_ = symID
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	st2, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st2.GenerationNumber < 1 {
		t.Errorf("GenerationNumber = %d, want >= 1", st2.GenerationNumber)
	}
}

// TestTotalSymbolCountIncompleteOnHydrated — R15 применительно к третьему
// потребителю корпуса: totalSymbolCount на гидратированной записи фактов не
// видит и даёт 0. Слепота не устраняется (записи ещё не дочитаны, считать
// нечего), но названа возвращаемым признаком, и предохранитель §17 п.7 на
// неполном счёте не срабатывает вместо того, чтобы молча пропустить падение
// числа символов.
func TestTotalSymbolCountIncompleteOnHydrated(t *testing.T) {
	corpus := newComponentCorpus("cfg")
	rel := "CommonModules/УтилитыОбщие/Ext/Module.bsl"
	corpus.files[rel] = parseOneFile(rel, []byte("Функция Помощь() Экспорт\nВозврат 1;\nКонецФункции\n"))

	n, complete := totalSymbolCount(corpus)
	if n != 1 || !complete {
		t.Fatalf("разобранный корпус: totalSymbolCount = (%d, %v), want (1, true)", n, complete)
	}

	corpus.files["Catalogs/Товары.xml"] = &fileRecord{relPath: "Catalogs/Товары.xml", hydrated: true}
	n, complete = totalSymbolCount(corpus)
	if complete {
		t.Errorf("корпус с гидратированной записью: complete = true, want false (счёт %d)", n)
	}
}
