package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// TestDeriveHandlerBindingResolved — обработчик формы, объявленный в
// FormStructureFact, находит свой метод в модуле формы.
func TestDeriveHandlerBindingResolved(t *testing.T) {
	formModule := "Catalogs/Товары/Forms/ФормаЭлемента/Ext/Form/Module.bsl"
	handler := proc(formModule, "ПриОткрытии", true)
	env := mustEnv(t, EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			{ModulePath: formModule, Kind: bsl.ModuleForm, Symbols: []domain.Symbol{handler}},
		},
	}, nil)

	facts := []meta.HandlerBindingFact{
		{Source: "", Event: "ПриОткрытии", HandlerNameNorm: domain.NormalizeName("ПриОткрытии"), HandlerDisplay: "ПриОткрытии"},
	}
	results := DeriveHandlerBinding(formModule, facts, env)
	if len(results) != 1 {
		t.Fatalf("results = %d, ожидался 1", len(results))
	}
	if results[0].Resolution != domain.ResolutionResolved || results[0].HandlerUID != handler.UID {
		t.Fatalf("resolution=%s uid=%s, ожидался resolved/%s", results[0].Resolution, results[0].HandlerUID, handler.UID)
	}
}

// TestDeriveHandlerBindingUnresolved: критерий приёмки: обработчик
// объявлен, но метода в модуле формы нет — unresolved, а не пустая выдача.
func TestDeriveHandlerBindingUnresolved(t *testing.T) {
	formModule := "Catalogs/Товары/Forms/ФормаЭлемента/Ext/Form/Module.bsl"
	env := mustEnv(t, EnvInput{
		Component: testComponent,
		Modules: []ModuleEntry{
			{ModulePath: formModule, Kind: bsl.ModuleForm},
		},
	}, nil)

	facts := []meta.HandlerBindingFact{
		{Source: "ТаблицаТовары", Event: "ПриАктивизацииСтроки", HandlerNameNorm: domain.NormalizeName("ПропавшийОбработчик"), HandlerDisplay: "ПропавшийОбработчик"},
	}
	results := DeriveHandlerBinding(formModule, facts, env)
	if len(results) != 1 {
		t.Fatalf("results = %d, ожидался 1", len(results))
	}
	if results[0].Resolution != domain.ResolutionUnresolved {
		t.Fatalf("resolution = %s, ожидалось unresolved (обработчик не найден)", results[0].Resolution)
	}
	if results[0].HandlerUID != "" {
		t.Errorf("HandlerUID = %q, у unresolved цели быть не должно", results[0].HandlerUID)
	}
}
