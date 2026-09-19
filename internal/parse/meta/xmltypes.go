package meta

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// Выгрузка 1С оборачивает объект в <MetaDataObject> со смесью namespace по
// умолчанию (MDClasses) и с префиксом (v8, xr, ...). encoding/xml сопоставляет
// тег структуры с элементом по локальному имени, когда тег не называет
// namespace, поэтому структуры ниже намеренно используют голые локальные имена
// — тот же приём, что и в internal/source/xmltypes.go (образец семантики).

// xmlObjectRoot — корень файла объекта метаданных: <MetaDataObject><Вид>...
// Единственный дочерний элемент захватывается обобщённо (`,any`), поэтому один
// разбор обслуживает все виды объектов: Catalog, Document, CommonModule,
// InformationRegister, ExternalDataProcessor, Role, ScheduledJob и так далее.
type xmlObjectRoot struct {
	XMLName xml.Name  `xml:"MetaDataObject"`
	Object  xmlObject `xml:",any"`
}

type xmlObject struct {
	XMLName      xml.Name
	UUID         string         `xml:"uuid,attr"`
	Properties   xmlProperties  `xml:"Properties"`
	ChildObjects xmlObjChildren `xml:"ChildObjects"`
}

// xmlProperties захватывает как именованные поля, нужные логике (общий модуль,
// регламентное задание, регистр), так и произвольные скалярные свойства
// объекта — вторые идут в MetadataObjectFact.Props для схемы запросов и не
// требуют по отдельному полю на каждое из полусотни свойств выгрузки.
type xmlProperties struct {
	Name    string     `xml:"Name"`
	Synonym xmlSynonym `xml:"Synonym"`
	Comment string     `xml:"Comment"`

	// Общий модуль (module registry, критерий приёмки таска).
	Global                    xmlOptBool `xml:"Global"`
	Server                    xmlOptBool `xml:"Server"`
	ClientManagedApplication  xmlOptBool `xml:"ClientManagedApplication"`
	ClientOrdinaryApplication xmlOptBool `xml:"ClientOrdinaryApplication"`
	ExternalConnection        xmlOptBool `xml:"ExternalConnection"`
	ServerCall                xmlOptBool `xml:"ServerCall"`
	Privileged                xmlOptBool `xml:"Privileged"`
	ReturnValuesReuse         string     `xml:"ReturnValuesReuse"`

	// Документ: движения, декларированные метаданными. Явное поле, а не Extra:
	// у <RegisterRecords> сложное содержимое, chardata оно не даёт.
	RegisterRecords xmlRegisterRecords `xml:"RegisterRecords"`

	// Регламентное задание.
	MethodName string     `xml:"MethodName"`
	Use        xmlOptBool `xml:"Use"`
	Predefined xmlOptBool `xml:"Predefined"`

	// Произвольные скалярные свойства объекта (Hierarchical, Posting,
	// CodeLength, InformationRegisterPeriodicity, WriteMode, RegisterType, ...):
	// захватываются обобщённо, без поля на каждое.
	Extra []xmlScalarProp `xml:",any"`
}

// xmlRegisterRecords — <RegisterRecords> документа: список регистров, по
// которым документ делает движения. Каждый элемент записан как
// <xr:Item xsi:type="xr:MDObjectRef">AccumulationRegister.Имя</xr:Item>;
// префикс namespace тега не важен, сопоставление идёт по локальному имени.
type xmlRegisterRecords struct {
	Items []string `xml:"Item"`
}

// xmlScalarProp — один скалярный дочерний элемент <Properties>, захваченный
// как имя+текст. Элементы со сложным содержимым (Synonym, StandardAttributes,
// ...) тоже попадут сюда с пустым текстом — Extra используется только для
// плоских свойств, извлекаемых по имени.
type xmlScalarProp struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

// xmlOptBool — булево свойство выгрузки ("true"/"false"), различающее
// "поле отсутствует" от "поле = false": оба порождают Value() == false, но
// только Present различает их для module registry.
type xmlOptBool struct {
	Present bool
	Value   bool
}

func (b *xmlOptBool) UnmarshalText(text []byte) error {
	b.Present = true
	b.Value = string(text) == "true"
	return nil
}

type xmlSynonym struct {
	Items []xmlLangContent `xml:"item"`
}

type xmlLangContent struct {
	Lang    string `xml:"lang"`
	Content string `xml:"content"`
}

// ru возвращает русский синоним, иначе первый доступный.
func (s xmlSynonym) ru() string {
	for _, it := range s.Items {
		if it.Lang == "ru" {
			return it.Content
		}
	}
	if len(s.Items) > 0 {
		return s.Items[0].Content
	}
	return ""
}

type xmlObjChildren struct {
	Items []xmlChildElem `xml:",any"`
}

// xmlChildElem — любой элемент внутри <ChildObjects>: реквизит/ресурс/измерение
// с <Properties>, ссылка вида <Form>Имя</Form> текстом, табличная часть со
// своим вложенным <ChildObjects>.
type xmlChildElem struct {
	XMLName      xml.Name
	Props        *xmlChildProps  `xml:"Properties"`
	ChildObjects *xmlObjChildren `xml:"ChildObjects"`
	CharData     string          `xml:",chardata"`
}

