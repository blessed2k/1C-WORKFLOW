package source

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// The 1C XML export wraps every object in <MetaDataObject> with a mix of a
// default namespace (MDClasses) and prefixed namespaces (v8, xr, ...). Go's
// encoding/xml matches struct tags to elements by local name when the tag omits
// a namespace, so the types below deliberately use bare local names.

// xmlConfigRoot is the root of Configuration.xml.
type xmlConfigRoot struct {
	XMLName       xml.Name         `xml:"MetaDataObject"`
	Configuration xmlConfiguration `xml:"Configuration"`
}

type xmlConfiguration struct {
	UUID         string          `xml:"uuid,attr"`
	Properties   xmlConfigProps  `xml:"Properties"`
	ChildObjects xmlChildObjects `xml:"ChildObjects"`
}

type xmlConfigProps struct {
	Name                          string     `xml:"Name"`
	Synonym                       xmlSynonym `xml:"Synonym"`
	Vendor                        string     `xml:"Vendor"`
	Version                       string     `xml:"Version"`
	ScriptVariant                 string     `xml:"ScriptVariant"`
	DefaultRunMode                string     `xml:"DefaultRunMode"`
	ConfigurationExtensionPurpose string     `xml:"ConfigurationExtensionPurpose"`
	NamePrefix                    string     `xml:"NamePrefix"`
}

type xmlSynonym struct {
	Items []xmlLangContent `xml:"item"`
}

type xmlLangContent struct {
	Lang    string `xml:"lang"`
	Content string `xml:"content"`
}

// xmlChildObjects captures the heterogeneous list of child objects, where each
// element name is the metadata type and its text is the object name.
type xmlChildObjects struct {
	Items []xmlNamedType `xml:",any"`
}

type xmlNamedType struct {
	XMLName xml.Name
	Name    string `xml:",chardata"`
}

// ru returns the Russian synonym, falling back to the first available one.
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

// xmlObjectRoot is the root of an object file (Catalogs/Name.xml, ...). The
// single child (Catalog, Document, InformationRegister, ...) is captured
// generically so one parser serves every object type.
type xmlObjectRoot struct {
	XMLName xml.Name  `xml:"MetaDataObject"`
	Object  xmlObject `xml:",any"`
}

type xmlObject struct {
	XMLName      xml.Name
	UUID         string         `xml:"uuid,attr"`
	Properties   xmlObjProps    `xml:"Properties"`
	ChildObjects xmlObjChildren `xml:"ChildObjects"`
}

type xmlObjProps struct {
	Name            string     `xml:"Name"`
	Synonym         xmlSynonym `xml:"Synonym"`
	ObjectBelonging string     `xml:"ObjectBelonging"` // extension export: "Adopted" for borrowed objects
	RegisterRecords xmlItems2  `xml:"RegisterRecords"` // Document: registers it posts to
	// Query-schema-relevant properties. They appear only on the object types
	// that own them; absent elements unmarshal to the zero value.
	Hierarchical      string    `xml:"Hierarchical"`                  // Catalog: "true"/"false"
	HierarchyType     string    `xml:"HierarchyType"`                 // Catalog: HierarchyFoldersAndItems/HierarchyOfItems
	CodeLength        string    `xml:"CodeLength"`                    // Catalog
	DescriptionLength string    `xml:"DescriptionLength"`             // Catalog
	Owners            xmlOwners `xml:"Owners"`                        // Catalog: non-empty => subordinate
	Posting           string    `xml:"Posting"`                       // Document: "Allow"/"Deny"
	Periodicity       string    `xml:"InformationRegisterPeriodicity"` // InformationRegister
	WriteMode         string    `xml:"WriteMode"`                     // InformationRegister: Independent/RecorderSubordinate
	RegisterType      string    `xml:"RegisterType"`                  // AccumulationRegister: Balance/Turnovers
}

// xmlOwners captures whether a catalog is subordinate: any inner content means
// it has at least one owner.
type xmlOwners struct {
	Inner string `xml:",innerxml"`
}

// xmlItems2 is a list of <Item> references (e.g. RegisterRecords entries like
// "AccumulationRegister.ТоварыНаСкладах").
type xmlItems2 struct {
	Items []string `xml:"Item"`
}

