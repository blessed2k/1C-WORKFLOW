package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The connector interrupts a query only when the client names a time limit, so the limit
// has to reach the body of /query; without one the connector runs the query as before.
func TestHTTPSourceSendsQueryTimeout(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		want    any // nil: the field must be absent
	}{
		{"off by default", 0, nil},
		{"whole seconds", 30 * time.Second, float64(30)},
		{"a fraction of a second rounds up, never down to no limit", 500 * time.Millisecond, float64(1)},
		{"clamped below the HTTP timeout of the client", 20 * time.Minute, float64(240)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ConfigureLive(tc.timeout)
			t.Cleanup(func() { ConfigureLive(0) })
			m := newMockMCPService(t)
			s := NewHTTPSource(m.server.URL, "", "")

			if _, err := s.ExecuteQuery(context.Background(), QueryParams{Text: "ВЫБРАТЬ 1"}); err != nil {
				t.Fatalf("ExecuteQuery: %v", err)
			}
			got, present := m.lastBody["timeout"]
			if tc.want == nil {
				if present {
					t.Errorf("timeout = %v, want the field absent", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("timeout = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHTTPSourceReportsQueryDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"columns": []string{"Число"}, "rows": []any{[]any{1}}, "total": 1, "duration_ms": 1234,
		})
	}))
	t.Cleanup(srv.Close)
	s := NewHTTPSource(srv.URL, "", "")

	res, err := s.ExecuteQuery(context.Background(), QueryParams{Text: "ВЫБРАТЬ 1"})
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if res.DurationMS != 1234 {
		t.Errorf("DurationMS = %d, want 1234", res.DurationMS)
	}
}
