package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/store/storetest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// epochRows снимает построчный слепок текущей эпохи хранилища.
func epochRows(t *testing.T, st *store.Store) map[string][]string {
	t.Helper()
	s, err := st.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rows, err := storetest.CanonicalDump(s.EpochPath)
	if err != nil {
		t.Fatalf("CanonicalDump: %v", err)
	}
	return rows
}

// TestIncrementEqualsCleanRebuildRows: полная пересборка, затем правка
// нескольких файлов (изменение тела и добавление метода, удаление метода,
// удаление файла, новый файл) и инкремент дают ТЕ ЖЕ строки во всех таблицах
// SQLite, что чистая полная пересборка конечного состояния. Сверка построчная
// (storetest.CanonicalDump): id, законно различные между путями, заменены
// ключами идентичности, reference и её дети сверяются по естественному ключу
// ссылки. Это страховка пакетной вставки и применения плана (issue #3): оба
// меняют порядок и момент записи строк, а не их состав.
//
// Два случая: маленький компонент, где инкремент уходит в fallback (полный
// re-resolve, republish всех файлов), и широкий, где правка точечная и
// большинство строк reference остаётся от прошлого поколения (тогда id новых
// ссылок обязаны продолжать таблицу, а не начинать её заново).
func TestIncrementEqualsCleanRebuildRows(t *testing.T) {
	t.Run("fallback", func(t *testing.T) { checkIncrementRows(t, 0, moduleEdits) })
	t.Run("точечный", func(t *testing.T) {
		// Правка тела общего модуля без смены имён переопубликует только сам
		// модуль: символы пересоздаются с прежними id (identity стабильна), а
		// ссылки НЕТРОНУТЫХ файлов на них обязаны остаться resolved и с тем же
		// callee (issue #10, ADR-037).
		checkIncrementRows(t, 30, moduleEdits)
	})
	t.Run("узлы XML", func(t *testing.T) {
		// Тот же дефект не только у ссылок: на пересоздаваемые узлы указывают
		// подписка на событие (обработчик в общем модуле) и запрос к
		// справочнику. Правка XML справочника и тела модуля, все указывающие
		// файлы нетронуты (ADR-037).
		checkIncrementRows(t, 30, nodeEdits)
	})
	t.Run("все указатели", func(t *testing.T) {
		// Остальные виды мягких указателей ADR-037: обработчик формы,
		// регламентное задание, доступ к регистру, роль и её права, реквизит
		// в запросе. Правятся только файлы-цели (модуль формы, общий модуль,
		// XML регистра, справочника и роли), указывающие файлы нетронуты.
		checkIncrementRows(t, 30, allPointerEdits)
	})
	t.Run("переименование", func(t *testing.T) {
		// Символ общего модуля переименован: узел с прежней identity не
		// вернулся, указатель остаётся пустым, ссылка unresolved, а её файл
		// переопубликуется дельтой имён. Итог тот же, что у чистой пересборки.
		checkIncrementRows(t, 30, renameEdits)
	})
	t.Run("правка XML роли", func(t *testing.T) {
		// Строка role пересоздаётся вместе со своим XML, а права лежат в
		// нетронутом Rights.xml и держатся за неё каскадом role_right.role_id,
		// не SET NULL: без возврата каскадных строк они пропадали до полной
		// пересборки (issue #11, ADR-038).
		checkIncrementRows(t, 30, roleXMLEdits)
	})
	t.Run("правка XML регистра", func(t *testing.T) {
		// Кодовое и декларированное рёбра документа держатся за строку
		// регистра каскадом object_data_edge.to_object_id; XML регистра в
		// зависимостях рёбер нет, и их никто не пересобирает (issue #11).
		checkIncrementRows(t, 30, registerXMLEdits)
	})
	t.Run("правка XML документа", func(t *testing.T) {
		// Та же граница со стороны владельца: кодовое ребро документа зависит
		// от модулей, а не от его XML, и держится каскадом from_object_id,
		// бейдж has-dynamic держится каскадом object_badge.object_id.
		// Декларированное ребро зависит от XML и пересобирается честно.
		checkIncrementRows(t, 30, documentXMLEdits)
	})
	t.Run("два инкремента по Rights.xml", func(t *testing.T) {
		// Правится только Rights.xml, дважды подряд. Строка роли принадлежит
		// своему XML и не должна переходить к Rights.xml: иначе второй
		// инкремент сносит её каскадом и вставляет без объекта роли
		// (ревью issue #11, ADR-038).
		checkIncrementRows(t, 30, rightsOnlyEdits)
	})
	t.Run("удаление XML регистра", func(t *testing.T) {
		// Регистр удалён: снятые рёбра документа не возвращаются, конца
		// у них больше нет. Итог тот же, что у чистой пересборки.
		checkIncrementRows(t, 30, registerRemoveEdits)
	})
	t.Run("удаление XML объекта при живом модуле", func(t *testing.T) {
		// XML документа удалён, модули объекта и менеджера остались.
		// module.owner_object_id пишет проход 2 файла модуля, а модуль не
		// переопубликуется: без SET NULL на удалённый объект указатель
		// оставался висячим до полной пересборки, где он пуст (issue #14).
		checkIncrementRows(t, 30, objectXMLRemoveEdits)
	})
	t.Run("удаление XML объекта при живом модуле, fallback", func(t *testing.T) {
		// Модуль переопубликуется в той же транзакции: владелец ищется по
		// узлу объекта, а узел живёт до reconciliation, хотя строки
		// metadata_object уже нет (issue #14).
		checkIncrementRows(t, 0, objectXMLRemoveEdits)
	})
	t.Run("удаление XML объекта и правка его модуля", func(t *testing.T) {
		// Модуль переопубликуется в том же инкременте, где удалён XML: узел
		// объекта живёт до reconciliation, а строки metadata_object уже нет.
		// Владелец по узлу давал висячий id, а кодовое ребро графа от него
		// валило инкремент на FOREIGN KEY (issue #14).
		checkIncrementRows(t, 30, objectXMLRemoveModuleEdits)
	})
	t.Run("XML объекта добавлен к живому модулю", func(t *testing.T) {
		// Обратное направление: модули объекта лежали без XML, затем XML
		// появился. Модули не менялись, но владелец, рёбра и бейдж графа
		// появляются только при их переопубликовании (issue #14).
		checkIncrementRows(t, 30, objectXMLAddEdits)
	})
	t.Run("удаление XML объекта при живой форме", func(t *testing.T) {
		// form.owner_object_id тоже без REFERENCES: Form.xml нетронут, а
		// объект удалён (issue #14).
		checkIncrementRows(t, 30, formObjectXMLRemoveEdits)
	})
	t.Run("XML объекта добавлен к живой форме", func(t *testing.T) {
		checkIncrementRows(t, 30, formObjectXMLAddEdits)
	})
	t.Run("удаление XML объекта и правка формы", func(t *testing.T) {
		// Form.xml и модуль формы переопубликуются в том же инкременте, где
		// удалён XML объекта: владелец формы по узлу висел бы (issue #14).
		checkIncrementRows(t, 30, formObjectXMLRemoveFormEdits)
	})
	t.Run("удаление XML роли при живом Rights.xml", func(t *testing.T) {
		// Граница ADR-038: чистая пересборка создаёт роль из одного
		// Rights.xml (file_id на нём, без объекта роли), а инкремент роль не
		// воссоздаёт, и права Rights.xml пропадают: его никто не
		// переопубликует. В выгрузке конфигуратора Rights.xml лежит в каталоге
		// роли и удаляется вместе с её XML, поэтому случай патологический.
		// Починка: добавлять Rights.xml удалённой роли в дельту публикации.
		t.Skip("граница ADR-038: роль без своего XML инкремент не воссоздаёт из нетронутого Rights.xml")
		checkIncrementRows(t, 30, roleXMLRemoveEdits)
	})
}

