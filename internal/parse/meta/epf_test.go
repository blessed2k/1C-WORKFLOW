package meta

import (
	"reflect"
	"testing"
)

// Фикстура — корень EPF-исходника, полученный скиллом epf-init (тот же
// формат, что и epf-dump выдаёт из реальной обработки): <ExternalDataProcessor>
// вместо <DataProcessor>, но та же обёртка <MetaDataObject>/<Properties> —
// разбирается тем же генерическим путём, что и объект конфигурации.
const externalDataProcessorFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.17">
	<ExternalDataProcessor uuid="5c1909aa-6837-4134-acfb-6a91f363da74">
		<InternalInfo>
			<xr:GeneratedType name="ExternalDataProcessorObject.ТестОбработка" category="Object">
				<xr:TypeId>cc6c68a9-a403-4766-a919-a38c17002aa1</xr:TypeId>
			</xr:GeneratedType>
		</InternalInfo>
		<Properties>
			<Name>ТестОбработка</Name>
			<Synonym>
				<v8:item>
					<v8:lang>ru</v8:lang>
					<v8:content>Тест обработка</v8:content>
				</v8:item>
			</Synonym>
			<Comment/>
			<DefaultForm/>
			<AuxiliaryForm/>
		</Properties>
		<ChildObjects/>
	</ExternalDataProcessor>
</MetaDataObject>`

// EPF/ERF-исходники разбираются: корень компонента "<Имя>.xml" без
// оборачивающей папки (в отличие от объектов конфигурации, лежащих под
// "<Тип>/<Имя>.xml").
func TestParseExternalDataProcessorRoot(t *testing.T) {
	facts, diags := ParseFile("ТестОбработка.xml", []byte(externalDataProcessorFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	if facts.Object == nil {
		t.Fatal("Object не заполнен")
	}
	if facts.Object.MType != "ExternalDataProcessor" {
		t.Errorf("MType = %q, хотели ExternalDataProcessor", facts.Object.MType)
	}
	if facts.Object.NameDisplay != "ТестОбработка" {
		t.Errorf("NameDisplay = %q", facts.Object.NameDisplay)
	}
	if facts.Object.UUID != "5c1909aa-6837-4134-acfb-6a91f363da74" {
		t.Errorf("UUID = %q", facts.Object.UUID)
	}
}

// R: бинарный .epf/.erf не открывается никогда. Classify обязана назвать его
// KindBinary ДО чтения содержимого — это сигнал вызывающему коду (обнаружение
// файлов) вообще не читать байты такого файла с диска. Belt-and-
// braces: даже если байты каким-то образом дошли до ParseFile, парсер не
// пытается разобрать их как XML и не падает на произвольном бинарном мусоре.
func TestBinaryEPFNeverOpened(t *testing.T) {
	if got := Classify("ExternalDataProcessors/ТестОбработка.epf"); got != KindBinary {
		t.Fatalf("Classify(.epf) = %q, хотели %q — вызывающий код не поймёт, что файл нельзя читать", got, KindBinary)
	}
	if got := Classify("Отчет.erf"); got != KindBinary {
		t.Fatalf("Classify(.erf) = %q, хотели %q", got, KindBinary)
	}

	garbage := []byte{0x50, 0x4B, 0x03, 0x04, 0x00, 0x00, 0xFF, 0xFE, 0x00, 0x01, 0x02, 0x03}
	facts, diags := ParseFile("ExternalDataProcessors/ТестОбработка.epf", garbage)
	if len(diags) != 0 {
		t.Errorf("диагностик по бинарному файлу быть не должно (его вообще не открывают), получено: %+v", diags)
	}
	if !reflect.DeepEqual(facts, Facts{}) {
		t.Errorf("Facts по бинарному файлу должны быть пустыми, получено: %+v", facts)
	}
}
