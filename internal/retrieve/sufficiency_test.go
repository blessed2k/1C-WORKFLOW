package retrieve

import (
	"testing"
)

// statusOf — статус покрытия категории в ответе; отсутствие категории в
// requiredCoverage — ошибка теста, а не «пусто».
func statusOf(t *testing.T, r Result, category string) CoverageStatus {
	t.Helper()
	c, ok := coverageOf(r, category)
	if !ok {
		t.Fatalf("категории %q нет в requiredCoverage: %+v", category, r.RequiredCoverage)
	}
	return c.Status
}

// TestHonestlyEmptyCategoriesDoNotSinkAnswer — критерии приёмки П3.1/П3.2
// (R26, R27, R28, R31): документ Заказ ТОЛЬКО пишет движения — регистров он не
// читает и подписок на него нет. Обе категории обязаны отчитаться
// complete_empty («собрана, фактов нет»), не missing; ответ при этом остаётся
// sufficient_inline и не называет их отсутствующими.
func TestHonestlyEmptyCategoriesDoNotSinkAnswer(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingFixture(t, st, postingFixtureOpts{})

	res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p"})
	if res.Intent.Primary != IntentPosting {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentPosting)
	}

	if got := statusOf(t, res, "movements"); got != CompleteInline {
		t.Errorf("movements = %q, want %q (движение по ТоварыНаСкладах есть и влезло)", got, CompleteInline)
	}
	for _, cat := range []string{"register_access", "subscriptions"} {
		if got := statusOf(t, res, cat); got != CompleteEmpty {
			t.Errorf("%s = %q, want %q (категория собрана, фактов нет)", cat, got, CompleteEmpty)
		}
	}
	if res.SufficiencyStatus != SufficientInline {
		t.Fatalf("SufficiencyStatus = %q, want %q (coverage=%+v)", res.SufficiencyStatus, SufficientInline, res.RequiredCoverage)
	}
	for _, m := range res.MissingRequired {
		if m == "register_access" || m == "subscriptions" {
			t.Errorf("missingRequired называет честно пустую категорию %q: %v", m, res.MissingRequired)
		}
	}
}

// TestBudgetStarvedCategoryStaysMissing — вторая половина того же критерия
// приёмки (R26/R28): расщепление ветки returned==0 не должно проглотить
// НАСТОЯЩУЮ неполноту. Факты есть (totalCount=2), бюджета не хватает даже на
// один — статус прежний missing, ответ прежний insufficient.
func TestBudgetStarvedCategoryStaysMissing(t *testing.T) {
	st := openFixtureStore(t)
	seedPartialRegisterFixture(t, st)

	res := buildFor(t, st, Request{Task: "Кто пишет в регистр Рег", ProjectID: "p", BudgetChars: 5})
	cov, ok := coverageOf(res, "writes_movements")
	if !ok {
		t.Fatalf("нет категории writes_movements: %+v", res.RequiredCoverage)
	}
	if cov.TotalCount == 0 {
		t.Fatalf("фикстура не дала фактов (%+v) — тест проверяет именно случай totalCount>0", cov)
	}
	if cov.Status != Missing || cov.ReturnedCount != 0 {
		t.Fatalf("writes_movements = %+v, want %q при returnedCount=0 и totalCount>0", cov, Missing)
	}
	if res.SufficiencyStatus != Insufficient {
		t.Fatalf("SufficiencyStatus = %q, want %q", res.SufficiencyStatus, Insufficient)
	}
	var named bool
	for _, m := range res.MissingRequired {
		if m == "writes_movements" {
			named = true
		}
	}
	if !named {
		t.Errorf("missingRequired = %v, want запись writes_movements", res.MissingRequired)
	}
}

