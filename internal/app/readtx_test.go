package app

import (
	"context"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

const testStateDir = ".mcp1c-test"

func openFixtureStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.Options{ProjectID: "proj", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestReadTxSnapshotIsolatedFromConcurrentWrite — критерий приёмки тикета 10:
// «одна read-транзакция на вызов — инкремент в середине вызова не виден
// вызову». Тест реально коммитит write-транзакцию НА ТОМ ЖЕ store, пока
// ReadTx ещё открыт, и доказывает через настоящий store.Store (не мок), что
// снятый снапшот этого не видит: WAL-изоляция store.Store.Read, на которой
// стоит ReadTx, держит именно это свойство.
func TestReadTxSnapshotIsolatedFromConcurrentWrite(t *testing.T) {
	st := openFixtureStore(t)
	ctx := context.Background()

	before, err := ReadTx(ctx, st, func(tx *store.ReadTx) (int64, error) {
		return tx.GenerationNumber()
	})
	if err != nil {
		t.Fatalf("ReadTx (baseline): %v", err)
	}

	var duringTx, afterCommitStillInTx int64
	_, err = ReadTx(ctx, st, func(tx *store.ReadTx) (struct{}, error) {
		g, err := tx.GenerationNumber()
		if err != nil {
			return struct{}{}, err
		}
		duringTx = g

		// Коммитим независимую write-транзакцию на том же store, пока read-
		// транзакция ещё открыта: это и есть "инкремент в середине вызова".
		if err := st.Write(ctx, func(*store.WriteTx) error { return nil }); err != nil {
			return struct{}{}, err
		}

		g2, err := tx.GenerationNumber()
		afterCommitStillInTx = g2
		return struct{}{}, err
	})
	if err != nil {
		t.Fatalf("ReadTx (снапшот): %v", err)
	}

	if duringTx != before {
		t.Fatalf("duringTx = %d, want %d (baseline)", duringTx, before)
	}
	if afterCommitStillInTx != duringTx {
		t.Fatalf("afterCommitStillInTx = %d, want %d: конкурентный commit был виден внутри уже открытой read-транзакции",
			afterCommitStillInTx, duringTx)
	}

	after, err := ReadTx(ctx, st, func(tx *store.ReadTx) (int64, error) {
		return tx.GenerationNumber()
	})
	if err != nil {
		t.Fatalf("ReadTx (после): %v", err)
	}
	if after != before+1 {
		t.Fatalf("after = %d, want %d: write-транзакция должна была реально закоммититься", after, before+1)
	}
}
