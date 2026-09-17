package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The routing map tells the model which tool answers which question, but it
// stops one step short: on a real task the answer needs several tools in order,
// and the model reliably calls the first one and stops. These prompts spell the
// order out for the tasks that come up most, so the chain is followed rather
// than remembered.
type workflowPrompt struct {
	name        string
	title       string
	description string
	args        []*mcp.PromptArgument
	// steps builds the ordered plan from the filled arguments.
	steps func(args map[string]string) ([]string, error)
}

// arg returns a trimmed argument or an error when a required one is missing.
func arg(args map[string]string, name string, required bool) (string, error) {
	v := strings.TrimSpace(args[name])
	if v == "" && required {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

// registerWorkflowPrompts wires the task chains.
func registerWorkflowPrompts(server *mcp.Server) {
	for _, wf := range workflowPrompts() {
		wf := wf
		server.AddPrompt(&mcp.Prompt{
			Name:        wf.name,
			Title:       wf.title,
			Description: wf.description,
			Arguments:   wf.args,
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			steps, err := wf.steps(req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			var b strings.Builder
			b.WriteString(wf.title + ".\n\nПорядок работы, шаги выполнять по очереди и не пропускать:\n\n")
			for i, s := range steps {
				fmt.Fprintf(&b, "%d. %s\n", i+1, s)
			}
			b.WriteString("\nЕсли шаг вернул что-то неожиданное, разбираться с этим до перехода к следующему.")
			return &mcp.GetPromptResult{
				Description: wf.description,
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: b.String()},
				}},
			}, nil
		})
	}
}

