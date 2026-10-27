package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// контрактыЗаписи: the write tools exist only under --tools=write, so they are
// pinned apart from контрактыИнструментов, with the same strictness: exact
// required inputs and outputs, typed input fields.
var контрактыЗаписи = []контрактИнструмента{
	{
		имя:              "create_object",
		входОбязательные: []string{"type", "ref"},
		входПоля: map[string]string{"base": "string", "type": "string", "ref": "string", "attributes": "object",
			"tabular_sections": "object", "mode": "string", "dry_run": "boolean"},
	},
	{
		имя:              "update_object",
		входОбязательные: []string{"type", "ref"},
		входПоля: map[string]string{"base": "string", "type": "string", "ref": "string", "attributes": "object",
			"tabular_sections": "object", "mode": "string", "dry_run": "boolean"},
	},
	{
		имя:              "post_document",
		входОбязательные: []string{"type", "ref"},
		входПоля:         map[string]string{"base": "string", "type": "string", "ref": "string", "dry_run": "boolean"},
	},
	{
		имя:              "unpost_document",
		входОбязательные: []string{"type", "ref"},
		входПоля:         map[string]string{"base": "string", "type": "string", "ref": "string", "dry_run": "boolean"},
	},
	{
		имя:              "set_deletion_mark",
		входОбязательные: []string{"type", "ref", "value"},
		входПоля: map[string]string{"base": "string", "type": "string", "ref": "string", "value": "boolean",
			"dry_run": "boolean"},
	},
	{
		имя:              "set_constant",
		входОбязательные: []string{"name", "value"},
		входПоля:         map[string]string{"base": "string", "name": "string", "dry_run": "boolean"},
	},
	{
		имя:              "write_register_record",
		входОбязательные: []string{"type"},
		входПоля: map[string]string{"base": "string", "type": "string", "period": "string", "dimensions": "object",
			"resources": "object", "attributes": "object", "dry_run": "boolean"},
	},
	{
		имя:              "delete_register_record",
		входОбязательные: []string{"type"},
		входПоля: map[string]string{"base": "string", "type": "string", "period": "string", "dimensions": "object",
			"dry_run": "boolean"},
	},
}

// выходЗаписи is the answer shape every write tool shares.
var выходЗаписиОбязательные = []string{"base", "ok", "written", "dry_run"}

// fakeWriteService stands in for the MCPЗапись extension: it records each call
// and answers with the body the test sets for the path.
type fakeWriteService struct {
	mu      sync.Mutex
	paths   []string
	bodies  []map[string]any
	headers []http.Header
	users   []string
	answer  map[string]any
}

func newFakeWriteService(t *testing.T) (*fakeWriteService, *httptest.Server) {
	t.Helper()
	f := &fakeWriteService{answer: map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		user, _, _ := r.BasicAuth()
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())
		f.users = append(f.users, user)
		answer := f.answer[r.URL.Path]
		f.mu.Unlock()
		if answer == nil {
			answer = map[string]any{"ok": true, "written": true, "dry_run": false}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeWriteService) last(t *testing.T) (string, map[string]any, http.Header, string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) == 0 {
		t.Fatal("the write service was not called")
	}
	i := len(f.paths) - 1
	return f.paths[i], f.bodies[i], f.headers[i], f.users[i]
}

func writeOptions(baseURL string) options {
	return options{toolsProfile: profileWrite, baseURL: baseURL, user: "агент"}
}

func callWrite(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, writeOutput) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out writeOutput
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: decode output: %v", name, err)
		}
	}
	return res, out
}

// TestWriteProfileSurface: the write server registers server_info, the base
// switching and the eight write tools, nothing else, in live and offline start.
func TestWriteProfileSurface(t *testing.T) {
	want := append([]string{"server_info", "list_bases", "set_base"}, writeToolNames...)
	sort.Strings(want)
	for _, v := range []struct {
		name string
		opts options
	}{
		{"live", writeOptions("http://localhost/base/hs/mcp-write")},
		{"no-base", options{toolsProfile: profileWrite}},
		{"with-projects-root", options{toolsProfile: profileWrite, projectsRoot: t.TempDir()}},
	} {
		t.Run(v.name, func(t *testing.T) {
			got := registeredToolNames(t, v.opts)
			if !slices.Equal(got, want) {
				t.Errorf("tools = %v, want %v", got, want)
			}
		})
	}
}

