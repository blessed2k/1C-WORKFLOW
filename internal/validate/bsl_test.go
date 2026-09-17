package validate

import (
	"strings"
	"testing"
)

// meta accepts the objects a small fixture configuration would have.
func meta(mdType, name string) bool {
	known := map[string]bool{
		"Catalog.контрагенты":             true,
		"Catalog.номенклатура":            true,
		"Document.реализациятоваровуслуг": true,
		"Enum.ставкиндс":                  true,
	}
	return known[mdType+"."+strings.ToLower(name)]
}

// method knows two global-context methods, as the platform reference does.
func method(name string) (Method, bool) {
	switch strings.ToLower(name) {
	case "значениезаполнено":
		return Method{Signature: "ЗначениеЗаполнено(<Значение>)", Required: 1, Total: 1}, true
	case "записьжурналарегистрации":
		return Method{Signature: "ЗаписьЖурналаРегистрации(<ИмяСобытия>, <Уровень>, <ОбъектМетаданных>, <Данные>, <Комментарий>, <РежимТранзакции>)", Required: 1, Total: 6}, true
	case "начатьтранзакцию":
		return Method{Signature: "НачатьТранзакцию(<РежимБлокировок>)", Required: 0, Total: 1}, true
	}
	return Method{}, false
}

func codes(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

// TestUnknownMetadataObject: a reference that cannot resolve at runtime is the
// error this tool exists to catch — the code compiles in the editor's head and
// fails in the base.
func TestUnknownMetadataObject(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	А = Справочники.Контрагенты.НайтиПоКоду("1");
	Б = Справочники.КонтрагентыНеТе.НайтиПоКоду("1");
	В = Документы.РеализацияТоваровУслуг.СоздатьДокумент();
	Г = РегистрыСведений.НетТакого.СоздатьМенеджерЗаписи();
КонецПроцедуры`, meta, method)

	if len(fs) != 2 {
		t.Fatalf("want exactly the two unknown references, got %v", codes(fs))
	}
	for _, f := range fs {
		if f.Code != "UnknownMetadataObject" || f.Severity != "high" {
			t.Errorf("finding = %+v", f)
		}
	}
	if !strings.Contains(fs[0].Message, "КонтрагентыНеТе") || fs[0].Line != 4 {
		t.Errorf("first finding must name the object and its line: %+v", fs[0])
	}
	if !strings.Contains(fs[1].Message, "РегистрыСведений.НетТакого") {
		t.Errorf("second finding = %+v", fs[1])
	}
}

// TestWrongArgCount: the misremembered signature — exactly what the writer is
// certain about and therefore never looks up.
func TestWrongArgCount(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	Если ЗначениеЗаполнено() Тогда
	КонецЕсли;
	ЗначениеЗаполнено(А, Б);
КонецПроцедуры`, meta, method)

	if len(fs) != 2 {
		t.Fatalf("want a finding for too few and for too many: %v", codes(fs))
	}
	if !strings.Contains(fs[0].Message, "0 аргумент") || !strings.Contains(fs[0].Message, "обязательных — 1") {
		t.Errorf("too-few finding = %+v", fs[0])
	}
	if !strings.Contains(fs[1].Message, "параметров — 1") {
		t.Errorf("too-many finding = %+v", fs[1])
	}
	if !strings.Contains(fs[0].Suggestion, "bsl_syntax") {
		t.Errorf("finding must route to the reference: %+v", fs[0])
	}
}

// TestValidCodeIsSilent is the test that decides whether the tool gets trusted:
// correct code must produce nothing at all.
func TestValidCodeIsSilent(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	НачатьТранзакцию();
	Попытка
		Если ЗначениеЗаполнено(Ссылка) Тогда
			Объект = Справочники.Номенклатура.СоздатьЭлемент();
		КонецЕсли;
		ЗафиксироватьТранзакцию();
	Исключение
		ОтменитьТранзакцию();
		ЗаписьЖурналаРегистрации("Тест", УровеньЖурналаРегистрации.Ошибка, , ,
			ИнформацияОбОшибке().ПодробноеПредставлениеОшибки());
		ВызватьИсключение;
	КонецПопытки;
КонецПроцедуры`, meta, method)

	if len(fs) != 0 {
		t.Errorf("correct code must be silent, got %+v", fs)
	}
}

// TestSkippedOptionalArgsAreNotMissing: 1C lets an optional argument be omitted
// with a bare comma, and the position still counts. Reading those commas as
// missing arguments would flag the most ordinary BSP call there is.
func TestSkippedOptionalArgsAreNotMissing(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	ЗаписьЖурналаРегистрации("Событие", УровеньЖурналаРегистрации.Информация, , , "текст");
КонецПроцедуры`, meta, method)

	if len(fs) != 0 {
		t.Errorf("skipped optional arguments are legal, got %+v", fs)
	}
}

// TestLocalProcedureShadowsPlatformName: a module may define its own procedure
// whose name also exists in the platform. Judging that call against the platform
// signature would be an invented error.
func TestLocalProcedureShadowsPlatformName(t *testing.T) {
	fs := BSL(`
Процедура ЗначениеЗаполнено(А, Б, В)
КонецПроцедуры

Процедура Тест()
	ЗначениеЗаполнено(1, 2, 3);
КонецПроцедуры`, meta, method)

	if len(fs) != 0 {
		t.Errorf("the module defines this name itself, the platform signature does not apply: %+v", fs)
	}
}

// TestUnknownCallsAreNotJudged: a common-module or local call is not in the
// global context, and nothing about it is provable here. Silence is correct.
func TestUnknownCallsAreNotJudged(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	ОбщегоНазначения.СообщитьПользователю("текст");
	МояПроцедура(1, 2, 3, 4, 5);
	РаботаСФайлами.Записать(А);
КонецПроцедуры`, meta, method)

	if len(fs) != 0 {
		t.Errorf("nothing provable about non-platform calls: %+v", fs)
	}
}

// TestLiteralsAndCommentsAreNotCode: an object named in a message is not a
// reference, and a comma inside a literal is not an argument separator.
func TestLiteralsAndCommentsAreNotCode(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	// Справочники.ВообщеНетТакого — это комментарий
	Сообщение = "Справочники.ТожеНетТакого, и запятая внутри";
	ЗначениеЗаполнено("а, б, в");
КонецПроцедуры`, meta, method)

	if len(fs) != 0 {
		t.Errorf("comments and literals are not code: %+v", fs)
	}
}

// TestMultilineCall: a call split across lines is one call; counting only the
// first line would report a missing argument that is on the next one.
func TestMultilineCall(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	ЗначениеЗаполнено(
		Ссылка);
КонецПроцедуры`, meta, method)

	if len(fs) != 0 {
		t.Errorf("the argument is on the next line: %+v", fs)
	}
}

// TestWithoutLookupsNothingIsInvented: with no dump and no index there is
// nothing to prove anything against, so there must be no findings.
func TestWithoutLookupsNothingIsInvented(t *testing.T) {
	fs := BSL(`
Процедура Тест()
	А = Справочники.ЧтоУгодно.НайтиПоКоду("1");
	ЗначениеЗаполнено();
КонецПроцедуры`, nil, nil)

	if len(fs) != 0 {
		t.Errorf("nothing to check against, nothing to report: %+v", fs)
	}
}
