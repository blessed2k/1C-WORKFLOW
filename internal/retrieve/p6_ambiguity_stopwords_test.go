package retrieve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestFixtureAmbiguityOptionsCappedWithHonestTruncation — доводка P6, часть
// (б), fixture-уровень (быстро, без ONEC_DUMP): 12 символов с одинаковым
// name_norm в разных модулях одного компонента — та же природа, что реальный
// «Проверить» на ut_demo (конвенция именования), но воспроизводимо без
// реальной выгрузки. До фикса opts в Ambiguity писался без потолка — этот
// тест краснеет, если убрать maxAmbiguityOptions-обрезку в findAnchors
// (anchors.go, третий проход exactSymbolLookup).
func TestFixtureAmbiguityOptionsCappedWithHonestTruncation(t *testing.T) {
	st := openFixtureStore(t)
	ctx := context.Background()
	const n = 12

	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			rel := fmt.Sprintf("CommonModules/Модуль%02d/Ext/Module.bsl", i)
			f := fileHelper(t, tx, "cfg", rel, "// noop")
			m := moduleHelper(t, tx, "cfg", rel, fmt.Sprintf("модуль%02d", i), fmt.Sprintf("Модуль%02d", i), f)
			symbolHelper(t, tx, symbolSpec{
				uid: fmt.Sprintf("sym-gomonim-%02d", i), componentID: "cfg", nameNorm: "гомонимовск", nameDisplay: "ГомонимОвск",
				kind: "procedure", moduleID: m, fileID: f, export: true, span: sp(),
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := buildFor(t, st, Request{Task: "ГомонимОвск где-то в конфигурации"})

	var target *Ambiguity
	for i := range res.Ambiguities {
		if res.Ambiguities[i].Subject == "гомонимовск" {
			target = &res.Ambiguities[i]
		}
	}
	if target == nil {
		t.Fatalf("ожидалась ambiguity с subject=гомонимовск (%d одноимённых символов) — ambiguities=%+v", n, res.Ambiguities)
	}
	if len(target.Options) > maxAmbiguityOptions {
		t.Errorf("Options не обрезан потолком: len=%d, maxAmbiguityOptions=%d", len(target.Options), maxAmbiguityOptions)
	}
	if target.TotalOptions != n {
		t.Errorf("TotalOptions = %d, want %d (реальное число гомонимов в фикстуре)", target.TotalOptions, n)
	}
	if !target.Truncated {
		t.Errorf("Truncated=false при TotalOptions(%d) > maxAmbiguityOptions(%d) — обрезка должна быть честно отмечена", target.TotalOptions, maxAmbiguityOptions)
	}
}

// TestExtractCandidateNamesFiltersTaskStopWords — доводка P6, часть (а):
// unit-уровень, без ONEC_DUMP — императивный глагол задачи («Проверить»,
// «Добавь») не должен попадать в кандидаты anchor'ов, разобранные из
// свободного текста задачи, но содержательные слова рядом с ним обязаны
// пройти. Быстрый мутационный тест: откати taskStopWords[nameNorm]-проверку
// в add() (anchors.go) — этот тест сразу краснеет, реальная выгрузка для
// диагностики не нужна.
func TestExtractCandidateNamesFiltersTaskStopWords(t *testing.T) {
	cands := extractCandidateNames("Проверить план обмена для синхронизации контрагентов при обмене данными", nil)
	names := map[string]bool{}
	for _, c := range cands {
		names[c.name] = true
	}
	if names["проверить"] {
		t.Errorf("extractCandidateNames не отфильтровал стоп-слово «проверить»: candidates=%+v", cands)
	}
	for _, want := range []string{"план", "обмена", "синхронизации", "контрагентов", "данными"} {
		if !names[want] {
			t.Errorf("extractCandidateNames потерял содержательное слово %q вместе со стоп-словом: candidates=%+v", want, cands)
		}
	}
}

// TestExtractCandidateNamesStopWordListDoesNotEatOtherVerbs — стоп-лист не
// должен разрастись настолько, чтобы задеть слова ВНЕ своего списка:
// контрольная проверка, что «Проведение» (существительное, часть реальных
// имён объектов 1С, явно упомянуто в задаче доводки как «не трогать») в
// кандидатах остаётся.
func TestExtractCandidateNamesStopWordListDoesNotEatOtherVerbs(t *testing.T) {
	cands := extractCandidateNames("Проведение документа ЗаказКлиента идёт неверно", nil)
	names := map[string]bool{}
	for _, c := range cands {
		names[c.name] = true
	}
	if !names["проведение"] {
		t.Errorf("extractCandidateNames неверно отфильтровал «Проведение» (не стоп-слово, потенциальный термин): candidates=%+v", cands)
	}
}

// TestExtractCandidateNamesStopWordSkippedOnlyForBareTaskTokens — стоп-слова
// режут только голые кандидаты, разобранные из ТЕКСТА задачи. focusHints
// (агент назвал явно) и структурная форма Модуль.Имя (сильный явный сигнал)
// фильтру не подлежат — иначе легитимная explicit-ссылка на реальный метод
// «Проверить» пропадала бы вместе с шумом.
func TestExtractCandidateNamesStopWordSkippedOnlyForBareTaskTokens(t *testing.T) {
	t.Run("focusHints не фильтруются", func(t *testing.T) {
		cands := extractCandidateNames("", []string{"Проверить"})
		found := false
		for _, c := range cands {
			if c.name == "проверить" {
				found = true
			}
		}
		if !found {
			t.Errorf("focusHints=[Проверить] отфильтрован вместе со стоп-словом из текста задачи: candidates=%+v", cands)
		}
	})

	t.Run("структурная форма Модуль.Проверить не фильтруется", func(t *testing.T) {
		cands := extractCandidateNames("Посмотри МодульX.Проверить в конфигурации", nil)
		found := false
		for _, c := range cands {
			if c.name == "проверить" && c.structural {
				found = true
			}
		}
		if !found {
			t.Errorf("структурный кандидат МодульX.Проверить отфильтрован вместе со стоп-словом: candidates=%+v", cands)
		}
	})
}

// TestBoundAmbiguitiesCapsCountAndBudget — доводка P6, часть (б): unit-
// уровень для boundAmbiguities (pack.go) — не более maxAmbiguities записей,
// usedChars никогда не превышает переданный остаток бюджета, дропнутые
// записи честно посчитаны в droppedCount (используется build.go для
// warning'а "ambiguities_truncated", тот же приём, что
// call_graph_expansion_truncated/references_truncated в expand.go).
func TestBoundAmbiguitiesCapsCountAndBudget(t *testing.T) {
	t.Run("потолок числа записей", func(t *testing.T) {
		var in []Ambiguity
		for i := 0; i < maxAmbiguities+5; i++ {
			in = append(in, Ambiguity{Subject: "x", Options: []string{"a"}})
		}
		out, used, dropped := boundAmbiguities(in, 1_000_000)
		if len(out) > maxAmbiguities {
			t.Errorf("len(out) = %d, потолок maxAmbiguities = %d", len(out), maxAmbiguities)
		}
		if dropped != len(in)-len(out) {
			t.Errorf("dropped = %d, want %d (len(in)-len(out))", dropped, len(in)-len(out))
		}
		var want int
		for _, a := range out {
			want += ambiguityCharCost(a)
		}
		if used != want {
			t.Errorf("used = %d, want %d (сумма ambiguityCharCost упакованных записей)", used, want)
		}
	})

	t.Run("жёсткий char budget без перерасхода", func(t *testing.T) {
		big := Ambiguity{Subject: "проверить", Options: []string{
			"a.b@c", "d.e@f", "g.h@i", "j.k@l", "m.n@o", "p.q@r", "s.t@u", "v.w@x",
		}}
		cost := ambiguityCharCost(big)
		in := []Ambiguity{big, big, big}
		budget := cost + 1 // хватает ровно на одну запись, вторая не влезает целиком
		out, used, dropped := boundAmbiguities(in, budget)
		if used > budget {
			t.Fatalf("used(%d) > budget(%d) — инвариант нарушен", used, budget)
		}
		if len(out) != 1 {
			t.Errorf("len(out) = %d, want 1 (влезает ровно одна запись при этом budget)", len(out))
		}
		if dropped != len(in)-len(out) {
			t.Errorf("dropped = %d, want %d", dropped, len(in)-len(out))
		}
	})

	t.Run("пустой budget — все записи дропаются, инвариант держится", func(t *testing.T) {
		in := []Ambiguity{{Subject: "x", Options: []string{"a"}}}
		out, used, dropped := boundAmbiguities(in, 0)
		if len(out) != 0 || used != 0 || dropped != 1 {
			t.Errorf("out=%+v used=%d dropped=%d, want out=[] used=0 dropped=1", out, used, dropped)
		}
	})
}

// TestRealDumpTaskImperativeVerbStopWordAvoidsHomonymNoise — доводка P6,
// сквозной регресс на реальной выгрузке (ut_demo): контрастный сценарий из
// отчёта критика, воспроизведён дважды буквально этой же формулировкой.
// «Проверить план обмена для синхронизации контрагентов при обмене данными»
// ДО фикса резолвил «Проверить» в 21 несвязанный anchor/ambiguity (конвенция
// именования — процедура-проверка с этим именем в шести+ формах справочников
// МЧД003/НастройкиПодключенияКОблачнымКассам/Сертификат*/КонструкторФормул/
// РазблокированиеРеквизитов), давая ответ 46906 байт при budgetChars=16000
// по умолчанию и sufficiencyStatus=sufficient_inline/confidence=1.0 —
// уверенно неверный ответ. Мутационная проверка: закомментируй
// `if filterStopWords && !hasDot && taskStopWords[nameNorm]` в
// extractCandidateNames (anchors.go) — этот тест сразу краснеет на
// anchors/ambiguities-ассертах ниже, реальные числа совпадут с находкой.
func TestRealDumpTaskImperativeVerbStopWordAvoidsHomonymNoise(t *testing.T) {
	root := retrieveRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-retrieve-p6-stopwords")
	manifest := workspace.Manifest{
		Version: 1, Project: projectID, Root: root,
		Components: []workspace.Component{{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root}},
	}
	workspaceRoot := t.TempDir()
	st, err := store.Open(workspaceRoot, store.Options{ProjectID: projectID, StateDirName: workspace.RegistryDirName})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc := index.NewService(st, projectID, manifest, builtins, index.Config{})
	t.Cleanup(func() { svc.Close() })

	ctx := context.Background()
	if _, err := svc.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}

	task := "Проверить план обмена для синхронизации контрагентов при обмене данными"
	res, err := Run(ctx, svc, st, Request{Task: task, ProjectID: projectID, Freshness: FreshnessAllowStale})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, a := range res.Anchors {
		if strings.HasSuffix(a.Display, ".Проверить") {
			t.Errorf("anchor остался гомонимом стоп-слова «Проверить»: %+v — anchors=%+v", a, res.Anchors)
		}
	}
	for _, amb := range res.Ambiguities {
		if amb.Subject == "проверить" {
			t.Errorf("ambiguities всё ещё несут стоп-слово «проверить» как subject: %+v", amb)
		}
	}

	// Находка была именно о том, что БЕЗ фильтра anchors/ambiguities целиком
	// уводятся посторонним словом — здесь anchors должны остаться (реальная
	// тема задачи: обмен/контрагенты), просто без гомонимов «Проверить».
	if len(res.Anchors) == 0 {
		t.Errorf("anchors пуст — фильтр стоп-слова не должен убирать реальную тему задачи целиком")
	}

	// «Жёсткий budget» — контракт budget<=budget держится на UsedChars, не на
	// сыром байт-размере JSON-конверта целиком (Anchors/Warnings/Coverage
	// budget'ом не покрываются по архитектуре — см. doc-комментарий Result,
	// types.go): это НЕ то же самое измерение, что до-фиксовый разгон
	// ambiguities. До фикса тот же сценарий весил 46906 байт ИМЕННО из-за
	// одной Ambiguity с 21 неограниченной опцией (замерено этой же сессией на
	// unpatched-коде); после фикса ambiguities для этого текста задачи пуст
	// (стоп-слово убрало «Проверить» из кандидатов ДО того, как гомонимия
	// вообще возникла) — реальный ответ теперь честно тяжелее ИЗ-ЗА
	// содержательных anchors (которые раньше вытеснялись шумом), не из-за
	// неограниченной ambiguities-обрезки; проверяем именно инвариант budget,
	// не абсолютный байт-порог.
	if res.Budget.UsedChars > res.Budget.RequestedChars {
		t.Errorf("Budget.UsedChars(%d) > Budget.RequestedChars(%d) — жёсткий budget нарушен", res.Budget.UsedChars, res.Budget.RequestedChars)
	}
}

// TestRealDumpAmbiguityOptionsCappedWithHonestTruncation — доводка P6,
// вторая половина находки: даже когда гомоним найден ЯВНО (через focusHints,
// которые намеренно не фильтруются стоп-словами — агент попросил именно это
// имя), Options внутри Ambiguity не разрастаются без потолка. Использует то
// же реальное слово «Проверить» с реальными ~21 гомонимом на ut_demo, но
// заходит через focusHints, чтобы независимо от фикса (а) проверить фикс
// (б) — потолок Options/запись плюс честный Truncated/TotalOptions.
func TestRealDumpAmbiguityOptionsCappedWithHonestTruncation(t *testing.T) {
	root := retrieveRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-retrieve-p6-ambig-cap")
	manifest := workspace.Manifest{
		Version: 1, Project: projectID, Root: root,
		Components: []workspace.Component{{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root}},
	}
	workspaceRoot := t.TempDir()
	st, err := store.Open(workspaceRoot, store.Options{ProjectID: projectID, StateDirName: workspace.RegistryDirName})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc := index.NewService(st, projectID, manifest, builtins, index.Config{})
	t.Cleanup(func() { svc.Close() })

	ctx := context.Background()
	if _, err := svc.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}

	res, err := Run(ctx, svc, st, Request{
		Task: "разберись с формой", FocusHints: []string{"Проверить"},
		ProjectID: projectID, Freshness: FreshnessAllowStale,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var target *Ambiguity
	for i := range res.Ambiguities {
		if res.Ambiguities[i].Subject == "проверить" {
			target = &res.Ambiguities[i]
		}
	}
	if target == nil {
		t.Fatalf("ожидалась ambiguity с subject=проверить (реальный гомоним ut_demo через явный focusHint) — ambiguities=%+v", res.Ambiguities)
	}
	if len(target.Options) > maxAmbiguityOptions {
		t.Errorf("Options не обрезан потолком: len=%d, maxAmbiguityOptions=%d", len(target.Options), maxAmbiguityOptions)
	}
	if target.TotalOptions <= maxAmbiguityOptions {
		t.Errorf("TotalOptions=%d — ожидался реальный гомоним ut_demo (>%d), иначе тест не про находку", target.TotalOptions, maxAmbiguityOptions)
	}
	if !target.Truncated {
		t.Errorf("Truncated=false при TotalOptions(%d) > len(Options)(%d) — обрезка должна быть честно отмечена", target.TotalOptions, len(target.Options))
	}

	if res.Budget.UsedChars > res.Budget.RequestedChars {
		t.Errorf("Budget.UsedChars(%d) > Budget.RequestedChars(%d) — жёсткий budget нарушен", res.Budget.UsedChars, res.Budget.RequestedChars)
	}
}
