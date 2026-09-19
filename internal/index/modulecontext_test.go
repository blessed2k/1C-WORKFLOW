package index

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestXMLPropsChangePreservesSymbolIdentity: правка XML-свойств
// общего модуля (module_context) не трогает identity его Module.bsl —
// source_file этого файла и id его символов не меняются, а зависимая
// ссылка из ManagerModule.bsl переразрешается (её файл republish-ится
// заново — id меняется, потому что store не даёт UPDATE отдельной строки
// reference, только delete+insert файла целиком, см. publish.go).
func TestXMLPropsChangePreservesSymbolIdentity(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	bslID, symID, callerFileIDBefore := idsBefore(t, ctx, st)

	// Правим ТОЛЬКО XML общего модуля: снимаем Server — вызывающий
	// (ManagerModule, серверный контекст по виду модуля) теряет доступ к
	// цели (resolve.commonModuleAvailability -> contextAvailable), меняется
	// и confidence (ConfidenceContextMismatch), и diagnostics ссылки.
	mustWrite(t, filepath.Join(root, "CommonModules/УтилитыОбщие.xml"), `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="11111111-1111-1111-1111-111111111111">
    <Properties>
      <Name>УтилитыОбщие</Name>
      <Global>true</Global>
      <ClientManagedApplication>true</ClientManagedApplication>
      <Server>false</Server>
      <ServerCall>true</ServerCall>
    </Properties>
  </CommonModule>
</MetaDataObject>`)
	touchFuture(t, filepath.Join(root, "CommonModules/УтилитыОбщие.xml"))

	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental после правки XML): %v", err)
	}

	bslIDAfter, symIDAfter, callerFileIDAfter := idsBefore(t, ctx, st)

	if bslIDAfter != bslID {
		t.Errorf("source_file id Module.bsl изменился: было %d, стало %d — файл не должен был переиндексироваться", bslID, bslIDAfter)
	}
	if symIDAfter != symID {
		t.Errorf("узел символа Помощь изменился: было %d, стало %d — symbol_uid обязан сохраниться", symID, symIDAfter)
	}
	if callerFileIDAfter == callerFileIDBefore {
		t.Errorf("source_file id ManagerModule.bsl не изменился (%d) — зависимая ссылка должна была переразрешиться (republish файла)", callerFileIDAfter)
	}

	err := st.Read(ctx, func(tx *store.ReadTx) error {
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

func idsBefore(t *testing.T, ctx context.Context, st *store.Store) (bslFileID, symNodeID, callerFileID int64) {
	t.Helper()
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		var ok bool
		var err error
		bslFileID, ok, err = tx.SourceFileID("cfg", "CommonModules/УтилитыОбщие/Ext/Module.bsl")
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("Module.bsl не найден в индексе")
		}
		symUID := domain.NewSymbolUID("proj", "cfg", "CommonModules/УтилитыОбщие/Ext/Module.bsl", "помощь")
		symNodeID, ok, err = tx.NodeID(symbolIdentityKey(symUID))
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("символ Помощь не найден в индексе")
		}
		callerFileID, ok, err = tx.SourceFileID("cfg", "Catalogs/Товары/Ext/ManagerModule.bsl")
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("ManagerModule.bsl не найден в индексе")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return bslFileID, symNodeID, callerFileID
}
