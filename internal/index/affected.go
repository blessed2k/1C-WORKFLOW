package index

import (
	"sort"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// symbolNameNorms — нормализованные имена методов/переменных модуля
// (упрощение: без дедупликации — resolve.NameDelta.Names допускает
// повторы, AffectedKeys нормализует и хеширует каждое независимо).
func symbolNameNorms(mod *bsl.Module) []string {
	if mod == nil {
		return nil
	}
	out := make([]string, 0, len(mod.Methods)+len(mod.Variables))
	for _, m := range mod.Methods {
		out = append(out, m.NameNorm)
	}
	for _, v := range mod.Variables {
		out = append(out, v.NameNorm)
	}
	return out
}

func exportedNameNorms(mod *bsl.Module) []string {
	if mod == nil {
		return nil
	}
	out := make([]string, 0, len(mod.Methods))
	for _, m := range mod.Methods {
		if m.Export {
			out = append(out, m.NameNorm)
		}
	}
	return out
}

func union(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// affectedByChange считает resolve.AffectedKeys (§18.4) для одного
// изменившегося/удалённого/добавленного BSL-файла: дельта имён — объединение
// набора имён «до» и «после» (безопасное over-approximation — недостающая
// точность здесь стоит лишнего re-resolve нескольких ссылок, а не
// пропущенного переразрешения).
func affectedByChange(comp domain.ComponentID, layer domain.Layer, env resolve.Env, modulePath string, info bsl.ModuleInfo, oldMod, newMod *bsl.Module, ownerType string) []resolve.KeyHash {
	names := union(symbolNameNorms(oldMod), symbolNameNorms(newMod))
	if len(names) == 0 {
		return nil
	}
	delta := resolve.NameDelta{
		Component: comp, Layer: layer, ModulePath: domain.NormalizeModulePath(modulePath),
		ModuleKind: info.Kind, Names: names,
	}
	if info.Kind == bsl.ModuleCommon {
		delta.ModuleNameNorm = info.OwnerNameNorm
		if m, ok := env.CommonModuleByName(info.OwnerNameNorm); ok && m.Registry != nil {
			delta.Global = m.Registry.Global
		}
	}
	if info.Kind == bsl.ModuleManager {
		if mtype, ok := ownerTypeToMType(ownerType); ok {
			delta.OwnerMType, delta.OwnerNameNorm = mtype, info.OwnerNameNorm
		}
	}
	return resolve.AffectedKeys(delta)
}

// affectedByCommonModuleXMLChange: правка ТОЛЬКО XML общего модуля
// (module_context) без изменения его Module.bsl — дельта охватывает ВСЕ
// экспортные имена модуля (NameDelta doc: «изменение XML общего модуля...
// Names = все его экспортные имена»), Global — было ИЛИ стало.
func affectedByCommonModuleXMLChange(comp domain.ComponentID, layer domain.Layer, bslPath string, bslMod *bsl.Module, oldGlobal, newGlobal bool, nameNorm string) []resolve.KeyHash {
	names := exportedNameNorms(bslMod)
	if len(names) == 0 {
		return nil
	}
	delta := resolve.NameDelta{
		Component: comp, Layer: layer, ModulePath: domain.NormalizeModulePath(bslPath),
		ModuleKind: bsl.ModuleCommon, ModuleNameNorm: nameNorm,
		Global: oldGlobal || newGlobal, Names: names,
	}
	return resolve.AffectedKeys(delta)
}

// filesConsulting возвращает объединение relPath-ов, чьи ссылки
// консультировали хотя бы один из keys (corpus.keyIndex — обратный индекс
// resolution_dep в памяти, §18.4).
func filesConsulting(corpus *componentCorpus, keys []resolve.KeyHash) map[string]bool {
	out := make(map[string]bool)
	for _, k := range keys {
		for rel := range corpus.keyIndex[k] {
			out[rel] = true
		}
	}
	return out
}

func sortedFileKeys(corpus *componentCorpus) []string {
	out := make([]string, 0, len(corpus.files))
	for p := range corpus.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
