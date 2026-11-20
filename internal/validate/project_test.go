package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

const mdHeader = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
`

func writeFile(t *testing.T, root, rel, text string) string {
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

func commonModuleXML(name string, props string) string {
	return mdHeader + "\t<CommonModule uuid=\"1\">\n\t\t<Properties>\n\t\t\t<Name>" + name + "</Name>\n" + props +
		"\t\t</Properties>\n\t</CommonModule>\n</MetaDataObject>\n"
}

func documentXML(name, props string, attrs ...string) string {
	var b strings.Builder
	b.WriteString(mdHeader + "\t<Document uuid=\"1\">\n\t\t<Properties>\n" + props + "\t\t\t<Name>" + name + "</Name>\n\t\t</Properties>\n\t\t<ChildObjects>\n")
	for _, a := range attrs {
		kind := "Attribute"
		if strings.HasPrefix(a, "ТЧ:") {
			kind, a = "TabularSection", strings.TrimPrefix(a, "ТЧ:")
		}
		b.WriteString("\t\t\t<" + kind + " uuid=\"2\">\n\t\t\t\t<Properties>\n\t\t\t\t\t<Name>" + a + "</Name>\n\t\t\t\t</Properties>\n")
		if kind == "TabularSection" {
			b.WriteString("\t\t\t\t<ChildObjects><Attribute uuid=\"3\"><Properties><Name>ВложенныйРеквизит</Name></Properties></Attribute></ChildObjects>\n")
		}
		b.WriteString("\t\t\t</" + kind + ">\n")
	}
	b.WriteString("\t\t</ChildObjects>\n\t</Document>\n</MetaDataObject>\n")
	return b.String()
}

// fixtureProject lays out a configuration and an extension of it, the way
// DumpConfigToFiles does.
func fixtureProject(t *testing.T) (cfg, ext string) {
	t.Helper()
	dir := t.TempDir()
	cfg, ext = filepath.Join(dir, "cfg"), filepath.Join(dir, "ext")
	writeFile(t, cfg, "Configuration.xml", mdHeader+"<Configuration><Properties><Name>Тест</Name></Properties></Configuration></MetaDataObject>")
	writeFile(t, ext, "Configuration.xml", mdHeader+"<Configuration><Properties><ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose></Properties></Configuration></MetaDataObject>")

	writeFile(t, cfg, workspace.DumpDeclarationPath("CommonModule", "ОбщийСервер"),
		commonModuleXML("ОбщийСервер", "\t\t\t<Global>false</Global>\n\t\t\t<Server>true</Server>\n\t\t\t<ClientManagedApplication>false</ClientManagedApplication>\n"))
	writeFile(t, cfg, workspace.DumpModulePath("CommonModule", "ОбщийСервер", workspace.ModuleCommon),
		"Функция Метод() Экспорт\nКонецФункции\n\nПроцедура Служебная()\nКонецПроцедуры\n")
	writeFile(t, cfg, workspace.DumpDeclarationPath("CommonModule", "ГлобальныйМодуль"),
		commonModuleXML("ГлобальныйМодуль", "\t\t\t<Global>true</Global>\n\t\t\t<Server>true</Server>\n\t\t\t<ClientManagedApplication>true</ClientManagedApplication>\n"))
	writeFile(t, cfg, workspace.DumpModulePath("CommonModule", "ГлобальныйМодуль", workspace.ModuleCommon),
		"Функция ГлобальнаяФункция() Экспорт\nКонецФункции\n")
	writeFile(t, cfg, workspace.DumpDeclarationPath("CommonModule", "ВызовСервераТест"),
		commonModuleXML("ВызовСервераТест", "\t\t\t<Server>true</Server>\n\t\t\t<ServerCall>true</ServerCall>\n\t\t\t<ClientManagedApplication>false</ClientManagedApplication>\n"))
	writeFile(t, cfg, workspace.DumpDeclarationPath("Document", "Заявка"),
		documentXML("Заявка", "", "Сумма", "РучнаяКорректировка", "ТЧ:Товары"))
	writeFile(t, cfg, workspace.DumpModulePath("Document", "Заявка", workspace.ModuleObject),
		"Процедура БазоваяПроцедура()\nКонецПроцедуры\n")

	// The extension borrows ОбщийСервер and adds a method to it, has a module
	// of its own, borrows Заявка with one attribute and adds a data processor
	// with a form.
	writeFile(t, ext, workspace.DumpDeclarationPath("CommonModule", "ОбщийСервер"),
		commonModuleXML("ОбщийСервер", "\t\t\t<ObjectBelonging>Adopted</ObjectBelonging>\n\t\t\t<Server>true</Server>\n"))
	writeFile(t, ext, workspace.DumpModulePath("CommonModule", "ОбщийСервер", workspace.ModuleCommon),
		"Функция ДобавленВРасширении() Экспорт\nКонецФункции\n")
	writeFile(t, ext, workspace.DumpDeclarationPath("CommonModule", "МодульРасширения"),
		commonModuleXML("МодульРасширения", "\t\t\t<Server>true</Server>\n"))
	writeFile(t, ext, workspace.DumpModulePath("CommonModule", "МодульРасширения", workspace.ModuleCommon),
		"Функция Есть() Экспорт\nКонецФункции\n")
	writeFile(t, ext, workspace.DumpDeclarationPath("Document", "Заявка"),
		documentXML("Заявка", "\t\t\t<ObjectBelonging>Adopted</ObjectBelonging>\n", "Сумма"))
	formModule := workspace.DumpFormModulePath("DataProcessor", "Обработка", "Форма")
	writeFile(t, ext, strings.TrimSuffix(formModule, "/Form/Module.bsl")+"/Form.xml",
		`<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.20">
	<Attributes>
		<Attribute name="Объект" id="1"/>
		<Attribute name="Документы" id="2">
			<Columns><Column name="Ссылка" id="1"/></Columns>
		</Attribute>
	</Attributes>
