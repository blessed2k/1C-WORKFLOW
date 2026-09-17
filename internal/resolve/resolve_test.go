package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestResolveUnqualifiedLocalModule — §19.1 п.3(а): вызов без квалификатора
// находит символ в своём же модуле раньше всего остального.
func TestResolveUnqualifiedLocalModule(t *testing.T) {
	callee := proc("CommonModules/Utils/Ext/Module.bsl", "Обработать", true)
	input := EnvInput{
		Component: testComponent,
		Layer:     domain.BaseLayer(testComponent),
		Modules: []ModuleEntry{
			{ModulePath: "CommonModules/Utils/Ext/Module.bsl", Kind: bsl.ModuleCommon,
				NameNorm: "utils", Registry: &meta.ModuleRegistryFact{Server: true},
				Symbols: []domain.Symbol{callee}},
		},
	}
	env := mustEnv(t, input, nil)

	raw := RawRef{Form: FormUnqualified, NameNorm: "обработать", NameDisplay: "Обработать",
		CallerModulePath: "CommonModules/Utils/Ext/Module.bsl", CallerModuleKind: bsl.ModuleCommon}
	res := Resolve(raw, env)

	if res.Resolution != domain.ResolutionResolved {
		t.Fatalf("resolution = %s, ожидалось resolved", res.Resolution)
	}
	if res.TargetClass != domain.TargetSymbol || res.TargetUID != callee.UID {
		t.Fatalf("target = %s/%s, ожидался symbol/%s", res.TargetClass, res.TargetUID, callee.UID)
	}
	if res.CallKind != CallLocal {
		t.Errorf("callKind = %s, ожидалось local", res.CallKind)
	}
	if len(res.ConsultedKeys) != 1 {
		t.Errorf("consulted keys = %d, ожидался 1 (местный найден — дальше не ходим)", len(res.ConsultedKeys))
	}
}

// TestResolveQualifiedCommonModuleNotExported — критерий приёмки: попадание
// в неэкспортный символ общего модуля даёт binding + not-exported, а не
// потерю ссылки (resolved, а не unresolved).
func TestResolveQualifiedCommonModuleNotExported(t *testing.T) {
	callee := proc("CommonModules/Utils/Ext/Module.bsl", "Внутренний", false)
	input := EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			commonModule("CommonModules/Utils/Ext/Module.bsl", "Utils",
				meta.ModuleRegistryFact{Server: true, ClientManagedApplication: true}, callee),
		},
	}
	env := mustEnv(t, input, nil)

	raw := RawRef{Form: FormQualifiedModule, NameNorm: "внутренний", NameDisplay: "Внутренний",
		Qualifier: "Utils", QualifierNorm: "utils", CallerModulePath: "CommonModules/Other/Ext/Module.bsl"}
	res := Resolve(raw, env)

	if res.Resolution != domain.ResolutionResolved {
		t.Fatalf("resolution = %s, ожидалось resolved (находка, а не потеря)", res.Resolution)
	}
	if res.TargetClass != domain.TargetSymbol || res.TargetUID != callee.UID {
		t.Fatalf("target = %s/%s, ожидался symbol/%s", res.TargetClass, res.TargetUID, callee.UID)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != DiagNotExported {
		t.Fatalf("diagnostics = %+v, ожидался ровно один not-exported", res.Diagnostics)
	}
}

// TestResolveContextMismatch — критерий приёмки: клиентский вызов
// Server-only общего модуля без ServerCall даёт context-mismatch, ребро
// (resolved) не блокируется.
func TestResolveContextMismatch(t *testing.T) {
	callee := proc("CommonModules/ServerUtils/Ext/Module.bsl", "Обработать", true)
	input := EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			commonModule("CommonModules/ServerUtils/Ext/Module.bsl", "ServerUtils",
				meta.ModuleRegistryFact{Server: true}, callee), // ни клиентских флагов, ни ServerCall
		},
	}
	env := mustEnv(t, input, nil)

	raw := RawRef{Form: FormQualifiedModule, NameNorm: "обработать", NameDisplay: "Обработать",
		Qualifier: "ServerUtils", QualifierNorm: "serverutils",
		CallerModulePath: "CommonModules/ClientUtils/Ext/Module.bsl", CallerDirective: "&НаКлиенте"}
	res := Resolve(raw, env)

	if res.Resolution != domain.ResolutionResolved {
		t.Fatalf("resolution = %s, ожидалось resolved (ребро не блокируется)", res.Resolution)
	}
	if res.TargetUID != callee.UID {
		t.Fatalf("target uid = %s, ожидался %s", res.TargetUID, callee.UID)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != DiagContextMismatch {
		t.Fatalf("diagnostics = %+v, ожидался ровно один context-mismatch", res.Diagnostics)
	}
	if !res.Confidence.Valid() || res.Confidence >= domain.ConfidenceExact {
		t.Errorf("confidence = %v, ожидалось < 1 (статическое приближение)", res.Confidence)
	}
}

