// Effective-слияние перехватчиков расширений внутри typed expansion
// get_context_for_task (закрывает долг: view=effective принимался схемой
// input'а, но молча откатывался на raw — cmd/mcp1c/idx_context.go и
// build.go). Семантика идентична get_symbol/get_object/get_module_structure
// (internal/app/effective.go): перехватчики (&Перед/&После/
// &Вместо/ИзменениеИКонтроль) вычисляются НА ЧТЕНИИ, не материализуются;
// каждый effective-факт несёт свой слой (provenance); конфликт нескольких
// &Вместо на один метод — diagnostic (Warning) с обоими слоями и confidence<1,
// а не молчаливый выбор одного (ADR-4, docs/architecture-index.md §13/§20).
//
// Само наложение слоёв (отбор расширений, разбор заимствованных модулей,
// диагностики, текст конфликта &Вместо) живёт в internal/effective и общее
// с internal/app: retrieve не импортирует app (гард CheckRetrieveNotApp), и
// до выноса это держалось двумя копиями, у которых разошлись порядок
// расширений и подсказки. Здесь только перевод результата в типы retrieve и
// то, что нужно одному get_context_for_task.
package retrieve

import (
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/effective"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// effectiveIntercepts переводит effective-вид модуля modulePath компонента
// base в типы retrieve. borrowedBy нужен вызывающему, чтобы отличить
// «расширения нет вовсе» от «расширение заимствовало модуль, но не
// перехватывает именно этот метод».
func effectiveIntercepts(tx *store.ReadTx, base, modulePath string) (ics []effective.Intercept, borrowedBy []string, warnings []Warning, err error) {
	r, err := effective.Module(effective.StoreSource(tx), base, modulePath)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, d := range r.Diagnostics {
		warnings = append(warnings, warningOf(d))
	}
	return r.Intercepts, r.BorrowedBy, warnings, nil
}

func warningOf(n effective.Notice) Warning {
	return Warning{Code: n.Code, Message: n.Message, Hint: n.Hint}
}

// effectiveInterceptsForSymbol сужает effectiveIntercepts до перехватчиков
// КОНКРЕТНОГО символа (TargetNameNorm == row.NameNorm) — общий шаг
// bugfix/signature-change/form builder'ов (expand.go/expand2.go).
func effectiveInterceptsForSymbol(tx *store.ReadTx, row store.SymbolRow) (mine []effective.Intercept, borrowedBy []string, warnings []Warning, err error) {
	ics, borrowedBy, warnings, ierr := effectiveIntercepts(tx, row.ComponentID, row.ModulePath)
	if ierr != nil {
		return nil, nil, nil, ierr
	}
	for _, ic := range ics {
		if ic.TargetNameNorm == row.NameNorm {
			mine = append(mine, ic)
		}
	}
	return mine, borrowedBy, warnings, nil
}

// makeInterceptCandidate строит Signature-кандидата с ФАКТИЧЕСКИМ текстом
// перехватчика — effective-аналог эвристики signatureInterceptors
// (expand.go: там — совпадение по имени в любом компоненте-расширении,
// confidence 0.5, provenance "heuristic"; здесь — точный факт из
// resolve.DeriveIntercepts, с реальным &Перед/&После/&Вместо/
// ИзменениеИКонтроль и confidence ic.Confidence, домен ConfidenceExact для
// самого факта — layer.go).
func makeInterceptCandidate(bctx *buildCtx, sc scoreCtx, ic effective.Intercept, category, why string) *candidate {
	text := ic.Text
	if text == "" {
		text = fmt.Sprintf("&%s(%q)", ic.Kind, ic.TargetNameNorm)
	}
	// Ключ различает модуль и сам перехватчик, а не только слой и имя цели:
	// у writer_intercepts (register) одно расширение перехватывает одно и то же
	// имя ОбработкаПроведения в модулях РАЗНЫХ документов, и без модуля в
	// ключе второй факт молча терялся бы при дедупликации кандидатов.
	c := &candidate{
		id: fmt.Sprintf("intercept-eff:%s:%s:%d:%s:%s:%s", category, ic.Layer.Component, ic.Layer.ApplyOrder,
			domain.NormalizeModulePath(ic.ModulePath), ic.TargetNameNorm, ic.InterceptorNameNorm),
		bucket:      bucketSignature,
		category:    category,
		component:   string(ic.Layer.Component),
		module:      ic.ModulePath,
		display:     ic.InterceptorNameNorm,
		kindLabel:   string(ic.Kind),
		text:        text,
		span:        ic.InterceptorSpan,
		resolution:  string(domain.ResolutionResolved),
		confidence:  ic.Confidence,
		whyIncluded: why,
		charCost:    runeLen(text),
	}
	return bctx.apply(sc, c)
}

// interceptConflictWarnings строит warnings диагностик &Вместо-конфликта по
// набору перехватчиков, общий хвост effectiveSignatureInterceptors и
// effectiveFormHandlerIntercepts. Текст общий с internal/app
// (effective.InsteadConflict).
func interceptConflictWarnings(mine []effective.Intercept) []Warning {
	if len(mine) < 2 {
		return nil
	}
	resolveIcs := make([]resolve.Intercept, len(mine))
	for i, ic := range mine {
		resolveIcs[i] = ic.Intercept
	}
	var out []Warning
	for _, c := range resolve.DetectInsteadConflicts(resolveIcs) {
		out = append(out, warningOf(effective.InsteadConflict(c)))
	}
	return out
}

// effectiveSignatureInterceptors — "interceptors" (signature-change,
// обязательная категория) под view=effective: точные факты вместо эвристики
// signatureInterceptors (по имени, confidence 0.5) — вызывается ТОЛЬКО когда
// bctx.view==domain.ViewEffective; raw остаётся на signatureInterceptors без
// изменений (регрессия по умолчанию не допускается, см. doc-комментарий
// expandSignatureChange).
func effectiveSignatureInterceptors(bctx *buildCtx, row store.SymbolRow) ([]*candidate, []Warning) {
	mine, borrowedBy, warn, err := effectiveInterceptsForSymbol(bctx.tx, row)
	if err != nil {
		return nil, nil
	}
	var out []*candidate
	sc := scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: row.ComponentID}
	for _, ic := range mine {
		out = append(out, makeInterceptCandidate(bctx, sc, ic, "interceptors",
			fmt.Sprintf("перехватчик расширения %s (&%s %q, effective — точный факт resolve.DeriveIntercepts, не эвристика по имени)",
				ic.Layer.Component, ic.Kind, row.NameDisplay)))
	}
	warnings := append([]Warning{}, warn...)
	warnings = append(warnings, interceptConflictWarnings(mine)...)
	if len(mine) == 0 && len(borrowedBy) > 0 {
		warnings = append(warnings, Warning{
			Code: "no_interceptor_candidates",
			Message: fmt.Sprintf(
				"модуль %s заимствован расширением(ями) %s, но ни один перехватчик (&Перед/&После/&Вместо/ИзменениеИКонтроль) не назван %q",
				row.ModulePath, strings.Join(borrowedBy, ", "), row.NameDisplay),
			Hint: "если перехват ожидался, проверьте точное имя аннотации в исходнике расширения (get_module_structure component=<расширение> view=effective)",
		})
	}
	return out, warnings
}

