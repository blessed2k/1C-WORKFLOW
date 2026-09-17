package retrieve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// retrieveRealDumpEnvVars — тот же приём, что internal/app/symbol_realworld_test.go
// и внутри internal/index/internal/resolve realworld-тесты (§28): путь к
// реальной выгрузке из переменной окружения, никогда не зашит в код.
var retrieveRealDumpEnvVars = []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"}

func retrieveRealDumpRoot(t *testing.T) string {
	t.Helper()
	for _, env := range retrieveRealDumpEnvVars {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if _, err := os.Stat(v); err != nil {
				t.Skipf("%s указывает на недоступный путь: %v", env, err)
			}
			return v
		}
	}
	t.Skip("ONEC_DUMP/MCP1C_SPIKE_DUMP не заданы — прогон на реальной выгрузке пропущен")
	return ""
}

// TestRealDumpLatencyBudget — критерий приёмки тикета 15: get_context_for_task
// замерен p50/p95 против бюджета 1/2.5с (spec §Бюджеты производительности) на
// реальной выгрузке ut_demo, N=10 прогонов, не одна выборка. Anchor берётся
// не из памяти о конкретном имени выгрузки, а находится на месте — тем же
// приёмом, что graph_realworld_test.go («ищем стартовый символ с хоть каким-то
// выходящим ребром»): любой достаточно частый экспортный символ общего
// модуля даст bugfix-подобный сценарий с ненулевым expansion.
func TestRealDumpLatencyBudget(t *testing.T) {
	root := retrieveRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-retrieve-15")
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
	t0 := time.Now()
	if _, err := svc.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}
	t.Logf("полная индексация %s: %s", root, time.Since(t0))

	// Находим реальный anchor: символ, у которого есть и callers, и callees
	// (иначе тест меряет вырожденный случай без expansion), через сам Build,
	// подстрочный поиск по частому слову "Получить" — тот же приём, что
	// symbol_realworld_test.go/graph_realworld_test.go этого репозитория.
	var task string
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		rows, ferr := tx.FindSymbols(store.SymbolSearch{NameNorm: "получить", Kind: "function", Limit: 200})
		if ferr != nil {
			return ferr
		}
		for _, r := range rows {
			edges, eerr := tx.CallEdgesTo(r.ID)
			if eerr != nil {
				continue
			}
			if len(edges) > 0 {
				task = "Исправь ошибку в " + r.NameDisplay
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("поиск anchor: %v", err)
	}
	if task == "" {
		t.Skip("на реальной выгрузке не нашлось символа с callers среди кандидатов 'Получить' — замер пропущен")
	}
	t.Logf("task = %q", task)

	const n = 10
	durs := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		res, err := Run(ctx, svc, st, Request{Task: task, ProjectID: projectID, Freshness: FreshnessAllowStale})
		d := time.Since(t0)
		if err != nil {
			t.Fatalf("прогон %d: Run: %v", i, err)
		}
		if res.Budget.UsedChars > res.Budget.RequestedChars {
			t.Fatalf("прогон %d: usedChars(%d) > budget(%d)", i, res.Budget.UsedChars, res.Budget.RequestedChars)
		}
		durs = append(durs, d)
	}
	p50, p95 := percentileDur(durs, 0.5), percentileDur(durs, 0.95)
	t.Logf("get_context_for_task(%q, %d прогонов): p50=%s p95=%s", task, n, p50, p95)
	if p50 > 1*time.Second {
		t.Errorf("p50 = %s, бюджет 1с превышен", p50)
	}
	if p95 > 2500*time.Millisecond {
		t.Errorf("p95 = %s, бюджет 2.5с превышен", p95)
	}
}