// TestResolvePlatformBuiltin — критерий приёмки: platform-вызов имеет
// target_class=platform и непустой platform_key, resolved (не unresolved).
func TestResolvePlatformBuiltin(t *testing.T) {
	builtins := loadBuiltins(t)
	env := mustEnv(t, EnvInput{Component: testComponent}, builtins)

	raw := RawRef{Form: FormUnqualified, NameNorm: domain.NormalizeName("СтрНайти"), NameDisplay: "СтрНайти",
		CallerModulePath: "CommonModules/X/Ext/Module.bsl"}
	res := Resolve(raw, env)

	if res.Resolution != domain.ResolutionResolved {
		t.Fatalf("resolution = %s, ожидалось resolved", res.Resolution)
	}
	if res.TargetClass != domain.TargetPlatform {
		t.Fatalf("targetClass = %s, ожидался platform", res.TargetClass)
	}
	if res.TargetKey == "" {
		t.Error("platform_key пуст")
	}
	if res.CallKind != CallPlatform {
		t.Errorf("callKind = %s, ожидалось platform", res.CallKind)
	}
}

// TestResolveAmbiguousGlobalCommon — критерий приёмки: ambiguous даёт
// кандидатов с rank и причиной, и это состояние, а не resolved/unresolved.
func TestResolveAmbiguousGlobalCommon(t *testing.T) {
	a := proc("CommonModules/A/Ext/Module.bsl", "Обработать", true)
	b := proc("CommonModules/B/Ext/Module.bsl", "Обработать", true)
	input := EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			commonModule("CommonModules/A/Ext/Module.bsl", "A", meta.ModuleRegistryFact{Global: true, Server: true}, a),
			commonModule("CommonModules/B/Ext/Module.bsl", "B", meta.ModuleRegistryFact{Global: true, Server: true}, b),
			{ModulePath: "CommonModules/Caller/Ext/Module.bsl", Kind: bsl.ModuleCommon, NameNorm: "caller"},
		},
	}
	env := mustEnv(t, input, nil)

	raw := RawRef{Form: FormUnqualified, NameNorm: "обработать", NameDisplay: "Обработать",
		CallerModulePath: "CommonModules/Caller/Ext/Module.bsl", CallerModuleKind: bsl.ModuleCommon}
	res := Resolve(raw, env)

	if res.Resolution != domain.ResolutionAmbiguous {
		t.Fatalf("resolution = %s, ожидалось ambiguous", res.Resolution)
	}
	if res.TargetClass != "" {
		t.Errorf("targetClass = %s, у ambiguous цели быть не должно (XOR §14)", res.TargetClass)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %d, ожидалось 2", len(res.Candidates))
	}
	seen := map[domain.SymbolUID]bool{}
	for i, c := range res.Candidates {
		if c.Rank != i+1 {
			t.Errorf("candidate[%d].Rank = %d, ожидался %d", i, c.Rank, i+1)
		}
		if c.Reason == "" {
			t.Errorf("candidate[%d] без причины", i)
		}
		seen[c.TargetUID] = true
	}
	if !seen[a.UID] || !seen[b.UID] {
		t.Errorf("кандидаты не покрывают оба символа: %+v", res.Candidates)
	}
}

// TestResolveQualifiedManager — §19.1 п.2 через реальный парсер: Справочники.
// Товары.НайтиПоКоду( должен разрешиться в экспортный метод менеджерного
// модуля справочника Товары, а не спутаться с обычным «Модуль.Метод(».
func TestResolveQualifiedManager(t *testing.T) {
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tСсылка = Справочники.Товары.НайтиПоКоду(\"000001\");\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "CommonModules/X/Ext/Module.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик парсера быть не должно: %v", diags)
	}
	raws := BuildRawRefs("CommonModules/X/Ext/Module.bsl", mod)
	var managerRaw *RawRef
	for i := range raws {
		if raws[i].Form == FormQualifiedManager {
			managerRaw = &raws[i]
		}
	}
	if managerRaw == nil {
		t.Fatalf("не нашли FormQualifiedManager среди %+v", raws)
	}
	if managerRaw.ManagerMType != "Catalog" || managerRaw.ManagerObjectNameNorm != "товары" {
		t.Fatalf("manager ref = %+v, ожидался Catalog/товары", managerRaw)
	}

	callee := sym("Catalogs/Товары/Ext/ManagerModule.bsl", "НайтиПоКоду", domain.SymbolFunction, true)
	input := EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			{ModulePath: "Catalogs/Товары/Ext/ManagerModule.bsl", Kind: bsl.ModuleManager,
				OwnerMType: "Catalog", OwnerNameNorm: "товары", Symbols: []domain.Symbol{callee}},
		},
	}
	env := mustEnv(t, input, nil)

	res := Resolve(*managerRaw, env)
	if res.Resolution != domain.ResolutionResolved {
		t.Fatalf("resolution = %s, ожидалось resolved", res.Resolution)
	}
	if res.TargetUID != callee.UID {
		t.Fatalf("target uid = %s, ожидался %s", res.TargetUID, callee.UID)
	}
	if res.CallKind != CallManager {
		t.Errorf("callKind = %s, ожидалось manager", res.CallKind)
	}
}

