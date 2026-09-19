package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Фикстура объектного графа: документ Отгрузка объявляет в метаданных
// движения (один регистр — ДВАЖДЫ, второй в конфигурации отсутствует) и пишет
// в регистр кодом через общий модуль, который зовёт и второй документ. Свою
// нестатическую запись Отгрузка держит в модуле менеджера — отдельно от
// цепочки через общий модуль. На ней проверяются обе половины таска 07:
// декларированные рёбра (§4) и кодовые (§5), включая инкремент.
func writeGraphFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"Documents/Отгрузка.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.20">
  <Document uuid="44444444-4444-4444-4444-444444444444">
    <Properties>
      <Name>Отгрузка</Name>
      <RegisterRecords>
        <xr:Item xsi:type="xr:MDObjectRef">AccumulationRegister.ТоварыНаСкладах</xr:Item>
        <xr:Item xsi:type="xr:MDObjectRef">AccumulationRegister.НетВКонфигурации</xr:Item>
        <xr:Item xsi:type="xr:MDObjectRef">AccumulationRegister.ТоварыНаСкладах</xr:Item>
      </RegisterRecords>
    </Properties>
  </Document>
</MetaDataObject>`,
		"CommonModules/ПроведениеДвижений.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="66666666-6666-6666-6666-666666666666">
    <Properties>
      <Name>ПроведениеДвижений</Name>
      <Server>true</Server>
    </Properties>
  </CommonModule>
</MetaDataObject>`,
		"CommonModules/ПроведениеДвижений/Ext/Module.bsl": `
Процедура ЗаписатьДвижения(Движения) Экспорт
	Движения.ТоварыНаСкладах.Записать();
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`,
		"Documents/Отгрузка/Ext/ObjectModule.bsl": `
Процедура ОбработкаПроведения(Отказ, Режим)
	ПроведениеДвижений.ЗаписатьДвижения(Движения);
КонецПроцедуры
`,
		// Вторая половина строк register_access того же владельца лежит в
		// ДРУГОМ его модуле: правка модуля объекта не тянет менеджер в
		// republish, и без замыкания сида по владельцу счётчик бейджа
		// Отгрузки пересчитался бы по одной из двух записей.
		"Documents/Отгрузка/Ext/ManagerModule.bsl": `
Процедура ПересчитатьОстатки() Экспорт
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`,
		// Второй общий модуль отличается от первого тем, что статических
		// записей у него НЕТ: его вызывающие получают бейдж и ни одного
		// ребра, то есть до них атрибуция доходит только бейджем.
		"CommonModules/ПроверкаОстатков.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb">
    <Properties>
      <Name>ПроверкаОстатков</Name>
      <Server>true</Server>
    </Properties>
  </CommonModule>
</MetaDataObject>`,
		"CommonModules/ПроверкаОстатков/Ext/Module.bsl": `
Процедура ПроверитьОстатки() Экспорт
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Очистить();
КонецПроцедуры
`,
		"Documents/Инвентаризация.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Document uuid="cccccccc-cccc-cccc-cccc-cccccccccccc">
    <Properties>
      <Name>Инвентаризация</Name>
    </Properties>
  </Document>
</MetaDataObject>`,
		// Единственный документ фикстуры, пишущий в регистр ПРЯМО у себя:
		// вся цепочка его ребра — один файл, поэтому по нему видно честное
		// удаление связи, а не устаревшую атрибуцию.
		"Documents/Инвентаризация/Ext/ObjectModule.bsl": `
Процедура ОбработкаПроведения(Отказ, Режим)
	Движения.ТоварыНаСкладах.Записать();
	ПроверкаОстатков.ПроверитьОстатки();
КонецПроцедуры
`,
		"Documents/Приемка.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Document uuid="77777777-7777-7777-7777-777777777777">
    <Properties>
      <Name>Приемка</Name>
    </Properties>
  </Document>
</MetaDataObject>`,
		// Приемка достижима двумя независимыми путями: через
		// ПроведениеДвижений (даёт ребро) и через ПроверкаОстатков (даёт
		// только бейдж). Правка первого обрывает первый путь, оставляя второй
		// целым, — на этом видно, что признак устаревшей атрибуции переживает
		// чужую правку, задевшую того же владельца.
		"Documents/Приемка/Ext/ObjectModule.bsl": `
Процедура ОбработкаПроведения(Отказ, Режим)
	ПроведениеДвижений.ЗаписатьДвижения(Движения);
	ПроверкаОстатков.ПроверитьОстатки();
КонецПроцедуры
`,
		// Вторая нестатическая запись Приемки — в её собственном модуле
		// менеджера. Приемка не владеет ни одним republish-нутым модулем,
		// когда правится Отгрузка: её находит атрибуция, и её строки обязано
		// добрать замыкание сида уже по найденному владельцу.
		"Documents/Приемка/Ext/ManagerModule.bsl": `
Процедура ПроверитьОстатки() Экспорт
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`,
		// Возврат несёт три нестатические записи: две в своих СОБСТВЕННЫХ
		// модулях и одну через ПроверкаОстатков, где статических фактов нет.
		// Рёбер у него нет вовсе, поэтому на нём видно и уборку бейджа, и то,
		// что владелец, до которого атрибуция дошла ТОЛЬКО бейджем, обязан
		// попасть в пересчёт наравне с концами рёбер.
		"Documents/Возврат.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Document uuid="99999999-9999-9999-9999-999999999999">
    <Properties>
      <Name>Возврат</Name>
    </Properties>
  </Document>
</MetaDataObject>`,
		"Documents/Возврат/Ext/ObjectModule.bsl": `
Процедура ОбработкаПроведения(Отказ, Режим)
	ПроверкаОстатков.ПроверитьОстатки();
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`,
		"Documents/Возврат/Ext/ManagerModule.bsl": `
Процедура ОчиститьОстатки() Экспорт
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Очистить();
КонецПроцедуры
`,
		"AccumulationRegisters/ТоварыНаСкладах.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <AccumulationRegister uuid="55555555-5555-5555-5555-555555555555">
    <Properties>
      <Name>ТоварыНаСкладах</Name>
    </Properties>
  </AccumulationRegister>
