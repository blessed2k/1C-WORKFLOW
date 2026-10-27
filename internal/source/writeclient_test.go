package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteClientDo: the client posts JSON to the action path under the service
// root with basic auth and the envelope header, and decodes the answer.
func TestWriteClientDo(t *testing.T) {
	var gotPath, gotUser, gotEnvelope string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, _, _ = r.BasicAuth()
		gotEnvelope = r.Header.Get(errorEnvelopeHeader)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ref": "r1"})
	}))
	defer srv.Close()

	var out struct {
		OK  bool   `json:"ok"`
		Ref string `json:"ref"`
	}
	c := NewWriteClient(srv.URL+"/base/hs/mcp-write/", "агент", "секрет")
	if err := c.Do(context.Background(), WritePathCreate, map[string]any{"type": "Справочник.Контрагенты"}, &out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/base/hs/mcp-write/create" || gotUser != "агент" || gotEnvelope != "envelope" {
		t.Errorf("request: path %q user %q envelope %q", gotPath, gotUser, gotEnvelope)
	}
	if gotBody["type"] != "Справочник.Контрагенты" || !out.OK || out.Ref != "r1" {
		t.Errorf("body %v, answer %+v", gotBody, out)
	}
}

// TestWriteClientEnvelopeError: an error the extension reports in the envelope
// comes back as ConnectorError with its status and code.
func TestWriteClientEnvelopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"status": 409, "code": "locked", "message": "Объект заблокирован"}})
	}))
	defer srv.Close()

	err := NewWriteClient(srv.URL, "", "").Do(context.Background(), WritePathUpdate, map[string]any{}, nil)
	var ce *ConnectorError
	if !errors.As(err, &ce) || ce.Status != 409 || ce.Code != "locked" || ce.Path != "/update" {
		t.Fatalf("err = %v", err)
	}
}
