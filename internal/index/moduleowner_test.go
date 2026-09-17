package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// moduleRowByPath отдаёт строку модуля, чей аспект кода лежит в файле
// relPath. Читается публичной читалкой store (ModuleByFile), не своим SQL:
// тест обязан видеть колонку тем же способом, каким её увидит потребитель
// индекса.
func moduleRowByPath(t *testing.T, ctx context.Context, st *store.Store, relPath string) store.ModuleRow {
	t.Helper()
	var row store.ModuleRow
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		fileID, ok, err := tx.SourceFileID("cfg", relPath)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("файл %s не найден в индексе", relPath)
		}
		r, ok, err := tx.ModuleByFile(fileID)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("модуль файла %s не найден в индексе", relPath)
		}
		row = r
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return row
}

// moduleOwnerByPath — только owner_object_id того же модуля.
func moduleOwnerByPath(t *testing.T, ctx context.Context, st *store.Store, relPath string) int64 {
	t.Helper()
	return moduleRowByPath(t, ctx, st, relPath).OwnerObjectID
}

// objectNodeID отдаёт id узла объекта метаданных по его виду и
// нормализованному имени.
func objectNodeID(t *testing.T, ctx context.Context, st *store.Store, mtype, nameNorm string) int64 {
	t.Helper()
	var id int64
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		var ok bool
		var err error
		id, ok, err = tx.NodeID(metadataObjectIdentityKey("cfg", mtype, nameNorm))
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("объект %s.%s не найден в индексе", mtype, nameNorm)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return id
}

// TestModuleOwnerFilledOnPublish — история 8 (G06): у модуля, чей владелец
// есть в индексе, module.owner_object_id указывает на объект метаданных.
// Проверяются обе точки вызова EnsureModule: общий модуль (ветка
// CommonModule в publishMetadataObject плюс его же Module.bsl) и обычный
// модуль кода (publishModuleSymbols).
func TestModuleOwnerFilledOnPublish(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	cases := []struct {
		module   string
		mtype    string
		nameNorm string
	}{
		{"CommonModules/УтилитыОбщие/Ext/Module.bsl", "CommonModule", "утилитыобщие"},
		{"Catalogs/Товары/Ext/ManagerModule.bsl", "Catalog", "товары"},
	}
	for _, c := range cases {
		want := objectNodeID(t, ctx, st, c.mtype, c.nameNorm)
		if got := moduleOwnerByPath(t, ctx, st, c.module); got != want {
			t.Errorf("owner_object_id модуля %s = %d, want %d (%s.%s)", c.module, got, want, c.mtype, c.nameNorm)
		}
	}
}

// writeOwnerAfterModuleFixture — компонент, в котором XML владельца лежит
// в файле, сортирующемся ПОСЛЕ его же Module.bsl: имя объекта берётся из
// <Name>, а не из имени файла, поэтому «Catalogs/яПорядок.xml» описывает тот
// же справочник Порядок, чей модуль объекта лежит в «Catalogs/Порядок/...».
// Проход 1 publishFiles идёт по отсортированному списку файлов, значит
// модуль публикуется РАНЬШЕ своего владельца — тот самый порядок из истории 9.
func writeOwnerAfterModuleFixture(t *testing.T) (root, modulePath, ownerXMLPath string) {
	t.Helper()
	root = t.TempDir()
	modulePath = "Catalogs/Порядок/Ext/ObjectModule.bsl"
	ownerXMLPath = "Catalogs/яПорядок.xml"
	mustMkdirAll(t, filepath.Join(root, filepath.FromSlash("Catalogs/Порядок/Ext")))
	mustWrite(t, filepath.Join(root, filepath.FromSlash(modulePath)), `
Процедура ПередЗаписью(Отказ) Экспорт
КонецПроцедуры
`)
	mustWrite(t, filepath.Join(root, filepath.FromSlash(ownerXMLPath)), `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Catalog uuid="33333333-3333-3333-3333-333333333333">
    <Properties>
      <Name>Порядок</Name>
    </Properties>
  </Catalog>
</MetaDataObject>`)
	return root, modulePath, ownerXMLPath
}

