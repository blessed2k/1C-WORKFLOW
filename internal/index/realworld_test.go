package index

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// dumpEnvVars — те же переменные, что internal/parse/{bsl,meta,query} и
// internal/resolve (см. их realworld/corpus тесты): один прогон, один и тот
// же путь к выгрузке, не зашитый в код (interfaces.md, §28).
var dumpEnvVars = []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"}

func realDumpRoot(t *testing.T) string {
	t.Helper()
	for _, env := range dumpEnvVars {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if _, err := os.Stat(v); err != nil {
				t.Skipf("%s указывает на недоступный путь: %v", env, err)
			}
			return v
		}
	}
	t.Skip("переменная окружения ONEC_DUMP не задана — прогон на реальной выгрузке пропущен")
	return ""
}

// TestRealDumpFullIndex — R89i: полный индекс на реальной выгрузке УТ
// (~48 698 файлов, 12 434 .bsl) проходит до конца; время и размер файла
// эпохи — числа бюджета §28 (cold full index < 90с), логируются, а не
// подгоняются.
func TestRealDumpFullIndex(t *testing.T) {
	root := realDumpRoot(t)
	st := openTestStore(t)
	m := workspace.Manifest{
		Version: 1, Project: "utdemo", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
	builtins := syntaxtest.RealOrSkip(t)
	svc := NewService(st, "utdemo", m, builtins, Config{})
	t.Cleanup(func() { svc.Close() })

	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	ctx := context.Background()
	start := time.Now()
	res, err := svc.Reindex(ctx, ModeFull, "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке: %v", err)
	}
	runtime.ReadMemStats(&memAfter)

	st2, statusErr := st.Status(ctx)
	if statusErr != nil {
		t.Fatalf("Status: %v", statusErr)
	}

	t.Logf("полный индекс ut_demo: %s (бюджет §28 < 90с)", elapsed)
	t.Logf("файлов republish: %d, символов: %d", res.Components[0].FilesChanged, res.Components[0].SymbolCount)
	t.Logf("размер эпохи (DB): %d байт (%.1f МиБ)", st2.DBBytes, float64(st2.DBBytes)/(1<<20))
	t.Logf("HeapAlloc до/после (не peak RSS процесса, приближение): %.1f МиБ -> %.1f МиБ",
		float64(memBefore.HeapAlloc)/(1<<20), float64(memAfter.HeapAlloc)/(1<<20))
	if len(res.Components[0].Diagnostics) > 0 {
		t.Logf("diagnostics: %d (первые до 5): %v", len(res.Components[0].Diagnostics), firstN(res.Components[0].Diagnostics, 5))
	}

	if elapsed > 90*time.Second {
		t.Errorf("cold full index занял %s, бюджет §28 — 90с", elapsed)
	}

	err = st.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() на реальной выгрузке = %v, want пусто", bad)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}

func firstN(d []domain.Diagnostic, n int) []domain.Diagnostic {
	if len(d) < n {
		return d
	}
	return d[:n]
}

// TestRealDumpIncrementalTwoFiles — R32/R89i: правка двух реальных .bsl
// файлов выгрузки даёт инкремент, время — число бюджета §28 (< 300 мс),
// логируется без подгонки.
func TestRealDumpIncrementalTwoFiles(t *testing.T) {
	dumpRoot := realDumpRoot(t)
	// Подвыборка реальной выгрузки (не весь ut_demo — копия 2.2 ГБ на
	// каждый прогон теста того не стоит, а SafeJoin отказывает в симлинках
	// наружу корня, R62, так что зеркало ссылками несовместимо с ним):
	// CommonModules — самостоятельный, немаленький (5478 файлов) срез
	// реального кода. Тест правит файлы, исходную выгрузку трогать нельзя
	// (interfaces.md).
	// Владелец модуля (bsl.ClassifyModule) выводится из ПУТИ: первый сегмент
	// обязан быть коллекцией выгрузки ("CommonModules"), поэтому копия
	// сохраняет "CommonModules/..." — плоская копия одних внутренностей
	// сломала бы OwnerType/OwnerName молча.
	origRoot := filepath.Join(dumpRoot, "CommonModules")
	if _, err := os.Stat(origRoot); err != nil {
		t.Skipf("в выгрузке нет CommonModules: %v", err)
	}
	root := t.TempDir()
	copyTree(t, origRoot, filepath.Join(root, "CommonModules"))
	files := findTwoBSLFiles(t, root)

	st := openTestStore(t)
	m := workspace.Manifest{
		Version: 1, Project: "utdemo", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
	builtins := syntaxtest.RealOrSkip(t)
	svc := NewService(st, "utdemo", m, builtins, Config{})
	t.Cleanup(func() { svc.Close() })

	ctx := context.Background()
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	genBefore, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	for _, rel := range files {
		abs := filepath.Join(root, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, append(data, []byte("\n// touch\n")...), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", rel, err)
		}
		touchFuture(t, abs)
	}

	start := time.Now()
	res, err := svc.Reindex(ctx, ModeIncremental, "")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	t.Logf("инкремент 2 реальных BSL: %s (бюджет §28 < 300мс)", elapsed)
	if elapsed > 300*time.Millisecond {
		t.Errorf("инкремент занял %s, бюджет §28 — 300мс", elapsed)
	}

	genAfter, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if genAfter.GenerationNumber != genBefore.GenerationNumber+1 {
		t.Errorf("GenerationNumber = %d, want %d", genAfter.GenerationNumber, genBefore.GenerationNumber+1)
	}
	if len(res.Components) != 1 || res.Components[0].FilesChanged < 2 {
		t.Errorf("FilesChanged = %+v, want >= 2", res.Components)
	}
}

// findTwoBSLFiles находит два произвольных .bsl файла реальной выгрузки для
// правки (детерминированно — первые по сортировке относительного пути).
func findTwoBSLFiles(t *testing.T, root string) []string {
	t.Helper()
	discovered, err := discoverComponent(root, nil, nil)
	if err != nil {
		t.Fatalf("discoverComponent: %v", err)
	}
	var out []string
	for _, d := range discovered {
		if strings.HasSuffix(strings.ToLower(d.relPath), ".bsl") {
			out = append(out, d.relPath)
			if len(out) == 2 {
				break
			}
		}
	}
	if len(out) < 2 {
		t.Fatalf("в выгрузке %s не нашлось двух .bsl файлов", root)
	}
	return out
}

// copyTree копирует дерево файлов src -> dst (тест не пишет в исходную
// выгрузку). Служебные каталоги (workspace.SkipDir) не копируются — они и не
// индексируются.
func copyTree(t *testing.T, src, dst string) {
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
			copyTree(t, s, d)
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