// TestWriteToolsNotInReadProfiles: full and core never expose a write tool.
func TestWriteToolsNotInReadProfiles(t *testing.T) {
	for _, режим := range []режимИнструмента{режимОффлайн, режимLive} {
		for _, профиль := range []toolsProfile{profileFull, profileCore} {
			for _, имя := range registeredToolNames(t, optionsFor(t, режим, профиль, реестрОткрыт)) {
				if slices.Contains(writeToolNames, имя) {
					t.Errorf("profile %s registers write tool %q", профиль, имя)
				}
			}
		}
	}
}

// TestКонтрактИнструментовЗаписи pins input and output schemas of the write tools.
func TestКонтрактИнструментовЗаписи(t *testing.T) {
	список, err := сессияКлиента(t, writeOptions("http://localhost/base/hs/mcp-write")).
		ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	объявленные := map[string]*mcp.Tool{}
	for _, инструмент := range список.Tools {
		объявленные[инструмент.Name] = инструмент
	}
	if len(контрактыЗаписи) != len(writeToolNames) {
		t.Fatalf("контрактов %d, инструментов записи %d", len(контрактыЗаписи), len(writeToolNames))
	}
	for _, контракт := range контрактыЗаписи {
		инструмент := объявленные[контракт.имя]
		if инструмент == nil {
			t.Errorf("%s не зарегистрирован", контракт.имя)
			continue
		}
		обязательные, поля := схемаЗаписи(t, инструмент.InputSchema)
		сверитьОбязательные(t, "вход", контракт.имя, контракт.входОбязательные, обязательные)
		for имя, тип := range контракт.входПоля {
			тело, есть := поля[имя]
			if !есть {
				t.Errorf("%s: нет входного поля %q", контракт.имя, имя)
				continue
			}
			if фактический := типПоля(t, тело); фактический != тип {
				t.Errorf("%s: входное поле %q: тип %q, ожидается %q", контракт.имя, имя, фактический, тип)
			}
		}
		if инструмент.OutputSchema == nil {
			t.Fatalf("%s: нет выходной схемы", контракт.имя)
		}
		выходОбязательные, _ := схемаЗаписи(t, инструмент.OutputSchema)
		сверитьОбязательные(t, "выход", контракт.имя, выходЗаписиОбязательные, выходОбязательные)
		if len([]rune(инструмент.Description)) < 40 {
			t.Errorf("%s: короткое описание", контракт.имя)
		}
	}
}

// схемаЗаписи is разобратьСхему without typing every field: a value of any JSON
// type (set_constant.value, number, code) has the boolean schema true, which
// типПоля cannot read and the contract does not pin.
func схемаЗаписи(t *testing.T, схема any) (map[string]bool, map[string]json.RawMessage) {
	t.Helper()
	сырая, err := json.Marshal(схема)
	if err != nil {
		t.Fatal(err)
	}
	var разобранная struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(сырая, &разобранная); err != nil {
		t.Fatal(err)
	}
	обязательные := map[string]bool{}
	for _, поле := range разобранная.Required {
		обязательные[поле] = true
	}
	return обязательные, разобранная.Properties
}

