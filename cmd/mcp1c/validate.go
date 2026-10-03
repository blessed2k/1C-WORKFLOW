package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/validate"
)

type validateBSLInput struct {
	Code   string `json:"code" jsonschema:"the BSL you just wrote (module or procedure)"`
	Module string `json:"module,omitempty" jsonschema:"the module this code belongs to, as find_api names it (ОбщегоНазначения, Справочники.Номенклатура): its own methods are then not offered as ready-made ones"`
}

type validateBSLOutput struct {
	Count    int                `json:"count"`
	Findings []validate.Finding `json:"findings"`
	// ReadyMethodsNote comes before the list it qualifies: the list is a lexical
	// hint and most of it is unrelated to the procedure it stands next to.
	ReadyMethodsNote string                `json:"readyMethodsNote,omitempty"`
	ReadyMethods     []app.ReadyMethodItem `json:"readyMethods,omitempty" jsonschema:"possible ready-made methods of БСП and the configuration per procedure of the draft: a lexical hint, never counted as a finding"`
	Checked          []string              `json:"checked" jsonschema:"what was actually verified, so that an empty result is not read as more than it is"`
}

// readyMethodsFinder names, for the procedures declared in a draft, ready-made
// methods of the program interface close to them by words. Declared here, on
// the consumer side: the test hands in a stub instead of a whole indexed
// project.
type readyMethodsFinder interface {
	ReadyMethods(ctx context.Context, in app.ReadyMethodsInput) (app.Response[app.ReadyMethodsReport], error)
}

// registerValidateBSL wires validate_bsl: the finished-draft counterpart of
// get_query_schema and bsl_syntax.
//
// Those two must be called while writing, against the writer's own certainty,
// and that is a contest they lose — this session wrote ten queries and three
// handlers without consulting either. A check that runs on a completed text has
// the opposite trigger ("written — now verify"), which fires by itself; that is
// why syntax checkers and validators get called without being asked. So the same
// knowledge is offered again, at the moment the model will actually reach for it.
//
// The same trigger carries the question the writer skips before writing: is
// there a ready-made method already. For every procedure the draft declares
// the answer names the closest methods of БСП and the configuration.
func registerValidateBSL(server *mcp.Server, provide func() source.ConfigSource, idx syntaxCorpus, ready readyMethodsFinder) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_bsl",
		Description: "Checks BSL you just wrote against the configuration and the platform reference: references to metadata that does not exist (Справочники.X) and platform calls with a wrong argument count. For the procedures the draft declares (event handlers aside), readyMethods lists possible ready-made methods of БСП and the configuration found by the words of the name and comment: a lexical hint, most entries are unrelated, so read their summaries and replace your procedure only when one really does the same; an empty list proves nothing, find_api searches by the task. Call it after writing a module or procedure, before applying it. Offline; it neither compiles nor runs the code; findings are only what it can prove.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in validateBSLInput) (*mcp.CallToolResult, validateBSLOutput, error) {
		if strings.TrimSpace(in.Code) == "" {
			return nil, validateBSLOutput{}, errNoCode
		}
		// Half of what this tool proves comes from the platform reference. With a
		// corpus it cannot read, a clean report would mean "nothing was checked",
		// which is the one answer a validator must never give quietly.
		if err := syntaxCorpusErr(idx); err != nil {
			return nil, validateBSLOutput{}, err
		}
		var checked []string

		var meta validate.MetadataLookup
		if src := provide(); src != nil {
			if names, err := metadataNames(ctx, src); err == nil {
				meta = func(mdType, name string) bool { return names[mdType+"."+strings.ToLower(name)] }
				checked = append(checked, "ссылки на метаданные — по активной выгрузке")
			}
		}
		method := validate.MethodLookup(func(name string) (validate.Method, bool) {
			e, ok := idx.GlobalMethod(name)
			if !ok {
				return validate.Method{}, false
			}
			req, total := syntax.ParamCounts(e)
			return validate.Method{Signature: e.Signature, Required: req, Total: total}, true
		})
		checked = append(checked, "число аргументов вызовов глобального контекста — по справочнику платформы")
		if meta == nil {
			checked = append(checked, "ссылки на метаданные НЕ проверены: нет активной выгрузки (set_dump)")
		}

		findings := validate.BSL(in.Code, meta, method)
		out := validateBSLOutput{Count: len(findings), Findings: findings}
		out.ReadyMethods, out.ReadyMethodsNote, out.Checked = readyMethodsFor(ctx, ready, in, checked)
		return nil, out, nil
	})
}

const readyMethodsNote = "Подсказка по словам имени и комментария процедуры, не находка: большинство названных методов к процедуре не относятся. " +
	"Читайте назначение (summary; параметры даст get_symbol по uid) и заменяйте свой код вызовом, только когда метод делает то же самое."

// readyMethodsFor asks the index for ready-made methods and says in checked
// what was and was not looked up. A skipped lookup, a failed one and an empty
// one are three different answers, and none of them may read as "nothing ready
// exists".
func readyMethodsFor(ctx context.Context, ready readyMethodsFinder, in validateBSLInput, checked []string) ([]app.ReadyMethodItem, string, []string) {
	if ready == nil {
		return nil, "", checked
	}
	resp, err := ready.ReadyMethods(ctx, app.ReadyMethodsInput{Code: in.Code, Module: in.Module})
	if err != nil {
		// The error names its own cause and remedy (no project root, nothing
		// indexed, a read failure): repeat it instead of guessing.
		return nil, "", append(checked, "готовые методы НЕ искались: "+err.Error())
	}
	if len(resp.Items) == 0 {
		return nil, "", checked
	}
	rep := resp.Items[0]
	for _, w := range resp.Warnings {
		checked = append(checked, "готовые методы, предупреждение "+w.Code+": "+w.Message)
	}
	if resp.Stale {
		checked = append(checked, "готовые методы искались по индексу, который отстал от выгрузки")
	}
	if rep.Checked == 0 {
		return nil, "", append(checked, "готовые методы не искались: в черновике "+strconv.Itoa(rep.Declared)+
			" объявлений, все пропущены (обработчики событий, перехватчики, имена из одного слова без комментария)")
	}
	line := "готовые методы: объявлений в черновике " + strconv.Itoa(rep.Declared) + ", поиск шёл по " + strconv.Itoa(rep.Checked) +
		" (пропущено обработчиков событий и имён из одного слова: " + strconv.Itoa(rep.Skipped) + ")"
	if rep.NotChecked > 0 {
		line += ", ещё " + strconv.Itoa(rep.NotChecked) + " не проверено: сверх потолка на один вызов"
	}
	line += "; возможные готовые методы названы для " + strconv.Itoa(len(rep.Methods)) +
		". Поиск по словам: пустой список не значит, что готового метода нет (синонимы не сводятся), ищите find_api по задаче"
	checked = append(checked, line)
	if len(rep.Methods) == 0 {
		return nil, "", checked
	}
	return rep.Methods, readyMethodsNote, checked
}

var errNoCode = errUsage("pass code: the BSL to check")

type errUsage string

func (e errUsage) Error() string { return string(e) }

// metadataNames flattens the configuration into a lookup set keyed by
// "Type.lowercasename".
func metadataNames(ctx context.Context, src source.ConfigSource) (map[string]bool, error) {
	tree, err := src.MetadataTree(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, g := range tree.Groups {
		for _, name := range g.Objects {
			out[g.Type+"."+strings.ToLower(name)] = true
		}
	}
	return out, nil
}