// TestBuildIsolatedLatency — изолированный замер Build() САМОГО ПО СЕБЕ,
// напрямую через уже открытую read-транзакцию, БЕЗ retrieve.Run/EnsureFresh
// (ревью запросило это отдельно от TestRealDumpLatencyBudget: тот меряет
// Run целиком, где EnsureFresh->precheckChangedCount статит все файлы
// выгрузки на каждый вызов — эта функция изолирует именно Build, чтобы
// доказать/опровергнуть атрибуцию p50-долга индексу, а не гадать по коду).
// N=20 прогонов, каждый — своя read-транзакция (то же самое, что делает
// каждый реальный MCP-вызов после Run), percentile тот же nearest-rank метод.
func TestBuildIsolatedLatency(t *testing.T) {
	root := retrieveRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-retrieve-15-isolated")
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
	t0 := time.Now()
	if _, err := svc.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}
	t.Logf("полная индексация %s: %s", root, time.Since(t0))

	var task string
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		rows, ferr := tx.FindSymbols(store.SymbolSearch{NameNorm: "получить", Kind: "function", Limit: 200})
		if ferr != nil {
			return ferr
		}
		for _, r := range rows {
			edges, eerr := tx.CallEdgesTo(r.ID)
			if eerr != nil {
				continue
			}
			if len(edges) > 0 {
				task = "Исправь ошибку в " + r.NameDisplay
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("поиск anchor: %v", err)
	}
	if task == "" {
		t.Skip("на реальной выгрузке не нашлось символа с callers среди кандидатов 'Получить' — замер пропущен")
	}
	t.Logf("task = %q", task)
	req := Request{Task: task, ProjectID: projectID}

	const n = 20
	durs := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		var res Result
		err := st.Read(ctx, func(tx *store.ReadTx) error {
			r, berr := Build(ctx, tx, req)
			res = r
			return berr
		})
		d := time.Since(t0)
		if err != nil {
			t.Fatalf("прогон %d: Build: %v", i, err)
		}
		if res.Budget.UsedChars > res.Budget.RequestedChars {
			t.Fatalf("прогон %d: usedChars(%d) > budget(%d)", i, res.Budget.UsedChars, res.Budget.RequestedChars)
		}
		durs = append(durs, d)
	}
	p50, p95 := percentileDur(durs, 0.5), percentileDur(durs, 0.95)
	t.Logf("Build(tx, %q) БЕЗ Run/EnsureFresh, %d прогонов: p50=%s p95=%s", task, n, p50, p95)
	if p50 > 1*time.Second {
		t.Errorf("Build p50 = %s, бюджет 1с превышен — долг ВНУТРИ internal/retrieve, не только в EnsureFresh", p50)
	}
	if p95 > 2500*time.Millisecond {
		t.Errorf("Build p95 = %s, бюджет 2.5с превышен — долг ВНУТРИ internal/retrieve, не только в EnsureFresh", p95)
	}
}

// copyBSLTreeForFreshnessTest копирует поддерево реальной выгрузки во
// временный каталог теста — тот же приём, что
// internal/index/realworld_test.go:copyTree (TestRealDumpIncrementalTwoFiles):
// тест правит файл, исходную выгрузку трогать нельзя (interfaces.md), а
// зеркало симлинками несовместимо с workspace.SafeJoin (R62). Симлинки и
// служебные каталоги (workspace.SkipDir) пропускаются.
func copyBSLTreeForFreshnessTest(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", src, err)
	}
	for _, e := range entries {
		if workspace.SkipDir(e.Name()) {
			continue
		}
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			copyBSLTreeForFreshnessTest(t, s, d)
			continue
		}
		info, err := e.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", s, err)
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", d, err)
		}
	}
}

// firstBSLFile находит первый (по лексикографическому порядку обхода,
// детерминированно) .bsl-файл под root — тот же приём, что
// internal/index/realworld_test.go:findTwoBSLFiles.
func firstBSLFile(t *testing.T, root string) string {
	t.Helper()
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			if workspace.SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil {
				found = rel
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir %s: %v", root, err)
	}
	if found == "" {
		t.Fatalf("под %s не нашлось .bsl файла", root)
	}
	return found
}

