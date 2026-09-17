package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// realDumpExtensionProject строит проект на РЕАЛЬНОЙ выгрузке вместе с её
// расширениями: манифест берётся из самой выгрузки (1c-project.json), а не
// собирается в памяти, — иначе слои расширений в проект не попадут и
// проверять было бы нечего. Путь к выгрузке — только из ONEC_DUMP
// (symbolToolsRealDumpRoot), никогда не зашит в код.
//
// Выгрузка без манифеста или без расширений — честный Skip, а не падение:
// у ut_demo расширений нет, пары «перехватчик → цель» задаются окружением
// под выгрузку с расширениями.
func realDumpExtensionProject(t *testing.T) (*Projects, workspace.Manifest) {
	t.Helper()
	root := symbolToolsRealDumpRoot(t)
	manifest, err := workspace.LoadManifest(root)
	if err != nil {
		t.Skipf("в выгрузке %s нет читаемого 1c-project.json (%v) — слои расширений не заданы", root, err)
	}
	if len(manifest.Extensions()) == 0 {
		t.Skipf("выгрузка %s не объявляет расширений — проверять связь «перехватчик → цель» не на чем", root)
	}
	builtins := syntaxtest.RealOrSkip(t)

	workspaceRoot := t.TempDir()
	st, err := store.Open(workspaceRoot, store.Options{ProjectID: manifest.Project, StateDirName: workspace.RegistryDirName})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc := index.NewService(st, manifest.Project, manifest, builtins, index.Config{})
	t.Cleanup(func() { svc.Close() })

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	// Root реестровой записи — временный каталог: opened прогрет ниже, до
	// LoadManifest по нему дело не дойдёт, а в саму выгрузку писать нельзя.
	entry := workspace.ProjectEntry{ID: manifest.Project, Root: workspaceRoot}
	if err := reg.Upsert(entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject(manifest.Project); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	p := &Projects{workspaceRoot: workspaceRoot, registry: reg, builtins: builtins, opened: map[domain.ProjectID]*openProject{}}
	p.opened[manifest.Project] = &openProject{Entry: entry, Manifest: manifest, Store: st, Service: svc}

	t0 := time.Now()
	res, err := svc.Reindex(context.Background(), index.ModeFull, "")
	if err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}
	t.Logf("полная индексация %s: %s, файлов изменено %d", root, time.Since(t0), sumFilesChanged(res))
	return p, manifest
}

// interceptPair — модуль базового слоя, перехваченный метод, вид перехвата,
// слой расширения и имя самого перехватчика. Имена цели и перехватчика
// РАЗНЫЕ — на таких парах старое правило «цель = имя метода» не давало ни
// одного факта. Пары задаются окружением под конкретную выгрузку:
//
//	ONEC_POSTING_INTERCEPTS="Документ:Вид:слой:ИмяПерехватчика;..."
//
// Цель у всех пар — ОбработкаПроведения документа; у каждого документа
// обязан быть модуль объекта в базовом слое.
type interceptPair struct {
	module      string
	target      string
	kind        string
	layer       domain.ComponentID
	interceptor string
}

// interceptPairsFromEnv разбирает ONEC_POSTING_INTERCEPTS. Пустая переменная
// — пропуск, битая запись — красный.
func interceptPairsFromEnv(t *testing.T) []interceptPair {
	t.Helper()
	const env = "ONEC_POSTING_INTERCEPTS"
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		t.Skipf("%s не задан — пары «перехватчик → цель» для этой выгрузки не названы", env)
	}
	var out []interceptPair
	for _, item := range strings.Split(raw, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.Split(item, ":")
		if len(parts) != 4 {
			t.Fatalf("%s: запись %q, want Документ:Вид:слой:ИмяПерехватчика", env, item)
		}
		out = append(out, interceptPair{
			module:      workspace.DumpModulePath("Document", strings.TrimSpace(parts[0]), workspace.ModuleObject),
			target:      "ОбработкаПроведения",
			kind:        strings.TrimSpace(parts[1]),
			layer:       domain.ComponentID(strings.TrimSpace(parts[2])),
			interceptor: domain.NormalizeName(strings.TrimSpace(parts[3])),
		})
	}
	if len(out) == 0 {
		t.Skipf("%s не содержит ни одной записи", env)
	}
	return out
}

// baseComponentOf — базовый слой, к которому применяются расширения проекта.
func baseComponentOf(m workspace.Manifest) domain.ComponentID {
	return m.Extensions()[0].AppliesTo
}

