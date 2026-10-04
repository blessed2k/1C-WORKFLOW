package main

import (
	"context"
	"errors"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/retrieve"
)

// taskSearcherStub отвечает на поиск готовых методов без индексного проекта.
type taskSearcherStub struct {
	items   []app.APITaskMethod
	core    []app.APICoreModule
	err     error
	coreErr error
	task    string
	calls   int
}

func (s *taskSearcherStub) CoreMethods(_ context.Context) (app.Response[app.APICoreItem], error) {
	s.calls++
	if s.coreErr != nil {
		return app.Response[app.APICoreItem]{}, s.coreErr
	}
	return app.Response[app.APICoreItem]{Items: []app.APICoreItem{{Modules: s.core}}}, nil
}

func (s *taskSearcherStub) ReadyForTask(_ context.Context, task string) (app.Response[app.APITaskMethod], error) {
	s.task = task
	s.calls++
	if s.err != nil {
		return app.Response[app.APITaskMethod]{}, s.err
	}
	return app.Response[app.APITaskMethod]{Items: s.items}, nil
}

func contextResponse(intent string) app.Response[retrieve.Result] {
	return app.Response[retrieve.Result]{Items: []retrieve.Result{{Intent: retrieve.Intent{Primary: intent}, SufficiencyStatus: "sufficient_inline"}}}
}

// TestAttachReadyMethods: к ответу get_context_for_task дописываются готовые
// методы, найденные по тексту задачи; для вопроса о правах и об обмене блок
// не собирается вовсе.
func TestAttachReadyMethods(t *testing.T) {
	stub := &taskSearcherStub{core: []app.APICoreModule{{Module: "ОбщегоНазначения", Methods: []string{"ЗначениеРеквизитаОбъекта", "СообщитьПользователю"}}}, items: []app.APITaskMethod{
		{Section: "bsp", Call: "ОбщегоНазначения.ЗначениеРеквизитаОбъекта", Summary: "Значение реквизита.", UID: "u1"},
		{Section: "other", Call: "ПродажиСервер.ПроверитьЗаказ", UID: "u2"},
	}}
	resp := contextResponse(retrieve.IntentBugfix)
	attachReadyMethods(context.Background(), stub, &resp, "проверить ИНН контрагента")
	if stub.task != "проверить ИНН контрагента" {
		t.Errorf("поиск шёл по %q, want текст задачи", stub.task)
	}
	want := []retrieve.ReadyMethod{
		{Section: "bsp", Call: "ОбщегоНазначения.ЗначениеРеквизитаОбъекта", Summary: "Значение реквизита.", UID: "u1"},
		{Section: "other", Call: "ПродажиСервер.ПроверитьЗаказ", UID: "u2"},
	}
	if got := resp.Items[0].ReadyMethods; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("готовые методы = %+v, want %+v", got, want)
	}
	// Ходовые методы библиотеки приходят тем же ответом, только имена.
	if got := resp.Items[0].CoreMethods; len(got) != 1 || got[0].Module != "ОбщегоНазначения" || len(got[0].Methods) != 2 {
		t.Errorf("ходовые методы = %+v", got)
	}

	for _, intent := range []string{retrieve.IntentRights, retrieve.IntentExchange} {
		before := stub.calls
		resp := contextResponse(intent)
		attachReadyMethods(context.Background(), stub, &resp, "почему нет доступа к документу")
		if stub.calls != before || resp.Items[0].ReadyMethods != nil || resp.Items[0].CoreMethods != nil {
			t.Errorf("intent %s: поиск готовых методов шёл (%d вызовов), блок %+v", intent, stub.calls-before, resp.Items[0].ReadyMethods)
		}
	}
}

// TestAttachReadyMethodsSurvivesSearchError: сбой поиска готовых методов
// основной ответ не трогает и называет себя предупреждением; пустой итог
// поиска блока не создаёт.
func TestAttachReadyMethodsSurvivesSearchError(t *testing.T) {
	resp := contextResponse(retrieve.IntentBugfix)
	attachReadyMethods(context.Background(), &taskSearcherStub{err: errors.New("индекс не читается")}, &resp, "задача про код")
	if len(resp.Items) != 1 || resp.Items[0].ReadyMethods != nil || resp.Items[0].SufficiencyStatus != "sufficient_inline" {
		t.Errorf("ответ после сбоя поиска: %+v", resp.Items)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0].Code != "ready_methods_unavailable" {
		t.Errorf("предупреждения после сбоя поиска: %+v", resp.Warnings)
	}

	// Сбой подсчёта ходовых методов опускает блок и называет себя предупреждением.
	resp = contextResponse(retrieve.IntentBugfix)
	attachReadyMethods(context.Background(), &taskSearcherStub{coreErr: errors.New("индекс не читается")}, &resp, "задача про код")
	if resp.Items[0].CoreMethods != nil || len(resp.Warnings) != 1 || resp.Warnings[0].Code != "core_methods_unavailable" {
		t.Errorf("сбой ходовых методов: блок %+v, предупреждения %+v", resp.Items[0].CoreMethods, resp.Warnings)
	}

	resp = contextResponse(retrieve.IntentBugfix)
	attachReadyMethods(context.Background(), &taskSearcherStub{}, &resp, "задача про код")
	if resp.Items[0].ReadyMethods != nil || len(resp.Warnings) != 0 {
		t.Errorf("пустой итог поиска: блок %+v, предупреждения %+v", resp.Items[0].ReadyMethods, resp.Warnings)
	}

	empty := app.Response[retrieve.Result]{}
	attachReadyMethods(context.Background(), &taskSearcherStub{}, &empty, "задача")
	if len(empty.Items) != 0 {
		t.Errorf("пустой ответ изменён: %+v", empty.Items)
	}
}