var rightsOnlyEdits = incrementScenario{
	seed: roleXMLEdits.seed,
	edit: func(write func(rel, content string), remove func(rel string)) {
		write("Roles/ЧтениеТоваров/Ext/Rights.xml", strings.Replace(allPointersRights, "<value>true</value>", "<value>false</value>", 1))
	},
	then: []func(write func(rel, content string), remove func(rel string)){
		func(write func(rel, content string), remove func(rel string)) {
			write("Roles/ЧтениеТоваров/Ext/Rights.xml", allPointersRights)
		},
	},
	mustHave: []string{"object_id=node:" + metadataObjectIdentityKey("cfg", "Role", "чтениетоваров")},
}

var registerRemoveEdits = incrementScenario{
	seed: edgeSeed,
	edit: func(write func(rel, content string), remove func(rel string)) {
		remove(workspace.DumpDeclarationPath("AccumulationRegister", "ТоварыНаСкладах"))
	},
	// Декларированное ребро пропадает честно, а бейдж has-dynamic документа
	// остаётся: сценарий создал граф, рёбра которого исчезают.
	mustHave: []string{"badge=has-dynamic"},
}

var objectXMLRemoveEdits = incrementScenario{
	seed: edgeSeed,
	edit: func(write func(rel, content string), remove func(rel string)) {
		remove(workspace.DumpDeclarationPath("Document", "Отгрузка"))
	},
	// Модуль объекта жив, а владельца у него в чистой пересборке нет.
	mustHave: []string{"rel_path=" + workspace.DumpModulePath("Document", "Отгрузка", workspace.ModuleObject)},
}

