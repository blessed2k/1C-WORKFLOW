package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// eventLogServer answers /eventlog with the given body and remembers what it was asked.
func eventLogServer(t *testing.T, answer map[string]any) (*HTTPSource, *map[string]any) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return NewHTTPSource(srv.URL, "", ""), &body
}

func TestHTTPSourceEventLogFiltersAndFields(t *testing.T) {
	ref := map[string]any{"type": "Документ.Счет", "ref": "6042d8d4-a3d7-4285-9eb4-ae8a86aa2e9d"}
	s, body := eventLogServer(t, map[string]any{
		"events": []any{map[string]any{
			"date": "2026-09-01T10:00:00", "level": "Информация", "user": "Иванов",
			"event": "Данные. Добавление", "event_id": "_$Data$_.New",
			"computer": "PC-1", "application": "Тонкий клиент", "application_id": "1CV8C",
			"session": 704, "connection": 12,
			"metadata": "Счет", "metadata_id": "Документ.Счет",
			"data":              map[string]any{"presentation": "Счет 15", "type": "Документ.Счет", "ref": ref["ref"]},
			"data_presentation": "Счет 15 от 01.09.2026", "comment": "текст",
			"transaction_status": "Зафиксирована", "transaction": "01.09.2026 10:00:00 (5)",
		}},
		"total": 1, "truncated": true,
		"applied": []string{"user", "events", "metadata", "sessions", "applications", "computer", "data",
			"data_presentation", "comment", "transaction_status", "transaction", "order", "offset"},
	})

	res, err := s.EventLog(context.Background(), EventLogParams{
		User: "Иванов", Events: []string{"_$Data$_.New"}, Metadata: []string{"Документ.Счет"},
		Sessions: []int{704}, Applications: []string{"1CV8C"}, Computer: "PC-1", Data: ref,
		DataPresentation: "Счет 15 от 01.09.2026", Comment: "текст",
		TransactionStatus: "Committed", Transaction: "01.09.2026 10:00:00 (5)",
		Order: "asc", Offset: 20, MaxComment: 500, Limit: 10,
	})
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}

	sent := *body
	for key, want := range map[string]any{
		"user": "Иванов", "computer": "PC-1", "data_presentation": "Счет 15 от 01.09.2026", "comment": "текст",
		"transaction_status": "Committed", "transaction": "01.09.2026 10:00:00 (5)",
		"order": "asc", "offset": float64(20), "max_comment": float64(500), "limit": float64(10),
	} {
		if sent[key] != want {
			t.Errorf("body[%q] = %v, want %v", key, sent[key], want)
		}
	}
	for _, key := range []string{"events", "metadata", "sessions", "applications"} {
		if list, ok := sent[key].([]any); !ok || len(list) != 1 {
			t.Errorf("body[%q] = %v, want a one-element array", key, sent[key])
		}
	}
	if data, _ := sent["data"].(map[string]any); data["ref"] != ref["ref"] {
		t.Errorf("body[data] = %v, want the reference object", sent["data"])
	}

	if len(res.Entries) != 1 || !res.Truncated {
		t.Fatalf("result = %+v", res)
	}
	e := res.Entries[0]
	if e.EventID != "_$Data$_.New" || e.Computer != "PC-1" || e.Application != "Тонкий клиент" ||
		e.ApplicationID != "1CV8C" || e.Session != 704 || e.Connection != 12 ||
		e.MetadataID != "Документ.Счет" || e.DataPresentation != "Счет 15 от 01.09.2026" ||
		e.TransactionStatus != "Зафиксирована" || e.Transaction != "01.09.2026 10:00:00 (5)" {
		t.Errorf("entry lost fields: %+v", e)
	}
	if data, _ := e.Data.(map[string]any); data["ref"] != ref["ref"] {
		t.Errorf("entry data = %v, want the reference object", e.Data)
	}
}

