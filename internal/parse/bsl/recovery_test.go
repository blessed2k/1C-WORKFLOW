package bsl

import (
	"strings"
	"testing"
)

// Чеклист §12: сломанный или неполный модуль — error recovery без потери
// остальных методов. Разобрано руками: в модуле три объявления, у первого нет
// КонецПроцедуры, у второго не закрыта строка, третий корректен и обязан дожить
// до результата.
func TestParseВосстановлениеНаСломанномМодуле(t *testing.T) {
	src := []byte("" +
		"Процедура БезЗакрытия()\n" +
		"\tА = 1;\n" +
		"\n" +
		"Функция СНезакрытойСтрокой() Экспорт\n" +
		"\tБ = \"оборвана;\n" +
		"\tВозврат Б;\n" +
		"КонецФункции\n" +
		"\n" +
		"Процедура Целая(Парам) Экспорт\n" +
		"\tВыполнить(Парам);\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})

	names := make([]string, 0, len(mod.Methods))
	for _, m := range mod.Methods {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "БезЗакрытия,СНезакрытойСтрокой,Целая" {
		t.Fatalf("методы: %v — потерян хотя бы один", names)
	}
	if mod.Methods[0].Complete {
		t.Error("БезЗакрытия не закрыта, Complete должен быть false")
	}
	if !mod.Methods[2].Complete || !mod.Methods[2].Export {
		t.Error("последний метод должен быть целым и экспортным")
	}
	if len(mod.Methods[2].Params) != 1 || mod.Methods[2].Params[0].Name != "Парам" {
		t.Errorf("параметры последнего метода: %+v", mod.Methods[2].Params)
	}
	// Диагностики сверяются по кодам, не по формулировкам.
	if !hasCode(diags, DiagUnclosedMethod) || !hasCode(diags, DiagUnclosedString) {
		t.Errorf("обе поломки должны быть в диагностиках, получено %v", codes(diags))
	}
}

// Метод, оборвавшийся на конце файла, тоже попадает в выдачу — с диагностикой
// и со span, дотянутым до конца файла.
func TestParseМетодОборванныйКонцомФайла(t *testing.T) {
	src := []byte("Процедура Целая()\nКонецПроцедуры\n\nПроцедура Оборванная(А)\n\tБ = А;\n")

	mod, diags := Parse(src, Options{})
	if len(mod.Methods) != 2 {
		t.Fatalf("методов: %d, ожидалось 2", len(mod.Methods))
	}
	if !mod.Methods[0].Complete {
		t.Error("первый метод закрыт и не должен быть помечен незакрытым")
	}
	last := mod.Methods[1]
	if last.Complete {
		t.Error("последний метод не закрыт")
	}
	if last.Span.EndByte != len(src) {
		t.Errorf("span оборванного метода кончается на %d, длина файла %d", last.Span.EndByte, len(src))
	}
	if !hasCode(diags, DiagUnclosedMethod) {
		t.Errorf("ожидалась диагностика %s, получено %v", DiagUnclosedMethod, codes(diags))
	}
}

// Инвариант §18.3 на дефектном входе: любой span остаётся внутри границ файла
// и его текст извлекается.
func TestSpansВГраницахНаДефектномВходе(t *testing.T) {
	defective := []string{
		"",
		"\xef\xbb\xbfПроцедура",
		"Процедура (",
		"Функция Ф(Знач",
		"\"незакрытая строка",
		"'20240101",
		"#Если\n#КонецЕсли",
		"&НаКлиенте\n&НаСервере\n",
		"Процедура А() Экспорт Экспорт\nКонецПроцедуры",
		"Перем",
		"Перем ;",
		strings.Repeat("Процедура П()\n", 50),
	}
	for _, s := range defective {
		src := []byte(s)
		mod, diags := Parse(src, Options{File: "CommonModules/Х/Ext/Module.bsl"})
		checkSpans(t, src, mod, diags, s)
	}
}
