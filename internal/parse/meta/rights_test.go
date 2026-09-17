package meta

import "testing"

// Фикстура — вырезка из реальной Roles/БазовыеПраваБИД/Ext/Rights.xml (объект
// с RLS-ограничением по условию) плюс собственная строка setForNewObjects=true,
// как у Roles/ПолныеПрава/Ext/Rights.xml. Ожидание — задокументированное
// поведение internal/source/rightsaudit.go.roleRights (образец семантики, не
// вызывается отсюда): "нет объекта в Objects" при setForNewObjects=true не
// равно "нет доступа", это должен решать слой над фактами, а не этот парсер —
// поэтому парсер обязан просто НЕ ВЫДУМЫВАТЬ строку под отсутствующий объект.
const rightsFixture = "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<Rights xmlns="http://v8.1c.ru/8.2/roles" version="2.20">
	<setForNewObjects>true</setForNewObjects>
	<setForAttributesByDefault>true</setForAttributesByDefault>
	<independentRightsOfChildObjects>false</independentRightsOfChildObjects>
	<object>
		<name>InformationRegister.НастройкиАвторизацииВ1СДокументообороте</name>
		<right>
			<name>Read</name>
			<value>true</value>
			<restrictionByCondition>
				<condition>ГДЕ Пользователь = &amp;ТекущийПользователь</condition>
			</restrictionByCondition>
		</right>
		<right>
			<name>Update</name>
			<value>true</value>
			<restrictionByCondition>
				<condition>ГДЕ Пользователь = &amp;ТекущийПользователь</condition>
			</restrictionByCondition>
		</right>
		<right>
			<name>Delete</name>
			<value>false</value>
		</right>
	</object>
</Rights>`

func TestParseRoleRights(t *testing.T) {
	facts, diags := ParseFile("Roles/БазовыеПраваБИД/Ext/Rights.xml", []byte(rightsFixture))
	if len(diags) != 0 {
		t.Fatalf("диагностик быть не должно: %+v", diags)
	}
	r := facts.RoleRights
	if r == nil {
		t.Fatal("RoleRights не заполнен")
	}
	// Имя роли в Rights.xml не пишется — оно берётся из пути.
	if r.RoleNameDisplay != "БазовыеПраваБИД" {
		t.Errorf("RoleNameDisplay = %q, хотели из пути БазовыеПраваБИД", r.RoleNameDisplay)
	}
	if !r.SetForNewObjects {
		t.Error("SetForNewObjects=true в XML потерялось")
	}
	if len(r.Objects) != 1 {
		t.Fatalf("Objects = %+v, хотели ровно один перечисленный объект — парсер не должен выдумывать строки под отсутствующие объекты", r.Objects)
	}
	obj := r.Objects[0]
	if obj.ObjectNameRaw != "InformationRegister.НастройкиАвторизацииВ1СДокументообороте" {
		t.Errorf("ObjectNameRaw = %q", obj.ObjectNameRaw)
	}
	if len(obj.Rights) != 3 {
		t.Fatalf("Rights = %+v, хотели 3 строки (Read, Update, Delete) — value=false тоже сырой факт", obj.Rights)
	}

	byName := map[string]RoleRightEntryFact{}
	for _, right := range obj.Rights {
		byName[right.Name] = right
	}
	read, ok := byName["Read"]
	if !ok || !read.Value {
		t.Fatalf("Read = %+v, ok=%v", read, ok)
	}
	if len(read.RLS) != 1 || read.RLS[0].Condition != "ГДЕ Пользователь = &ТекущийПользователь" {
		t.Errorf("Read.RLS = %+v", read.RLS)
	}
	del, ok := byName["Delete"]
	if !ok {
		t.Fatal("Delete должен остаться сырым фактом даже при value=false")
	}
	if del.Value {
		t.Error("Delete: value=false в XML не должно стать true")
	}
	if len(del.RLS) != 0 {
		t.Errorf("Delete: RLS = %+v, ограничений в XML не было", del.RLS)
	}
}

// setForNewObjects=false — самый частый случай в выгрузке (1087 ролей УТ,
// подавляющее большинство — false). Проверка на противоположном значении, не
// на нуле по умолчанию структуры.
func TestParseRoleRightsSetForNewObjectsFalse(t *testing.T) {
	src := []byte("\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>
<Rights xmlns="http://v8.1c.ru/8.2/roles" version="2.20">
	<setForNewObjects>false</setForNewObjects>
	<object>
		<name>Task.ЗадачаИсполнителя.AddressingAttribute.ДополнительныйОбъектАдресации</name>
		<right><name>View</name><value>false</value></right>
		<right><name>Edit</name><value>false</value></right>
	</object>
</Rights>`)
	facts, _ := ParseFile("Roles/ЧтениеСчетовНаОплатуКлиентам/Ext/Rights.xml", src)
	r := facts.RoleRights
	if r == nil {
		t.Fatal("RoleRights не заполнен")
	}
	if r.SetForNewObjects {
		t.Error("SetForNewObjects=false в XML не должно стать true")
	}
	if r.RoleNameDisplay != "ЧтениеСчетовНаОплатуКлиентам" {
		t.Errorf("RoleNameDisplay = %q", r.RoleNameDisplay)
	}
}
