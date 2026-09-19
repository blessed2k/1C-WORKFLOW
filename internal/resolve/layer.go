// Перехватчики методов расширений (ADR-4, architecture-index.md
// §13). Семантика распознавания аннотации перенесена из единственной
// существующей реализации, internal/source/extension.go:36-45 (файл не
// редактируется и не импортируется: старый слой).
//
// Целевой метод перехватчика определяется ПО АРГУМЕНТУ аннотации
// (&Вместо("ОбработкаПроведения")), а имя перехватчика — по имени метода
// расширения (РасшБ_ОбработкаПроведения). В реальном коде это РАЗНЫЕ имена:
// расширение обычно префиксует свои методы, и прежнее правило «цель = имя
// метода» не совпадало ни с одним перехватчиком выгрузки.
//
// Аннотация, у которой аргумент не разобран (его нет либо парсер отметил его
// DiagBadAnnotationArgument), факта перехвата НЕ порождает: вместо догадки
// кладётся диагностика DiagInterceptTargetUnknown (ADR-027). Перехват самого
// себя — не менее уверенный, а просто другой и заведомо неверный факт.
package resolve

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// InterceptKind — вид перехватчика метода расширения.
type InterceptKind string

const (
	// InterceptBefore — &Перед: выполняется до оригинала, не может его отменить.
	InterceptBefore InterceptKind = "Перед"
	// InterceptAfter — &После: выполняется после оригинала.
	InterceptAfter InterceptKind = "После"
	// InterceptInstead — &Вместо: заменяет оригинал (ПродолжитьВызов вызывает его).
	InterceptInstead InterceptKind = "Вместо"
	// InterceptChangeAndValidate — &ИзменениеИКонтроль: перехват изменения
	// значения реквизита.
	InterceptChangeAndValidate InterceptKind = "ИзменениеИКонтроль"
)

// Valid сообщает, известен ли вид перехватчика.
func (k InterceptKind) Valid() bool {
	switch k {
	case InterceptBefore, InterceptAfter, InterceptInstead, InterceptChangeAndValidate:
		return true
	default:
		return false
	}
}

// interceptKindByLower — русские и английские синонимы аннотации
// (регистронезависимо), та же таблица, что canonicalKind в
// internal/source/extension.go:42-45.
var interceptKindByLower = map[string]InterceptKind{
	"перед": InterceptBefore, "before": InterceptBefore,
	"после": InterceptAfter, "after": InterceptAfter,
	"вместо": InterceptInstead, "around": InterceptInstead,
	"изменениеиконтроль": InterceptChangeAndValidate, "changeandvalidate": InterceptChangeAndValidate,
}

// ParseInterceptAnnotation разбирает одну аннотацию из bsl.Method.Annotations
// ("&Перед", "&Before", ...) в канонический вид перехватчика. ok=false, если
// строка — не аннотация перехватчика (или пуста).
func ParseInterceptAnnotation(raw string) (InterceptKind, bool) {
	s := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "&")))
	kind, ok := interceptKindByLower[s]
	return kind, ok
}

// DiagInterceptTargetUnknown — аннотация перехвата есть, а имя цели из неё не
// выводится: аргумента нет (&Вместо), скобки пусты (&Вместо()) либо аргумент
// не разобрался (&Вместо(123), незакрытая скобка). Факт перехвата в этом
// случае не строится вовсе — ADR-027, спецификация §2 «отказ вместо догадки».
const DiagInterceptTargetUnknown = "intercept_target_unknown"

// Intercept — факт перехвата метода базового слоя методом расширения
// (ADR-4: «перехватчики как факты intercepts со ссылкой на перехватываемый
// символ базового слоя»). Ссылка на базовый символ — по (ModulePath,
// TargetNameNorm): расширение заимствует модуль под тем же относительным
// путём, что и базовый слой (см. doc-комментарий пакета).
type Intercept struct {
	Kind                InterceptKind
	ModulePath          string
	TargetNameNorm      string
	InterceptorNameNorm string
	InterceptorSpan     domain.Span
	Layer               domain.Layer
	Confidence          domain.Confidence
}