// TestPostingSubscriptionsFilled — критерий приёмки П3.3 (R29, R30):
// категория subscriptions строится из event_subscription по источнику
// «объект-анкер», включая подписки, ОБЪЯВЛЕННЫЕ расширением, которое
// применяется к компоненту объекта. component/layer факта — компонент,
// объявивший подписку. Подписка на чужой документ и подписка из
// конфигурации, к которой объект не относится, в ответ не попадают.
func TestPostingSubscriptionsFilled(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingFixture(t, st, postingFixtureOpts{subs: true})

	res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p"})
	got := map[string]string{}
	for _, f := range res.Facts {
		if f.Category == "subscriptions" {
			got[f.Display] = f.Component
		}
	}
	if len(got) != 2 {
		t.Fatalf("subscriptions = %v, want ровно две записи (своя и из расширения)", got)
	}
	if c := got["ПриПроведенииЗаказа"]; c != "cfg" {
		t.Errorf("подписка конфигурации: component = %q, want cfg (%v)", c, got)
	}
	if c := got["РасшА_ПриПроведенииЗаказа"]; c != "ext-subs" {
		t.Errorf("подписка расширения: component = %q, want ext-subs — слой ОБЪЯВИВШЕГО, не документа (%v)", c, got)
	}
	if _, ok := got["ПриПроведенииЗаказаКлиента"]; ok {
		t.Errorf("подписка чужого документа просочилась: %v", got)
	}
	if _, ok := got["ЧужаяПриПроведенииЗаказа"]; ok {
		t.Errorf("подписка компонента, к которому документ не относится, просочилась: %v", got)
	}
	if s := statusOf(t, res, "subscriptions"); s != CompleteInline {
		t.Errorf("subscriptions = %q, want %q — факты есть и влезли", s, CompleteInline)
	}
}

// TestUncollectedCategoriesStayMissing — D03 (§6 спецификации, находка ревью
// таска 06): «собрана» — это ЗАЯВЛЕНИЕ сборщика, а не вывод из счёта.
// Интент form в этот прогон не входит и ничего не заявляет: у документа Заказ
// в фикстуре форм нет вовсе, ни одна из четырёх обязательных категорий не
// собиралась — значит missing и insufficient, как до таска 06. Иначе ответ,
// не нашедший ничего, объявляет себя полным (ровно та регрессия, что была
// воспроизведена на реальной выгрузке).
func TestUncollectedCategoriesStayMissing(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingFixture(t, st, postingFixtureOpts{})

	res := buildFor(t, st, Request{Task: "Поменяй обработчик ЗаполнитьДвижения на форме документа Заказ", ProjectID: "p"})
	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
	}
	if len(res.Anchors) == 0 {
		t.Fatalf("анкеров нет — сработает ветка no_anchors, а тест проверяет другую")
	}
	for _, cat := range []string{"binding", "handler", "server_calls", "attributes"} {
		cov, ok := coverageOf(res, cat)
		if !ok {
			t.Fatalf("категории %q нет в requiredCoverage: %+v", cat, res.RequiredCoverage)
		}
		if cov.TotalCount != 0 {
			t.Fatalf("фикстура дала факты в %q (%+v) — тест проверяет случай «не собирали»", cat, cov)
		}
		if cov.Status != Missing {
			t.Errorf("%s = %q, want %q — категорию никто не собирал", cat, cov.Status, Missing)
		}
	}
	if res.SufficiencyStatus != Insufficient {
		t.Fatalf("SufficiencyStatus = %q, want %q (coverage=%+v)", res.SufficiencyStatus, Insufficient, res.RequiredCoverage)
	}
	if len(res.MissingRequired) == 0 {
		t.Errorf("missingRequired пуст, хотя не собрана ни одна категория")
	}
}

// TestCollectionFailureRevokesDeclaration — D03, вторая половина: сбой чтения
// источника не имеет права выглядеть как честная пустота. Отзыв сильнее
// заявления и не перебивается удачным чтением по другому анкеру, поэтому
// категория из семейства complete_* не получит.
func TestCollectionFailureRevokesDeclaration(t *testing.T) {
	bctx := &buildCtx{}
	bctx.declareCollected("movements", "register_access", "subscriptions")
	bctx.declareCollectionFailed("subscriptions")
	bctx.declareCollected("subscriptions")

	honest := bctx.collectedEmptyCategories()
	if !honest["movements"] || !honest["register_access"] {
		t.Errorf("удачно собранные категории потеряли заявление: %v", honest)
	}
	if honest["subscriptions"] {
		t.Errorf("категория со сбоем чтения объявлена честно пустой: %v", honest)
	}

	coverage := []CoverageEntry{}
	_, coverage, _, _ = packBudget(nil, []string{"movements", "subscriptions"}, 1000, honest)
	for _, c := range coverage {
		switch c.Category {
		case "movements":
			if c.Status != CompleteEmpty {
				t.Errorf("movements = %q, want %q", c.Status, CompleteEmpty)
			}
		case "subscriptions":
			if c.Status != Missing {
				t.Errorf("subscriptions = %q, want %q — читать источник не удалось", c.Status, Missing)
			}
		}
	}
}
