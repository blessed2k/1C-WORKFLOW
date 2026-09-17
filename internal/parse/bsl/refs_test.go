package bsl

import "testing"

// Разобрано руками: в модуле ниже настоящих вызовов ровно четыре, и ни один из них
// не находится в строке, в тексте запроса или в комментарии.
func TestParseЛожныеВызовыИКвалификация(t *testing.T) {
	src := []byte("" +
		"Процедура Обработать()\n" +
		"\t// ОбщегоНазначения.ЛожныйВызовВКомментарии(1);\n" +
		"\tТекст = \"ЛожныйВызовВСтроке(1) и \"\"ЕщёОдин(2)\"\"\";\n" +
		"\tЗапрос.Текст = \"ВЫБРАТЬ\n" +
		"\t\t|\tТаблица.Ссылка КАК Ссылка // ВложенныйЛожныйВызов(3)\n" +
		"\t\t|ИЗ Справочник.Товары КАК Таблица\";\n" +
		"\tЛокальная();\n" +
		"\tОбщегоНазначения.ЗначениеРеквизитаОбъекта(Ссылка, \"Код\");\n" +
		"\tСправочники.Товары.НайтиПоКоду(\"1\");\n" +
		"\tОбъект = Новый Структура(\"А, Б\");\n" +
		"\tЕсли (Истина) Тогда\n" +
		"\t\tВозврат;\n" +
		"\tКонецЕсли;\n" +
		"КонецПроцедуры\n")

	mod, _ := Parse(src, Options{})

	type want struct {
		name string
		qual string
		kind ReferenceKind
	}
	expected := []want{
		{"Локальная", "", RefCall},
		{"ЗначениеРеквизитаОбъекта", "ОбщегоНазначения", RefCall},
		{"НайтиПоКоду", "Товары", RefCall},
		{"Структура", "", RefNew},
	}

	if len(mod.References) != len(expected) {
		t.Fatalf("ссылок: %d, ожидалось %d: %s", len(mod.References), len(expected), refNames(mod))
	}
	for i, w := range expected {
		got := mod.References[i]
		if name := mod.Name(got.Span); name != w.name {
			t.Errorf("ссылка %d: имя %q, ожидалось %q", i, name, w.name)
		}
		if qual := mod.Name(got.QualifierSpan); qual != w.qual {
			t.Errorf("ссылка %d: квалификатор %q, ожидался %q", i, qual, w.qual)
		}
		if got.Kind != w.kind {
			t.Errorf("ссылка %d: вид %q, ожидался %q", i, got.Kind, w.kind)
		}
		if mod.MethodName(got.Method) != "Обработать" {
			t.Errorf("ссылка %d приписана методу %q", i, mod.MethodName(got.Method))
		}
	}
	// Unqualified-вызов не должен получить квалификатор из воздуха.
	if !mod.References[0].QualifierSpan.IsZero() {
		t.Errorf("у Локальная() появился квалификатор: %q", mod.Name(mod.References[0].QualifierSpan))
	}
}

// Код вне методов существует: модуль формы начинается с операторов
// инициализации. Такие ссылки не приписываются никакому методу.
func TestParseСсылкиВнеМетодов(t *testing.T) {
	src := []byte("" +
		"Инициализировать();\n" +
		"\n" +
		"Процедура Обработать()\n" +
		"\tВторая();\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", diags)
	}
	if len(mod.References) != 2 {
		t.Fatalf("ссылок: %s", refNames(mod))
	}
	if mod.References[0].Method != NoMethod {
		t.Errorf("ссылка вне метода приписана методу %d", mod.References[0].Method)
	}
	if mod.MethodName(mod.References[1].Method) != "Обработать" {
		t.Errorf("вторая ссылка приписана методу %q", mod.MethodName(mod.References[1].Method))
	}
}

// Чеклист §12: конструктор «Новый Тип» без скобок — тоже ссылка на тип
// (RefNew), не только форма с круглыми скобками (Новый Структура(...) уже
// закрыт TestParseЛожныеВызовыИКвалификация). До сих пор эта форма была
// накрыта только неявной затравкой fuzz, отдельного golden-теста не было.
func TestParseНовыйБезСкобок(t *testing.T) {
	src := []byte("" +
		"Процедура Создать() Экспорт\n" +
		"\tМас = Новый Массив;\n" +
		"\tВозврат Мас;\n" +
		"КонецПроцедуры\n")

	mod, diags := Parse(src, Options{})
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %v", codes(diags))
	}
	if len(mod.References) != 1 {
		t.Fatalf("ссылок: %s, ожидалась одна", refNames(mod))
	}
	ref := mod.References[0]
	if ref.Kind != RefNew {
		t.Errorf("вид ссылки: %q, ожидался new", ref.Kind)
	}
	if got := mod.Name(ref.Span); got != "Массив" {
		t.Errorf("span ссылки: %q, ожидалось Массив", got)
	}
	if !ref.QualifierSpan.IsZero() {
		t.Errorf("у Новый Массив не должно быть квалификатора: %q", mod.Name(ref.QualifierSpan))
	}
}

func refNames(mod *Module) string {
	out := ""
	for _, r := range mod.References {
		if out != "" {
			out += ", "
		}
		if q := mod.Name(r.QualifierSpan); q != "" {
			out += q + "."
		}
		out += mod.Name(r.Span) + "(" + string(r.Kind) + ")"
	}
	return out
}
