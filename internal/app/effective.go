// Effective view расширений (тикет 14, ADR-4 — docs/architecture-index.md
// §13, §20). Effective view ВЫЧИСЛЯЕТСЯ НА ЧТЕНИИ и НЕ материализуется:
// каждый вызов заново читает факты слоёв (базового + применяющихся к нему
// расширений) из store и сливает их здесь — архитектурное решение ADR-4
// («вычисление дешевле поддержки инвалидации»), не временная мера и не долг.
//
// Перехватчики (&Перед/&После/&Вместо/ИзменениеИКонтроль) не хранятся в
// store как отдельный факт (parse/bsl сохраняет только вид аннотации
// метода — Method.Annotations, — не факт "модуль X перехватывает символ Y":
// схема таска 03 не заводила под это отдельную таблицу, а строить её —
// правка internal/store/internal/index вне зоны этого тикета). Поэтому
// исходник заимствовавшего модуля разбирается заново при каждом обращении;
// само наложение живёт в internal/effective (общее с internal/retrieve),
// здесь только перевод его результата в типы app.
//
// Предел, зафиксированный честно (ADR-4, docs/tools-index.md): порядок
// применения расширений из XML не выводится, «на замке»/safe mode не
// моделируются — см. интерсепторы конфликтов ниже.
package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/effective"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// parseView разбирает и валидирует общий параметр view=raw|effective (тикет
// 14, п.5). Пусто -> raw (умолчание, архитектура §20, регрессии по
// умолчанию нет). Неизвестное значение — ошибка: тасков 12/13 трактовали
// любое v != "raw" как «пока не поддержано» с молчаливым откатом на raw —
// теперь effective реализован, и опечатка в имени обязана быть замечена, а
// не тихо превращена в raw.
func parseView(raw string) (domain.View, *Error) {
	v := domain.View(strings.TrimSpace(raw))
	if v == "" {
		return domain.ViewRaw, nil
	}
	if !v.Valid() {
		return "", NewError(CodeNotFound, fmt.Sprintf("view %q неизвестен", raw),
			"допустимые значения: raw, effective")
	}
	return v, nil
}

// applyOrderOf — порядок применения компонента-слоя; 0 у базового слоя и у
// компонентов, отсутствующих в манифесте (не должно случаться для строк из
// store того же проекта, но не паникуем на рассинхроне).
func applyOrderOf(manifest workspace.Manifest, componentID string) int {
	c, ok := manifest.Component(domain.ComponentID(componentID))
	if !ok {
		return 0
	}
	return c.ApplyOrder
}

// effectiveIntercepts переводит effective-вид модуля (internal/effective,
// единственная реализация наложения слоёв) в типы app. Порядок расширений
// берётся из store той же транзакции, а не из манифеста: так get_symbol и
// get_context_for_task не могут разойтись в порядке перехватчиков.
func effectiveIntercepts(tx *store.ReadTx, base domain.ComponentID, modulePath string) ([]resolve.Intercept, []Warning, error) {
	r, err := effective.Module(effective.StoreSource(tx), string(base), modulePath)
	if err != nil {
		return nil, nil, err
	}
	var ics []resolve.Intercept
	for _, ic := range r.Intercepts {
		ics = append(ics, ic.Intercept)
	}
	var warnings []Warning
	for _, d := range r.Diagnostics {
		warnings = append(warnings, warningOf(d))
	}
	return ics, warnings, nil
}

func warningOf(n effective.Notice) Warning {
	return Warning{Code: n.Code, Message: n.Message, Hint: n.Hint}
}

// InterceptItem — один факт перехвата в API-выдаче: перехватчик, его вид и
// его слой (ADR-4: «каждый effective-факт несёт provenance»).
type InterceptItem struct {
	Kind            string             `json:"kind"`
	TargetName      string             `json:"targetName"`
	InterceptorName string             `json:"interceptorName"`
	Layer           domain.ComponentID `json:"layer"`
	ApplyOrder      int                `json:"applyOrder"`
	Confidence      float64            `json:"confidence"`
}

func interceptItem(ic resolve.Intercept) InterceptItem {
	return InterceptItem{
		Kind: string(ic.Kind), TargetName: ic.TargetNameNorm, InterceptorName: ic.InterceptorNameNorm,
		Layer: ic.Layer.Component, ApplyOrder: ic.Layer.ApplyOrder, Confidence: float64(ic.Confidence),
	}
}

func interceptItems(ics []resolve.Intercept) []InterceptItem {
	if len(ics) == 0 {
		return nil
	}
	out := make([]InterceptItem, len(ics))
	for i, ic := range ics {
		out[i] = interceptItem(ic)
	}
	return out
}

// interceptConflictWarning: предупреждение о конфликте &Вместо, текст общий
// с get_context_for_task (effective.InsteadConflict).
func interceptConflictWarning(c resolve.InterceptConflict) Warning {
	return warningOf(effective.InsteadConflict(c))
}

// targetInterceptWarnings строит предупреждения о перехватчиках символа
// targetID для инструментов графа (find_references, trace_call_graph), где
// прикреплять полный InterceptItem к каждой строке выдачи было бы слишком
// дорого (доступен только для корневого символа запроса, см.
// doc-комментарий FindReferencesInput.View/TraceCallGraphInput.View).
func targetInterceptWarnings(tx *store.ReadTx, targetID int64) []Warning {
	row, ok, err := tx.SymbolByID(targetID)
	if err != nil || !ok {
		return nil
	}
	ics, blobWarn, err := effectiveIntercepts(tx, domain.ComponentID(row.ComponentID), row.ModulePath)
	if err != nil {
		return nil
	}
	var mine []resolve.Intercept
	for _, ic := range ics {
		if ic.TargetNameNorm == row.NameNorm {
			mine = append(mine, ic)
		}
	}
	out := append([]Warning{}, blobWarn...)
	if len(mine) > 0 {
		names := make([]string, len(mine))
		for i, ic := range mine {
			names[i] = fmt.Sprintf("%s(%s)", ic.Layer.Component, ic.Kind)
		}
		out = append(out, Warning{
			Code:    "intercepted_by_extension",
			Message: fmt.Sprintf("символ %s перехвачен расширениями: %s", row.NameDisplay, strings.Join(names, ", ")),
			Hint:    "передайте view=effective в get_symbol, чтобы увидеть перехватчики целиком",
		})
	}
	for _, c := range resolve.DetectInsteadConflicts(mine) {
		out = append(out, interceptConflictWarning(c))
	}
	return out
}

// --- effective-слияние metadata_object (get_object, meta.go) ---

// sortObjectRowsByLayer упорядочивает строки одного объекта (across слоёв)
// для слияния: базовый слой первым, затем расширения по applyOrder,
// стабильно (тай-брейк по component_id — детерминированный вывод).
func sortObjectRowsByLayer(manifest workspace.Manifest, rows []store.MetadataObjectRow) []store.MetadataObjectRow {
	out := append([]store.MetadataObjectRow(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		bi, bj := out[i].Layer == "base", out[j].Layer == "base"
		if bi != bj {
			return bi
		}
		oi, oj := applyOrderOf(manifest, out[i].ComponentID), applyOrderOf(manifest, out[j].ComponentID)
		if oi != oj {
			return oi < oj
		}
		return out[i].ComponentID < out[j].ComponentID
	})
	return out
}
