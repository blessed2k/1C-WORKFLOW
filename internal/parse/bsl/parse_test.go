package bsl

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Ожидаемые значения ниже разобраны руками по тексту фикстур: имена, экспортность,
// параметры и фрагменты, которые обязан покрыть span. Ни одно из них не получено
// прогоном парсера.

func TestParseСигнатурыИПараметры(t *testing.T) {
	src := []byte("" +
		"Процедура ПростаяПроцедура()\n" +
		"КонецПроцедуры\n" +
		"\n" +
		"Функция СложнаяФункция(Знач Первый,\n" +
		"\tВторой = Неопределено,\n" +
		"\tТретий = -1.5,\n" +
		"\tЧетвёртый = \"строка с \"\"кавычками\"\"\",\n" +
		"\tПятый = Истина) Экспорт\n" +
		"\tВозврат Пятый;\n" +
		"КонецФункции\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("на корректном модуле не должно быть диагностик, получено %d: %v", len(diags), diags)
	}
	if len(mod.Methods) != 2 {
		t.Fatalf("методов: %d, ожидалось 2", len(mod.Methods))
	}

	first := mod.Methods[0]
	if first.Name != "ПростаяПроцедура" || first.Kind != domain.SymbolProcedure || first.Export {
		t.Errorf("первый метод: имя=%q вид=%q экспорт=%v", first.Name, first.Kind, first.Export)
	}
	if first.NameNorm != "простаяпроцедура" {
		t.Errorf("нормализованное имя: %q", first.NameNorm)
	}
	if len(first.Params) != 0 {
		t.Errorf("у ПростаяПроцедура нет параметров, получено %d", len(first.Params))
	}
	if got := mod.Name(first.Span); got != "Процедура ПростаяПроцедура()\nКонецПроцедуры" {
		t.Errorf("span первого метода покрывает %q", got)
	}
	if first.Region != NoRegion {
		t.Errorf("метод вне областей, получен индекс области %d", first.Region)
	}

	second := mod.Methods[1]
	if second.Name != "СложнаяФункция" || second.Kind != domain.SymbolFunction || !second.Export {
		t.Errorf("второй метод: имя=%q вид=%q экспорт=%v", second.Name, second.Kind, second.Export)
	}
	if got := mod.Name(second.NameSpan); got != "СложнаяФункция" {
		t.Errorf("NameSpan покрывает %q", got)
	}
	// Тело функции — ровно строка «Возврат Пятый;» с окружающими переводами строк.
	if got := mod.Name(second.BodySpan); got != "Возврат Пятый;\n" {
		t.Errorf("BodySpan покрывает %q", got)
	}

	want := []struct {
		name       string
		byVal      bool
		hasDefault bool
		def        string
	}{
		{"Первый", true, false, ""},
		{"Второй", false, true, "Неопределено"},
		{"Третий", false, true, "-1.5"},
		{"Четвёртый", false, true, `"строка с ""кавычками"""`},
		{"Пятый", false, true, "Истина"},
	}
	if len(second.Params) != len(want) {
		t.Fatalf("параметров: %d, ожидалось %d (%+v)", len(second.Params), len(want), second.Params)
	}
	for i, w := range want {
		p := second.Params[i]
		if p.Index != i {
			t.Errorf("параметр %d: Index=%d", i, p.Index)
		}
		if p.Name != w.name || p.ByValue != w.byVal || p.HasDefault != w.hasDefault || p.Default != w.def {
			t.Errorf("параметр %d: %+v, ожидалось %+v", i, p, w)
		}
		if got := mod.Name(p.NameSpan); got != w.name {
			t.Errorf("параметр %d: NameSpan покрывает %q", i, got)
		}
	}
	// Span первого параметра включает Знач.
	if got := mod.Name(second.Params[0].Span); got != "Знач Первый" {
		t.Errorf("span параметра Первый покрывает %q", got)
	}
	// Span параметра со значением по умолчанию доходит до конца значения.
	if got := mod.Name(second.Params[2].Span); got != "Третий = -1.5" {
		t.Errorf("span параметра Третий покрывает %q", got)
	}
}

// Переменные модуля объявляются оператором Перем вне методов; экспортной
// является та переменная, за именем которой стоит Экспорт.
func TestParseПеременныеМодуля(t *testing.T) {
	src := []byte("" +
		"#Область СлужебныеПроцедурыИФункции\n" +
		"Перем КэшНастроек;\n" +
		"Перем СчётчикОшибок, ТекущийПользователь Экспорт;\n" +
		"#КонецОбласти\n" +
		"\n" +
		"Процедура Обработать()\n" +
		"\tПерем Локальная;\n" +
		"\tЛокальная = 1;\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", diags)
	}

	want := []struct {
		name   string
		export bool
		stmt   string
	}{
		{"КэшНастроек", false, "Перем КэшНастроек;"},
		{"СчётчикОшибок", false, "Перем СчётчикОшибок, ТекущийПользователь Экспорт;"},
		{"ТекущийПользователь", true, "Перем СчётчикОшибок, ТекущийПользователь Экспорт;"},
	}
	if len(mod.Variables) != len(want) {
		t.Fatalf("переменных модуля: %d, ожидалось %d (%+v)", len(mod.Variables), len(want), mod.Variables)
	}
	for i, w := range want {
		v := mod.Variables[i]
		if v.Name != w.name || v.Export != w.export {
			t.Errorf("переменная %d: имя=%q экспорт=%v, ожидалось %q/%v", i, v.Name, v.Export, w.name, w.export)
		}
		if got := mod.Name(v.NameSpan); got != w.name {
			t.Errorf("переменная %d: NameSpan покрывает %q", i, got)
		}
		if got := mod.Name(v.Span); got != w.stmt {
			t.Errorf("переменная %d: Span покрывает %q, ожидалось %q", i, got, w.stmt)
		}
		if mod.RegionName(v.Region) != "СлужебныеПроцедурыИФункции" {
			t.Errorf("переменная %d: область %q", i, mod.RegionName(v.Region))
		}
	}
}
