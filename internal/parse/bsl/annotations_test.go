package bsl

import "testing"

// Аргумент аннотации перехвата: у метода расширения имя
// перехватчика и имя цели разные, и парсер обязан донести оба. Ожидаемые
// значения прочитаны по тексту фикстуры, а не сняты с парсера.
func TestParseАргументАннотацииПерехвата(t *testing.T) {
	src := []byte("" +
		"&Вместо(\"ОбработкаПроведения\")\n" +
		"Процедура РасшБ_ОбработкаПроведения(Отказ, Режим)\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно, получено: %v", diags)
	}
	if len(mod.Methods) != 1 {
		t.Fatalf("методов: %d, ожидался 1", len(mod.Methods))
	}
	m := mod.Methods[0]
	if m.Name != "РасшБ_ОбработкаПроведения" {
		t.Fatalf("имя метода: %q", m.Name)
	}
	if len(m.Annotations) != 1 {
		t.Fatalf("аннотаций: %d (%+v), ожидалась 1", len(m.Annotations), m.Annotations)
	}
	want := Annotation{Name: "&Вместо", Arg: "ОбработкаПроведения", HasArg: true}
	if m.Annotations[0] != want {
		t.Errorf("аннотация: %+v, ожидалась %+v", m.Annotations[0], want)
	}
}

// Все четыре вида аннотаций и их английские синонимы разбираются одинаково.
// Виды перечислены по таблице isExtensionAnnotation, ожидаемые
// аргументы — по тексту фикстуры.
func TestParseВсеВидыАннотацийИАнглийскиеСинонимы(t *testing.T) {
	src := []byte("" +
		"&Перед(\"ПередЗаписью\")\n" +
		"Процедура Расш_ПередЗаписью()\nКонецПроцедуры\n" +
		"&После(\"ПриЗаписи\")\n" +
		"Процедура Расш_ПриЗаписи()\nКонецПроцедуры\n" +
		"&Вместо(\"ОбработкаПроведения\")\n" +
		"Процедура Расш_ОбработкаПроведения()\nКонецПроцедуры\n" +
		"&ИзменениеИКонтроль(\"ЗаполнитьШапку\")\n" +
		"Процедура Расш_ЗаполнитьШапку()\nКонецПроцедуры\n" +
		"&Before(\"BeforeWrite\")\n" +
		"Procedure Ext_BeforeWrite()\nEndProcedure\n" +
		"&After(\"OnWrite\")\n" +
		"Procedure Ext_OnWrite()\nEndProcedure\n" +
		"&Around(\"Posting\")\n" +
		"Procedure Ext_Posting()\nEndProcedure\n" +
		"&ChangeAndValidate(\"FillHeader\")\n" +
		"Procedure Ext_FillHeader()\nEndProcedure\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно, получено: %v", diags)
	}
	want := []Annotation{
		{"&Перед", "ПередЗаписью", true},
		{"&После", "ПриЗаписи", true},
		{"&Вместо", "ОбработкаПроведения", true},
		{"&ИзменениеИКонтроль", "ЗаполнитьШапку", true},
		{"&Before", "BeforeWrite", true},
		{"&After", "OnWrite", true},
		{"&Around", "Posting", true},
		{"&ChangeAndValidate", "FillHeader", true},
	}
	if len(mod.Methods) != len(want) {
		t.Fatalf("методов: %d, ожидалось %d", len(mod.Methods), len(want))
	}
	for i, w := range want {
		m := mod.Methods[i]
		if m.Directive != "" {
			t.Errorf("метод %s: аннотация утекла в директиву %q", m.Name, m.Directive)
		}
		if len(m.Annotations) != 1 {
			t.Fatalf("метод %s: аннотаций %d (%+v), ожидалась 1", m.Name, len(m.Annotations), m.Annotations)
		}
		if got := m.Annotations[0]; got != w {
			t.Errorf("метод %s: аннотация %+v, ожидалась %+v", m.Name, got, w)
		}
	}
}

