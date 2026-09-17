package resolve

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// TestParseInterceptAnnotation — все восемь написаний (4 русских + 4
// английских синонима), регистронезависимо, плюс отказ на не-перехватчиках
// (обычные директивы компиляции).
func TestParseInterceptAnnotation(t *testing.T) {
	cases := []struct {
		raw    string
		want   InterceptKind
		wantOK bool
	}{
		{"&Перед", InterceptBefore, true},
		{"&перед", InterceptBefore, true},
		{"&Before", InterceptBefore, true},
		{"&После", InterceptAfter, true},
		{"&After", InterceptAfter, true},
		{"&Вместо", InterceptInstead, true},
		{"&Around", InterceptInstead, true},
		{"&ИзменениеИКонтроль", InterceptChangeAndValidate, true},
		{"&ChangeAndValidate", InterceptChangeAndValidate, true},
		{"&НаСервере", "", false},
		{"&НаКлиенте", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got, ok := ParseInterceptAnnotation(c.raw)
			if ok != c.wantOK || (ok && got != c.want) {
				t.Errorf("ParseInterceptAnnotation(%q) = (%q, %v), want (%q, %v)", c.raw, got, ok, c.want, c.wantOK)
			}
		})
	}
}

func mustParse(t *testing.T, src string) *bsl.Module {
	t.Helper()
	mod, diags := bsl.Parse([]byte(src), bsl.Options{File: "x.bsl", SkipReferences: true})
	for _, d := range diags {
		t.Fatalf("неожиданная диагностика парсера: %+v", d)
	}
	return mod
}

// TestDeriveInterceptsAllFourKinds — все четыре вида перехватчика в одном
// модуле распознаются, цель берётся из АРГУМЕНТА аннотации, а имя
// перехватчика — из имени метода. Фикстура намеренно повторяет реальный код
// реальной выгрузки (префикс расширения `РасшБ_`): имена цели и перехватчика
// РАЗНЫЕ, и старый контракт «связываются по имени» на ней бы не сработал.
func TestDeriveInterceptsAllFourKinds(t *testing.T) {
	src := `
&Перед("ПередЗаписью")
Процедура РасшБ_ПередЗаписью(Отказ)
КонецПроцедуры

&После("ПриЗаписи")
Процедура РасшБ_ПриЗаписи()
КонецПроцедуры

&Вместо("ОбработкаПроведения")
Процедура РасшБ_ОбработкаПроведения(Отказ, РежимПроведения)
КонецПроцедуры

&ИзменениеИКонтроль("ОбработкаПроверкиЗаполнения")
Процедура РасшБ_ОбработкаПроверкиЗаполнения(Отказ, ПроверяемыеРеквизиты)
КонецПроцедуры
`
	mod := mustParse(t, src)
	layer := domain.ExtensionLayer("ext", 1)
	got, diags := DeriveIntercepts("Catalogs/Товары/Ext/ManagerModule.bsl", mod, layer)
	if len(diags) != 0 {
		t.Fatalf("на разобранных аргументах диагностик быть не должно: %+v", diags)
	}
	if len(got) != 4 {
		t.Fatalf("DeriveIntercepts вернул %d фактов, want 4: %+v", len(got), got)
	}
	wantKinds := map[string]InterceptKind{
		"передзаписью":                InterceptBefore,
		"призаписи":                   InterceptAfter,
		"обработкапроведения":         InterceptInstead,
		"обработкапроверкизаполнения": InterceptChangeAndValidate,
	}
	wantInterceptors := map[string]string{
		"передзаписью":                "расшб_передзаписью",
		"призаписи":                   "расшб_призаписи",
		"обработкапроведения":         "расшб_обработкапроведения",
		"обработкапроверкизаполнения": "расшб_обработкапроверкизаполнения",
	}
	for _, ic := range got {
		want, ok := wantKinds[ic.TargetNameNorm]
		if !ok {
			t.Fatalf("неожиданный TargetNameNorm %q в %+v", ic.TargetNameNorm, ic)
		}
		if ic.Kind != want {
			t.Errorf("%s: kind = %s, want %s", ic.TargetNameNorm, ic.Kind, want)
		}
		if ic.InterceptorNameNorm != wantInterceptors[ic.TargetNameNorm] {
			t.Errorf("%s: interceptor = %q, want %q (имя метода расширения, не имя цели)",
				ic.TargetNameNorm, ic.InterceptorNameNorm, wantInterceptors[ic.TargetNameNorm])
		}
		if ic.InterceptorNameNorm == ic.TargetNameNorm {
			t.Errorf("%s: цель и перехватчик совпали — связь снова строится по имени", ic.TargetNameNorm)
		}
		if ic.Layer != layer {
			t.Errorf("%s: layer = %+v, want %+v", ic.TargetNameNorm, ic.Layer, layer)
		}
		if ic.Confidence != domain.ConfidenceExact {
			t.Errorf("%s: confidence = %v, want ConfidenceExact (факт точный: парсер, не эвристика)", ic.TargetNameNorm, ic.Confidence)
		}
		if ic.ModulePath != "Catalogs/Товары/Ext/ManagerModule.bsl" {
			t.Errorf("%s: modulePath = %q", ic.TargetNameNorm, ic.ModulePath)
		}
	}
}