// TestModuleOwnerResolvedWhenOwnerPublishedLater — история 9 (G06): владелец
// резолвится и тогда, когда его объект метаданных публикуется в транзакции
// позже своего Module.bsl.
func TestModuleOwnerResolvedWhenOwnerPublishedLater(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root, modulePath, ownerXMLPath := writeOwnerAfterModuleFixture(t)

	// Предпосылка теста: publishFiles обходит файлы в лексикографическом
	// порядке (sort.Strings в pipeline.go). Если она перестанет выполняться,
	// тест обязан сказать это вслух, а не тихо проверять другой сценарий.
	if !(modulePath < ownerXMLPath) {
		t.Fatalf("фикстура не воспроизводит порядок: %q не раньше %q", modulePath, ownerXMLPath)
	}

	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	want := objectNodeID(t, ctx, st, "Catalog", "порядок")
	if got := moduleOwnerByPath(t, ctx, st, modulePath); got != want {
		t.Errorf("owner_object_id модуля %s = %d, want %d (Catalog.порядок)", modulePath, got, want)
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
}

// TestModulesWithoutOwnerStayNull — история 8, вторая её половина: у модулей
// приложения, сеанса и внешнего соединения объекта-владельца в конфигурации
// нет, и пустая колонка для них — правильный ответ. Строка модуля при этом
// обязана существовать: «нет владельца» не равно «модуль не опубликован».
func TestModulesWithoutOwnerStayNull(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	mustMkdirAll(t, filepath.Join(root, "Ext"))
	ownerless := map[string]string{
		"Ext/ManagedApplicationModule.bsl": "application",
		"Ext/SessionModule.bsl":            "session",
		"Ext/ExternalConnectionModule.bsl": "external-connection",
	}
	for rel := range ownerless {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(rel)), "\nПроцедура Служебная()\nКонецПроцедуры\n")
	}

	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	for rel, wantKind := range ownerless {
		row := moduleRowByPath(t, ctx, st, rel)
		if row.Kind != wantKind {
			t.Errorf("kind модуля %s = %q, want %q", rel, row.Kind, wantKind)
		}
		if row.OwnerObjectID != 0 {
			t.Errorf("owner_object_id модуля %s = %d, want пусто: владельца-объекта у него нет", rel, row.OwnerObjectID)
		}
	}
}

// TestModuleOwnerSurvivesIncrementalRepublish — G06 на инкременте: правится
// ТОЛЬКО код модуля, XML владельца в транзакцию не попадает, и владелец
// обязан остаться. EnsureModule — безусловный upsert по всем колонкам,
// поэтому перепубликация модуля без резолва владельца обнулила бы колонку.
func TestModuleOwnerSurvivesIncrementalRepublish(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	wantCommon := objectNodeID(t, ctx, st, "CommonModule", "утилитыобщие")
	wantCatalog := objectNodeID(t, ctx, st, "Catalog", "товары")
	if got := moduleOwnerByPath(t, ctx, st, "CommonModules/УтилитыОбщие/Ext/Module.bsl"); got != wantCommon {
		t.Fatalf("предпосылка теста не выполнена: владелец общего модуля после полного индекса = %d, want %d", got, wantCommon)
	}

	edits := map[string]string{
		"CommonModules/УтилитыОбщие/Ext/Module.bsl": `
// правка
Функция Помощь() Экспорт
	Возврат "ok2";
КонецФункции
`,
		"Catalogs/Товары/Ext/ManagerModule.bsl": `
Процедура Тест() Экспорт
	УтилитыОбщие.Помощь();
	УтилитыОбщие.Помощь();
КонецПроцедуры
`,
	}
	for rel, content := range edits {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		mustWrite(t, abs, content)
		touchFuture(t, abs)
	}

	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}

	if got := moduleOwnerByPath(t, ctx, st, "CommonModules/УтилитыОбщие/Ext/Module.bsl"); got != wantCommon {
		t.Errorf("после инкремента owner_object_id общего модуля = %d, want %d", got, wantCommon)
	}
	if got := moduleOwnerByPath(t, ctx, st, "Catalogs/Товары/Ext/ManagerModule.bsl"); got != wantCatalog {
		t.Errorf("после инкремента owner_object_id модуля менеджера = %d, want %d", got, wantCatalog)
	}
}

