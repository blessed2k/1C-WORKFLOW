package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestErrorBodyText: a web-server page keeps only its title, plain text from
// the connector stays as is, and a long body is cut.
func TestErrorBodyText(t *testing.T) {
	page := "<!DOCTYPE html>\r\n<html><head><title>IIS 10.0 Detailed Error -\r\n 404.0 - Not Found</title>" +
		"<style>body{}</style></head><body>" + strings.Repeat("<div>x</div>", 200) + "</body></html>"
	long := strings.Repeat("я", maxErrorBodyRunes+10)
	for _, tc := range []struct {
		name, contentType, body, want string
	}{
		{"iis page", "text/html", page, "IIS 10.0 Detailed Error - 404.0 - Not Found (страница веб-сервера, разметка опущена)"},
		{"html without content type", "", page, "IIS 10.0 Detailed Error - 404.0 - Not Found (страница веб-сервера, разметка опущена)"},
		{"html without title", "text/html; charset=utf-8", "<html><body>oops</body></html>", "страница веб-сервера без текста ошибки (разметка опущена)"},
		{"connector text", "text/plain; charset=utf-8", "  Запрос должен начинаться с ВЫБРАТЬ  ", "Запрос должен начинаться с ВЫБРАТЬ"},
		{"long text", "text/plain", long, strings.Repeat("я", maxErrorBodyRunes) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorBodyText(tc.contentType, []byte(tc.body)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWriteClientWebServerPage: an IIS page in place of the service answer
// becomes a ConnectorError with the status and the page title only.
func TestWriteClientWebServerPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><head><title>404 - File or directory not found.</title></head><body><h2>long page</h2></body></html>"))
	}))
	defer srv.Close()

	err := NewWriteClient(srv.URL, "", "").Do(context.Background(), WritePathCreate, map[string]any{}, nil)
	var ce *ConnectorError
	if !errors.As(err, &ce) || ce.Status != http.StatusNotFound ||
		ce.Message != "404 - File or directory not found. (страница веб-сервера, разметка опущена)" {
		t.Fatalf("err = %v", err)
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
