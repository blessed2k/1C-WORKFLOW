package app

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const apiDraftFixture = `&НаСервере
Процедура ПриСозданииНаСервере(Отказ, СтандартнаяОбработка)
КонецПроцедуры

// ++ Иванов 01.02.2026
// Делит строку на части по разделителю.
//
// Параметры:
//  Значение - Строка - что делить.
&НаСервереБезКонтекста
Функция РазделитьСтроку(Значение)
	Возврат Новый Массив;
КонецФункции

Процедура РазбитьСтрокуЗаказа(Заказ)
КонецПроцедуры

Асинх Процедура СтрокуВывести ()
КонецПроцедуры

&Вместо("РазбитьСтрокуЗаказа")
Процедура Расш1_РазбитьСтрокуЗаказа(Заказ)
КонецПроцедуры

&НаКлиенте Процедура Заполнить(Команда)
КонецПроцедуры
`

// TestDraftMethods: в черновике находятся объявления процедур и функций со
// строкой объявления и первой строкой комментария; директива между
// комментарием и объявлением комментарий не отрывает, маркер авторства
// первой строкой не считается, аннотация перехвата замечена.
func TestDraftMethods(t *testing.T) {
	got := draftMethods(apiDraftFixture)
	want := []draftMethod{
		{name: "ПриСозданииНаСервере", line: 2, params: "Отказ, СтандартнаяОбработка"},
		{name: "РазделитьСтроку", line: 11, comment: "Делит строку на части по разделителю.", params: "Значение"},
		{name: "РазбитьСтрокуЗаказа", line: 15, params: "Заказ"},
		{name: "СтрокуВывести", line: 18},
		{name: "Расш1_РазбитьСтрокуЗаказа", line: 22, params: "Заказ", intercept: true},
		{name: "Заполнить", line: 25, params: "Команда"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("методы черновика = %+v, want %+v", got, want)
	}
}

// TestDraftIsHandler: обработчик опознаётся по первому слову имени целиком, а
// не по началу строки: функции, чьё имя лишь начинается теми же буквами,
// проверяются.
func TestDraftIsHandler(t *testing.T) {
	for _, tc := range []struct {
		method draftMethod
		want   bool
	}{
		{draftMethod{name: "ПриСозданииНаСервере"}, true},
		{draftMethod{name: "ОбработкаПроведения"}, true},
		{draftMethod{name: "ПередЗаписью"}, true},
		{draftMethod{name: "ПослеЗаписиНаСервере"}, true},
		{draftMethod{name: "Подключаемый_ВыполнитьКоманду"}, true},
		{draftMethod{name: "ТоварыПриИзменении"}, true},
		{draftMethod{name: "ВыборФайлаЗавершение"}, true},
		{draftMethod{name: "Заполнить", params: "Команда"}, true},
		{draftMethod{name: "Расш1_ПровестиДокумент", intercept: true}, true},
		{draftMethod{name: "OnOpen"}, true},
		{draftMethod{name: "ПриведениеТипа"}, false},
		{draftMethod{name: "ПрименитьОтбор"}, false},
		{draftMethod{name: "ПередатьДанные"}, false},
		{draftMethod{name: "ПоследнийДеньМесяца"}, false},
		{draftMethod{name: "ПоследовательностьДокументов"}, false},
		{draftMethod{name: "КомандаПечати"}, false},
		{draftMethod{name: "OnlyDigits"}, false},
		{draftMethod{name: "Заполнить", params: "Команда, Отказ"}, false},
		{draftMethod{name: "РазделитьСтроку"}, false},
	} {
		if got := draftIsHandler(tc.method); got != tc.want {
			t.Errorf("draftIsHandler(%+v) = %v, want %v", tc.method, got, tc.want)
		}
	}
}

func readyCalls(items []APIBriefItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Call
	}
	return out
}

func readyReport(t *testing.T, p *Projects, in ReadyMethodsInput) ReadyMethodsReport {
	t.Helper()
	resp, err := NewAPIService(p).ReadyMethods(context.Background(), in)
	if err != nil || len(resp.Items) != 1 {
		t.Fatalf("ReadyMethods: %+v, %v", resp.Items, err)
	}
	return resp.Items[0]
}