// effectivePostingIntercepts — "posting_handler_intercepts" под
// view=effective: перехватчики обработчика проведения и их
// СОБСТВЕННЫЕ обращения к регистрам. Подключение: тем же
// приёмом, что handler_intercepts у формы (expand2.go): общий
// effectiveInterceptsForSymbol + makeInterceptCandidate +
// interceptConflictWarnings, второй реализации механики перехвата здесь нет.
//
// Обращения к регистрам берутся у СИМВОЛА перехватчика, найденного по факту
// (слой + путь модуля + имя перехватчика из resolve.Intercept), поэтому их
// component — компонент расширения, а не документа-анкера: движение,
// добавленное расширением, в ответе отличимо от движения базового слоя.
func effectivePostingIntercepts(bctx *buildCtx, handler store.SymbolRow, ownerComponent string) ([]*candidate, []Warning) {
	ics, _, warnings, err := effectiveInterceptsForSymbol(bctx.tx, handler)
	if err != nil {
		return nil, nil
	}
	out, icWarnings := postingInterceptCandidates(bctx, ics, ownerComponent, handler.ComponentID, handler.NameDisplay, "")
	warnings = append(warnings, icWarnings...)
	warnings = append(warnings, interceptConflictWarnings(ics)...)
	return out, warnings
}

