package meta

import "testing"

// Фикстура — дословно ScheduledJobs/ABCКлассификацияНоменклатуры.xml
// реальной выгрузки УТ.
const scheduledJobFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<ScheduledJob uuid="3a0dd0b7-89db-44cf-aff7-3cefed774ca8">
		<Properties>
			<Name>ABCКлассификацияНоменклатуры</Name>
			<Synonym>
				<v8:item>
					<v8:lang>ru</v8:lang>
					<v8:content>ABC-классификация номенклатуры</v8:content>
				</v8:item>
			</Synonym>
			<Comment/>
			<MethodName>CommonModule.Классификация.ВыполнитьABCКлассификациюНоменклатурыРегламентноеЗадание</MethodName>
			<Description/>
			<Key/>
			<Use>false</Use>
			<Predefined>true</Predefined>
			<RestartCountOnFailure>3</RestartCountOnFailure>
			<RestartIntervalOnFailure>10</RestartIntervalOnFailure>
		</Properties>
	</ScheduledJob>
</MetaDataObject>`

func TestParseScheduledJob(t *testing.T) {
	facts, diags := ParseFile("ScheduledJobs/ABCКлассификацияНоменклатуры.xml", []byte(scheduledJobFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	j := facts.ScheduledJob
	if j == nil {
		t.Fatal("ScheduledJob не заполнен")
	}
	if j.NameDisplay != "ABCКлассификацияНоменклатуры" {
		t.Errorf("NameDisplay = %q", j.NameDisplay)
	}
	if j.MethodRaw != "CommonModule.Классификация.ВыполнитьABCКлассификациюНоменклатурыРегламентноеЗадание" {
		t.Errorf("MethodRaw = %q", j.MethodRaw)
	}
	if j.Use {
		t.Error("Use=false в XML не должно стать true")
	}
	if !j.Predefined {
		t.Error("Predefined=true в XML потерялось")
	}
	// Регламентное задание — тоже объект метаданных (для get_object).
	if facts.Object == nil || facts.Object.MType != "ScheduledJob" {
		t.Errorf("Object = %+v", facts.Object)
	}
}
