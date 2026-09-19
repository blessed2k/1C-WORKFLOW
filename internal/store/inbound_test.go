package store

import (
	"context"
	"sort"
	"testing"
)

// TestInboundKindsCoverSetNullColumns: каждый столбец схемы с ON DELETE SET
// NULL на строку-узел, которую удаление файла пересоздаёт (symbol,
// metadata_object, metadata_member), обязан быть в inboundKinds. Пропущенный
// столбец — тот же дефект issue #10 для нового вида указателя: правка файла
// цели молча обнулит его в нетронутых файлах (ADR-037).
func TestInboundKindsCoverSetNullColumns(t *testing.T) {
	s := openTestStore(t, Options{})
	targets := map[string]bool{"symbol": true, "metadata_object": true, "metadata_member": true}
	var want []string
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		rows, err := tx.c.query(tx.ctx, `SELECT m.name, f."from", f."table", f.on_delete
			FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f
			WHERE m.type='table'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var table, col, target, onDelete string
			if err := rows.Scan(&table, &col, &target, &onDelete); err != nil {
				return err
			}
			if onDelete == "SET NULL" && targets[target] {
				want = append(want, table+"."+col)
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("в схеме не найдено ни одного SET NULL на узлы: выборка сломана, проверка ничего не значит")
	}
	have := map[string]bool{}
	for _, k := range inboundKinds {
		have[k.name] = true
	}
	sort.Strings(want)
	for _, col := range want {
		if !have[col] {
			t.Errorf("столбец %s (ON DELETE SET NULL на узел) не входит в inboundKinds", col)
		}
		delete(have, col)
	}
	for extra := range have {
		t.Errorf("inboundKinds содержит %s, которого нет среди SET NULL-столбцов схемы", extra)
	}
}