// postingInterceptCandidates — общий хвост ОБОИХ путей posting-перехватчиков
// (базовый обработчик есть — effectivePostingIntercepts; базового нет вовсе —
// postingInterceptsWithoutBaseHandler): факт перехвата в
// posting_handler_intercepts плюс СОБСТВЕННЫЕ обращения перехватчика к
// регистрам. Второй реализации механики перехвата в пакете по-прежнему нет —
// путь «цели нет» отличается только тем, ОТКУДА взяты факты, а не тем, что с
// ними делают.
//
// whyTail дописывается к объяснению каждого кандидата: в пути «базового
// метода нет» потребитель обязан видеть это прямо на факте, а не только в
// предупреждении ответа.
func postingInterceptCandidates(bctx *buildCtx, ics []effective.Intercept,
	ownerComponent, anchorComponent, targetDisplay, whyTail string) ([]*candidate, []Warning) {
	sc := scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: anchorComponent}
	var out []*candidate
	var warnings []Warning
	for _, ic := range ics {
		why := fmt.Sprintf("перехватчик расширения %s обработчика проведения %s (&%s, effective — точный факт resolve.DeriveIntercepts, не совпадение по имени)",
			ic.Layer.Component, targetDisplay, ic.Kind)
		out = append(out, makeInterceptCandidate(bctx, sc, ic, "posting_handler_intercepts", why+whyTail))
		sym, symWarnings, ok := interceptorSymbol(bctx.symbols, ic)
		warnings = append(warnings, symWarnings...)
		if !ok {
			continue
		}
		accCands, accWarnings := postingRegisterAccesses(bctx, ownerComponent, sym.ID, sym.NameDisplay,
			fmt.Sprintf("(перехватчик расширения %s, &%s)", ic.Layer.Component, ic.Kind))
		out = append(out, accCands...)
		warnings = append(warnings, accWarnings...)
	}
	return out, warnings
}

// postingHandlerDisplayName — платформенное имя события в том написании, в
// каком его пишет конфигуратор. Нужно там, где символа с этим именем НЕТ и
// взять NameDisplay не у чего (путь «базового метода не существует»).
const postingHandlerDisplayName = "ОбработкаПроведения"

// postingInterceptsWithoutBaseHandler — путь «базового обработчика нет
// вовсе» (ADR-034). Отличие от effectivePostingIntercepts ровно одно: цель
// перехвата — не найденный символ, а ИМЯ события по пути модуля объекта,
// выведенному из объявления (postingObjectModulePath). Отбор расширений —
// тот же internal/effective внутри effectiveIntercepts, отдельного
// механизма здесь нет.
//
// Про view. Факты перехватчиков добавляются только при view=effective —
// правило «raw не несёт фактов расширений» держится. Но ПРОВЕРКА идёт в
// обоих видах, и предупреждение уходит в оба: raw, промолчавший про документ,
// у которого проведение целиком написано расширением, — не «сырой вид», а
// ложный ответ. Цена — разбор модулей объекта в применяющихся расширениях и
// на raw, но только в этом узком случае (базового обработчика нет), и она
// названа в ADR-034.
func postingInterceptsWithoutBaseHandler(bctx *buildCtx, obj store.MetadataObjectRow) ([]*candidate, []Warning) {
	readFailed := func(what string, err error) []Warning {
		return []Warning{{
			Code: "posting_intercepts_read_failed",
			Message: fmt.Sprintf(
				"не удалось прочитать %s для %s: %v — у объекта нет собственного обработчика проведения, и есть ли перехватчик расширения, ответ не знает",
				what, obj.NameDisplay, err),
			Hint: hintRetryAfterReindex,
		}}
	}
	modulePath, ok, err := postingObjectModulePath(bctx.tx, obj)
	if err != nil {
		return nil, readFailed("объявление объекта", err)
	}
	if !ok {
		return nil, []Warning{postingBaseHandlerMissingWarning(obj, nil, false)}
	}
	ics, _, warnings, ierr := effectiveIntercepts(bctx.tx, obj.ComponentID, modulePath)
	if ierr != nil {
		return nil, readFailed("модули расширений", ierr)
	}
	var mine []effective.Intercept
	var names []string
	for _, ic := range ics {
		if ic.TargetNameNorm != postingHandlerNameNorm {
			continue
		}
		mine = append(mine, ic)
		names = append(names, fmt.Sprintf("%s: &%s %s", ic.Layer.Component, ic.Kind, ic.InterceptorNameNorm))
	}
	warnings = append(warnings, postingBaseHandlerMissingWarning(obj, names, true))
	if len(mine) == 0 {
		return nil, warnings
	}
	warnings = append(warnings, interceptConflictWarnings(mine)...)
	if bctx.view != domain.ViewEffective {
		return nil, warnings
	}
	out, icWarnings := postingInterceptCandidates(bctx, mine, obj.ComponentID, obj.ComponentID,
		postingHandlerDisplayName, "; базового ОбработкаПроведения у объекта нет — это единственный исполняемый код проведения")
	return out, append(warnings, icWarnings...)
}