</MetaDataObject>`,
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

// edgesOfKind — исходящие рёбра объекта одного вида.
func edgesOfKind(t *testing.T, ctx context.Context, st *store.Store, objectID int64, kind string) []store.ObjectDataEdgeRow {
	t.Helper()
	var out []store.ObjectDataEdgeRow
	for _, e := range edgesOf(t, ctx, st, objectID) {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// edgesOf читает исходящие рёбра объекта публичной читалкой store.
func edgesOf(t *testing.T, ctx context.Context, st *store.Store, objectID int64) []store.ObjectDataEdgeRow {
	t.Helper()
	var out []store.ObjectDataEdgeRow
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.ObjectDataEdges(store.ObjectEdgeFilter{
			ObjectID: objectID, Direction: store.EdgeDirectionOut, Limit: 100,
		})
		out = rows
		return err
	})
	if err != nil {
		t.Fatalf("ObjectDataEdges: %v", err)
	}
	return out
}

// TestDeclaredMovementEdgePublished — история 17/19 (G08, G10): движения,
// объявленные метаданными документа, становятся рёбрами writes-declared с
// собственной provenance и достоверностью НИЖЕ кодового факта.
//
// Фикстура объявляет три пункта, а ребро обязано остаться одно: регистр,
// которого нет в конфигурации, ребра не даёт (висячее ребро хуже
// отсутствующего), а повторно объявленный регистр не даёт ВТОРОГО ребра —
// дубль отличался бы от оригинала только id и удваивал бы вес связи на карте.
func TestDeclaredMovementEdgePublished(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	svc := NewService(st, "proj", testManifest(t, writeGraphFixture(t)), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	regID := objectNodeID(t, ctx, st, "AccumulationRegister", "товарынаскладах")

	edges := edgesOfKind(t, ctx, st, docID, store.EdgeWritesDeclared)
	if len(edges) != 1 {
		t.Fatalf("рёбер writes-declared у документа = %d, want 1: три объявленных пункта — это один существующий регистр, один отсутствующий и дубль первого: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.ToObjectID != regID {
		t.Errorf("to_object_id = %d, want %d (AccumulationRegister.ТоварыНаСкладах)", e.ToObjectID, regID)
	}
	if e.Kind != store.EdgeWritesDeclared {
		t.Errorf("kind = %q, want %q", e.Kind, store.EdgeWritesDeclared)
	}
	if e.Provenance != store.EdgeProvenanceDeclared {
		t.Errorf("provenance = %q, want %q", e.Provenance, store.EdgeProvenanceDeclared)
	}
	if e.Mode != "movement" {
		t.Errorf("mode = %q, want movement", e.Mode)
	}
	if e.InTransaction != nil {
		t.Errorf("in_transaction = %v, want NULL", *e.InTransaction)
	}
	if e.Layer != "base" {
		t.Errorf("layer = %q, want base", e.Layer)
	}
	// Достоверность декларации строго ниже достоверности кодового факта:
	// иначе декларация выдавала бы себя за код-факт (история 19).
	if !(e.Confidence < codeFactConfidence) {
		t.Errorf("confidence = %v, want строго меньше %v", e.Confidence, codeFactConfidence)
	}
	// Evidence ссылается на XML документа, а не на код.
	if want := "Documents/Отгрузка.xml"; !strings.Contains(e.Evidence, want) {
		t.Errorf("evidence = %q, want ссылку на %s", e.Evidence, want)
	}
	if !strings.Contains(e.Evidence, "AccumulationRegister.ТоварыНаСкладах") {
		t.Errorf("evidence = %q, want имя регистра как оно в XML", e.Evidence)
	}
}

// TestCodeObjectEdgeThroughCommonModule — истории 10/21 (R10, G07, R48):
// запись в регистр, физически сделанная в общем модуле, приписывается
// ДОКУМЕНТУ, который его зовёт. Общий модуль узлом графа не появляется, а
// object_data_edge_dep несёт ОБА файла цепочки: правка любого звена обязана
// уметь снести ребро.
func TestCodeObjectEdgeThroughCommonModule(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	svc := NewService(st, "proj", testManifest(t, writeGraphFixture(t)), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	res, err := svc.Reindex(ctx, ModeFull, "")
	if err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	otherID := objectNodeID(t, ctx, st, "Document", "приемка")
	regID := objectNodeID(t, ctx, st, "AccumulationRegister", "товарынаскладах")
	commonID := objectNodeID(t, ctx, st, "CommonModule", "проведениедвижений")

	// Счётчик публикации считается в момент успешного InsertObjectDataEdge и
	// обязан сойтись с тем, что реально лежит в таблице: расхождение — это
	// ровно тот класс регрессии (факт потерялся ВНУТРИ publish), ради
	// которого счётчики в этом пайплайне и заведены.
	invID := objectNodeID(t, ctx, st, "Document", "инвентаризация")
	inStore := len(edgesOf(t, ctx, st, docID)) + len(edgesOf(t, ctx, st, otherID)) + len(edgesOf(t, ctx, st, invID))
	if len(res.Components) != 1 || res.Components[0].Counts.objectEdge != inStore {
		t.Errorf("Counts.objectEdge = %+v, в таблице рёбер %d", res.Components, inStore)
	}
	// То же самое для бейджей: их счётчик считается по строкам, поэтому он
	// ловит и лишнюю строку на входе атрибуции — доехавшую вторым экземпляром
	// из-за того, что её прочитали из store, хотя она уже была в памяти.
	if badgesInStore := len(allFixtureBadges(t, ctx, st)); res.Components[0].Counts.objectBadge != badgesInStore {
		t.Errorf("Counts.objectBadge = %d, в таблице бейджей %d", res.Components[0].Counts.objectBadge, badgesInStore)
	}

	edges := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)
	if len(edges) != 1 {
		t.Fatalf("рёбер writes-register у документа = %d, want 1: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.ToObjectID != regID {
		t.Errorf("to_object_id = %d, want %d (регистр)", e.ToObjectID, regID)
	}
	if e.Provenance != store.EdgeProvenanceCode {
		t.Errorf("provenance = %q, want %q", e.Provenance, store.EdgeProvenanceCode)
	}
	if e.Layer != "base" {
		t.Errorf("layer = %q, want base", e.Layer)
	}
	if e.Mode != "movement" {
		t.Errorf("mode = %q, want movement", e.Mode)
	}

	// Общий модуль владельцем данных не считается: своего ребра в регистр у
	// него быть не должно (решение D6, история 4).
	if got := edgesOfKind(t, ctx, st, commonID, store.EdgeWritesRegister); len(got) != 0 {
		t.Errorf("у общего модуля %d рёбер writes-register, want 0: %+v", len(got), got)
	}

	// Файловые зависимости ребра — вся цепочка, а не только место факта.
	var deps []string
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.ObjectDataEdgeFiles(e.ID)
		for _, r := range rows {
			deps = append(deps, r.RelPath)
		}
		return err
	})
	if err != nil {
		t.Fatalf("ObjectDataEdgeFiles: %v", err)
	}
	want := []string{
		"CommonModules/ПроведениеДвижений/Ext/Module.bsl",
		"Documents/Отгрузка/Ext/ObjectModule.bsl",
	}
	for _, w := range want {
		found := false
		for _, d := range deps {
			if d == w {
				found = true
			}
		}
		if !found {
			t.Errorf("object_data_edge_dep не содержит %s: %v", w, deps)
		}
	}
}

// badgesOf читает бейджи узла публичной читалкой store.
func badgesOf(t *testing.T, ctx context.Context, st *store.Store, objectID int64) []store.ObjectBadge {
	t.Helper()
	var out []store.ObjectBadge
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.ObjectBadges(objectID)
		out = rows
		return err
	})
	if err != nil {
		t.Fatalf("ObjectBadges: %v", err)
	}
	return out
}

// TestDynamicBadgePublished — история 13 (R14): запись, которую статически
// приписать регистру нельзя, ребра не даёт, но и не замалчивается: владелец
// получает бейдж has-dynamic со счётчиком, тем же проходом публикации.
func TestDynamicBadgePublished(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	svc := NewService(st, "proj", testManifest(t, writeGraphFixture(t)), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	badges := badgesOf(t, ctx, st, docID)
	if len(badges) != 1 {
		t.Fatalf("бейджей у документа = %d, want 1: %+v", len(badges), badges)
	}
	b := badges[0]
	if b.Badge != resolve.BadgeHasDynamic {
		t.Errorf("badge = %q, want %q", b.Badge, resolve.BadgeHasDynamic)
	}
	// Две нестатические записи ведут к документу: одна в общем модуле, куда
	// он делегирует проведение, вторая в его собственном модуле менеджера.
	// Счётчик считает факты, а не пути к ним.
	if b.Count != 2 {
		t.Errorf("count = %d, want 2 (записи в общем модуле и в модуле менеджера)", b.Count)
	}
	if b.Layer != "base" {
		t.Errorf("layer = %q, want base", b.Layer)
	}
}

// allFixtureBadges — все строки object_badge фикстуры: читалка store умеет
// отдавать бейджи только по объекту, поэтому объекты перечислены поимённо.
// Список закрыт составом writeGraphFixture: появится новый объект — его сюда
// придётся дописать, и это правильно, иначе сверка счётчика тихо перестала бы
// покрывать часть таблицы.
func allFixtureBadges(t *testing.T, ctx context.Context, st *store.Store) []store.ObjectBadge {
	t.Helper()
	objects := []struct{ mtype, name string }{
		{"Document", "отгрузка"}, {"Document", "приемка"},
		{"Document", "возврат"}, {"Document", "инвентаризация"},
		{"CommonModule", "проведениедвижений"}, {"CommonModule", "проверкаостатков"},
		{"AccumulationRegister", "товарынаскладах"},
	}
	var out []store.ObjectBadge
	for _, o := range objects {
		out = append(out, badgesOf(t, ctx, st, objectNodeID(t, ctx, st, o.mtype, o.name))...)
	}
	return out
}

// TestIncrementalEdgeRebuildIsLocal — история 21 (R11): правка одного модуля
// пересобирает рёбра только затронутых владельцев.
//
// Оба документа фикстуры пишут в регистр через ОДИН общий модуль, то есть их
// рёбра выведены из одного и того же факта. Правится модуль только первого:
//   - его ребро обязано пересобраться (id меняется — старое снесено, новое
//     вставлено) и остаться единственным: цепочка идёт через файл, который в
//     republish не попал, и без спуска по графу вызовов ребро пропало бы;
//   - ребро второго документа обязано остаться ТЕМ ЖЕ (id не изменился) и не
//     задвоиться: пересобирать его было не с чего.
func TestIncrementalEdgeRebuildIsLocal(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	otherID := objectNodeID(t, ctx, st, "Document", "приемка")
	edgeIDOf := func(objectID int64) int64 {
		t.Helper()
		edges := edgesOfKind(t, ctx, st, objectID, store.EdgeWritesRegister)
		if len(edges) != 1 {
			t.Fatalf("рёбер writes-register у объекта %d = %d, want 1: %+v", objectID, len(edges), edges)
		}
		return edges[0].ID
	}
	beforeDoc := edgeIDOf(docID)
	beforeOther := edgeIDOf(otherID)

	modulePath := filepath.Join(root, filepath.FromSlash("Documents/Отгрузка/Ext/ObjectModule.bsl"))
	mustWrite(t, modulePath, `
