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

// A connector older than the envelope answers a failed query with a bare HTTP 400;
// the text is recovered from /validate-query and comes back as query_failed.
func TestHTTPSourceBareQueryFailureUsesValidation(t *testing.T) {
	cases := []struct {
		name         string
		queryStatus  int
		queryBody    string
		validateBody string
		wantCode     string
		wantText     string
		wantValidate bool
	}{
		{
			name:         "bare 400: the compile error comes from validate-query",
			queryStatus:  http.StatusBadRequest,
			queryBody:    "Неправильный запрос",
			validateBody: `{"valid":false,"errors":["{(1, 30)}: Ожидается имя\nИЗ Справочник.Товары КАК <<?>>В"]}`,
			wantCode:     "query_failed",
			wantText:     "Ожидается имя",
			wantValidate: true,
		},
		{
			name:         "bare 400 on a query that compiles: the original error plus the likely cause",
			queryStatus:  http.StatusBadRequest,
			queryBody:    "Неправильный запрос",
			validateBody: `{"valid":true,"errors":[]}`,
			wantCode:     "",
			wantText:     "запрос компилируется",
			wantValidate: true,
		},
		{
			name:         "an envelope error is not second-guessed",
			queryStatus:  http.StatusOK,
			queryBody:    `{"error":{"status":400,"code":"bad_request","message":"Параметр Дата: неизвестный тип"}}`,
			validateBody: `{"valid":false,"errors":["не должна вызываться"]}`,
			wantCode:     "bad_request",
			wantText:     "неизвестный тип",
			wantValidate: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validated := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/validate-query") {
					validated = true
					_, _ = w.Write([]byte(tc.validateBody))
					return
				}
				w.WriteHeader(tc.queryStatus)
				_, _ = w.Write([]byte(tc.queryBody))
			}))
			t.Cleanup(srv.Close)
			s := NewHTTPSource(srv.URL, "", "")

			_, err := s.ExecuteQuery(context.Background(), QueryParams{Text: "ВЫБРАТЬ 1 ИЗ Справочник.Товары КАК В"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if validated != tc.wantValidate {
				t.Errorf("validate-query called = %v, want %v", validated, tc.wantValidate)
			}
			var ce *ConnectorError
			if !errors.As(err, &ce) {
				t.Fatalf("error %v is not a *ConnectorError", err)
			}
			if ce.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", ce.Code, tc.wantCode)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not carry %q", err.Error(), tc.wantText)
			}
		})
	}
}

// IIS writes its error pages in the server codepage (windows-1251 on a Russian
// Windows); the title has to come out readable, not as replacement characters.
func TestErrorBodyTextDecodesWindows1251Title(t *testing.T) {
	// "500 - Внутренняя ошибка сервера." in windows-1251.
	title := []byte{'5', '0', '0', ' ', '-', ' ', 0xC2, 0xED, 0xF3, 0xF2, 0xF0, 0xE5, 0xED, 0xED, 0xFF, 0xFF, ' ',
		0xEE, 0xF8, 0xE8, 0xE1, 0xEA, 0xE0, ' ', 0xF1, 0xE5, 0xF0, 0xE2, 0xE5, 0xF0, 0xE0, '.'}
	page := append([]byte(`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN"><html><head>`+
		`<meta http-equiv="Content-Type" content="text/html; charset=windows-1251"/><title>`), title...)
	page = append(page, []byte(`</title><style>body{margin:0}</style></head><body><h2>...</h2></body></html>`)...)

	got := errorBodyText("text/html", page)
	if !strings.HasPrefix(got, "500 - Внутренняя ошибка сервера.") {
		t.Errorf("title not decoded: %q", got)
	}
	if strings.Contains(got, "<") || strings.Contains(got, "margin") {
		t.Errorf("markup leaked into the error: %q", got)
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
