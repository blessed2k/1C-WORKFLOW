package resolve

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// NameDelta — изменение поставщика имён (§18.4): что менять решает
// вызывающий (пайплайн index, у него до/после фактов), NameDelta лишь называет
// затронутые имена и область, где их искали.
//
//   - правка BSL-модуля (символ добавлен/удалён/сменил экспортность,
//     директиву, сигнатуру): Names = [одно имя], ModuleKind/ModuleNameNorm/
//     OwnerMType+OwnerNameNorm — как у изменившегося модуля, Global — его
//     текущее (или бывшее — см. ниже) свойство Global;
//   - правка XML общего модуля (Global, Server, ...): Names = ВСЕ его
//     экспортные имена (до и после объединением — модуль мог перестать
//     существовать в списке или появиться в нём из-за самой правки не
//     бывает, но экспортность конкретных методов могла разойтись, поэтому
//     вызывающий обязан передать объединение имён «до» и «после»), Global —
//     true, если модуль Global ЛИБО был, ЛИБО стал (иначе переразрешение
//     потеряет переход в одну из сторон);
//   - смена владельца/переименование модуля: вызывается ДВАЖДЫ — со старой
//     идентичностью и её именами, затем с новой и её именами; результаты
//     объединяются вызывающим (это и есть «удаление + добавление всех
//     имён» из архитектуры).
//
// Смена meta.builtin_registry_version сюда не входит: она требует полного
// re-resolve компонента, а не точечной дельты — множество имён, когда-либо
// консультировавших ScopePlatform, заранее неизвестно и не перечислимо
// через resolution_dep без обхода всех ссылок. Это дело пайплайна (internal/index),
// не AffectedKeys.
type NameDelta struct {
	Component domain.ComponentID
	Layer     domain.Layer

	ModulePath     string
	ModuleKind     bsl.ModuleKind
	ModuleNameNorm string // только для ModuleKind == bsl.ModuleCommon
	OwnerMType     string // только для менеджерных/объектных модулей
	OwnerNameNorm  string
	Global         bool

	// Names — имена (нормализованные или нет — AffectedKeys нормализует
	// сам, идемпотентно), затронутые этим изменением.
	Names []string
}

// AffectedKeys возвращает ключи разрешения, которые эта дельта делает
// подозрительными: любая ссылка, зарегистрировавшая такой ключ в
// resolution_dep (в том числе с пустым результатом — §18.4), обязана
// переразрешиться.
func AffectedKeys(d NameDelta) []KeyHash {
	var out []KeyHash
	for _, name := range d.Names {
		nameNorm := domain.NormalizeName(name)

		out = append(out, resolutionKey{
			Component: d.Component, Layer: d.Layer, Scope: ScopeLocalModule,
			Owner: d.ModulePath, Name: nameNorm,
		}.Hash())

		if d.ModuleKind == bsl.ModuleCommon && d.ModuleNameNorm != "" {
			out = append(out, resolutionKey{
				Component: d.Component, Layer: d.Layer, Scope: ScopeModuleExport,
				Owner: d.ModuleNameNorm, Name: nameNorm,
			}.Hash())
			if d.Global {
				out = append(out, resolutionKey{
					Component: d.Component, Layer: d.Layer, Scope: ScopeGlobalCommon, Name: nameNorm,
				}.Hash())
			}
		}

		if d.OwnerMType != "" && d.OwnerNameNorm != "" {
			out = append(out, resolutionKey{
				Component: d.Component, Layer: d.Layer, Scope: ScopeManagerModule,
				Owner: d.OwnerMType + "\x00" + d.OwnerNameNorm, Name: nameNorm,
			}.Hash())
		}
	}
	return out
}