Процедура ОбработкаПроведения(Отказ, Режим)
	// правка, не меняющая смысла проведения
	ПроведениеДвижений.ЗаписатьДвижения(Движения);
КонецПроцедуры
`)
	touchFuture(t, modulePath)
	res, err := svc.Reindex(ctx, ModeIncremental, "")
	if err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	if len(res.Components) != 1 || res.Components[0].FilesChanged != 1 {
		t.Fatalf("FilesChanged = %+v, want 1: инкремент обязан тронуть один файл", res.Components)
	}

	if got := edgeIDOf(docID); got == beforeDoc {
		t.Errorf("ребро правленого документа сохранило id %d: оно обязано быть пересобрано", got)
	}
	if got := edgeIDOf(otherID); got != beforeOther {
		t.Errorf("ребро нетронутого документа id = %d, было %d: пересобрано лишнее", got, beforeOther)
	}
	// Ребро снесено и в этом же прогоне восстановлено, хотя переопубликован
	// был не весь его файловый состав: признака устаревшей атрибуции быть не
	// должно. Иначе он загорался бы на каждой обычной правке и перестал бы
	// что-либо значить.
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак устаревшей атрибуции у правленого документа = %d, want 0: связь восстановлена", got)
	}

	err = st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() = %v, want пусто (в том числе object_data_edge_without_dep)", bad)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Инкремент обязан дать то же, что чистая пересборка того же дерева: иначе
	// «пересобрали не всё» и «пересобрали лишнее» отличались бы только тем,
	// какую половину ошибки заметили.
	fresh := openTestStore(t)
	freshSvc := NewService(fresh, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { freshSvc.Close() })
	if _, err := freshSvc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) контрольной сборки: %v", err)
	}
	for _, mtype := range []string{"Document"} {
		for _, name := range []string{"отгрузка", "приемка"} {
			got := edgeShapes(t, ctx, st, objectNodeID(t, ctx, st, mtype, name))
			want := edgeShapes(t, ctx, fresh, objectNodeID(t, ctx, fresh, mtype, name))
			if len(got) != len(want) {
				t.Fatalf("%s.%s: инкремент дал %v, чистая пересборка %v", mtype, name, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s.%s ребро %d: инкремент %q, чистая пересборка %q", mtype, name, i, got[i], want[i])
				}
			}
		}
	}
}

// edgeShapes — рёбра объекта в виде, сравнимом между двумя разными индексами:
// без id (они свои в каждой сборке), но с видом, слоем, достоверностью и
// ИМЕНЕМ объекта-цели.
func edgeShapes(t *testing.T, ctx context.Context, st *store.Store, objectID int64) []string {
	t.Helper()
	var out []string
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		edges, err := tx.ObjectDataEdges(store.ObjectEdgeFilter{
			ObjectID: objectID, Direction: store.EdgeDirectionOut, Limit: 500,
		})
		if err != nil {
			return err
		}
		for _, e := range edges {
			row, ok, err := tx.MetadataObjectByID(e.ToObjectID)
			if err != nil {
				return err
			}
			name := "?"
			if ok {
				name = row.MType + "." + row.NameDisplay
			}
			out = append(out, fmt.Sprintf("%s|%s|%s|%s|%.4f", e.Kind, name, e.Layer, e.Provenance, e.Confidence))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sort.Strings(out)
	return out
}

// TestObjectEdgeGraphTransportErrorSurfaces — требование ревью таска 06:
// методы resolve.ObjectEdgeGraph ошибок не возвращают, поэтому вся цена
// сбоя транспорта лежит на реализации. Сбой обязан кончиться ошибкой
// наружу, а не тихо пустым графом: пустой ответ на обходе неотличим от
// честного «цепочка никуда не ведёт» и дал бы карту без рёбер без единого
// слова о причине.
func TestObjectEdgeGraphTransportErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Завершённая транзакция — честный сбой транспорта: любой её запрос
	// отвечает store.ErrTxDone.
	var dead *store.WriteTx
	if err := st.Write(ctx, func(tx *store.WriteTx) error { dead = tx; return nil }); err != nil {
		t.Fatalf("Write: %v", err)
	}

	g := newTxEdgeGraph(dead)
	edges, badges := resolve.DeriveObjectDataEdges(resolve.ObjectEdgeInput{
		Accesses: []store.RegisterAccessRow{{
			ID: 1, FileID: 1, SymbolID: 1, ObjectID: 2,
			RegisterNameNorm: "товарынаскладах", Mode: "movement", Static: true, Confidence: 1,
		}},
		Graph: g,
	})
	if len(edges) != 0 || len(badges) != 0 {
		t.Errorf("на сломанном транспорте получено %d рёбер и %d бейджей, want ноль", len(edges), len(badges))
	}
	if g.Err() == nil {
		t.Fatal("Err() пуст: ошибка транспорта потеряна, наружу ушёл бы молча пустой граф")
	}

	// Та же ошибка обязана дойти до вызывающего публикации, а не остаться
	// внутри адаптера.
	ts := &txState{nodes: newTxNodeCache(), fileID: map[string]int64{}}
	err := publishCodeObjectEdges(dead, publishInput{}, ts,
		map[string]modulePublishState{"m": {fileID: 1, methodSymbolID: map[int]int64{0: 1}}})
	if err == nil {
		t.Error("publishCodeObjectEdges вернул nil на сломанном транспорте: ошибка проглочена")
	}
}

// TestGraphTunablesReachDeriver — вторая половина маршрута флагов -graph-*:
// options -> app.DefaultIndexConfig -> index.Config -> дериватор. Первая
// половина закреплена в cmd/mcp1c (TestNewServerConfiguresGraphTunables), эта
// проверяет, что index.Config не теряет пороги по дороге к атрибуции.
//
// Заодно история 15 (R16): превышение порога СНИЖАЕТ достоверность, но ребро
// остаётся. Общий модуль фикстуры зовут два документа, поэтому fan-in 1
// делает его хабом.
func TestGraphTunablesReachDeriver(t *testing.T) {
	ctx := context.Background()
	root := writeGraphFixture(t)

	confidenceWith := func(cfg Config) float64 {
		t.Helper()
		st := openTestStore(t)
		svc := NewService(st, "proj", testManifest(t, root), nil, cfg)
		t.Cleanup(func() { svc.Close() })
		if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
			t.Fatalf("Reindex(full): %v", err)
		}
		docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
		edges := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)
		if len(edges) != 1 {
			t.Fatalf("рёбер writes-register = %d, want 1 (порог не режет ребро): %+v", len(edges), edges)
		}
		return edges[0].Confidence
	}

	base := confidenceWith(Config{})
	penalised := confidenceWith(Config{GraphTunables: resolve.ObjectEdgeTunables{
		HubFanIn: 1, HubPenalty: 0.5,
	}})
	if !(penalised < base) {
		t.Errorf("confidence с порогом хаба = %v, без порога = %v: пороги до дериватора не доехали", penalised, base)
	}
}

// TestRealDumpDeclaredMovementsOrderDocument — история 17 (G08, G10) на
// реальной выгрузке: у Документ.ЗаказКлиента есть рёбра writes-declared.
//
// Именно этот документ был доказательством в отчёте 2026-08-20: строк
// register_access у него нет вовсе (проведение делегировано в
// ОбщийМодуль.ПроведениеДокументов), поэтому декларация метаданных —
// единственный источник его рёбер.
//
// Ожидание взято не из кода под тестом: два регистра ниже выписаны из вырезки
// реальной выгрузки в internal/parse/meta/document_test.go.
func TestRealDumpDeclaredMovementsOrderDocument(t *testing.T) {
	root := realDumpRoot(t)
	st := openTestStore(t)
	m := workspace.Manifest{
		Version: 1, Project: "utdemo", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
	builtins := syntaxtest.RealOrSkip(t)
	svc := NewService(st, "utdemo", m, builtins, Config{})
	t.Cleanup(func() { svc.Close() })

	ctx := context.Background()
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке: %v", err)
	}

	var got []string
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		docID, ok, err := tx.NodeID(metadataObjectIdentityKey("cfg", "Document", "заказклиента"))
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("Документ.ЗаказКлиента не найден в индексе")
		}
		edges, err := tx.ObjectDataEdges(store.ObjectEdgeFilter{
			ObjectID: docID, Direction: store.EdgeDirectionOut,
			Kinds: []string{store.EdgeWritesDeclared}, Limit: 500,
		})
		if err != nil {
			return err
		}
		for _, e := range edges {
			if e.Provenance != store.EdgeProvenanceDeclared {
				t.Errorf("provenance ребра %d = %q, want %q", e.ID, e.Provenance, store.EdgeProvenanceDeclared)
			}
			row, ok, err := tx.MetadataObjectByID(e.ToObjectID)
			if err != nil {
				return err
			}
			if !ok {
				t.Errorf("ребро %d указывает на несуществующий объект %d", e.ID, e.ToObjectID)
				continue
			}
			got = append(got, row.MType+"."+row.NameDisplay)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	t.Logf("writes-declared у ЗаказКлиента (%d): %v", len(got), got)
	if len(got) == 0 {
		t.Fatal("у ЗаказКлиента нет рёбер writes-declared")
	}
	for _, want := range []string{
		"AccumulationRegister.ТоварыКОтгрузке",
		"AccumulationRegister.РасчетыСКлиентами",
	} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("движение %s не стало ребром: %v", want, got)
		}
	}
}

// badgeCount — счётчик одного бейджа объекта; ноль означает «бейджа нет».
func badgeCount(t *testing.T, ctx context.Context, st *store.Store, objectID int64, badge string) int64 {
	t.Helper()
	for _, b := range badgesOf(t, ctx, st, objectID) {
		if b.Badge == badge {
			return b.Count
		}
	}
	return 0
}

// TestBadgeSeedClosesOverOwners — требование ревью таска 06: InsertObjectBadge
// счётчик ЗАМЕЩАЕТ, поэтому владельцу нельзя подать часть его строк
// register_access. Правится ОДИН модуль, а закрыть тест обязаны два разных
// замыкания сида по владельцу (ModuleFilesByOwnerObject):
//
//   - Отгрузка владеет правленым модулем менеджера, но вторая её нестатическая
//     запись лежит в общем модуле, куда ведёт её модуль объекта, а тот в
//     republish не попал.
//     Замыкание идёт ДО первой атрибуции: бейджи scope будут снесены, значит
//     строки обязаны быть полными уже в первом раунде;
//   - Приемка не владеет ни одним правленым модулем: её находит атрибуция,
//     поднимаясь от факта в общем модуле. Её вторая запись тоже в модуле
//     менеджера, и добрать её может только СЛЕДУЮЩИЙ раунд замыкания.
//
// Без замыкания оба счётчика записались бы уменьшенными, и дыра выглядела бы
// заросшей, а не недосчитанной.
func TestBadgeSeedClosesOverOwners(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	otherID := objectNodeID(t, ctx, st, "Document", "приемка")
	// У Отгрузки две нестатические записи, у Приемки три: она зовёт ещё и
	// ПроверкаОстатков.
	for _, c := range []struct {
		name string
		id   int64
		want int64
	}{{"отгрузка", docID, 2}, {"приемка", otherID, 3}} {
		if got := badgeCount(t, ctx, st, c.id, resolve.BadgeHasDynamic); got != c.want {
			t.Fatalf("счётчик has-dynamic у %s после полной сборки = %d, want %d: фикстура не воспроизводит записи в разных модулях", c.name, got, c.want)
		}
	}

	// Правится модуль менеджера Отгрузки, и только он. Правка сохраняет его
	// запись — значит эта строка register_access приходит на вход атрибуции
	// ИЗ ПАМЯТИ, а строки общего модуля дочитываются из store. Прочитать
	// первую ещё и из store значило бы посчитать её дважды, и счётчик бейджа,
	// который считается по строкам, назвал бы дыру больше, чем она есть.
	modulePath := filepath.Join(root, filepath.FromSlash("Documents/Отгрузка/Ext/ManagerModule.bsl"))
	mustWrite(t, modulePath, `