// interceptorSymbol находит СТРОКУ символа самого перехватчика в индексе:
// слой (компонент расширения) + путь заимствованного модуля + имя метода из
// resolve.Intercept. Все три — из уже построенного факта перехвата, поэтому
// это точное совпадение, а не поиск по подстроке имени.
//
// Отдаётся вся строка, а не один ID: у вызывающего нет другого источника
// DISPLAY-имени перехватчика (resolve.Intercept несёт только нормализованное
// InterceptorNameNorm), а движения перехватчика обязаны попадать в те же
// категории и в том же виде, что движения базового обработчика — иначе в
// одной категории ответа оказываются два разных регистра написания.
//
// Ни один исход не молчит. Отказ чтения, ненайденный символ и НЕОДНОЗНАЧНОСТЬ
// (несколько символов на одно имя в одном модуле слоя) возвращаются
// предупреждениями: без них перехватчик в ответе есть, движений у него нет, и
// почему — не сказано; это тот же молчаливый выбор одного из нескольких, что
// уже запрещён для &Вместо (effective.InsteadConflict). При неоднозначности
// работа продолжается с первым совпадением — порядок FindSymbols
// детерминирован (ORDER BY name_norm, id), — но факт выбора назван.
// symbolFinder — единственная выборка store, которой пользуется поиск символа
// перехватчика. Интерфейс объявлен ПОТРЕБИТЕЛЕМ (соглашение CLAUDE.md, тот же
// приём, что syntaxCorpus в cmd/mcp1c), и заведён ровно ради одной вещи: без
// него ветка interceptor_symbol_read_failed недостижима ни одним тестом.
// Отказ чтения у живой *store.ReadTx посреди сборки не воспроизводится, и
// предупреждение, написанное ради того, чтобы сбой не выглядел пустотой, само
// осталось бы непроверенным — ровно тот случай, когда зелёный прогон ничего
// не значит.
type symbolFinder interface {
	FindSymbols(q store.SymbolSearch) ([]store.SymbolRow, error)
}

