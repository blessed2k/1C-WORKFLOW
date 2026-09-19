package bsl

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestHTTPCalls: исходящий HTTP-вызов распознаётся по форме кода внутри
// метода; адрес, который из текста метода не выводится, помечается
// динамическим, а не угадывается (D7: динамика даёт бейдж, не ребро).
func TestHTTPCalls(t *testing.T) {
	type want struct {
		verb       string
		host       string
		hostStatic bool
		path       string
		kind       HTTPPathKind
		suffix     string
	}
	cases := []struct {
		name string
		body string
		want []want
	}{
		{
			name: "литералы сервера и пути",
			body: "Соединение = Новый HTTPСоединение(\"erp.example.local\", 443);\n" +
				"Запрос = Новый HTTPЗапрос(\"/erp/hs/exchange/v1/orders\", Заголовки);\n" +
				"Ответ = Соединение.ОтправитьДляОбработки(Запрос);\n",
			want: []want{{"POST", "erp.example.local", true, "/erp/hs/exchange/v1/orders", HTTPPathStatic, ""}},
		},
		{
			name: "английские имена и запрос прямо в аргументе",
			body: "Conn = New HTTPConnection(\"erp.example.local\");\n" +
				"Resp = Conn.Get(New HTTPRequest(\"/erp/hs/exchange/version\"));\n",
			want: []want{{"GET", "erp.example.local", true, "/erp/hs/exchange/version", HTTPPathStatic, ""}},
		},
		{
			name: "строковая переменная и склейка с хвостом",
			body: "Адрес = \"/erp/hs/exchange/v1/orders/\" + Номер;\n" +
				"Соединение = Новый HTTPСоединение(Сервер);\n" +
				"Запрос = Новый HTTPЗапрос(Адрес);\n" +
				"Ответ = Соединение.Получить(Запрос);\n",
			want: []want{{"GET", "", false, "/erp/hs/exchange/v1/orders/", HTTPPathPrefix, ""}},
		},
		{
			name: "СтрШаблон даёт начало до подстановки",
			body: "Соединение = Новый HTTPСоединение(\"erp.example.local\");\n" +
				"Запрос = Новый HTTPЗапрос(СтрШаблон(\"/erp/hs/exchange/%1\", Метод));\n" +
				"Ответ = Соединение.Удалить(Запрос);\n",
			want: []want{{"DELETE", "erp.example.local", true, "/erp/hs/exchange/", HTTPPathPrefix, ""}},
		},
		{
			name: "АдресРесурса переписывает путь запроса",
			body: "Соединение = Новый HTTPСоединение(\"erp.example.local\");\n" +
				"Запрос = Новый HTTPЗапрос();\n" +
				"Запрос.АдресРесурса = \"/erp/hs/exchange/version\";\n" +
				"Ответ = Соединение.ВызватьHTTPМетод(\"patch\", Запрос);\n",
			want: []want{{"PATCH", "erp.example.local", true, "/erp/hs/exchange/version", HTTPPathStatic, ""}},
		},
		{
			name: "путь из поля структуры динамический",
			body: "Соединение = Новый HTTPСоединение(Параметры.Сервер);\n" +
				"Запрос = Новый HTTPЗапрос(Параметры.Путь);\n" +
				"Ответ = Соединение.Записать(Запрос);\n",
			want: []want{{"PUT", "", false, "", HTTPPathDynamic, ""}},
		},
		{
			name: "запрос, пришедший параметром, динамический",
			body: "Соединение = Новый HTTPСоединение(\"erp.example.local\");\n" +
				"Ответ = Соединение.Получить(ЗапросИзвне);\n",
			want: []want{{"GET", "erp.example.local", true, "", HTTPPathDynamic, ""}},
		},
		{
			name: "метод ВызватьHTTPМетод с вычисляемым именем",
			body: "Соединение = Новый HTTPСоединение(\"erp.example.local\");\n" +
				"Запрос = Новый HTTPЗапрос(\"/erp/hs/exchange/version\");\n" +
				"Ответ = Соединение.ВызватьHTTPМетод(ИмяМетода, Запрос);\n",
			want: []want{{"", "erp.example.local", true, "/erp/hs/exchange/version", HTTPPathStatic, ""}},
		},
		{
			name: "статический конец пути после вычисляемой части",
			body: "Соединение = Новый HTTPСоединение(\"erp.example.local\");\n" +
				"Запрос = Новый HTTPЗапрос(\"/erp/hs/exchange/\" + Версия + \"/GetIBParameters\");\n" +
				"Ответ = Соединение.Получить(Запрос);\n",
			want: []want{{"GET", "erp.example.local", true, "/erp/hs/exchange/", HTTPPathPrefix, "/GetIBParameters"}},
		},
		{
			name: "СтрШаблон с концом после подстановки",
			body: "Соединение = Новый HTTPСоединение(\"erp.example.local\");\n" +
				"Запрос = Новый HTTPЗапрос(СтрШаблон(\"/erp/hs/exchange/%1/GetFilePart\", Версия));\n" +
				"Ответ = Соединение.Получить(Запрос);\n",
			want: []want{{"GET", "erp.example.local", true, "/erp/hs/exchange/", HTTPPathPrefix, "/GetFilePart"}},
		},
		{
			name: "Соответствие.Получить не HTTP-вызов",
			body: "Значение = Соответствие.Получить(Ключ);\n" +
				"Структура.Удалить(\"Ключ\");\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte("Процедура Обмен(Параметры, ЗапросИзвне, Номер, Сервер, Метод, ИмяМетода, Ключ, Версия)\n" +
				tc.body + "КонецПроцедуры\n")
			mod, diags := Parse(src, Options{File: "CommonModules/ОбменССайтом/Ext/Module.bsl"})
			if len(diags) != 0 {
				t.Fatalf("диагностик быть не должно: %v", codes(diags))
			}
			checkSpans(t, src, mod, diags, tc.name)
			if len(mod.HTTPCalls) != len(tc.want) {
				t.Fatalf("HTTP-вызовов %d, ожидалось %d: %+v", len(mod.HTTPCalls), len(tc.want), mod.HTTPCalls)
			}
			for i, w := range tc.want {
				c := mod.HTTPCalls[i]
				got := want{c.Verb, c.Host, c.HostStatic, c.Path, c.PathKind, c.PathSuffix}
				if got != w {
					t.Errorf("вызов %d: %+v, ожидалось %+v", i, got, w)
				}
				if mod.MethodName(c.Method) != "Обмен" {
					t.Errorf("метод вызова %q, ожидался Обмен", mod.MethodName(c.Method))
				}
				if c.Confidence >= domain.ConfidenceExact {
					t.Errorf("факт эвристический, confidence %v не может быть 1", c.Confidence)
				}
				if err := c.Provenance.Validate(c.Confidence); err != nil {
					t.Errorf("provenance: %v", err)
				}
			}
		})
	}
}

// TestHTTPBindingsAreMethodLocal: привязка соединения из одного метода не
// переходит в другой, как и привязка набора записей регистра.
func TestHTTPBindingsAreMethodLocal(t *testing.T) {
	src := []byte("Процедура Первая()\n" +
		"Соединение = Новый HTTPСоединение(\"erp.example.local\");\n" +
		"КонецПроцедуры\n" +
		"Процедура Вторая(Ключ)\n" +
		"Значение = Соединение.Получить(Ключ);\n" +
		"КонецПроцедуры\n")
	mod, _ := Parse(src, Options{})
	if len(mod.HTTPCalls) != 0 {
		t.Fatalf("привязка утекла из метода: %+v", mod.HTTPCalls)
	}
}