Процедура ПересчитатьОстатки() Экспорт
	// правка, не меняющая записей в регистры
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`)
	touchFuture(t, modulePath)
	res, err := svc.Reindex(ctx, ModeIncremental, "")
	if err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	if len(res.Components) != 1 || res.Components[0].FilesChanged != 1 {
		t.Fatalf("FilesChanged = %+v, want 1: остальные модули не должны попасть в republish", res.Components)
	}

	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 2 {
		t.Errorf("счётчик has-dynamic Отгрузки после инкремента = %d, want 2: строки владельца поданы не полностью либо поданы дважды", got)
	}
	if got := badgeCount(t, ctx, st, otherID, resolve.BadgeHasDynamic); got != 3 {
		t.Errorf("счётчик has-dynamic Приемки после инкремента = %d, want 3: найденному атрибуцией владельцу подана часть его строк", got)
	}
}

// TestBadgeShrinksThenDisappears — уборка устаревших бейджей, обе её половины.
// Бейдж файловой зависимости не имеет (его поля закрыты контрактом §2),
// поэтому пересборка чистит его по объекту и пишет заново.
//
// У Возврата две нестатические записи в двух его собственных модулях, и
// правится по одному модулю за шаг:
//
//   - первый шаг убирает запись из модуля объекта. Атрибуция не найдёт в нём
//     ничего, то есть Возврат не попадёт в найденные ею объекты, а бейджи его
//     всё равно будут снесены — значит вторая запись обязана быть в сиде ещё
//     ДО первой атрибуции, иначе счётчик обнулился бы при живой дыре;
//   - второй шаг убирает последнюю запись. Записать поверх больше нечего:
//     бейдж обязан ИСЧЕЗНУТЬ, а не остаться прошлым счётчиком.
func TestBadgeShrinksThenDisappears(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "возврат")
	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 3 {
		t.Fatalf("счётчик has-dynamic после полной сборки = %d, want 3", got)
	}

	empty := "\nПроцедура Пустая() Экспорт\n\t// записей больше нет\nКонецПроцедуры\n"
	rewrite := func(rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		mustWrite(t, p, empty)
		touchFuture(t, p)
		if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
			t.Fatalf("Reindex(incremental) после правки %s: %v", rel, err)
		}
	}

	// Правка уносит и собственную запись модуля объекта, и вызов
	// ПроверкаОстатков: остаётся одна запись, в модуле менеджера.
	rewrite("Documents/Возврат/Ext/ObjectModule.bsl")
	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 1 {
		t.Errorf("счётчик has-dynamic после первой правки = %d, want 1: пересчитан по части модулей владельца", got)
	}

	rewrite("Documents/Возврат/Ext/ManagerModule.bsl")
	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 0 {
		t.Errorf("счётчик has-dynamic после второй правки = %d, want 0: бейдж не убран", got)
	}
	if badges := badgesOf(t, ctx, st, docID); len(badges) != 0 {
		t.Errorf("бейджи объекта = %+v, want пусто", badges)
	}
}

// TestBadgeOnlyOwnerIsRecounted — вторая половина замыкания сида: владелец, до
// которого атрибуция дошла ТОЛЬКО бейджем, обязан попасть в пересчёт наравне с
// концами рёбер.
//
// Возврат зовёт ПроверкаОстатков, где нет ни одного статического факта: рёбер
// он не даёт, а бейдж даёт. Правится модуль Инвентаризации — другого
// вызывающего того же общего модуля. Возврат при этом не владеет ни одним
// republish-нутым модулем, и найти его можно только по бейджу; если пересчёт
// смотрит лишь на концы рёбер, его строки не доберутся и счётчик запишется
// уменьшенным поверх полного.
func TestBadgeOnlyOwnerIsRecounted(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "возврат")
	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 3 {
		t.Fatalf("счётчик has-dynamic Возврата после полной сборки = %d, want 3", got)
	}
	// Предпосылка теста: рёбер у Возврата нет, найти его можно только бейджем.
	if got := edgesOf(t, ctx, st, docID); len(got) != 0 {
		t.Fatalf("рёбер у Возврата = %d, want 0: фикстура не воспроизводит владельца, найденного только бейджем: %+v", len(got), got)
	}

	modulePath := filepath.Join(root, filepath.FromSlash("Documents/Инвентаризация/Ext/ObjectModule.bsl"))
	mustWrite(t, modulePath, `