func interceptorSymbol(finder symbolFinder, ic effective.Intercept) (store.SymbolRow, []Warning, bool) {
	hint := "исходник перехватчика: get_module_structure component=" + string(ic.Layer.Component) + " view=effective"
	rows, err := finder.FindSymbols(store.SymbolSearch{
		NameNorm: ic.InterceptorNameNorm, ComponentID: string(ic.Layer.Component), Limit: 50,
	})
	if err != nil {
		return store.SymbolRow{}, []Warning{{
			Code: "interceptor_symbol_read_failed",
			Message: fmt.Sprintf(
				"не удалось найти символ перехватчика %s (расширение %s, модуль %s): %v — его обращения к регистрам в ответ не вошли",
				ic.InterceptorNameNorm, ic.Layer.Component, ic.ModulePath, err),
			Hint: hintRetryAfterReindex,
		}}, false
	}
	want := domain.NormalizeModulePath(ic.ModulePath)
	var matched []store.SymbolRow
	for _, r := range rows {
		if r.NameNorm == ic.InterceptorNameNorm && domain.NormalizeModulePath(r.ModulePath) == want {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return store.SymbolRow{}, []Warning{{
			Code: "interceptor_symbol_not_found",
			Message: fmt.Sprintf(
				"перехватчик %s расширения %s есть в исходнике модуля %s, но его символа нет в индексе — обращения к регистрам самого перехватчика в ответ не вошли",
				ic.InterceptorNameNorm, ic.Layer.Component, ic.ModulePath),
			Hint: "модуль расширения мог быть проиндексирован раньше правки — вызовите reindex; " + hint,
		}}, false
	}
	if len(matched) > 1 {
		return matched[0], []Warning{{
			Code: "interceptor_symbol_ambiguous",
			Message: fmt.Sprintf(
				"имени перехватчика %s в модуле %s расширения %s отвечают %d символа(ов) — обращения к регистрам взяты у первого, остальные в ответ не вошли",
				ic.InterceptorNameNorm, ic.ModulePath, ic.Layer.Component, len(matched)),
			Hint: hint,
		}}, true
	}
	return matched[0], nil, true
}

// --- register (ADR-035) --------------------------------------------------

// registerInterceptReadFailed: отказ чтения наложения для писателя регистра.
// Сбой не имеет права выглядеть как «перехватчиков нет» (ADR-030): факт
// перехвата писателя меняет ответ на вопрос «кто пишет», и молчание о том, что
// его не удалось проверить, было бы той же немотой.
func registerInterceptReadFailed(writer store.SymbolRow, err error) Warning {
	return readFailedWarning("register_writer_intercepts_read_failed",
		fmt.Sprintf("не удалось наложить расширения на писателя регистра %s.%s: %v; перехватчики этого писателя в ответ не вошли",
			writer.ModulePath, writer.NameDisplay, err))
}

// hintRetryAfterReindex: общая подсказка предупреждений об отказе чтения.
// Сбой чтения посреди вызова лечится повтором после reindex, и текст этой
// подсказки обязан быть один у всех *_read_failed.
const hintRetryAfterReindex = "повторите вызов после reindex"

// readFailedWarning: конструктор предупреждения *_read_failed с общей
// подсказкой.
func readFailedWarning(code, message string) Warning {
	return Warning{Code: code, Message: message, Hint: hintRetryAfterReindex}
}

// interceptsOfSymbol: перехватчики символа row по наложению вызова (кэш
// Overlay), тот же отбор по имени цели, что effectiveInterceptsForSymbol.
func interceptsOfSymbol(o *effective.Overlay, row store.SymbolRow) ([]effective.Intercept, []Warning, error) {
	r, err := o.Module(row.ComponentID, row.ModulePath)
	if err != nil {
		return nil, nil, err
	}
	var mine []effective.Intercept
	for _, ic := range r.Intercepts {
		if ic.TargetNameNorm == row.NameNorm {
			mine = append(mine, ic)
		}
	}
	return mine, warningsOf(r.Diagnostics), nil
}

func warningsOf(notes []effective.Notice) []Warning {
	var out []Warning
	for _, n := range notes {
		out = append(out, warningOf(n))
	}
	return out
}

// interceptWhy: общий текст о факте перехвата для объяснений кандидатов
// register/query: вид аннотации, слой и цель в базовом модуле.
func interceptWhy(ic effective.Intercept) string {
	return fmt.Sprintf("перехватчик расширения %s: &%s(%q) метода базового модуля %s",
		ic.Layer.Component, ic.Kind, ic.TargetNameNorm, ic.ModulePath)
}

// registerWriterIntercepts: "writer_intercepts" под view=effective
// (ADR-035): факты перехвата писателей регистра в обе стороны.
//
//   - Писатель базового слоя перехвачен расширением. &Вместо заменяет его, и
//     запись базового слоя исполняется только через ПродолжитьВызов; без
//     факта перехвата агент читает её как безусловную.
//   - Запись сделана самим перехватчиком (register_access со слоем
//     расширения). raw видит её как запись процедуры расширения, не связанной
//     ни с каким базовым методом; факт перехвата называет, какое событие
//     базового объекта её исполняет.
//
// Механика перехвата общая с form/posting (effectiveInterceptsForSymbol,
// makeInterceptCandidate, interceptConflictWarnings): второй реализации нет.
// Категория необязательная: обязательная объявила бы недостаточным любой
// ответ по регистру без расширений.
func registerWriterIntercepts(bctx *buildCtx, obj store.MetadataObjectRow, overlay *effective.Overlay,
	baseWriters []store.SymbolRow, writerInterceptFacts []effective.Intercept) ([]*candidate, []Warning) {
	sc := scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: obj.ComponentID}
	var out []*candidate
	var warnings []Warning
	for _, w := range baseWriters {
		mine, ws, err := interceptsOfSymbol(overlay, w)
		if err != nil {
			warnings = append(warnings, registerInterceptReadFailed(w, err))
			continue
		}
		warnings = append(warnings, ws...)
		for _, ic := range mine {
			why := fmt.Sprintf("%s: пишет в %s (register_access), в effective-виде перехвачен", w.NameDisplay, obj.NameDisplay)
			if ic.Kind == resolve.InterceptInstead {
				why += " и заменён: запись базового слоя исполняется только через ПродолжитьВызов"
			}
			out = append(out, makeInterceptCandidate(bctx, sc, ic, "writer_intercepts", why+"; "+interceptWhy(ic)))
		}
		warnings = append(warnings, interceptConflictWarnings(mine)...)
	}
	for _, ic := range writerInterceptFacts {
		out = append(out, makeInterceptCandidate(bctx, sc, ic, "writer_intercepts",
			fmt.Sprintf("запись в %s сделана самим перехватчиком; %s", obj.NameDisplay, interceptWhy(ic))))
	}
	return out, warnings
}