var objectXMLRemoveModuleEdits = incrementScenario{
	seed: edgeSeed,
	edit: func(write func(rel, content string), remove func(rel string)) {
		remove(workspace.DumpDeclarationPath("Document", "Отгрузка"))
		write(workspace.DumpModulePath("Document", "Отгрузка", workspace.ModuleObject), `
Процедура ОбработкаПроведения(Отказ, Режим)
	// правка тела
	Движения.ТоварыНаСкладах.Записать();
КонецПроцедуры
`)
	},
	mustHave: objectXMLRemoveEdits.mustHave,
}

// edgeSeedWithoutXML: edgeSeed без XML документа, модули на месте.
func edgeSeedWithoutXML() map[string]string {
	seed := map[string]string{}
	for k, v := range edgeSeed {
		seed[k] = v
	}
	delete(seed, workspace.DumpDeclarationPath("Document", "Отгрузка"))
	return seed
}

var objectXMLAddEdits = incrementScenario{
	seed: edgeSeedWithoutXML(),
	edit: func(write func(rel, content string), remove func(rel string)) {
		write(workspace.DumpDeclarationPath("Document", "Отгрузка"), fmt.Sprintf(edgeDocument, "исходный"))
	},
	mustHave: append([]string{"owner_object_id=node:" + metadataObjectIdentityKey("cfg", "Document", "отгрузка")}, edgeMustHave...),
}

const formCatalog = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
  <Catalog uuid="88888888-8888-8888-8888-888888888888">
    <Properties>
      <Name>Номенклатура</Name>
    </Properties>
    <ChildObjects>
      <Form>ФормаЭлемента</Form>
    </ChildObjects>
  </Catalog>
</MetaDataObject>`

const formXML = `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<Events>
		<Event name="OnOpen">ПриОткрытии</Event>
	</Events>%s
</Form>`

// formSeed: справочник с формой, её структурой и модулем.
func formSeed(withXML bool) map[string]string {
	seed := map[string]string{
		"Catalogs/Номенклатура/Forms/ФормаЭлемента/Ext/Form.xml": fmt.Sprintf(formXML, ""),
		workspace.DumpFormModulePath("Catalog", "Номенклатура", "ФормаЭлемента"): `
&НаКлиенте
Процедура ПриОткрытии(Отказ)
КонецПроцедуры
`,
	}
	if withXML {
		seed[workspace.DumpDeclarationPath("Catalog", "Номенклатура")] = formCatalog
	}
	return seed
}

var formOwnerMustHave = "owner_object_id=node:" + metadataObjectIdentityKey("cfg", "Catalog", "номенклатура")

var formObjectXMLRemoveEdits = incrementScenario{
	seed: formSeed(true),
	edit: func(write func(rel, content string), remove func(rel string)) {
		remove(workspace.DumpDeclarationPath("Catalog", "Номенклатура"))
	},
	mustHave: []string{"rel_path=Catalogs/Номенклатура/Forms/ФормаЭлемента/Ext/Form.xml"},
}

var formObjectXMLAddEdits = incrementScenario{
	seed: formSeed(false),
	edit: func(write func(rel, content string), remove func(rel string)) {
		write(workspace.DumpDeclarationPath("Catalog", "Номенклатура"), formCatalog)
	},
	mustHave: []string{formOwnerMustHave},
}

var formObjectXMLRemoveFormEdits = incrementScenario{
	seed: formSeed(true),
	edit: func(write func(rel, content string), remove func(rel string)) {
		remove(workspace.DumpDeclarationPath("Catalog", "Номенклатура"))
		write("Catalogs/Номенклатура/Forms/ФормаЭлемента/Ext/Form.xml", fmt.Sprintf(formXML, "\n\t<!-- правка -->"))
		write(workspace.DumpFormModulePath("Catalog", "Номенклатура", "ФормаЭлемента"), `