// TestModuleOwnerFilledForServiceCollections — история 8, полнота словаря
// ownerTypeToMType: коллекции выгрузки, чьи модули есть в реальной
// конфигурации (хранилища настроек, web- и integration-сервисы, критерии
// отбора), обязаны находить своего владельца. Ожидаемые mtype взяты из
// имени корневого элемента XML выгрузки (парсер метаданных берёт mtype
// именно оттуда), не из словаря под тестом.
func TestModuleOwnerFilledForServiceCollections(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	object := func(collection, mtype, name, uuid, modulePath string) {
		mustMkdirAll(t, filepath.Join(root, collection))
		mustWrite(t, filepath.Join(root, collection, name+".xml"), `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <`+mtype+` uuid="`+uuid+`">
    <Properties>
      <Name>`+name+`</Name>
    </Properties>
  </`+mtype+`>
</MetaDataObject>`)
		abs := filepath.Join(root, filepath.FromSlash(modulePath))
		mustMkdirAll(t, filepath.Dir(abs))
		mustWrite(t, abs, "\nПроцедура Служебная() Экспорт\nКонецПроцедуры\n")
	}

	cases := []struct{ collection, mtype, name, uuid, modulePath string }{
		{"SettingsStorages", "SettingsStorage", "БуферОбмена", "44444444-4444-4444-4444-444444444444",
			"SettingsStorages/БуферОбмена/Ext/ManagerModule.bsl"},
		{"WebServices", "WebService", "ОбменЗаказами", "55555555-5555-5555-5555-555555555555",
			"WebServices/ОбменЗаказами/Ext/Module.bsl"},
		{"IntegrationServices", "IntegrationService", "ОбменСообщениями", "66666666-6666-6666-6666-666666666666",
			"IntegrationServices/ОбменСообщениями/Ext/Module.bsl"},
		{"FilterCriteria", "FilterCriterion", "СвязанныеДокументы", "77777777-7777-7777-7777-777777777777",
			"FilterCriteria/СвязанныеДокументы/Ext/ManagerModule.bsl"},
	}
	for _, c := range cases {
		object(c.collection, c.mtype, c.name, c.uuid, c.modulePath)
	}

	st := openTestStore(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	for _, c := range cases {
		want := objectNodeID(t, ctx, st, c.mtype, domain.NormalizeName(c.name))
		if got := moduleOwnerByPath(t, ctx, st, c.modulePath); got != want {
			t.Errorf("owner_object_id модуля %s = %d, want %d (%s.%s)", c.modulePath, got, want, c.mtype, c.name)
		}
	}
}

// TestUnknownOwnerCollectionLeavesDiagnostic — словарь ownerTypeToMType
// ведётся руками, и его неполнота не должна быть видна только на реальной
// выгрузке: модуль, лежащий в коллекции выгрузки, для которой mtype не
// известен, получает диагностику. Известная коллекция, чей объект просто
// отсутствует, диагностики НЕ даёт — это не пробел словаря, а неполная
// выгрузка.
func TestUnknownOwnerCollectionLeavesDiagnostic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	const unknownColl = "Sequences"
	if _, known := ownerTypeToMType(unknownColl); known {
		t.Fatalf("предпосылка теста не выполнена: коллекция %s стала известной, нужна другая", unknownColl)
	}
	unknownModule := unknownColl + "/ПродажиПоДокументам/Ext/RecordSetModule.bsl"
	knownModule := "Catalogs/БезXML/Ext/ObjectModule.bsl"
	for _, rel := range []string{unknownModule, knownModule} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		mustMkdirAll(t, filepath.Dir(abs))
		mustWrite(t, abs, "\nПроцедура Служебная() Экспорт\nКонецПроцедуры\n")
	}

	st := openTestStore(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	res, err := svc.Reindex(ctx, ModeFull, "")
	if err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	const code = "index_module_owner_unknown_collection"
	byFile := map[string]int{}
	for _, d := range res.Components[0].Diagnostics {
		if d.Code == code {
			byFile[d.File]++
		}
	}
	if byFile[unknownModule] != 1 {
		t.Errorf("диагностик %s для %s = %d, want 1: пробел словаря обязан оставлять след",
			code, unknownModule, byFile[unknownModule])
	}
	if byFile[knownModule] != 0 {
		t.Errorf("диагностик %s для %s = %d, want 0: коллекция известна, отсутствует сам объект",
			code, knownModule, byFile[knownModule])
	}
	if got := moduleOwnerByPath(t, ctx, st, unknownModule); got != 0 {
		t.Errorf("owner_object_id модуля %s = %d, want пусто", unknownModule, got)
	}
}

// TestCommonModuleOwnerWrittenByXMLBranch — закрепляет ВТОРУЮ точку вызова
// EnsureModule отдельно от первой: правится только XML общего модуля, его
// Module.bsl в транзакцию не попадает (R32.3, см. modulecontext_test.go),
// поэтому отложенный резолв прохода 2 не запускается и владельца в строке
// module оставляет ровно ветка CommonModule из publishMetadataObject. Если
// она перестанет писать OwnerObjectID, колонка обнулится безусловным
// upsert-ом, и это увидит только этот тест.
func TestCommonModuleOwnerWrittenByXMLBranch(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	const bslPath = "CommonModules/УтилитыОбщие/Ext/Module.bsl"
	want := objectNodeID(t, ctx, st, "CommonModule", "утилитыобщие")
	bslFileBefore := sourceFileID(t, ctx, st, bslPath)

	xml := filepath.Join(root, "CommonModules/УтилитыОбщие.xml")
	mustWrite(t, xml, `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="11111111-1111-1111-1111-111111111111">
    <Properties>
      <Name>УтилитыОбщие</Name>
      <Global>false</Global>
      <Server>true</Server>
      <ServerCall>true</ServerCall>
    </Properties>
  </CommonModule>
</MetaDataObject>`)
	touchFuture(t, xml)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}

	// Предпосылка: Module.bsl не переиндексировался, значит владельца в
	// строке module не мог дописать проход 2 — писала только XML-ветка.
	if got := sourceFileID(t, ctx, st, bslPath); got != bslFileBefore {
		t.Fatalf("предпосылка не выполнена: Module.bsl переиндексирован (source_file %d -> %d), сцена проверяет не ту точку вызова", bslFileBefore, got)
	}
	if got := moduleOwnerByPath(t, ctx, st, bslPath); got != want {
		t.Errorf("owner_object_id общего модуля после правки только XML = %d, want %d", got, want)
	}
}

func sourceFileID(t *testing.T, ctx context.Context, st *store.Store, relPath string) int64 {
	t.Helper()
	var id int64
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		var ok bool
		var err error
		id, ok, err = tx.SourceFileID("cfg", relPath)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("файл %s не найден в индексе", relPath)
		}
		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	return id
}
