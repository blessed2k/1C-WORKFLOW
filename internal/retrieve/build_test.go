package retrieve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestDedupWarningsCollapsesDuplicateNoForms — юнит-уровень регрессии P5
// (доводка круга 2, см. TestRealDumpFormIntentAmbiguousHomonymNoDuplicateWarning
// в realdump_test.go для живого воспроизведения на ut_demo): синтетически
// мутирует вход, дублируя ровно ту warning-пару, что реально производит
// expandForm для двух омонимов объекта метаданных без форм (CommonPicture +
// DefinedType «Номенклатура» — text warning несёт только общее для омонимов
// Display-имя, поэтому текст двух РАЗНЫХ анкоров совпадает буквально).
// Доказывает механизм дедупликации независимо от индекса/ut_demo (быстрый,
// без ONEC_DUMP) — если dedupWarnings удалить или сломать (мутация: вернуть
// `in` без фильтрации), этот тест первым укажет на разницу длины/состава.
func TestDedupWarningsCollapsesDuplicateNoForms(t *testing.T) {
	in := []Warning{
		{Code: "form_binding_not_matched", Message: "ни один обработчик формы не совпал по имени элемента/события с текстом задачи", Hint: "уточните задачу точным именем элемента формы и события, либо вызовите get_form_handlers"},
		{Code: "no_forms", Message: "у объекта Номенклатура не найдено форм в индексе"},
		{Code: "no_forms", Message: "у объекта Номенклатура не найдено форм в индексе"},
	}
	got := dedupWarnings(in)

	want := []Warning{
		{Code: "form_binding_not_matched", Message: "ни один обработчик формы не совпал по имени элемента/события с текстом задачи", Hint: "уточните задачу точным именем элемента формы и события, либо вызовите get_form_handlers"},
		{Code: "no_forms", Message: "у объекта Номенклатура не найдено форм в индексе"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupWarnings(%+v) = %+v, want %+v (дубликат no_forms обязан схлопнуться в одно предупреждение, порядок и первое Hint сохраняются)", in, got, want)
	}

	// Различающийся Message (например, у объекта с другим Display) — НЕ
	// дубликат, обе строки обязаны остаться: дедупликация только по буквально
	// одинаковой паре (Code,Message), не по одному Code.
	distinct := []Warning{
		{Code: "no_forms", Message: "у объекта Номенклатура не найдено форм в индексе"},
		{Code: "no_forms", Message: "у объекта ЗаказКлиента не найдено форм в индексе"},
	}
	if got := dedupWarnings(distinct); len(got) != 2 {
		t.Fatalf("dedupWarnings схлопнул различающиеся по Message предупреждения одного Code: %+v", got)
	}
}

// TestCoverageStatusCompleteViaResource — определение с телом длиннее
// bodyInlineCap обязано выйти complete_via_resource (усечено, но ЦЕЛИКОМ
// представлено ссылкой на ресурс — returned=total=1, usedResource=true), а
// НЕ complete_inline и НЕ partial.
func TestCoverageStatusCompleteViaResource(t *testing.T) {
	st := openFixtureStore(t)
	uid := seedLongBodyFixture(t, st)

	res := buildFor(t, st, Request{Task: "Исправь ошибку в ДлинныйМетод", ProjectID: "p"})
	requireCoverageStatus(t, res, "definition", CompleteViaResource)

	cov, _ := coverageOf(res, "definition")
	if cov.ReturnedCount != 1 || cov.TotalCount != 1 {
		t.Fatalf("definition coverage = %+v, want returned=total=1", cov)
	}
	if cov.SuggestedNextAction == "" {
		t.Error("complete_via_resource обязан нести suggestedNextAction")
	}
	if !strings.Contains(cov.SuggestedNextAction, "onec://symbol/") {
		t.Errorf("suggestedNextAction = %q, want ссылку onec://symbol/...", cov.SuggestedNextAction)
	}
	var found bool
	for _, s := range res.Snippets {
		if s.UID == uid {
			found = true
			if !s.Truncated {
				t.Error("Snippet.Truncated = false для тела длиннее bodyInlineCap")
			}
			if s.ResourceURI == "" {
				t.Error("Snippet.ResourceURI пуст при усечении")
			}
		}
	}
	if !found {
		t.Fatal("не найден snippet определения ДлинныйМетод")
	}

	// requiresResourceFetch: definition=complete_via_resource, callers/callees
	// маленькие -> complete_inline, никакой partial/missing.
	if res.SufficiencyStatus != RequiresResourceFetch {
		t.Fatalf("SufficiencyStatus = %q, want %q (callers=%v)", res.SufficiencyStatus, RequiresResourceFetch, res.RequiredCoverage)
	}
	assertBudgetInvariant(t, res, DefaultBudgetChars)
}

// TestCoverageStatusPartial — фикстура с посчитанными вручную charCost:
// budgetChars=47 влезает ровно один из двух writes_movements кандидатов
// (47 и 50 рун — см. doc-комментарий seedPartialRegisterFixture).
func TestCoverageStatusPartial(t *testing.T) {
	st := openFixtureStore(t)
	seedPartialRegisterFixture(t, st)

	res := buildFor(t, st, Request{Task: "Кто пишет в регистр Рег", ProjectID: "p", BudgetChars: 47})
	requireCoverageStatus(t, res, "writes_movements", Partial)
	cov, _ := coverageOf(res, "writes_movements")
	if cov.TotalCount != 2 {
		t.Fatalf("writes_movements.TotalCount = %d, want 2", cov.TotalCount)
	}
	if cov.ReturnedCount != 1 {
		t.Fatalf("writes_movements.ReturnedCount = %d, want 1 (budget=47 влезает ровно один из 47/50-символьных кандидатов)", cov.ReturnedCount)
	}
	assertBudgetInvariant(t, res, 47)
	if len(res.MissingRequired) == 0 {
		t.Error("MissingRequired пуст при partial-категории")
	}
	if res.SufficiencyStatus != Insufficient {
		t.Fatalf("SufficiencyStatus = %q, want %q", res.SufficiencyStatus, Insufficient)
	}
}

// TestCoverageStatusMissing — totalCount=0 (регистр без единого write/movement
// доступа в индексе) -> missing честно, а не пустой список без объяснения.
func TestCoverageStatusMissing(t *testing.T) {
	st := openFixtureStore(t)
	ctx := context.Background()
	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		fMeta := fileHelper(t, tx, "cfg", declPath("InformationRegister", "Пустой"), "<meta/>")
		_, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00InformationRegister\x00пустой", ComponentID: "cfg",
			MType: "InformationRegister", NameNorm: "пустой", NameDisplay: "Пустой", FileID: fMeta, Layer: "base",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := buildFor(t, st, Request{Task: "Кто пишет в регистр Пустой", ProjectID: "p"})
	requireCoverageStatus(t, res, "writes_movements", Missing)
	cov, _ := coverageOf(res, "writes_movements")
	if cov.TotalCount != 0 || cov.ReturnedCount != 0 {
		t.Fatalf("writes_movements coverage = %+v, want 0/0", cov)
	}
	if res.SufficiencyStatus != Insufficient {
		t.Fatalf("SufficiencyStatus = %q, want %q", res.SufficiencyStatus, Insufficient)
	}
}

// TestSufficiencyStatusTable — все три исхода правила §Бюджет и покрытие,
// закреплены за конкретными сценариями (не изобретаются здесь заново).
func TestSufficiencyStatusTable(t *testing.T) {
	t.Run("sufficient_inline", func(t *testing.T) {
		st := openFixtureStore(t)
		seedScenarioFixture(t, st)
		res := buildFor(t, st, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p"})
		if res.SufficiencyStatus != SufficientInline {
			t.Fatalf("got %q, want %q", res.SufficiencyStatus, SufficientInline)
		}
	})
	t.Run("requires_resource_fetch", func(t *testing.T) {
		st := openFixtureStore(t)
		seedLongBodyFixture(t, st)
		res := buildFor(t, st, Request{Task: "Исправь ошибку в ДлинныйМетод", ProjectID: "p"})
		if res.SufficiencyStatus != RequiresResourceFetch {
			t.Fatalf("got %q, want %q", res.SufficiencyStatus, RequiresResourceFetch)
		}
	})
	t.Run("insufficient", func(t *testing.T) {
		st := openFixtureStore(t)
		seedScenarioFixture(t, st)
		res := buildFor(t, st, Request{Task: "Добавь реквизит СрокДоставки в Документ.ЗаказКлиента и оцени impact", ProjectID: "p"})
		if res.SufficiencyStatus != Insufficient {
			t.Fatalf("got %q, want %q", res.SufficiencyStatus, Insufficient)
		}
		// Один исход insufficient ПРИЧИНУ не называет: partial и missing
		// дают его одинаково, и подмена одной неполноты другой прошла бы
		// зелёной. Причина здесь известна по фикстуре: у ЗаказКлиента нет
		// ни одного факта обмена (seedScenarioFixture их не заводит),
		// поэтому обязательная категория exchanges — missing.
		cov, ok := coverageOf(res, "exchanges")
		if !ok {
			t.Fatalf("requiredCoverage не содержит категорию exchanges: %+v", res.RequiredCoverage)
		}
		if cov.Status != Missing || cov.TotalCount != 0 {
			t.Fatalf("exchanges = %+v, want %q при totalCount=0", cov, Missing)
		}
		var named bool
		for _, m := range res.MissingRequired {
			if m == "exchanges" {
				named = true
			}
		}
		if !named {
			t.Errorf("missingRequired = %v, want запись exchanges", res.MissingRequired)
		}
	})
}

// TestFactWithoutAnchorPathIsExcluded — символ, никак не связанный с anchor'ом
// задачи (ни callee, ни caller, ни ссылка), не должен просочиться в выдачу
// «на всякий случай» (spec: «факт, не достижимый по разрешённому ребру, не
// добирается на всякий случай»).
func TestFactWithoutAnchorPathIsExcluded(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	res := buildFor(t, st, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p"})
	text := factsAndRelationsText(res)
	if strings.Contains(text, "ПолучитьЦену") {
		t.Errorf("ПолучитьЦену (не связан с ЗаполнитьСтатус) просочился в выдачу: %q", text)
	}
	if strings.Contains(text, "СписатьТовар") {
		t.Errorf("СписатьТовар (регистр, не связан с этим bugfix) просочился в выдачу: %q", text)
	}
}

// TestModuleNeverReturnedWhole — ни один snippet ни в одном из пяти intent
// не превышает bodyInlineCap до усечения (usedResource=true срабатывает
// РОВНО тогда, когда текст обрезан) — модуль целиком (который был бы на
// порядки больше одного символа) никогда не проходит инвариант «сигнатура <
// тело < модуль-никогда».
func TestModuleNeverReturnedWhole(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	seedLongBodyFixture(t, st)

	tasks := []string{
		"Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус",
		"Измени сигнатуру экспортной ОбщегоНазначения27.ПолучитьЦену",
		"Кто пишет в регистр ТоварыНаСкладах",
		"Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ",
		"Добавь реквизит СрокДоставки в Документ.ЗаказКлиента и оцени impact",
		"Исправь ошибку в ДлинныйМетод",
	}
	for _, task := range tasks {
		res := buildFor(t, st, Request{Task: task, ProjectID: "p"})
		for _, s := range res.Snippets {
			if !s.Truncated && len([]rune(s.Text)) > bodyInlineCap {
				t.Errorf("%q: snippet длиной %d не усечён — похоже на модуль целиком", task, len([]rune(s.Text)))
			}
			if s.Truncated && len([]rune(s.Text)) > bodyInlineCap+10 {
				t.Errorf("%q: усечённый snippet длиннее bodyInlineCap+запас: %d", task, len([]rune(s.Text)))
			}
		}
	}
}

// TestDeterminism — два одинаковых вызова на одном поколении дают
// идентичный результат (stateless-контракт §23).
func TestDeterminism(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	req := Request{Task: "Измени сигнатуру экспортной ОбщегоНазначения27.ПолучитьЦену", ProjectID: "p"}

	first := buildFor(t, st, req)
	second := buildFor(t, st, req)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("два одинаковых вызова дали разный результат:\n1: %+v\n2: %+v", first, second)
	}
}

// TestBudgetInvariantAcrossSizes — usedChars<=budget держится на широком
// диапазоне бюджетов, включая экстремально малые (инвариант
// проверяется на всём evaluation-наборе).
func TestBudgetInvariantAcrossSizes(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	seedLongBodyFixture(t, st)

	tasks := []string{
		"Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус",
		"Измени сигнатуру экспортной ОбщегоНазначения27.ПолучитьЦену",
		"Кто пишет в регистр ТоварыНаСкладах",
		"Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ",
		"Добавь реквизит СрокДоставки в Документ.ЗаказКлиента и оцени impact",
	}
	budgets := []int{1, 5, 12, 50, 100, 500, 1000, 4000, DefaultBudgetChars}
	for _, task := range tasks {
		for _, b := range budgets {
			res := buildFor(t, st, Request{Task: task, ProjectID: "p", BudgetChars: b})
			if res.Budget.UsedChars > b {
				t.Fatalf("task=%q budget=%d: usedChars=%d > budget", task, b, res.Budget.UsedChars)
			}
		}
	}
}

// TestBudgetTokensConversion — budgetTokens конвертируется chars=tokens*3,
// когда budgetChars не задан.
func TestBudgetTokensConversion(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p", BudgetTokens: 100})
	if res.Budget.RequestedChars != 300 {
		t.Fatalf("RequestedChars = %d, want 300 (100 токенов * 3)", res.Budget.RequestedChars)
	}
}

// TestAmbiguityWhenMultipleExactMatches — символ с тем же нормальным именем
// в двух разных модулях одного компонента (без структурного квалификатора в
// тексте задачи, чтобы оба остались кандидатами) — регистрируется
// Ambiguity, а не тихо выбирается первый попавшийся.
func TestAmbiguityWhenMultipleExactMatches(t *testing.T) {
	st := openFixtureStore(t)
	ctx := context.Background()
	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		f1 := fileHelper(t, tx, "cfg", commonModulePath("ПерваяГруппа"), "// 1")
		m1 := moduleHelper(t, tx, "cfg", commonModulePath("ПерваяГруппа"), "перваягруппа", "ПерваяГруппа", f1)
		symbolHelper(t, tx, symbolSpec{uid: "sym-dup-1", componentID: "cfg", nameNorm: "обработать", nameDisplay: "Обработать", kind: "procedure", moduleID: m1, fileID: f1, export: true, span: sp()})
		f2 := fileHelper(t, tx, "cfg", commonModulePath("ВтораяГруппа"), "// 2")
		m2 := moduleHelper(t, tx, "cfg", commonModulePath("ВтораяГруппа"), "втораягруппа", "ВтораяГруппа", f2)
		symbolHelper(t, tx, symbolSpec{uid: "sym-dup-2", componentID: "cfg", nameNorm: "обработать", nameDisplay: "Обработать", kind: "procedure", moduleID: m2, fileID: f2, export: true, span: sp()})
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := buildFor(t, st, Request{Task: "Исправь ошибку в Обработать", ProjectID: "p"})
	if len(res.Anchors) < 2 {
		t.Fatalf("anchors = %+v, want минимум 2 (одноимённый символ в двух модулях)", res.Anchors)
	}
	if len(res.Ambiguities) == 0 {
		t.Error("Ambiguities пуст при двух точных совпадениях одного имени")
	}
}

// TestCallersCategoryExcludesTransitive — регрессия evaluation-report.md
// §4.1: на реальной выгрузке ut_demo intent=bugfix задача про
// ЗаказыКлиентСервер.ДобавитьДействиеЗаполнитьПризнакРасхождениеЗаказ
// вернула callers=17/17, тогда как grep и Serena find_referencing_symbols
// независимо сошлись на 7 ПРЯМЫХ вызывающих — root cause: expandBugfix
// (expand.go) гонял walkCallGraph с bctx.maxDepth (DefaultMaxDepth=2), и
// транзитивные callers (вызывающие вызывающих) попадали в ту же категорию
// "callers" без пометки уровня. Фикстура строит символ R с тремя ПРЯМЫМИ
// вызывающими (D1/D2/D3) и одним ТРАНЗИТИВНЫМ (T1 вызывает D1, но сам R не
// вызывает) — категория callers обязана содержать ровно D1/D2/D3, без T1,
// независимо от bctx.maxDepth (проверяется и с дефолтным MaxDepth, и
// намеренно завышенным, чтобы поймать регрессию, если maxDepth вернут назад).
func TestCallersCategoryExcludesTransitive(t *testing.T) {
	seedTransitive := func(t *testing.T, st *store.Store) {
		t.Helper()
		ctx := context.Background()
		err := st.Write(ctx, func(tx *store.WriteTx) error {
			if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
				return err
			}
			fR := fileHelper(t, tx, "cfg", commonModulePath("МодульЦелевой"), "Процедура Целевой() Экспорт\nКонецПроцедуры")
			mR := moduleHelper(t, tx, "cfg", commonModulePath("МодульЦелевой"), "модульцелевой", "МодульЦелевой", fR)
			sR := symbolHelper(t, tx, symbolSpec{
				uid: "sym-r", componentID: "cfg", nameNorm: "целевой", nameDisplay: "Целевой",
				kind: "procedure", moduleID: mR, fileID: fR, export: true, span: sp(),
			})

			var direct []int64
			for _, name := range []string{"Первый", "Второй", "Третий"} {
				rel := commonModulePath("Модуль" + name)
				f := fileHelper(t, tx, "cfg", rel, "// "+name)
				m := moduleHelper(t, tx, "cfg", rel, "модуль"+strings.ToLower(name), "Модуль"+name, f)
				s := symbolHelper(t, tx, symbolSpec{
					uid: "sym-" + strings.ToLower(name), componentID: "cfg", nameNorm: strings.ToLower(name), nameDisplay: name,
					kind: "procedure", moduleID: m, fileID: f, export: true, span: sp(),
				})
				callHelper(t, tx, f, s, sR, "целевой")
				direct = append(direct, s)
			}

			// Транзит — вызывает Первый (прямой вызывающий Целевой), сам
			// Целевой не вызывает — транзитивный caller на глубине 2, не
			// должен попасть в "callers".
			fT1 := fileHelper(t, tx, "cfg", commonModulePath("МодульТранзит"), "// транзит")
			mT1 := moduleHelper(t, tx, "cfg", commonModulePath("МодульТранзит"), "модультранзит", "МодульТранзит", fT1)
			sT1 := symbolHelper(t, tx, symbolSpec{
				uid: "sym-tranzit", componentID: "cfg", nameNorm: "транзит", nameDisplay: "Транзит",
				kind: "procedure", moduleID: mT1, fileID: fT1, export: true, span: sp(),
			})
			callHelper(t, tx, fT1, sT1, direct[0], "первый")
			return nil
		})
		if err != nil {
			t.Fatalf("seedTransitive: %v", err)
		}
	}

	for _, maxDepth := range []int{0, 2, 6} {
		t.Run("maxDepth="+strconv.Itoa(maxDepth), func(t *testing.T) {
			st := openFixtureStore(t)
			seedTransitive(t, st)

			res := buildFor(t, st, Request{Task: "Исправь ошибку в МодульЦелевой.Целевой", ProjectID: "p", MaxDepth: maxDepth})
			if res.Intent.Primary != IntentBugfix {
				t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentBugfix)
			}
			requireCoverageStatus(t, res, "callers", CompleteInline)
			cov, _ := coverageOf(res, "callers")
			if cov.TotalCount != 3 || cov.ReturnedCount != 3 {
				t.Fatalf("callers coverage = %+v, want returned=total=3 (Первый/Второй/Третий, без транзитивного Транзит)", cov)
			}
			text := factsAndRelationsText(res)
			for _, want := range []string{"Первый", "Второй", "Третий"} {
				if !strings.Contains(text, want) {
					t.Errorf("callers не содержит прямого вызывающего %s: %q", want, text)
				}
			}
			if strings.Contains(text, "Транзит") {
				t.Errorf("callers содержит транзитивного вызывающего Транзит (баг из evaluation-report.md §4.1): %q", text)
			}
		})
	}
}

