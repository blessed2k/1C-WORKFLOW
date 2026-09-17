package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func conflictByCode(fi *FormImpact) map[string]FormConflict {
	m := make(map[string]FormConflict, len(fi.Conflicts))
	for _, c := range fi.Conflicts {
		m[c.Code] = c
	}
	return m
}

// fiReport analyses the form across the base configuration and extensions:
// extA — &После with ИзменитьРеквизиты (legitimate), extB — &Вместо WITHOUT
// ПродолжитьВызов (kills the base method), extC — the correct &Перед variant.
func fiReport(t *testing.T, dumps ...string) *FormImpact {
	t.Helper()
	if len(dumps) == 0 {
		dumps = []string{"testdata/fi/base", "testdata/fi/extA", "testdata/fi/extB", "testdata/fi/extC"}
	}
	fi, err := NewXMLSource("testdata/fi/base").
		FormImpact(context.Background(), "Document", "ЗаказКлиента", "ФормаДокумента", dumps, nil)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	return fi
}

func TestFormImpactSources(t *testing.T) {
	fi := fiReport(t)
	if fi.Form != "Документ.ЗаказКлиента.ФормаДокумента" {
		t.Errorf("Form = %q", fi.Form)
	}
	if fi.Sources[0].Extension != "" {
		t.Errorf("base configuration must come first, got %q", fi.Sources[0].Extension)
	}

	var extA, extB *FormModifier
	for i := range fi.Sources {
		switch fi.Sources[i].Extension {
		case "РасширениеА":
			extA = &fi.Sources[i]
		case "РасширениеБ":
			extB = &fi.Sources[i]
		}
	}
	if extA == nil || extB == nil {
		t.Fatalf("extensions not resolved: %+v", fi.Sources)
	}
	// Interceptor kind is captured, not just its label.
	if extA.Kind != "После" || extA.Target != "ПриСозданииНаСервере" || extA.Method != "расшА_ПриСозданииНаСервереПосле" {
		t.Errorf("extA interceptor = %+v", extA)
	}
	if extB.Kind != "Вместо" || extB.HasContinue {
		t.Errorf("extB must be Вместо without ПродолжитьВызов: %+v", extB)
	}
	// Edits made in a helper procedure are charged to the interceptor calling it.
	kinds := map[string]bool{}
	for _, c := range extA.Changes {
		kinds[c.Kind] = true
	}
	for _, want := range []string{"ИзменитьРеквизиты", "ДобавитьЭлемент", "ДобавитьРеквизит"} {
		if !kinds[want] {
			t.Errorf("extA (via helper) missing %q; got %v", want, kinds)
		}
	}
}

// TestFormImpactInsteadWithoutContinue is the provable way print dies silently:
// &Вместо skips the base method that attaches BSP print commands.
func TestFormImpactInsteadWithoutContinue(t *testing.T) {
	c, ok := conflictByCode(fiReport(t))["InsteadWithoutContinueOverPrint"]
	if !ok {
		t.Fatalf("instead-without-continue over print not detected: %+v", fiReport(t).Conflicts)
	}
	if c.Severity != "high" || !strings.Contains(c.Message, "ПродолжитьВызов") || !strings.Contains(c.Message, "печат") {
		t.Errorf("conflict = %+v", c)
	}
}

// TestFormImpactInsteadCopiesBaseWiring guards the precision of the rule above.
// An &Вместо interceptor that is a copy of the base method re-attaches the BSP
// wiring itself, so print does NOT disappear: the high verdict must not fire.
// What is left is the copy falling behind the typical method on the next update.
func TestFormImpactInsteadCopiesBaseWiring(t *testing.T) {
	fi := fiReport(t, "testdata/fi/base", "testdata/fi/extD")
	byCode := conflictByCode(fi)
	if c, ok := byCode["InsteadWithoutContinueOverPrint"]; ok {
		t.Errorf("interceptor re-attaches the wiring itself, print is not lost: %+v", c)
	}
	c, ok := byCode["InsteadDuplicatesBaseWiring"]
	if !ok {
		t.Fatalf("copy of the base method not reported: %+v", fi.Conflicts)
	}
	if c.Severity != "medium" || !strings.Contains(c.Message, "копия типового метода") {
		t.Errorf("conflict = %+v", c)
	}
}

// TestFormImpactBeforeIsNotFlagged guards against punishing the correct fix:
// &Перед runs before the base method, so ИзменитьРеквизиты there is fine.
func TestFormImpactBeforeIsNotFlagged(t *testing.T) {
	fi := fiReport(t, "testdata/fi/base", "testdata/fi/extC")
	for _, c := range fi.Conflicts {
		if strings.Contains(c.Message, "РасширениеВ") && c.Severity == "high" {
			t.Errorf("correct &Перед variant must not be flagged high: %+v", c)
		}
	}
}

