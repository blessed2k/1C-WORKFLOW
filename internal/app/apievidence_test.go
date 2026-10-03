package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
)

const apiEvidenceСклад = `#Область ПрограммныйИнтерфейс

// Возвращает остатки товаров на складе.
//
// Параметры:
//  Склад - СправочникСсылка.Склады - склад.
//  Дата - Дата, Неопределено - на какую дату.
//
// Возвращаемое значение:
//  ТаблицаЗначений - остатки:
//   * Товар - СправочникСсылка.Товары - товар.
//
Функция ОстаткиТоваров(Склад, Дата = Неопределено) Экспорт
	Возврат Новый ТаблицаЗначений;
КонецФункции

// Возвращает остаток одного товара на складе.
//
// Параметры:
//  Товар - СправочникСсылка.Товары - товар.
//
// Возвращаемое значение:
//  Число - остаток.
//
Функция ОстатокТовара(Товар) Экспорт
	Возврат ОстаткиТоваров(Товар).Количество();
КонецФункции

// Записывает остатки товаров на складе.
//
// Возвращаемое значение:
//  Булево - у процедуры результата нет, раздел оставлен по ошибке.
//
Процедура ЗаписатьОстаткиТоваров(Остатки) Экспорт
КонецПроцедуры

#КонецОбласти
`

const apiEvidenceПродажи = `#Область ПрограммныйИнтерфейс

// Проверяет заказ.
Процедура ПроверитьЗаказ(Заказ) Экспорт
	Остатки = СкладскойУчет.ОстаткиТоваров(Заказ.Склад, ТекущаяДатаСеанса());
	Если СкладскойУчет.ОстаткиТоваров(Заказ.Склад).Количество() = 0 Тогда
		Возврат;
	КонецЕсли;
	Остаток = СкладскойУчет.ОстатокТовара(
		Заказ.Товар);
КонецПроцедуры

#КонецОбласти
`

func apiEvidenceFixture(t *testing.T, id string) *Projects {
	t.Helper()
	files := map[string]string{
		declPath("CommonModule", "СкладскойУчет"): общийМодульXML("СкладскойУчет", true, false, false),
		commonModulePath("СкладскойУчет"):         apiEvidenceСклад,
		declPath("CommonModule", "Продажи"):       общийМодульXML("Продажи", true, false, false),
		commonModulePath("Продажи"):               apiEvidenceПродажи,
	}
	p, _ := newFilesFixtureProject(t, domain.ProjectID("api-evidence-"+id), files, index.Config{})
	return p
}

// TestFindAPIReturnTypes: тип возвращаемого значения берётся из комментария
// метода и стоит в ответе; отбор returns и accepts оставляет методы с этим
// типом и работает и в поиске, и в списке модуля.
func TestFindAPIReturnTypes(t *testing.T) {
	p := apiEvidenceFixture(t, "types")
	svc := NewAPIService(p)
	ctx := context.Background()

	item, _ := findAPIItem(t, p, "остатки товаров на складе")
	returns := map[string]string{}
	for _, m := range item.Other {
		returns[m.Call] = m.Returns
	}
	want := map[string]string{
		"СкладскойУчет.ОстаткиТоваров": "ТаблицаЗначений", "СкладскойУчет.ОстатокТовара": "Число", "СкладскойУчет.ЗаписатьОстаткиТоваров": "",
	}
	if !reflect.DeepEqual(returns, want) {
		t.Errorf("типы результата = %q, want %q", returns, want)
	}

	for _, tc := range []struct {
		name string
		in   FindAPIInput
		want []string
	}{
		{"поиск по типу результата", FindAPIInput{Query: "остатки товаров на складе", Returns: "таблица значений"}, []string{"СкладскойУчет.ОстаткиТоваров"}},
		{"поиск по типу параметра", FindAPIInput{Query: "остатки товаров на складе", Accepts: "СправочникСсылка.Товары"}, []string{"СкладскойУчет.ОстатокТовара"}},
		{"оба отбора не сходятся", FindAPIInput{Query: "остатки товаров на складе", Returns: "Число", Accepts: "Дата"}, nil},
	} {
		resp, err := svc.FindAPI(ctx, tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := apiCalls(resp.Items[0].Other); len(got)+len(tc.want) > 0 && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: other = %q, want %q", tc.name, got, tc.want)
		}
	}

	resp, err := svc.FindAPI(ctx, FindAPIInput{Module: "СкладскойУчет", Returns: "Число"})
	if err != nil || len(resp.Items[0].Methods) != 1 || resp.Items[0].Methods[0].Call != "СкладскойУчет.ОстатокТовара" || resp.Items[0].MethodsTotal != 1 {
		t.Errorf("список модуля с отбором по типу: %+v, %v", resp.Items, err)
	}

	_, err = svc.FindAPI(ctx, FindAPIInput{Returns: "Число"})
	if appErr, ok := err.(*Error); !ok || appErr.Code != CodeInvalidArgument {
		t.Errorf("отбор по типу без query и module: %v, want invalid_argument", err)
	}
}

