package source

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// The declarations below break a header regular expression: the annotation or
// the directive sits on the same line as the keyword. The parser reads them as
// any other declaration, and a commented-out one stays out.

func TestParseInterceptorsFromParser(t *testing.T) {
	module := `&НаСервере
&Перед("ПередЗаписью") Процедура Расш_ПередЗаписью(Отказ)
КонецПроцедуры

&After("OnWrite")
// the handler of the base configuration writes the log
Procedure Ext_OnWrite(Cancel)
EndProcedure

// &Вместо("Закомментирован")
// Процедура Расш_Закомментирован()
`
	want := []Interceptor{
		{Kind: "Перед", Target: "ПередЗаписью", Method: "Расш_ПередЗаписью"},
		{Kind: "После", Target: "OnWrite", Method: "Ext_OnWrite"},
	}
	if got := parseInterceptors([]byte(module)); !reflect.DeepEqual(got, want) {
		t.Errorf("interceptors = %+v, want %+v", got, want)
	}
}

func TestObjectModuleHandlersFromParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ObjectModule.bsl")
	module := bom + `&НаСервере Процедура ПередЗаписью(Отказ)
КонецПроцедуры

// Процедура ПриЗаписи(Отказ)
// КонецПроцедуры
`
	if err := os.WriteFile(path, []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	got := objectModuleHandlers(path)
	if !got["ПередЗаписью"] || got["ПриЗаписи"] {
		t.Errorf("handlers = %v, want ПередЗаписью only", got)
	}
}

// The modules below break the header regular expression in another way: the
// keyword and the name of a method stand on different lines, the parameter
// list is wrapped, and the keywords are English. BSL allows all of it.

// writeModule writes a module under the export root at a path of the dump
// layout.
func writeModule(t *testing.T, root, rel, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMultilineEnglishExtensionContext(t *testing.T) {
	base := copyFixture(t, "testdata/base")
	original := "&AtServer Procedure\n" +
		"\tПриСозданииНаСервере(Cancel, // the form is opened\n" +
		"\t\tStandardProcessing) Export\n" +
		"\tЗаполнитьЗначенияПоУмолчанию();\n" +
		"EndProcedure"
	writeModule(t, base, workspace.DumpFormModulePath("Document", "ЗаказКлиента", "ФормаДокумента"),
		bom+"Procedure Предыдущая()\nEndProcedure\n\n"+original+
			"\n\nProcedure ЗаполнитьЗначенияПоУмолчанию()\nEndProcedure\n")

	ec, err := NewXMLSource("testdata/ext").ExtensionContext(context.Background(), base)
	if err != nil {
		t.Fatalf("ExtensionContext: %v", err)
	}
	for _, ic := range ec.Interceptors {
		if ic.Target == "ПриСозданииНаСервере" {
			if ic.Original != original {
				t.Errorf("original =\n%s\nwant\n%s", ic.Original, original)
			}
			return
		}
	}
	t.Fatalf("interceptor of ПриСозданииНаСервере not found: %+v", ec.Interceptors)
}

func TestMultilineEnglishExchangeAudit(t *testing.T) {
	root := copyFixture(t, "testdata/wp")
	rel := workspace.DumpModulePath("CommonModule", "ОбработчикиЗаказа", workspace.ModuleCommon)
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	old := "Процедура ЗарегистрироватьДокумент(Источник, Отказ, РежимЗаписи, РежимПроведения) Экспорт\n\n" +
		"\tПланыОбмена.ЗарегистрироватьИзменения(Узел, Источник);\n\nКонецПроцедуры"
	if !strings.Contains(string(data), old) {
		t.Fatalf("fixture changed, the registering handler is not found in %s", rel)
	}
	multiline := "Procedure\n\tЗарегистрироватьДокумент(Источник, Отказ,\n\t\tРежимЗаписи, РежимПроведения) Export\n\n" +
		"\tExchangePlans.ЗарегистрироватьИзменения(Узел, Источник);\n\nEndProcedure"
	writeModule(t, root, rel, strings.Replace(string(data), old, multiline, 1))

	rep, err := NewXMLSource(root).ExchangeAudit(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("ExchangeAudit: %v", err)
	}
	for _, r := range rep.Registrars {
		if r.Handler == "CommonModule.ОбработчикиЗаказа.ЗарегистрироватьДокумент" {
			return
		}
	}
	t.Errorf("the registering handler was not found: registrars %+v, findings %+v", rep.Registrars, rep.Findings)
}

func TestMultilineEnglishFormImpact(t *testing.T) {
	draft := &DraftOptions{
		Extension: "МоёРасширение",
		Prefix:    "моё_",
		Code: `&AtServer
&After("ПриСозданииНаСервере")
Procedure
	моё_ПриСозданииНаСервере(Cancel,
		StandardProcessing)
	Элементы.Добавить("моё_Поле", Тип("ПолеФормы"));
	моё_Оформить();
EndProcedure

Procedure моё_Оформить()
	Элементы.Удалить(Элементы.моё_Старое);
EndProcedure`,
	}
	fi, err := NewXMLSource("testdata/fi/base").FormImpact(context.Background(),
		"Document", "ЗаказКлиента", "ФормаДокумента", []string{"testdata/fi/base"}, draft)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	for _, src := range fi.Sources {
		if src.Extension != "МоёРасширение" {
			continue
		}
		if src.Kind != "После" || src.Target != "ПриСозданииНаСервере" || src.Method != "моё_ПриСозданииНаСервере" {
			t.Errorf("draft source = %+v", src)
		}
		var got []string
		for _, e := range src.Changes {
			got = append(got, e.Kind+"|"+e.Target+"|"+strconv.Itoa(e.Line))
		}
		// The helper's edit is charged to the interceptor that calls it.
		want := []string{"ДобавитьЭлемент|моё_Поле|6", "УдалитьЭлемент|Элементы.моё_Старое|11"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("changes = %v, want %v", got, want)
		}
		return
	}
	t.Fatalf("the draft was not analysed: sources %+v, conflicts %+v", fi.Sources, fi.Conflicts)
}

func TestMultilineEnglishAccessProfiles(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, workspace.DumpModulePath("CommonModule", "УправлениеДоступомПереопределяемый", workspace.ModuleCommon), bom+`Procedure
	ПриЗаполненииПоставляемыхПрофилейГруппДоступа(ОписанияПрофилей,
		ПараметрыОбновления) Export
	ОписаниеПрофиля = УправлениеДоступомБСП.НовоеОписаниеПрофиляГруппДоступа();
	ОписаниеПрофиля.Имя = "Кладовщик";
	ОписаниеПрофиля.Роли.Добавить("ЧтениеТоваров");
	ОписанияПрофилей.Добавить(ОписаниеПрофиля);
EndProcedure

Procedure
	ДополнитьПрофильКассир(ОписаниеПрофиля) Export
	ОписаниеПрофиля.Роли.Добавить("ПробитиеЧеков");
EndProcedure
`)
	profiles, err := NewXMLSource(root).AccessProfiles()
	if err != nil {
		t.Fatalf("AccessProfiles: %v", err)
	}
	var got []string
	for _, p := range profiles {
		got = append(got, p.Name+"|"+p.Procedure+"|"+strings.Join(p.Roles, ","))
	}
	// Each role belongs to the procedure it is added in: the second procedure
	// adds to a profile of its own, not to the last profile of the first one.
	want := []string{
		"Кладовщик|ПриЗаполненииПоставляемыхПрофилейГруппДоступа|ЧтениеТоваров",
		"|ДополнитьПрофильКассир|ПробитиеЧеков",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("profiles = %v, want %v", got, want)
	}
}