// TestRealDumpFreshnessAfterDiskEdit — регрессия доводки precheck
// (internal/index/discover.go:discoverComponentMeta заменил
// discoverComponent внутри precheckChangedCount ради latency §28, см.
// TestBuildIsolatedLatency): ускорение НЕ имеет права ослабить
// freshness-гарантию §18.5/§23 — устаревшее под видом свежего не отдаётся
// никогда (R33.1). discoverComponentMeta по-прежнему статит КАЖДЫЙ файл
// компонента (просто без workspace.SafeJoin, который precheck не
// использует), так что обнаружение изменений обязано остаться точным.
//
// Сценарий на реальных данных (копия CommonModules из ut_demo, тот же приём,
// что TestRealDumpIncrementalTwoFiles): холодный full index -> первый Run
// (allow-stale, без ручного reindex) с задачей на СУЩЕСТВУЮЩУЮ функцию —
// контроль, что фикстура вообще резолвится -> в файл дописывается новая
// экспортная функция с уникальным именем, mtime сдвигается в будущее
// (touchFuture, как и остальные incremental-тесты этого репозитория) ->
// второй Run (снова allow-stale, снова без ручного reindex) с задачей,
// называющей НОВУЮ функцию по имени. SmallChangeFileLimit по умолчанию 50
// покрывает 1 изменённый файл, поэтому EnsureFresh обязан сделать
// синхронный инкремент ДО ответа (§18.5) — если бы discoverComponentMeta по
// ошибке недосчитывал изменения (например, забыл сравнить mtime не только
// новых, но и уже известных файлов), второй Run отдал бы факты по старому
// содержимому файла, молча пометив ответ как fresh: этот тест ловит именно
// такой регресс, а не просто «что-то изменилось».
func TestRealDumpFreshnessAfterDiskEdit(t *testing.T) {
	dumpRoot := retrieveRealDumpRoot(t)
	origRoot := filepath.Join(dumpRoot, "CommonModules")
	if _, err := os.Stat(origRoot); err != nil {
		t.Skipf("в выгрузке нет CommonModules: %v", err)
	}
	root := t.TempDir()
	copyBSLTreeForFreshnessTest(t, origRoot, filepath.Join(root, "CommonModules"))

	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-retrieve-freshness-after-edit")
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
		t.Fatalf("Reindex(full) на копии CommonModules из %s: %v", dumpRoot, err)
	}

	targetRel := firstBSLFile(t, root)
	targetAbs := filepath.Join(root, targetRel)
	// moduleName — "X" из "CommonModules/X/Ext/Module.bsl" (структура
	// выгрузки), нужен только для осмысленного текста задачи ДО правки.
	moduleName := filepath.Base(filepath.Dir(filepath.Dir(targetRel)))
	t.Logf("правим %s (модуль %s)", targetRel, moduleName)

	// Контроль ДО правки: get_context_for_task без ручного reindex сразу
	// после cold full index обязан быть fresh (нечего инкрементировать).
	before, err := Run(ctx, svc, st, Request{
		Task:      "Расскажи про модуль " + moduleName,
		ProjectID: projectID, Freshness: FreshnessAllowStale,
	})
	if err != nil {
		t.Fatalf("Run (до правки): %v", err)
	}
	if stale, msg := staleIndexWarning(before); stale {
		t.Fatalf("до правки диска warning stale_index=%q — precheck не должен видеть изменений сразу после full index", msg)
	}

	const newFuncName = "ПроверкаСвежестиТикетДоводкаPrecheck"
	data, err := os.ReadFile(targetAbs)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", targetAbs, err)
	}
	edited := string(data) + "\n\nФункция " + newFuncName + "() Экспорт\n\tВозврат Истина;\nКонецФункции\n"
	if err := os.WriteFile(targetAbs, []byte(edited), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", targetAbs, err)
	}
	touchFutureRetrieve(t, targetAbs)

	after, err := Run(ctx, svc, st, Request{
		Task: "Исправь ошибку в " + newFuncName, ProjectID: projectID, Freshness: FreshnessAllowStale,
	})
	if err != nil {
		t.Fatalf("Run (после правки): %v", err)
	}
	if stale, msg := staleIndexWarning(after); stale {
		t.Errorf("после правки warning stale_index=%q — 1 файл в пределах SmallChangeFileLimit(50) обязан дать синхронный инкремент, не warning-ответ из старой эпохи", msg)
	}

	foundAnchor := false
	for _, a := range after.Anchors {
		if strings.Contains(a.Display, newFuncName) {
			foundAnchor = true
		}
	}
	text := factsAndRelationsText(after)
	if !foundAnchor && !strings.Contains(text, newFuncName) {
		t.Fatalf("новая функция %q не видна в ответе после правки диска (anchors=%+v) — либо precheck не заметил изменение, либо соврал fresh на самом деле устаревшему индексу", newFuncName, after.Anchors)
	}
}