// TestDeriveInterceptsIgnoresOrdinaryMethods — метод без аннотации
// перехвата не порождает Intercept.
func TestDeriveInterceptsIgnoresOrdinaryMethods(t *testing.T) {
	mod := mustParse(t, `
&НаСервере
Процедура Обычная()
КонецПроцедуры
`)
	got, diags := DeriveIntercepts("x.bsl", mod, domain.ExtensionLayer("ext", 1))
	if len(got) != 0 {
		t.Fatalf("DeriveIntercepts на обычном методе вернул %d фактов, want 0: %+v", len(got), got)
	}
	if len(diags) != 0 {
		t.Fatalf("обычный метод не повод для диагностики: %+v", diags)
	}
}

// TestDeriveInterceptsRefusesUnknownTarget — ADR-027: аннотация, из которой
// имя цели не выводится, факта перехвата не порождает НИ ПРИ КАКОМ входе, а
// даёт диагностику. Перехват самого себя (старое поведение) не появляется:
// именно он молча ломал effective view.
func TestDeriveInterceptsRefusesUnknownTarget(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"без скобок", "&Вместо\nПроцедура РасшБ_ОбработкаПроведения()\nКонецПроцедуры\n"},
		{"пустые скобки", "&Вместо()\nПроцедура РасшБ_ОбработкаПроведения()\nКонецПроцедуры\n"},
		{"аргумент не имя", "&Вместо(123)\nПроцедура РасшБ_ОбработкаПроведения()\nКонецПроцедуры\n"},
		{"скобка не закрыта", "&Вместо(\nПроцедура РасшБ_ОбработкаПроведения()\nКонецПроцедуры\n"},
		{"пустая строка", "&После(\"\")\nПроцедура РасшБ_ПриЗаписи()\nКонецПроцедуры\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Диагностики парсера здесь ожидаемы (DiagBadAnnotationArgument),
			// поэтому не mustParse.
			mod, _ := bsl.Parse([]byte(c.src), bsl.Options{File: "m.bsl", SkipReferences: true})
			got, diags := DeriveIntercepts("m.bsl", mod, domain.ExtensionLayer("ext", 1))
			if len(got) != 0 {
				t.Fatalf("построен факт перехвата на неразобранной цели: %+v", got)
			}
			if len(diags) != 1 {
				t.Fatalf("диагностик %d, want 1: %+v", len(diags), diags)
			}
			d := diags[0]
			if d.Code != DiagInterceptTargetUnknown {
				t.Errorf("code = %q, want %q", d.Code, DiagInterceptTargetUnknown)
			}
			if d.Severity != domain.SeverityWarning {
				t.Errorf("severity = %q, want warning", d.Severity)
			}
			if d.File != "m.bsl" {
				t.Errorf("file = %q, want путь модуля", d.File)
			}
			if !strings.Contains(d.Message, "РасшБ_") {
				t.Errorf("сообщение обязано называть метод: %q", d.Message)
			}
		})
	}
}