// TestMethodByPathUnclosed: a module cut short in the export has no closing
// keyword. The method ends before the line of the next declaration, whose
// directive must not be charged to it.
func TestMethodByPathUnclosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Module.bsl")
	module := "&НаСервере\nПроцедура Оборванная()\n\tСообщить(1);\n\n&НаКлиенте\nПроцедура Следующая()\nКонецПроцедуры\n"
	if err := os.WriteFile(path, []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	mods := moduleCache{}
	if got, want := mods.methodByPath(path, "оборванная"), "&НаСервере\nПроцедура Оборванная()\n\tСообщить(1);\n"; got != want {
		t.Errorf("unclosed = %q, want %q", got, want)
	}
	if got, want := mods.methodByPath(path, "Следующая"), "&НаКлиенте\nПроцедура Следующая()\nКонецПроцедуры"; got != want {
		t.Errorf("next = %q, want %q", got, want)
	}
	if len(mods) != 1 {
		t.Errorf("module parsed into %d cache entries, want 1", len(mods))
	}
}

// TestAccessProfilesSkipCommentedCode: БСП quotes usage examples in comments
// inside procedure bodies too (a comment before the first statement is outside
// the body span anyway, so the example stands after it). A commented-out profile is not a profile, while
// "//" inside a string literal is not a comment.
func TestAccessProfilesSkipCommentedCode(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, workspace.DumpModulePath("CommonModule", "УправлениеДоступомПереопределяемый", workspace.ModuleCommon), `Процедура ПриЗаполненииПоставляемыхПрофилейГруппДоступа(ОписанияПрофилей, ПараметрыОбновления) Экспорт
	ОписаниеПрофиля = УправлениеДоступомБСП.НовоеОписаниеПрофиляГруппДоступа();
	// Пример:
	//  ОписаниеПрофиля.Имя = "Менеджер";
	//  ОписаниеПрофиля.Роли.Добавить("ЧтениеВсех");
	ОписаниеПрофиля.Имя = "Кладовщик"; // профиль склада
	ОписаниеПрофиля.Наименование = "Кладовщик // склад";
	ОписаниеПрофиля.Роли.Добавить("ЧтениеТоваров"); // ОписаниеПрофиля.Роли.Добавить("Лишняя");
	ОписанияПрофилей.Добавить(ОписаниеПрофиля);
КонецПроцедуры
`)
	profiles, err := NewXMLSource(root).AccessProfiles()
	if err != nil {
		t.Fatalf("AccessProfiles: %v", err)
	}
	var got []string
	for _, p := range profiles {
		got = append(got, p.Name+"|"+p.Description+"|"+strings.Join(p.Roles, ","))
	}
	want := []string{"Кладовщик|Кладовщик // склад|ЧтениеТоваров"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("profiles = %v, want %v", got, want)
	}
}