// TestResolveFilterCriterionManager: КритерииОтбора.X.Метод( разрешается в
// экспортный метод менеджерного модуля критерия отбора. До сведения словаря
// видов в domain.MetaKinds (C2) резолвер этого вида не знал: ManagerMType
// оставался пустым, и вызов не разрешался.
func TestResolveFilterCriterionManager(t *testing.T) {
	const caller = "CommonModules/X/Ext/Module.bsl"
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tСписок = КритерииОтбора.СвязанныеДокументы.Найти(Ссылка);\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: caller})
	if len(diags) != 0 {
		t.Fatalf("диагностик парсера быть не должно: %v", diags)
	}
	var managerRaw *RawRef
	raws := BuildRawRefs(caller, mod)
	for i := range raws {
		if raws[i].Form == FormQualifiedManager {
			managerRaw = &raws[i]
		}
	}
	if managerRaw == nil {
		t.Fatalf("не нашли FormQualifiedManager среди %+v", raws)
	}
	if managerRaw.ManagerMType != "FilterCriterion" || managerRaw.ManagerObjectNameNorm != "связанныедокументы" {
		t.Fatalf("manager ref = %+v, ожидался FilterCriterion/связанныедокументы", managerRaw)
	}

	modulePath := workspace.DumpModulePath("FilterCriterion", "СвязанныеДокументы", workspace.ModuleManager)
	callee := sym(modulePath, "Найти", domain.SymbolFunction, true)
	env := mustEnv(t, EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			{ModulePath: modulePath, Kind: bsl.ModuleManager,
				OwnerMType: "FilterCriterion", OwnerNameNorm: "связанныедокументы", Symbols: []domain.Symbol{callee}},
		},
	}, nil)

	res := Resolve(*managerRaw, env)
	if res.Resolution != domain.ResolutionResolved || res.TargetUID != callee.UID {
		t.Fatalf("resolution = %s, target = %s; ожидалось resolved в %s", res.Resolution, res.TargetUID, callee.UID)
	}
	if res.CallKind != CallManager {
		t.Errorf("callKind = %s, ожидалось manager", res.CallKind)
	}
}

// TestResolveDynamicViaUnknownQualifier — §19.1 п.5: квалификатор, не
// опознанный как общий модуль компонента, — вызов через переменную/объект,
// dynamic с confidence < 1, а не unresolved.
func TestResolveDynamicViaUnknownQualifier(t *testing.T) {
	env := mustEnv(t, EnvInput{Component: testComponent}, nil)
	raw := RawRef{Form: FormQualifiedModule, NameNorm: "количество", NameDisplay: "Количество",
		Qualifier: "Результат", QualifierNorm: "результат", CallerModulePath: "CommonModules/X/Ext/Module.bsl"}
	res := Resolve(raw, env)
	if res.Resolution != domain.ResolutionDynamic {
		t.Fatalf("resolution = %s, ожидалось dynamic", res.Resolution)
	}
	if res.Confidence >= domain.ConfidenceExact {
		t.Errorf("confidence = %v, ожидалось < 1", res.Confidence)
	}
}

// TestResolveUnresolved — имя не найдено нигде: местный модуль пуст,
// глобальных общих модулей нет, platform builtins не подключены (nil).
func TestResolveUnresolved(t *testing.T) {
	env := mustEnv(t, EnvInput{Component: testComponent}, nil)
	raw := RawRef{Form: FormUnqualified, NameNorm: "нетнигде", NameDisplay: "НетНигде",
		CallerModulePath: "CommonModules/X/Ext/Module.bsl"}
	res := Resolve(raw, env)
	if res.Resolution != domain.ResolutionUnresolved {
		t.Fatalf("resolution = %s, ожидалось unresolved", res.Resolution)
	}
	if res.TargetClass != "" {
		t.Errorf("targetClass = %s, у unresolved цели быть не должно", res.TargetClass)
	}
}
