package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// TestShadowingOrderCorpus — corpus-тест гипотезы §19.1: порядок затенения
// местный модуль -> глобальные общие модули -> platform builtins, доказанный
// одноимённым символом на ВСЕХ ТРЁХ уровнях одновременно, а не просто
// заявленный. "СтрНайти" выбран платформенным именем специально: если бы
// порядка не было и резолвер бил в platform раньше локали/глобали, тест бы
// поймал это немедленно.
func TestShadowingOrderCorpus(t *testing.T) {
	const name = "СтрНайти" // существует в internal/syntax как platform builtin
	nameNorm := domain.NormalizeName(name)

	local := proc("CommonModules/Caller/Ext/Module.bsl", name, false)
	global := proc("CommonModules/Global/Ext/Module.bsl", name, true)
	builtins := loadBuiltins(t)
	if _, ok := builtins.GlobalMethod(name); !ok {
		t.Fatalf("фикстура теста сломана: %q не найден как platform builtin", name)
	}

	baseInput := func() EnvInput {
		return EnvInput{
			Component: testComponent,
			Modules: []ModuleEntry{
				{ModulePath: "CommonModules/Caller/Ext/Module.bsl", Kind: bsl.ModuleCommon,
					NameNorm: "caller", Symbols: []domain.Symbol{local}},
				commonModule("CommonModules/Global/Ext/Module.bsl", "Global",
					meta.ModuleRegistryFact{Global: true, Server: true}, global),
			},
		}
	}
	raw := RawRef{Form: FormUnqualified, NameNorm: nameNorm, NameDisplay: name,
		CallerModulePath: "CommonModules/Caller/Ext/Module.bsl", CallerModuleKind: bsl.ModuleCommon}

	// Уровень 1: все три источника присутствуют — обязан выиграть местный.
	env := mustEnv(t, baseInput(), builtins)
	res := Resolve(raw, env)
	if res.Resolution != domain.ResolutionResolved || res.TargetUID != local.UID {
		t.Fatalf("при всех трёх уровнях: resolution=%s target=%s, ожидался местный %s",
			res.Resolution, res.TargetUID, local.UID)
	}
	if res.CallKind != CallLocal {
		t.Errorf("callKind = %s, ожидалось local", res.CallKind)
	}

	// Уровень 2: местного больше нет — обязан выиграть глобальный общий.
	input2 := baseInput()
	input2.Modules[0].Symbols = nil
	env2 := mustEnv(t, input2, builtins)
	res2 := Resolve(raw, env2)
	if res2.Resolution != domain.ResolutionResolved || res2.TargetUID != global.UID {
		t.Fatalf("без местного: resolution=%s target=%s, ожидался глобальный %s",
			res2.Resolution, res2.TargetUID, global.UID)
	}
	if res2.CallKind != CallGlobalCommon {
		t.Errorf("callKind = %s, ожидалось global-common", res2.CallKind)
	}

	// Уровень 3: ни местного, ни глобального — обязан выиграть platform.
	input3 := baseInput()
	input3.Modules[0].Symbols = nil
	input3.Modules[1].Symbols = nil
	env3 := mustEnv(t, input3, builtins)
	res3 := Resolve(raw, env3)
	if res3.Resolution != domain.ResolutionResolved || res3.TargetClass != domain.TargetPlatform {
		t.Fatalf("без местного и глобального: resolution=%s targetClass=%s, ожидался platform",
			res3.Resolution, res3.TargetClass)
	}
	if res3.CallKind != CallPlatform {
		t.Errorf("callKind = %s, ожидалось platform", res3.CallKind)
	}
}

// TestNewТипБезСкобокКорпус — corpus-гипотеза (а), НЕ смешивать с (б):
// «Новый Тип» без скобок — подтверждённо валидная синтаксическая форма,
// парсер обязан выдать ссылку RefNew. Резолвер вызовов (Resolve) её не
// обрабатывает: это ссылка на тип, а не на метод (см. BuildRawRefs) — тест
// фиксирует именно это разделение, а не пытается её "разрешить".
func TestNewТипБезСкобокКорпус(t *testing.T) {
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tМассив = Новый Массив;\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "CommonModules/X/Ext/Module.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", diags)
	}
	var found bool
	for _, r := range mod.References {
		if r.Kind == bsl.RefNew && mod.Name(r.Span) == "Массив" {
			found = true
		}
	}
	if !found {
		t.Fatalf("«Новый Тип» без скобок не дал RefNew среди %+v", mod.References)
	}
	raws := BuildRawRefs("CommonModules/X/Ext/Module.bsl", mod)
	if len(raws) != 0 {
		t.Errorf("BuildRawRefs не должен превращать RefNew в RawRef вызова: получено %d", len(raws))
	}
}

// TestГолыйВызовБезСкобокКорпус — corpus-гипотеза (б), отдельно от (а):
// голый вызов процедуры без скобок ("Обработать;") парсер НЕ собирает как
// ссылку — collectRef реагирует только на "имя(" (или "Новый Имя" без
// скобок). Это фиксирует текущее поведение как invalid-кейс error recovery,
// а не как обнаруженный, но потерянный резолвером вызов.
func TestГолыйВызовБезСкобокКорпус(t *testing.T) {
	src := []byte("Процедура Тест() Экспорт\n" +
		"\tОбработать;\n" +
		"КонецПроцедуры\n")
	mod, diags := bsl.Parse(src, bsl.Options{File: "CommonModules/X/Ext/Module.bsl"})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", diags)
	}
	for _, r := range mod.References {
		if mod.Name(r.Span) == "Обработать" {
			t.Fatalf("голый вызов без скобок неожиданно дал ссылку %+v — гипотеза (б) подтвердилась, "+
				"resolve должен начать её обрабатывать, а не просто фиксировать отсутствие", r)
		}
	}
}