// TestDetectInsteadConflictsTwoExtensions — два расширения перехватывают
// один и тот же метод через &Вместо -> один конфликт с ОБОИМИ слоями,
// confidence < 1.
func TestDetectInsteadConflictsTwoExtensions(t *testing.T) {
	layerA := domain.ExtensionLayer("extA", 1)
	layerB := domain.ExtensionLayer("extB", 2)
	intercepts := []Intercept{
		{Kind: InterceptInstead, ModulePath: "m.bsl", TargetNameNorm: "рассчитать", Layer: layerA, Confidence: domain.ConfidenceExact},
		{Kind: InterceptInstead, ModulePath: "m.bsl", TargetNameNorm: "рассчитать", Layer: layerB, Confidence: domain.ConfidenceExact},
	}
	conflicts := DetectInsteadConflicts(intercepts)
	if len(conflicts) != 1 {
		t.Fatalf("DetectInsteadConflicts = %d конфликтов, want 1: %+v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.ModulePath != "m.bsl" || c.TargetNameNorm != "рассчитать" {
		t.Errorf("конфликт не на ту цель: %+v", c)
	}
	if len(c.Layers) != 2 || c.Layers[0] != layerA || c.Layers[1] != layerB {
		t.Errorf("Layers = %+v, want оба слоя [%+v %+v]", c.Layers, layerA, layerB)
	}
	if c.Confidence >= domain.ConfidenceExact {
		t.Errorf("confidence = %v, want < 1 (порядок применения не выведен)", c.Confidence)
	}
}

// TestDetectInsteadConflictsGroupsByTargetNotInterceptor — конфликт считается
// по ЦЕЛИ. Два расширения перехватывают ОДИН метод, но зовут свои
// перехватчики по-разному (РасшА_ и РасшБ_ — как в реальных расширениях): по старому
// полю (цель = имя перехватчика) группы бы не совпали и конфликт не нашёлся.
// Факты строятся через DeriveIntercepts, а не руками, — иначе тест проверял
// бы только группировку, но не то, что в неё приходит.
func TestDetectInsteadConflictsGroupsByTargetNotInterceptor(t *testing.T) {
	const modulePath = "Documents/ДопНачисления/Ext/ObjectModule.bsl"
	extA := domain.ExtensionLayer("ext-a", 1)
	element := domain.ExtensionLayer("ext-b", 2)

	a, diagsA := DeriveIntercepts(modulePath, mustParse(t, `
&Вместо("ОбработкаПроведения")
Процедура РасшА_ОбработкаПроведения(Отказ, РежимПроведения)
КонецПроцедуры
`), extA)
	b, diagsB := DeriveIntercepts(modulePath, mustParse(t, `
&Вместо("ОбработкаПроведения")
Процедура РасшБ_ОбработкаПроведения(Отказ, РежимПроведения)
КонецПроцедуры
`), element)
	if len(diagsA)+len(diagsB) != 0 {
		t.Fatalf("неожиданные диагностики: %+v %+v", diagsA, diagsB)
	}
	if a[0].InterceptorNameNorm == b[0].InterceptorNameNorm {
		t.Fatalf("фикстура бессмысленна: имена перехватчиков совпали (%q)", a[0].InterceptorNameNorm)
	}

	conflicts := DetectInsteadConflicts(append(a, b...))
	if len(conflicts) != 1 {
		t.Fatalf("DetectInsteadConflicts = %d конфликтов, want 1: %+v", len(conflicts), conflicts)
	}
	c := conflicts[0]
	if c.TargetNameNorm != "обработкапроведения" || c.ModulePath != modulePath {
		t.Errorf("конфликт не на ту цель: %+v", c)
	}
	if len(c.Layers) != 2 || c.Layers[0] != extA || c.Layers[1] != element {
		t.Errorf("Layers = %+v, want оба слоя [%+v %+v]", c.Layers, extA, element)
	}
	if c.Confidence >= domain.ConfidenceExact {
		t.Errorf("confidence = %v, want < 1 (порядок применения не выведен)", c.Confidence)
	}
}

// TestDetectInsteadConflictsSingleExtensionNoConflict — один &Вместо не
// конфликт; &Перед+&Вместо на один метод от разных слоёв тоже не конфликт
// (конфликтуют только два и более &Вместо).
func TestDetectInsteadConflictsSingleExtensionNoConflict(t *testing.T) {
	layerA := domain.ExtensionLayer("extA", 1)
	layerB := domain.ExtensionLayer("extB", 2)
	intercepts := []Intercept{
		{Kind: InterceptInstead, ModulePath: "m.bsl", TargetNameNorm: "рассчитать", Layer: layerA},
		{Kind: InterceptBefore, ModulePath: "m.bsl", TargetNameNorm: "рассчитать", Layer: layerB},
	}
	if got := DetectInsteadConflicts(intercepts); len(got) != 0 {
		t.Fatalf("DetectInsteadConflicts = %+v, want пусто (только один &Вместо на цель)", got)
	}
}

// TestDetectInsteadConflictsDifferentTargetsNoConflict — два &Вместо на
// РАЗНЫЕ методы не конфликтуют друг с другом.
func TestDetectInsteadConflictsDifferentTargetsNoConflict(t *testing.T) {
	layerA := domain.ExtensionLayer("extA", 1)
	layerB := domain.ExtensionLayer("extB", 2)
	intercepts := []Intercept{
		{Kind: InterceptInstead, ModulePath: "m.bsl", TargetNameNorm: "рассчитать", Layer: layerA},
		{Kind: InterceptInstead, ModulePath: "m.bsl", TargetNameNorm: "провести", Layer: layerB},
	}
	if got := DetectInsteadConflicts(intercepts); len(got) != 0 {
		t.Fatalf("DetectInsteadConflicts = %+v, want пусто (разные цели)", got)
	}
}
