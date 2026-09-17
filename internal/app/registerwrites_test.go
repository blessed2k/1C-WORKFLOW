package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestFindRegisterWritesEmptyIsArrayWithDeclaredHint: a register with no code
// writers answers items=[] (not null); when documents declare movements into it
// the answer says so and routes to object_graph, when nothing is declared it
// stays silent.
func TestFindRegisterWritesEmptyIsArrayWithDeclaredHint(t *testing.T) {
	graphSvc, _ := buildObjectGraphFixture(t)
	svc := NewRegisterService(graphSvc.projects)
	ctx := context.Background()

	declared, err := svc.FindRegisterWrites(ctx, FindRegisterWritesInput{Register: "СтавкиНалогов"})
	if err != nil {
		t.Fatalf("FindRegisterWrites(СтавкиНалогов): %v", err)
	}
	raw, err := json.Marshal(declared)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Errorf("пустой ответ не массив: %s", raw)
	}
	if declared.Items == nil || len(declared.Items) != 0 || declared.TotalCount != 0 {
		t.Errorf("items = %#v, totalCount = %d", declared.Items, declared.TotalCount)
	}
	var hint *Warning
	for i, w := range declared.Warnings {
		if w.Code == WarnRegisterWritesDeclaredOnly {
			hint = &declared.Warnings[i]
		}
	}
	if hint == nil {
		t.Fatalf("нет подсказки про объявленные движения: %+v", declared.Warnings)
	}
	for _, want := range []string{"кодовых записей нет", "1 объект", "object_graph", "direction=in", "СтавкиНалогов"} {
		if !strings.Contains(hint.Message+" "+hint.Hint, want) {
			t.Errorf("подсказка без %q: %+v", want, *hint)
		}
	}

	// ТоварыОрганизаций: в register_access тоже пусто, объявленных движений
	// нет (только кодовые рёбра графа): подсказки нет, список пустой.
	silent, err := svc.FindRegisterWrites(ctx, FindRegisterWritesInput{Register: "ТоварыОрганизаций"})
	if err != nil {
		t.Fatalf("FindRegisterWrites(ТоварыОрганизаций): %v", err)
	}
	if silent.Items == nil || len(silent.Items) != 0 {
		t.Errorf("items = %#v, want []", silent.Items)
	}
	for _, w := range silent.Warnings {
		if w.Code == WarnRegisterWritesDeclaredOnly {
			t.Errorf("подсказка без объявленных движений: %+v", w)
		}
	}

	// modes=read: подсказка про запись там не к месту.
	read, err := svc.FindRegisterWrites(ctx, FindRegisterWritesInput{Register: "СтавкиНалогов", Modes: "read"})
	if err != nil {
		t.Fatalf("FindRegisterWrites(read): %v", err)
	}
	for _, w := range read.Warnings {
		if w.Code == WarnRegisterWritesDeclaredOnly {
			t.Errorf("подсказка про запись при modes=read: %+v", w)
		}
	}
}

// TestFindRegisterWritesDeclaredHintNamesActualModesAndAllKinds: текст называет
// фактические modes, а при одном имени у регистров разных видов подсказка
// перечисляет все виды, а не последний попавшийся.
func TestFindRegisterWritesDeclaredHintNamesActualModesAndAllKinds(t *testing.T) {
	graphSvc, ids := buildObjectGraphFixture(t)
	ctx := context.Background()
	op, err := graphSvc.projects.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Второй регистр с тем же именем, другого вида, с объявленным движением от O5.
	err = op.Store.Write(ctx, func(tx *store.WriteTx) error {
		rel := "AccumulationRegisters/СтавкиНалогов.xml"
		hash, err := tx.PutBlob([]byte(rel))
		if err != nil {
			return err
		}
		fileID, err := tx.InsertSourceFile(store.SourceFile{
			ComponentID: "cfg", RelPath: rel, Size: int64(len(rel)), ContentHash: hash, ParserVersion: 1,
		})
		if err != nil {
			return err
		}
		regID, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00AccumulationRegister\x00ставкиналогов", ComponentID: "cfg",
			MType: "AccumulationRegister", NameNorm: "ставкиналогов", NameDisplay: "СтавкиНалогов", FileID: fileID,
		})
		if err != nil {
			return err
		}
		_, err = tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: ids.o5, ToObjectID: regID, Kind: store.EdgeWritesDeclared, Layer: "base",
			Provenance: store.EdgeProvenanceDeclared, Confidence: 0.5, Mode: "movement",
			Evidence: `{"xmlFile":"Documents/ПоступлениеТоваров.xml"}`, FileIDs: []int64{fileID},
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := NewRegisterService(graphSvc.projects).FindRegisterWrites(ctx,
		FindRegisterWritesInput{Register: "СтавкиНалогов", Modes: "write,read"})
	if err != nil {
		t.Fatal(err)
	}
	var hint *Warning
	for i, w := range resp.Warnings {
		if w.Code == WarnRegisterWritesDeclaredOnly {
			hint = &resp.Warnings[i]
		}
	}
	if hint == nil {
		t.Fatalf("нет подсказки: %+v", resp.Warnings)
	}
	if !strings.Contains(hint.Message, "modes=write,read") {
		t.Errorf("текст не называет фактические modes: %q", hint.Message)
	}
	if !strings.Contains(hint.Message, "2 объекта") {
		t.Errorf("объектов должно быть два (O1 и O5): %q", hint.Message)
	}
	for _, kind := range []string{"AccumulationRegister", "InformationRegister"} {
		if !strings.Contains(hint.Hint, kind) {
			t.Errorf("подсказка не называет вид %s: %q", kind, hint.Hint)
		}
	}
}
