package meta

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Фикстура — вырезка из реальной выгрузки УТ
// (dumps/ut_demo/Catalogs/АвансовыйОтчетПрисоединенныеФайлы.xml): несёт BOM и
// реальный порядок <InternalInfo> ПЕРЕД <Properties>, как того требует
// рискованная ветка чтения (Go's encoding/xml сопоставляет по имени тега вне
// зависимости от порядка — фикстура проверяет это, а не выдумывает удобный
// порядок).
const catalogFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<Catalog uuid="d11b89e1-90a2-47e7-b43f-7f231ec64b2f">
		<InternalInfo>
			<xr:GeneratedType name="CatalogObject.Х" category="Object">
				<xr:TypeId>46c3b9ff-b2b7-4a05-94e9-a55a6a6aa606</xr:TypeId>
			</xr:GeneratedType>
		</InternalInfo>
		<Properties>
			<Name>АвансовыйОтчетПрисоединенныеФайлы</Name>
			<Synonym>
				<v8:item>
					<v8:lang>ru</v8:lang>
					<v8:content>Присоединенные файлы (Авансовый отчет)</v8:content>
				</v8:item>
			</Synonym>
			<Comment/>
			<Hierarchical>true</Hierarchical>
			<HierarchyType>HierarchyFoldersAndItems</HierarchyType>
			<Owners/>
			<CodeLength>0</CodeLength>
			<DescriptionLength>150</DescriptionLength>
		</Properties>
		<ChildObjects>
			<Attribute uuid="a1">
				<Properties>
					<Name>Файл</Name>
					<Synonym/>
					<Type>
						<v8:Type>xs:string</v8:Type>
					</Type>
					<Indexing>Index</Indexing>
				</Properties>
			</Attribute>
			<TabularSection uuid="ts1">
				<Properties>
					<Name>Реквизиты</Name>
				</Properties>
				<ChildObjects>
					<Attribute uuid="a2">
						<Properties>
							<Name>Значение</Name>
							<Type>
								<v8:Type>xs:decimal</v8:Type>
							</Type>
							<Indexing>DontIndex</Indexing>
						</Properties>
					</Attribute>
				</ChildObjects>
			</TabularSection>
			<Form>ФормаЭлемента</Form>
			<Command>ЗагрузитьФайл</Command>
		</ChildObjects>
	</Catalog>
</MetaDataObject>`

func TestParseMetadataObjectCatalog(t *testing.T) {
	facts, diags := ParseFile("Catalogs/АвансовыйОтчетПрисоединенныеФайлы.xml", []byte(catalogFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	if facts.Object == nil {
		t.Fatal("Object не заполнен")
	}
	if facts.Object.MType != "Catalog" {
		t.Errorf("MType = %q, хотели Catalog", facts.Object.MType)
	}
	if facts.Object.NameDisplay != "АвансовыйОтчетПрисоединенныеФайлы" {
		t.Errorf("NameDisplay = %q", facts.Object.NameDisplay)
	}
	if facts.Object.NameNorm != domain.NormalizeName("АвансовыйОтчетПрисоединенныеФайлы") {
		t.Errorf("NameNorm не нормализовано: %q", facts.Object.NameNorm)
	}
	if facts.Object.Synonym != "Присоединенные файлы (Авансовый отчет)" {
		t.Errorf("Synonym = %q", facts.Object.Synonym)
	}
	if facts.Object.UUID != "d11b89e1-90a2-47e7-b43f-7f231ec64b2f" {
		t.Errorf("UUID = %q", facts.Object.UUID)
	}
	if facts.Object.Props["Hierarchical"] != "true" {
		t.Errorf("Props[Hierarchical] = %q, хотели true", facts.Object.Props["Hierarchical"])
	}
	if facts.Object.Props["CodeLength"] != "0" {
		t.Errorf("Props[CodeLength] = %q", facts.Object.Props["CodeLength"])
	}

	// Атрибут верхнего уровня: индексирован, тип захвачен.
	var attr *MetadataMemberFact
	for i := range facts.Members {
		if facts.Members[i].NameDisplay == "Файл" {
			attr = &facts.Members[i]
		}
	}
	if attr == nil {
		t.Fatal("реквизит Файл не найден")
	}
	if !attr.Indexed {
		t.Error("Файл: Indexing=Index должен дать Indexed=true")
	}
	if len(attr.Types) != 1 || attr.Types[0] != "xs:string" {
		t.Errorf("Файл: Types = %v", attr.Types)
	}
	if attr.ParentNorm != "" {
		t.Errorf("Файл: реквизит верхнего уровня, ParentNorm должен быть пуст, получено %q", attr.ParentNorm)
	}

	// Реквизит табличной части несёт имя владеющей ТЧ.
	var nested *MetadataMemberFact
	for i := range facts.Members {
		if facts.Members[i].NameDisplay == "Значение" {
			nested = &facts.Members[i]
		}
	}
	if nested == nil {
		t.Fatal("реквизит табличной части Значение не найден")
	}
	if nested.ParentNorm != domain.NormalizeName("Реквизиты") {
		t.Errorf("Значение: ParentNorm = %q, хотели %q", nested.ParentNorm, domain.NormalizeName("Реквизиты"))
	}
	if nested.Indexed {
		t.Error("Значение: DontIndex должен дать Indexed=false")
	}

	// Форма и команда объекта — отдельные списки, не члены.
	if len(facts.FormDecls) != 1 || facts.FormDecls[0].NameDisplay != "ФормаЭлемента" {
		t.Errorf("FormDecls = %+v", facts.FormDecls)
	}
	wantKey := "Catalogs/АвансовыйОтчетПрисоединенныеФайлы/Forms/ФормаЭлемента"
	if facts.FormDecls[0].Key != wantKey {
		t.Errorf("FormDecls[0].Key = %q, хотели %q", facts.FormDecls[0].Key, wantKey)
	}
	if len(facts.Commands) != 1 || facts.Commands[0].NameDisplay != "ЗагрузитьФайл" {
		t.Errorf("Commands = %+v", facts.Commands)
	}
}
