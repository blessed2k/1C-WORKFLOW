package source

import (
	"context"
	"strings"
	"testing"
)

// The fixture mirrors the production incident: a base form that adds its print
// commands programmatically (Справочник.Сотрудники.ФормаЭлемента in a production configuration).
// Keeping it synthetic makes the regression independent of any export on disk.
const (
	dynBase  = "testdata/dyn/base"  // form WITH dynamically added commands
	dynExt   = "testdata/dyn/ext"   // empty extension (РасширениеА, prefix РасшА_)
	dynPlain = "testdata/dyn/plain" // form WITHOUT dynamic commands
)

// dynReport analyses Справочник.Сотрудники.ФормаЭлемента of the fixture with the
// given draft.
func dynReport(t *testing.T, root string, draft *DraftOptions, dumps ...string) *FormImpact {
	t.Helper()
	if len(dumps) == 0 {
		dumps = []string{root, dynExt}
	}
	fi, err := NewXMLSource(root).
		FormImpact(context.Background(), "Catalog", "Сотрудники", "ФормаЭлемента", dumps, draft)
	if err != nil {
		t.Fatalf("FormImpact: %v", err)
	}
	return fi
}

func draftAfter(code string) *DraftOptions {
	return &DraftOptions{Code: code, Extension: "РасширениеА", Prefix: "РасшА_"}
}

// TC-01. The production bug: ИзменитьРеквизиты without a removal list still
// resets the commands the base method added, and nothing errors out.
func TestFormImpactChangeAttrsDropsDynamicCommands(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
&После("ПриСозданииНаСервере")
Процедура РасшА_ПриСозданииНаСервереПосле(Отказ, СтандартнаяОбработка)
	Доб = Новый Массив;
	Доб.Добавить(Новый РеквизитФормы("РасшА_Таблица", Новый ОписаниеТипов("ТаблицаЗначений")));
	ИзменитьРеквизиты(Доб);
КонецПроцедуры`))

	c, ok := conflictByCode(fi)["ChangeAttributesDropsDynamicCommands"]
	if !ok {
		t.Fatalf("dropped dynamic commands not detected: %+v", fi.Conflicts)
	}
	if c.Severity != "high" {
		t.Errorf("severity = %q, want high", c.Severity)
	}
	if !strings.Contains(c.Message, "ПечатьБейджиков") {
		t.Errorf("message must name the lost command: %q", c.Message)
	}
	if len(c.Involved) < 2 {
		t.Errorf("involved must list the draft and the base configuration: %+v", c.Involved)
	}
}

// TC-11. The boundary that keeps the rule honest: &Перед runs BEFORE the base
// method, so the commands are created afterwards and survive.
func TestFormImpactChangeAttrsBeforeIsSafe(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
&Перед("ПриСозданииНаСервере")
Процедура РасшА_ПриСозданииНаСервереПеред(Отказ, СтандартнаяОбработка)
	Доб = Новый Массив;
	Доб.Добавить(Новый РеквизитФормы("РасшА_Таблица", Новый ОписаниеТипов("ТаблицаЗначений")));
	ИзменитьРеквизиты(Доб);
КонецПроцедуры`))

	if c, ok := conflictByCode(fi)["ChangeAttributesDropsDynamicCommands"]; ok {
		t.Errorf("&Перед runs before the commands are created: %+v", c)
	}
}

// TC-12. Nothing dynamic on the form: there is nothing to drop.
func TestFormImpactChangeAttrsWithoutDynamicCommands(t *testing.T) {
	fi := dynReport(t, dynPlain, draftAfter(`&НаСервере
&После("ПриСозданииНаСервере")
Процедура РасшА_ПриСозданииНаСервереПосле(Отказ, СтандартнаяОбработка)
	Доб = Новый Массив;
	Доб.Добавить(Новый РеквизитФормы("РасшА_Таблица", Новый ОписаниеТипов("ТаблицаЗначений")));
	ИзменитьРеквизиты(Доб);
КонецПроцедуры`), dynPlain, dynExt)

	if c, ok := conflictByCode(fi)["ChangeAttributesDropsDynamicCommands"]; ok {
		t.Errorf("no dynamic commands on this form: %+v", c)
	}
}