type xmlChildProps struct {
	Name     string  `xml:"Name"`
	Type     xmlType `xml:"Type"`
	Indexing string  `xml:"Indexing"`

	// HTTP-сервис: шаблон URL (<URLTemplate>) и его метод (<Method>).
	Template   string `xml:"Template"`
	HTTPMethod string `xml:"HTTPMethod"`
	Handler    string `xml:"Handler"`
}

func (e xmlChildElem) indexed() bool {
	if e.Props == nil {
		return false
	}
	return e.Props.Indexing == "Index" || e.Props.Indexing == "IndexWithAdditionalOrder"
}

// xmlType — один или несколько идентификаторов типа <v8:Type>.
type xmlType struct {
	Types []string `xml:"Type"`
}

// name возвращает имя объекта: <Properties><Name>, когда есть, иначе
// собственный текст элемента (для <Form>, <Command>, <Template> и подобных).
func (e xmlChildElem) name() string {
	if e.Props != nil && e.Props.Name != "" {
		return e.Props.Name
	}
	return strings.TrimSpace(e.CharData)
}

func (e xmlChildElem) typeList() []string {
	if e.Props == nil {
		return nil
	}
	return e.Props.Type.Types
}

// --- подписки на события ---

// xmlSubscription — корень EventSubscriptions/<Имя>.xml. Источник задаётся
// либо <v8:Type> (один конкретный тип), либо <v8:TypeSet>: голым видом
// ("DocumentObject" — срабатывает на КАЖДЫЙ документ конфигурации) или
// ОпределяемымТипом. Чтение только <v8:Type> теряет оба случая TypeSet.
type xmlSubscription struct {
	Object struct {
		Properties struct {
			Name    string        `xml:"Name"`
			Synonym xmlSynonym    `xml:"Synonym"`
			Source  xmlSourceType `xml:"Source"`
			Event   string        `xml:"Event"`
			Handler string        `xml:"Handler"`
		} `xml:"Properties"`
	} `xml:"EventSubscription"`
}

type xmlSourceType struct {
	Types    []string `xml:"Type"`
	TypeSets []string `xml:"TypeSet"`
}

// --- права роли ---

// xmlRights — корень Roles/<Имя>/Ext/Rights.xml. Rights.xml хранит отклонения
// от умолчаний роли: отсутствие объекта в Objects не означает "нет доступа",
// когда включён setForNewObjects (см. rightsaudit.go — образец семантики).
type xmlRights struct {
	XMLName          xml.Name         `xml:"Rights"`
	SetForNewObjects string           `xml:"setForNewObjects"`
	Objects          []xmlRightObject `xml:"object"`
}

type xmlRightObject struct {
	Name   string     `xml:"name"`
	Rights []xmlRight `xml:"right"`
}

type xmlRight struct {
	Name         string           `xml:"name"`
	Value        string           `xml:"value"`
	Restrictions []xmlRestriction `xml:"restrictionByCondition"`
}

type xmlRestriction struct {
	Fields    []string `xml:"field"`
	Condition string   `xml:"condition"`
}

// --- предопределённые элементы ---

// xmlPredefinedData — корень ".../Ext/Predefined.xml".
type xmlPredefinedData struct {
	XMLName xml.Name            `xml:"PredefinedData"`
	Items   []xmlPredefinedItem `xml:"Item"`
}

type xmlPredefinedItem struct {
	ID          string `xml:"id,attr"`
	Name        string `xml:"Name"`
	Code        string `xml:"Code"`
	Description string `xml:"Description"`
	IsFolder    string `xml:"IsFolder"`
}

// --- форма ---

// xmlForm — корень управляемой формы (.../Ext/Form.xml).
type xmlForm struct {
	XMLName    xml.Name      `xml:"Form"`
	Events     xmlFormEvents `xml:"Events"`
	ChildItems xmlItems      `xml:"ChildItems"`
	Attributes xmlFormAttrs  `xml:"Attributes"`
	Commands   xmlFormCmds   `xml:"Commands"`
}

type xmlFormEvents struct {
	Events []xmlEvent `xml:"Event"`
}

type xmlEvent struct {
	Name    string `xml:"name,attr"`
	Handler string `xml:",chardata"`
}

type xmlItems struct {
	Items []xmlItem `xml:",any"`
}

// xmlItem — элемент формы. Вложенность идёт только через <ChildItems>:
// контекстные меню, командные панели и всплывающие подсказки не засоряют
// список элементов.
type xmlItem struct {
	XMLName     xml.Name
	Name        string        `xml:"name,attr"`
	DataPath    string        `xml:"DataPath"`
	CommandName string        `xml:"CommandName"`
	Events      xmlFormEvents `xml:"Events"`
	ChildItems  *xmlItems     `xml:"ChildItems"`
}

type xmlFormAttrs struct {
	Attributes []xmlFormAttr `xml:"Attribute"`
}

type xmlFormAttr struct {
	Name string  `xml:"name,attr"`
	Type xmlType `xml:"Type"`
	Main bool    `xml:"MainAttribute"`
}

type xmlFormCmds struct {
	Commands []xmlFormCmd `xml:"Command"`
}

type xmlFormCmd struct {
	Name   string `xml:"name,attr"`
	Action string `xml:"Action"`
}

// --- общее ---

// utf8BOM — маркер порядка байт UTF-8, которым иногда открываются файлы
// выгрузки.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// stripBOM убирает ведущий UTF-8 BOM, если он есть.
func stripBOM(b []byte) []byte { return bytes.TrimPrefix(b, utf8BOM) }
