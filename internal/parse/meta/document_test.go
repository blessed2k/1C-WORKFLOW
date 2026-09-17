package meta

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Фикстура — вырезка из реальной выгрузки УТ
// (dumps/ut_demo/Documents/ЗаказКлиента.xml): <RegisterRecords> лежит внутри
// <Properties> и перечисляет регистры элементами <xr:Item> с префиксом
// namespace. Соседние скалярные свойства (RegisterRecordsDeletion,
// RegisterRecordsWritingOnPost) оставлены нарочно: они начинаются с того же
// слова и не должны попасть в список движений.
const documentWithMovementsFixture = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.20">
	<Document uuid="04eecd8b-9d92-4d40-a30e-6a274c26e6c8">
		<Properties>
			<Name>ЗаказКлиента</Name>
			<Comment/>
			<Posting>Allow</Posting>
			<RegisterRecordsDeletion>AutoDeleteOff</RegisterRecordsDeletion>
			<RegisterRecordsWritingOnPost>WriteSelected</RegisterRecordsWritingOnPost>
			<RegisterRecords>
				<xr:Item xsi:type="xr:MDObjectRef">AccumulationRegister.ТоварыКОтгрузке</xr:Item>
				<xr:Item xsi:type="xr:MDObjectRef">AccumulationRegister.РасчетыСКлиентами</xr:Item>
				<xr:Item xsi:type="xr:MDObjectRef">InformationRegister.СуммыДокументовВВалютахУчета</xr:Item>
			</RegisterRecords>
		</Properties>
		<ChildObjects>
			<Attribute uuid="a1">
				<Properties>
					<Name>Партнер</Name>
				</Properties>
			</Attribute>
		</ChildObjects>
	</Document>
</MetaDataObject>`

func TestДокументОтдаётДекларированныеДвижения(t *testing.T) {
	facts, diags := ParseFile("Documents/ЗаказКлиента.xml", []byte(documentWithMovementsFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	if facts.Document == nil {
		t.Fatal("Document не заполнен")
	}

	// Ожидание выписано из фикстуры руками: имена ровно в том виде, в каком
	// они в XML, и в том же порядке.
	want := []string{
		"AccumulationRegister.ТоварыКОтгрузке",
		"AccumulationRegister.РасчетыСКлиентами",
		"InformationRegister.СуммыДокументовВВалютахУчета",
	}
	got := facts.Document.RegisterRecords
	if len(got) != len(want) {
		t.Fatalf("RegisterRecords = %q, хотели %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RegisterRecords[%d] = %q, хотели %q", i, got[i], want[i])
		}
	}
}

// Документ без движений в выгрузке встречается в двух видах: с пустым
// самозакрывающимся <RegisterRecords/> и вовсе без этого элемента (оба
// встречаются в ut_demo). Оба обязаны давать пустой список, а не отсутствие
// факта и не диагностику.
func TestДокументБезДвиженийДаётПустойСписок(t *testing.T) {
	const head = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Document uuid="u1">
		<Properties>
			<Name>Встреча</Name>
			<Posting>Deny</Posting>
`
	const tail = `		</Properties>
		<ChildObjects/>
	</Document>
</MetaDataObject>`

	cases := map[string]string{
		"пустой элемент":     head + "\t\t\t<RegisterRecords/>\n" + tail,
		"элемента нет вовсе": head + tail,
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			facts, diags := ParseFile("Documents/Встреча.xml", []byte(src))
			if len(diags) != 0 {
				t.Fatalf("диагностик быть не должно: %+v", diags)
			}
			if facts.Document == nil {
				t.Fatal("Document не заполнен: отсутствие движений — это факт, а не отсутствие факта")
			}
			if facts.Document.RegisterRecords == nil {
				t.Error("RegisterRecords == nil, хотели пустой список")
			}
			if len(facts.Document.RegisterRecords) != 0 {
				t.Errorf("RegisterRecords = %q, хотели пустой список", facts.Document.RegisterRecords)
			}
		})
	}
}

// TestРеальныйЗаказКлиентаОбъявляетДвижения — критерий приёмки на реальной
// выгрузке. ЗаказКлиента выбран не случайно: проведение делегировано в общий
// модуль, кодового факта записи в регистр у самого документа нет, и без
// декларации из XML он остался бы на карте графа пустым.
// Ожидание выписано глазами из Documents/ЗаказКлиента.xml выгрузки ut_demo,
// не посчитано тем же кодом, что под тестом.
func TestРеальныйЗаказКлиентаОбъявляетДвижения(t *testing.T) {
	root := dumpRoot(t)

	rel := "Documents/ЗаказКлиента.xml"
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Skipf("в выгрузке нет %s: %v", rel, err)
	}

	facts, diags := ParseFile(rel, src)
	for _, d := range diags {
		if d.Severity == domain.SeverityError {
			t.Fatalf("ошибка разбора: %+v", d)
		}
	}
	if facts.Document == nil {
		t.Fatal("Document не заполнен")
	}
	got := facts.Document.RegisterRecords
	if len(got) == 0 {
		t.Fatal("RegisterRecords пуст: декларированные движения ЗаказКлиента потеряны")
	}

	have := make(map[string]bool, len(got))
	for _, name := range got {
		have[name] = true
	}
	for _, want := range []string{
		"AccumulationRegister.ТоварыКОтгрузке",
		"AccumulationRegister.ЗапасыИПотребности",
		"InformationRegister.СуммыДокументовВВалютахУчета",
	} {
		if !have[want] {
			t.Errorf("в RegisterRecords нет %q; получено %q", want, got)
		}
	}
}
