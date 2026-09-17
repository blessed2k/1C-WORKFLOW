package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockMCPService emulates the MCPService HTTP-service of the connector extension.
type mockMCPService struct {
	server   *httptest.Server
	lastUser string
	lastPass string
	lastPath string
	lastBody map[string]any
	hits     int
}

func newMockMCPService(t *testing.T) *mockMCPService {
	m := &mockMCPService{}
	mux := http.NewServeMux()

	record := func(r *http.Request) {
		m.hits++
		m.lastUser, m.lastPass, _ = r.BasicAuth()
		m.lastPath = r.URL.Path
		m.lastBody = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&m.lastBody)
		}
	}

	mux.HandleFunc("/configuration", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{
			"name": "БоеваяБаза", "synonym": "Боевая база", "version": "1.2.3", "vendor": "ООО Ромашка",
			"platform_version": "8.3.27.1", "mode": "server",
		})
	})
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{
			"Справочники": []string{"Контрагенты", "Валюты"},
			"Документы":   []string{"РеализацияТоваров"},
			"HTTPСервисы": []string{"MCPService"},
		})
	})
	mux.HandleFunc("/object/", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{
			"name": "Контрагенты", "synonym": "Контрагенты",
			"attributes": []any{
				map[string]any{"name": "ИНН", "type": []string{"Строка"}},
				map[string]any{"name": "Банк", "type": []string{"Справочник.Банки"}},
			},
			"tabularParts": []any{
				map[string]any{"name": "КонтактныеЛица", "attributes": []any{
					map[string]any{"name": "ФИО", "type": []string{"Строка"}},
				}},
			},
			"forms":    []string{"ФормаЭлемента", "ФормаСписка"},
			"commands": []string{"Команда1"},
		})
	})
	mux.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{
			"columns":   []string{"Ссылка", "Наименование"},
			"rows":      []any{[]any{"abc", "Тест"}},
			"total":     1,
			"truncated": false,
		})
	})
	mux.HandleFunc("/validate-query", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "errors": []string{"нет поля Ссылка1"}})
	})
	mux.HandleFunc("/eventlog", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{
			"events": []any{map[string]any{
				"date": "2026-07-13T10:00:00", "level": "Ошибка", "user": "Админ", "comment": "боом",
			}},
			"total": 1,
		})
	})

	mux.HandleFunc("/subsystem/", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{
			"name": "Продажи", "synonym": "Продажи",
			"subsystems": []string{"Отчеты"},
			"content":    []string{"Справочник.Контрагенты", "Документ.РеализацияТоваров"},
		})
	})
	mux.HandleFunc("/predefined/", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{
			"items": []any{
				map[string]any{"name": "Основной", "ref": "Валюты: Рубль"},
				map[string]any{"name": "Доллар", "ref": "Валюты: Доллар"},
			},
			"total": 2,
		})
	})
	// Эндпоинта /query-analyze у коннектора нет: анализ текста запроса считает сам клиент.

	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func TestHTTPSourceConfigurationInfo(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "admin", "secret")

	info, err := s.ConfigurationInfo(context.Background())
	if err != nil {
		t.Fatalf("ConfigurationInfo: %v", err)
	}
	if info.Name != "БоеваяБаза" || info.Version != "1.2.3" || info.Vendor != "ООО Ромашка" {
		t.Errorf("info = %+v", info)
	}
	if info.PlatformVersion != "8.3.27.1" || info.Mode != "server" {
		t.Errorf("platform/mode = %q/%q", info.PlatformVersion, info.Mode)
	}
	if info.Synonym != "Боевая база" {
		t.Errorf("synonym = %q", info.Synonym)
	}
	if m.lastUser != "admin" || m.lastPass != "secret" {
		t.Errorf("basic auth = %q:%q", m.lastUser, m.lastPass)
	}
}

