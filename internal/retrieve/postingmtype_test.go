package retrieve

import (
	"context"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestPostingObjectModulePathUnknownMTypeIsQuiet — вид объекта в
// postingObjectModulePath приходит ИЗ ИНДЕКСА (metadata_object.mtype), а
// индекс берёт его как корневой элемент <MetaDataObject> и белым списком не
// ограничивает (internal/parse/meta). Строитель раскладки на неописанном виде
// ПАНИКУЕТ — для фикстур это правильно (ошибка автора теста обязана быть
// громкой), но в production паника кладёт весь процесс mcp1c вместе с
// индексом: recover в сервере ровно один и он в internal/store, вокруг
// диспетчеризации инструментов его нет.
//
// Здесь проверяется реакция на вид, которого в карте раскладки нет:
// «путь не вывелся» вместо падения. Пара «путь объявления документа + вид
// Bot» искусственна намеренно — гард фикстур принимает только конформные по
// ФОРМЕ пути (тот же приём, что Documents/Заказ_Метаданные.xml в
// postingfallback_test.go), а проверяется здесь не правдоподобие пары, а то,
// что вид из индекса не обязан быть в карте.
func TestPostingObjectModulePathUnknownMTypeIsQuiet(t *testing.T) {
	st := openFixtureStore(t)
	ctx := context.Background()

	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		f := fileHelper(t, tx, "cfg", declPath("Document", "Бот"), "<meta/>")
		_, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg" + "\x00object\x00Bot\x00" + "бот", ComponentID: "cfg",
			MType: "Bot", NameNorm: "бот", NameDisplay: "Бот", FileID: f, Layer: "base",
		})
		return err
	}); err != nil {
		t.Fatalf("сид: %v", err)
	}

	var path string
	var ok bool
	var panicked any
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.MetadataObjectsByNameNormAnyType("бот")
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("в индексе %d объектов с именем бот, want 1", len(rows))
		}
		func() {
			defer func() { panicked = recover() }()
			path, ok, err = postingObjectModulePath(tx, rows[0])
		}()
		return err
	}); err != nil {
		t.Fatalf("чтение: %v", err)
	}

	if panicked != nil {
		t.Fatalf("postingObjectModulePath упал паникой на виде объекта Bot: %v — в production это падение всего процесса mcp1c", panicked)
	}
	if ok || path != "" {
		t.Errorf("вид Bot в карте раскладки не описан, а путь построен: %q (ok=%v)", path, ok)
	}
}