// TC-13. &Вместо WITH ПродолжитьВызов creates the commands and then drops them
// in the same procedure — the worst variant, and not an InsteadWithoutContinue.
func TestFormImpactChangeAttrsAfterContinueCall(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
&Вместо("ПриСозданииНаСервере")
Процедура РасшА_Вместо(Отказ, СтандартнаяОбработка)
	ПродолжитьВызов(Отказ, СтандартнаяОбработка);
	Доб = Новый Массив;
	Доб.Добавить(Новый РеквизитФормы("РасшА_Таблица", Новый ОписаниеТипов("ТаблицаЗначений")));
	ИзменитьРеквизиты(Доб);
КонецПроцедуры`))

	byCode := conflictByCode(fi)
	if _, ok := byCode["ChangeAttributesDropsDynamicCommands"]; !ok {
		t.Errorf("ПродолжитьВызов creates the commands, ИзменитьРеквизиты drops them: %+v", fi.Conflicts)
	}
	if c, ok := byCode["InsteadWithoutContinue"]; ok {
		t.Errorf("ПродолжитьВызов is called, base method runs: %+v", c)
	}
}

// TC-02. Columns of a value-table attribute carry a parent path as the third
// argument: they live in that attribute's namespace, cannot collide with other
// extensions and must not be pushed to carry a prefix.
func TestFormImpactColumnsNeedNoPrefix(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
&После("ПриСозданииНаСервере")
Процедура РасшА_ПриСозданииНаСервереПосле(Отказ, СтандартнаяОбработка)
	Доб = Новый Массив;
	Доб.Добавить(Новый РеквизитФормы("РасшА_Таблица", Новый ОписаниеТипов("ТаблицаЗначений")));
	Доб.Добавить(Новый РеквизитФормы("Дата", Новый ОписаниеТипов("Дата"), "РасшА_Таблица", "Дата"));
	Доб.Добавить(Новый РеквизитФормы("ТекстПримечания", Новый ОписаниеТипов("Строка"), "РасшА_Таблица", "Примечание"));
	ИзменитьРеквизиты(Доб);
КонецПроцедуры`))

	if c, ok := conflictByCode(fi)["UnprefixedName"]; ok {
		t.Errorf("columns live in their attribute's namespace, no prefix required: %+v", c)
	}
	var cols, attrs []string
	for _, s := range fi.Sources {
		if s.Dump != draftDump {
			continue
		}
		for _, c := range s.Changes {
			switch c.Kind {
			case "ДобавитьКолонку":
				cols = append(cols, c.Target)
			case "ДобавитьРеквизит":
				attrs = append(attrs, c.Target)
			}
		}
	}
	if len(cols) != 2 {
		t.Errorf("columns = %v, want Дата and ТекстПримечания", cols)
	}
	if len(attrs) != 1 || attrs[0] != "РасшА_Таблица" {
		t.Errorf("top-level attributes = %v, want [РасшА_Таблица]", attrs)
	}
}

// TC-09 contrast: a top-level attribute has no parent path, so the prefix is
// still required.
func TestFormImpactTopLevelAttrStillNeedsPrefix(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
&После("ПриСозданииНаСервере")
Процедура РасшА_ПриСозданииНаСервереПосле(Отказ, СтандартнаяОбработка)
	Доб = Новый Массив;
	Доб.Добавить(Новый РеквизитФормы("ПримечанияДТП", Новый ОписаниеТипов("ТаблицаЗначений")));
	ИзменитьРеквизиты(Доб);
КонецПроцедуры`))

	c, ok := conflictByCode(fi)["UnprefixedName"]
	if !ok {
		t.Fatalf("top-level attribute without the prefix must be flagged: %+v", fi.Conflicts)
	}
	if !strings.Contains(c.Message, "ПримечанияДТП") {
		t.Errorf("message = %q", c.Message)
	}
}

// TC-03. Unreadable dumps must not read as "the form is clean".
func TestFormImpactNoSourcesScanned(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
&После("ПриСозданииНаСервере")
Процедура РасшА_Пусто(Отказ, СтандартнаяОбработка)
КонецПроцедуры`), "testdata/dyn/no-such-export")

	c, ok := conflictByCode(fi)["NoSourcesScanned"]
	if !ok {
		t.Fatalf("unreadable dumps must be reported: %+v", fi.Conflicts)
	}
	if c.Severity != "high" {
		t.Errorf("severity = %q, want high", c.Severity)
	}
	for _, g := range fi.Guidance {
		if strings.Contains(g, "нет программных изменений") {
			t.Errorf("must not reassure when nothing was scanned: %q", g)
		}
	}
}

// TC-04. A draft without an interceptor annotation yields no source at all;
// answering as if the draft did not exist is worse than saying so.
func TestFormImpactDraftWithoutAnnotation(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
Процедура РасшА_ПриСозданииНаСервереПосле(Отказ, СтандартнаяОбработка)
	Доб = Новый Массив;
	Доб.Добавить(Новый РеквизитФормы("РасшА_Таблица", Новый ОписаниеТипов("ТаблицаЗначений")));
	ИзменитьРеквизиты(Доб);
КонецПроцедуры`))

	if _, ok := conflictByCode(fi)["DraftNotAnalysed"]; !ok {
		t.Fatalf("draft without an annotation must be reported: %+v", fi.Conflicts)
	}
	for _, s := range fi.Sources {
		if s.Dump == draftDump {
			t.Errorf("draft without an interceptor must not appear as a source: %+v", s)
		}
	}
}

// Объект.Родитель sets the catalog item's place in the data hierarchy; reading
// it as a form-element reparent put the base configuration into sources for a
// change it never made.
func TestFormImpactDataObjectParentIsNotFormChange(t *testing.T) {
	fi := dynReport(t, dynBase, nil)
	for _, s := range fi.Sources {
		for _, c := range s.Changes {
			if c.Kind == "СменитьРодителя" {
				t.Errorf("Объект.Родитель is the data hierarchy, not a form element: %+v", c)
			}
		}
	}
}

// TC-14. The correct draft: everything static, code only fills the data.
func TestFormImpactCleanDraft(t *testing.T) {
	fi := dynReport(t, dynBase, draftAfter(`&НаСервере
&После("ПриСозданииНаСервере")
Процедура РасшА_ПриСозданииНаСервереПосле(Отказ, СтандартнаяОбработка)
	РасшА_ЗаполнитьТаблицу();
КонецПроцедуры`))

	if len(fi.Conflicts) != 0 {
		t.Errorf("correct draft must be clean: %+v", fi.Conflicts)
	}
}
