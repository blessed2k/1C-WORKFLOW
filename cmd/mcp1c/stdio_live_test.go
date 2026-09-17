package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Сквозной прогон: тем же SDK, что и у клиента, поднимаем собранный сервер по stdio и зовём
// live-инструменты. Тесты в internal/source проверяют разбор ответов коннектора, а здесь
// проверяется вся цепочка целиком: JSON-RPC поверх stdio -> регистрация инструментов ->
// HTTPSource -> расширение в базе. Без этого «слой проверен» не равно «сервер работает».
//
// Запуск:
//
//	# учётные данные базы — в переменных окружения MCP_1C_USER/MCP_1C_PASSWORD
//	MCP_1C_BASE_URL=http://localhost:8314/ut/hs/mcp-1c \
//	    go test ./cmd/mcp1c -run TestLiveStdio -v
func TestLiveStdioSession(t *testing.T) {
	if os.Getenv("MCP_1C_BASE_URL") == "" {
		t.Skip("MCP_1C_BASE_URL не задан: живой базы нет")
	}

	двоичный := buildMCP1C(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	клиент := mcp.NewClient(&mcp.Implementation{Name: "e2e-проба", Version: "1"}, nil)
	сессия, err := клиент.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(двоичный)}, nil)
	if err != nil {
		t.Fatalf("подключение к серверу по stdio: %v", err)
	}
	defer сессия.Close()

	инструменты, err := сессия.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	имена := map[string]bool{}
	for _, инструмент := range инструменты.Tools {
		имена[инструмент.Name] = true
	}
	for _, обязательный := range []string{
		"server_info", "get_configuration_info", "get_metadata_tree", "get_object_structure",
		"execute_query", "validate_query", "analyze_query", "get_event_log", "get_predefined",
		"check_sync",
	} {
		if !имена[обязательный] {
			t.Errorf("сервер не зарегистрировал инструмент %s", обязательный)
		}
	}
	t.Logf("инструментов зарегистрировано: %d", len(инструменты.Tools))

	вызов := func(имя string, аргументы map[string]any) map[string]any {
		t.Helper()
		результат, err := сессия.CallTool(ctx, &mcp.CallToolParams{Name: имя, Arguments: аргументы})
		if err != nil {
			t.Fatalf("%s: %v", имя, err)
		}
		if результат.IsError {
			t.Fatalf("%s вернул ошибку: %s", имя, текстОтвета(результат))
		}
		сырой, err := json.Marshal(результат.StructuredContent)
		if err != nil {
			t.Fatalf("%s: разбор ответа: %v", имя, err)
		}
		разобранный := map[string]any{}
		if err := json.Unmarshal(сырой, &разобранный); err != nil {
			t.Fatalf("%s: ответ не объект: %v", имя, err)
		}
		return разобранный
	}

	состояние := вызов("server_info", map[string]any{})
	if состояние["liveBase"] == nil && состояние["base"] == nil && состояние["mode"] == nil {
		t.Errorf("server_info не сообщил активный источник: %+v", состояние)
	}

	сведения := вызов("get_configuration_info", map[string]any{})
	if сведения["name"] == "" || сведения["name"] == nil {
		t.Errorf("get_configuration_info без имени конфигурации: %+v", сведения)
	}
	t.Logf("конфигурация: %v %v, платформа %v", сведения["name"], сведения["version"], сведения["platformVersion"])

	дерево := вызов("get_metadata_tree", map[string]any{})
	группы, _ := дерево["groups"].([]any)
	if len(группы) < 30 {
		t.Errorf("get_metadata_tree отдал %d типов", len(группы))
	}

	объект := вызов("get_object_structure", map[string]any{"type": "Document", "name": "ЗаказКлиента"})
	if разделы, _ := объект["tabularSections"].([]any); len(разделы) == 0 {
		t.Errorf("get_object_structure без табличных частей: %+v", объект)
	}

	запрос := вызов("execute_query", map[string]any{
		"text":  "ВЫБРАТЬ ПЕРВЫЕ 2 Ссылка, Наименование ИЗ Справочник.Номенклатура",
		"limit": 2,
	})
	if строки, _ := запрос["rows"].([]any); len(строки) == 0 {
		t.Errorf("execute_query без строк: %+v", запрос)
	}

	валидация := вызов("validate_query", map[string]any{"text": "ВЫБРАТЬ ИЗ ГДЕ"})
	if валидация["valid"] != false {
		t.Errorf("validate_query признал битый запрос валидным: %+v", валидация)
	}

	анализ := вызов("analyze_query", map[string]any{
		"text": "ВЫБРАТЬ * ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки",
	})
	if предупреждения, _ := анализ["warnings"].([]any); len(предупреждения) == 0 {
		t.Errorf("analyze_query молчит на заведомо тяжёлом запросе: %+v", анализ)
	}

	// Имя берём из живого дерева, а не зашиваем: состав подсистем у конфигураций разный.
	имяПодсистемы := ""
	for _, группа := range группы {
		запись, _ := группа.(map[string]any)
		if запись["type"] != "Subsystem" {
			continue
		}
		объекты, _ := запись["objects"].([]any)
		if len(объекты) > 0 {
			имяПодсистемы, _ = объекты[0].(string)
		}
		break
	}
	if имяПодсистемы == "" {
		t.Fatal("в дереве нет подсистем: get_subsystem не на чем проверить")
	}
	подсистема := вызов("get_subsystem", map[string]any{"name": имяПодсистемы})
	if подсистема["name"] != имяПодсистемы {
		t.Errorf("get_subsystem вернул %+v для %s", подсистема["name"], имяПодсистемы)
	}

	если := вызов("object_exists", map[string]any{"type": "Document", "name": "ЗаказКлиента"})
	if если["exists"] != true {
		t.Errorf("object_exists не нашёл существующий документ: %+v", если)
	}

	журнал := вызов("get_event_log", map[string]any{"limit": 3})
	if записи, _ := журнал["entries"].([]any); len(записи) == 0 {
		t.Errorf("get_event_log без записей: %+v", журнал)
	}

	предопределённые := вызов("get_predefined", map[string]any{
		"type": "Catalog", "name": "ВидыКонтактнойИнформации",
	})
	if элементы, _ := предопределённые["items"].([]any); len(элементы) == 0 {
		t.Errorf("get_predefined без элементов: %+v", предопределённые)
	}

	if выгрузка := os.Getenv("MCP_1C_DUMP"); выгрузка != "" {
		сверка := вызов("check_sync", map[string]any{"dumpPath": выгрузка})
		if сверка["configuration"] == nil {
			t.Errorf("check_sync без имени конфигурации: %+v", сверка)
		}
		// Поля с omitempty на синхронной базе просто отсутствуют: приведение без проверки
		// уронило бы тест паникой ровно там, где сравнение прошло идеально.
		расхождения, _ := сверка["typeDiffs"].([]any)
		несравнённые, _ := сверка["notComparedTypes"].([]any)
		if len(несравнённые) > 0 {
			t.Errorf("check_sync не сравнил %d типов: живое дерево не покрывает выгрузку", len(несравнённые))
		}
		t.Logf("check_sync: версии совпадают=%v, расхождений по типам=%d, не сравнивалось=%d",
			сверка["versionMatch"], len(расхождения), len(несравнённые))
	}

	// Инструменты, которых в live быть не должно: ошибка обязана дойти до клиента,
	// а не превратиться в пустой успешный ответ.
	формы, err := сессия.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_form_structure",
		Arguments: map[string]any{"type": "Catalog", "name": "Номенклатура", "form": "ФормаЭлемента"},
	})
	if err == nil && !формы.IsError {
		t.Error("get_form_structure в live обязан вернуть ошибку")
	}
}

// текстОтвета собирает текстовые блоки ответа: в них лежит причина ошибки инструмента.
func текстОтвета(результат *mcp.CallToolResult) string {
	части := make([]string, 0, len(результат.Content))
	for _, блок := range результат.Content {
		if текст, ок := блок.(*mcp.TextContent); ок {
			части = append(части, текст.Text)
		}
	}
	if len(части) == 0 {
		return "(без текстового содержимого)"
	}
	return strings.Join(части, "; ")
}
