package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// The raw analyzers read BSL through the parser of the index layer, so a
// module written in English, with a declaration wrapped over several lines and
// a comment inside it, answers the same way a Russian one-liner does. Each
// fixture below broke one of the regular expressions this replaced.

// englishObjectModule posts with the English spelling of the platform: the
// RegisterRecords collection, the Write flag, Add and a record variable.
const englishObjectModule = `Procedure Posting(Cancel, PostingMode)
	RegisterRecords.ИмуществоНаСкладах.Write = True;
	For Each Row In Товары Do
		Record = RegisterRecords.ИмуществоНаСкладах.Add();
		Record.Период = Date;
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

// englishObjectModuleNoFlag fills a set and never writes it.
const englishObjectModuleNoFlag = `Procedure Posting(Cancel, PostingMode)
	Record = RegisterRecords.ИмуществоНаСкладах.Add();
	Record.Количество = 1;
EndProcedure
`

// thisObjectModule reaches the movements of its own object through ЭтотОбъект;
// the movements of another object are not the document's own.
const thisObjectModule = `Процедура ОбработкаПроведения(Отказ, РежимПроведения)
	ЭтотОбъект.Движения.ИмуществоВПути.Записывать = Истина;
	Запись = ЭтотОбъект.Движения.ИмуществоВПути.Добавить();
	Запись.Сумма = 1;
	Документ.Движения.ИмуществоНаСкладах.Записывать = Истина;
КонецПроцедуры
`

// overridableModule holds one extension point whose parameter list is wrapped
// and whose first line carries a ")" inside a string default: counting
// parentheses by line ended the declaration before Export and lost the point.
const overridableModule = `// Called when the property is moved between warehouses.
Procedure ПриПеремещенииИмущества(Разделитель = ")", // the separator (one char)
	Отказ) Export
	Отказ = False;
EndProcedure

Procedure НеТочка(Документ) // Export is only mentioned here
EndProcedure
`

// bom opens every file of a real export.
const bom = "\ufeff"

func parseDocumentXML(name string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Document uuid="9d444444-0000-0000-0000-000000000001">
		<Properties>
			<Name>` + name + `</Name>
			<Posting>Allow</Posting>
			<RegisterRecords>
				<xr:Item>AccumulationRegister.ИмуществоНаСкладах</xr:Item>
				<xr:Item>AccumulationRegister.ИмуществоВПути</xr:Item>
			</RegisterRecords>
		</Properties>
	</Document>
</MetaDataObject>`
}

