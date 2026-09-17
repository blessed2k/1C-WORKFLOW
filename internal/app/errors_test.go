package app

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestErrorStringCarriesCodeMessageHint: Error() — то, что реально уходит
// клиенту (см. doc-комментарий на Error) — обязано содержать код, сообщение,
// подсказку и, когда заданы, проект/generation: это и есть "actionable" в
// текущем транспортном ограничении SDK.
func TestErrorStringCarriesCodeMessageHint(t *testing.T) {
	err := NewError(CodeNoActiveProject, "нет активного проекта", "вызовите reindex").
		WithProject("ut-main").WithGeneration(domain.NewGeneration(1, 5))
	got := err.Error()
	for _, want := range []string{"no_active_project", "нет активного проекта", "вызовите reindex", "ut-main", "e1.g5"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, не содержит %q", got, want)
		}
	}
}

// TestNotFoundErrorNamesNearest: "не найдено" называет ближайшие по написанию
// имена (R60), не первые попавшиеся и не все подряд.
func TestNotFoundErrorNamesNearest(t *testing.T) {
	candidates := []string{"ПолучитьЦену", "ПолучитьЦеныНоменклатуры", "ЗаписатьДокумент"}
	err := NotFoundError("символ", "ПолучитьЦены", candidates)
	if err.Code != CodeNotFound {
		t.Fatalf("code = %s, want %s", err.Code, CodeNotFound)
	}
	if !strings.Contains(err.Hint, "ПолучитьЦену") || !strings.Contains(err.Hint, "ПолучитьЦеныНоменклатуры") {
		t.Errorf("hint = %q, ожидались похожие имена ПолучитьЦену и ПолучитьЦеныНоменклатуры", err.Hint)
	}
	if strings.Contains(err.Hint, "ЗаписатьДокумент") {
		t.Errorf("hint = %q, не должен предлагать несвязанное имя ЗаписатьДокумент", err.Hint)
	}
}

// TestNotFoundErrorNoCandidates: без похожих имён подсказка честно говорит
// проверить написание, а не пустой список.
func TestNotFoundErrorNoCandidates(t *testing.T) {
	err := NotFoundError("символ", "ЧтоУгодно", []string{"СовсемДругое"})
	if err.Hint == "" {
		t.Fatal("hint пуст")
	}
	if strings.Contains(err.Hint, "похожие имена") {
		t.Errorf("hint = %q не должен утверждать про похожие имена, когда их нет", err.Hint)
	}
}

// TestComponentNotRegisteredErrorListsKnown: component_not_registered
// перечисляет реальные компоненты манифеста в подсказке, отсортированными —
// не порядком карты.
func TestComponentNotRegisteredErrorListsKnown(t *testing.T) {
	err := ComponentNotRegisteredError("ext-x", []domain.ComponentID{"tests", "cfg", "ext-fix"})
	if err.Code != CodeComponentNotRegistered {
		t.Fatalf("code = %s, want %s", err.Code, CodeComponentNotRegistered)
	}
	wantOrder := "cfg, ext-fix, tests"
	if !strings.Contains(err.Hint, wantOrder) {
		t.Errorf("hint = %q, ожидался отсортированный список %q", err.Hint, wantOrder)
	}
}

// TestComponentNotRegisteredErrorEmptyManifest: манифест без компонентов —
// честная подсказка, не пустая строка после "известные компоненты: ".
func TestComponentNotRegisteredErrorEmptyManifest(t *testing.T) {
	err := ComponentNotRegisteredError("x", nil)
	if strings.Contains(err.Hint, "известные компоненты") {
		t.Errorf("hint = %q не должен утверждать про список известных компонентов, когда их нет", err.Hint)
	}
}

// TestResourceExpiredErrorCode проверяет конструктор ресурсной ошибки
// (используется тасками 11+ для onec://src|symbol|references ссылок).
func TestResourceExpiredErrorCode(t *testing.T) {
	err := ResourceExpiredError("onec://symbol/proj/abc?gen=e1.g1", "generation не совпадает")
	if err.Code != CodeResourceExpired {
		t.Fatalf("code = %s, want %s", err.Code, CodeResourceExpired)
	}
	if !strings.Contains(err.Message, "onec://symbol/proj/abc") {
		t.Errorf("message = %q, должен называть ресурс", err.Message)
	}
}

// TestFromPathErrorTranslatesSafeJoin: path_outside_workspace достигается
// РЕАЛЬНЫМ отказом workspace.SafeJoin — не сконструированной вручную ошибкой,
// чтобы тест доказывал перевод настоящего контракта, а не самого себя.
func TestFromPathErrorTranslatesSafeJoin(t *testing.T) {
	root := t.TempDir()
	_, safeErr := workspace.SafeJoin(root, "../outside")
	if safeErr == nil {
		t.Fatal("SafeJoin(../outside) не вернул ошибку — фикстура сломана")
	}
	err := FromPathError(safeErr)
	if err == nil {
		t.Fatal("FromPathError вернул nil на реальной ошибке SafeJoin")
	}
	if err.Code != CodePathOutsideWorkspace {
		t.Fatalf("code = %s, want %s", err.Code, CodePathOutsideWorkspace)
	}
}

// TestFromPathErrorIgnoresUnrelated: ошибка, не связанная с выходом за
// workspace, не подменяется — FromPathError отдаёт nil, и вызывающий обязан
// обработать исходную ошибку сам.
func TestFromPathErrorIgnoresUnrelated(t *testing.T) {
	if got := FromPathError(nil); got != nil {
		t.Fatalf("FromPathError(nil) = %v, want nil", got)
	}
	other := &Error{Code: CodeNotFound}
	if got := FromPathError(other); got != nil {
		t.Fatalf("FromPathError(несвязанная ошибка) = %v, want nil", got)
	}
}

// TestFromIndexNotFreshTranslatesRealError: index_not_fresh достигается
// реальным index.ErrIndexNotFresh, не текстовым совпадением.
func TestFromIndexNotFreshTranslatesRealError(t *testing.T) {
	src := &index.ErrIndexNotFresh{Progress: "индексируется файл 42 из 100"}
	err := FromIndexNotFresh(src)
	if err == nil {
		t.Fatal("FromIndexNotFresh вернул nil на реальном ErrIndexNotFresh")
	}
	if err.Code != CodeIndexNotFresh {
		t.Fatalf("code = %s, want %s", err.Code, CodeIndexNotFresh)
	}
	if !strings.Contains(err.Message, "42 из 100") {
		t.Errorf("message = %q, должен нести прогресс из исходной ошибки", err.Message)
	}
	if FromIndexNotFresh(nil) != nil {
		t.Fatal("FromIndexNotFresh(nil) должен быть nil")
	}
}
