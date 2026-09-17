package meta

import "testing"

// Пути ниже взяты из реальной выгрузки УТ (dumps/ut_demo): это не
// синтетические придумки, а формы путей, которые парсер обязан различать.
func TestClassify(t *testing.T) {
	cases := []struct {
		path string
		want Kind
	}{
		{"Configuration.xml", KindConfigurationRoot},
		{"ConfigDumpInfo.xml", KindUnsupported},
		{"Catalogs/Товары.xml", KindMetadataObject},
		{"CommonModules/CRMЛокализация.xml", KindMetadataObject},
		{"ScheduledJobs/ABCКлассификацияНоменклатуры.xml", KindMetadataObject},
		{"DefinedTypes/GLN.xml", KindMetadataObject},
		{"Roles/ЧтениеСчетовНаОплатуКлиентам.xml", KindMetadataObject},
		{"EventSubscriptions/ВариантОтчетаПередУдалением.xml", KindEventSubscription},
		{"Roles/ЧтениеСчетовНаОплатуКлиентам/Ext/Rights.xml", KindRoleRights},
		{"Documents/АвансовыйОтчет/Forms/ФормаДокумента/Ext/Form.xml", KindFormStructure},
		{"CommonForms/НачальнаяСтраница/Ext/Form.xml", KindFormStructure},
		{"ТестОбработка/Forms/Форма/Ext/Form.xml", KindFormStructure}, // EPF/ERF
		{"Catalogs/Справки2ЕГАИС/Ext/Predefined.xml", KindPredefinedData},
		{"ТестОбработка.xml", KindMetadataObject}, // корень EPF/ERF
		{"CommonPictures/Логотип.xml", KindMetadataObject},
		{"Ext/MainSectionPicture.xml", KindUnsupported},
		{"Ext/MainSectionCommandInterface.xml", KindUnsupported},
		{"DataProcessors/Х/Templates/ПФ/Ext/Template.xml", KindUnsupported},
		{"Tasks/Х/Forms/Y/Ext/Help.xml", KindUnsupported},
		{"ExchangePlans/Х/Ext/Content.xml", KindUnsupported},
		{"ScheduledJobs/Х/Ext/Schedule.xml", KindUnsupported},
		{"DataProcessors/Х/Ext/ObjectModule.bsl", KindUnsupported},
		{"ExternalDataProcessors/Х.epf", KindBinary},
		{"Х.erf", KindBinary},
		// разные написания одного пути — один и тот же вид.
		{`Roles\ЧтениеСчетовНаОплатуКлиентам\Ext\Rights.xml`, KindRoleRights},
		{"Roles//ЧтениеСчетовНаОплатуКлиентам//Ext//Rights.xml", KindRoleRights},
	}
	for _, c := range cases {
		if got := Classify(c.path); got != c.want {
			t.Errorf("Classify(%q) = %q, хотели %q", c.path, got, c.want)
		}
	}
}