func TestHTTPSourceMetadataTree(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	tree, err := s.MetadataTree(context.Background())
	if err != nil {
		t.Fatalf("MetadataTree: %v", err)
	}
	if tree.TotalObjects != 4 {
		t.Errorf("total = %d, want 4", tree.TotalObjects)
	}
	cat := findGroup(tree.Groups, "Catalog")
	if cat == nil || len(cat.Objects) != 2 || cat.Objects[0] != "Валюты" || cat.Objects[1] != "Контрагенты" {
		t.Errorf("Catalog group = %+v, want [Валюты Контрагенты] (sorted)", cat)
	}
	if findGroup(tree.Groups, "HTTPService") == nil {
		t.Errorf("HTTPСервисы not mapped to HTTPService; groups: %+v", tree.Groups)
	}
}

func TestHTTPSourceObjectStructure(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	obj, err := s.ObjectStructure(context.Background(), "Catalog", "Контрагенты")
	if err != nil {
		t.Fatalf("ObjectStructure: %v", err)
	}
	if obj.Type != "Catalog" || obj.Name != "Контрагенты" {
		t.Errorf("type/name = %q/%q", obj.Type, obj.Name)
	}
	// Client escapes Cyrillic; the server decodes it back, so type and name
	// arrive intact (as 1C would receive them in ПараметрыURL).
	if m.lastPath != "/object/Catalog/Контрагенты" {
		t.Errorf("decoded path = %q, want /object/Catalog/Контрагенты", m.lastPath)
	}
	if len(obj.Attributes) != 2 || obj.Attributes[1].Name != "Банк" || obj.Attributes[1].Type[0] != "Справочник.Банки" {
		t.Errorf("attributes = %+v", obj.Attributes)
	}
	if len(obj.TabularSections) != 1 || obj.TabularSections[0].Name != "КонтактныеЛица" {
		t.Errorf("tabular sections = %+v", obj.TabularSections)
	}
	if len(obj.Forms) != 2 || len(obj.Commands) != 1 {
		t.Errorf("forms=%v commands=%v", obj.Forms, obj.Commands)
	}
}

func TestHTTPSourceExecuteQuery(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	res, err := s.ExecuteQuery(context.Background(), QueryParams{Text: "ВЫБРАТЬ Ссылка ИЗ Справочник.Контрагенты"})
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if res.Count != 1 || len(res.Rows) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Rows[0]["Наименование"] != "Тест" || res.Rows[0]["Ссылка"] != "abc" {
		t.Errorf("row re-keyed wrong: %+v", res.Rows[0])
	}
	if m.lastBody["query"] != "ВЫБРАТЬ Ссылка ИЗ Справочник.Контрагенты" {
		t.Errorf("posted query = %v", m.lastBody["query"])
	}
}

func TestHTTPSourceExecuteQueryRejectsNonSelect(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	if _, err := s.ExecuteQuery(context.Background(), QueryParams{Text: "УНИЧТОЖИТЬ ВТ"}); err == nil {
		t.Fatal("expected non-SELECT to be rejected")
	}
	if m.hits != 0 {
		t.Errorf("connector hit %d times; a rejected query must never reach the base", m.hits)
	}
}

func TestHTTPSourceValidateQuery(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	res, err := s.ValidateQuery(context.Background(), "ВЫБРАТЬ Ссылка1")
	if err != nil {
		t.Fatalf("ValidateQuery: %v", err)
	}
	if res.Valid || res.Error != "нет поля Ссылка1" {
		t.Errorf("result = %+v", res)
	}
}

func TestHTTPSourceEventLog(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	res, err := s.EventLog(context.Background(), EventLogParams{Level: "Error", Limit: 50})
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	if res.Count != 1 || res.Entries[0].User != "Админ" {
		t.Errorf("result = %+v", res)
	}
	if m.lastBody["level"] != "Ошибка" {
		t.Errorf("level in body = %v, want Ошибка (English mapped to Russian)", m.lastBody["level"])
	}
}

func TestHTTPSourceSubsystem(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	res, err := s.Subsystem(context.Background(), "Продажи")
	if err != nil {
		t.Fatalf("Subsystem: %v", err)
	}
	if res.Name != "Продажи" || len(res.Subsystems) != 1 || len(res.Content) != 2 {
		t.Errorf("subsystem = %+v", res)
	}
	if m.lastPath != "/subsystem/Продажи" {
		t.Errorf("path = %q", m.lastPath)
	}
}

