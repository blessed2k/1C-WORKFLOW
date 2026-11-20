package validate

import (
	"strings"
	"testing"
)

// platform knows the global-context methods the cases below call, with the
// counts syntax.Arity reads from the reference.
func platform(name string) (Method, bool) {
	switch strings.ToLower(name) {
	case "значениезаполнено":
		return Method{Signature: "ЗначениеЗаполнено(<Значение>)", Required: 1, Total: 1}, true
	case "конецмесяца":
		return Method{Signature: "КонецМесяца(<Дата>)", Required: 1, Total: 1}, true
	case "день":
		return Method{Signature: "День(<Дата>)", Required: 1, Total: 1}, true
	case "деньгода":
		return Method{Signature: "ДеньГода(<Дата>)", Required: 1, Total: 1}, true
	case "дата":
		return Method{Signature: "Дата(...)", Unknown: true}, true
	case "стршаблон":
		return Method{Signature: "СтрШаблон(<Шаблон>, <Значение1-Значение10>)", Required: 1, Total: 11}, true
	case "мин":
		return Method{Signature: "Мин(<Значение1,...,ЗначениеN>)", Required: 1, Variadic: true}, true
	case "сообщить":
		return Method{Signature: "Сообщить(<ТекстСообщения>, <Статус>)", Required: 1, Total: 2}, true
	case "тип":
		return Method{Signature: "Тип(<ИмяТипа>)", Required: 1, Total: 1}, true
	case "типзнч":
		return Method{Signature: "ТипЗнч(<Значение>)", Required: 1, Total: 1}, true
	}
	return Method{}, false
}

func platformNames() []string {
	return []string{"День", "ДеньГода", "КонецМесяца", "СтрШаблон"}
}

// globalProperty: Документы and Метаданные live on the server only, as in the
// reference; ПараметрЗапуска on the client.
func globalProperty(name string) (Availability, bool) {
	switch strings.ToLower(name) {
	case "документы", "метаданные", "справочники":
		return Availability{Server: true}, true
	case "параметрзапуска":
		return Availability{Client: true}, true
	}
	return Availability{}, false
}

type fakeProject struct {
	modules  map[string]CommonModule
	globals  map[string]bool
	complete bool
}

func (f fakeProject) CommonModule(n string) (CommonModule, bool) {
	m, ok := f.modules[strings.ToLower(n)]
	return m, ok
}
func (f fakeProject) GlobalExport(n string) bool { return f.globals[strings.ToLower(n)] }
func (f fakeProject) Complete() bool             { return f.complete }

func methods(export bool, names ...string) map[string]MethodInfo {
	out := map[string]MethodInfo{}
	for _, n := range names {
		out[strings.ToLower(n)] = MethodInfo{Name: n, Export: export}
	}
	return out
}