func (p xmlObjProps) hierarchical() bool { return p.Hierarchical == "true" }
func (p xmlObjProps) hasGroups() bool {
	return p.hierarchical() && p.HierarchyType == "HierarchyFoldersAndItems"
}
func (p xmlObjProps) subordinate() bool  { return strings.TrimSpace(p.Owners.Inner) != "" }
func (p xmlObjProps) hasCode() bool      { return p.CodeLength != "" && p.CodeLength != "0" }
func (p xmlObjProps) hasName() bool      { return p.DescriptionLength != "" && p.DescriptionLength != "0" }
func (p xmlObjProps) posts() bool        { return p.Posting != "Deny" } // documents post by default
func (p xmlObjProps) periodic() bool     { return p.Periodicity != "" && p.Periodicity != "Nonperiodical" }
func (p xmlObjProps) byRecorder() bool   { return p.WriteMode == "RecorderSubordinate" }
func (p xmlObjProps) balanceType() bool  { return p.RegisterType == "" || p.RegisterType == "Balance" }

type xmlObjChildren struct {
	Items []xmlChildElem `xml:",any"`
}

// xmlChildElem is any element inside <ChildObjects>: an attribute-like element
// carrying <Properties>, a text-only reference such as <Form>Name</Form>, or a
// tabular section carrying its own nested <ChildObjects>.
type xmlChildElem struct {
	XMLName      xml.Name
	Props        *xmlChildProps  `xml:"Properties"`
	ChildObjects *xmlObjChildren `xml:"ChildObjects"`
	CharData     string          `xml:",chardata"`
}

type xmlChildProps struct {
	Name     string  `xml:"Name"`
	Type     xmlType `xml:"Type"`
	Indexing string  `xml:"Indexing"` // Index, IndexWithAdditionalOrder or DontIndex
}

// indexed reports whether a field is indexed (Indexing = Index or
// IndexWithAdditionalOrder).
func (e xmlChildElem) indexed() bool {
	if e.Props == nil {
		return false
	}
	return e.Props.Indexing == "Index" || e.Props.Indexing == "IndexWithAdditionalOrder"
}

// xmlType holds the one or more <v8:Type> identifiers of a typed field.
type xmlType struct {
	Types []string `xml:"Type"`
}

// name returns the object name: the <Properties><Name> when present, otherwise
// the element's own text (for <Form>, <Template> and similar references).
func (e xmlChildElem) name() string {
	if e.Props != nil && e.Props.Name != "" {
		return e.Props.Name
	}
	return strings.TrimSpace(e.CharData)
}

// typeList returns the field's type identifiers, if any.
func (e xmlChildElem) typeList() []string {
	if e.Props == nil {
		return nil
	}
	return e.Props.Type.Types
}

// xmlForm is the root of a managed form file (Forms/Name/Ext/Form.xml).
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

// xmlItem is a UI element. Only <ChildItems> is followed for nesting, so
// context menus, command bars and tooltips do not pollute the item list.
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
	Name    string     `xml:"name,attr"`
	Type    xmlType    `xml:"Type"`
	Main    bool       `xml:"MainAttribute"`
	Columns xmlColumns `xml:"Columns"`
}

type xmlColumns struct {
	Columns []xmlColumn `xml:"Column"`
}

type xmlColumn struct {
	Name string  `xml:"name,attr"`
	Type xmlType `xml:"Type"`
}

type xmlFormCmds struct {
	Commands []xmlFormCmd `xml:"Command"`
}

type xmlFormCmd struct {
	Name   string `xml:"name,attr"`
	Action string `xml:"Action"`
}

// xmlRights is the root of a role's Rights.xml (Roles/<Name>/Ext/Rights.xml).
// Rights.xml records deviations from the role's defaults, so an object missing
// from Objects does not have to mean "no rights": with setForNewObjects the role
// covers objects it never lists (that is how ПолныеПрава exports as a few lines).
type xmlRights struct {
	XMLName          xml.Name         `xml:"Rights"`
	SetForNewObjects string           `xml:"setForNewObjects"` // "true" = устанавливать права для новых объектов
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

// xmlRestriction is one RLS restriction of a right: optional field scope plus
// the condition text.
type xmlRestriction struct {
	Fields    []string `xml:"field"`
	Condition string   `xml:"condition"`
}

// utf8BOM is the UTF-8 byte-order mark that some 1C export files carry.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// stripBOM removes a leading UTF-8 BOM if present.
func stripBOM(b []byte) []byte { return bytes.TrimPrefix(b, utf8BOM) }

// readXML reads an XML file, strips a leading UTF-8 BOM, and unmarshals it into
// v. 1C exports are UTF-8; the object files sometimes carry a BOM that the xml
// decoder would otherwise reject.
func readXML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = stripBOM(data)
	if err := xml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
