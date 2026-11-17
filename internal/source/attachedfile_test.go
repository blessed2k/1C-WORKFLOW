package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeFileConnector answers GET /file the way the connector does and /configuration
// with the given features.
func fakeFileConnector(t *testing.T, features []string, file http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/configuration", func(w http.ResponseWriter, r *http.Request) {
		answer := map[string]any{"name": "Бухгалтерия"}
		if features != nil {
			answer["connector"] = map[string]any{"version": "0.3.0.0", "features": features}
		}
		_ = json.NewEncoder(w).Encode(answer)
	})
	if file != nil {
		mux.HandleFunc("/file/", file)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPSourceAttachedFile(t *testing.T) {
	pdf := []byte("%PDF-1.4 счёт поставщика")
	var gotPath, gotEnvelope, gotUser string
	srv := fakeFileConnector(t, []string{featureFileDownload}, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotEnvelope = r.Header.Get("X-MCP-Errors")
		gotUser, _, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="file.pdf"; filename*=UTF-8''`+
			url.PathEscape("Счёт 17 от 01.11.pdf"))
		_, _ = w.Write(pdf)
	})
	s := NewHTTPSource(srv.URL, "reader", "secret")

	got, err := s.AttachedFile(context.Background(), "Справочник.Файлы", "0f7b8c1e-1111-2222-3333-444455556666")
	if err != nil {
		t.Fatalf("AttachedFile: %v", err)
	}
	if !bytes.Equal(got.Data, pdf) {
		t.Errorf("data = %q, want %q", got.Data, pdf)
	}
	if got.Name != "Счёт 17 от 01.11.pdf" {
		t.Errorf("name = %q, want the decoded filename*, not the ASCII stand-in", got.Name)
	}
	if got.ContentType != "application/pdf" {
		t.Errorf("content type = %q", got.ContentType)
	}
	if gotPath != "/file/Справочник.Файлы/0f7b8c1e-1111-2222-3333-444455556666" {
		t.Errorf("path = %q", gotPath)
	}
	if gotEnvelope != "envelope" || gotUser != "reader" {
		t.Errorf("X-MCP-Errors = %q, user = %q: the request must ask for the envelope and authenticate", gotEnvelope, gotUser)
	}
}

func TestAttachmentFileName(t *testing.T) {
	cases := []struct{ header, want string }{
		{`attachment; filename="file.pdf"; filename*=UTF-8''%D0%A1%D1%87%D1%91%D1%82.pdf`, "Счёт.pdf"},
		// 1C may leave ( ) and ' as they are: mime rejects them, the fallback decodes.
		{`attachment; filename="file.pdf"; filename*=UTF-8''%D0%A1%D1%87%D0%B5%D1%82%20(1).pdf`, "Счет (1).pdf"},
		{`attachment; filename="file.pdf"; filename*=UTF-8''O'Brien%20(2).pdf`, "O'Brien (2).pdf"},
		{`attachment; filename="file.pdf"`, "file.pdf"},
		{"", ""},
		{"attachment; filename*=UTF-8''%zz", ""},
	}
	for _, tc := range cases {
		if got := attachmentFileName(tc.header); got != tc.want {
			t.Errorf("attachmentFileName(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestHTTPSourceAttachedFileErrors(t *testing.T) {
	cases := []struct {
		name       string
		features   []string
		handler    http.HandlerFunc
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:     "envelope from the connector",
			features: []string{featureFileDownload},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_, _ = w.Write([]byte(`{"error":{"status":400,"code":"not_a_file","message":"Тип Документ.Счет не справочник присоединённых файлов"}}`))
			},
			wantStatus: 400,
			wantCode:   "not_a_file",
			wantText:   "не справочник присоединённых файлов",
		},
		{
			name:     "404 from a connector that has the feature stays a 404",
			features: []string{featureFileDownload},
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Файл не найден", http.StatusNotFound)
			},
			wantStatus: 404,
			wantText:   "Файл не найден",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewHTTPSource(fakeFileConnector(t, tc.features, tc.handler).URL, "", "")
			_, err := s.AttachedFile(context.Background(), "Справочник.Файлы", "0f7b8c1e-1111-2222-3333-444455556666")
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

// A connector older than GET /file answers 404 for the unknown address; the client
// must say the connector needs an upgrade, not that the file is missing.
func TestHTTPSourceAttachedFileOlderConnector(t *testing.T) {
	for _, features := range [][]string{{"error_envelope", "query_refs"}, nil} {
		s := NewHTTPSource(fakeFileConnector(t, features, nil).URL, "", "")
		_, err := s.AttachedFile(context.Background(), "Справочник.Файлы", "0f7b8c1e-1111-2222-3333-444455556666")
		if err == nil || !strings.Contains(err.Error(), "не умеет отдавать файлы") {
			t.Errorf("features %v: error %v, want the upgrade hint", features, err)
		}
		var ce *ConnectorError
		if !errors.As(err, &ce) || ce.Status != http.StatusNotFound {
			t.Errorf("features %v: the original 404 must stay wrapped, got %v", features, err)
		}
	}
}

func TestHTTPSourceAttachedFileTooLarge(t *testing.T) {
	srv := fakeFileConnector(t, []string{featureFileDownload}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(make([]byte, maxAttachedFileBytes+1))
	})
	_, err := NewHTTPSource(srv.URL, "", "").AttachedFile(context.Background(), "Справочник.Файлы", "x")
	if err == nil || !strings.Contains(err.Error(), "МБ") {
		t.Errorf("error %v, want the size limit", err)
	}
}

func TestHTTPSourceAttachedFileNeedsTypeAndRef(t *testing.T) {
	s := NewHTTPSource("http://127.0.0.1:1", "", "")
	if _, err := s.AttachedFile(context.Background(), "", "ref"); err == nil {
		t.Error("empty type accepted")
	}
	if _, err := s.AttachedFile(context.Background(), "Справочник.Файлы", ""); err == nil {
		t.Error("empty ref accepted")
	}
}