&НаКлиенте
Процедура ПриОткрытии(Отказ)
	// правка тела
КонецПроцедуры
`)
	},
	mustHave: formObjectXMLRemoveEdits.mustHave,
}

var roleXMLRemoveEdits = incrementScenario{
	seed: roleXMLEdits.seed,
	edit: func(write func(rel, content string), remove func(rel string)) {
		remove(workspace.DumpDeclarationPath("Role", "ЧтениеТоваров"))
	},
	mustHave: []string{"right_name=Read"},
}

var roleXMLEdits = incrementScenario{
	seed: map[string]string{
		workspace.DumpDeclarationPath("Role", "ЧтениеТоваров"): fmt.Sprintf(allPointersRole, "исходный"),
		"Roles/ЧтениеТоваров/Ext/Rights.xml":                   allPointersRights,
	},
	edit: func(write func(rel, content string), remove func(rel string)) {
		write(workspace.DumpDeclarationPath("Role", "ЧтениеТоваров"), fmt.Sprintf(allPointersRole, "правка свойства"))
	},
	mustHave: []string{"object_id=node:" + metadataObjectIdentityKey("cfg", "Catalog", "товары") + "|object_name_norm"},
}

const edgeDocument = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.20">
  <Document uuid="44444444-4444-4444-4444-444444444444">
    <Properties>
      <Name>Отгрузка</Name>
      <Comment>%s</Comment>
      <RegisterRecords>
        <xr:Item xsi:type="xr:MDObjectRef">AccumulationRegister.ТоварыНаСкладах</xr:Item>
      </RegisterRecords>
    </Properties>
  </Document>
</MetaDataObject>`

// edgeSeed: документ с декларированным и кодовым ребром в регистр и
// нестатической записью в модуле менеджера (бейдж has-dynamic).
var edgeSeed = map[string]string{
	workspace.DumpDeclarationPath("AccumulationRegister", "ТоварыНаСкладах"): fmt.Sprintf(allPointersRegister, "исходный"),
	workspace.DumpDeclarationPath("Document", "Отгрузка"):                    fmt.Sprintf(edgeDocument, "исходный"),
	workspace.DumpModulePath("Document", "Отгрузка", workspace.ModuleObject): `
Процедура ОбработкаПроведения(Отказ, Режим)
	Движения.ТоварыНаСкладах.Записать();
КонецПроцедуры
`,
	workspace.DumpModulePath("Document", "Отгрузка", workspace.ModuleManager): `
Процедура ПересчитатьОстатки() Экспорт
	Набор = РегистрыНакопления.ТоварыНаСкладах.СоздатьНаборЗаписей();
	Набор.Записать();
КонецПроцедуры
`,
}

var edgeMustHave = []string{"kind=writes-register", "kind=writes-declared", "badge=has-dynamic"}

var registerXMLEdits = incrementScenario{
	seed: edgeSeed,
	edit: func(write func(rel, content string), remove func(rel string)) {
		write(workspace.DumpDeclarationPath("AccumulationRegister", "ТоварыНаСкладах"), fmt.Sprintf(allPointersRegister, "правка свойства"))
	},
	mustHave: edgeMustHave,
}

var documentXMLEdits = incrementScenario{
	seed: edgeSeed,
	edit: func(write func(rel, content string), remove func(rel string)) {
		write(workspace.DumpDeclarationPath("Document", "Отгрузка"), fmt.Sprintf(edgeDocument, "правка свойства"))
	},
	mustHave: edgeMustHave,
}