// TestFormImpactMultilineAndComparison covers the incident's real shape: calls
// split across lines must still be seen, and "Родитель =" in a condition is a
// comparison, not a form change.
func TestFormImpactMultilineAndComparison(t *testing.T) {
	fi := fiReport(t, "testdata/fi/base", "testdata/fi/extC")
	var extC *FormModifier
	for i := range fi.Sources {
		if fi.Sources[i].Extension == "РасширениеВ" {
			extC = &fi.Sources[i]
		}
	}
	if extC == nil {
		t.Fatal("РасширениеВ not among sources")
	}
	var addedAttr, parent bool
	for _, c := range extC.Changes {
		if c.Kind == "ДобавитьРеквизит" && c.Target == "расшВ_Признак" {
			addedAttr = true
		}
		if c.Kind == "СменитьРодителя" {
			parent = true
		}
	}
	if !addedAttr {
		t.Errorf("multi-line Новый РеквизитФормы not detected: %+v", extC.Changes)
	}
	if parent {
		t.Errorf("comparison Родитель = must not count as a change: %+v", extC.Changes)
	}
}

func TestFormImpactNameCollision(t *testing.T) {
	c, ok := conflictByCode(fiReport(t))["NameCollision"]
	if !ok {
		t.Fatal("name collision not detected")
	}
	// Original case is preserved, and both extensions are named.
	if !strings.Contains(c.Message, "Номенклатура") || len(c.Involved) != 2 {
		t.Errorf("collision = %+v", c)
	}
}

func TestFormImpactForeignMutationAndPrefix(t *testing.T) {
	byCode := conflictByCode(fiReport(t))
	if c, ok := byCode["UnprefixedName"]; !ok || !strings.Contains(c.Message, "Номенклатура") {
		t.Errorf("unprefixed not detected: %+v", c)
	}
	if c, ok := byCode["ForeignElementMutation"]; !ok || !strings.Contains(c.Message, "Переместить") {
		t.Errorf("foreign mutation not detected: %+v", c)
	}
}

// TestFormImpactDuplicateDumps guards the false-positive source: the same export
// passed twice (or with a trailing slash) must not conflict with itself.
func TestFormImpactDuplicateDumps(t *testing.T) {
	fi := fiReport(t, "testdata/fi/base", "testdata/fi/extA", "testdata/fi/extA/")
	for _, c := range fi.Conflicts {
		if c.Code == "MultipleChangeAttributes" || c.Code == "NameCollision" {
			t.Errorf("duplicate dump produced a self-conflict: %+v", c)
		}
	}
	n := 0
	for _, s := range fi.Sources {
		if s.Extension == "РасширениеА" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("РасширениеА counted %d times, want 1", n)
	}
}

func TestFormImpactMultipleChangeAttrs(t *testing.T) {
	// extA and extC both change the attribute set, from different exports.
	c, ok := conflictByCode(fiReport(t, "testdata/fi/base", "testdata/fi/extA", "testdata/fi/extC"))["MultipleChangeAttributes"]
	if !ok {
		t.Fatal("multiple ИзменитьРеквизиты not detected")
	}
	if c.Severity != "medium" || strings.Contains(c.Message, "рвёт") {
		t.Errorf("must be a truthful medium warning, got %+v", c)
	}
}

func TestFormImpactCleanForm(t *testing.T) {
	fi := fiReport(t, "testdata/fi/base")
	if len(fi.Conflicts) != 0 {
		t.Errorf("base alone must have no conflicts; got %+v", fi.Conflicts)
	}
}

func TestFormImpactDumpWithoutForm(t *testing.T) {
	// An export that does not contain this form is skipped silently.
	fi := fiReport(t, "testdata/fi/base", "testdata/dump")
	for _, s := range fi.Sources {
		if strings.Contains(s.Dump, "testdata/dump") {
			t.Errorf("export without the form must be skipped: %+v", s)
		}
	}
}

func TestFormImpactCommonForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CommonForms", "МояФорма", "Ext", "Form")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	module := "&НаСервере\nПроцедура ПриСозданииНаСервере(Отказ, СтандартнаяОбработка)\n\tЭлементы.Добавить(\"Поле\", Тип(\"ПолеФормы\"));\nКонецПроцедуры\n"
	if err := os.WriteFile(filepath.Join(path, "Module.bsl"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := NewXMLSource(dir).FormImpact(context.Background(), "CommonForm", "МояФорма", "", []string{dir}, nil)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	if fi.Form != "ОбщаяФорма.МояФорма" || len(fi.Sources) != 1 {
		t.Errorf("common form = %q, sources = %+v", fi.Form, fi.Sources)
	}
}

func TestFormImpactDefaultsToActiveDump(t *testing.T) {
	fi, err := NewXMLSource("testdata/fi/extA").
		FormImpact(context.Background(), "Document", "ЗаказКлиента", "ФормаДокумента", nil, nil)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	if len(fi.Sources) != 1 || fi.Sources[0].Extension != "РасширениеА" {
		t.Errorf("nil dumps must fall back to the active dump; got %+v", fi.Sources)
	}
}

func TestFormImpactRequiresOwnerAndForm(t *testing.T) {
	s := NewXMLSource("testdata/fi/base")
	if _, err := s.FormImpact(context.Background(), "", "X", "F", nil, nil); err == nil {
		t.Error("expected error for empty owner type")
	}
	if _, err := s.FormImpact(context.Background(), "Document", "ЗаказКлиента", "", nil, nil); err == nil {
		t.Error("expected error for empty form name")
	}
}

// --- Guidance: rules for writing on this form ---

func TestFormGuidance(t *testing.T) {
	fi := fiReport(t)
	joined := strings.Join(fi.Guidance, "\n")
	for _, want := range []string{
		"ПродолжитьВызов",  // base module attaches BSP print
		"префикс",          // naming rule
		"Номенклатура",     // taken name
		"ИзменитьРеквизиты", // attribute-set rule
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("guidance missing %q:\n%s", want, joined)
		}
	}
}

func TestFormGuidanceCleanForm(t *testing.T) {
	dir := t.TempDir()
	fi, err := NewXMLSource(dir).FormImpact(context.Background(), "CommonForm", "Пустая", "", []string{dir}, nil)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	if len(fi.Guidance) == 0 {
		t.Error("guidance must be given even for a form nobody touches")
	}
}

// --- Draft: checking code that is not written yet ---

// TestFormImpactDraftCatchesIncident is the point of draftCode: the mistake is
// caught BEFORE the code is applied.
func TestFormImpactDraftCatchesIncident(t *testing.T) {
	draft := &DraftOptions{
		Extension: "МоёРасширение",
		Prefix:    "моё_",
		Code: `&Вместо("ПриСозданииНаСервере")
Процедура моё_ПриСозданииНаСервереВместо(Отказ, СтандартнаяОбработка)
	Поле = Элементы.Добавить("Номенклатура", Тип("ПолеФормы"), Элементы.ГруппаШапка);
КонецПроцедуры`,
	}
	fi, err := NewXMLSource("testdata/fi/base").FormImpact(context.Background(),
		"Document", "ЗаказКлиента", "ФормаДокумента",
		[]string{"testdata/fi/base", "testdata/fi/extA"}, draft)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	byCode := conflictByCode(fi)
	// &Вместо without ПродолжитьВызов over a form with BSP print.
	c, ok := byCode["InsteadWithoutContinueOverPrint"]
	if !ok || !strings.Contains(c.Message, "МоёРасширение") {
		t.Errorf("draft's &Вместо not caught: %+v", fi.Conflicts)
	}
	// The name is already taken by РасширениеА.
	if c, ok := byCode["NameCollision"]; !ok || !strings.Contains(c.Message, "Номенклатура") {
		t.Errorf("draft's name collision not caught: %+v", c)
	}
	// The draft ignores its own prefix.
	if c, ok := byCode["UnprefixedName"]; !ok || !strings.Contains(c.Message, "моё_") {
		t.Errorf("draft's unprefixed name not caught: %+v", c)
	}
}

// TestFormImpactDraftClean guards against crying wolf: a correctly written
// draft must pass.
func TestFormImpactDraftClean(t *testing.T) {
	draft := &DraftOptions{
		Extension: "МоёРасширение",
		Prefix:    "моё_",
		Code: `&После("ПриСозданииНаСервере")
Процедура моё_ПриСозданииНаСервереПосле(Отказ, СтандартнаяОбработка)
	ДР = Новый Массив;
	ДР.Добавить(Новый РеквизитФормы("моё_Признак", Новый ОписаниеТипов("Булево")));
	ИзменитьРеквизиты(ДР);
	Поле = Элементы.Добавить("моё_Поле", Тип("ПолеФормы"), Элементы.ГруппаШапка);
КонецПроцедуры`,
	}
	fi, err := NewXMLSource("testdata/fi/base").FormImpact(context.Background(),
		"Document", "ЗаказКлиента", "ФормаДокумента",
		[]string{"testdata/fi/base"}, draft)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	for _, c := range fi.Conflicts {
		if c.Severity == "high" {
			t.Errorf("correct draft must not raise high: %+v", c)
		}
	}
}

// TestFormImpactDraftIsNotItsOwnNeighbour: guidance describes the environment,
// so names added by the draft must not be reported as "already taken".
func TestFormImpactDraftIsNotItsOwnNeighbour(t *testing.T) {
	draft := &DraftOptions{
		Extension: "МоёРасширение", Prefix: "моё_",
		Code: `&После("ПриСозданииНаСервере")
Процедура моё_Посл(Отказ, СтандартнаяОбработка)
	Элементы.Добавить("моё_УникальноеПоле", Тип("ПолеФормы"), Элементы.ГруппаШапка);
КонецПроцедуры`,
	}
	fi, err := NewXMLSource("testdata/fi/base").FormImpact(context.Background(),
		"Document", "ЗаказКлиента", "ФормаДокумента", []string{"testdata/fi/base"}, draft)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	if strings.Contains(strings.Join(fi.Guidance, "\n"), "моё_УникальноеПоле") {
		t.Errorf("draft listed as its own neighbour:\n%s", strings.Join(fi.Guidance, "\n"))
	}
}
