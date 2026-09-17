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

// Синтетическая фикстура из двух компонентов (§9, история 35): конфигурация и
// расширение с AppliesTo, ОДИН И ТОТ ЖЕ путь модуля документа в обоих слоях.
// В ut_demo расширений нет, поэтому слой в рёбрах доказывается только так.
//
// Отступление от буквы §9 («фикстура строится типизированными Insert*/Ensure*»)
// сознательное: здесь проверяется доезд слоя ЧЕРЕЗ ПУБЛИКАТОР, от
// register_access.layer до object_data_edge.layer. Фикстура через Insert*
// расставила бы слои руками и обошла бы ровно проверяемый участок — доказывала
// бы, что store умеет хранить колонку, а не что пайплайн её заполняет. Причина
// записана в ADR-026.
//
// extRegister — какой регистр пишет модуль расширения. Параметр и есть мутация
// теста: тот же путь модуля с ДРУГИМ регистром обязан дать другой набор рёбер,
// а с тем же — тот же. Проверка, зелёная при обоих значениях, ничего бы не
// доказывала.
func writeLayerFixture(t *testing.T, extRegister string) workspace.Manifest {
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
	document := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Document uuid="88888888-8888-8888-8888-888888888888">
    <Properties>
      <Name>Отгрузка</Name>
    </Properties>
  </Document>
</MetaDataObject>`
	objectModule := func(regName string) string {
		return "\nПроцедура ОбработкаПроведения(Отказ, Режим)\n\tДвижения." + regName + ".Записать();\nКонецПроцедуры\n"
	}

	files := map[string]string{
		"cfg/Documents/Отгрузка.xml":                    document,
		"cfg/Documents/Отгрузка/Ext/ObjectModule.bsl":   objectModule("ТоварыНаСкладах"),
		"cfg/AccumulationRegisters/ТоварыНаСкладах.xml": register("ТоварыНаСкладах", "aaaaaaaa-0000-0000-0000-000000000001"),
		"cfg/AccumulationRegisters/ТоварыВРезерве.xml":  register("ТоварыВРезерве", "aaaaaaaa-0000-0000-0000-000000000002"),
		"ext/Documents/Отгрузка.xml":                    document,
		"ext/Documents/Отгрузка/Ext/ObjectModule.bsl":   objectModule(extRegister),
		"ext/AccumulationRegisters/ТоварыНаСкладах.xml": register("ТоварыНаСкладах", "aaaaaaaa-0000-0000-0000-000000000001"),
		"ext/AccumulationRegisters/ТоварыВРезерве.xml":  register("ТоварыВРезерве", "aaaaaaaa-0000-0000-0000-000000000002"),
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
	return workspace.Manifest{
		Version: 1, Project: "proj", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: "cfg", AbsRoot: filepath.Join(root, "cfg")},
			{ID: "ext", Kind: domain.KindExtension, Root: "ext", AbsRoot: filepath.Join(root, "ext"),
				AppliesTo: "cfg", ApplyOrder: 1},
		},
	}
}

// registersWrittenInLayer — имена регистров, в которые документ пишет в
// заданном слое, по данным объектного графа.
func registersWrittenInLayer(t *testing.T, ctx context.Context, st *store.Store, component, layer string) []string {
	t.Helper()
	var out []string
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		docID, ok, err := tx.NodeID(metadataObjectIdentityKey(domain.ComponentID(component), "Document", "отгрузка"))
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("документ Отгрузка компонента %s не найден", component)
		}
		edges, err := tx.ObjectDataEdges(store.ObjectEdgeFilter{
			ObjectID: docID, Direction: store.EdgeDirectionOut,
			Kinds: []string{store.EdgeWritesRegister}, Layer: layer, Limit: 100,
		})
		if err != nil {
			return err
		}
		for _, e := range edges {
			row, ok, err := tx.MetadataObjectByID(e.ToObjectID)
			if err != nil {
				return err
			}
			if ok {
				out = append(out, row.NameDisplay)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return out
}

// TestObjectEdgeLayersRawVsEffective — истории 6 и 35 (R08, R34, R35): слой в
// рёбрах работает. Один и тот же путь модуля в конфигурации и в расширении
// даёт РАЗНЫЕ рёбра, и различие видно фильтром по layer.
func TestObjectEdgeLayersRawVsEffective(t *testing.T) {
	run := func(t *testing.T, extRegister string) (base, ext []string) {
		t.Helper()
		ctx := context.Background()
		st := openTestStore(t)
		svc := NewService(st, "proj", writeLayerFixture(t, extRegister), nil, Config{})
		t.Cleanup(func() { svc.Close() })
		if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
			t.Fatalf("Reindex(full): %v", err)
		}
		return registersWrittenInLayer(t, ctx, st, "cfg", "base"),
			registersWrittenInLayer(t, ctx, st, "ext", "ext")
	}

	t.Run("расширение пишет другой регистр", func(t *testing.T) {
		base, ext := run(t, "ТоварыВРезерве")
		if len(base) != 1 || base[0] != "ТоварыНаСкладах" {
			t.Fatalf("слой base = %v, want [ТоварыНаСкладах]", base)
		}
		if len(ext) != 1 || ext[0] != "ТоварыВРезерве" {
			t.Fatalf("слой ext = %v, want [ТоварыВРезерве]", ext)
		}
		if base[0] == ext[0] {
			t.Errorf("raw и effective дали один набор рёбер (%v): слой в рёбрах ничего не различает", base)
		}
	})

	t.Run("расширение пишет тот же регистр", func(t *testing.T) {
		base, ext := run(t, "ТоварыНаСкладах")
		if len(base) != 1 || len(ext) != 1 {
			t.Fatalf("слои = %v и %v, want по одному ребру", base, ext)
		}
		// Мутация фикстуры: тот же регистр — те же рёбра. Без этой половины
		// первая проверяла бы, что слои различаются ВСЕГДА, то есть что
		// layer шумит, а не что он содержателен.
		if base[0] != ext[0] {
			t.Errorf("слои разошлись (%v против %v) на одинаковом коде модуля", base, ext)
		}
	})
}