// TestReadyMethods: для функции черновика называются методы библиотеки по
// словам её имени и комментария; устаревший метод не называется; прикладной
// метод называется только при близком имени; обработчики событий,
// перехватчики и обработчики команд не проверяются.
func TestReadyMethods(t *testing.T) {
	p := newAPIFixtureProject(t, "api-draft")
	rep := readyReport(t, p, ReadyMethodsInput{Code: apiDraftFixture})
	if rep.Declared != 6 || rep.Skipped != 3 || rep.Checked != 3 || rep.NotChecked != 0 {
		t.Errorf("объявлено %d, пропущено %d, проверено %d, сверх потолка %d; want 6, 3, 3, 0",
			rep.Declared, rep.Skipped, rep.Checked, rep.NotChecked)
	}
	got := map[string][]string{}
	for _, it := range rep.Methods {
		got[it.Name] = readyCalls(it.Candidates)
		if it.Line == 0 {
			t.Errorf("%s: строка объявления не названа", it.Name)
		}
		for _, c := range it.Candidates {
			if c.UID == "" || strings.HasSuffix(c.Call, ".РазбитьСтроку") {
				t.Errorf("%s: кандидат %+v (без uid или устаревший)", it.Name, c)
			}
		}
	}
	want := map[string][]string{
		// Слова комментария нашли метод библиотеки; у прикладного метода
		// общее с именем функции одно слово, он не назван.
		"РазделитьСтроку": {"СтроковыеУтилиты.РазложитьСтрокуВМассив"},
		// У библиотечного метода с именем функции общее одно слово из трёх:
		// он не назван. Прикладной метод с тем же именем назван.
		"РазбитьСтрокуЗаказа": {"ПродажиВызовСервера.РазбитьСтрокуЗаказа"},
		// СтрокуВывести: с готовыми методами общее одно слово, не назван никто.
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("готовые методы = %q, want %q", got, want)
	}
}

// TestReadyMethodsSkipsOwnModule: метод модуля, которому принадлежит
// черновик, кандидатом не называется: иначе записанный и уже
// проиндексированный метод находил бы сам себя.
func TestReadyMethodsSkipsOwnModule(t *testing.T) {
	p := newAPIFixtureProject(t, "api-draft-own")
	const code = "Процедура РазбитьСтрокуЗаказа(Заказ, Количество) Экспорт\nКонецПроцедуры\n"
	if rep := readyReport(t, p, ReadyMethodsInput{Code: code}); len(rep.Methods) != 1 {
		t.Fatalf("без модуля: %+v, want сам метод кандидатом", rep.Methods)
	}
	for _, module := range []string{"ПродажиВызовСервера", "CommonModule.продаживызовсервера"} {
		if rep := readyReport(t, p, ReadyMethodsInput{Code: code, Module: module}); len(rep.Methods) != 0 || rep.Checked != 1 {
			t.Errorf("модуль %q: %+v, проверено %d; want без кандидатов", module, rep.Methods, rep.Checked)
		}
	}
}

// TestReadyMethodsSkipsOneWordNames: по одному слову готовый метод не
// ищется: имя из одного слова без комментария пропускается, с комментарием
// проверяется.
func TestReadyMethodsSkipsOneWordNames(t *testing.T) {
	p := newAPIFixtureProject(t, "api-draft-word")
	rep := readyReport(t, p, ReadyMethodsInput{Code: "Функция Строка()\nКонецФункции\n"})
	if rep.Declared != 1 || rep.Skipped != 1 || rep.Checked != 0 || len(rep.Methods) != 0 {
		t.Errorf("имя из одного слова: %+v", rep)
	}
	rep = readyReport(t, p, ReadyMethodsInput{Code: "// Разбивает строку по разделителю.\nФункция Разбор(Значение)\nКонецФункции\n"})
	if rep.Checked != 1 || len(rep.Methods) != 1 {
		t.Errorf("имя из одного слова с комментарием: %+v", rep)
	}
}

