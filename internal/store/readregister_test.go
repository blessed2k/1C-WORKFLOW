package store

import (
	"context"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Слой доступа к регистру доезжает до читателя: без него raw и effective
// неразличимы (D11, docs/architecture-graph.md). Строка без явного слоя
// читается как base: тот же дефолт, что стоит в схеме.
func TestRegisterAccessKeepsLayer(t *testing.T) {
	s, f := seeded(t)
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.InsertRegisterAccess(RegisterAccess{
			FileID: f.fileModuleBSL, SymbolID: f.symB, RegisterNameNorm: "регистр",
			Mode: "write", Static: true, Confidence: 0.9, Layer: "ext-1",
			Span: domain.Span{StartByte: 20, EndByte: 30},
		})
	}); err != nil {
		t.Fatalf("вставка доступа со слоем расширения: %v", err)
	}
	var got []RegisterAccessRow
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		got, err = tx.RegisterAccesses(RegisterAccessFilter{RegisterNameNorm: "регистр"})
		return err
	}); err != nil {
		t.Fatalf("чтение доступов: %v", err)
	}
	layers := map[string]int{}
	for _, r := range got {
		layers[r.Layer]++
	}
	if layers["base"] != 1 || layers["ext-1"] != 1 {
		t.Fatalf("слои доступов %v, ожидались по одному base и ext-1", layers)
	}
}