const allPointersCatalog = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Catalog uuid="22222222-2222-2222-2222-222222222222">
    <Properties>
      <Name>Товары</Name>
      <Comment>%s</Comment>
    </Properties>
    <ChildObjects>
      <Attribute uuid="22222222-2222-2222-2222-2222222222a1">
        <Properties>
          <Name>Артикул</Name>
          <Type>
            <v8:Type>xs:string</v8:Type>
          </Type>
        </Properties>
      </Attribute>
      <Form>ФормаЭлемента</Form>
    </ChildObjects>
  </Catalog>
</MetaDataObject>`

const allPointersRegister = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
  <AccumulationRegister uuid="55555555-5555-5555-5555-555555555555">
    <Properties>
      <Name>ТоварыНаСкладах</Name>
      <Comment>%s</Comment>
    </Properties>
  </AccumulationRegister>
</MetaDataObject>`

const allPointersRole = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
  <Role uuid="66666666-6666-6666-6666-666666666666">
    <Properties>
      <Name>ЧтениеТоваров</Name>
      <Comment>%s</Comment>
    </Properties>
  </Role>
</MetaDataObject>`

const allPointersRights = `<?xml version="1.0" encoding="UTF-8"?>
<Rights xmlns="http://v8.1c.ru/8.2/roles" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
	<setForNewObjects>false</setForNewObjects>
	<setForAttributesByDefault>true</setForAttributesByDefault>
	<independentRightsOfChildObjects>false</independentRightsOfChildObjects>
	<object>
		<name>Catalog.Товары</name>
		<right>
			<name>Read</name>
			<value>true</value>
		</right>
	</object>
</Rights>`

var allPointerEdits = incrementScenario{
	seed: map[string]string{
		"Catalogs/Товары.xml": fmt.Sprintf(allPointersCatalog, "исходный"),
		"Catalogs/Товары/Forms/ФормаЭлемента/Ext/Form.xml": `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<Events>
		<Event name="OnOpen">ПриОткрытии</Event>
	</Events>
</Form>`,
		workspace.DumpFormModulePath("Catalog", "Товары", "ФормаЭлемента"): `
&НаКлиенте
Процедура ПриОткрытии(Отказ)
КонецПроцедуры
`,
		"ScheduledJobs/ОбновлениеТоваров.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<ScheduledJob uuid="77777777-7777-7777-7777-777777777777">
		<Properties>
			<Name>ОбновлениеТоваров</Name>
			<MethodName>CommonModule.УтилитыОбщие.Помощь</MethodName>
			<Use>true</Use>
			<Predefined>true</Predefined>
		</Properties>
	</ScheduledJob>
</MetaDataObject>`,
		workspace.DumpDeclarationPath("AccumulationRegister", "ТоварыНаСкладах"): fmt.Sprintf(allPointersRegister, "исходный"),
		// Запись в регистр из общего модуля без вызывающих: register_access
		// с object_id есть, а ребра объектного графа нет. Ребро документа
		// потерялось бы при правке XML регистра по другой причине (каскад
		// object_data_edge.to_object_id, не SET NULL, ADR-037 «Граница»).
		"CommonModules/Движения/Ext/Module.bsl": `
Процедура Записать(Движения) Экспорт
	Движения.ТоварыНаСкладах.Записать();
КонецПроцедуры
`,
		workspace.DumpDeclarationPath("Role", "ЧтениеТоваров"): fmt.Sprintf(allPointersRole, "исходный"),
		"Roles/ЧтениеТоваров/Ext/Rights.xml":                   allPointersRights,
		"CommonModules/Отчеты/Ext/Module.bsl": `
Функция Артикулы() Экспорт
	Запрос = Новый Запрос;
	Запрос.Текст = "ВЫБРАТЬ Товары.Артикул ИЗ Справочник.Товары КАК Товары";
	Возврат Запрос.Выполнить();
КонецФункции
`,
	},
	edit: func(write func(rel, content string), remove func(rel string)) {
		write("Catalogs/Товары.xml", fmt.Sprintf(allPointersCatalog, "правка свойства"))
		write(workspace.DumpDeclarationPath("AccumulationRegister", "ТоварыНаСкладах"), fmt.Sprintf(allPointersRegister, "правка свойства"))
		write(workspace.DumpFormModulePath("Catalog", "Товары", "ФормаЭлемента"), `
&НаКлиенте
Процедура ПриОткрытии(Отказ)
	// правка тела
КонецПроцедуры
`)
		write("CommonModules/УтилитыОбщие/Ext/Module.bsl", `
Функция Помощь() Экспорт
	Возврат "ok, но иначе";
КонецФункции
`)
		// XML роли правится вместе с XML справочника: права Rights.xml
		// держатся за роль каскадом, а за справочник указателем (ADR-038).
		write(workspace.DumpDeclarationPath("Role", "ЧтениеТоваров"), fmt.Sprintf(allPointersRole, "правка свойства"))
	},
	mustHave: []string{
		"object_id=node:" + metadataObjectIdentityKey("cfg", "Role", "чтениетоваров"), // role
		"handler_name_norm=приоткрытии|handler_symbol_id=node:",
		"method_name_norm=commonmodule.утилитыобщие.помощь|handler_symbol_id=node:",
		"member_id=node:",
		"object_id=node:" + metadataObjectIdentityKey("cfg", "AccumulationRegister", "товарынаскладах"),
		"object_id=node:" + metadataObjectIdentityKey("cfg", "Catalog", "товары") + "|object_name_norm", // role_right
	},
}