func TestHTTPSourcePredefined(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	res, err := s.Predefined(context.Background(), "Catalog", "Валюты")
	if err != nil {
		t.Fatalf("Predefined: %v", err)
	}
	if res.Count != 2 || res.Items[0].Name != "Основной" || res.Items[0].Ref != "Валюты: Рубль" {
		t.Errorf("predefined = %+v", res)
	}
	if m.lastPath != "/predefined/Catalog/Валюты" {
		t.Errorf("path = %q", m.lastPath)
	}
}

// AnalyzeQuery is answered locally by the shared text analyser, so the base is
// never contacted: the mock must stay untouched.
func TestHTTPSourceAnalyzeQuery(t *testing.T) {
	m := newMockMCPService(t)
	s := NewHTTPSource(m.server.URL, "", "")

	res, err := s.AnalyzeQuery(context.Background(),
		"ВЫБРАТЬ * ИЗ Справочник.Контрагенты КАК К ВНУТРЕННЕЕ СОЕДИНЕНИЕ (ВЫБРАТЬ 1 КАК П) КАК П ПО П.П = 1")
	if err != nil {
		t.Fatalf("AnalyzeQuery: %v", err)
	}
	codes := map[string]string{}
	for _, w := range res.Warnings {
		codes[w.Code] = w.Severity
	}
	if res.Count != len(res.Warnings) || codes["SelectStar"] != "medium" || codes["JoinWithSubquery"] != "high" {
		t.Errorf("analysis = %+v", res)
	}
	if m.hits != 0 {
		t.Errorf("connector was called %d times, expected none", m.hits)
	}
}

func TestHTTPSourceHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"база недоступна"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	s := NewHTTPSource(srv.URL, "", "")

	if _, err := s.ConfigurationInfo(context.Background()); err == nil {
		t.Fatal("expected an error on HTTP 500")
	}
}

func TestHTTPSourceOfflineOnlyMethods(t *testing.T) {
	s := NewHTTPSource("http://unused", "", "")
	if _, err := s.SearchCode(context.Background(), SearchParams{Query: "x"}); err == nil {
		t.Error("SearchCode should be offline-only in live mode")
	}
	if _, err := s.FormStructure(context.Background(), "Catalog", "X", "Ф"); err == nil {
		t.Error("FormStructure should be offline-only in live mode")
	}
}

func TestFlexTypes(t *testing.T) {
	var arr flexTypes
	if err := json.Unmarshal([]byte(`["Строка","Число"]`), &arr); err != nil || len(arr) != 2 {
		t.Errorf("array form: %v %v", arr, err)
	}
	var one flexTypes
	if err := json.Unmarshal([]byte(`"СправочникСсылка.Валюты"`), &one); err != nil || len(one) != 1 || one[0] != "СправочникСсылка.Валюты" {
		t.Errorf("string form: %v %v", one, err)
	}
	var empty flexTypes
	if err := json.Unmarshal([]byte(`""`), &empty); err != nil || len(empty) != 0 {
		t.Errorf("empty string form: %v %v", empty, err)
	}
}

func TestEnsureSelect(t *testing.T) {
	ok := []string{"ВЫБРАТЬ 1", "выбрать Ссылка", "// c\n\nВЫБРАТЬ 1", "SELECT 1"}
	for _, q := range ok {
		if err := ensureSelect(q); err != nil {
			t.Errorf("ensureSelect(%q) = %v", q, err)
		}
	}
	// Ключевое слово проверяется как целое слово: идентификатор, начинающийся с ВЫБРАТЬ,
	// не должен проходить за SELECT.
	for _, q := range []string{"УНИЧТОЖИТЬ ВТ", "", "  \n  ", "ВЫБРАТЬЧТОУГОДНО 1", "SELECTED 1"} {
		if err := ensureSelect(q); err == nil {
			t.Errorf("ensureSelect(%q) = nil, want error", q)
		}
	}
}
