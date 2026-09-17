package index

import (
	"fmt"
	"sort"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// buildEnvInput строит resolve.EnvInput из резидентного корпуса компонента —
// это и есть шаг D02/долга таска 09: EnvInput собирается ЗДЕСЬ, из фактов,
// уже разобранных пайплайном, а не запросом к store (store их на выборку не
// отдаёт, см. interfaces.md). Обход карт — по отсортированным ключам:
// детерминизм входа (§18.7).
//
// ИНВАРИАНТ (ADR-028): в корпусе не должно остаться ни одной гидратированной
// записи — восстановленной из source_file и потому не несущей ни bslModule,
// ни metaFacts. Такая запись прошла бы оба continue ниже молча, окружение
// собралось бы без модуля или объекта, и публикация записала бы НЕВЕРНЫЕ
// результаты разрешения имён: не падение и не красный тест, а тихо
// неразрешённые вызовы. Поэтому ответ — ошибка, которая абортит транзакцию
// пайплайна. Обеспечивает инвариант runComponent (все гидратированные записи
// компонента попадают в toRead), здесь он только проверяется.
func buildEnvInput(project domain.ProjectID, component domain.ComponentID, layer domain.Layer, corpus *componentCorpus) (resolve.EnvInput, error) {
	paths := make([]string, 0, len(corpus.files))
	for p := range corpus.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		if corpus.files[p].hydrated {
			return resolve.EnvInput{}, fmt.Errorf(
				"запись %s восстановлена из индекса и не содержит разобранных фактов — "+
					"окружение резолвера на таком корпусе не собирается", p)
		}
	}

	var objects []resolve.ObjectRef
	var members []resolve.MemberRef
	registryByCommonModule := make(map[string]*meta.ModuleRegistryFact)

	for _, p := range paths {
		rec := corpus.files[p]
		obj := rec.metaFacts.Object
		if obj == nil {
			continue
		}
		objKey := metadataObjectIdentityKey(component, obj.MType, obj.NameNorm)
		objects = append(objects, resolve.ObjectRef{MType: obj.MType, NameNorm: obj.NameNorm, IdentityKey: objKey})
		if obj.MType == "CommonModule" && rec.metaFacts.ModuleRegistry != nil {
			registryByCommonModule[obj.NameNorm] = rec.metaFacts.ModuleRegistry
		}
		for _, m := range rec.metaFacts.Members {
			members = append(members, resolve.MemberRef{
				ObjectMType: obj.MType, ObjectNameNorm: obj.NameNorm,
				Kind: m.Kind, NameNorm: m.NameNorm, Types: m.Types,
				IdentityKey: metadataMemberIdentityKey(objKey, m),
			})
		}
	}

	var modules []resolve.ModuleEntry
	for _, p := range paths {
		rec := corpus.files[p]
		if rec.bslModule == nil {
			continue
		}
		entry := resolve.ModuleEntry{
			ModulePath: domain.NormalizeModulePath(p),
			Kind:       rec.moduleInfo.Kind,
			Layer:      layer,
		}
		switch rec.moduleInfo.Kind {
		case bsl.ModuleCommon:
			entry.NameNorm = rec.moduleInfo.OwnerNameNorm
			if reg, ok := registryByCommonModule[rec.moduleInfo.OwnerNameNorm]; ok {
				entry.Registry = reg
			}
		case bsl.ModuleManager:
			// Только менеджерный модуль отвечает на «Коллекция.Объект.Метод(»
			// (§19.1 п.2, resolve.resolveManagerCall): модуль объекта того же
			// владельца в env.manager не кладём — иначе оба модуля одного
			// владельца боролись бы за один ключ карты (resolve/env.go:
			// env.manager индексируется ровно по OwnerMType+OwnerNameNorm).
			if mtype, ok := ownerTypeToMType(rec.moduleInfo.OwnerType); ok {
				entry.OwnerMType = mtype
				entry.OwnerNameNorm = rec.moduleInfo.OwnerNameNorm
			}
		}
		entry.Symbols = buildSymbols(project, component, layer, p, rec.bslModule)
		modules = append(modules, entry)
	}

	return resolve.EnvInput{Component: component, Layer: layer, Modules: modules, Objects: objects, Members: members}, nil
}

// buildSymbols переводит факты bsl.Module в domain.Symbol для Env: методы и
// переменные модуля становятся именами, видимыми резолверу.
func buildSymbols(project domain.ProjectID, component domain.ComponentID, layer domain.Layer, relPath string, mod *bsl.Module) []domain.Symbol {
	modulePath := domain.NormalizeModulePath(relPath)
	out := make([]domain.Symbol, 0, len(mod.Methods)+len(mod.Variables))
	for _, meth := range mod.Methods {
		nameNorm := domain.NormalizeName(meth.Name)
		params := make([]domain.Parameter, 0, len(meth.Params))
		for _, p := range meth.Params {
			params = append(params, domain.Parameter{
				Index: p.Index, NameNorm: p.NameNorm, NameDisplay: p.Name,
				ByValue: p.ByValue, HasDefault: p.HasDefault, Default: p.Default, Span: p.Span,
			})
		}
		out = append(out, domain.Symbol{
			UID:         domain.NewSymbolUID(project, component, modulePath, nameNorm),
			Project:     project,
			Component:   component,
			ModulePath:  modulePath,
			Kind:        meth.Kind,
			NameNorm:    nameNorm,
			NameDisplay: meth.Name,
			Export:      meth.Export,
			Async:       meth.Async,
			Directive:   meth.Directive,
			Params:      params,
			Span:        meth.Span,
			BodySpan:    meth.BodySpan,
			Layer:       layer,
			Provenance:  domain.Provenance{Source: domain.SourceBSLParser, File: relPath},
			Confidence:  domain.ConfidenceExact,
		})
	}
	for _, v := range mod.Variables {
		nameNorm := domain.NormalizeName(v.Name)
		out = append(out, domain.Symbol{
			UID:         domain.NewSymbolUID(project, component, modulePath, nameNorm),
			Project:     project,
			Component:   component,
			ModulePath:  modulePath,
			Kind:        domain.SymbolVariable,
			NameNorm:    nameNorm,
			NameDisplay: v.Name,
			Export:      v.Export,
			Span:        v.Span,
			Layer:       layer,
			Provenance:  domain.Provenance{Source: domain.SourceBSLParser, File: relPath},
			Confidence:  domain.ConfidenceExact,
		})
	}
	return out
}