// manifestHasLayers — выгрузка опознаётся по объявленным слоям расширений, а
// НЕ по тем данным, которые тест собирается проверять: иначе пропажа самих
// проверяемых модулей выглядела бы как «другая выгрузка» и уводила прогон в
// зелёный SKIP.
func manifestHasLayers(m workspace.Manifest, ids ...domain.ComponentID) bool {
	have := map[domain.ComponentID]bool{}
	for _, c := range m.Extensions() {
		have[c.ID] = true
	}
	for _, id := range ids {
		if !have[id] {
			return false
		}
	}
	return true
}

// baseModuleIndexed — несёт ли БАЗОВЫЙ слой файл модуля по этому пути. Факт
// берётся из индекса (source_file), а не с диска, и проверяется отдельно от
// вызова инструмента, чтобы ошибка get_symbol никогда не могла быть принята
// за отсутствие модуля.
func baseModuleIndexed(t *testing.T, p *Projects, project domain.ProjectID, base domain.ComponentID, modulePath string) bool {
	t.Helper()
	op := p.opened[project]
	found, err := ReadTx(context.Background(), op.Store, func(tx *store.ReadTx) (bool, error) {
		_, ok, err := tx.SourceFileID(string(base), modulePath)
		return ok, err
	})
	if err != nil {
		t.Fatalf("source_file(%s, %s): %v", base, modulePath, err)
	}
	return found
}

// TestRealDumpEffectiveInterceptsPairs — регрессия на реальных данных:
// get_symbol view=effective на парах из окружения отдаёт перехватчик
// расширения — вид, слой и имя, — а get_module_structure view=effective
// показывает его же у метода модуля. Ожидаемые значения берутся из
// исходников выгрузки, а не из того, что вернул код.
func TestRealDumpEffectiveInterceptsPairs(t *testing.T) {
	pairs := interceptPairsFromEnv(t)
	p, manifest := realDumpExtensionProject(t)
	base := baseComponentOf(manifest)
	for _, c := range pairs {
		if !manifestHasLayers(manifest, c.layer) {
			t.Fatalf("выгрузка %s не объявляет слой %s из окружения", manifest.Root, c.layer)
		}
	}
	symSvc := NewSymbolService(p)
	ctx := context.Background()

	for _, c := range pairs {
		t.Run(c.module, func(t *testing.T) {
			// Отсутствие базового модуля — красный: пара из окружения
			// обещает, что он есть. Ошибка инструмента тоже красная:
			// регрессия «символ перестал находиться» приходит именно ею.
			if !baseModuleIndexed(t, p, manifest.Project, base, c.module) {
				t.Fatalf("в базовом слое %s нет модуля %s", base, c.module)
			}
			resp, err := symSvc.GetSymbol(ctx, GetSymbolInput{
				Module: c.module, Name: c.target, Component: string(base), View: "effective",
			})
			if err != nil {
				t.Fatalf("get_symbol(view=effective) на %s %s: %v", c.module, c.target, err)
			}
			if len(resp.Items) != 1 {
				t.Fatalf("get_symbol вернул %d элементов, want 1", len(resp.Items))
			}
			assertHasIntercept(t, "get_symbol", resp.Items[0].Intercepts, c)

			ms, err := symSvc.GetModuleStructure(ctx, GetModuleStructureInput{
				Module: c.module, Component: string(base), View: "effective",
			})
			if err != nil {
				t.Fatalf("GetModuleStructure(effective): %v", err)
			}
			found := false
			for _, item := range ms.Items {
				for _, sym := range item.Symbols {
					if !strings.EqualFold(sym.Name, c.target) {
						continue
					}
					found = true
					assertHasIntercept(t, "get_module_structure", sym.Intercepts, c)
				}
			}
			if !found {
				t.Fatalf("get_module_structure не показал метод %s в модуле %s", c.target, c.module)
			}
		})
	}
}

// assertHasIntercept — искомый перехватчик обязан быть в списке: имя
// перехватчика, вид и слой сверяются с парой из окружения.
func assertHasIntercept(t *testing.T, tool string, ics []InterceptItem, c interceptPair) {
	t.Helper()
	for _, ic := range ics {
		if ic.InterceptorName != c.interceptor {
			continue
		}
		if ic.Kind != c.kind {
			t.Errorf("%s: kind = %q, want %q", tool, ic.Kind, c.kind)
		}
		if ic.Layer != c.layer {
			t.Errorf("%s: layer = %q, want %q", tool, ic.Layer, c.layer)
		}
		if !strings.EqualFold(ic.TargetName, c.target) {
			t.Errorf("%s: targetName = %q, want %q", tool, ic.TargetName, c.target)
		}
		if ic.InterceptorName == ic.TargetName {
			t.Errorf("%s: цель и перехватчик совпали — связь снова строится по имени", tool)
		}
		return
	}
	t.Fatalf("%s: перехватчик %s не найден среди %+v", tool, c.interceptor, ics)
}