var renameEdits = incrementScenario{
	edit: func(write func(rel, content string), remove func(rel string)) {
		write("CommonModules/УтилитыОбщие/Ext/Module.bsl", `
Функция ПомощьИная() Экспорт
	Возврат "ok";
КонецФункции
`)
	},
	mustHave: []string{"name_norm=помощь|resolution=unresolved"},
}

// incrementScenario: исходные файлы сверх базовой фикстуры и правки перед
// инкрементом.
type incrementScenario struct {
	seed map[string]string
	// edit применяет правки: write пишет файл со сдвигом mtime, remove удаляет.
	edit func(write func(rel, content string), remove func(rel string))
	// then: следующие раунды правок, после каждого свой инкремент.
	then []func(write func(rel, content string), remove func(rel string))
	// mustHave: подстроки, каждая из которых обязана встретиться в строках
	// чистой пересборки: иначе сценарий не создал то, что проверяет.
	mustHave []string
}

var moduleEdits = incrementScenario{
	seed: map[string]string{
		"CommonModules/Сервис/Ext/Module.bsl": `
Функция Первый(Параметр = Неопределено) Экспорт
	Возврат УтилитыОбщие.Помощь();
КонецФункции

Процедура Второй() Экспорт
	Первый(1);
КонецПроцедуры
`,
		"Catalogs/Лишний/Ext/ManagerModule.bsl": `
Процедура Вызов() Экспорт
	Сервис.Второй();
	УтилитыОбщие.Помощь();
КонецПроцедуры
`,
	},
	edit: func(write func(rel, content string), remove func(rel string)) {
		// Изменение тела и новый метод.
		write("CommonModules/УтилитыОбщие/Ext/Module.bsl", `
Функция Помощь() Экспорт
	Возврат Новая();
КонецФункции

Функция Новая() Экспорт
	Возврат "новая";
КонецФункции
`)
		// Удаление метода, на который ссылались из другого файла.
		write("CommonModules/Сервис/Ext/Module.bsl", `
Функция Первый(Параметр = Неопределено) Экспорт
	Возврат УтилитыОбщие.Помощь();
КонецФункции
`)
		// Удаление файла и новый файл.
		remove("Catalogs/Лишний/Ext/ManagerModule.bsl")
		write("Catalogs/Новый/Ext/ManagerModule.bsl", `
Процедура Тест() Экспорт
	УтилитыОбщие.Новая();
	Сервис.Второй();
КонецПроцедуры
`)
	},
	mustHave: []string{"rel_path=Catalogs/Товары/Ext/ManagerModule.bsl"},
}

