package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Issue #1 (C4): the raw tools read BSL with the same parser the index does, so
// one module answers the same way on both paths. The module is the hard case
// for the regular expressions this replaced: English syntax, a declaration
// wrapped over two lines with a comment inside it, and RegisterRecords instead
// of Движения.

const parityObjectModule = `Procedure Posting(Cancel, PostingMode)
	RegisterRecords.ИмуществоНаСкладах.Write = True;
	For Each Row In Товары Do
		Record = RegisterRecords.ИмуществоНаСкладах.Add();
		Record.Количество = Row.Количество;
	EndDo;
EndProcedure

// Fills the document from a base one.
Function ЗаполнитьПоОснованию(Основание, // the base document (may be empty)
	Val Режим = "(full)") Export
	Return True;
EndFunction

Procedure Служебная() // not Export: the word is in a comment
EndProcedure
`

const parityOverridableModule = `// Called when the property is moved between warehouses.
Procedure ПриПеремещенииИмущества(Разделитель = ")", // the separator (one char)
	Отказ) Export
	Отказ = False;
EndProcedure

Procedure НеТочка(Документ) // Export is only mentioned here
EndProcedure
`

const parityAccumulationRegisterXML = metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<AccumulationRegister uuid="UID">
		<Properties>
			<Name>NAME</Name>
		</Properties>
		<ChildObjects/>
	</AccumulationRegister>
