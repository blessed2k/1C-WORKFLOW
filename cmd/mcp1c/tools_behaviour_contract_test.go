package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Вторая половина контракта: поведение. Схема говорит, что можно послать, эти
// тесты фиксируют, что приходит в ответ — сообщение об ошибке без источника,
// семантику полей и порядок элементов (строгость D6, архитектура §29).
//
// Ожидаемые значения взяты из фикстурной выгрузки internal/source/testdata/dump,
// разобранной руками: Configuration.xml перечисляет Catalog Товары и Контрагенты,
// Catalogs/Товары.xml — реквизиты Артикул и ЕдиницаИзмерения, табличную часть
// Цены и формы ФормаЭлемента, ФормаСписка.

// сообщениеБезИсточника — дословный текст, который видит клиент, когда источник
// не выбран. Это часть контракта: по нему агент понимает, что делать дальше.
const сообщениеБезИсточника = "no configuration source: call set_dump <xml-export-dir> " +
	"for offline mode, or start the server with --base for live mode"

// вызовБезИсточника — минимально допустимый вызов инструмента и то, чем он
// обязан ответить, когда сервер поднят без выгрузки и без базы.
type вызовБезИсточника struct {
	имя             string
	аргументы       map[string]any
	требуетИсточник bool
}

// вызовыБезИсточника покрывает все инструменты оффлайн-режима, кроме set_dump и
// list_projects: те ходят в файловую систему и в elicitation, их поведение уже
// закреплено в TestSetDumpTool и projects_test.go.
var вызовыБезИсточника = []вызовБезИсточника{
	{имя: "bsl_syntax", аргументы: map[string]any{"query": "Сообщить"}},
	{имя: "server_info", аргументы: map[string]any{}},
	{имя: "validate_bsl", аргументы: map[string]any{"code": "Процедура Тест() КонецПроцедуры"}},

	{имя: "bsp_extension_points", аргументы: map[string]any{"query": "проведение"}, требуетИсточник: true},
	{имя: "command_visibility", аргументы: map[string]any{}, требуетИсточник: true},
	{имя: "context_pack", аргументы: map[string]any{"type": "Document", "name": "Х"}, требуетИсточник: true},
	{имя: "dump_diff", аргументы: map[string]any{"dumpB": "нет-такой-выгрузки"}, требуетИсточник: true},
	{имя: "exchange_audit", аргументы: map[string]any{"objectType": "Document", "name": "Х"}, требуетИсточник: true},
	{имя: "extension_context", аргументы: map[string]any{}, требуетИсточник: true},
	{имя: "find_dependency_paths", аргументы: map[string]any{
		"fromType": "Document", "fromName": "Х", "toType": "Catalog", "toName": "Y"}, требуетИсточник: true},
	{имя: "find_metadata_usages", аргументы: map[string]any{"type": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "form_impact", аргументы: map[string]any{"type": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "get_configuration_info", аргументы: map[string]any{}, требуетИсточник: true},
	{имя: "get_form_structure", аргументы: map[string]any{"type": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "get_metadata_tree", аргументы: map[string]any{}, требуетИсточник: true},
	{имя: "get_movements", аргументы: map[string]any{"name": "Х"}, требуетИсточник: true},
	{имя: "get_object_structure", аргументы: map[string]any{"type": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "get_query_schema", аргументы: map[string]any{"type": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "new_object_checklist", аргументы: map[string]any{
		"objectType": "Catalog", "name": "Х", "like": "Товары"}, требуетИсточник: true},
	{имя: "object_exists", аргументы: map[string]any{"type": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "get_movements", аргументы: map[string]any{"name": "Х", "review": true}, требуетИсточник: true},
	{имя: "query_advisor", аргументы: map[string]any{"text": "ВЫБРАТЬ * ИЗ Справочник.Х"}, требуетИсточник: true},
	{имя: "rights_audit", аргументы: map[string]any{"type": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "search_code", аргументы: map[string]any{"query": "Х"}, требуетИсточник: true},
	{имя: "visibility_audit", аргументы: map[string]any{"objectType": "Catalog", "name": "Х"}, требуетИсточник: true},
	{имя: "write_path", аргументы: map[string]any{"objectType": "Catalog", "name": "Х"}, требуетИсточник: true},
}

// TestКонтрактОшибкиБезИсточника: инструмент, которому нужна выгрузка, обязан
// вернуть ошибку с дословным указанием, что делать; инструменту, который живёт
// на встроенных данных, источник не нужен, и он обязан отвечать без него.
func TestКонтрактОшибкиБезИсточника(t *testing.T) {
	ctx := context.Background()
	сессия := сессияКлиента(t, options{}) // режим none: ни выгрузки, ни базы
	for _, вызов := range вызовыБезИсточника {
		t.Run(вызов.имя, func(t *testing.T) {
			результат, err := сессия.CallTool(ctx, &mcp.CallToolParams{Name: вызов.имя, Arguments: вызов.аргументы})
			if err != nil {
				t.Fatalf("транспорт вернул ошибку вместо ответа инструмента: %v", err)
			}
			if !вызов.требуетИсточник {
				if результат.IsError {
					t.Fatalf("инструмент не зависит от выгрузки, но ответил ошибкой: %s", contentText(результат))
				}
				return
			}
			if !результат.IsError {
				t.Fatalf("без источника инструмент обязан вернуть ошибку, а вернул: %.200s", contentText(результат))
			}
			if got := contentText(результат); got != сообщениеБезИсточника {
				t.Errorf("текст ошибки изменился:\n получено: %q\n ожидалось: %q", got, сообщениеБезИсточника)
			}
		})
	}
}

// TestКонтрактПорядкаВыдачи: bsl_syntax обещает в схеме «one entry per requested
// name, in the order asked». Порядок — часть контракта: по нему вызывающий
// сопоставляет ответы со своим списком имён, не сверяя строки.
func TestКонтрактПорядкаВыдачи(t *testing.T) {
	ctx := context.Background()
	сессия := сессияКлиента(t, options{})
	имена := []any{"ТипЗнч", "Сообщить", "СтрДлина"}

	результат, err := сессия.CallTool(ctx, &mcp.CallToolParams{
		Name:      "bsl_syntax",
		Arguments: map[string]any{"queries": имена},
	})
	if err != nil || результат.IsError {
		t.Fatalf("bsl_syntax: %v %s", err, contentText(результат))
	}

	var ответ struct {
		Count   int `json:"count"`
		Results []struct {
			Query string `json:"query"`
			Count int    `json:"count"`
		} `json:"results"`
	}
	разобрать(t, результат, &ответ)

	if len(ответ.Results) != len(имена) {
		t.Fatalf("на %d имён вернулось %d разделов", len(имена), len(ответ.Results))
	}
	сумма := 0
	for i, раздел := range ответ.Results {
		if раздел.Query != имена[i] {
			t.Errorf("раздел %d относится к %q, а спрашивали %q", i, раздел.Query, имена[i])
		}
		сумма += раздел.Count
	}
	if ответ.Count != сумма {
		t.Errorf("count=%d не равен сумме разделов %d", ответ.Count, сумма)
	}
}

// TestКонтрактСемантикиДереваМетаданных: фильтры type и like сужают дерево, а не
// меняют его форму, totalObjects считает то, что реально отдано.
func TestКонтрактСемантикиДереваМетаданных(t *testing.T) {
	ctx := context.Background()
	сессия := сессияНаФикстуре(t)

	var поТипу struct {
		Configuration string `json:"configuration"`
		TotalObjects  int    `json:"totalObjects"`
		Groups        []struct {
			Type    string   `json:"type"`
			Objects []string `json:"objects"`
		} `json:"groups"`
	}
	результат := вызватьУспешно(t, сессия, ctx, "get_metadata_tree", map[string]any{"type": "Catalog"})
	разобрать(t, результат, &поТипу)

	if поТипу.Configuration != "ДемоКонфигурация" {
		t.Errorf("configuration = %q, в Configuration.xml — ДемоКонфигурация", поТипу.Configuration)
	}
	if len(поТипу.Groups) != 1 || поТипу.Groups[0].Type != "Catalog" {
		t.Fatalf("фильтр по типу оставил не только Catalog: %+v", поТипу.Groups)
	}
	// Порядок объектов внутри группы — алфавитный, а не порядок Configuration.xml
	// (там Товары идут раньше Контрагентов). Это и есть зафиксированный контракт:
	// вызывающий вправе рассчитывать на стабильную сортировку выдачи.
	ожидаемые := []string{"Контрагенты", "Товары"}
	if len(поТипу.Groups[0].Objects) != len(ожидаемые) {
		t.Fatalf("справочников %d, в выгрузке %d: %+v", len(поТипу.Groups[0].Objects), len(ожидаемые), поТипу.Groups[0].Objects)
	}
	for i, имя := range ожидаемые {
		if поТипу.Groups[0].Objects[i] != имя {
			t.Errorf("объект %d = %q, в Configuration.xml на этом месте %q", i, поТипу.Groups[0].Objects[i], имя)
		}
	}
	if поТипу.TotalObjects != len(ожидаемые) {
		t.Errorf("totalObjects=%d при %d отданных объектах", поТипу.TotalObjects, len(ожидаемые))
	}

	// like — подстрока без учёта регистра, по всем типам сразу.
	var поПодстроке struct {
		TotalObjects int `json:"totalObjects"`
		Groups       []struct {
			Type    string   `json:"type"`
			Objects []string `json:"objects"`
		} `json:"groups"`
	}
	разобрать(t, вызватьУспешно(t, сессия, ctx, "get_metadata_tree", map[string]any{"like": "товар"}), &поПодстроке)
	найдено := map[string]bool{}
	всего := 0
	for _, группа := range поПодстроке.Groups {
		всего += len(группа.Objects)
		for _, имя := range группа.Objects {
			найдено[группа.Type+"."+имя] = true
		}
	}
	for _, ожидаемый := range []string{"Catalog.Товары", "Document.РеализацияТоваровУслуг", "AccumulationRegister.ТоварыНаСкладах"} {
		if !найдено[ожидаемый] {
			t.Errorf("like=товар не нашёл %s: %+v", ожидаемый, найдено)
		}
	}
	if найдено["Catalog.Контрагенты"] {
		t.Error("like=товар вернул Контрагенты: подстрока перестала быть подстрокой")
	}
	if поПодстроке.TotalObjects != всего {
		t.Errorf("totalObjects=%d при %d отданных объектах", поПодстроке.TotalObjects, всего)
	}
}

// TestКонтрактСемантикиObjectExists: инструмент отвечает на двоичный вопрос и на
// промахе подсказывает близкие имена — ради этого его и зовут вместо дерева.
func TestКонтрактСемантикиObjectExists(t *testing.T) {
	ctx := context.Background()
	сессия := сессияНаФикстуре(t)

	var есть struct {
		Exists  bool     `json:"exists"`
		Object  string   `json:"object"`
		Count   int      `json:"count"`
		Similar []string `json:"similar"`
	}
	разобрать(t, вызватьУспешно(t, сессия, ctx, "object_exists",
		map[string]any{"type": "Catalog", "name": "Товары"}), &есть)
	if !есть.Exists {
		t.Error("Catalog.Товары есть в выгрузке, но exists=false")
	}
	if есть.Object != "Catalog.Товары" {
		t.Errorf("object = %q, ожидалось Catalog.Товары", есть.Object)
	}
	if есть.Count != 2 {
		t.Errorf("count = %d, в Configuration.xml два справочника", есть.Count)
	}

	var нет struct {
		Exists  bool     `json:"exists"`
		Object  string   `json:"object"`
		Count   int      `json:"count"`
		Similar []string `json:"similar"`
	}
	разобрать(t, вызватьУспешно(t, сессия, ctx, "object_exists",
		map[string]any{"type": "Catalog", "name": "Товар"}), &нет)
	if нет.Exists {
		t.Error("Catalog.Товар в выгрузке нет, но exists=true")
	}
	if !содержит(нет.Similar, "Товары") {
		t.Errorf("промах по Товар не подсказал Товары: %v", нет.Similar)
	}
}

// TestКонтрактРазделовОбъекта: parts отсекает разделы структуры и не трогает
// шапку объекта — на этом построена экономия контекста у вызывающего.
func TestКонтрактРазделовОбъекта(t *testing.T) {
	ctx := context.Background()
	сессия := сессияНаФикстуре(t)

	type структура struct {
		Type            string   `json:"type"`
		Name            string   `json:"name"`
		Forms           []string `json:"forms"`
		Attributes      []any    `json:"attributes"`
		TabularSections []any    `json:"tabularSections"`
	}

	var целиком структура
	разобрать(t, вызватьУспешно(t, сессия, ctx, "get_object_structure",
		map[string]any{"type": "Catalog", "name": "Товары"}), &целиком)
	if len(целиком.Attributes) != 2 {
		t.Errorf("реквизитов %d, в Товары.xml их два (Артикул, ЕдиницаИзмерения)", len(целиком.Attributes))
	}
	if len(целиком.TabularSections) != 1 {
		t.Errorf("табличных частей %d, в Товары.xml одна (Цены)", len(целиком.TabularSections))
	}
	if len(целиком.Forms) != 2 {
		t.Errorf("форм %d, в Товары.xml их две: %v", len(целиком.Forms), целиком.Forms)
	}

	var толькоФормы структура
	разобрать(t, вызватьУспешно(t, сессия, ctx, "get_object_structure",
		map[string]any{"type": "Catalog", "name": "Товары", "parts": "forms"}), &толькоФормы)
	if len(толькоФормы.Forms) != 2 {
		t.Errorf("parts=forms потерял формы: %v", толькоФормы.Forms)
	}
	if len(толькоФормы.Attributes) != 0 || len(толькоФормы.TabularSections) != 0 {
		t.Errorf("parts=forms оставил лишние разделы: реквизитов %d, табличных частей %d",
			len(толькоФормы.Attributes), len(толькоФормы.TabularSections))
	}
	if толькоФормы.Type != "Catalog" || толькоФормы.Name != "Товары" {
		t.Errorf("parts=forms потерял шапку объекта: %+v", толькоФормы)
	}
}

// сессияНаФикстуре поднимает сервер и переключает его на фикстурную выгрузку.
func сессияНаФикстуре(t *testing.T) *mcp.ClientSession {
	t.Helper()
	сессия := сессияКлиента(t, options{})
	выгрузка, err := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "dump"))
	if err != nil {
		t.Fatalf("путь к фикстуре: %v", err)
	}
	результат, err := сессия.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "set_dump",
		Arguments: map[string]any{"path": выгрузка},
	})
	if err != nil || результат.IsError {
		t.Fatalf("set_dump %s: %v %s", выгрузка, err, contentText(результат))
	}
	return сессия
}

// вызватьУспешно зовёт инструмент и валит тест, если он ответил ошибкой.
func вызватьУспешно(t *testing.T, сессия *mcp.ClientSession, ctx context.Context,
	имя string, аргументы map[string]any) *mcp.CallToolResult {
	t.Helper()
	результат, err := сессия.CallTool(ctx, &mcp.CallToolParams{Name: имя, Arguments: аргументы})
	if err != nil {
		t.Fatalf("%s: %v", имя, err)
	}
	if результат.IsError {
		t.Fatalf("%s вернул ошибку: %s", имя, contentText(результат))
	}
	return результат
}

// разобрать раскладывает structuredContent ответа в переданную структуру: клиент
// работает именно с ним, TextContent — совместимость.
func разобрать(t *testing.T, результат *mcp.CallToolResult, куда any) {
	t.Helper()
	if результат.StructuredContent == nil {
		t.Fatal("ответ без structuredContent")
	}
	сырой, err := json.Marshal(результат.StructuredContent)
	if err != nil {
		t.Fatalf("сериализация ответа: %v", err)
	}
	if err := json.Unmarshal(сырой, куда); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
}

func содержит(строки []string, искомая string) bool {
	for _, строка := range строки {
		if строка == искомая {
			return true
		}
	}
	return false
}