// TestStaleWarningCarriesGenerationAndAge — Build отдаёт warning с generation
// и возрастом, когда req.Stale=true (allow-stale precheck уже произошёл до
// вызова Build — см. Run/freshness.go).
func TestStaleWarningCarriesGenerationAndAge(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)
	res := buildFor(t, st, Request{
		Task: "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", ProjectID: "p",
		Stale: true, StaleReason: "background-rebuild-scheduled", StaleAgeSeconds: 42,
	})
	var found bool
	for _, w := range res.Warnings {
		if w.Code == "stale_index" {
			found = true
			if !strings.Contains(w.Message, string(res.Generation)) {
				t.Errorf("stale_index message не называет generation: %q", w.Message)
			}
			if !strings.Contains(w.Message, "42") {
				t.Errorf("stale_index message не называет возраст: %q", w.Message)
			}
		}
	}
	if !found {
		t.Fatal("нет warning stale_index при req.Stale=true")
	}
}

// --- Run()/freshness.go: require-fresh vs allow-stale, реальный index.Service ---

// newFreshnessHarness строит реальный store+index.Service над каталогом с
// компонентом test-sources (только .bsl-файлы — DetectableFromFiles=false,
// но тест сам объявляет компонент в манифесте, discovery по нему всё равно
// проходит через workspace.Discover-независимый путь index.Service, которому
// достаточно Component.Root+Include/Exclude). smallChangeLimit управляет
// границей «мало/много изменений» (§18.5) — тест намеренно ставит её низкой,
// чтобы несколько новых файлов гарантированно ушли в background-rebuild, а
// не в синхронный инкремент.
func newFreshnessHarness(t *testing.T, smallChangeLimit int, deadline time.Duration) (*store.Store, *index.Service, string) {
	t.Helper()
	root := t.TempDir()
	writeSrc := func(rel, content string) {
		writeTestFile(t, root+"/"+rel, content)
	}
	writeSrc("CommonModules/A/Ext/Module.bsl", "Процедура Один() Экспорт\nКонецПроцедуры")

	workspaceRoot := t.TempDir()
	const projectID = domain.ProjectID("freshness-fixture")
	manifest := workspace.Manifest{
		Version: 1, Project: projectID, Root: root,
		Components: []workspace.Component{{ID: "cfg", Kind: domain.KindStandaloneBSL, Root: ".", AbsRoot: root}},
	}
	st, err := store.Open(workspaceRoot, store.Options{ProjectID: projectID, StateDirName: workspace.RegistryDirName})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := index.Config{SmallChangeFileLimit: smallChangeLimit, RequireFreshDeadline: deadline}
	svc := index.NewService(st, projectID, manifest, nil, cfg)
	t.Cleanup(func() { svc.Close() })

	ctx := context.Background()
	if _, err := svc.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	return st, svc, root
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestRunRequireFreshNeverReturnsStale — много изменённых файлов (сверх
// SmallChangeFileLimit) уходят в фоновую пересборку; require-fresh с коротким
// deadline не дожидается её и обязан вернуть index_not_fresh, а НЕ
// Result{Stale:true} под видом свежего.
func TestRunRequireFreshNeverReturnsStale(t *testing.T) {
	st, svc, root := newFreshnessHarness(t, 1, 15*time.Millisecond)
	for i := 0; i < 5; i++ {
		writeTestFile(t, root+"/CommonModules/Extra"+strconv.Itoa(i)+"/Ext/Module.bsl",
			"Процедура П"+strconv.Itoa(i)+"() Экспорт\nКонецПроцедуры")
	}

	_, err := Run(context.Background(), svc, st, Request{Task: "Исправь ошибку в Один", Freshness: FreshnessRequireFresh, ProjectID: "freshness-fixture"})
	if err == nil {
		t.Fatal("require-fresh с 5 новыми файлами и deadline=15ms обязан вернуть ошибку, а не Result")
	}
	var nf *NotFreshError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v (%T), want *NotFreshError", err, err)
	}
	if nf.Progress == "" {
		t.Error("NotFreshError.Progress пуст")
	}
}

// TestRunAllowStaleReturnsWarning — тот же сценарий (много изменений, фон не
// успел), но allow-stale отвечает сразу с warning вместо ожидания/ошибки.
func TestRunAllowStaleReturnsWarning(t *testing.T) {
	st, svc, root := newFreshnessHarness(t, 1, 10*time.Second)
	for i := 0; i < 5; i++ {
		writeTestFile(t, root+"/CommonModules/Extra"+strconv.Itoa(i)+"/Ext/Module.bsl",
			"Процедура П"+strconv.Itoa(i)+"() Экспорт\nКонецПроцедуры")
	}

	res, err := Run(context.Background(), svc, st, Request{Task: "Исправь ошибку в Один", Freshness: FreshnessAllowStale, ProjectID: "freshness-fixture"})
	if err != nil {
		t.Fatalf("allow-stale не должен ошибаться: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if w.Code == "stale_index" {
			found = true
		}
	}
	if !found {
		t.Errorf("allow-stale при фоновой пересборке обязан нести warning stale_index, Warnings=%+v", res.Warnings)
	}
}

// TestConcurrentBuildIsRaceFree — Build не хранит состояние между вызовами
// (stateless-контракт §23): параллельные вызовы над одним *store.Store не
// должны гонять общие данные (проверяется go test -race).
func TestConcurrentBuildIsRaceFree(t *testing.T) {
	st := openFixtureStore(t)
	seedScenarioFixture(t, st)

	tasks := []string{
		"Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус",
		"Измени сигнатуру экспортной ОбщегоНазначения27.ПолучитьЦену",
		"Кто пишет в регистр ТоварыНаСкладах",
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		task := tasks[i%len(tasks)]
		wg.Add(1)
		go func(task string) {
			defer wg.Done()
			// t.Fatalf изнутри спавненной горутины небезопасен (testing:
			// "FailNow must be called from the goroutine running the test") —
			// собираем ошибку в канал, проверяем в теле теста.
			errCh <- st.Read(context.Background(), func(tx *store.ReadTx) error {
				_, err := Build(context.Background(), tx, Request{Task: task, ProjectID: "p"})
				return err
			})
		}(task)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("Build параллельно: %v", err)
		}
	}
}