// TestReadyMethodsLimit: модуль длиннее потолка проверяется по первым
// методам, и ответ говорит, сколько осталось.
func TestReadyMethodsLimit(t *testing.T) {
	p := newAPIFixtureProject(t, "api-draft-limit")
	var b strings.Builder
	for i := 0; i < apiDraftMethodsLimit+4; i++ {
		b.WriteString("Функция РазбитьСтрокуНаЧасти" + string(rune('А'+i)) + "(Значение)\nКонецФункции\n")
	}
	rep := readyReport(t, p, ReadyMethodsInput{Code: b.String()})
	if rep.Declared != apiDraftMethodsLimit+4 || rep.Checked != apiDraftMethodsLimit || rep.NotChecked != 4 {
		t.Errorf("объявлено %d, проверено %d, сверх потолка %d", rep.Declared, rep.Checked, rep.NotChecked)
	}
}

// TestReadyMethodsWithoutDeclarations: в тексте без объявлений проверять
// нечего; это не ошибка, и активный проект ради этого не нужен.
func TestReadyMethodsWithoutDeclarations(t *testing.T) {
	resp, err := NewAPIService(NewProjectsUnavailable("", nil)).ReadyMethods(context.Background(), ReadyMethodsInput{Code: "Сообщить(1);\n"})
	if err != nil || len(resp.Items) != 1 || resp.Items[0].Declared != 0 || len(resp.Items[0].Methods) != 0 {
		t.Errorf("ReadyMethods = %+v, ошибка %v", resp.Items, err)
	}
}

// TestReadyMethodsWithoutLibrary: в конфигурации без библиотеки проверка
// работает по прикладным методам и предупреждает, что библиотеки нет.
func TestReadyMethodsWithoutLibrary(t *testing.T) {
	p := apiRankFixture(t, "draft-nolib", [][3]string{{"ЗначениеРеквизитаОбъекта", "Возвращает значение реквизита.", ""}})
	resp, err := NewAPIService(p).ReadyMethods(context.Background(), ReadyMethodsInput{Code: "Функция ЗначениеРеквизитаОбъекта(Ссылка)\nКонецФункции\n"})
	if err != nil {
		t.Fatalf("ReadyMethods: %v", err)
	}
	rep := resp.Items[0]
	if len(rep.Methods) != 1 || rep.Methods[0].Candidates[0].Call != "Прочее.ЗначениеРеквизитаОбъекта" || !hasWarning(resp.Warnings, "bsp_library_not_found") {
		t.Errorf("без библиотеки: %+v, предупреждения %+v", rep.Methods, resp.Warnings)
	}
}

// TestReadyForTask: по тексту задачи называются методы, у которых не меньше
// двух слов задачи стоят в имени, имени модуля или первой строке описания;
// устаревший не называется; задача из одного слова ничего не ищет.
func TestReadyForTask(t *testing.T) {
	p := newAPIFixtureProject(t, "api-task")
	svc := NewAPIService(p)
	resp, err := svc.ReadyForTask(context.Background(), "в форме заказа надо разбить строку заказа по количеству")
	if err != nil {
		t.Fatalf("ReadyForTask: %v", err)
	}
	var got []string
	for _, it := range resp.Items {
		got = append(got, it.Section+":"+it.Call)
		if it.UID == "" {
			t.Errorf("%s: нет uid", it.Call)
		}
	}
	// Метод библиотеки РазложитьСтрокуВМассив совпал с задачей только словом
	// «строку» в сильных полях и не назван.
	want := []string{"other:ПродажиВызовСервера.РазбитьСтрокуЗаказа"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("готовые методы по задаче = %q, want %q", got, want)
	}
	for _, task := range []string{"строку", "или для при"} {
		resp, err := svc.ReadyForTask(context.Background(), task)
		if err != nil || len(resp.Items) != 0 {
			t.Errorf("задача %q: %+v, %v; want пусто без ошибки", task, resp.Items, err)
		}
	}
}

// TestAPIServicesShareWordIndex: сервисы одного резолвера проектов делят
// индекс слов: find_api, validate_bsl и get_context_for_task держат каждый
// свой сервис, а индекс на проект один.
func TestAPIServicesShareWordIndex(t *testing.T) {
	p := newAPIFixtureProject(t, "api-shared")
	a, b := NewAPIService(p), NewAPIService(p)
	if _, err := a.FindAPI(context.Background(), FindAPIInput{Query: "разбить строку"}); err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	if a.indexes != b.indexes || len(b.indexes.indexes) != 1 {
		t.Errorf("сервисы держат разные индексы слов: %p и %p, проектов в кэше %d", a.indexes, b.indexes, len(b.indexes.indexes))
	}
}
