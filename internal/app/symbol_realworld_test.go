package app

import (
	"context"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// symbolToolsRealDumpEnvVars — тот же приём, что internal/index/realworld_test.go и
// internal/resolve/realworld_test.go: путь к реальной выгрузке из
// переменной окружения, никогда не зашит в код (interfaces.md, §28).
var symbolToolsRealDumpEnvVars = []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"}

func symbolToolsRealDumpRoot(t *testing.T) string {
	t.Helper()
	for _, env := range symbolToolsRealDumpEnvVars {
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

// percentile — p в [0,1], ближайший-вверх элемент отсортированного среза
// (nearest-rank method) — то же определение, что бюджеты §28 используют.
func percentile(durs []time.Duration, p float64) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// newRealDumpProject строит store+index.Service НАПРЯМУЮ на реальной
// выгрузке (тот же приём, что internal/index/realworld_test.go: Manifest
// собирается в памяти, а не через LoadManifest+1c-project.json — читать
// выгрузку нужно только на чтение, писать в неё манифест нельзя, выгрузка
// общая для параллельных прогонов). *Projects — обычный, но с уже прогретым
// opened-кэшем: Active(ctx) должен ПОПАСТЬ в кэш и НЕ дойти до
// workspace.LoadManifest (единственный путь без записи в реестр на диске
// внутри выгрузки).
func newRealDumpProject(t *testing.T) (*Projects, *openProject) {
	t.Helper()
	root := symbolToolsRealDumpRoot(t)
	builtins := syntaxtest.RealOrSkip(t)
	const projectID = domain.ProjectID("utdemo-symtools-11")
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

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	// Root реестровой записи — bogus: LoadManifest по нему не пойдёт, opened
	// уже прогрет ниже.
	if err := reg.Upsert(workspace.ProjectEntry{ID: projectID, Root: workspaceRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject(projectID); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	p := &Projects{workspaceRoot: workspaceRoot, registry: reg, builtins: builtins, opened: map[domain.ProjectID]*openProject{}}
	op := &openProject{Entry: workspace.ProjectEntry{ID: projectID, Root: workspaceRoot}, Manifest: manifest, Store: st, Service: svc}
	p.opened[projectID] = op

	ctx := context.Background()
	t0 := time.Now()
	res, err := svc.Reindex(ctx, index.ModeFull, "")
	if err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}
	t.Logf("полная индексация %s: %s, файлов изменено %d", root, time.Since(t0), sumFilesChanged(res))
	return p, op
}

func sumFilesChanged(r index.Result) int {
	n := 0
	for _, c := range r.Components {
		n += c.FilesChanged
	}
	return n
}

// TestRealDumpSymbolToolLatencyBudgets — критерии приёмки тикета 11: p50/p95
// find_symbol (20/50мс) и find_references (50/200мс) РЕАЛЬНО замерены на
// ut_demo — N вызовов, не одна выборка. get_module_structure на самом
// большом по числу символов модуле выгрузки не должен утечь текст модуля
// (то же требование, что symbol_test.go проверяет на фикстуре — здесь то же
// самое, но на реальных данных, где утечка тела была бы намного заметнее
// по объёму ответа).
func TestRealDumpSymbolToolLatencyBudgets(t *testing.T) {
	p, op := newRealDumpProject(t)
	symSvc := NewSymbolService(p)
	ctx := context.Background()

	// "Получить" — частая приставка имён функций в конфигурациях 1С; на
	// ut_demo даёт реалистичную непустую подстрочную выборку без ручного
	// перечисления конкретных имён (они у каждой выгрузки свои).
	const needle = "Получить"

	t.Run("find_symbol_p50_p95", func(t *testing.T) {
		const n = 40
		durs := make([]time.Duration, 0, n)
		var lastCount int
		for i := 0; i < n; i++ {
			t0 := time.Now()
			resp, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: needle, Limit: 50})
			d := time.Since(t0)
			if err != nil {
				t.Fatalf("FindSymbol(%q) прогон %d: %v", needle, i, err)
			}
			durs = append(durs, d)
			lastCount = len(resp.Items)
		}
		if lastCount == 0 {
			t.Fatalf("FindSymbol(%q) на реальной выгрузке вернул 0 совпадений — подстрока подобрана плохо, замер недостоверен", needle)
		}
		p50, p95 := percentile(durs, 0.5), percentile(durs, 0.95)
		t.Logf("find_symbol(%q, %d прогонов, %d совпадений): p50=%s p95=%s", needle, n, lastCount, p50, p95)
		if p50 > 20*time.Millisecond {
			t.Errorf("find_symbol p50 = %s, бюджет 20мс превышен", p50)
		}
		if p95 > 50*time.Millisecond {
			t.Errorf("find_symbol p95 = %s, бюджет 50мс превышен", p95)
		}
	})

	t.Run("get_module_structure_no_module_text_leak", func(t *testing.T) {
		found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: needle, Limit: 200})
		if err != nil || len(found.Items) == 0 {
			t.Fatalf("FindSymbol setup: %+v %v", found.Items, err)
		}
		// Модуль с наибольшим числом найденных совпадений среди страницы —
		// плейсхолдер «самого большого модуля»: настоящий top-1 по count(*)
		// потребовал бы агрегата, которого store сегодня не выставляет
		// (interfaces.md, «Из таска 10»); подстрочный поиск по частой
		// приставке — практическая замена без выдумывания нового примитива.
		counts := map[string]int{}
		for _, s := range found.Items {
			counts[s.Module]++
		}
		var biggestModule string
		best := 0
		for m, c := range counts {
			if c > best {
				best, biggestModule = c, m
			}
		}
		resp, err := symSvc.GetModuleStructure(ctx, GetModuleStructureInput{Module: biggestModule, Component: "cfg"})
		if err != nil {
			t.Fatalf("GetModuleStructure(%s): %v", biggestModule, err)
		}
		if len(resp.Items) != 1 {
			t.Fatalf("Items = %d, want 1", len(resp.Items))
		}
		if resp.Items[0].SymbolCount+resp.Items[0].VariableCount == 0 {
			t.Fatalf("модуль %s: 0 символов и переменных — подозрительно для реального модуля с найденными совпадениями", biggestModule)
		}
		// Гард против утечки текста модуля: ни у одного символа сигнатура не
		// длиннее разумной сигнатуры (сигнатура — «имя(параметры)», не тело).
		for _, s := range resp.Items[0].Symbols {
			if len(s.Signature) > 500 {
				t.Errorf("Signature символа %s длиной %d — похоже на утёкшее тело, не сигнатуру", s.Name, len(s.Signature))
			}
		}
	})

	t.Run("find_references_p50_p95_and_correctness", func(t *testing.T) {
		found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: needle, Kind: string(domain.SymbolFunction), Limit: 50})
		if err != nil || len(found.Items) == 0 {
			t.Fatalf("FindSymbol setup: %+v %v", found.Items, err)
		}
		// Ищем среди кандидатов символ с непустой выдачей find_references —
		// не любое совпадение needle обязательно вызывается где-то ещё.
		graphSvc := NewGraphService(p)
		var target string
		var refCount int
		for _, s := range found.Items {
			resp, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: s.UID, Limit: 200})
			if err != nil {
				t.Fatalf("FindReferences(%s): %v", s.UID, err)
			}
			c := 0
			for _, g := range resp.Items {
				c += len(g.References)
			}
			if c > refCount {
				refCount, target = c, s.UID
			}
			if refCount > 0 {
				break
			}
		}
		if target == "" {
			t.Skip("ни один кандидат по подстроке не имеет ссылок на реальной выгрузке — замер пропущен")
		}

		const n = 20
		durs := make([]time.Duration, 0, n)
		for i := 0; i < n; i++ {
			t0 := time.Now()
			resp, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: target, Limit: 200})
			d := time.Since(t0)
			if err != nil {
				t.Fatalf("FindReferences прогон %d: %v", i, err)
			}
			durs = append(durs, d)
			total := 0
			for _, g := range resp.Items {
				total += len(g.References)
			}
			if total != refCount {
				t.Errorf("прогон %d: total=%d, want %d (то же между вызовами без reindex)", i, total, refCount)
			}
		}
		p50, p95 := percentile(durs, 0.5), percentile(durs, 0.95)
		t.Logf("find_references(%s, %d прогонов, %d ссылок): p50=%s p95=%s", target, n, refCount, p50, p95)
		if p50 > 50*time.Millisecond {
			t.Errorf("find_references p50 = %s, бюджет 50мс превышен", p50)
		}
		if p95 > 200*time.Millisecond {
			t.Errorf("find_references p95 = %s, бюджет 200мс превышен", p95)
		}
	})

	_ = op
}
