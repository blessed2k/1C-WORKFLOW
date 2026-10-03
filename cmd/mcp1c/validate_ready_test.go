package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// readyStub answers the ready-methods lookup without an indexed project.
type readyStub struct {
	report app.ReadyMethodsReport
	warn   []app.Warning
	err    error
	in     app.ReadyMethodsInput
}

func (s *readyStub) ReadyMethods(_ context.Context, in app.ReadyMethodsInput) (app.Response[app.ReadyMethodsReport], error) {
	s.in = in
	if s.err != nil {
		return app.Response[app.ReadyMethodsReport]{}, s.err
	}
	return app.Response[app.ReadyMethodsReport]{Items: []app.ReadyMethodsReport{s.report}, Warnings: s.warn}, nil
}

func callValidateBSL(t *testing.T, ready readyMethodsFinder, args map[string]any) validateBSLOutput {
	t.Helper()
	cs := connectTools(t, func(srv *mcp.Server) {
		registerValidateBSL(srv, func() source.ConfigSource { return nil }, brokenCorpus{}, ready)
	})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "validate_bsl", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("validate_bsl: %v %s", err, contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out validateBSLOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

func checkedMentions(checked []string, text string) bool {
	for _, c := range checked {
		if strings.Contains(c, text) {
			return true
		}
	}
	return false
}

// TestValidateBSLNamesReadyMethods: the draft and its module go to the lookup
// as given; its hits come back as readyMethods with the note that qualifies
// them, never count as findings, and checked says what was looked up, what was
// skipped and that an empty list proves nothing.
func TestValidateBSLNamesReadyMethods(t *testing.T) {
	const code = "Функция УдалитьДублиВМассиве(Массив)\nКонецФункции\n"
	stub := &readyStub{
		report: app.ReadyMethodsReport{Declared: 40, Skipped: 5, Checked: 30, NotChecked: 5, Methods: []app.ReadyMethodItem{{
			Name: "УдалитьДублиВМассиве", Line: 1,
			Candidates: []app.APIBriefItem{{Call: "ОбщегоНазначенияКлиентСервер.СвернутьМассив", Summary: "Возвращает копию массива с уникальными значениями.", UID: "u1"}},
		}}},
		warn: []app.Warning{{Code: "bsp_library_not_found", Message: "библиотеки в выгрузке нет"}},
	}
	out := callValidateBSL(t, stub, map[string]any{"code": code, "module": "ОбщегоНазначенияУТ"})
	if stub.in.Code != code || stub.in.Module != "ОбщегоНазначенияУТ" {
		t.Errorf("the lookup got %+v, want the draft and its module as given", stub.in)
	}
	if len(out.ReadyMethods) != 1 || out.ReadyMethods[0].Candidates[0].Call != "ОбщегоНазначенияКлиентСервер.СвернутьМассив" {
		t.Errorf("readyMethods = %+v", out.ReadyMethods)
	}
	if !strings.Contains(out.ReadyMethodsNote, "не находка") {
		t.Errorf("readyMethodsNote does not qualify the list: %q", out.ReadyMethodsNote)
	}
	if out.Count != len(out.Findings) {
		t.Errorf("count = %d with %d findings: a hint was counted as a finding", out.Count, len(out.Findings))
	}
	for _, want := range []string{"объявлений в черновике 40", "поиск шёл по 30", "ещё 5 не проверено", "пустой список не значит", "bsp_library_not_found"} {
		if !checkedMentions(out.Checked, want) {
			t.Errorf("checked does not say %q: %q", want, out.Checked)
		}
	}
}

// TestValidateBSLReadyMethodsHonestAboutSkips: a failed lookup, a draft with
// nothing to look up and a lookup that named nothing are different answers,
// and none of them reads as "nothing ready exists".
func TestValidateBSLReadyMethodsHonestAboutSkips(t *testing.T) {
	args := map[string]any{"code": "Сообщить(1);"}

	out := callValidateBSL(t, &readyStub{err: errors.New("нет активного проекта: задайте --projects-root")}, args)
	if len(out.ReadyMethods) != 0 || !checkedMentions(out.Checked, "НЕ искались: нет активного проекта: задайте --projects-root") {
		t.Errorf("failed lookup: readyMethods %+v, checked %q", out.ReadyMethods, out.Checked)
	}

	out = callValidateBSL(t, &readyStub{report: app.ReadyMethodsReport{Declared: 2, Skipped: 2}}, args)
	if !checkedMentions(out.Checked, "все пропущены") || out.ReadyMethodsNote != "" {
		t.Errorf("nothing to look up: checked %q, note %q", out.Checked, out.ReadyMethodsNote)
	}

	out = callValidateBSL(t, &readyStub{report: app.ReadyMethodsReport{Declared: 1, Checked: 1}}, args)
	if !checkedMentions(out.Checked, "названы для 0") || !checkedMentions(out.Checked, "пустой список не значит") || out.ReadyMethodsNote != "" {
		t.Errorf("nothing named: checked %q, note %q", out.Checked, out.ReadyMethodsNote)
	}

	// No finder wired at all: the tool works as before and says nothing about it.
	out = callValidateBSL(t, nil, args)
	if checkedMentions(out.Checked, "готовые методы") {
		t.Errorf("no finder: checked %q", out.Checked)
	}
}
