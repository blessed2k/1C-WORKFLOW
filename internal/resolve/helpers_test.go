package resolve

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

const testProject domain.ProjectID = "proj"
const testComponent domain.ComponentID = "cfg"

// sym строит domain.Symbol с корректным uid для теста — процедура/функция,
// экспортная по умолчанию.
func sym(modulePath, name string, kind domain.SymbolKind, export bool) domain.Symbol {
	nameNorm := domain.NormalizeName(name)
	return domain.Symbol{
		UID:         domain.NewSymbolUID(testProject, testComponent, modulePath, nameNorm),
		Project:     testProject,
		Component:   testComponent,
		ModulePath:  modulePath,
		Kind:        kind,
		NameNorm:    nameNorm,
		NameDisplay: name,
		Export:      export,
		Layer:       domain.BaseLayer(testComponent),
		Provenance:  domain.Provenance{Source: domain.SourceBSLParser, File: modulePath},
		Confidence:  domain.ConfidenceExact,
	}
}

func proc(modulePath, name string, export bool) domain.Symbol {
	return sym(modulePath, name, domain.SymbolProcedure, export)
}

// loadBuiltins загружает справочник платформы один раз на пакет тестов —
// это embedded-данные, а не реальная выгрузка, доступны всегда.
func loadBuiltins(t *testing.T) *syntax.Index {
	t.Helper()
	return syntaxtest.Fixture(t)
}

// mustEnv собирает Env и падает на ошибке — сокращение для тестов, которым
// сама ошибка NewEnv не интересна.
func mustEnv(t *testing.T, input EnvInput, builtins *syntax.Index) Env {
	t.Helper()
	env, err := NewEnv(input, builtins)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// commonModule строит ModuleEntry общего модуля с заданными флагами реестра.
func commonModule(path, name string, reg meta.ModuleRegistryFact, symbols ...domain.Symbol) ModuleEntry {
	r := reg
	return ModuleEntry{
		ModulePath: path,
		Kind:       bsl.ModuleCommon,
		NameNorm:   domain.NormalizeName(name),
		Registry:   &r,
		Layer:      domain.BaseLayer(testComponent),
		Symbols:    symbols,
	}
}