// TestWriteToolsPostBodyToTheirAction: each tool posts to its path, with the
// input minus base as the body, basic auth and the error envelope header.
func TestWriteToolsPostBodyToTheirAction(t *testing.T) {
	f, srv := newFakeWriteService(t)
	cs := сессияКлиента(t, writeOptions(srv.URL))
	ref := "6f1c3e9a-1111-4222-8333-944455556666"
	for _, tc := range []struct {
		tool string
		args map[string]any
		path string
		body map[string]any
	}{
		{"create_object", map[string]any{"type": "Справочник.Контрагенты", "ref": ref,
			"attributes": map[string]any{"Наименование": "Ромашка"}, "dry_run": true},
			"/create", map[string]any{"type": "Справочник.Контрагенты", "ref": ref,
				"attributes": map[string]any{"Наименование": "Ромашка"}, "dry_run": true}},
		{"update_object", map[string]any{"type": "Документ.Заказ", "ref": ref, "mode": "post",
			"tabular_sections": map[string]any{"Товары": []any{map[string]any{"Количество": 2}}}},
			"/update", map[string]any{"type": "Документ.Заказ", "ref": ref, "mode": "post",
				"tabular_sections": map[string]any{"Товары": []any{map[string]any{"Количество": float64(2)}}}}},
		{"post_document", map[string]any{"type": "Документ.Заказ", "ref": ref},
			"/post", map[string]any{"type": "Документ.Заказ", "ref": ref}},
		{"unpost_document", map[string]any{"type": "Документ.Заказ", "ref": ref, "dry_run": true},
			"/unpost", map[string]any{"type": "Документ.Заказ", "ref": ref, "dry_run": true}},
		{"set_deletion_mark", map[string]any{"type": "Документ.Заказ", "ref": ref, "value": false},
			"/deletion-mark", map[string]any{"type": "Документ.Заказ", "ref": ref, "value": false}},
		{"set_constant", map[string]any{"name": "ЗаголовокСистемы", "value": nil},
			"/constant", map[string]any{"name": "ЗаголовокСистемы", "value": nil}},
		{"write_register_record", map[string]any{"type": "РегистрСведений.Курсы", "period": "2026-10-27",
			"dimensions": map[string]any{"Валюта": map[string]any{"type": "Справочник.Валюты", "ref": ref}},
			"resources":  map[string]any{"Курс": 95.5}},
			"/register-write", map[string]any{"type": "РегистрСведений.Курсы", "period": "2026-10-27",
				"dimensions": map[string]any{"Валюта": map[string]any{"type": "Справочник.Валюты", "ref": ref}},
				"resources":  map[string]any{"Курс": 95.5}}},
		{"delete_register_record", map[string]any{"type": "РегистрСведений.Курсы", "period": "2026-10-27"},
			"/register-delete", map[string]any{"type": "РегистрСведений.Курсы", "period": "2026-10-27"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			res, out := callWrite(t, cs, tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("tool error: %s", contentText(res))
			}
			path, body, header, user := f.last(t)
			if path != tc.path {
				t.Errorf("path = %s, want %s", path, tc.path)
			}
			gotBody, _ := json.Marshal(body)
			wantBody, _ := json.Marshal(tc.body)
			if string(gotBody) != string(wantBody) {
				t.Errorf("body = %s, want %s", gotBody, wantBody)
			}
			if header.Get("X-MCP-Errors") != "envelope" {
				t.Errorf("no error envelope header")
			}
			if user != "агент" {
				t.Errorf("basic auth user = %q", user)
			}
			if out.Base != "default" || !out.OK {
				t.Errorf("output = %+v", out)
			}
		})
	}
}

// TestWriteToolPassesTheAnswerThrough: the fields of the extension answer reach
// the agent, including a refused write that is not a tool error.
func TestWriteToolPassesTheAnswerThrough(t *testing.T) {
	f, srv := newFakeWriteService(t)
	f.answer["/post"] = map[string]any{
		"type": "Документ.Заказ", "ref": "r1", "ok": true, "written": false, "dry_run": true, "posted": true,
		"movements": map[string]any{"РегистрНакопления.Товары": []any{map[string]any{"Количество": 2}}},
	}
	f.answer["/create"] = map[string]any{
		"type": "Справочник.Контрагенты", "ref": "r2", "created": true, "ok": false, "written": false,
		"dry_run": false, "messages": []any{map[string]any{"text": "Не заполнено наименование", "field": "Наименование"}},
	}
	cs := сессияКлиента(t, writeOptions(srv.URL))

	res, out := callWrite(t, cs, "post_document", map[string]any{"type": "Документ.Заказ", "ref": "r1", "dry_run": true})
	if res.IsError || out.Posted == nil || !*out.Posted || len(out.Movements["РегистрНакопления.Товары"]) != 1 {
		t.Errorf("post dry_run output = %+v (%s)", out, contentText(res))
	}
	res, out = callWrite(t, cs, "create_object", map[string]any{"type": "Справочник.Контрагенты", "ref": "r2"})
	if res.IsError {
		t.Fatalf("a refused write must not be a tool error: %s", contentText(res))
	}
	if out.OK || len(out.Messages) != 1 || out.Messages[0].Field != "Наименование" || out.Created == nil || !*out.Created {
		t.Errorf("create output = %+v", out)
	}
}

