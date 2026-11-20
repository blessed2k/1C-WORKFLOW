package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

func cliWrite(t *testing.T, root, rel, text string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// cliFixture: a configuration with one server common module, and an
// extension dump next to it that the manifest does not list: the CLI finds it
// by its Configuration.xml.
func cliFixture(t *testing.T) (cfg, ext string) {
	dir := t.TempDir()
	cfg, ext = filepath.Join(dir, "cfg"), filepath.Join(dir, "ext")
	hdr := `<?xml version="1.0" encoding="UTF-8"?><MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">`
	cliWrite(t, cfg, "Configuration.xml", hdr+`<Configuration><Properties><Name>Тест</Name></Properties><ChildObjects><CommonModule>ОбщийСервер</CommonModule></ChildObjects></Configuration></MetaDataObject>`)
	cliWrite(t, cfg, workspace.DumpDeclarationPath("CommonModule", "ОбщийСервер"),
		hdr+`<CommonModule><Properties><Name>ОбщийСервер</Name><Server>true</Server></Properties></CommonModule></MetaDataObject>`)
	cliWrite(t, cfg, workspace.DumpModulePath("CommonModule", "ОбщийСервер", workspace.ModuleCommon),
		"Функция Метод() Экспорт\nКонецФункции\n")
	cliWrite(t, ext, "Configuration.xml", hdr+`<Configuration><Properties><ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose></Properties></Configuration></MetaDataObject>`)
	return cfg, ext
}

func runCLI(t *testing.T, stdin string, args ...string) (int, validateCLIReport, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runValidateBSL(append([]string{"-syntax-index", syntaxtest.FixtureFile(t)}, args...), strings.NewReader(stdin), &out, &errb)
	var rep validateCLIReport
	if code != 2 {
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("output is not one JSON report: %v\n%s", err, out.String())
		}
	}
	return code, rep, errb.String()
}

// TestValidateCLIExitCodes: high findings fail the step, a clean file passes,
// and a run without files or a project is a usage error, not a pass.
func TestValidateCLIExitCodes(t *testing.T) {
	cfg, ext := cliFixture(t)
	bad := cliWrite(t, ext, workspace.DumpModulePath("CommonModule", "Плохой", workspace.ModuleCommon), `
Процедура Тест() Экспорт
	ОбщийСервер.НетТакогоМетода();
КонецПроцедуры

Процедура Тест2() Экспорт
	ОбщийСервер = 1;
КонецПроцедуры`)
	good := cliWrite(t, ext, workspace.DumpModulePath("CommonModule", "Хороший", workspace.ModuleCommon), `
Процедура Тест() Экспорт
	ОбщийСервер.Метод();
КонецПроцедуры`)

	code, rep, stderr := runCLI(t, "", "-project", cfg, bad, good)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %s)", code, stderr)
	}
	if rep.Checked != 2 || rep.High != 2 || len(rep.Files) != 1 || rep.Files[0].Path != bad {
		t.Fatalf("report = %+v", rep)
	}
	got := []string{rep.Files[0].Findings[0].Code, rep.Files[0].Findings[1].Code}
	if got[0] != "UnknownCommonModuleMethod" || got[1] != "AssignToGlobalName" {
		t.Errorf("findings = %v", got)
	}

	if code, rep, _ := runCLI(t, good+"\n"+filepath.Join(ext, "удалён.bsl")+"\n", "-project", cfg, "-files-from", "-"); code != 0 || rep.Checked != 1 || len(rep.Skipped) != 1 {
		t.Errorf("stdin list: exit %d, report %+v", code, rep)
	}
	if code, rep, _ := runCLI(t, "", "-project", cfg, "-min-severity", "high", ext); code != 1 || rep.Checked != 2 {
		t.Errorf("directory walk: exit %d, report %+v", code, rep)
	}
	if code, _, _ := runCLI(t, ""); code != 2 {
		t.Errorf("no files: exit %d, want 2", code)
	}
	if code, _, _ := runCLI(t, "", "-project", filepath.Join(cfg, "нет"), good); code != 2 {
		t.Errorf("no project: exit %d, want 2", code)
	}
}
