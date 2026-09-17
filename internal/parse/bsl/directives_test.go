package bsl

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Проверяются пункты чеклиста §12: директивы компиляции (включая
// &НаКлиентеНаСервереБезКонтекста), препроцессор #Если/#Область, английский
// синтаксис, Асинх и аннотации расширения. Разметка ниже прочитана по тексту
// фикстуры, а не снята с парсера.
func TestParseДирективыПрепроцессорАсинхИАнглийский(t *testing.T) {
	src := []byte("" +
		"#Область ПрограммныйИнтерфейс\n" +
		"\n" +
		"&НаКлиентеНаСервереБезКонтекста\n" +
		"Функция ПересчитатьСумму(Знач Цена, Знач Количество) Экспорт\n" +
		"\tВозврат Цена * Количество;\n" +
		"КонецФункции\n" +
		"\n" +
		"#Если Сервер Тогда\n" +
		"&НаСервере\n" +
		"Асинх Процедура ЗагрузитьДанные() Экспорт\n" +
		"КонецПроцедуры\n" +
		"#КонецЕсли\n" +
		"\n" +
		"&AtClient\n" +
		"Async Function LoadAsync(Val Source) Export\n" +
		"\tReturn Source;\n" +
		"EndFunction\n" +
		"\n" +
		"#КонецОбласти\n" +
		"\n" +
		"#Область СлужебныеПроцедурыИФункции\n" +
		"&Вместо(\"ЗаполнитьДокумент\")\n" +
		"Процедура Расш1_ЗаполнитьДокумент(Основание)\n" +
		"КонецПроцедуры\n" +
		"#КонецОбласти\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно, получено: %v", diags)
	}

	want := []struct {
		name        string
		kind        domain.SymbolKind
		export      bool
		async       bool
		directive   string
		annotations int
		region      string
		params      int
	}{
		{"ПересчитатьСумму", domain.SymbolFunction, true, false, "&НаКлиентеНаСервереБезКонтекста", 0, "ПрограммныйИнтерфейс", 2},
		{"ЗагрузитьДанные", domain.SymbolProcedure, true, true, "&НаСервере", 0, "ПрограммныйИнтерфейс", 0},
		{"LoadAsync", domain.SymbolFunction, true, true, "&AtClient", 0, "ПрограммныйИнтерфейс", 1},
		{"Расш1_ЗаполнитьДокумент", domain.SymbolProcedure, false, false, "", 1, "СлужебныеПроцедурыИФункции", 1},
	}
	if len(mod.Methods) != len(want) {
		t.Fatalf("методов: %d, ожидалось %d", len(mod.Methods), len(want))
	}
	for i, w := range want {
		m := mod.Methods[i]
		if m.Name != w.name || m.Kind != w.kind || m.Export != w.export || m.Async != w.async {
			t.Errorf("метод %d: имя=%q вид=%q экспорт=%v асинх=%v, ожидалось %+v",
				i, m.Name, m.Kind, m.Export, m.Async, w)
		}
		if m.Directive != w.directive {
			t.Errorf("метод %s: директива %q, ожидалась %q", w.name, m.Directive, w.directive)
		}
		if len(m.Annotations) != w.annotations {
			t.Errorf("метод %s: аннотаций %d (%v), ожидалось %d", w.name, len(m.Annotations), m.Annotations, w.annotations)
		}
		if got := mod.RegionName(m.Region); got != w.region {
			t.Errorf("метод %s: область %q, ожидалась %q", w.name, got, w.region)
		}
		if len(m.Params) != w.params {
			t.Errorf("метод %s: параметров %d, ожидалось %d", w.name, len(m.Params), w.params)
		}
		if !m.Complete {
			t.Errorf("метод %s не закрыт", w.name)
		}
	}

	// Аннотация расширения — не директива компиляции, и она сохраняется
	// отдельно, вместе с аргументом: имя перехватчика и имя цели разные.
	// Длина проверяется Fatalf'ом ДО сравнения: под `if len(...) == 1`
	// сравнение молча пропускалось, и пропажа аннотации из вывода парсера
	// проходила зелёной — проверка, которая не может покраснеть.
	if len(mod.Methods[3].Annotations) != 1 {
		t.Fatalf("аннотаций у метода %s: %d (%v), ожидалась 1", mod.Methods[3].Name,
			len(mod.Methods[3].Annotations), mod.Methods[3].Annotations)
	}
	wantAnnotation := Annotation{Name: "&Вместо", Arg: "ЗаполнитьДокумент", HasArg: true}
	if got := mod.Methods[3].Annotations[0]; got != wantAnnotation {
		t.Errorf("аннотация: %+v, ожидалась %+v", got, wantAnnotation)
	}

	// Две области, обе закрыты; span области покрывает и заголовок, и #КонецОбласти.
	if len(mod.Regions) != 2 {
		t.Fatalf("областей: %d, ожидалось 2", len(mod.Regions))
	}
	for _, r := range mod.Regions {
		if !r.Closed {
			t.Errorf("область %q помечена незакрытой", r.Name)
		}
		text := mod.Name(r.Span)
		if len(text) < len("#Область") || text[:len("#Область")] != "#Область" {
			t.Errorf("span области %q начинается не с #Область: %q", r.Name, text)
		}
		if got := mod.Name(r.HeaderSpan); got != "#Область "+r.Name {
			t.Errorf("HeaderSpan области: %q", got)
		}
	}
	if mod.Regions[1].NameNorm != "служебныепроцедурыифункции" {
		t.Errorf("нормализованное имя области: %q", mod.Regions[1].NameNorm)
	}

	// Шесть строк препроцессора, каждая опознана видом.
	wantPre := []PreprocKind{
		PreprocRegion, PreprocIf, PreprocEndIf, PreprocEndRegion, PreprocRegion, PreprocEndRegion,
	}
	if len(mod.Preprocs) != len(wantPre) {
		t.Fatalf("строк препроцессора: %d, ожидалось %d (%+v)", len(mod.Preprocs), len(wantPre), mod.Preprocs)
	}
	for i, w := range wantPre {
		if mod.Preprocs[i].Kind != w {
			t.Errorf("строка препроцессора %d: вид %q, ожидался %q", i, mod.Preprocs[i].Kind, w)
		}
	}
	if got := mod.Name(mod.Preprocs[1].TextSpan); got != "Сервер Тогда" {
		t.Errorf("условие #Если: %q", got)
	}
}