// TestWriteToolReportsTheEnvelope: an input error of the extension comes back
// as a tool error with its status, code and text, and the base it went to.
func TestWriteToolReportsTheEnvelope(t *testing.T) {
	f, srv := newFakeWriteService(t)
	f.answer["/update"] = map[string]any{"error": map[string]any{
		"status": 404, "code": "not_found", "message": "Объект Документ.Заказ со ссылкой r3 не найден."}}
	cs := сессияКлиента(t, writeOptions(srv.URL))
	res, _ := callWrite(t, cs, "update_object", map[string]any{"type": "Документ.Заказ", "ref": "r3"})
	if !res.IsError {
		t.Fatal("an envelope error must be a tool error")
	}
	text := contentText(res)
	for _, want := range []string{"base default", "404", "not_found", "не найден"} {
		if !strings.Contains(text, want) {
			t.Errorf("error %q does not name %q", text, want)
		}
	}
}

// TestWriteToolBaseResolution: base= picks a base for one call, a call without
// it uses the active base, and with several bases and none active the error
// names the choice.
func TestWriteToolBaseResolution(t *testing.T) {
	fa, srvA := newFakeWriteService(t)
	fb, srvB := newFakeWriteService(t)
	file := filepath.Join(t.TempDir(), "bases.json")
	list, _ := json.Marshal([]baseConfig{{Name: "тест-а", URL: srvA.URL}, {Name: "тест-б", URL: srvB.URL}})
	if err := os.WriteFile(file, list, 0o600); err != nil {
		t.Fatal(err)
	}
	cs := сессияКлиента(t, options{toolsProfile: profileWrite, basesFile: file})
	args := map[string]any{"type": "Документ.Заказ", "ref": "r"}

	res, _ := callWrite(t, cs, "post_document", args)
	if !res.IsError || !strings.Contains(contentText(res), "тест-а") || !strings.Contains(contentText(res), "тест-б") {
		t.Fatalf("no active base: want an error naming the bases, got %s", contentText(res))
	}

	res, out := callWrite(t, cs, "post_document", map[string]any{"type": "Документ.Заказ", "ref": "r", "base": "тест-б"})
	if res.IsError || out.Base != "тест-б" || len(fb.paths) != 1 || len(fa.paths) != 0 {
		t.Fatalf("base=тест-б: %s %+v", contentText(res), out)
	}

	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "set_base",
		Arguments: map[string]any{"name": "тест-а"}}); err != nil {
		t.Fatal(err)
	}
	res, out = callWrite(t, cs, "post_document", args)
	if res.IsError || out.Base != "тест-а" || len(fa.paths) != 1 {
		t.Fatalf("active тест-а: %s %+v", contentText(res), out)
	}

	res, _ = callWrite(t, cs, "post_document", map[string]any{"type": "Документ.Заказ", "ref": "r", "base": "прод"})
	if !res.IsError || !strings.Contains(contentText(res), "прод") {
		t.Fatalf("unknown base: %s", contentText(res))
	}
}

// TestWriteInstructions: the write server routes its tools, names nothing it
// does not register and fits the client limit.
func TestWriteInstructions(t *testing.T) {
	cs := сессияКлиента(t, writeOptions("http://localhost/base/hs/mcp-write"))
	got := cs.InitializeResult().Instructions
	if n := len([]rune(got)); n > instructionsLimit {
		t.Fatalf("instructions are %d runes, limit %d", n, instructionsLimit)
	}
	for _, want := range append([]string{"WRITE DATA", "dry_run"}, writeToolNames...) {
		if !strings.Contains(got, want) {
			t.Errorf("instructions do not route %q:\n%s", want, got)
		}
	}
	for _, к := range контрактыИнструментов {
		if к.имя != "server_info" && к.имя != "set_base" && к.имя != "list_bases" && mentions(got, к.имя) {
			t.Errorf("write instructions name read tool %q", к.имя)
		}
	}
}
