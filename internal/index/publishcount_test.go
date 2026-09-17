package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// TestPublishedCountsMatchDerivedFacts — ревью нашло дыру мутацией
// (`results = results[1:]` в publishRegisterAccess): snapshotCorpus сравнивает
// corpus/resolved ДО публикации (проверяет резолюцию), а не то, что реально
// осело в store — пропажу факта ВНУТРИ publishXxx ни один из прежних тестов
// не видел. Здесь ComponentResult.Counts (ts.counts, publish.go — считается
// РОВНО в момент успешного tx.InsertXxx) сравнивается с числом фактов,
// которые resolve.DeriveXxx/сам corpus производят для того же корпуса,
// пересчитанным заново тестом — независимо от publish-кода.
//
// Не покрыто здесь тем же способом (честно, не подгоняется): event_subscription,
// scheduled_job, role_right — в этой подвыборке таких фактов нет (папки
// EventSubscriptions/ScheduledJobs/Roles не входят в Catalogs/Номенклатура), а
// строить их синтетически ради счётчика заново — риск в самом независимом
// пересчёте теста незаметно повторить баг publish-кода. У них по крайней
// мере есть собственные publishXxx-функции с ts.counts.* — реальная
// публикация идёт тем же путём, что у покрытых здесь видов, просто без
// независимого счётчика в тесте.
func TestPublishedCountsMatchDerivedFacts(t *testing.T) {
	dumpRoot := realDumpRoot(t)
	relRoot := filepath.Join("Catalogs", "Номенклатура")
	src := filepath.Join(dumpRoot, relRoot)
	if _, err := os.Stat(src); err != nil {
		t.Skipf("в выгрузке нет Catalogs/Номенклатура: %v", err)
	}
	root := t.TempDir()
	copyTree(t, src, filepath.Join(root, relRoot))

	ctx := context.Background()
	st := openTestStore(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	res, err := svc.Reindex(ctx, ModeFull, "")
	if err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	if len(res.Components) != 1 {
		t.Fatalf("Components = %+v, want 1", res.Components)
	}
	got := res.Components[0].Counts

	// Пересчёт ожидаемого НЕЗАВИСИМО от publish-кода: тот же Env (строится
	// той же buildEnvInput/resolve.NewEnv, что и пайплайн — это не второй
	// резолвер, а прогон ТЕХ ЖЕ производящих функций второй раз для
	// сравнения, ровно то, что просил ревьюер), но подсчёт публикуемых строк
	// — своей отдельной арифметикой, а не копией publishXxx.
	corpus := svc.corpora["cfg"]
	envInput, err := buildEnvInput("proj", "cfg", domain.BaseLayer("cfg"), corpus)
	if err != nil {
		t.Fatalf("buildEnvInput: %v", err)
	}
	env, err := resolve.NewEnv(envInput, nil)
	if err != nil {
		t.Fatalf("resolve.NewEnv: %v", err)
	}

	var wantRegisterAccess, wantQuery, wantQueryReference, wantFormElement, wantFormCommand, wantHandlerBinding, wantDependencyEdge int
	for _, rec := range corpus.files {
		if rec.bslModule == nil {
			continue
		}
		wantRegisterAccess += len(resolve.DeriveRegisterAccess(rec.bslModule, env))
		for _, ql := range rec.bslModule.Queries {
			if ql.Method >= 0 {
				wantQuery++
			}
		}
		// query_reference (таск 12, долг тасков 08/09): та же независимая
		// арифметика, что publishQueryReferences использует для публикации —
		// группа литерала считается ТОЛЬКО если у литерала есть query_id
		// (литерал внутри метода, как у wantQuery выше).
		for _, g := range resolve.DeriveQueryReference(rec.bslModule, env) {
			if g.LiteralIndex < len(rec.bslModule.Queries) && rec.bslModule.Queries[g.LiteralIndex].Method >= 0 {
				wantQueryReference += len(g.References)
			}
		}
	}
	for _, rec := range corpus.files {
		if rec.metaFacts.FormStructure == nil {
			continue
		}
		fs := rec.metaFacts.FormStructure
		wantFormElement += len(fs.Elements)
		wantFormCommand += len(fs.Commands)
		if len(fs.Handlers) > 0 {
			formModulePath := domain.NormalizeModulePath(strings.TrimSuffix(rec.relPath, "Form.xml") + "Form/Module.bsl")
			wantHandlerBinding += len(resolve.DeriveHandlerBinding(formModulePath, fs.Handlers, env))
		}
	}
	for _, e := range resolve.DeriveDependencyEdges(env) {
		_ = e
		wantDependencyEdge++
	}

	if got.registerAccess != wantRegisterAccess {
		t.Errorf("register_access опубликовано %d, ожидалось %d (DeriveRegisterAccess по корпусу)", got.registerAccess, wantRegisterAccess)
	}
	if got.query != wantQuery {
		t.Errorf("query опубликовано %d, ожидалось %d (литералов внутри метода)", got.query, wantQuery)
	}
	if got.queryReference != wantQueryReference {
		t.Errorf("query_reference опубликовано %d, ожидалось %d (DeriveQueryReference по корпусу)", got.queryReference, wantQueryReference)
	}
	if got.formElement != wantFormElement {
		t.Errorf("form_element опубликовано %d, ожидалось %d", got.formElement, wantFormElement)
	}
	if got.formCommand != wantFormCommand {
		t.Errorf("form_command опубликовано %d, ожидалось %d", got.formCommand, wantFormCommand)
	}
	if got.handlerBinding != wantHandlerBinding {
		t.Errorf("handler_binding опубликовано %d, ожидалось %d", got.handlerBinding, wantHandlerBinding)
	}
	if got.dependencyEdge != wantDependencyEdge {
		t.Errorf("dependency_edge опубликовано %d, ожидалось %d (DeriveDependencyEdges по Env)", got.dependencyEdge, wantDependencyEdge)
	}

	if wantRegisterAccess == 0 && wantQuery == 0 && wantFormElement == 0 {
		t.Fatal("ожидаемые счётчики все нулевые — тест ничего не проверяет (подвыборка не подходит)")
	}
	if wantQuery > 0 && wantQueryReference == 0 {
		t.Fatal("есть query, но ни одной query_reference — подозрительно для этой подвыборки, проверка не была бы значимой")
	}
}