// DeriveIntercepts находит перехватчики в разобранном модуле расширения.
// modulePath — канонический (NormalizeModulePath) путь модуля, общий для
// базового слоя и расширения. layer — слой самого расширения (не базового).
//
// Один метод способен нести несколько аннотаций перехвата одновременно
// (парсер это допускает — bsl.Method.Annotations это срез); на практике 1С
// такое не порождает, но DeriveIntercepts честно не отбрасывает такую
// комбинацию, а строит по факту на аннотацию.
//
// Вторым значением возвращаются диагностики DiagInterceptTargetUnknown — по
// одной на аннотацию, у которой имя цели не выводится. Вызывающий обязан их
// показать: молчаливое «фактов нет» здесь неотличимо от «перехватчиков нет».
func DeriveIntercepts(modulePath string, mod *bsl.Module, layer domain.Layer) ([]Intercept, []domain.Diagnostic) {
	if mod == nil {
		return nil, nil
	}
	var out []Intercept
	var diags []domain.Diagnostic
	for _, m := range mod.Methods {
		for _, a := range m.Annotations {
			kind, ok := ParseInterceptAnnotation(a.Name)
			if !ok {
				continue
			}
			// Цель — из аргумента аннотации. Пустой Arg значит, что имя цели не
			// выводится (аргумента нет, скобки пусты либо парсер не разобрал их
			// содержимое): факт не строится, ADR-027.
			target := domain.NormalizeName(strings.TrimSpace(a.Arg))
			if target == "" {
				diags = append(diags, domain.Diagnostic{
					Code:     DiagInterceptTargetUnknown,
					Severity: domain.SeverityWarning,
					Message: "у аннотации " + a.Name + " метода " + m.Name +
						" не разобрано имя перехватываемого метода — факт перехвата не построен;" +
						" укажите цель явно: " + a.Name + "(\"ИмяМетодаБазовогоСлоя\")",
					File: modulePath,
					Span: m.Span,
				})
				continue
			}
			out = append(out, Intercept{
				Kind:                kind,
				ModulePath:          modulePath,
				TargetNameNorm:      target,
				InterceptorNameNorm: m.NameNorm,
				InterceptorSpan:     m.Span,
				Layer:               layer,
				// Сам факт (вид перехватчика + имя цели + имя перехватчика) — точный,
				// из парсера: kind, аргумент аннотации и имя метода читаются буквально,
				// без догадки. confidence < 1 относится к КОНФЛИКТУ порядка применения
				// (DetectInsteadConflicts), не к этому факту.
				Confidence: domain.ConfidenceExact,
			})
		}
	}
	return out, diags
}

// InsteadConflictConfidence — confidence диагностики конфликта двух и более
// &Вместо на один метод. // упрощение: фиксированная константа, а не расчёт
// от числа слоёв или иного сигнала — задача различает «конфликт есть»/«нет»,
// не ранжирует конфликты между собой.
const InsteadConflictConfidence domain.Confidence = 0.5

// InterceptConflict — диагностика: несколько расширений перехватывают один
// метод через &Вместо. Runtime-порядок применения расширений из XML не
// выводится (ADR-4) — какая версия реально выполнится, определить статически
// нельзя, поэтому это diagnostic с ОБОИМИ (всеми) слоями и confidence < 1, а
// не молчаливый выбор одного. Семантика — та же, что у formconflicts.go
// (internal/source/formconflicts.go: «порядок выполнения расширений не
// гарантирован»), перенесённая на факты перехвата метода, а не form-specific
// изменения (ИзменитьРеквизиты/ДобавитьЭлемент и т.п. formconflicts.go не
// портируются — вне области этого тикета, метод-level перехват уже, чем
// form-level конфликты).
type InterceptConflict struct {
	ModulePath     string
	TargetNameNorm string
	Layers         []domain.Layer
	Confidence     domain.Confidence
}

// DetectInsteadConflicts группирует перехватчики по (ModulePath,
// TargetNameNorm) и возвращает конфликт для каждой группы, где &Вместо
// перехватывают ДВА И БОЛЕЕ расширения. Порядок вызывающего среза
// сохраняется (стабильная группировка) — важно для детерминированных тестов
// и воспроизводимой выдачи.
func DetectInsteadConflicts(intercepts []Intercept) []InterceptConflict {
	type key struct{ module, name string }
	byKey := map[key][]Intercept{}
	var order []key
	for _, ic := range intercepts {
		if ic.Kind != InterceptInstead {
			continue
		}
		k := key{ic.ModulePath, ic.TargetNameNorm}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], ic)
	}
	var out []InterceptConflict
	for _, k := range order {
		group := byKey[k]
		if len(group) < 2 {
			continue
		}
		layers := make([]domain.Layer, len(group))
		for i, ic := range group {
			layers[i] = ic.Layer
		}
		out = append(out, InterceptConflict{
			ModulePath: k.module, TargetNameNorm: k.name,
			Layers: layers, Confidence: InsteadConflictConfidence,
		})
	}
	return out
}