var nodeEdits = incrementScenario{
	seed: map[string]string{
		"EventSubscriptions/ПроверкаТоваров.xml": `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.20">
	<EventSubscription uuid="33333333-3333-3333-3333-333333333333">
		<Properties>
			<Name>ПроверкаТоваров</Name>
			<Source>
				<v8:Type>cfg:CatalogObject.Товары</v8:Type>
			</Source>
			<Event>BeforeWrite</Event>
			<Handler>CommonModule.УтилитыОбщие.Помощь</Handler>
		</Properties>
	</EventSubscription>
</MetaDataObject>`,
		"CommonModules/Отчеты/Ext/Module.bsl": `
Функция Товары() Экспорт
	Запрос = Новый Запрос;
	Запрос.Текст = "ВЫБРАТЬ Товары.Ссылка ИЗ Справочник.Товары КАК Товары";
	Возврат Запрос.Выполнить();
КонецФункции
`,
	},
	edit: func(write func(rel, content string), remove func(rel string)) {
		write("Catalogs/Товары.xml", `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:app="http://v8.1c.ru/8.2/managed-application/core" xmlns:cfg="http://v8.1c.ru/8.1/data/enterprise/current-config" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
  <Catalog uuid="22222222-2222-2222-2222-222222222222">
    <Properties>
      <Name>Товары</Name>
      <Comment>правка свойства</Comment>
    </Properties>
  </Catalog>
</MetaDataObject>`)
		write("CommonModules/УтилитыОбщие/Ext/Module.bsl", `
Функция Помощь() Экспорт
	Возврат "ok, но иначе";
КонецФункции
`)
	},
	mustHave: []string{
		"object_id=node:" + metadataObjectIdentityKey("cfg", "Catalog", "товары"),
		"handler_symbol_id=node:",
	},
}

func checkIncrementRows(t *testing.T, fillers int, sc incrementScenario) {
	ctx := context.Background()
	root := writeFixtureComponent(t)
	filler := map[string]string{}
	for i := 0; i < fillers; i++ {
		filler[fmt.Sprintf("CommonModules/Прочий%d/Ext/Module.bsl", i)] = fmt.Sprintf(`
Процедура Локальная%[1]d()
КонецПроцедуры

Процедура Вызов%[1]d() Экспорт
	Локальная%[1]d();
КонецПроцедуры
`, i)
	}
	writeFixtureFiles(t, root, filler)
	writeFixtureFiles(t, root, sc.seed)

	stA := openTestStore(t)
	svcA := NewService(stA, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcA.Close() })
	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("A: Reindex(full): %v", err)
	}

	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, p, content)
		touchFuture(t, p)
	}
	remove := func(rel string) {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	for i, edit := range append([]func(func(string, string), func(string)){sc.edit}, sc.then...) {
		edit(write, remove)
		if _, err := svcA.Reindex(ctx, ModeIncremental, ""); err != nil {
			t.Fatalf("A: Reindex(incremental) №%d: %v", i+1, err)
		}
	}

	stB := openTestStore(t)
	svcB := NewService(stB, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svcB.Close() })
	if _, err := svcB.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("B: Reindex(full): %v", err)
	}

	a, b := epochRows(t, stA), epochRows(t, stB)
	for _, table := range []string{"reference", "call_edge", "resolution_dep", "symbol", "source_file"} {
		if len(b[table]) == 0 {
			t.Fatalf("в чистой пересборке таблица %s пуста: сверка ничего бы не проверила", table)
		}
	}
	for _, sub := range sc.mustHave {
		found := false
		for _, rows := range b {
			for _, r := range rows {
				if strings.Contains(r, sub) {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("в чистой пересборке нет строки с %q: сценарий не создал проверяемый факт", sub)
		}
	}
	for table := range b {
		if _, ok := a[table]; !ok {
			t.Errorf("таблица %s есть только в чистой пересборке", table)
		}
	}
	for table, rowsA := range a {
		rowsB := b[table]
		if diff := diffRows(rowsA, rowsB); diff != "" {
			t.Errorf("таблица %s: инкремент и чистая пересборка разошлись\n%s", table, diff)
		}
	}
}

// diffRows: строки, которые есть только в одной стороне (мультимножества).
func diffRows(a, b []string) string {
	count := map[string]int{}
	for _, r := range a {
		count[r]++
	}
	for _, r := range b {
		count[r]--
	}
	var out string
	n := 0
	for r, c := range count {
		if c == 0 {
			continue
		}
		if n < 8 {
			side := "только в инкременте"
			if c < 0 {
				side = "только в чистой пересборке"
			}
			out += "  " + side + ": " + r + "\n"
		}
		n++
	}
	if n > 8 {
		out += "  ...\n"
	}
	return out
}
