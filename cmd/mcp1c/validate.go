package main

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/validate"
)

type validateBSLInput struct {
	Code string `json:"code" jsonschema:"the BSL you just wrote (module or procedure)"`
}

type validateBSLOutput struct {
	Count    int                `json:"count"`
	Findings []validate.Finding `json:"findings"`
	Checked  []string           `json:"checked" jsonschema:"what was actually verified, so that an empty result is not read as more than it is"`
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
func registerValidateBSL(server *mcp.Server, provide func() source.ConfigSource, idx syntaxCorpus) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_bsl",
		Description: "Checks BSL you just wrote against the configuration and the platform reference: references to metadata that does not exist (Справочники.X) and platform calls with a wrong argument count. Call it after writing a module or procedure, before applying it. Offline; it neither compiles nor runs the code and reports only what it can prove.",
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
		return nil, validateBSLOutput{Count: len(findings), Findings: findings, Checked: checked}, nil
	})
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
