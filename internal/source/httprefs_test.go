package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A reference returned as its presentation cannot be passed to the next query, so the
// caller ends up searching by number or name. With refs the connector returns an object
// carrying the identifier, and the same object goes back as a query parameter.
func TestHTTPSourceQueryRefs(t *testing.T) {
	ref := map[string]any{"presentation": "Счет 15 от 30.09.2026", "type": "Документ.Счет", "ref": "6042d8d4-a3d7-4285-9eb4-ae8a86aa2e9d"}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"columns": []string{"Ссылка"}, "rows": []any{[]any{ref}}, "total": 1,
		})
	}))
	t.Cleanup(srv.Close)
	s := NewHTTPSource(srv.URL, "", "")

	res, err := s.ExecuteQuery(context.Background(), QueryParams{
		Text:   "ВЫБРАТЬ Док.Ссылка ИЗ Документ.Счет КАК Док ГДЕ Док.Ссылка = &Счет",
		Params: map[string]any{"Счет": ref},
		Refs:   true,
	})
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if body["refs"] != true {
		t.Errorf("refs in body = %v, want true", body["refs"])
	}
	sent, _ := body["parameters"].(map[string]any)["Счет"].(map[string]any)
	if sent["ref"] != ref["ref"] || sent["type"] != ref["type"] {
		t.Errorf("reference parameter reached the connector as %v", sent)
	}
	got, _ := res.Rows[0]["Ссылка"].(map[string]any)
	if got["ref"] != ref["ref"] || got["presentation"] != ref["presentation"] {
		t.Errorf("reference in the row = %v, want the object the connector returned", res.Rows[0]["Ссылка"])
	}
}

func TestHTTPSourceQueryRefsOffByDefault(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	if _, err := s.ExecuteQuery(context.Background(), QueryParams{Text: "ВЫБРАТЬ 1"}); err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if _, present := m.lastBody["refs"]; present {
		t.Errorf("refs = %v, want the field absent: the answer of an existing caller must not change shape", m.lastBody["refs"])
	}
}
