package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestValidateBSLReadsTheProjectByPath: the tool reaches the extension through
// the active dump's other components, reads the module kind from path (a path
// relative to a component counts), and says in checked what the draft was.
func TestValidateBSLReadsTheProjectByPath(t *testing.T) {
	cfg, ext := cliFixture(t)
	formRel := workspace.DumpFormModulePath("DataProcessor", "Обработка", "Форма")
	cliWrite(t, ext, formRel, "")
	cliWrite(t, ext, filepath.Join(filepath.Dir(filepath.Dir(formRel)), "Form.xml"),
		`<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"><Attributes><Attribute name="Объект" id="1"/></Attributes></Form>`)

	src := source.NewXMLSource(cfg)
	src.OtherComponents = func() []source.ComponentRoot { return []source.ComponentRoot{{Name: "ext", Dir: ext}} }
	cs := connectTools(t, func(srv *mcp.Server) {
		registerValidateBSL(srv, func() source.ConfigSource { return src }, syntaxtest.Fixture(t), nil)
	})
	call := func(args map[string]any) validateBSLOutput {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "validate_bsl", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("validate_bsl: %v %s", err, contentText(res))
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var out validateBSLOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return out
	}

	code := "&НаКлиентеНаСервере\nПроцедура П()\n\tОбщийСервер.НетТакого();\nКонецПроцедуры\n"
	out := call(map[string]any{"code": code, "path": filepath.ToSlash(formRel)})
	codes := map[string]string{}
	for _, f := range out.Findings {
		codes[f.Code] = f.Severity
	}
	if codes["FormDirectiveClientServer"] != "high" || codes["UnknownCommonModuleMethod"] != "high" {
		t.Errorf("findings = %+v", out.Findings)
	}
	if !checkedMentions(out.Checked, "Обработка.Обработка.Форма.Форма") || !checkedMentions(out.Checked, "расширение ext") {
		t.Errorf("checked must name the module and its extension: %v", out.Checked)
	}

	// Without path the directive is not judged, and checked says why.
	out = call(map[string]any{"code": code})
	for _, f := range out.Findings {
		if f.Code == "FormDirectiveClientServer" {
			t.Errorf("without path the module is not known to be a form: %+v", f)
		}
	}
	if !checkedMentions(out.Checked, "вид модуля не известен") {
		t.Errorf("checked must say the module kind is unknown: %v", out.Checked)
	}
}