</MetaDataObject>`

// parityWriteProject lays out a one-component project: a document posting to
// one of its two declared registers, and an overridable common module.
func parityWriteProject(t *testing.T, root string) {
	t.Helper()
	ogManifest(t, root, "parity")
	cfg := filepath.Join(root, "cfg")
	file := func(rel string) string { return filepath.Join(cfg, filepath.FromSlash(rel)) }
	metaWriteFile(t, file("Configuration.xml"), metaConfigurationXML("КонфигурацияСверки"))
	for i, reg := range []string{"ИмуществоНаСкладах", "ИмуществоВПути"} {
		xml := strings.NewReplacer("UID", ogUID(200+i), "NAME", reg).Replace(parityAccumulationRegisterXML)
		metaWriteFile(t, file(workspace.DumpDeclarationPath("AccumulationRegister", reg)), xml)
	}
	metaWriteFile(t, file(workspace.DumpDeclarationPath("Document", "ПеремещениеИмущества")),
		ogDocumentXML(ogUID(1), "ПеремещениеИмущества",
			"AccumulationRegister.ИмуществоНаСкладах", "AccumulationRegister.ИмуществоВПути"))
	metaWriteFile(t, file(workspace.DumpModulePath("Document", "ПеремещениеИмущества", workspace.ModuleObject)),
		metaBOM+parityObjectModule)
	module := "ИмуществоПереопределяемый"
	metaWriteFile(t, file(workspace.DumpDeclarationPath("CommonModule", module)),
		strings.ReplaceAll(strings.ReplaceAll(apModuleXML, "MODNAME", module), "NN", "07"))
	metaWriteFile(t, file(workspace.DumpModulePath("CommonModule", module, workspace.ModuleCommon)),
		metaBOM+parityOverridableModule)
}

func TestRawAndIndexAgreeOnOneModule(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	parityWriteProject(t, root)
	cs, _ := apConnect(t, ctx, options{projectsRoot: t.TempDir()})
	ogReindex(t, ctx, cs, root)

	call := func(name string, args map[string]any, out any) {
		t.Helper()
		isErr, raw := apCall(t, ctx, cs, name, args)
		if isErr {
			t.Fatalf("%s: %s", name, raw)
		}
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			t.Fatalf("%s: unmarshal: %v (%s)", name, err, raw)
		}
	}
	// indexExports: the exported methods of one module in the index.
	indexExports := func(module string) []string {
		t.Helper()
		var res struct {
			Items []struct {
				Symbols []struct {
					Name   string `json:"name"`
					Export bool   `json:"export"`
				} `json:"symbols"`
			} `json:"items"`
		}
		call("get_module_structure", map[string]any{"module": module}, &res)
		if len(res.Items) != 1 {
			t.Fatalf("get_module_structure %s: %d items", module, len(res.Items))
		}
		var out []string
		for _, s := range res.Items[0].Symbols {
			if s.Export {
				out = append(out, s.Name)
			}
		}
		sort.Strings(out)
		return out
	}
	same := func(what string, raw, index []string) {
		t.Helper()
		sort.Strings(raw)
		if strings.Join(raw, ",") != strings.Join(index, ",") || len(raw) == 0 {
			t.Errorf("%s: raw %q, index %q", what, raw, index)
		}
	}

	// context_pack against get_module_structure.
	var pack struct {
		Modules []struct {
			Kind    string   `json:"kind"`
			Exports []string `json:"exports"`
		} `json:"modules"`
	}
	call("context_pack", map[string]any{"type": "Document", "name": "ПеремещениеИмущества"}, &pack)
	var packExports []string
	for _, m := range pack.Modules {
		for _, header := range m.Exports {
			// "Function Имя(Параметры)" -> "Имя"
			_, rest, _ := strings.Cut(header, " ")
			name, _, _ := strings.Cut(rest, "(")
			packExports = append(packExports, name)
		}
	}
	same("context_pack", packExports,
		indexExports(workspace.DumpModulePath("Document", "ПеремещениеИмущества", workspace.ModuleObject)))

	// bsp_extension_points against get_module_structure.
	var points struct {
		Points []struct {
			Module    string `json:"module"`
			Procedure string `json:"procedure"`
		} `json:"points"`
	}
	call("bsp_extension_points", map[string]any{"query": "имущество", "limit": 100}, &points)
	var pointNames []string
	for _, p := range points.Points {
		pointNames = append(pointNames, p.Procedure)
	}
	same("bsp_extension_points", pointNames,
		indexExports(workspace.DumpModulePath("CommonModule", "ИмуществоПереопределяемый", workspace.ModuleCommon)))

	// get_movements (with review) and write_path against find_register_writes.
	var movements struct {
		Registers []struct {
			Register   string `json:"register"`
			UsedInCode bool   `json:"usedInCode"`
			WriteFlag  bool   `json:"writeFlag"`
		} `json:"registers"`
		Review *struct {
			Style    string   `json:"style"`
			Handlers []string `json:"handlers"`
		} `json:"review"`
	}
	call("get_movements", map[string]any{"name": "ПеремещениеИмущества", "review": true}, &movements)
	var steps struct {
		Steps []struct {
			Kind   string `json:"kind"`
			Source string `json:"source"`
			Detail string `json:"detail"`
		} `json:"steps"`
	}
	call("write_path", map[string]any{"objectType": "Document", "name": "ПеремещениеИмущества"}, &steps)
	var rawUsed, indexUsed []string
	for _, r := range movements.Registers {
		_, short, _ := strings.Cut(r.Register, ".")
		if r.UsedInCode {
			rawUsed = append(rawUsed, short)
			if !r.WriteFlag {
				t.Errorf("get_movements: %s used without the Write = True flag", r.Register)
			}
		}
		var writes struct {
			Items []struct {
				Mode string `json:"mode"`
			} `json:"items"`
		}
		call("find_register_writes", map[string]any{"register": short, "modes": "movement"}, &writes)
		if len(writes.Items) > 0 {
			indexUsed = append(indexUsed, short)
		}
	}
	sort.Strings(indexUsed)
	same("get_movements", rawUsed, indexUsed)
	if movements.Review == nil || movements.Review.Style != "inline" ||
		strings.Join(movements.Review.Handlers, ",") != "Posting" {
		t.Errorf("get_movements review=true: %+v", movements.Review)
	}
	var stepUsed []string
	for _, s := range steps.Steps {
		if s.Kind == "movements" && strings.Contains(s.Detail, "Записывать = Истина найдено") {
			_, short, _ := strings.Cut(s.Source, ".")
			stepUsed = append(stepUsed, short)
		}
	}
	same("write_path", stepUsed, indexUsed)
}