// Директива компиляции рядом с аннотацией и несколько аннотаций на одном
// методе. Отсутствие аргумента у &НаСервере ошибкой не считается:
// это вообще не аннотация расширения.
func TestParseДирективаРядомСАннотациямиИПорядок(t *testing.T) {
	src := []byte("" +
		"&НаСервере\n" +
		"&После(\"ЗаполнитьТабличнуюЧасть\")\n" +
		"Процедура Расш_ЗаполнитьТабличнуюЧасть(Товары)\nКонецПроцедуры\n" +
		"&Перед(\"ПередЗаписью\")\n" +
		"&После(\"ПередЗаписью\")\n" +
		"&НаКлиенте\n" +
		"Процедура Расш_ПередЗаписью(Отказ)\nКонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно, получено: %v", diags)
	}
	if len(mod.Methods) != 2 {
		t.Fatalf("методов: %d, ожидалось 2", len(mod.Methods))
	}

	first := mod.Methods[0]
	if first.Directive != "&НаСервере" {
		t.Errorf("первый метод: директива %q, ожидалась &НаСервере", first.Directive)
	}
	wantFirst := []Annotation{{"&После", "ЗаполнитьТабличнуюЧасть", true}}
	if len(first.Annotations) != 1 || first.Annotations[0] != wantFirst[0] {
		t.Errorf("первый метод: аннотации %+v, ожидались %+v", first.Annotations, wantFirst)
	}

	// Две аннотации одного метода: порядок исходника, у каждой свой аргумент.
	second := mod.Methods[1]
	if second.Directive != "&НаКлиенте" {
		t.Errorf("второй метод: директива %q, ожидалась &НаКлиенте", second.Directive)
	}
	wantSecond := []Annotation{
		{"&Перед", "ПередЗаписью", true},
		{"&После", "ПередЗаписью", true},
	}
	if len(second.Annotations) != len(wantSecond) {
		t.Fatalf("второй метод: аннотаций %d (%+v), ожидалось %d",
			len(second.Annotations), second.Annotations, len(wantSecond))
	}
	for i := range wantSecond {
		if second.Annotations[i] != wantSecond[i] {
			t.Errorf("второй метод, аннотация %d: %+v, ожидалась %+v",
				i, second.Annotations[i], wantSecond[i])
		}
	}
}

// Битый аргумент аннотации: парсер не паникует, имя метода не съедено,
// аргумент либо отсутствует, либо сопровождён честной диагностикой. Ожидания
// прочитаны по тексту фикстур, не сняты с парсера.
func TestParseБитыйАргументАннотации(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		methodName string
		annotation Annotation
		wantDiag   bool
	}{
		{
			name:       "пустые скобки",
			src:        "&Вместо()\nПроцедура Расш_П()\nКонецПроцедуры\n",
			methodName: "Расш_П",
			annotation: Annotation{"&Вместо", "", true},
		},
		{
			name:       "скобка не закрыта",
			src:        "&Вместо(\nПроцедура Расш_П()\nКонецПроцедуры\n",
			methodName: "Расш_П",
			annotation: Annotation{"&Вместо", "", true},
			wantDiag:   true,
		},
		{
			name:       "аргумент число",
			src:        "&Вместо(123)\nПроцедура Расш_П()\nКонецПроцедуры\n",
			methodName: "Расш_П",
			annotation: Annotation{"&Вместо", "", true},
			wantDiag:   true,
		},
		{
			name:       "скобок нет вовсе",
			src:        "&Вместо\nПроцедура Расш_П()\nКонецПроцедуры\n",
			methodName: "Расш_П",
			annotation: Annotation{"&Вместо", "", false},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mod, diags := Parse([]byte(c.src), Options{})
			if len(mod.Methods) != 1 {
				t.Fatalf("методов: %d, ожидался 1", len(mod.Methods))
			}
			m := mod.Methods[0]
			if m.Name != c.methodName {
				t.Errorf("имя метода %q, ожидалось %q — имя съедено разбором аннотации", m.Name, c.methodName)
			}
			if len(m.Annotations) != 1 {
				t.Fatalf("аннотаций %d (%+v), ожидалась 1", len(m.Annotations), m.Annotations)
			}
			if m.Annotations[0] != c.annotation {
				t.Errorf("аннотация %+v, ожидалась %+v", m.Annotations[0], c.annotation)
			}
			gotDiag := false
			for _, d := range diags {
				if d.Code == DiagBadAnnotationArgument {
					gotDiag = true
				}
			}
			if gotDiag != c.wantDiag {
				t.Errorf("диагностика %s: %v, ожидалось %v (все: %v)", DiagBadAnnotationArgument, gotDiag, c.wantDiag, diags)
			}
		})
	}
}

// Аннотация без следующего за ней метода: не паника и не выдуманный метод.
func TestParseАннотацияБезМетода(t *testing.T) {
	for _, src := range []string{
		"&Вместо(\"ОбработкаПроведения\")\n",
		"&Вместо(\"ОбработкаПроведения\")",
		"&Вместо(",
		"&Вместо",
	} {
		mod, _ := Parse([]byte(src), Options{})
		if len(mod.Methods) != 0 {
			t.Errorf("вход %q: методов %d, ожидалось 0", src, len(mod.Methods))
		}
	}
}
