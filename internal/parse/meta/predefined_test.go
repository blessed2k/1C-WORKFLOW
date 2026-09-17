package meta

import "testing"

// Фикстура — дословно Catalogs/Справки2ЕГАИС/Ext/Predefined.xml реальной
// выгрузки УТ.
const predefinedFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<PredefinedData xmlns="http://v8.1c.ru/8.3/xcf/predef" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<Item id="1d97d083-023e-41d1-afab-9a7c0c2ba641">
		<Name>ДляОприходованияИзлишков</Name>
		<Code/>
		<Description>Для оприходования излишков</Description>
		<IsFolder>false</IsFolder>
	</Item>
</PredefinedData>`

func TestParsePredefinedData(t *testing.T) {
	facts, diags := ParseFile("Catalogs/Справки2ЕГАИС/Ext/Predefined.xml", []byte(predefinedFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	if len(facts.Predefined) != 1 {
		t.Fatalf("Predefined = %+v, хотели 1 элемент", facts.Predefined)
	}
	item := facts.Predefined[0]
	if item.OwnerType != "Catalogs" || item.OwnerNameRaw != "Справки2ЕГАИС" {
		t.Errorf("владелец = %q/%q, хотели Catalogs/Справки2ЕГАИС", item.OwnerType, item.OwnerNameRaw)
	}
	if item.NameDisplay != "ДляОприходованияИзлишков" {
		t.Errorf("NameDisplay = %q", item.NameDisplay)
	}
	if item.IsFolder {
		t.Error("IsFolder=false в XML не должно стать true")
	}
}