// staleIndexWarning сообщает, несёт ли Result предупреждение stale_index
// (Build выставляет его ровно тогда, когда req.Stale=true, см.
// internal/retrieve/build.go) — то же самое, что build_test.go проверяет
// вручную циклом по Warnings в каждом тесте freshness.
func staleIndexWarning(res Result) (bool, string) {
	for _, w := range res.Warnings {
		if w.Code == "stale_index" {
			return true, w.Message
		}
	}
	return false, ""
}

// touchFutureRetrieve — см. internal/index/incremental_test.go:touchFuture;
// продублировано здесь (без экспорта из internal/index) ровно на один вызов,
// не тянуть ради этого межпакетную зависимость только для тестов.
func touchFutureRetrieve(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}

// TestRealDumpFormIntentReturnsHandlerData — регрессия evaluation-report.md
// §4.3 на реальной выгрузке ut_demo: обе form-задачи прогона (evals/06-form-
// warehouse-onchange.json, evals/07-form-nomenclature-kind.json) вернули
// `missing` по ВСЕМ четырём обязательным категориям intent=form (binding,
// handler, server_calls, attributes) БЕЗ единого warning, хотя follow-up
// get_form_handlers (internal/app/meta.go, тот же индекс) находил реальные
// данные (67061/55100 байт). Root cause — не builder expandForm сам по
// себе, а anchors.go: имя обработчика формы "СкладПриИзменении"/
// "ВидНоменклатурыПриИзменении" — конвенция именования, совпадает как ГОЛЫЙ
// символ в шести+ разных формах на реальной выгрузке; findAnchors раньше
// шёл одним интерливом (структурные+объекты+символы вперемешку по
// кандидатам), и maxAnchors=6 съедался этими одноимёнными символами ДО
// того, как кандидат "ЗаказКлиента"/"Номенклатура" (нужный expandForm
// metadata_object anchor) вообще успевал попасть в список — обрезался
// молча (никакого warning: expandForm/expandRegister/... на чужом a.Kind
// честно возвращают nil,nil, это не их баг). Фикс — anchors.go теперь идёт
// тремя проходами (структурные -> объекты метаданных -> голые символы), так
// metadata_object anchor не вытесняется массовыми тёзками-обработчиками.
func TestRealDumpFormIntentReturnsHandlerData(t *testing.T) {
	root := retrieveRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-retrieve-form-regression")
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
	t0 := time.Now()
	if _, err := svc.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}
	t.Logf("полная индексация %s: %s", root, time.Since(t0))

	// Тексты задач — буквально evals/06-form-warehouse-onchange.json и
	// evals/07-form-nomenclature-kind.json (не парсим JSON из evals/, чтобы
	// не создавать зависимость retrieve -> evals/ — эта директория честно
	// не production-код, см. doc-комментарий в конце evaluation-report.md).
	cases := []struct {
		name       string
		task       string
		wantOwner  string // подстрока NameDisplay ожидаемого metadata_object anchor
		wantSymbol string // подстрока имени символа-обработчика в handler/binding
	}{
		{
			name:       "06-form-warehouse-onchange",
			task:       "Поменяй обработчик СкладПриИзменении на форме ФормаДокумента документа ЗаказКлиента — при смене склада нужно дополнительно пересчитывать доступность товаров",
			wantOwner:  "ЗаказКлиента",
			wantSymbol: "СкладПриИзменении",
		},
		{
			name:       "07-form-nomenclature-kind",
			task:       "Поменяй обработчик ВидНоменклатурыПриИзменении на форме элемента справочника Номенклатура — нужно скрывать несовместимые упаковки при смене вида номенклатуры",
			wantOwner:  "Номенклатура",
			wantSymbol: "ВидНоменклатурыПриИзменении",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := Run(ctx, svc, st, Request{Task: c.task, ProjectID: projectID, Freshness: FreshnessAllowStale})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Intent.Primary != IntentForm {
				t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
			}

			foundOwnerAnchor := false
			for _, a := range res.Anchors {
				if a.Kind == "metadata_object" && strings.Contains(a.Display, c.wantOwner) {
					foundOwnerAnchor = true
				}
			}
			if !foundOwnerAnchor {
				t.Fatalf("anchors не содержит metadata_object %q — anchors=%+v (баг §4.3: обрезан массовыми тёзками-символами)", c.wantOwner, res.Anchors)
			}

			for _, category := range []string{"binding", "handler", "server_calls", "attributes"} {
				cov, ok := coverageOf(res, category)
				if !ok {
					t.Fatalf("requiredCoverage не содержит категорию %q", category)
				}
				if cov.Status == Missing {
					t.Errorf("категория %q = missing (баг §4.3 не исправлен): coverage=%+v warnings=%+v", category, cov, res.Warnings)
				}
			}

			text := factsAndRelationsText(res)
			if !strings.Contains(text, c.wantSymbol) {
				t.Errorf("ответ не содержит имя обработчика %q: %q", c.wantSymbol, text)
			}
		})
	}
}