// workflowPrompts is the catalogue of chains. Each one is a task a developer
// actually brings, not a tool of this server.
func workflowPrompts() []workflowPrompt {
	return []workflowPrompt{
		{
			name:        "add-attribute",
			title:       "Добавить реквизит объекту",
			description: "Порядок для добавления реквизита: сначала выяснить, что сломается и где реквизит должен появиться, потом писать.",
			args: []*mcp.PromptArgument{
				{Name: "objectType", Description: "вид объекта, например Document", Required: true},
				{Name: "name", Description: "имя объекта, например ЗаказКлиента", Required: true},
				{Name: "attribute", Description: "имя нового реквизита"},
			},
			steps: func(a map[string]string) ([]string, error) {
				t, err := arg(a, "objectType", true)
				if err != nil {
					return nil, err
				}
				n, err := arg(a, "name", true)
				if err != nil {
					return nil, err
				}
				attr, _ := arg(a, "attribute", false)
				if attr == "" {
					attr = "новый реквизит"
				}
				return []string{
					fmt.Sprintf("context_pack %s %s: структура объекта, интерфейс модулей и где объект используется.", t, n),
					fmt.Sprintf("find_metadata_usages %s %s с withTemplates=true: схемы компоновки отчётов и правила регистрации обмена ломаются от изменения состава первыми.", t, n),
					fmt.Sprintf("visibility_audit %s %s: посмотреть, какие функциональные опции управляют реквизитами этого объекта. Новый реквизит, скорее всего, тоже должен быть под опцией.", t, n),
					fmt.Sprintf("meta-edit или правка XML: добавить реквизит %s. Тип брать из get_query_schema соседнего реквизита, а не по памяти.", attr),
					fmt.Sprintf("form_impact по формам объекта с draftCode, если реквизит выводится на форму программно: проверить черновик до записи."),
					fmt.Sprintf("write_path %s %s: если реквизит заполняется при записи, увидеть, кто ещё пишет в объект и в каком порядке.", t, n),
					"validate_bsl на весь написанный код: несуществующие метаданные и неверное число аргументов.",
				}, nil
			},
		},
		{
			name:        "new-object",
			title:       "Добавить объект в конфигурацию",
			description: "Порядок для нового объекта: создать по образцу и зарегистрировать везде, где зарегистрирован образец.",
			args: []*mcp.PromptArgument{
				{Name: "objectType", Description: "вид объекта, например Document", Required: true},
				{Name: "name", Description: "имя нового объекта", Required: true},
				{Name: "like", Description: "объект-образец того же вида", Required: true},
			},
			steps: func(a map[string]string) ([]string, error) {
				t, err := arg(a, "objectType", true)
				if err != nil {
					return nil, err
				}
				n, err := arg(a, "name", true)
				if err != nil {
					return nil, err
				}
				like, err := arg(a, "like", true)
				if err != nil {
					return nil, err
				}
				return []string{
					fmt.Sprintf("context_pack %s %s: разобрать образец, по которому делаем.", t, like),
					fmt.Sprintf("meta-compile: создать %s.%s по структуре образца.", t, n),
					fmt.Sprintf("скилл bsl-module-skeleton: модуль объекта в порядке областей БСП, если нужен код."),
					fmt.Sprintf("new_object_checklist %s %s like=%s: подсистемы, роли, функциональные опции, планы обмена, подписки, журналы, критерии отбора. Это половина работы, которую забывают.", t, n, like),
					fmt.Sprintf("visibility_audit %s %s: убедиться, что объект вообще доходит до командного интерфейса.", t, n),
					"meta-validate и validate_bsl: проверить созданное.",
				}, nil
			},
		},
		{
			name:        "why-invisible",
			title:       "Пользователь не видит объект",
			description: "Порядок разбора: видимость держится на трёх вещах сразу, права проверяют первыми, а виноваты чаще опция или раздел.",
			args: []*mcp.PromptArgument{
				{Name: "objectType", Description: "вид объекта", Required: true},
				{Name: "name", Description: "имя объекта", Required: true},
				{Name: "role", Description: "роль или профиль пользователя, если известны"},
			},
			steps: func(a map[string]string) ([]string, error) {
				t, err := arg(a, "objectType", true)
				if err != nil {
					return nil, err
				}
				n, err := arg(a, "name", true)
				if err != nil {
					return nil, err
				}
				role, _ := arg(a, "role", false)
				rights := fmt.Sprintf("rights_audit %s %s: какие роли дают права и какие RLS-ограничения на них висят.", t, n)
				if role != "" {
					rights = fmt.Sprintf("rights_audit %s %s с profile=%q или roles=[%q]: эффективные права именно этого набора, роли складываются по ИЛИ.", t, n, role, role)
				}
				return []string{
					fmt.Sprintf("visibility_audit %s %s: подсистемы с признаком попадания в командный интерфейс и функциональные опции. Помнить: у нескольких опций логика ИЛИ, объект виден, пока включена хотя бы одна.", t, n),
					rights,
					"Если опции найдены: посмотреть их значения. Для параметризованных опций значения своего для каждого склада или организации, единого нет.",
					fmt.Sprintf("Если дело не в видимости, а в данных: write_path %s %s покажет, кто вмешивается при записи.", t, n),
				}, nil
			},
		},
		{
			name:        "not-in-exchange",
			title:       "Объект не ушёл в обмен",
			description: "Порядок разбора обмена: состав плана, авторегистрация, регистраторы.",
			args: []*mcp.PromptArgument{
				{Name: "objectType", Description: "вид объекта", Required: true},
				{Name: "name", Description: "имя объекта", Required: true},
			},
			steps: func(a map[string]string) ([]string, error) {
				t, err := arg(a, "objectType", true)
				if err != nil {
					return nil, err
				}
				n, err := arg(a, "name", true)
				if err != nil {
					return nil, err
				}
				return []string{
					fmt.Sprintf("exchange_audit %s %s: в какие планы объект входит, где авторегистрация запрещена и чем покрыт каждый план (авторегистрация, подписка или правила регистрации ППД).", t, n),
					"Если объекта нет в составе нужного плана: дальше искать нечего, его надо туда добавить.",
					fmt.Sprintf("Если план покрыт подпиской: write_path %s %s покажет, на каком событии она висит и что выполняется до неё.", t, n),
					"Если регистрация есть, а объект не ушёл: причина вне выгрузки (узел, отбор ППД, функциональная опция, ошибка обмена). Смотреть журнал регистрации на живой базе через get_event_log.",
				}, nil
			},
		},
		{
			name:        "change-posting",
			title:       "Изменить проведение документа",
			description: "Порядок для правки проведения: сначала понять, где формируются движения, потом трогать код.",
			args: []*mcp.PromptArgument{
				{Name: "name", Description: "имя документа", Required: true},
			},
			steps: func(a map[string]string) ([]string, error) {
				n, err := arg(a, "name", true)
				if err != nil {
					return nil, err
				}
				return []string{
					fmt.Sprintf("get_movements %s review=true: состав движений против того, что делает код, и стиль проведения. Если review.style=delegated, движения формируются механизмом вне модуля объекта, и править надо там.", n),
					fmt.Sprintf("write_path Document %s: подписки на ОбработкаПроведения и на запись, которые выполнятся вместе с твоим кодом.", n),
					"get_query_schema по регистрам движений перед написанием запроса: поля и виртуальные таблицы, а не по памяти.",
					fmt.Sprintf("После правки: get_movements %s review=true ещё раз плюс query_advisor на написанные запросы.", n),
				}, nil
			},
		},
	}
}