Процедура ОбработкаПроведения(Отказ, Режим)
	// правка, не меняющая записей в регистры
	Движения.ТоварыНаСкладах.Записать();
	ПроверкаОстатков.ПроверитьОстатки();
КонецПроцедуры
`)
	touchFuture(t, modulePath)
	res, err := svc.Reindex(ctx, ModeIncremental, "")
	if err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	if len(res.Components) != 1 || res.Components[0].FilesChanged != 1 {
		t.Fatalf("FilesChanged = %+v, want 1: модули Возврата не должны попасть в republish", res.Components)
	}

	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 3 {
		t.Errorf("счётчик has-dynamic Возврата после инкремента = %d, want 3: владельцу, найденному бейджем, подана часть его строк", got)
	}
}

// TestStaleAttributionBadgeMarksLostEdge: снесённая связь обязана быть видимой.
//
// Первая половина (закрытая граница D03, ADR-026, ADR-037): правка общего
// модуля, не меняющая записей, больше не рвёт входящие вызовы, и атрибуция
// поднимается от факта в правленом модуле к документу, которого правка не
// касалась. Ребро восстанавливается той же публикацией, признака нет.
//
// Вторая половина проверяет сам признак. Правка чужого звена цепочки, ЧЕСТНО убравшая запись
// (общий модуль больше не пишет в регистр), сносит ребро документа, чей
// модуль не переопубликован: отличить это от потери по карте нечем (ADR-026,
// «в обе стороны признак не симметричен»), поэтому владелец получает признак.
//
// Проверяются четыре вещи: связь переживает правку без смены записей; признак
// появляется там, где связь пропала у непереопубликованного владельца; он НЕ
// появляется там, где связь убрана правкой модуля самого владельца; полная
// пересборка его снимает.
func TestStaleAttributionBadgeMarksLostEdge(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	invID := objectNodeID(t, ctx, st, "Document", "инвентаризация")
	if len(edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)) != 1 {
		t.Fatal("предпосылка теста: у Отгрузки после полной сборки есть ребро writes-register")
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Fatalf("признак устаревшей атрибуции после полной сборки = %d, want 0", got)
	}

	// Правка общего модуля, не меняющая записей в регистры: цепочка Отгрузки
	// проходит через него, ребро снесено по файловой зависимости и обязано
	// вернуться этой же публикацией (D03 закрыта, ADR-037).
	commonPath := filepath.Join(root, filepath.FromSlash("CommonModules/ПроведениеДвижений/Ext/Module.bsl"))
	mustWrite(t, commonPath, `
Процедура ЗаписатьДвижения(Движения) Экспорт
	// правка, не меняющая записей в регистры
	Движения.ТоварыНаСкладах.Записать();
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`)
	touchFuture(t, commonPath)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 1 {
		t.Fatalf("рёбер writes-register у Отгрузки после правки общего модуля = %d, want 1: связь, которую полная сборка имеет, потеряна инкрементом", len(got))
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак устаревшей атрибуции у Отгрузки = %d, want 0: связь восстановлена", got)
	}

	// Правка общего модуля, честно убравшая запись: модуль Отгрузки не
	// переопубликован, ребро снесено и не построено, владелец получает
	// признак.
	mustWrite(t, commonPath, `
Процедура ЗаписатьДвижения(Движения) Экспорт
	// записи в регистры убраны из общего модуля
КонецПроцедуры
`)
	touchFuture(t, commonPath)
	res, err := svc.Reindex(ctx, ModeIncremental, "")
	if err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 0 {
		t.Fatalf("ребро writes-register уцелело (%+v): запись из общего модуля убрана", got)
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 1 {
		t.Errorf("признак устаревшей атрибуции у Отгрузки = %d, want 1: связь снесена молча", got)
	}

	// Сверка счётчика публикации с таблицей — на инкременте, а не только на
	// полной сборке: признак устаревшей атрибуции на полной сборке не
	// возникает по определению, и его строки в счётчик там не попадают.
	//
	// Этот прогон записал ровно две группы строк, и обе видны в таблице:
	// бейджи объектов, чьи цепочки пересчитаны (атрибуция упёрлась в общий
	// модуль и повесила динамику на него самого), и признаки владельцам, чья
	// связь пропала.
	written := len(badgesOf(t, ctx, st, objectNodeID(t, ctx, st, "CommonModule", "проведениедвижений")))
	for _, b := range allFixtureBadges(t, ctx, st) {
		if b.Badge == BadgeAttributionStale {
			written++
		}
	}
	if res.Components[0].Counts.objectBadge != written {
		t.Errorf("Counts.objectBadge = %d, в таблице строк этого прогона %d",
			res.Components[0].Counts.objectBadge, written)
	}

	// Инвентаризация пишет в регистр прямо у себя: правка её модуля, убравшая
	// запись, — честное удаление связи, а не устаревшая атрибуция. Вся
	// цепочка её ребра переопубликована этой же транзакцией.
	invPath := filepath.Join(root, filepath.FromSlash("Documents/Инвентаризация/Ext/ObjectModule.bsl"))
	mustWrite(t, invPath, `
Процедура ОбработкаПроведения(Отказ, Режим)
	ПроверкаОстатков.ПроверитьОстатки();
КонецПроцедуры
`)
	touchFuture(t, invPath)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental) второй: %v", err)
	}
	if got := edgesOfKind(t, ctx, st, invID, store.EdgeWritesRegister); len(got) != 0 {
		t.Fatalf("ребро Инвентаризации уцелело (%+v): запись из кода убрана", got)
	}
	if got := badgeCount(t, ctx, st, invID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак устаревшей атрибуции у Инвентаризации = %d, want 0: связь убрана из кода, а не потеряна", got)
	}

	// Полная пересборка снимает признак: она строит эпоху заново, сносить в
	// ней нечего, и каждое ребро либо построено, либо его правда нет.
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) повторный: %v", err)
	}
	for _, c := range []struct {
		name string
		id   int64
	}{{"отгрузка", objectNodeID(t, ctx, st, "Document", "отгрузка")},
		{"инвентаризация", objectNodeID(t, ctx, st, "Document", "инвентаризация")}} {
		if got := badgeCount(t, ctx, st, c.id, BadgeAttributionStale); got != 0 {
			t.Errorf("признак устаревшей атрибуции у %s после полной пересборки = %d, want 0", c.name, got)
		}
	}
}

// TestStaleBadgeNotSetOnHonestRefactor — признак устаревшей атрибуции обязан
// молчать на обычном рефакторинге. Документ ЧЕСТНО перестаёт звать общий
// модуль: ребро законно исчезает, и загораться нечему.
//
// Файлового критерия («вся цепочка переопубликована») тут не хватает: цепочка
// была из двух файлов, а переопубликован один. Отвечает на вопрос владелец —
// его собственный модуль пересобран, значит атрибуция прошла по свежему
// тексту, и отсутствие ребра есть результат разбора, а не оборванной выборки.
// Сигнал, горящий и на потере, и на намеренном удалении, никому не нужен.
func TestStaleBadgeNotSetOnHonestRefactor(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	if len(edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)) != 1 {
		t.Fatal("предпосылка теста: у Отгрузки после полной сборки есть ребро writes-register")
	}

	modulePath := filepath.Join(root, filepath.FromSlash("Documents/Отгрузка/Ext/ObjectModule.bsl"))
	mustWrite(t, modulePath, `
