package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

func init() {
	registerIndexTool(registerAPITools)
}

// findAPIInput: вход find_api.
type findAPIInput struct {
	Query   string `json:"query,omitempty" jsonschema:"what the code must do, in words, e.g. разбить строку по разделителю; omit it to browse (see module)"`
	Module  string `json:"module,omitempty" jsonschema:"module name (ОбщегоНазначения, Справочники.Номенклатура): with query searches only there, without query lists its whole API"`
	Limit   int    `json:"limit,omitempty" jsonschema:"full hits per section, default 10, max 50; 10 more follow as one-liners"`
	Returns string `json:"returns,omitempty" jsonschema:"keep only methods whose doc comment names this return type, e.g. ТаблицаЗначений, Массив, ДокументСсылка; narrows query or module; a method with no type written in its comment is dropped, so retry without it when nothing fits"`
	Accepts string `json:"accepts,omitempty" jsonschema:"keep only methods with a parameter of this type by the doc comment, e.g. ДокументСсылка, Структура; narrows query or module"`
}

// registerAPITools регистрирует find_api: поиск готового метода программного
// интерфейса по описанию задачи.
func registerAPITools(server *mcp.Server, deps indexToolDeps) {
	svc := app.NewAPIService(deps.projects)
	falseHint := false

	mcp.AddTool(server, &mcp.Tool{
		Name:        "find_api",
		Description: "Finds a ready-made method by what it must do: exported methods of the ПрограммныйИнтерфейс region in common modules and manager modules, ranked against the task in two sections, bsp (the БСП library of this configuration, with its version) and other (the configuration itself). Each hit is a call expression with the exact signature and defaults, a summary, the return type from the doc comment, the execution context, how many times other modules call it (calls) with one real call line (example), and a deprecated mark; bspMore/otherMore continue each section as one-line hits (call, summary, uid for get_symbol): read them too. Call it before writing a helper of your own. The search is lexical over the method name, module name and the whole doc comment: add synonyms and the configuration's own terms to the query. No hit: browse instead. Without arguments it returns core, the БСП methods the applied code of this configuration uses most, and the map of the БСП library (subsystems and their modules); with module and no query it lists the whole API of that module. *Переопределяемый modules are not here.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &falseHint, DestructiveHint: &falseHint},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in findAPIInput) (*mcp.CallToolResult, app.Response[app.APISearchItem], error) {
		resp, err := svc.FindAPI(ctx, app.FindAPIInput{Query: in.Query, Module: in.Module, Limit: in.Limit, Returns: in.Returns, Accepts: in.Accepts})
		if err != nil {
			return nil, app.Response[app.APISearchItem]{}, err
		}
		return nil, resp, nil
	})
}
