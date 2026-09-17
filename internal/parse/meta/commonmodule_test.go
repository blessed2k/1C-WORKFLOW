package meta

import "testing"

// Фикстура — дословно CommonModules/CRMЛокализация.xml реальной выгрузки УТ:
// Global=false, Server=true, ExternalConnection=true, ServerCall=false — если
// свойства выводить по имени модуля (как делает
// internal/source/extpoints.go), "CRMЛокализация" не содержит ни "клиент", ни
// "сервер" и результат был бы угадан, а не прочитан.
const commonModuleFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<CommonModule uuid="4e1749be-07b1-404c-af39-797d4a8d69c7">
		<Properties>
			<Name>CRMЛокализация</Name>
			<Synonym>
				<v8:item>
					<v8:lang>ru</v8:lang>
					<v8:content>CRM (Локализация)</v8:content>
				</v8:item>
			</Synonym>
			<Comment/>
			<Global>false</Global>
			<ClientManagedApplication>false</ClientManagedApplication>
			<Server>true</Server>
			<ExternalConnection>true</ExternalConnection>
			<ClientOrdinaryApplication>true</ClientOrdinaryApplication>
			<ServerCall>false</ServerCall>
			<Privileged>false</Privileged>
			<ReturnValuesReuse>DontUse</ReturnValuesReuse>
		</Properties>
	</CommonModule>
</MetaDataObject>`

func TestParseCommonModuleRegistry(t *testing.T) {
	facts, diags := ParseFile("CommonModules/CRMЛокализация.xml", []byte(commonModuleFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	if facts.Object == nil || facts.Object.MType != "CommonModule" {
		t.Fatalf("Object = %+v", facts.Object)
	}
	reg := facts.ModuleRegistry
	if reg == nil {
		t.Fatal("ModuleRegistry не заполнен")
	}
	want := ModuleRegistryFact{
		Global:                    false,
		Server:                    true,
		ClientManagedApplication:  false,
		ClientOrdinaryApplication: true,
		ExternalConnection:        true,
		ServerCall:                false,
		Privileged:                false,
		ReturnValuesReuse:         "DontUse",
	}
	if *reg != want {
		t.Errorf("ModuleRegistry = %+v, хотели %+v", *reg, want)
	}
}

// Общий модуль без явно клиентского/серверного имени — прямая проверка
// критерия приёмки: свойства читаются из XML, а не выводятся по подстроке в
// имени (internal/source/extpoints.go.moduleContext — образец дефекта,
// который этот пакет обязан не повторить).
func TestParseCommonModuleNameDoesNotLeakIntoProperties(t *testing.T) {
	src := []byte("\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" version="2.20">
	<CommonModule uuid="00000000-0000-0000-0000-000000000001">
		<Properties>
			<Name>ОбработкаФайлов</Name>
			<Global>false</Global>
			<Server>false</Server>
			<ClientManagedApplication>true</ClientManagedApplication>
			<ClientOrdinaryApplication>true</ClientOrdinaryApplication>
			<ExternalConnection>false</ExternalConnection>
			<ServerCall>true</ServerCall>
			<Privileged>false</Privileged>
		</Properties>
	</CommonModule>
</MetaDataObject>`)
	// Имя модуля не содержит ни "клиент", ни "сервер" в буквальном смысле,
	// который сравнивает internal/source/extpoints.go.moduleContext, зато
	// свойства говорят однозначно: клиентский модуль без серверного вызова.
	facts, diags := ParseFile("CommonModules/ОбработкаФайлов.xml", src)
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	reg := facts.ModuleRegistry
	if reg == nil {
		t.Fatal("ModuleRegistry не заполнен")
	}
	if reg.Server {
		t.Error("Server=false в XML, факт не должен угадывать true по имени")
	}
	if !reg.ClientManagedApplication {
		t.Error("ClientManagedApplication=true в XML потерялось")
	}
	if !reg.ServerCall {
		t.Error("ServerCall=true в XML потерялось")
	}
}