Процедура ОбработкаПроведения(Отказ, Режим)
	// проведение больше не делегируется в общий модуль
КонецПроцедуры
`)
	touchFuture(t, modulePath)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}

	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 0 {
		t.Fatalf("ребро writes-register уцелело (%+v): вызов общего модуля убран", got)
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак устаревшей атрибуции = %d, want 0: связь убрана из кода намеренно", got)
	}

	// Второй вид честного удаления: движение убрано из МЕТАДАННЫХ. У
	// декларированного ребра нет модуля-владельца в цепочке вовсе — вся его
	// зависимость это XML документа, — поэтому владелец в переопубликованные
	// не попадает, и отвечает здесь файловый критерий.
	if len(edgesOfKind(t, ctx, st, docID, store.EdgeWritesDeclared)) != 1 {
		t.Fatal("предпосылка: у Отгрузки есть ребро writes-declared")
	}
	xmlPath := filepath.Join(root, filepath.FromSlash("Documents/Отгрузка.xml"))
	mustWrite(t, xmlPath, `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.20">
  <Document uuid="44444444-4444-4444-4444-444444444444">
    <Properties>
      <Name>Отгрузка</Name>
      <RegisterRecords/>
    </Properties>
  </Document>
</MetaDataObject>`)
	touchFuture(t, xmlPath)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental) после правки метаданных: %v", err)
	}
	docID = objectNodeID(t, ctx, st, "Document", "отгрузка")
	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesDeclared); len(got) != 0 {
		t.Fatalf("ребро writes-declared уцелело (%+v): движение убрано из метаданных", got)
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак после правки метаданных = %d, want 0: движение убрано намеренно", got)
	}
}

// TestStaleBadgeSurvivesUnrelatedEdit — признак живёт, пока живёт причина.
//
// Уборка бейджей идёт по объекту, поэтому ЛЮБАЯ правка, задевшая владельца,
// стирала бы и признак, а ребро при этом по-прежнему отсутствует. Проверяются
// обе половины. Причина жива, значит признак на месте; причина снята (запись
// возвращена в общий модуль и переопубликован модуль самого владельца, ребро
// вернулось), значит признака нет.
func TestStaleBadgeSurvivesUnrelatedEdit(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	edit := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		mustWrite(t, p, body)
		touchFuture(t, p)
		if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
			t.Fatalf("Reindex(incremental) после правки %s: %v", rel, err)
		}
	}
	docID := objectNodeID(t, ctx, st, "Document", "приемка")

	// Шаг 1: из общего модуля убрана запись. Ребро Приемки снесено и не
	// восстановлено, модуль Приемки не переопубликован.
	edit("CommonModules/ПроведениеДвижений/Ext/Module.bsl", `
Процедура ЗаписатьДвижения(Движения) Экспорт
	// записи в регистры убраны из общего модуля
КонецПроцедуры
`)
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 1 {
		t.Fatalf("признак после удаления записи из общего модуля = %d, want 1", got)
	}

	// Шаг 2: правка ЧУЖОГО модуля, которая приводит Приемку в пересчёт по
	// второму, целому пути (ПроверкаОстатков). Своих модулей она не
	// переопубликовывала, ребро не вернулось — признак обязан уцелеть.
	edit("Documents/Инвентаризация/Ext/ObjectModule.bsl", `
Процедура ОбработкаПроведения(Отказ, Режим)
	// правка, не меняющая записей в регистры
	Движения.ТоварыНаСкладах.Записать();
	ПроверкаОстатков.ПроверитьОстатки();
КонецПроцедуры
`)
	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 0 {
		t.Fatalf("ребро Приемки вернулось (%+v): предпосылка второго шага не выполняется", got)
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 1 {
		t.Errorf("признак после чужой правки = %d, want 1: стёрт вместе с остальными бейджами, хотя причина не снята", got)
	}

	// Шаг 3: запись возвращена в общий модуль. Ребро Приемки возвращается
	// этой же публикацией (указатели вызовов пережили правку, ADR-037), но
	// модуль Приемки не переопубликован, и признак прошлого прогона
	// остаётся: бейдж не знает, какие связи он считает. Это названное
	// упрощение carryStaleBadges, и тест держит его явно: снимут его,
	// тест обязан сказать это вслух.
	edit("CommonModules/ПроведениеДвижений/Ext/Module.bsl", `
Процедура ЗаписатьДвижения(Движения) Экспорт
	Движения.ТоварыНаСкладах.Записать();
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`)
	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 1 {
		t.Fatalf("ребро Приемки после возврата записи = %d, want 1", len(got))
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 1 {
		t.Errorf("признак после возврата записи без переопубликации владельца = %d, want 1 (упрощение carryStaleBadges)", got)
	}

	// Шаг 4: переопубликован модуль самого владельца: его цепочки посчитаны
	// заново, причина снята.
	edit("Documents/Приемка/Ext/ObjectModule.bsl", `
Процедура ОбработкаПроведения(Отказ, Режим)
	// правка, не меняющая записей в регистры
	ПроведениеДвижений.ЗаписатьДвижения(Движения);
	ПроверкаОстатков.ПроверитьОстатки();
