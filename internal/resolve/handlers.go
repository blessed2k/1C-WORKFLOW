package resolve

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// HandlerBindingResult — привязка обработчика события формы к символу
// модуля формы (store.HandlerBinding без form_id/origin_file_id — их знает
// вызывающий). Resolution=unresolved, когда обработчик объявлен в
// FormStructureFact, но метода с таким именем в модуле формы нет —
// критерий приёмки R43.1: честный unresolved, а не пустая выдача.
type HandlerBindingResult struct {
	Source          string
	Event           string
	HandlerNameNorm string
	HandlerDisplay  string
	Resolution      domain.Resolution
	HandlerUID      domain.SymbolUID // заполнен только при Resolution == resolved
}

// DeriveHandlerBinding разрешает обработчики формы (parse/meta.
// HandlerBindingFact, аспект form_structure) против символов модуля формы
// из Env. formModulePath — identity модуля формы (тот же путь, что несёт её
// ModuleEntry в Env).
func DeriveHandlerBinding(formModulePath string, handlers []meta.HandlerBindingFact, env Env) []HandlerBindingResult {
	if len(handlers) == 0 {
		return nil
	}
	mod, hasModule := env.Module(formModulePath)
	out := make([]HandlerBindingResult, 0, len(handlers))
	for _, h := range handlers {
		res := HandlerBindingResult{
			Source: h.Source, Event: h.Event,
			HandlerNameNorm: h.HandlerNameNorm, HandlerDisplay: h.HandlerDisplay,
			Resolution: domain.ResolutionUnresolved,
		}
		if hasModule {
			for _, s := range mod.Symbols {
				if isCallable(s.Kind) && s.NameNorm == h.HandlerNameNorm {
					res.Resolution = domain.ResolutionResolved
					res.HandlerUID = s.UID
					break
				}
			}
		}
		out = append(out, res)
	}
	return out
}
