package retrieve

import (
	"encoding/json"
	"testing"
)

// TestResultJSONMarshalsCleanly — Result идёт наружу через MCP как
// structuredContent (go-sdk маршалит типизированный Out), проверяем, что
// сериализация не падает и не пустая на реальном сценарии.
func TestResultJSONMarshalsCleanly(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p"})
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("пустой JSON")
	}
	t.Logf("JSON длина: %d байт", len(data))
}