</Form>`)
	return cfg, ext
}

func members(prefixes ...string) map[string]bool {
	for _, p := range prefixes {
		if strings.HasPrefix(p, "ДокументОбъект.") {
			return map[string]bool{"номер": true, "дата": true, "ссылка": true, "записать": true}
		}
	}
	return map[string]bool{"закрыть": true}
}

func fixtureEnv(p *DiskProject, path string) Env {
	return Env{
		Method:         platform,
		GlobalProperty: globalProperty,
		Project:        p,
		Module:         ResolveModule(p, ModuleInput{Path: path}, members),
	}
}

// TestDiskProjectCommonModules: a common module merges over the configuration
// and the extension that borrows it; a module known only as borrowed is not
// complete without the configuration.
func TestDiskProjectCommonModules(t *testing.T) {
	cfg, ext := fixtureProject(t)
	p := NewDiskProject([]Component{{ID: "cfg", Root: cfg}, {ID: "ext", Root: ext, Extension: true}})

	cm, ok := p.CommonModule("общийсервер")
	if !ok || !cm.Complete || cm.Availability != (Availability{Server: true}) {
		t.Fatalf("ОбщийСервер = %+v, %v", cm, ok)
	}
	for _, m := range []string{"метод", "добавленврасширении"} {
		if !cm.Methods[m].Export {
			t.Errorf("%s must be an export method of the merged module: %+v", m, cm.Methods)
		}
	}
	if cm.Methods["служебная"].Export {
		t.Error("Служебная is not exported")
	}
	// «Вызов сервера»: the name is visible on the client, the code runs on the
	// server only.
	sc, ok := p.CommonModule("ВызовСервераТест")
	if !ok || sc.Availability != (Availability{Client: true, Server: true}) || sc.Runs != (Availability{Server: true}) {
		t.Errorf("ВызовСервераТест = %+v", sc)
	}
	draft := writeFile(t, cfg, workspace.DumpModulePath("CommonModule", "ВызовСервераТест", workspace.ModuleCommon), "")
	if mc := ResolveModule(p, ModuleInput{Path: draft}, members); mc.Default != (Availability{Server: true}) {
		t.Errorf("a method of a server-call module runs on the server: %+v", mc.Default)
	}
	if !p.GlobalExport("ГлобальнаяФункция") || p.GlobalExport("Метод") {
		t.Error("only exports of global modules are global")
	}
	if !p.Complete() {
		t.Error("the configuration is a component")
	}

	onlyExt := NewDiskProject([]Component{{ID: "ext", Root: ext, Extension: true}})
	if cm, ok := onlyExt.CommonModule("ОбщийСервер"); !ok || cm.Complete {
		t.Errorf("a borrowed module alone is not complete: %+v", cm)
	}
	if cm, ok := onlyExt.CommonModule("МодульРасширения"); !ok || !cm.Complete {
		t.Errorf("the extension's own module is complete: %+v", cm)
	}
	if onlyExt.Complete() {
		t.Error("an extension alone is not a complete project")
	}
}

// TestExtensionObjectModuleFromDisk: the borrowed object module sees the base
// module's methods and its own attributes; the base attribute it did not
// borrow is caught, through the whole chain from the path.
func TestExtensionObjectModuleFromDisk(t *testing.T) {
	cfg, ext := fixtureProject(t)
	p := NewDiskProject([]Component{{ID: "cfg", Root: cfg}, {ID: "ext", Root: ext, Extension: true}})
	path := writeFile(t, ext, workspace.DumpModulePath("Document", "Заявка", workspace.ModuleObject), `