КонецПроцедуры
`)
	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 1 {
		t.Fatalf("ребро Приемки = %d, want 1: причина обязана быть снята переопубликацией владельца", len(got))
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак после возвращения ребра = %d, want 0", got)
	}
}

// writeTwoEdgeFixture — документ, чьи ДВА ребра выведены из одной цепочки
// через общий модуль. Отдельная фикстура, а не расширение основной: там от
// числа рёбер Отгрузки зависят несколько проверок, и подмешивать в них второй
// регистр значило бы менять смысл соседних тестов ради одного.
func writeTwoEdgeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	register := func(name, uuid string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <AccumulationRegister uuid="` + uuid + `">
    <Properties>
      <Name>` + name + `</Name>
    </Properties>
  </AccumulationRegister>
</MetaDataObject>`
	}
	files := map[string]string{
		"AccumulationRegisters/ПартииА.xml": register("ПартииА", "dddddddd-0000-0000-0000-000000000001"),
		"AccumulationRegisters/ПартииБ.xml": register("ПартииБ", "dddddddd-0000-0000-0000-000000000002"),
		"CommonModules/ДвижениеПартий.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="dddddddd-0000-0000-0000-00000000000a">
    <Properties>
      <Name>ДвижениеПартий</Name>
      <Server>true</Server>
    </Properties>
  </CommonModule>
</MetaDataObject>`,
		"CommonModules/ДвижениеПартий/Ext/Module.bsl": `
Процедура Записать(Движения) Экспорт
	Движения.ПартииА.Записать();
	Движения.ПартииБ.Записать();
КонецПроцедуры
`,
		"Documents/Партия.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Document uuid="dddddddd-0000-0000-0000-00000000000b">
    <Properties>
      <Name>Партия</Name>
    </Properties>
  </Document>
</MetaDataObject>`,
		"Documents/Партия/Ext/ObjectModule.bsl": `
Процедура ОбработкаПроведения(Отказ, Режим)
	ДвижениеПартий.Записать(Движения);
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

// TestStaleBadgeCountsEveryLostEdge — счётчик признака считает СВЯЗИ, а не
// факт беды. У документа два ребра из одной цепочки; правка общего модуля,
// убравшая обе записи, сносит оба ребра и не строит ни одного, а модуль
// документа не переопубликован, значит счётчик равен двум.
// Единица здесь означала бы «что-то потеряно», и по бейджу нельзя было бы
// понять масштаб дыры.
func TestStaleBadgeCountsEveryLostEdge(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeTwoEdgeFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "партия")
	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 2 {
		t.Fatalf("рёбер writes-register = %d, want 2: фикстура не воспроизводит две связи одного владельца", len(got))
	}

	commonPath := filepath.Join(root, filepath.FromSlash("CommonModules/ДвижениеПартий/Ext/Module.bsl"))
	mustWrite(t, commonPath, `
Процедура Записать(Движения) Экспорт
	// записи в регистры убраны из общего модуля
КонецПроцедуры
`)
	touchFuture(t, commonPath)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}

	if got := edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister); len(got) != 0 {
		t.Fatalf("рёбра уцелели (%+v): предпосылка теста не выполняется", got)
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 2 {
		t.Errorf("счётчик признака = %d, want 2: потеряны две разные связи", got)
	}
}

// TestPublishStaleAttributionBadgesTable — таблица на прямом вызове
// publishStaleAttributionBadges (приём TestObjectEdgeGraphTransportErrorSurfaces:
// зовём внутренность конвейера напрямую, минуя publishFiles).
//
// Прямой вызов проверяет каждую ветку в изоляции, как контракт функции.
// Ветка «связь вернулась» с ADR-037 достижима и через пайплайн (первая
// половина TestStaleAttributionBadgeMarksLostEdge), здесь она закреплена
// ещё и отдельно от соседней ветки «владелец переопубликован».
//
// Четыре исхода, один и тот же вход (снесённое ребро), различные условия:
func TestPublishStaleAttributionBadgesTable(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeGraphFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	docID := objectNodeID(t, ctx, st, "Document", "отгрузка")
	otherID := objectNodeID(t, ctx, st, "Document", "приемка")

	cases := []struct {
		name string
		// fromID — владелец, у которого мог бы появиться признак; каждый
		// случай использует СВОЙ объект, чтобы прогоны таблицы не влияли
		// друг на друга через один и тот же бейдж.
		fromID    int64
		layer     string
		fileIDs   []int64
		backEdge  bool // ребро есть в publishedEdges: вернулось этим же прогоном
		fresh     bool // владелец есть в republishedOwners
		allStale  bool // все fileIDs есть в ts.staleFiles: вся цепочка переопубликована
		wantCount int64
	}{
		{name: "связь вернулась", fromID: docID, layer: "base", fileIDs: []int64{101}, backEdge: true},
		{name: "владелец переопубликован", fromID: otherID, layer: "base", fileIDs: []int64{102, 103}, fresh: true},
		{
			name: "вся цепочка переопубликована", layer: "base", fileIDs: []int64{104, 105}, allStale: true,
			fromID: objectNodeID(t, ctx, st, "Document", "возврат"),
		},
		{
			name: "честная потеря", layer: "base", fileIDs: []int64{201, 202}, wantCount: 1,
			fromID: objectNodeID(t, ctx, st, "Document", "инвентаризация"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := store.ObjectDataEdgeKey{FromObjectID: c.fromID, ToObjectID: 999, Kind: store.EdgeWritesRegister, Layer: c.layer}
			ts := &txState{
				staleEdges:        []store.ObjectDataEdgeDeps{{ObjectDataEdgeKey: key, FileIDs: c.fileIDs}},
				publishedEdges:    map[store.ObjectDataEdgeKey]struct{}{},
				republishedOwners: map[int64]struct{}{},
				staleFiles:        map[int64]struct{}{},
			}
			if c.backEdge {
				ts.publishedEdges[key] = struct{}{}
			}
			if c.fresh {
				ts.republishedOwners[c.fromID] = struct{}{}
			}
			if c.allStale {
				for _, f := range c.fileIDs {
					ts.staleFiles[f] = struct{}{}
				}
			}
			if err := st.Write(ctx, func(tx *store.WriteTx) error {
				return publishStaleAttributionBadges(tx, ts)
			}); err != nil {
				t.Fatalf("publishStaleAttributionBadges: %v", err)
			}
			if got := badgeCount(t, ctx, st, c.fromID, BadgeAttributionStale); got != c.wantCount {
				t.Errorf("признак = %d, want %d", got, c.wantCount)
			}
		})
	}

	// Различие raw/effective (D11): признак живёт на паре объект+слой, а не
	// на объекте одном. Владелец расширения (writeLayerFixture, "ext") ловит
	// честную потерю в СВОЁМ слое — при полном совпадении объекта две потери
	// разных слоёв обязаны остаться ДВУМЯ записями, а не слиться в одну.
	//
	// Второй слой в этом сценарии синтетический (в V1 один объект не может
	// получить факты из двух слоёв через реальный пайплайн, ADR-026) — но
	// схема (object_badge, PK object_id+badge+layer) это различие держит, и
	// функция обязана его не терять независимо от того, кто сегодня её так
	// вызывает.
	t.Run("слой не путается с базовым", func(t *testing.T) {
		layerSt := openTestStore(t)
		layerSvc := NewService(layerSt, "proj", writeLayerFixture(t, "ТоварыВРезерве"), nil, Config{})
		t.Cleanup(func() { layerSvc.Close() })
		if _, err := layerSvc.Reindex(ctx, ModeFull, ""); err != nil {
			t.Fatalf("Reindex(full): %v", err)
		}
		var extDocID int64
		err := layerSt.Read(ctx, func(tx *store.ReadTx) error {
			id, ok, err := tx.NodeID(metadataObjectIdentityKey(domain.ComponentID("ext"), "Document", "отгрузка"))
			if err != nil {
				return err
			}
			if !ok {
				t.Fatal("Document.Отгрузка компонента ext не найден")
			}
			extDocID = id
			return nil
		})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}

		extKey := store.ObjectDataEdgeKey{FromObjectID: extDocID, ToObjectID: 999, Kind: store.EdgeWritesRegister, Layer: "ext"}
		baseKey := store.ObjectDataEdgeKey{FromObjectID: extDocID, ToObjectID: 999, Kind: store.EdgeWritesRegister, Layer: "base"}
		ts := &txState{
			staleEdges: []store.ObjectDataEdgeDeps{
				{ObjectDataEdgeKey: extKey, FileIDs: []int64{301}},
				{ObjectDataEdgeKey: baseKey, FileIDs: []int64{302}},
			},
			publishedEdges:    map[store.ObjectDataEdgeKey]struct{}{},
			republishedOwners: map[int64]struct{}{},
			staleFiles:        map[int64]struct{}{},
		}
		if err := layerSt.Write(ctx, func(tx *store.WriteTx) error {
			return publishStaleAttributionBadges(tx, ts)
		}); err != nil {
			t.Fatalf("publishStaleAttributionBadges: %v", err)
		}

		badges := badgesOf(t, ctx, layerSt, extDocID)
		var extCount, baseCount int64 = -1, -1
		for _, b := range badges {
			if b.Badge != BadgeAttributionStale {
				continue
			}
			switch b.Layer {
			case "ext":
				extCount = b.Count
			case "base":
				baseCount = b.Count
			}
		}
		if extCount != 1 {
			t.Errorf("признак слоя ext = %d, want 1", extCount)
		}
		if baseCount != 1 {
			t.Errorf("признак слоя base = %d, want 1: слился с ext или потерян", baseCount)
		}
	})
}

// writeTwoIndependentModulesFixture — документ Икс пишет в ДВА независимых
// регистра через ДВА РАЗНЫХ общих модуля (не через один, как
// writeTwoEdgeFixture): правка одного из них не задевает файл другого, и
// каждый из двух связанных рёбер можно снести ОТДЕЛЬНЫМ прогоном. Заведена
// специально под сцену «владелец вне scope теряет два НЕЗАВИСИМЫХ ребра в
// двух разных прогонах» (дефект внешнего ревью после таска 07): ни один из
// готовых фикстур проекта эту сцену не собирает — writeTwoEdgeFixture сносит
// оба ребра ОДНОЙ правкой общего файла (TestStaleBadgeCountsEveryLostEdge).
func writeTwoIndependentModulesFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	register := func(name, uuid string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <AccumulationRegister uuid="` + uuid + `">
    <Properties>
      <Name>` + name + `</Name>
    </Properties>
  </AccumulationRegister>
</MetaDataObject>`
	}
	commonModule := func(name, uuid string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <CommonModule uuid="` + uuid + `">
    <Properties>
      <Name>` + name + `</Name>
      <Server>true</Server>
    </Properties>
  </CommonModule>
</MetaDataObject>`
	}
	files := map[string]string{
		"AccumulationRegisters/РегА.xml": register("РегА", "eeeeeeee-0000-0000-0000-000000000001"),
		"AccumulationRegisters/РегБ.xml": register("РегБ", "eeeeeeee-0000-0000-0000-000000000002"),
		"CommonModules/ДвижениеА.xml":    commonModule("ДвижениеА", "eeeeeeee-0000-0000-0000-000000000003"),
		"CommonModules/ДвижениеА/Ext/Module.bsl": `
Процедура ЗаписатьА(Движения) Экспорт
	Движения.РегА.Записать();
КонецПроцедуры
`,
		"CommonModules/ДвижениеБ.xml": commonModule("ДвижениеБ", "eeeeeeee-0000-0000-0000-000000000004"),
		"CommonModules/ДвижениеБ/Ext/Module.bsl": `
Процедура ЗаписатьБ(Движения) Экспорт
	Движения.РегБ.Записать();
КонецПроцедуры
`,
		"Documents/Икс.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Document uuid="eeeeeeee-0000-0000-0000-000000000005">
    <Properties>
      <Name>Икс</Name>
    </Properties>
  </Document>
</MetaDataObject>`,
		"Documents/Икс/Ext/ObjectModule.bsl": `
Процедура ОбработкаПроведения(Отказ, Режим)
	ДвижениеА.ЗаписатьА(Движения);
	ДвижениеБ.ЗаписатьБ(Движения);
КонецПроцедуры
`,
		// Нестатическая запись через переменную-набор (тот же приём, что у
		// writeGraphFixture, TestDynamicBadgePublished): даёт Иксу бейдж
		// has-dynamic, НЕ участвующий в сценарии рёбер этого теста. Нужен
		// только для проверки, что уборка scope has-dynamic не задевает
		// владельца, пока он вне scope (критерий приёмки про DeleteObjectBadges).
		"Documents/Икс/Ext/ManagerModule.bsl": `
Процедура Пересчитать() Экспорт
	Набор = РегистрыНакопления.РегА.СоздатьНаборЗаписей();
	Набор.Записать();
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

// TestStaleBadgeAccumulatesAcrossIndependentOutOfScopeLosses — дефект,
// найденный внешним ревью после закрытия таска 07 (3eced9a): владелец,
// теряющий СВОЁ ребро в прогоне, где он НЕ входит в scope has-dynamic/
// attribution-truncated (не переопубликовал собственный модуль, атрибуцией
// этого прохода не найден), должен НАКАПЛИВАТЬ признак attribution-stale, а
// не терять предыдущий счётчик под свежим InsertObjectBadge (замещение
// ON CONFLICT, store.InsertObjectBadge). Документ Икс теряет РАЗНЫЕ рёбра в
// ДВУХ последовательных прогонах через ДВА независимых общих модуля; ни в
// одном из них не редактируется собственный модуль Икс.
func TestStaleBadgeAccumulatesAcrossIndependentOutOfScopeLosses(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeTwoIndependentModulesFixture(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	docID := objectNodeID(t, ctx, st, "Document", "икс")
	if len(edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)) != 2 {
		t.Fatal("предпосылка теста: у Икса после полной сборки два ребра writes-register")
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Fatalf("признак после полной сборки = %d, want 0", got)
	}
	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 1 {
		t.Fatalf("предпосылка теста: has-dynamic у Икса после полной сборки = %d, want 1", got)
	}

	// Прогон N: из ДвижениеА убрана запись: ребро к РегА снесено. Икс НЕ
	// переопубликовывал свой модуль и этим прогоном не найден атрибуцией
	// (подниматься не от чего): он вне scope has-dynamic/attribution-truncated.
	editA := filepath.Join(root, filepath.FromSlash("CommonModules/ДвижениеА/Ext/Module.bsl"))
	mustWrite(t, editA, `
Процедура ЗаписатьА(Движения) Экспорт
	// запись в регистр убрана
КонецПроцедуры
`)
	touchFuture(t, editA)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental) после правки ДвижениеА: %v", err)
	}
	if len(edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)) != 1 {
		t.Fatal("предпосылка: ребро к РегА снесено правкой ДвижениеА")
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 1 {
		t.Fatalf("признак после потери первого ребра = %d, want 1", got)
	}
	// Икс вне scope этого прогона (DeleteObjectBadges зовётся только по
	// scope) — has-dynamic обязан остаться НЕТРОНУТЫМ, не сброситься в 0 и
	// не потеряться вместе с чисткой чужих объектов.
	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 1 {
		t.Errorf("has-dynamic после прогона N = %d, want 1: DeleteObjectBadges не должен был задеть Икса вне scope", got)
	}

	// Прогон N+1: та же правка, но у ВТОРОГО, независимого модуля — сносит
	// ДРУГОЕ ребро (к РегБ). Икс снова вне scope: свой модуль не трогал,
	// атрибуцией не найден (в ДвижениеБ больше нет факта, от которого
	// подниматься). Счётчик обязан стать 2, а не замениться единицей.
	editB := filepath.Join(root, filepath.FromSlash("CommonModules/ДвижениеБ/Ext/Module.bsl"))
	mustWrite(t, editB, `
Процедура ЗаписатьБ(Движения) Экспорт
	// запись в регистр убрана
КонецПроцедуры
`)
	touchFuture(t, editB)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental) после правки ДвижениеБ: %v", err)
	}
	if len(edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)) != 0 {
		t.Fatal("предпосылка: оба ребра сейчас сняты")
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 2 {
		t.Errorf("признак после потери второго независимого ребра = %d, want 2: потеряны ДВЕ разные связи в ДВУХ разных прогонах", got)
	}

	// Ни один из общих модулей не является FromObjectID ни одного рёбра
	// (§4/§5: узел графа — объект, чьё имя пишется в регистр, а не модуль,
	// через который идёт вызов) и не терял своего ребра ни разу за оба
	// прогона: у него нет ни старого, ни нового attribution-stale, и carry-
	// расширение carryScope НЕ обязано читать и переставлять пустоту как
	// строку — новая строка бейджа не должна возникать из воздуха.
	commonAID := objectNodeID(t, ctx, st, "CommonModule", "движениеа")
	commonBID := objectNodeID(t, ctx, st, "CommonModule", "движениеб")
	if got := badgeCount(t, ctx, st, commonAID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак у ДвижениеА = %d, want 0: общий модуль не владелец данных, бейджу неоткуда взяться", got)
	}
	if got := badgeCount(t, ctx, st, commonBID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак у ДвижениеБ = %d, want 0: общий модуль не владелец данных, бейджу неоткуда взяться", got)
	}
	if got := badgeCount(t, ctx, st, docID, resolve.BadgeHasDynamic); got != 1 {
		t.Errorf("has-dynamic после прогона N+1 = %d, want 1: Икс вне scope и здесь, DeleteObjectBadges не должен был его коснуться", got)
	}

	// Прогон N+2: записи возвращены в оба общих модуля, и тем же прогоном
	// переопубликован СОБСТВЕННЫЙ модуль Икса: причина снята, обе цепочки
	// пересчитываются заново, признак обязан вернуться к 0, а не остаться
	// задвоенным поверх старого счётчика.
	mustWrite(t, editA, `
Процедура ЗаписатьА(Движения) Экспорт
	Движения.РегА.Записать();
КонецПроцедуры
`)
	touchFuture(t, editA)
	mustWrite(t, editB, `
Процедура ЗаписатьБ(Движения) Экспорт
	Движения.РегБ.Записать();
КонецПроцедуры
`)
	touchFuture(t, editB)
	ownPath := filepath.Join(root, filepath.FromSlash("Documents/Икс/Ext/ObjectModule.bsl"))
	mustWrite(t, ownPath, `
Процедура ОбработкаПроведения(Отказ, Режим)
	// косметическая правка, вызовы не меняются
	ДвижениеА.ЗаписатьА(Движения);
	ДвижениеБ.ЗаписатьБ(Движения);
КонецПроцедуры
`)
	touchFuture(t, ownPath)
	if _, err := svc.Reindex(ctx, ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental) после правки собственного модуля: %v", err)
	}
	if got := len(edgesOfKind(t, ctx, st, docID, store.EdgeWritesRegister)); got != 2 {
		t.Errorf("рёбер writes-register после переопубликации владельца = %d, want 2: обе цепочки обязаны восстановиться", got)
	}
	if got := badgeCount(t, ctx, st, docID, BadgeAttributionStale); got != 0 {
		t.Errorf("признак после переопубликации владельца = %d, want 0: причина снята, не должно остаться задвоенным остатком", got)
	}
}