// A connector older than the new filters ignores them and answers with the whole window.
// An answer wider than asked for must not pass for a filtered one.
func TestHTTPSourceEventLogRejectsIgnoredFilter(t *testing.T) {
	cases := []struct {
		name    string
		applied []string // nil: the connector does not report applied filters at all
		params  EventLogParams
		ignored string // empty: no error expected
	}{
		{"old connector, old filters only", nil, EventLogParams{User: "Иванов", Level: "Error"}, ""},
		{"old connector, new filter", nil, EventLogParams{Events: []string{"_$Data$_.New"}}, "events"},
		{"old connector, order", nil, EventLogParams{Order: "asc"}, "order"},
		{"new connector applied everything", []string{"sessions", "offset"}, EventLogParams{Sessions: []int{7}, Offset: 5}, ""},
		{"new connector skipped one", []string{"sessions"}, EventLogParams{Sessions: []int{7}, Comment: "x"}, "comment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer := map[string]any{"events": []any{}, "total": 0}
			if tc.applied != nil {
				answer["applied"] = tc.applied
			}
			s, _ := eventLogServer(t, answer)

			_, err := s.EventLog(context.Background(), tc.params)
			if tc.ignored == "" {
				if err != nil {
					t.Fatalf("EventLog: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.ignored) {
				t.Fatalf("error = %v, want it to name the ignored filter %q", err, tc.ignored)
			}
		})
	}
}

func TestHTTPSourceEventLogSendsTimeout(t *testing.T) {
	ConfigureLive(30 * time.Second)
	t.Cleanup(func() { ConfigureLive(0) })
	s, body := eventLogServer(t, map[string]any{"events": []any{}, "total": 0})

	if _, err := s.EventLog(context.Background(), EventLogParams{}); err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	if (*body)["timeout"] != float64(30) {
		t.Errorf("timeout = %v, want 30", (*body)["timeout"])
	}
}

func TestHTTPSourceConfigurationReportsConnector(t *testing.T) {
	cases := []struct {
		name           string
		answer         map[string]any
		wantVersion    string
		wantFeature    string
		wantExtensions int
		wantError      string
	}{
		{
			name: "connector that describes itself",
			answer: map[string]any{
				"name": "База", "version": "3.0.1",
				"connector": map[string]any{"version": "0.3.0.0", "features": []string{"error_envelope", "query_timeout"}},
				"extensions": []any{
					map[string]any{"name": "Доработки", "synonym": "Доработки", "version": "1.2", "active": true, "safe_mode": false},
					map[string]any{"name": "Профиль", "version": "2", "active": false, "safe_mode": "ПрофильБезопасности"},
				},
			},
			wantVersion: "0.3.0.0", wantFeature: "query_timeout", wantExtensions: 2,
		},
		{
			name:   "older connector says nothing about itself",
			answer: map[string]any{"name": "База", "version": "3.0.1"},
		},
		{
			name: "extensions are not available to this user",
			answer: map[string]any{
				"name": "База", "connector": map[string]any{"version": "0.3.0.0"},
				"extensions_error": "Нарушение прав доступа",
			},
			wantVersion: "0.3.0.0", wantError: "Нарушение прав доступа",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(tc.answer)
			}))
			t.Cleanup(srv.Close)

			info, err := NewHTTPSource(srv.URL, "", "").ConfigurationInfo(context.Background())
			if err != nil {
				t.Fatalf("ConfigurationInfo: %v", err)
			}
			if tc.wantVersion == "" {
				if info.Connector != nil {
					t.Errorf("Connector = %+v, want nil for a connector that does not describe itself", info.Connector)
				}
			} else if info.Connector == nil || info.Connector.Version != tc.wantVersion {
				t.Fatalf("Connector = %+v, want version %s", info.Connector, tc.wantVersion)
			}
			if tc.wantFeature != "" && !info.Connector.Has(tc.wantFeature) {
				t.Errorf("features = %v, want %s among them", info.Connector.Features, tc.wantFeature)
			}
			if len(info.Extensions) != tc.wantExtensions {
				t.Errorf("extensions = %+v, want %d", info.Extensions, tc.wantExtensions)
			}
			if tc.wantExtensions == 2 && (info.Extensions[0].Name != "Доработки" || !info.Extensions[0].Active || info.Extensions[1].SafeMode != "ПрофильБезопасности") {
				t.Errorf("extensions decoded wrong: %+v", info.Extensions)
			}
			if info.ExtensionsError != tc.wantError {
				t.Errorf("ExtensionsError = %q, want %q", info.ExtensionsError, tc.wantError)
			}
		})
	}
}

// failingSettingsRunner fails the combined settings query the way a base without the
// constant ИспользоватьУправлениеДоступом does, and answers every other query.
type failingSettingsRunner struct {
	failure error
	base    *scriptedRunner
}

func (r *failingSettingsRunner) ExecuteQuery(ctx context.Context, p QueryParams) (*QueryResult, error) {
	if strings.Contains(p.Text, keySettings) {
		return nil, r.failure
	}
	if strings.Contains(p.Text, "ОграничиватьДоступНаУровнеЗаписей") {
		return rows(map[string]any{"ПоЗаписям": true}), nil
	}
	return r.base.ExecuteQuery(ctx, p)
}

// The constant ИспользоватьУправлениеДоступом is not part of every configuration
// (Бухгалтерия предприятия has none), and a query naming a missing constant fails as a
// whole. access_profiles used to fail with it in every such base.
func TestAccessSettingsWithoutManagementConstant(t *testing.T) {
	base := &scriptedRunner{answers: map[string]*QueryResult{
		keyList: rows(map[string]any{"Профиль": "Бухгалтер", "ЧислоРолей": float64(120)}),
	}}
	cases := []struct {
		name    string
		failure error
		wantErr bool
	}{
		{"new connector: query_failed", &ConnectorError{Path: "/query", Status: 400, Code: "query_failed", Message: "Поле не найдено"}, false},
		{"old connector: HTTP 400 with the page of the web server", &ConnectorError{Path: "/query", Status: 400, Message: "Неправильный запрос"}, false},
		{"the base is unreachable: not a missing constant", errors.New("connector request /query: connection refused"), true},
		{"a timeout is not a missing constant either", &ConnectorError{Path: "/query", Status: 408, Code: "query_timeout", Message: "прерван"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := AccessProfiles(context.Background(), &failingSettingsRunner{failure: tc.failure, base: base}, AccessProfileOptions{})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected the error to be passed on")
				}
				return
			}
			if err != nil {
				t.Fatalf("AccessProfiles: %v", err)
			}
			if !rep.Settings.RecordLevel || rep.Settings.AccessManagement {
				t.Errorf("settings = %+v, want record level on and management off", rep.Settings)
			}
			if !strings.Contains(rep.Settings.Note, "ИспользоватьУправлениеДоступом") {
				t.Errorf("note = %q, want it to say the constant is missing", rep.Settings.Note)
			}
			if len(rep.Profiles) != 1 {
				t.Errorf("profiles = %+v, want the list to be read as usual", rep.Profiles)
			}
		})
	}
}
