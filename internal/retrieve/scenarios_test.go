package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// buildFor открывает read-транзакцию и зовёт Build — общий хелпер сценарных
// тестов (тот же приём, что ReadTx[T] в internal/app, но здесь напрямую:
// retrieve не имеет своей обёртки транзакции для тестов, семантика
// «одна read-транзакция на вызов» и так соблюдена одним st.Read).
func buildFor(t *testing.T, st *store.Store, req Request) Result {
	t.Helper()
	var out Result
	err := st.Read(context.Background(), func(tx *store.ReadTx) error {
		r, berr := Build(context.Background(), tx, req)
		out = r
		return berr
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return out
}

func coverageOf(r Result, category string) (CoverageEntry, bool) {
	for _, c := range r.RequiredCoverage {
		if c.Category == category {
			return c, true
		}
	}
	return CoverageEntry{}, false
}

func requireCoverageStatus(t *testing.T, r Result, category string, want CoverageStatus) {
	t.Helper()
	c, ok := coverageOf(r, category)
	if !ok {
		t.Fatalf("requiredCoverage не содержит категорию %q; coverage=%+v", category, r.RequiredCoverage)
	}
	if c.Status != want {
		t.Fatalf("requiredCoverage[%q].Status = %q, want %q (returned=%d/%d)", category, c.Status, want, c.ReturnedCount, c.TotalCount)
	}
}

func factsAndRelationsText(r Result) string {
	var b strings.Builder
	for _, f := range r.Facts {
		b.WriteString(f.Display)
		b.WriteString(" ")
		b.WriteString(f.Detail)
		b.WriteString("\n")
	}
	for _, s := range r.Signatures {
		b.WriteString(s.Name)
		b.WriteString(" ")
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
	for _, s := range r.Snippets {
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
	for _, rel := range r.Relations {
		b.WriteString(rel.From)
		b.WriteString(" ")
		b.WriteString(rel.To)
		b.WriteString(" ")
		b.WriteString(rel.Detail)
		b.WriteString("\n")
	}
	for _, m := range r.MetadataSummaries {
		b.WriteString(m.Name)
		b.WriteString("\n")
		for _, mm := range m.Members {
			b.WriteString(mm.Name)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func assertBudgetInvariant(t *testing.T, r Result, budget int) {
	t.Helper()
	if r.Budget.UsedChars > budget {
		t.Fatalf("usedChars(%d) > budget(%d) — инвариант нарушен", r.Budget.UsedChars, budget)
	}
	if r.Budget.RequestedChars != budget {
		t.Fatalf("Budget.RequestedChars = %d, want %d", r.Budget.RequestedChars, budget)
	}
}

func assertAllWhyIncluded(t *testing.T, r Result) {
	t.Helper()
	check := func(kind, why string) {
		if strings.TrimSpace(why) == "" {
			t.Errorf("факт вида %s несёт пустой whyIncluded", kind)
		}
	}
	for _, f := range r.Facts {
		check("fact:"+f.Category, f.WhyIncluded)
	}
	for _, s := range r.Signatures {
		check("signature", s.WhyIncluded)
	}
	for _, s := range r.Snippets {
		check("snippet", s.WhyIncluded)
	}
	for _, rel := range r.Relations {
		check("relation", rel.WhyIncluded)
	}
	for _, m := range r.MetadataSummaries {
		check("metadataSummary", m.WhyIncluded)
	}
}

// TestScenario1Bugfix — §25 №1 буквально: definition (тело) + сигнатуры
// callees/callers глубины 1 + запросы внутри тела со схемой; НЕ попадает
// остальной модуль, формы, права.
func TestScenario1Bugfix(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	res := buildFor(t, st, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentBugfix {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentBugfix)
	}
	if len(res.Anchors) == 0 {
		t.Fatal("anchors пуст")
	}
	foundAnchor := false
	for _, a := range res.Anchors {
		if a.Kind == "symbol" && strings.Contains(a.Display, "ЗаполнитьСтатус") {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatalf("anchors не содержит ЗаполнитьСтатус: %+v", res.Anchors)
	}

	requireCoverageStatus(t, res, "definition", CompleteInline)
	requireCoverageStatus(t, res, "callers", CompleteInline)
	requireCoverageStatus(t, res, "callees", CompleteInline)
	if res.SufficiencyStatus != SufficientInline {
		t.Fatalf("SufficiencyStatus = %q, want %q", res.SufficiencyStatus, SufficientInline)
	}

	text := factsAndRelationsText(res)
	if !strings.Contains(text, "ЗаполнитьСтатус") {
		t.Errorf("тело определения не содержит ЗаполнитьСтатус: %q", text)
	}
	if !strings.Contains(text, "ПроверитьЗаказ") {
		t.Errorf("callees не содержит ПроверитьЗаказ: %q", text)
	}
	if !strings.Contains(text, "ОбработатьЗаказы") {
		t.Errorf("callers не содержит ОбработатьЗаказы: %q", text)
	}
	// Не попадёт: остальной модуль (модуль никогда не возвращается целиком —
	// у нас просто нет candidate такого вида, проверяем размер: ни один
	// snippet/fact не содержит весь текст файла-модуля).
	for _, s := range res.Snippets {
		if len(s.Text) > 500 {
			t.Errorf("snippet длиной %d похож на модуль целиком, не на тело одного символа", len(s.Text))
		}
	}
	// Права/формы не затронуты вовсе (bugfix не строит такие категории).
	for _, c := range res.RequiredCoverage {
		if c.Category == "roles" || c.Category == "binding" {
			t.Errorf("bugfix не должен требовать категорию %q", c.Category)
		}
	}

	assertAllWhyIncluded(t, res)
	assertBudgetInvariant(t, res, DefaultBudgetChars)
}

// TestScenario2SignatureChange — §25 №2: definition (сигнатура) + ВСЕ
// references + callers + перехватчики расширений; тела вызывающих не попадают.
func TestScenario2SignatureChange(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	res := buildFor(t, st, Request{Task: "Измени сигнатуру экспортной ОбщегоНазначения27.ПолучитьЦену", ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentSignatureChange {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentSignatureChange)
	}

	requireCoverageStatus(t, res, "definition", CompleteInline)
	requireCoverageStatus(t, res, "references", CompleteInline)
	requireCoverageStatus(t, res, "callers", CompleteInline)
	requireCoverageStatus(t, res, "interceptors", CompleteInline)

	refCov, _ := coverageOf(res, "references")
	if refCov.TotalCount != 10 || refCov.ReturnedCount != 10 {
		t.Fatalf("references coverage = %+v, want 10/10 (10 вызывающих модулей в фикстуре)", refCov)
	}
	callersCov, _ := coverageOf(res, "callers")
	if callersCov.TotalCount != 10 {
		t.Fatalf("callers coverage = %+v, want total=10", callersCov)
	}
	interceptCov, _ := coverageOf(res, "interceptors")
	if interceptCov.TotalCount != 1 {
		t.Fatalf("interceptors coverage = %+v, want total=1 (символ в компоненте ext)", interceptCov)
	}

	if res.SufficiencyStatus != SufficientInline {
		t.Fatalf("SufficiencyStatus = %q, want %q", res.SufficiencyStatus, SufficientInline)
	}

	// Не попадёт: тела вызывающих — callers/references отданы сигнатурами/
	// relations, ни один snippet тела постороннего символа не появляется.
	if len(res.Snippets) != 0 {
		t.Errorf("signature-change не должен отдавать тела (snippets), получено %d: %+v", len(res.Snippets), res.Snippets)
	}

	assertAllWhyIncluded(t, res)
	assertBudgetInvariant(t, res, DefaultBudgetChars)
}

// TestScenario2SignatureChangeBudget500:
// budgetChars=500 на signature-change -> missingRequired непустой, бюджет
// не превышен ни разу.
func TestScenario2SignatureChangeBudget500(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	res := buildFor(t, st, Request{
		Task: "Измени сигнатуру экспортной ОбщегоНазначения27.ПолучитьЦену", ProjectID: "retrieve-fixture",
		BudgetChars: 500,
	})

	assertBudgetInvariant(t, res, 500)
	if len(res.MissingRequired) == 0 {
		t.Fatalf("MissingRequired пуст при budgetChars=500 — на 10 references+10 callers это не должно помещаться целиком")
	}
	if res.SufficiencyStatus == SufficientInline {
		t.Fatalf("SufficiencyStatus = %q при непустом MissingRequired — противоречие", res.SufficiencyStatus)
	}
	if len(res.SuggestedNextTools) == 0 {
		t.Fatal("SuggestedNextTools пуст при неполном покрытии")
	}
}

// TestScenario3Register — §25 №3: writes/movements + владеющие символы;
// reads НЕ обязателен (упомянут счётчиком), ПолучитьОстаток (читатель) не
// попадает в обязательные категории.
func TestScenario3Register(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	res := buildFor(t, st, Request{Task: "Кто пишет в регистр ТоварыНаСкладах", ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentRegister {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentRegister)
	}
	requireCoverageStatus(t, res, "writes_movements", CompleteInline)
	requireCoverageStatus(t, res, "owning_symbols", CompleteInline)

	wCov, _ := coverageOf(res, "writes_movements")
	if wCov.TotalCount != 2 {
		t.Fatalf("writes_movements coverage = %+v, want total=2 (write+movement)", wCov)
	}
	oCov, _ := coverageOf(res, "owning_symbols")
	if oCov.TotalCount != 2 {
		t.Fatalf("owning_symbols coverage = %+v, want total=2 (СписатьТовар, ОприходоватьТовар)", oCov)
	}

	text := factsAndRelationsText(res)
	if !strings.Contains(text, "СписатьТовар") || !strings.Contains(text, "ОприходоватьТовар") {
		t.Errorf("не найдены владеющие символы: %q", text)
	}
	// reads (ПолучитьОстаток) не должен попасть ни в writes_movements, ни в
	// owning_symbols — проверяем, что он не среди Signature/Relation этих
	// категорий (счётчик register_reads это отдельная, необязательная категория).
	for _, s := range res.Signatures {
		if s.Name == "ПолучитьОстаток" {
			t.Errorf("ПолучитьОстаток (read) не должен попасть в обязательные категории register: %+v", s)
		}
	}
	for _, rel := range res.Relations {
		if strings.Contains(rel.From, "ПолучитьОстаток") && rel.Kind == "register_access" {
			t.Errorf("ПолучитьОстаток (read) не должен попасть в writes_movements: %+v", rel)
		}
	}

	assertAllWhyIncluded(t, res)
	assertBudgetInvariant(t, res, DefaultBudgetChars)
}

// TestScenario4Form — §25 №4: binding + handler + серверные вызовы +
// затронутые реквизиты; вся структура формы не попадает.
func TestScenario4Form(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	res := buildFor(t, st, Request{Task: "Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ", ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
	}
	requireCoverageStatus(t, res, "binding", CompleteInline)
	requireCoverageStatus(t, res, "handler", CompleteInline)
	requireCoverageStatus(t, res, "server_calls", CompleteInline)
	requireCoverageStatus(t, res, "attributes", CompleteInline)

	text := factsAndRelationsText(res)
	if !strings.Contains(text, "СкладПриИзменении") {
		t.Errorf("тело обработчика не найдено: %q", text)
	}
	if !strings.Contains(text, "ОбновитьЦеныНаСервере") {
		t.Errorf("серверный вызов не найден: %q", text)
	}
	if !strings.Contains(text, "Склад") {
		t.Errorf("затронутый реквизит Склад не найден: %q", text)
	}

	if res.SufficiencyStatus != SufficientInline {
		t.Fatalf("SufficiencyStatus = %q, want %q", res.SufficiencyStatus, SufficientInline)
	}

	assertAllWhyIncluded(t, res)
	assertBudgetInvariant(t, res, DefaultBudgetChars)
}

// TestScenario5AddAttribute — §25 №5: структура + usages + формы + права +
// обмены; тела модулей не попадают. exchanges — честный missing (dependency_edge
// exchange-plan-contains не публикуется индексом).
func TestScenario5AddAttribute(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	res := buildFor(t, st, Request{Task: "Добавь реквизит СрокДоставки в Документ.ЗаказКлиента и оцени impact", ProjectID: "retrieve-fixture"})

	if res.Intent.Primary != IntentAddAttribute {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentAddAttribute)
	}
	requireCoverageStatus(t, res, "structure", CompleteInline)
	requireCoverageStatus(t, res, "usages", CompleteInline)
	requireCoverageStatus(t, res, "forms", CompleteInline)
	requireCoverageStatus(t, res, "rights", CompleteInline)
	requireCoverageStatus(t, res, "exchanges", Missing)

	if len(res.MetadataSummaries) != 1 || res.MetadataSummaries[0].MemberCount != 2 {
		t.Fatalf("structure = %+v, want ровно 1 объект с memberCount=2", res.MetadataSummaries)
	}
	found := false
	for _, w := range res.Warnings {
		if w.Code == "exchange_edges_not_built" {
			found = true
		}
	}
	if !found {
		t.Errorf("нет warning про exchange_edges_not_built: %+v", res.Warnings)
	}
	if res.SufficiencyStatus != Insufficient {
		t.Fatalf("SufficiencyStatus = %q, want %q (exchanges missing)", res.SufficiencyStatus, Insufficient)
	}
	stringMissing := false
	for _, m := range res.MissingRequired {
		if m == "exchanges" {
			stringMissing = true
		}
	}
	if !stringMissing {
		t.Errorf("MissingRequired не содержит exchanges: %v", res.MissingRequired)
	}

	// Тела модулей не попадают: add-attribute не строит ни одного snippet.
	if len(res.Snippets) != 0 {
		t.Errorf("add-attribute не должен отдавать тела модулей, получено %d snippets", len(res.Snippets))
	}

	assertAllWhyIncluded(t, res)
	assertBudgetInvariant(t, res, DefaultBudgetChars)
}