// TestFindAPICallsAndExample: ответ называет, сколько раз метод зовут из
// других модулей (вызов из своего модуля не в счёт), и показывает строку
// одного такого вызова.
func TestFindAPICallsAndExample(t *testing.T) {
	p := apiEvidenceFixture(t, "calls")
	item, _ := findAPIItem(t, p, "остатки товаров на складе")
	got := map[string]APIMethodItem{}
	for _, m := range item.Other {
		got[m.Call] = m
	}
	stock := got["СкладскойУчет.ОстаткиТоваров"]
	if stock.Calls != 2 || stock.Example != "Остатки = СкладскойУчет.ОстаткиТоваров(Заказ.Склад, ТекущаяДатаСеанса());" {
		t.Errorf("ОстаткиТоваров: вызовов %d, пример %q", stock.Calls, stock.Example)
	}
	// Вызов, перенесённый на две строки, показан целиком одной строкой.
	if one := got["СкладскойУчет.ОстатокТовара"]; one.Calls != 1 || one.Example != "Остаток = СкладскойУчет.ОстатокТовара( Заказ.Товар);" {
		t.Errorf("ОстатокТовара: вызовов %d, пример %q", one.Calls, one.Example)
	}
	// Метод, который из других модулей не зовут: полей нет.
	if write := got["СкладскойУчет.ЗаписатьОстаткиТоваров"]; write.Calls != 0 || write.Example != "" {
		t.Errorf("ЗаписатьОстаткиТоваров (никто не зовёт): вызовов %d, пример %q", write.Calls, write.Example)
	}

	resp, err := NewAPIService(p).FindAPI(context.Background(), FindAPIInput{Module: "СкладскойУчет"})
	if err != nil || len(resp.Items[0].Methods) != 3 || resp.Items[0].Methods[0].Calls != 2 || resp.Items[0].Methods[0].Returns != "ТаблицаЗначений" {
		t.Errorf("список модуля: %+v, %v", resp.Items, err)
	}
}

// TestFindAPITypeFilterSaysWhatItDropped: отбор по типу, отсеявший методы,
// которые подошли по словам, говорит об этом: пустая выдача не должна
// читаться как «такого метода нет».
func TestFindAPITypeFilterSaysWhatItDropped(t *testing.T) {
	p := apiEvidenceFixture(t, "dropped")
	svc := NewAPIService(p)
	ctx := context.Background()

	resp, err := svc.FindAPI(ctx, FindAPIInput{Query: "остатки товаров на складе", Returns: "Структура"})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	item := resp.Items[0]
	if len(item.Other) != 0 || item.OtherMatched != 0 || !strings.Contains(item.Note, "отсеял 3 методов") {
		t.Errorf("поиск с отбором, который ничего не оставил: other %q, совпало %d, note %q", apiCalls(item.Other), item.OtherMatched, item.Note)
	}

	resp, err = svc.FindAPI(ctx, FindAPIInput{Module: "СкладскойУчет", Returns: "Структура"})
	if err != nil {
		t.Fatalf("FindAPI: %v", err)
	}
	if item := resp.Items[0]; item.Methods == nil || len(item.Methods) != 0 || !strings.Contains(item.Note, "отсеял 3 методов") {
		t.Errorf("список модуля с отбором, который ничего не оставил: %+v, note %q", item.Methods, item.Note)
	}

	// Без отбора заметки об отсеянных нет.
	if item, _ := findAPIItem(t, p, "остатки товаров на складе"); strings.Contains(item.Note, "отсеял") {
		t.Errorf("заметка об отборе без отбора: %q", item.Note)
	}
}

// TestAPICallStatement: пример вызова это оператор целиком: перенесённый на
// строки вызов склеивается, вызов внутри условия обрывается на конце строки,
// длинный оператор режется.
func TestAPICallStatement(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		find string
		want string
	}{
		{"одна строка", "А = 1;\n\tБ = М.Метод(А, 2);\nВ = 3;", "М.Метод", "Б = М.Метод(А, 2);"},
		{"CRLF", "А = 1;\r\n\tБ = М.Метод(А);\r\nВ = 3;", "М.Метод", "Б = М.Метод(А);"},
		{"перенос аргументов", "Б = М.Метод(\n\tА,\n\tНовый Структура(\"К\", 1));\nВ = 3;", "М.Метод", "Б = М.Метод( А, Новый Структура(\"К\", 1));"},
		{"вызов в условии", "Если М.Метод(А) Тогда\n\tВозврат;\nКонецЕсли;", "М.Метод", "Если М.Метод(А) Тогда"},
		{"вызов на строке-продолжении", "Б = Другой(\n\tМ.Метод(А),\n\t2);\nВ = 3;", "М.Метод", "М.Метод(А),"},
		{"не длиннее четырёх строк", "Б = М.Метод(\n1,\n2,\n3,\n4,\n5);", "М.Метод", "Б = М.Метод( 1, 2, 3,"},
		{"последняя строка без перевода", "Б = М.Метод(А)", "М.Метод", "Б = М.Метод(А)"},
	} {
		at := strings.Index(tc.text, tc.find)
		if got := apiCallStatement([]byte(tc.text), at); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := apiCallStatement([]byte("А = 1;"), 100); got != "" {
		t.Errorf("смещение за концом текста: %q", got)
	}
	if got := apiCallStatement(nil, 0); got != "" {
		t.Errorf("текста нет: %q", got)
	}
	long := "Б = М.Метод(" + strings.Repeat("Аргумент, ", 60) + "1);"
	if got := apiCallStatement([]byte(long), 4); len([]rune(got)) != apiExampleMaxRunes+1 {
		t.Errorf("длинный оператор не обрезан: %d знаков", len([]rune(got)))
	}
}
