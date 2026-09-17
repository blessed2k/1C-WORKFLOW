package meta

import "testing"

// configurationRootFixture — вырезка корня Configuration.xml: несёт BOM и
// реальный порядок <InternalInfo> перед <Properties>, как настоящая выгрузка.
// ConfigurationExtensionPurpose непуст — это расширение.
const configurationRootFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Configuration uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0001">
		<InternalInfo>
			<xr:ContainedObject>
				<xr:ClassId>9cd510cd-abfc-11d4-9434-004095e12fc7</xr:ClassId>
			</xr:ContainedObject>
		</InternalInfo>
		<Properties>
			<Name>ИсправлениеОшибок</Name>
			<ConfigurationExtensionPurpose>Patch</ConfigurationExtensionPurpose>
		</Properties>
		<ChildObjects/>
	</Configuration>
</MetaDataObject>`

// externalDataProcessorRootFixture — корень разобранного EPF, без BOM и без
// ConfigurationExtensionPurpose (его там не бывает вовсе).
const externalDataProcessorRootFixture = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.17">
	<ExternalDataProcessor uuid="0d3a94e9-6b9d-4b5a-9c1e-2f4a1d1b0002">
		<InternalInfo>
			<xr:GeneratedType name="ExternalDataProcessorObject.Чужое" category="Object">
				<xr:TypeId>3c5e3e31-2d40-4f7c-b14d-9f8f3c4d5e6f</xr:TypeId>
			</xr:GeneratedType>
		</InternalInfo>
		<Properties>
			<Name>ЗагрузкаЦен</Name>
		</Properties>
	</ExternalDataProcessor>
</MetaDataObject>`

func TestDetectRoot(t *testing.T) {
	t.Run("расширение: BOM снят, InternalInfo перед Properties не путает поля", func(t *testing.T) {
		object, name, purpose, err := DetectRoot([]byte(configurationRootFixture))
		if err != nil {
			t.Fatalf("DetectRoot: %v", err)
		}
		if object != "Configuration" {
			t.Errorf("object = %q, хотели Configuration", object)
		}
		if name != "ИсправлениеОшибок" {
			t.Errorf("name = %q", name)
		}
		if purpose != "Patch" {
			t.Errorf("extensionPurpose = %q, хотели Patch", purpose)
		}
	})

	t.Run("EPF: без BOM, ConfigurationExtensionPurpose отсутствует", func(t *testing.T) {
		object, name, purpose, err := DetectRoot([]byte(externalDataProcessorRootFixture))
		if err != nil {
			t.Fatalf("DetectRoot: %v", err)
		}
		if object != "ExternalDataProcessor" {
			t.Errorf("object = %q, хотели ExternalDataProcessor", object)
		}
		if name != "ЗагрузкаЦен" {
			t.Errorf("name = %q", name)
		}
		if purpose != "" {
			t.Errorf("extensionPurpose = %q, хотели пусто", purpose)
		}
	})

	t.Run("не MetaDataObject — ошибка", func(t *testing.T) {
		if _, _, _, err := DetectRoot([]byte(`<Root/>`)); err == nil {
			t.Fatal("хотели ошибку на чужом корневом элементе")
		}
	})

	t.Run("не XML — ошибка", func(t *testing.T) {
		if _, _, _, err := DetectRoot([]byte(`это не xml вовсе`)); err == nil {
			t.Fatal("хотели ошибку на не-XML")
		}
	})
}
