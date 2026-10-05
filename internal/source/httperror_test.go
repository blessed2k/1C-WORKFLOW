package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The web server in front of 1C (IIS) replaces the body of a 4xx/5xx answer with its own
// page, so the text of a 1C exception never reaches the client. A connector that sees the
// X-MCP-Errors header answers HTTP 200 with an error envelope instead.
func TestHTTPSourceConnectorErrors(t *testing.T) {
	cases := []struct {
		name       string
		httpStatus int
		body       string
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:       "envelope inside HTTP 200 carries the 1C text",
			httpStatus: http.StatusOK,
			body:       `{"error":{"status":400,"code":"query_failed","message":"Поле не найдено \"Док.Нет\""}}`,
			wantStatus: 400,
			wantCode:   "query_failed",
			wantText:   `Поле не найдено "Док.Нет"`,
		},
		{
			name:       "envelope names a query timeout",
			httpStatus: http.StatusOK,
			body:       `{"error":{"status":408,"code":"query_timeout","message":"Запрос выполнялся дольше 30 с и прерван"}}`,
			wantStatus: 408,
			wantCode:   "query_timeout",
			wantText:   "дольше 30 с",
		},
		{
			name:       "older connector: HTTP status with a text body",
			httpStatus: http.StatusBadRequest,
			body:       "Ошибка выполнения запроса: Синтаксическая ошибка",
			wantStatus: 400,
			wantCode:   "",
			wantText:   "Синтаксическая ошибка",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotHeader string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("X-MCP-Errors")
				w.WriteHeader(tc.httpStatus)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			s := NewHTTPSource(srv.URL, "", "")

			_, err := s.ExecuteQuery(context.Background(), QueryParams{Text: "ВЫБРАТЬ 1"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if gotHeader != "envelope" {
				t.Errorf("X-MCP-Errors = %q, want envelope: the connector answers with an envelope only when asked", gotHeader)
			}
			var ce *ConnectorError
			if !errors.As(err, &ce) {
				t.Fatalf("error %v is not a *ConnectorError", err)
			}
			if ce.Status != tc.wantStatus || ce.Code != tc.wantCode {
				t.Errorf("status, code = %d, %q; want %d, %q", ce.Status, ce.Code, tc.wantStatus, tc.wantCode)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not carry %q", err.Error(), tc.wantText)
			}
		})
	}
}

// A successful answer whose JSON merely has no error must not be mistaken for an envelope.
func TestHTTPSourceSuccessIsNotAnEnvelope(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	if _, err := s.MetadataTree(context.Background()); err != nil {
		t.Fatalf("MetadataTree: %v", err)
	}
	if _, err := s.ValidateQuery(context.Background(), "ВЫБРАТЬ 1"); err != nil {
		t.Fatalf("ValidateQuery (its answer has an errors array): %v", err)
	}
}