// Чеклист §12 архитектуры требует, чтобы директива компиляции не терялась
// НИ У ОДНОЙ из них: TestParseДирективыПрепроцессорАсинхИАнглийский проверяет
// общий механизм разбора &Слово на трёх примерах, здесь — по одному golden-кейсу
// на каждое написание из чеклиста, включая английские синонимы.
func TestParseКаждаяДирективаКомпиляцииОтдельно(t *testing.T) {
	cases := []string{
		"&НаСервере",
		"&НаКлиенте",
		"&НаСервереБезКонтекста",
		"&НаКлиентеНаСервере",
		"&НаКлиентеНаСервереБезКонтекста",
		"&AtServer",
		"&AtClient",
		"&AtServerNoContext",
		"&AtClientAtServer",
		"&AtClientAtServerNoContext",
	}
	for _, directive := range cases {
		t.Run(directive, func(t *testing.T) {
			src := []byte(directive + "\nПроцедура П() Экспорт\nКонецПроцедуры\n")
			mod, diags := Parse(src, Options{})
			if len(diags) != 0 {
				t.Fatalf("диагностик быть не должно: %v", codes(diags))
			}
			if len(mod.Methods) != 1 {
				t.Fatalf("методов: %d, ожидался 1", len(mod.Methods))
			}
			m := mod.Methods[0]
			if m.Directive != directive {
				t.Errorf("директива: %q, ожидалась %q", m.Directive, directive)
			}
			if !m.Complete || !m.Export {
				t.Errorf("метод: Complete=%v Export=%v, ожидались true/true", m.Complete, m.Export)
			}
			if got := mod.Name(m.Span); got[:len(directive)] != directive {
				t.Errorf("Span метода не начинается с директивы: %q", got)
			}
		})
	}
}

// Незакрытая область даёт диагностику с кодом bsl_unclosed_region,
// а методы модуля при этом не теряются.
func TestParseНезакрытаяОбласть(t *testing.T) {
	src := []byte("" +
		"#Область ПрограммныйИнтерфейс\n" +
		"Процедура А() Экспорт\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(mod.Methods) != 1 || mod.Methods[0].Name != "А" {
		t.Fatalf("методы: %+v", mod.Methods)
	}
	if !hasCode(diags, DiagUnclosedRegion) {
		t.Fatalf("ожидалась диагностика %s, получено %+v", DiagUnclosedRegion, diags)
	}
	if mod.Regions[0].Closed {
		t.Error("область не закрыта, Closed должен быть false")
	}
}

// hasCode сообщает, есть ли среди диагностик указанный код.
// Диагностика опознаётся только кодом: формулировка сообщения не контракт.
func hasCode(diags []domain.Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

// codes возвращает коды диагностик в порядке выдачи.
func codes(diags []domain.Diagnostic) []string {
	out := make([]string, 0, len(diags))
	for _, d := range diags {
		out = append(out, d.Code)
	}
	return out
}
