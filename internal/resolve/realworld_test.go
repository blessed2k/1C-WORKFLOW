package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// dumpEnvVars — те же переменные, что и в internal/parse/{bsl,meta,query}
// (см. их corpus_test.go): один и тот же прогон, один и тот же путь к
// выгрузке, не зашитый в код.
var dumpEnvVars = []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"}

func dumpRoot(t *testing.T) string {
	t.Helper()
	for _, env := range dumpEnvVars {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if _, err := os.Stat(v); err != nil {
				t.Skipf("%s указывает на недоступный путь: %v", env, err)
			}
			return v
		}
	}
	t.Skip("переменная окружения ONEC_DUMP не задана — прогон резолвера на реальной выгрузке пропущен")
	return ""
}

// TestResolveКорпусРеальнойВыгрузки строит Env по ВСЕЙ реальной выгрузке
// (ONEC_DUMP): все объекты метаданных, все общие модули с их XML-свойствами,
// все BSL-модули с их символами, — и прогоняет Resolve по каждому вызову.
// Критерий приёмки: доли resolved/ambiguous/unresolved/dynamic ЗАПИСАНЫ (не
// только залогированы: см. пороги ниже) на не-фиктивных данных — метрика,
// структурно неспособная быть плохой, не измерение (регламент прогона).
func TestResolveКорпусРеальнойВыгрузки(t *testing.T) {
	root := dumpRoot(t)

	// Проход 1: метаданные — объекты, их члены и свойства общих модулей.
	var objects []ObjectRef
	var members []MemberRef
	registryByCommonModule := map[string]*meta.ModuleRegistryFact{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if meta.Classify(rel) != meta.KindMetadataObject {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		facts, _ := meta.ParseFile(rel, src)
		if facts.Object == nil {
			return nil
		}
		objects = append(objects, ObjectRef{
			MType: facts.Object.MType, NameNorm: facts.Object.NameNorm,
			IdentityKey: "metadata:" + facts.Object.MType + ":" + facts.Object.NameNorm,
		})
		if facts.Object.MType == "CommonModule" && facts.ModuleRegistry != nil {
			registryByCommonModule[facts.Object.NameNorm] = facts.ModuleRegistry
		}
		for _, mf := range facts.Members {
			members = append(members, MemberRef{
				ObjectMType: facts.Object.MType, ObjectNameNorm: facts.Object.NameNorm,
				Kind: mf.Kind, NameNorm: mf.NameNorm, Types: mf.Types,
				IdentityKey: "member:" + facts.Object.MType + ":" + facts.Object.NameNorm + ":" + mf.NameNorm,
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход метаданных: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("в выгрузке не нашлось ни одного объекта метаданных")
	}
	if len(members) == 0 {
		t.Fatal("в выгрузке не нашлось ни одного члена объекта метаданных")
	}

	// Проход 2: BSL-модули — символы и факты вызовов.
	type parsedModule struct {
		path string
		mod  *bsl.Module
	}
	var modules []parsedModule
	var moduleEntries []ModuleEntry
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".bsl") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		mod, _ := bsl.Parse(src, bsl.Options{File: rel})
		modules = append(modules, parsedModule{path: rel, mod: mod})

		info := mod.Info
		entry := ModuleEntry{
			ModulePath:    domain.NormalizeModulePath(rel),
			Kind:          info.Kind,
			OwnerNameNorm: info.OwnerNameNorm,
		}
		if info.Kind == bsl.ModuleCommon {
			entry.NameNorm = info.OwnerNameNorm
			entry.Registry = registryByCommonModule[info.OwnerNameNorm]
		}
		if mtype, ok := bslCollectionToMType(info.OwnerType); ok {
			entry.OwnerMType = mtype
		}
		for _, m := range mod.Methods {
			entry.Symbols = append(entry.Symbols, domain.Symbol{
				UID:         domain.NewSymbolUID("corpus", testComponent, rel, m.NameNorm),
				Component:   testComponent,
				ModulePath:  rel,
				Kind:        m.Kind,
				NameNorm:    m.NameNorm,
				NameDisplay: m.Name,
				Export:      m.Export,
				Directive:   m.Directive,
			})
		}
		moduleEntries = append(moduleEntries, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("обход BSL: %v", err)
	}
	if len(modules) == 0 {
		t.Fatal("в выгрузке не нашлось ни одного .bsl")
	}

	builtins := syntaxtest.RealOrSkip(t)
	env := mustEnv(t, EnvInput{Component: testComponent, Modules: moduleEntries, Objects: objects, Members: members}, builtins)

	buckets := map[domain.Resolution]int64{}
	var total int64
	var queryTables, queryTablesResolved, queryFields, queryFieldsResolved int64
	for _, pm := range modules {
		for _, raw := range BuildRawRefs(pm.path, pm.mod) {
			res := Resolve(raw, env)
			if !res.Resolution.Valid() {
				t.Fatalf("%s: невалидный Resolution %q для %+v", pm.path, res.Resolution, raw)
			}
			buckets[res.Resolution]++
			total++
		}
		for _, g := range DeriveQueryReference(pm.mod, env) {
			for _, qr := range g.References {
				switch qr.Kind {
				case QueryRefTable:
					queryTables++
					if qr.ObjectResolved {
						queryTablesResolved++
					}
				case QueryRefField:
					queryFields++
					if qr.MemberResolved {
						queryFieldsResolved++
					}
				}
			}
		}
	}

	if total == 0 {
		t.Fatal("ни одного вызова не разобрано — прогон на реальной выгрузке бессмысленен")
	}
	sum := buckets[domain.ResolutionResolved] + buckets[domain.ResolutionAmbiguous] +
		buckets[domain.ResolutionUnresolved] + buckets[domain.ResolutionDynamic]
	if sum != total {
		t.Fatalf("сумма корзин %d != total %d — резолвер вернул нечто вне четырёх состояний", sum, total)
	}

	pct := func(n int64) float64 { return 100 * float64(n) / float64(total) }
	t.Logf("резолвер на %s: файлов=%d, объектов метаданных=%d, вызовов=%d, "+
		"resolved=%d (%.1f%%) ambiguous=%d (%.1f%%) unresolved=%d (%.1f%%) dynamic=%d (%.1f%%)",
		root, len(modules), len(objects), total,
		buckets[domain.ResolutionResolved], pct(buckets[domain.ResolutionResolved]),
		buckets[domain.ResolutionAmbiguous], pct(buckets[domain.ResolutionAmbiguous]),
		buckets[domain.ResolutionUnresolved], pct(buckets[domain.ResolutionUnresolved]),
		buckets[domain.ResolutionDynamic], pct(buckets[domain.ResolutionDynamic]))

	// Пороги — не самоподтверждающиеся: посчитаны с запасом от разового
	// прогона на УТ (ADR-3 §4.2), а не выведены из формулы резолвера.
	// resolved обязан быть заметной долей (типовой код в основном зовёт
	// свои же и глобальные модули), ambiguous — редким (коллизии имён между
	// глобальными общими модулями не типичны для одной конфигурации).
	if got := pct(buckets[domain.ResolutionResolved]); got < 30 {
		t.Errorf("resolved = %.1f%%, ожидался хотя бы 30%% на реальном коде", got)
	}
	if got := pct(buckets[domain.ResolutionAmbiguous]); got > 5 {
		t.Errorf("ambiguous = %.1f%%, подозрительно много для одной конфигурации (ожидалось <= 5%%)", got)
	}

	if queryTables == 0 {
		t.Fatal("ни одной статичной таблицы запроса не разобрано — прогон query_reference на реальной выгрузке бессмысленен")
	}
	tablePct := 100 * float64(queryTablesResolved) / float64(queryTables)
	fieldPct := 0.0
	if queryFields > 0 {
		fieldPct = 100 * float64(queryFieldsResolved) / float64(queryFields)
	}
	t.Logf("query_reference на %s: статичных таблиц=%d resolved=%.1f%%, полей=%d resolved=%.1f%%",
		root, queryTables, tablePct, queryFields, fieldPct)
	// Порог с запасом от разового прогона, не выведен из кода под тестом:
	// подавляющее большинство таблиц в статичных запросах типовой
	// конфигурации — обычные объекты (Справочник/Документ/Регистр), которые
	// объекты метаданных этой же выгрузки обязаны находить.
	if tablePct < 50 {
		t.Errorf("resolved таблиц запроса = %.1f%%, ожидалось хотя бы 50%%", tablePct)
	}
}