// --- query (ADR-035) -----------------------------------------------------

// queryInterceptCandidates: "query_intercepts" под view=effective и тексты
// запросов самих перехватчиков символа-анкера row. Второе значение: сколько
// текстов запросов нашлось у перехватчиков, чтобы вызывающий не объявил
// no_query_in_symbol, когда запрос живёт только в расширении. Третье:
// перехватчики &ИзменениеИКонтроль, чей текст заменяет базовый.
//
// Символ перехватчика ищется по факту перехвата (interceptorSymbol: слой +
// путь модуля + имя), а не по подстроке имени; его запросы ложатся в те же
// query_text/schema/tables_fields, что запросы анкера, но с component слоя
// расширения, поэтому в ответе отличимы от базовых. Собираются ТОЛЬКО
// перехватчики самого анкера, а не все запросы модулей расширений.
func queryInterceptCandidates(bctx *buildCtx, row store.SymbolRow) ([]*candidate, int, []effective.Intercept, []Warning) {
	mine, _, warnings, err := effectiveInterceptsForSymbol(bctx.tx, row)
	if err != nil {
		return nil, 0, nil, []Warning{readFailedWarning("query_intercepts_read_failed",
			fmt.Sprintf("не удалось наложить расширения на %s.%s: %v; перехватчики и их запросы в ответ не вошли",
				row.ModulePath, row.NameDisplay, err))}
	}
	sc := scoreCtx{depth: 1, anchorStrength: 1.0, direction: 1.0, anchorComp: row.ComponentID}
	var out []*candidate
	var replacing []effective.Intercept
	count := 0
	for _, ic := range mine {
		if ic.Kind == resolve.InterceptChangeAndValidate {
			replacing = append(replacing, ic)
		}
		out = append(out, makeInterceptCandidate(bctx, sc, ic, "query_intercepts",
			fmt.Sprintf("владелец запроса %s перехвачен; %s", row.NameDisplay, interceptWhy(ic))))
		sym, symWarnings, ok := interceptorSymbol(bctx.symbols, ic)
		warnings = append(warnings, symWarnings...)
		if !ok {
			continue
		}
		queries, qerr := bctx.queries.QueriesBySymbolID(sym.ID)
		if qerr != nil {
			warnings = append(warnings, readFailedWarning("query_intercepts_read_failed",
				fmt.Sprintf("не удалось прочитать запросы перехватчика %s расширения %s: %v; его тексты запросов в ответ не вошли",
					sym.NameDisplay, ic.Layer.Component, qerr)))
			continue
		}
		count += len(queries)
		qCands, qWarnings := queryCandidates(bctx, sym, queries, sc, " ("+interceptWhy(ic)+")")
		out = append(out, qCands...)
		warnings = append(warnings, qWarnings...)
	}
	warnings = append(warnings, interceptConflictWarnings(mine)...)
	return out, count, replacing, warnings
}

// borrowedObjectsFinder: единственная выборка заимствований, которой
// пользуются add-attribute и rights. Объявлена потребителем (тот же приём,
// что symbolFinder): без неё отказ чтения и отзыв заявления forms/rls
// недостижимы тестом.
type borrowedObjectsFinder interface {
	BorrowedObjects(obj store.MetadataObjectRow) ([]store.MetadataObjectRow, error)
}

// storeBorrowedObjects: продакшн-вариант поверх транзакции вызова, наложение
// объекта из internal/effective.
type storeBorrowedObjects struct{ tx *store.ReadTx }

func (s storeBorrowedObjects) BorrowedObjects(obj store.MetadataObjectRow) ([]store.MetadataObjectRow, error) {
	return effective.BorrowedObjects(effective.StoreObjectSource(s.tx), obj)
}

// queryReader: чтение текстов запросов символа (query). Шов ради отказа
// чтения запросов анкера, который не должен теряться (ADR-035).
type queryReader interface {
	QueriesBySymbolID(symbolID int64) ([]store.QueryRow, error)
}