// writeParseDump lays out a small export: two documents with English object
// modules and one overridable common module.
func writeParseDump(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(bom+content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for doc, module := range map[string]string{
		"ПеремещениеИмущества": englishObjectModule,
		"СписаниеИмущества":    englishObjectModuleNoFlag,
		"ВозвратИмущества":     thisObjectModule,
	} {
		write(workspace.DumpDeclarationPath("Document", doc), parseDocumentXML(doc))
		write(workspace.DumpModulePath("Document", doc, workspace.ModuleObject), module)
	}
	write(workspace.DumpModulePath("CommonModule", "ИмуществоПереопределяемый", workspace.ModuleCommon), overridableModule)
	return root
}

func TestContextPackExportsFromParser(t *testing.T) {
	s := NewXMLSource(writeParseDump(t))
	pack, err := s.ContextPack(context.Background(), "Document", "ПеремещениеИмущества", ContextPackOptions{})
	if err != nil {
		t.Fatalf("ContextPack: %v", err)
	}
	obj := moduleByKind(pack, "ObjectModule")
	if obj == nil {
		t.Fatalf("ObjectModule missing: %+v", pack.Modules)
	}
	want := []string{`Function ЗаполнитьПоОснованию(Основание, Val Режим = "(full)")`}
	if strings.Join(obj.Exports, "\n") != strings.Join(want, "\n") {
		t.Errorf("exports = %q, want %q", obj.Exports, want)
	}
}

func TestExtensionPointsFromParser(t *testing.T) {
	s := NewXMLSource(writeParseDump(t))
	rep, err := s.ExtensionPoints(context.Background(), "перемещение имущества", 10)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	if len(rep.Points) != 1 {
		t.Fatalf("points = %v, want the one exported procedure", pointNames(rep))
	}
	p := rep.Points[0]
	if p.Procedure != "ПриПеремещенииИмущества" || p.Line != 2 {
		t.Errorf("point = %s at line %d", p.Procedure, p.Line)
	}
	if want := `Procedure ПриПеремещенииИмущества(Разделитель = ")", Отказ) Export`; p.Signature != want {
		t.Errorf("signature = %q, want %q", p.Signature, want)
	}
	if p.Summary != "Called when the property is moved between warehouses." || !p.Implemented {
		t.Errorf("summary = %q, implemented = %v", p.Summary, p.Implemented)
	}
}

func TestMovementsRegisterRecords(t *testing.T) {
	s := NewXMLSource(writeParseDump(t))
	rep, err := s.Movements(context.Background(), "ПеремещениеИмущества")
	if err != nil {
		t.Fatalf("Movements: %v", err)
	}
	byName := map[string]RegisterMovement{}
	for _, r := range rep.Registers {
		byName[r.Register] = r
	}
	if len(byName) != 2 {
		t.Fatalf("registers = %+v", rep.Registers)
	}
	stock := byName["РегистрНакопления.ИмуществоНаСкладах"]
	if !stock.Declared || !stock.UsedInCode || !stock.WriteFlag ||
		strings.Join(stock.FieldsSet, ",") != "Количество,Период" {
		t.Errorf("ИмуществоНаСкладах = %+v", stock)
	}
	transit := byName["РегистрНакопления.ИмуществоВПути"]
	if !transit.Declared || transit.UsedInCode || transit.WriteFlag {
		t.Errorf("ИмуществоВПути = %+v", transit)
	}
}

func TestPostingReviewRegisterRecords(t *testing.T) {
	s := NewXMLSource(writeParseDump(t))
	ctx := context.Background()

	rep, err := s.PostingReview(ctx, "ПеремещениеИмущества")
	if err != nil {
		t.Fatalf("PostingReview: %v", err)
	}
	if rep.Style != postingInline || strings.Join(rep.Handlers, ",") != "Posting" {
		t.Fatalf("style = %q, handlers = %v", rep.Style, rep.Handlers)
	}
	if findingCodes(rep)["NoWriteFlag"] {
		t.Errorf("the Write flag is raised, NoWriteFlag is a false positive: %+v", rep.Findings)
	}

	rep, err = s.PostingReview(ctx, "СписаниеИмущества")
	if err != nil {
		t.Fatalf("PostingReview: %v", err)
	}
	if rep.Style != postingInline || !findingCodes(rep)["NoWriteFlag"] {
		t.Errorf("a filled set that is never written must be reported: style = %q, findings = %+v", rep.Style, rep.Findings)
	}
}

func TestWritePathRegisterRecords(t *testing.T) {
	s := NewXMLSource(writeParseDump(t))
	rep, err := s.WritePath(context.Background(), "Document", "ПеремещениеИмущества")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	for _, step := range rep.Steps {
		if step.Kind == "movements" && step.Source == "РегистрНакопления.ИмуществоНаСкладах" {
			if !strings.Contains(step.Detail, "Записывать = Истина найдено") ||
				!strings.Contains(step.Detail, "Количество, Период") {
				t.Errorf("movement step = %+v", step)
			}
			return
		}
	}
	t.Errorf("no movement step for ИмуществоНаСкладах: %+v", rep.Steps)
}

func TestMovementsThroughThisObject(t *testing.T) {
	s := NewXMLSource(writeParseDump(t))
	rep, err := s.Movements(context.Background(), "ВозвратИмущества")
	if err != nil {
		t.Fatalf("Movements: %v", err)
	}
	byName := map[string]RegisterMovement{}
	for _, r := range rep.Registers {
		byName[r.Register] = r
	}
	transit := byName["РегистрНакопления.ИмуществоВПути"]
	if !transit.UsedInCode || !transit.WriteFlag || strings.Join(transit.FieldsSet, ",") != "Сумма" {
		t.Errorf("ИмуществоВПути = %+v", transit)
	}
	if stock := byName["РегистрНакопления.ИмуществоНаСкладах"]; stock.UsedInCode || stock.WriteFlag {
		t.Errorf("movements of another object leaked: %+v", stock)
	}
}
