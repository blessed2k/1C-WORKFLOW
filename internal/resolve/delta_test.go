package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// TestResolutionDepNegativeLookupTriggersReResolve — критерий приёмки: символ
// появился -> unresolved стал resolved, потому что резолвер зарегистрировал
// НЕГАТИВНЫЙ lookup, и AffectedKeys(delta) для новосозданного символа даёт
// ровно тот же ключ, который эта негативная попытка консультировала. Тест
// эмулирует внешний incremental-контур (resolution_dep живёт в store,
// пайплайн index), но сама гарантия целиком в этом пакете: Resolve и AffectedKeys
// обязаны согласованно хэшировать один и тот же ключ.
func TestResolutionDepNegativeLookupTriggersReResolve(t *testing.T) {
	modulePath := "CommonModules/Utils/Ext/Module.bsl"
	raw := RawRef{Form: FormUnqualified, NameNorm: "новый", NameDisplay: "Новый",
		CallerModulePath: modulePath, CallerModuleKind: bsl.ModuleCommon}

	// 1. До появления символа: ссылка unresolved, но консультирует местный
	// ключ и регистрирует его (эмуляция resolution_dep.key_hash) даже без
	// находки.
	before := mustEnv(t, EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			{ModulePath: modulePath, Kind: bsl.ModuleCommon, NameNorm: "utils"},
		},
	}, nil)
	res1 := Resolve(raw, before)
	if res1.Resolution != domain.ResolutionUnresolved {
		t.Fatalf("resolution до появления символа = %s, ожидалось unresolved", res1.Resolution)
	}
	if len(res1.ConsultedKeys) == 0 {
		t.Fatal("негативный lookup обязан консультировать хотя бы один ключ (§18.4)")
	}
	resolutionDep := map[KeyHash]bool{}
	for _, k := range res1.ConsultedKeys {
		resolutionDep[k] = true
	}

	// 2. Символ добавлен в тот же модуль — считаем дельту.
	delta := NameDelta{
		Component:  testComponent,
		ModulePath: modulePath,
		ModuleKind: bsl.ModuleCommon,
		Names:      []string{"Новый"},
	}
	affected := AffectedKeys(delta)
	if len(affected) == 0 {
		t.Fatal("AffectedKeys вернул пустой набор для добавленного символа")
	}

	// 3. Проверяем: хотя бы один из affected-ключей есть среди
	// консультированных при негативном lookup — то есть ссылка ДОЛЖНА
	// переразрешиться.
	var hit bool
	for _, k := range affected {
		if resolutionDep[k] {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("AffectedKeys(%+v) = %v не пересекается с консультированными ключами %v — "+
			"переразрешение не будет запущено, unresolved останется unresolved", delta, affected, res1.ConsultedKeys)
	}

	// 4. Символ появился физически — переразрешение даёт resolved.
	callee := proc(modulePath, "Новый", true)
	after := mustEnv(t, EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			{ModulePath: modulePath, Kind: bsl.ModuleCommon, NameNorm: "utils", Symbols: []domain.Symbol{callee}},
		},
	}, nil)
	res2 := Resolve(raw, after)
	if res2.Resolution != domain.ResolutionResolved || res2.TargetUID != callee.UID {
		t.Fatalf("после появления символа: resolution=%s target=%s, ожидался resolved/%s",
			res2.Resolution, res2.TargetUID, callee.UID)
	}
}

// TestAffectedKeysModuleXMLChange — дельта XML общего модуля (Global и
// прочие свойства) обязана включать ключ ScopeGlobalCommon для КАЖДОГО
// экспортного имени модуля: без этого ссылки в НЕИЗМЕНЁННЫХ BSL-файлах,
// консультировавшие глобальное пространство, не переразрешатся при смене
// Global (§18.4).
func TestAffectedKeysModuleXMLChange(t *testing.T) {
	delta := NameDelta{
		Component:      testComponent,
		ModulePath:     "CommonModules/Utils/Ext/Module.bsl",
		ModuleKind:     bsl.ModuleCommon,
		ModuleNameNorm: "utils",
		Global:         true, // стал (или был) глобальным — граница затронута
		Names:          []string{"Обработать", "Отменить"},
	}
	keys := AffectedKeys(delta)

	wantGlobal := resolutionKey{Component: testComponent, Scope: ScopeGlobalCommon, Name: "обработать"}.Hash()
	wantExport := resolutionKey{Component: testComponent, Scope: ScopeModuleExport, Owner: "utils", Name: "отменить"}.Hash()

	has := func(k KeyHash) bool {
		for _, x := range keys {
			if x == k {
				return true
			}
		}
		return false
	}
	if !has(wantGlobal) {
		t.Errorf("нет ключа global-common для %q среди %v", "Обработать", keys)
	}
	if !has(wantExport) {
		t.Errorf("нет ключа module-export для %q среди %v", "Отменить", keys)
	}
}
