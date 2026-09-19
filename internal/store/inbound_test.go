package store

import (
	"context"
	"sort"
	"testing"
)

// TestInboundKindsCoverSetNullColumns: каждый столбец схемы с ON DELETE SET
// NULL на строку-узел, которую удаление файла пересоздаёт (symbol,
// metadata_object, metadata_member), обязан быть в setNullKinds. Пропущенный
// столбец означает тот же дефект issue #10 для нового вида указателя: правка файла
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
	for _, k := range setNullKinds {
		have[k.name()] = true
	}
	// Мягкий указатель без REFERENCES: столбец обязан существовать и не иметь
	// FK, иначе SET NULL сделал бы SQLite, а явное обнуление в
	// DeleteSourceFiles было бы лишним (issue #14).
	for _, k := range softKinds {
		checkNoFKColumn(t, s, k)
	}
	sort.Strings(want)
	for _, col := range want {
		if !have[col] {
			t.Errorf("столбец %s (ON DELETE SET NULL на узел) не входит в setNullKinds", col)
		}
		delete(have, col)
	}
	for extra := range have {
		t.Errorf("setNullKinds содержит %s, которого нет среди SET NULL-столбцов схемы", extra)
	}
}

func checkNoFKColumn(t *testing.T, s *Store, k inboundKind) {
	t.Helper()
	var cols, fks int
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		if err := tx.c.sc.QueryRowContext(tx.ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name=?`,
			k.table, k.column).Scan(&cols); err != nil {
			return err
		}
		return tx.c.sc.QueryRowContext(tx.ctx, `SELECT count(*) FROM pragma_foreign_key_list(?) WHERE "from"=?`,
			k.table, k.column).Scan(&fks)
	}); err != nil {
		t.Fatal(err)
	}
	if cols != 1 {
		t.Errorf("столбца %s (softKinds) нет в схеме", k.name())
	}
	if fks != 0 {
		t.Errorf("у столбца %s есть REFERENCES, а вид лежит в softKinds", k.name())
	}
}

// TestCascadeKindsCoverCrossFileCascades: каждый столбец схемы с ON DELETE
// CASCADE на строку, которую удаление файла сносит (прямо или каскадом), обязан
// быть либо видом cascadeKinds (строка другого файла возвращается после
// переопубликования), либо в sameFileCascades (строка того же файла и уходит
// законно). Пропущенный столбец означает дефект issue #11 для новой таблицы:
// правка файла-владельца молча снесёт факты нетронутых файлов (ADR-038).
func TestCascadeKindsCoverCrossFileCascades(t *testing.T) {
	s := openTestStore(t, Options{})
	type fk struct{ table, col, target string }
	var cascades []fk
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		rows, err := tx.c.query(tx.ctx, `SELECT m.name, f."from", f."table"
			FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f
			WHERE m.type='table' AND f.on_delete='CASCADE'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c fk
			if err := rows.Scan(&c.table, &c.col, &c.target); err != nil {
				return err
			}
			cascades = append(cascades, c)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	// Таблицы, строки которых удаление source_file сносит каскадом.
	deleted := map[string]bool{"source_file": true}
	for grown := true; grown; {
		grown = false
		for _, c := range cascades {
			if deleted[c.target] && !deleted[c.table] {
				deleted[c.table], grown = true, true
			}
		}
	}
	var want []string
	for _, c := range cascades {
		if c.target != "source_file" && deleted[c.target] {
			want = append(want, c.table+"."+c.col)
		}
	}
	if len(want) == 0 {
		t.Fatal("в схеме не найдено ни одного каскада на строки файла: выборка сломана, проверка ничего не значит")
	}
	have := map[string]bool{}
	for _, k := range cascadeKinds {
		for _, c := range k.covers {
			have[c] = true
		}
	}
	for c := range sameFileCascades {
		if have[c] {
			t.Errorf("%s одновременно в cascadeKinds и sameFileCascades", c)
		}
		have[c] = true
	}
	sort.Strings(want)
	for _, col := range want {
		if !have[col] {
			t.Errorf("столбец %s (ON DELETE CASCADE на строку файла) не входит ни в cascadeKinds, ни в sameFileCascades", col)
		}
		delete(have, col)
	}
	for extra := range have {
		t.Errorf("%s объявлен, но среди каскадов на строки файла его нет", extra)
	}
}
