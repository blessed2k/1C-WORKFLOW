package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestNoAnchorsHonestPath — §24 шаг 2: «нет anchors -> честный результат с
// warning и suggestedNextTools, а не выдумка». Пустая задача без единого
// узнаваемого имени не должна ронять Build и не должна ничего изобретать.
func TestNoAnchorsHonestPath(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{Task: "Расскажи мне о совершенно постороннем предмете", ProjectID: "p"})
	if len(res.Anchors) != 0 {
		t.Fatalf("anchors = %+v, want пусто", res.Anchors)
	}
	if res.SufficiencyStatus != Insufficient {
		t.Fatalf("SufficiencyStatus = %q, want %q", res.SufficiencyStatus, Insufficient)
	}
	found := false
	for _, w := range res.Warnings {
		if w.Code == "no_anchors" {
			found = true
		}
	}
	if !found {
		t.Fatalf("нет warning no_anchors: %+v", res.Warnings)
	}
	if len(res.SuggestedNextTools) == 0 {
		t.Fatal("SuggestedNextTools пуст")
	}
	assertBudgetInvariant(t, res, DefaultBudgetChars)
}

// TestViewEffectiveBugfixNoInterceptorNoise — D08: bugfix — effective-aware
// intent (effectiveAwareIntent), поэтому НЕ должен нести
// effective_view_partial_coverage; фикстурный анкер ЗаполнитьСтатус не
// заимствован ни одним расширением (seedScenarioFixture заимствует только
// ОбщегоНазначения27, не РаботаСЗаказами) — обычный сценарий проходит как и
// раньше, definition остаётся complete_inline, никакого интерцептор-шума.
func TestViewEffectiveBugfixNoInterceptorNoise(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p", View: "effective"})
	for _, w := range res.Warnings {
		if w.Code == "effective_view_partial_coverage" {
			t.Fatalf("bugfix — effective-aware intent, не должен нести effective_view_partial_coverage: %+v", res.Warnings)
		}
	}
	requireCoverageStatus(t, res, "definition", CompleteInline)
}

// TestViewEffectivePartialCoverageWarningForRawOnlyIntent — D08: intent, для
// которого typed expansion пока не консультируется с наложением слоёв, обязан
// честно нести effective_view_partial_coverage при view=effective, а не
// молчать. Какие intent в этой группе, держит таблица
// TestEffectivePartialCoverageWarningScope (posting_test.go).
func TestViewEffectivePartialCoverageWarningForRawOnlyIntent(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{Task: "Добавь реквизит Комментарий в документ ЗаказКлиента", ProjectID: "p", View: "effective"})
	if !hasWarning(res, "effective_view_partial_coverage") {
		t.Fatalf("нет warning effective_view_partial_coverage для intent=%s: %+v", res.Intent.Primary, res.Warnings)
	}
}

// TestViewInvalidIsError — D08: опечатка в view — ошибка, не молчаливый
// откат на raw (тот же принцип, что internal/app/effective.go:parseView уже
// применяет к get_symbol/get_object/get_module_structure, тикет 14).
func TestViewInvalidIsError(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	ctx := context.Background()
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		_, berr := Build(ctx, tx, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p", View: "effectiv"})
		return berr
	})
	if err == nil {
		t.Fatal("Build(view=\"effectiv\") = nil error, want ошибку про неизвестный view")
	}
	if !strings.Contains(err.Error(), "view") {
		t.Fatalf("err = %v, want упоминание view", err)
	}
}

// TestIncludeCodeNoneFallsBackToSignatureOnly — includeCode=none подавляет
// ТЕЛО анкера: definition отдаётся сигнатурой, а не snippet'ом с телом
// (Category="body"). Другие виды snippet'ов (текст запроса внутри тела —
// query_in_body, контекстная, не обязательная категория) includeCode не
// затрагивает: doc-комментарий Request.IncludeCode называет definition
// «единственным местом, чувствительным к includeCode», а не «единственным
// snippet'ом вообще».
func TestIncludeCodeNoneFallsBackToSignatureOnly(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p", IncludeCode: IncludeNone})
	for _, s := range res.Snippets {
		if s.Category == "body" {
			t.Fatalf("includeCode=none не должен давать snippet тела (Category=body), получено %+v", s)
		}
	}
	found := false
	for _, s := range res.Signatures {
		if s.Name == "ЗаполнитьСтатус" {
			found = true
		}
	}
	if !found {
		t.Fatalf("definition обязана прийти сигнатурой при includeCode=none, Signatures=%+v", res.Signatures)
	}
	requireCoverageStatus(t, res, "definition", CompleteInline)
}

// TestComponentHintsNarrowsAnchorSearch — componentHints сужает anchors:
// символ существует и в "cfg", и в "ext" под тем же именем (см.
// seedScenarioFixture: ПолучитьЦену), но задача просит явно ограничиться
// компонентом "ext" — anchors не должны содержать символ из "cfg".
func TestComponentHintsNarrowsAnchorSearch(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{
		Task: "Исправь ошибку в ПолучитьЦену", ProjectID: "p", ComponentHints: []string{"ext"},
	})
	if len(res.Anchors) == 0 {
		t.Fatal("anchors пуст при componentHints=[ext], хотя символ там есть")
	}
	for _, a := range res.Anchors {
		if a.Component != "ext" {
			t.Errorf("anchor из компонента %q просочился при componentHints=[ext]: %+v", a.Component, a)
		}
	}
	text := factsAndRelationsText(res)
	if strings.Contains(text, "cfg") {
		t.Logf("вывод содержит подстроку 'cfg' (не обязательно ошибка, просто заметка): %q", text)
	}
}