&После("ОбработкаПроведения")
Процедура Расш_ОбработкаПроведения(Отказ, РежимПроведения)
	БазоваяПроцедура();
	ГлобальнаяФункция();
	Если Сумма > 0 И РучнаяКорректировка Тогда
		ОбщийСервер.ДобавленВРасширении();
		ОбщийСервер.Служебная();
		МодульРасширения.НетТакого();
	КонецЕсли;
	Для Номер = 1 По 2 Цикл
	КонецЦикла;
КонецПроцедуры`)
	e := fixtureEnv(p, path)
	if e.Module.Kind != KindObject || e.Module.Extension != "ext" || e.Module.Unborrowed["ручнаякорректировка"] == "" {
		t.Fatalf("module context = %+v", e.Module)
	}
	data, _ := os.ReadFile(path)
	check(t, "extension object module", string(data), e,
		want{"UnborrowedAttribute", "high", 6},
		want{"UnknownCommonModuleMethod", "high", 8},
		want{"UnknownCommonModuleMethod", "high", 9},
		want{"LoopCounterIsAttribute", "high", 11})
}

// TestFormModuleFromDisk: the form's attributes come from Form.xml, and the
// directive check knows the module is a form's.
func TestFormModuleFromDisk(t *testing.T) {
	cfg, ext := fixtureProject(t)
	p := NewDiskProject([]Component{{ID: "cfg", Root: cfg}, {ID: "ext", Root: ext, Extension: true}})
	path := writeFile(t, ext, workspace.DumpFormModulePath("DataProcessor", "Обработка", "Форма"), `
&НаСервере
Процедура Заполнить()
	Документы = Неопределено;
	ОбщийСервер = 1;
КонецПроцедуры

&НаКлиентеНаСервере
Процедура Общая()
	Закрыть();
КонецПроцедуры`)
	e := fixtureEnv(p, path)
	if e.Module.Kind != KindForm || !e.Module.ContextComplete || !e.Module.ContextNames["документы"] {
		t.Fatalf("form context = %+v", e.Module)
	}
	data, _ := os.ReadFile(path)
	check(t, "form module", string(data), e,
		want{"AssignToGlobalName", "high", 5},
		want{"FormDirectiveClientServer", "high", 8})
}

// TestClassifyPath reads the module kind from the dump layout.
func TestClassifyPath(t *testing.T) {
	cases := []struct {
		path string
		kind ModuleKind
		obj  string
	}{
		{workspace.DumpModulePath("CommonModule", "М", workspace.ModuleCommon), KindCommon, "М"},
		{workspace.DumpModulePath("Document", "Д", workspace.ModuleObject), KindObject, "Д"},
		{workspace.DumpModulePath("Document", "Д", workspace.ModuleManager), KindManager, "Д"},
		{workspace.DumpModulePath("InformationRegister", "Р", workspace.ModuleRecordSet), KindRecordSet, "Р"},
		{workspace.DumpFormModulePath("Catalog", "С", "ФормаЭлемента"), KindForm, "С"},
		{"CommonForms/ОбщаяФорма/Ext/Form/Module.bsl", KindForm, "ОбщаяФорма"},
		{"Ext/ManagedApplicationModule.bsl", KindApplication, ""},
		{"Обработка/Обработка/Ext/ObjectModule.bsl", KindObject, "Обработка"},
	}
	for _, c := range cases {
		pm, ok := ClassifyPath(c.path)
		if !ok || pm.Kind != c.kind || pm.Object != c.obj {
			t.Errorf("%s: %+v %v, want %s %s", c.path, pm, ok, c.kind, c.obj)
		}
	}
	if _, ok := ClassifyPath("readme.bsl"); ok {
		t.Error("a loose file has no kind")
	}
}