// duplicateWarning возвращает первую пару (Code,Message), встреченную в ws
// более одного раза — используется регрессионными тестами вместо голого
// len(ws), чтобы падение называло КОНКРЕТНОЕ предупреждение-дубликат, а не
// просто «что-то не так со счётом».
func duplicateWarning(ws []Warning) (Warning, bool) {
	seen := map[string]bool{}
	for _, w := range ws {
		key := w.Code + "\x00" + w.Message
		if seen[key] {
			return w, true
		}
		seen[key] = true
	}
	return Warning{}, false
}

// TestRealDumpFormIntentAmbiguousHomonymNoDuplicateWarning — регрессия P5
// доводки (круг 2): реалистичная задача «Почему не отображается команда в
// форме списка справочника Номенклатура» (focusHints: Номенклатура,
// ФормаСписка) на ut_demo. «Номенклатура» — частое имя: exactMetadataLookup
// (anchors.go) честно находит ТРИ объекта метаданных с этим name_norm разных
// видов (Catalog, CommonPicture, DefinedType — подтверждено вручную:
// MetadataObjectsByNameNormAnyType("номенклатура") реально возвращает все
// три на этой выгрузке) и корректно репортит это как Ambiguity. findAnchors
// строит anchor на КАЖДЫЙ из них (это не баг — данные объективно
// неоднозначны), но CommonPicture и DefinedType структурно не могут иметь
// форм — expandForm (expand2.go) для каждого из них честно находит 0 форм и
// возвращает warning "no_forms" с текстом, несущим только общее для
// омонимов Display-имя — отсюда БУКВАЛЬНЫЙ дубликат одного и того же
// предупреждения в списке (не два разных факта, один и тот же текст дважды).
//
// Этот тест — НЕ про потерю anchor'а объекта-владельца (Catalog.Номенклатура
// уже проверяется TestRealDumpFormIntentReturnsHandlerData/D07-приёмом и
// здесь ниже — anchor есть, maxAnchors=6 не обрезает 5 anchors этого
// сценария). Дубликат чинится дедупликацией warnings на границе агрегации в
// Build (dedupWarnings, build.go) — не точечным патчем внутри expandForm,
// потому что тот же паттерн («несколько anchor одного intent формируют
// warning с одинаковым текстом») воспроизводим ЛЮБЫМ typed expansion
// builder'ом, не только expandForm.
func TestRealDumpFormIntentAmbiguousHomonymNoDuplicateWarning(t *testing.T) {
	root := retrieveRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-retrieve-form-ambiguous-homonym")
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
	t0 := time.Now()
	if _, err := svc.Reindex(ctx, index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}
	t.Logf("полная индексация %s: %s", root, time.Since(t0))

	req := Request{
		Task:       "Почему не отображается команда в форме списка справочника Номенклатура",
		FocusHints: []string{"Номенклатура", "ФормаСписка"},
		ProjectID:  projectID, Freshness: FreshnessAllowStale,
	}
	res, err := Run(ctx, svc, st, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q (лексикон должен классифицировать «в форме списка» как form)", res.Intent.Primary, IntentForm)
	}

	// Anchor объекта-владельца (Catalog.Номенклатура) обязан присутствовать —
	// подтверждает, что омонимы (CommonPicture/DefinedType) НЕ вытеснили его
	// (гипотеза «maxAnchors съеден коллизией» из тикета — проверена и здесь
	// не подтвердилась: 5 anchors этого сценария < maxAnchors=6).
	foundCatalogAnchor := false
	for _, a := range res.Anchors {
		if a.Kind == "metadata_object" && a.ObjectType == "Catalog" && strings.Contains(a.Display, "Номенклатура") {
			foundCatalogAnchor = true
		}
	}
	if !foundCatalogAnchor {
		t.Fatalf("anchors не содержит metadata_object Catalog.Номенклатура — anchors=%+v", res.Anchors)
	}

	// Ядро регрессии: ни одна пара (Code,Message) не повторяется в Warnings.
	if dup, ok := duplicateWarning(res.Warnings); ok {
		t.Errorf("Warnings содержит дубликат %+v (баг: no_forms повторяется по числу нерелевантных omonym-anchors без дедупликации) — Warnings=%+v", dup, res.Warnings)
	}

	// Явная проверка конкретно по no_forms (та категория, где дубликат
	// воспроизведён живьём): не более одного предупреждения с этим кодом,
	// сколько бы омонимов без форм ни попало в anchors.
	noFormsCount := 0
	for _, w := range res.Warnings {
		if w.Code == "no_forms" {
			noFormsCount++
		}
	}
	if noFormsCount > 1 {
		t.Errorf("no_forms встречается %d раз в Warnings, ожидался максимум 1 — Warnings=%+v", noFormsCount, res.Warnings)
	}
}

