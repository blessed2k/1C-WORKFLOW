package meta

import "testing"

// Фикстура формы — компактный, но структурно верный образец реального
// Documents/АвансовыйОтчет/Forms/ФормаДокумента/Ext/Form.xml: событие формы,
// вложенный элемент (UsualGroup -> Field) со своим событием, команда формы.
const formStructureFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<Events>
		<Event name="OnCreateAtServer">ПриСозданииНаСервере</Event>
		<Event name="OnOpen">ПриОткрытии</Event>
	</Events>
	<ChildItems>
		<UsualGroup name="ГруппаОсновное" id="1">
			<ChildItems>
				<Field name="Организация" id="2">
					<DataPath>Объект.Организация</DataPath>
					<Events>
						<Event name="OnChange">ОрганизацияПриИзменении</Event>
					</Events>
				</Field>
			</ChildItems>
		</UsualGroup>
	</ChildItems>
	<Attributes/>
	<Commands>
		<Command name="ЗаполнитьПодразделение" id="4">
			<Action>ЗаполнитьПодразделение</Action>
		</Command>
	</Commands>
</Form>`

func TestParseFormStructure(t *testing.T) {
	relPath := "Documents/АвансовыйОтчет/Forms/ФормаДокумента/Ext/Form.xml"
	facts, diags := ParseFile(relPath, []byte(formStructureFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	fs := facts.FormStructure
	if fs == nil {
		t.Fatal("FormStructure не заполнен")
	}
	wantKey := "Documents/АвансовыйОтчет/Forms/ФормаДокумента"
	if fs.Key != wantKey {
		t.Errorf("Key = %q, хотели %q", fs.Key, wantKey)
	}

	// Элементы: и группа, и вложенное поле — рекурсия по ChildItems работает.
	names := map[string]FormElementFact{}
	for _, e := range fs.Elements {
		names[e.NameDisplay] = e
	}
	if len(fs.Elements) != 2 {
		t.Fatalf("Elements = %+v, хотели 2", fs.Elements)
	}
	field, ok := names["Организация"]
	if !ok {
		t.Fatal("вложенное поле Организация не найдено")
	}
	if field.EType != "Field" {
		t.Errorf("EType = %q, хотели Field", field.EType)
	}
	if field.DataPath != "Объект.Организация" {
		t.Errorf("DataPath = %q", field.DataPath)
	}

	// Обработчики: событие формы и событие элемента — оба, с разным Source.
	var formEvent, fieldEvent *HandlerBindingFact
	for i := range fs.Handlers {
		h := &fs.Handlers[i]
		switch h.Event {
		case "OnOpen":
			formEvent = h
		case "OnChange":
			fieldEvent = h
		}
	}
	if formEvent == nil || formEvent.Source != "" || formEvent.HandlerDisplay != "ПриОткрытии" {
		t.Errorf("formEvent = %+v", formEvent)
	}
	if fieldEvent == nil || fieldEvent.Source != "организация" || fieldEvent.HandlerDisplay != "ОрганизацияПриИзменении" {
		t.Errorf("fieldEvent = %+v", fieldEvent)
	}

	if len(fs.Commands) != 1 || fs.Commands[0].NameDisplay != "ЗаполнитьПодразделение" {
		t.Errorf("Commands = %+v", fs.Commands)
	}
}

// Форма даёт ОДНУ identity из двух аспектов: объявление у владельца
// (<Form>Имя</Form> в XML документа) и структура в Form.xml. Ключ обязан
// совпасть, иначе индексный пайплайн построит два разных узла под
// одну и ту же форму.
func TestFormDeclAndStructureShareIdentity(t *testing.T) {
	ownerFacts, diags := ParseFile("Documents/АвансовыйОтчет.xml", []byte(documentOwnerFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик у владельца быть не должно: %+v", diags)
	}
	var decl *FormDeclFact
	for i := range ownerFacts.FormDecls {
		if ownerFacts.FormDecls[i].NameDisplay == "ФормаДокумента" {
			decl = &ownerFacts.FormDecls[i]
		}
	}
	if decl == nil {
		t.Fatalf("FormDecls владельца = %+v, ФормаДокумента не найдена", ownerFacts.FormDecls)
	}

	structFacts, diags := ParseFile("Documents/АвансовыйОтчет/Forms/ФормаДокумента/Ext/Form.xml", []byte(formStructureFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик у структуры быть не должно: %+v", diags)
	}
	if structFacts.FormStructure == nil {
		t.Fatal("FormStructure не заполнен")
	}

	if decl.Key != structFacts.FormStructure.Key {
		t.Errorf("ключи не совпали: объявление %q, структура %q — два аспекта одной формы разошлись", decl.Key, structFacts.FormStructure.Key)
	}
}

// Общая форма самоидентична: CommonForms/<Имя>.xml и есть форма, без
// отдельного объекта-владельца. Ключ обязан совпасть с Form.xml того же
// объекта: тот же критерий одной identity, другая форма источника.
func TestCommonFormDeclAndStructureShareIdentity(t *testing.T) {
	ownerFacts, diags := ParseFile("CommonForms/АварийныйРежимИСМП.xml", []byte(commonFormFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик у владельца быть не должно: %+v", diags)
	}
	if len(ownerFacts.FormDecls) != 1 {
		t.Fatalf("FormDecls = %+v, хотели одну самоидентичную форму", ownerFacts.FormDecls)
	}
	decl := ownerFacts.FormDecls[0]

	structFacts, diags := ParseFile("CommonForms/АварийныйРежимИСМП/Ext/Form.xml", []byte(formStructureFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик у структуры быть не должно: %+v", diags)
	}
	if structFacts.FormStructure == nil {
		t.Fatal("FormStructure не заполнен")
	}
	if decl.Key != structFacts.FormStructure.Key {
		t.Errorf("ключи не совпали: объявление %q, структура %q", decl.Key, structFacts.FormStructure.Key)
	}
}

// commonFormFixture — вырезка из реальной CommonForms/АварийныйРежимИСМП.xml.
const commonFormFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<CommonForm uuid="42087d49-dae6-4e3f-a1d9-1f93a87ff95f">
		<Properties>
			<Name>АварийныйРежимИСМП</Name>
			<Synonym>
				<v8:item>
					<v8:lang>ru</v8:lang>
					<v8:content>Аварийный режим ИС МП</v8:content>
				</v8:item>
			</Synonym>
			<Comment/>
			<FormType>Managed</FormType>
		</Properties>
	</CommonForm>
</MetaDataObject>`

// documentOwnerFixture — минимальный, но структурно верный владелец формы:
// Documents/АвансовыйОтчет.xml с одной ссылкой <Form>ФормаДокумента</Form>
// среди прочих (реальный документ ссылается на восемь форм, здесь оставлена
// одна ради краткости фикстуры).
const documentOwnerFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<Document uuid="11111111-1111-1111-1111-111111111111">
		<Properties>
			<Name>АвансовыйОтчет</Name>
		</Properties>
		<ChildObjects>
			<Form>ФормаДокумента</Form>
			<Form>ФормаСписка</Form>
		</ChildObjects>
	</Document>
</MetaDataObject>`