func merge(ms ...map[string]MethodInfo) map[string]MethodInfo {
	out := map[string]MethodInfo{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

var server = Availability{Server: true}

func project() fakeProject {
	return fakeProject{
		complete: true,
		globals:  map[string]bool{"глобальнаяфункция": true},
		modules: map[string]CommonModule{
			"общегоназначения": {Name: "ОбщегоНазначения", Complete: true, Availability: server, Methods: merge(
				methods(true, "ЗначениеРеквизитаОбъектов", "ЗначенияРеквизитовОбъекта", "ЗначениеРеквизитаОбъекта", "СообщитьПользователю"),
				methods(false, "СлужебнаяПроцедура"))},
			"пользователи":  {Name: "Пользователи", Complete: true, Availability: server, Methods: methods(true, "ТекущийПользователь")},
			"интеграцияcrm": {Name: "ИнтеграцияCRM", Complete: true, Availability: server, Methods: methods(true, "Включена")},
			"общегоназначенияклиент": {Name: "ОбщегоНазначенияКлиент", Complete: true, Availability: Availability{Client: true},
				Methods: methods(true, "СообщитьПользователю")},
			"заимствованный": {Name: "Заимствованный", Complete: false, Availability: server, Methods: methods(true, "ДобавленВРасширении")},
		},
	}
}

func env(mc *ModuleContext) Env {
	return Env{
		Method:            platform,
		GlobalProperty:    globalProperty,
		Project:           project(),
		Module:            mc,
		GlobalMethodNames: platformNames,
	}
}

// commonModule: a server common module whose other methods were read.
func commonModule(name string, others ...string) *ModuleContext {
	mc := &ModuleContext{Kind: KindCommon, Display: name, Default: server, ContextComplete: true, Methods: map[string]bool{}}
	for _, o := range others {
		mc.Methods[strings.ToLower(o)] = true
	}
	return mc
}

func formModule(attrs ...string) *ModuleContext {
	mc := &ModuleContext{Kind: KindForm, Display: "Обработка.Тест.Форма.Форма", Default: server, ContextComplete: true,
		Methods: map[string]bool{}, ContextNames: map[string]bool{"элементы": true, "закрыть": true}}
	for _, a := range attrs {
		mc.ContextNames[strings.ToLower(a)] = true
	}
	return mc
}

type want struct {
	code     string
	severity string
	line     int
}

func check(t *testing.T, name string, code string, e Env, wants ...want) []Finding {
	t.Helper()
	fs := Check(code, e)
	if len(fs) != len(wants) {
		t.Errorf("%s: want %d findings, got %d: %+v", name, len(wants), len(fs), fs)
		return fs
	}
	for i, w := range wants {
		f := fs[i]
		if f.Code != w.code || f.Severity != w.severity || (w.line != 0 && f.Line != w.line) {
			t.Errorf("%s: finding %d = %s/%s line %d, want %s/%s line %d (%s)", name, i, f.Code, f.Severity, f.Line,
				w.code, w.severity, w.line, f.Message)
		}
	}
	return fs
}

// TestUnknownGlobalFunction: ДеньМесяца does not exist in the platform; the
// module compiled nowhere and the check of the merge request failed.
func TestUnknownGlobalFunction(t *testing.T) {
	fs := check(t, "ДеньМесяца", `
Функция ДатаИзОтвета(НомерГода, НомерМесяца, НомерДня)
	Если НомерДня > ДеньМесяца(КонецМесяца(Дата(НомерГода, НомерМесяца, 1))) Тогда
		Возврат Неопределено;
	КонецЕсли;
КонецФункции`, env(commonModule("РаспознаваниеСчетов")), want{"UnknownGlobalFunction", "high", 3})
	if len(fs) == 1 && !strings.Contains(fs[0].Suggestion, "День") {
		t.Errorf("the suggestion must name the platform's День: %q", fs[0].Suggestion)
	}

	// Without the module's own methods a call may be a sibling procedure
	// outside the draft: the finding is said, softly.
	check(t, "module unknown", `
Процедура Тест()
	ВспомогательнаяПроцедура();
КонецПроцедуры`, env(&ModuleContext{Kind: KindUnknown}), want{"UnknownGlobalFunction", "medium", 3})

	// Without the configuration the global modules are not known: silence.
	e := env(commonModule("Модуль"))
	p := project()
	p.complete = false
	e.Project = p
	check(t, "no configuration", "Процедура Тест()\n\tЧтоУгодно();\nКонецПроцедуры", e)
}

// TestKnownCallsAreSilent: everything a module reaches without a prefix.
func TestKnownCallsAreSilent(t *testing.T) {
	check(t, "known calls", `
Процедура Тест() Экспорт
	Если НомерДня > День(КонецМесяца(Дата(2024, 1, 1))) Тогда
		ЛокальнаяПроцедура();
		МетодМодуляВнеЧерновика();
		ГлобальнаяФункция();
		Закрыть();
		А = Новый Структура("Ключ", Строка(1));
		Б = Тип("Число");
		В = Объект.ЛюбойМетод(1);
		ВызватьИсключение("текст", КатегорияОшибки.ОшибкаКонфигурации);
		Сообщить("НетТакойФункции(1)"); // НетТакойФункции(2)
	КонецЕсли;
КонецПроцедуры

Асинх Процедура ЛокальнаяПроцедура()
	Ждать ЧтоТо;
КонецПроцедуры`, env(func() *ModuleContext {
		mc := formModule()
		mc.Methods["методмодулявнечерновика"] = true
		return mc
	}()))
}

// TestUnknownCommonModuleMethod: ЗначенияРеквизитаОбъектов is not in БСП (there
// are ЗначениеРеквизитаОбъектов and ЗначенияРеквизитовОбъекта); the call went
// to production.
func TestUnknownCommonModuleMethod(t *testing.T) {
	fs := check(t, "missing method", `
Функция Наименования(СписокПользователей) Экспорт
	Наименования = ОбщегоНазначения.ЗначенияРеквизитаОбъектов(СписокПользователей, "Наименование");
	Возврат Наименования;
КонецФункции`, env(commonModule("Модуль")), want{"UnknownCommonModuleMethod", "high", 3})
	if len(fs) == 1 && !strings.Contains(fs[0].Suggestion, "ЗначениеРеквизитаОбъектов") {
		t.Errorf("the suggestion must name the close export: %q", fs[0].Suggestion)
	}

	fs = check(t, "not exported", "Процедура Тест()\n\tОбщегоНазначения.СлужебнаяПроцедура();\nКонецПроцедуры",
		env(commonModule("Модуль")), want{"UnknownCommonModuleMethod", "high", 2})
	if len(fs) == 1 && !strings.Contains(fs[0].Message, "не экспортный") {
		t.Errorf("a non-export method is named as such: %q", fs[0].Message)
	}

	check(t, "known and unprovable calls", `
Процедура Тест(Пользователи)
	А = ОбщегоНазначения.ЗначениеРеквизитаОбъектов(Ссылки, "Код");
	Пользователи.Добавить(1);
	НеизвестныйМодуль.ЧтоУгодно();
	Заимствованный.ЧтоУгодно();
	Б = ОбщегоНазначения.ЗначениеРеквизитаОбъекта(
		Ссылка, "Код");
КонецПроцедуры`, env(commonModule("Модуль")))

	// On the client the server module Пользователи is no name: the variable
	// of that name is a plain array.
	check(t, "server module name on the client", `
&НаКлиенте
Процедура Список(Команда)
	Пользователи = Новый Массив;
	Пользователи.Добавить(1);
КонецПроцедуры`, env(formModule()))

	// The draft is the module itself: its new export counts before it is on
	// disk.
	check(t, "own new export", `
Функция НовыйМетод() Экспорт
	Возврат ОбщегоНазначения.НовыйМетод();
КонецФункции`, env(commonModule("ОбщегоНазначения")))

	// An attribute of an unknown object could carry the module's name.
	check(t, "context unknown", "Процедура Тест()\n\tПользователи.Добавить(1);\nКонецПроцедуры",
		env(&ModuleContext{Kind: KindUnknown}), want{"UnknownCommonModuleMethod", "medium", 2})
}

// TestAssignToGlobalName: the three cases that failed at run time with «Поле
// объекта недоступно для записи»: a variable named as a common module of the
// configuration, as the БСП module Пользователи, as the global property
// Документы.
func TestAssignToGlobalName(t *testing.T) {
	check(t, "common module of the configuration", `
Процедура ОбновитьСтатусы(ДокументОбъект) Экспорт
	ИнтеграцияCRM = ИнтеграцияCRMПовтИсп.ИнтеграцияВключена();
КонецПроцедуры`, env(commonModule("РазнесениеСервер")), want{"AssignToGlobalName", "high", 3})

	// The method call on the same name is the same mistake: one finding.
	check(t, "БСП module", `
Функция ДанныеСводки() Экспорт
	Пользователи = Новый Массив;
	Пользователи.Добавить(1);
	Возврат Пользователи;
КонецФункции`, env(commonModule("Сводка")), want{"AssignToGlobalName", "high", 3})

	check(t, "global property and loop variable", `
Функция Список(Запрос) Экспорт
	Документы = СтрокиДляМодели(Запрос.Выполнить());
	Для Каждого Метаданные Из Коллекция Цикл
	КонецЦикла;
КонецФункции

Функция СтрокиДляМодели(Р)
КонецФункции`, env(commonModule("Инструменты")), want{"AssignToGlobalName", "high", 3}, want{"AssignToGlobalName", "high", 4})

	check(t, "unknown module", "Процедура Тест()\n\tПользователи = 1;\nКонецПроцедуры",
		env(&ModuleContext{Kind: KindUnknown}), want{"AssignToGlobalName", "medium", 2})
}

// TestAssignToGlobalNameLocals: a parameter or a Перем is a variable of its
// own (БСП itself has parameters named Документы); on the client a
// server-only name is no name; a condition line is not an assignment; a form
// attribute owns its name.
func TestAssignToGlobalNameLocals(t *testing.T) {
	check(t, "locals", `
Процедура С_Параметром(Документы, Знач Пользователи = Неопределено)
	Документы = Новый Массив;
	Пользователи = 1;
КонецПроцедуры

Процедура С_Перем()
	Перем ИнтеграцияCRM;
	ИнтеграцияCRM = Истина;
КонецПроцедуры

Процедура Условие(А)
	Если А = 1
		И Пользователи = Неопределено Тогда
	КонецЕсли;
	Если Документы = Неопределено Тогда Б = 1; КонецЕсли;
КонецПроцедуры`, env(commonModule("Модуль")))

	check(t, "client method of a form", `
&НаКлиенте
Процедура Провести(Команда)
	Документы = ВыделенныеДокументы();
	Пользователи = Новый Массив;
	Если Документы.Количество() = 0 Тогда
		Возврат;
	КонецЕсли;
КонецПроцедуры

&НаСервере
Функция ВыделенныеДокументы()
КонецФункции`, env(formModule()))

	check(t, "form attribute", "&НаСервере\nПроцедура Заполнить()\n\tДокументы = 1;\nКонецПроцедуры", env(formModule("Документы")))

	check(t, "server method of a form", "&НаСервере\nПроцедура Заполнить()\n\tДокументы = 1;\nКонецПроцедуры",
		env(formModule()), want{"AssignToGlobalName", "high", 3})
}

// TestFormDirectiveClientServer: &НаКлиентеНаСервере does not exist in a form
// module; the merge request's check failed with «Процедура или функция с
// указанным именем не определена».
func TestFormDirectiveClientServer(t *testing.T) {
	code := `
&НаКлиентеНаСервере
Процедура УстановитьОтбор(Список, Значение)
КонецПроцедуры

&НаКлиентеНаСервереБезКонтекста
Процедура УстановитьПериод(Список, Период)
КонецПроцедуры`
	check(t, "form", code, env(formModule()), want{"FormDirectiveClientServer", "high", 2})
	check(t, "common module", code, env(commonModule("Модуль")))
	check(t, "unknown module", code, env(&ModuleContext{Kind: KindUnknown}))
}

func extObjectModule() *ModuleContext {
	return &ModuleContext{
		Kind: KindObject, Display: "Документ.Поступление (модуль объекта)", Default: server, ContextComplete: true,
		Methods:      map[string]bool{"обработкапроведения": true},
		ContextNames: map[string]bool{"ручнаякорректировка": true, "контрагент": true, "обменданными": true, "этотобъект": true},
		Unborrowed:   map[string]string{"ручнаякорректировка": "РучнаяКорректировка", "товары": "Товары"},
		ObjectProps:  map[string]string{"контрагент": "Контрагент", "номер": "", "дата": ""},
		Extension:    "РасширениеТест",
	}
}

// TestUnborrowedAttribute: the extension did not borrow РучнаяКорректировка,
// and the object module failed to compile in every base: «Переменная не
// определена».
func TestUnborrowedAttribute(t *testing.T) {
	check(t, "bare read", `
&После("ОбработкаПроведения")
Процедура Расш_ОбработкаПроведения(Отказ, РежимПроведения)
	Если Отказ Или РучнаяКорректировка Или ОбменДанными.Загрузка Тогда
		Возврат;
	КонецЕсли;
	Для Каждого Строка Из Товары Цикл
	КонецЦикла;
КонецПроцедуры`, env(extObjectModule()), want{"UnborrowedAttribute", "high", 4}, want{"UnborrowedAttribute", "high", 7})

	check(t, "through ЭтотОбъект, in strings, as a parameter", `
Процедура Расш_Заполнить(Товары)
	Если ЭтотОбъект.РучнаяКорректировка Тогда
		Сообщить("РучнаяКорректировка");
	КонецЕсли;
	Для Каждого Строка Из Товары Цикл
	КонецЦикла;
КонецПроцедуры`, env(extObjectModule()))

	fs := check(t, "assignment", "Процедура Расш()\n\tРучнаяКорректировка = Истина;\nКонецПроцедуры",
		env(extObjectModule()), want{"UnborrowedAttribute", "medium", 2})
	if len(fs) == 1 && !strings.Contains(fs[0].Message, "локальную переменную") {
		t.Errorf("an assignment creates a variable: %q", fs[0].Message)
	}
}

// TestLoopCounterIsAttribute: in an object module Номер is the document's
// number, and Для Номер = 1 По ... wrote into it.
func TestLoopCounterIsAttribute(t *testing.T) {
	mc := extObjectModule()
	mc.Unborrowed = nil
	check(t, "loop counters", `
Функция РазобратьМесяц(Текст)
	Для Номер = 1 По 12 Цикл
	КонецЦикла;
	Для Каждого Дата Из Даты Цикл
	КонецЦикла;
	Для Индекс = 1 По 12 Цикл
	КонецЦикла;
КонецФункции`, env(mc), want{"LoopCounterIsAttribute", "high", 3}, want{"LoopCounterIsAttribute", "high", 5})

	check(t, "Перем hides the attribute", `
Функция РазобратьМесяц(Текст)
	Перем Номер;
	Для Номер = 1 По 12 Цикл
	КонецЦикла;
КонецФункции`, env(mc), want{"LocalShadowsAttribute", "medium", 3})

	check(t, "common module", "Процедура П()\n\tДля Номер = 1 По 12 Цикл\n\tКонецЦикла;\nКонецПроцедуры", env(commonModule("М")))
}

// TestVariadicCalls: СтрШаблон takes up to ten values and Мин any number; the
// old count read both signatures as fixed and flagged correct calls.
func TestVariadicCalls(t *testing.T) {
	check(t, "variadic", `
Процедура Тест()
	Текст = СтрШаблон("Документ %1 от %2", "А", "Б");
	М = Мин(1, 2, 3, 4);
	Т = СтрШаблон(НСтр("ru = 'Документ %1'"), А, Б, В);
	П = СтрШаблон("%%1 %1", А);
	Р = СтрШаблон("%(1)0 руб.", А);
КонецПроцедуры`, env(commonModule("М")))

	check(t, "template and values disagree", `
Процедура Тест()
	Текст = СтрШаблон("Документ %1 от %2", А);
	Текст = СтрШаблон("Документ %1", А, Б);
	ЗначениеЗаполнено(А, Б);
КонецПроцедуры`, env(commonModule("М")),
		want{"StrTemplateArgs", "high", 3}, want{"StrTemplateArgs", "high", 4}, want{"WrongArgCount", "high", 5})
}

// TestOrderByPresentation: the query of a mail ordered by the alias of
// ПРЕДСТАВЛЕНИЕ() and failed at run time in every base.
func TestOrderByPresentation(t *testing.T) {
	check(t, "by alias", `
Функция Записи(Месяц)
	Запрос = Новый Запрос;
	Запрос.Текст =
	"ВЫБРАТЬ
	|	Готовность.Документ КАК Документ,
	|	ПРЕДСТАВЛЕНИЕ(Готовность.Контрагент) КАК Клиент,
	|	Готовность.Дата КАК Дата
	|ИЗ
	|	РегистрСведений.Готовность КАК Готовность
	|ГДЕ
	|	Готовность.Месяц = &Месяц
	|
	|УПОРЯДОЧИТЬ ПО
	|	Клиент,
	|	Дата";
КонецФункции`, env(commonModule("М")), want{"OrderByPresentation", "high", 14})

	check(t, "by the function, after a commented line", `
Функция Записи()
	Текст = "ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Т КАК Т
	//|ГДЕ Т.Код = 1
	|УПОРЯДОЧИТЬ ПО ПРЕДСТАВЛЕНИЕ(Т.Ссылка) УБЫВ";
КонецФункции`, env(commonModule("М")), want{"OrderByPresentation", "high", 5})

	check(t, "allowed orders", `
Функция Записи()
	Текст = "ВЫБРАТЬ ПРЕДСТАВЛЕНИЕ(Т.Ссылка) КАК Клиент, Т.Наименование КАК Имя
	|ИЗ Справочник.Т КАК Т
	|УПОРЯДОЧИТЬ ПО Т.Наименование, Имя УБЫВ
	|;
	|ВЫБРАТЬ Т.Код КАК Клиент ИЗ Справочник.Т КАК Т УПОРЯДОЧИТЬ ПО Клиент";
	Сообщение = "Упорядочить по представлению нельзя";
КонецФункции`, env(commonModule("М")))
}

// TestManagerRefsNotObjects: a method of the collection and a local variable
// that shares its name are not references to metadata.
func TestManagerRefsNotObjects(t *testing.T) {
	e := env(commonModule("М"))
	e.Meta = meta
	check(t, "collection method and variable", `
Процедура Тест(Предмет)
	Если Документы.ТипВсеСсылки().СодержитТип(ТипЗнч(Предмет)) Тогда
	КонецЕсли;
КонецПроцедуры

&НаКлиенте
Процедура Клиент()
	Документы = Новый Массив;
	Документы.Добавить(1);
КонецПроцедуры`, e)
	check(t, "real miss", "Процедура Т()\n\tА = Справочники.НетТакого.ПустаяСсылка();\nКонецПроцедуры", e,
		want{"UnknownMetadataObject", "high", 2})
}

// TestScanLiterals: a multi-line literal keeps its text with commented lines
// dropped; date literals and doubled quotes are not code.
func TestScanLiterals(t *testing.T) {
	lines, lits := scan("А = \"ВЫБРАТЬ\n|\tПоле // не комментарий\n// |ГДЕ\n|ИЗ \"\"Т\"\"\"; // комментарий\nД = '20240101';")
	if len(lits) != 1 {
		t.Fatalf("literals = %+v", lits)
	}
	if want := "ВЫБРАТЬ\n\tПоле // не комментарий\nИЗ \"Т\""; lits[0].text != want {
		t.Errorf("literal = %q, want %q", lits[0].text, want)
	}
	if strings.Contains(lines[3].code, "комментарий") || !strings.HasSuffix(strings.TrimSpace(lines[3].code), `";`) {
		t.Errorf("closing line code = %q", lines[3].code)
	}
	if strings.Contains(lines[4].code, "2024") {
		t.Errorf("date literal leaked into code: %q", lines[4].code)
	}
}

// TestDeletedBlockIsNotCode: lines between #Удаление and #КонецУдаления are
// the base text an extension removes.
func TestDeletedBlockIsNotCode(t *testing.T) {
	check(t, "deleted block", `
&ИзменениеИКонтроль("Заполнить")
Процедура Расш_Заполнить()
	#Удаление
	Если РучнаяКорректировка Тогда
		НетТакойФункции();
	КонецЕсли;
	#КонецУдаления
КонецПроцедуры`, env(extObjectModule()))
}