// percentileDur — тот же nearest-rank метод, что internal/app/symbol_realworld_test.go:percentile.
func percentileDur(durs []time.Duration, p float64) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), durs...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	idx := int(p*float64(len(sorted))+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// TestRealDumpFormIntentNoCompleteEmpty — D03 (§6 спецификации): проверка на
// РЕАЛЬНОЙ выгрузке того, что регрессия таска 06 не вернётся. Интент form в
// этот прогон не входит и ничего не заявляет собранным, поэтому ни одна его
// обязательная категория не имеет права на complete_empty — а вызов, в котором
// не совпало вообще ничего (на выгрузке с расширениями именно так: warning
// form_binding_not_matched, все четыре категории пусты), обязан остаться
// insufficient. Утверждение не зависит от того, какая выгрузка подставлена:
// на выгрузке, где обработчик совпадает, оно проверяет ту же инвариантность на
// непустом ответе. Скипается только отсутствие самой выгрузки.
func TestRealDumpFormIntentNoCompleteEmpty(t *testing.T) {
	root := retrieveRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("retrieve-form-complete-empty-guard")
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

	const task = "Поменяй обработчик ВидНоменклатурыПриИзменении на форме элемента справочника Номенклатура — нужно скрывать несовместимые упаковки при смене вида номенклатуры"
	res, err := Run(ctx, svc, st, Request{Task: task, ProjectID: projectID, Freshness: FreshnessAllowStale})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Intent.Primary != IntentForm {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentForm)
	}
	allEmpty := len(res.RequiredCoverage) > 0
	for _, cov := range res.RequiredCoverage {
		if cov.Status == CompleteEmpty {
			t.Errorf("категория %q = complete_empty, а интент form сборку не заявлял: %+v", cov.Category, cov)
		}
		if cov.TotalCount != 0 {
			allEmpty = false
		}
	}
	if allEmpty && res.SufficiencyStatus != Insufficient {
		t.Fatalf("не собрано НИЧЕГО, а ответ объявил себя %q: coverage=%+v warnings=%+v",
			res.SufficiencyStatus, res.RequiredCoverage, res.Warnings)
	}
	t.Logf("выгрузка %s: allEmpty=%v sufficiency=%s", root, allEmpty, res.SufficiencyStatus)
}
