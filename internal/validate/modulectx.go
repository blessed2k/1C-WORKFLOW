package validate

import (
	"os"
	"path/filepath"
	"strings"
)

// MemberFunc returns lower-cased member names of the syntax-reference owners
// matching the prefixes (syntax.Index.MemberNames).
type MemberFunc func(prefixes ...string) map[string]bool

// PathModule is a module file read by its place in a dump.
type PathModule struct {
	Kind       ModuleKind
	Collection string // dump directory of the owner: Documents, CommonModules, CommonForms
	Object     string // owner object; the common module, form or command for common ones
	Form       string
}

// collectionTypes maps a dump directory to the type word the syntax reference
// uses for its objects and managers (ДокументОбъект.<Имя документа>).
var collectionTypes = map[string]string{
	"Catalogs":                    "Справочник",
	"Documents":                   "Документ",
	"DataProcessors":              "Обработка",
	"Reports":                     "Отчет",
	"ChartsOfCharacteristicTypes": "ПланВидовХарактеристик",
	"ChartsOfAccounts":            "ПланСчетов",
	"ChartsOfCalculationTypes":    "ПланВидовРасчета",
	"ExchangePlans":               "ПланОбмена",
	"BusinessProcesses":           "БизнесПроцесс",
	"Tasks":                       "Задача",
	"InformationRegisters":        "РегистрСведений",
	"AccumulationRegisters":       "РегистрНакопления",
	"AccountingRegisters":         "РегистрБухгалтерии",
	"CalculationRegisters":        "РегистрРасчета",
	"Constants":                   "Константа",
	"Enums":                       "Перечисление",
	"DocumentJournals":            "ЖурналДокументов",
	"Sequences":                   "Последовательность",
	"SettingsStorages":            "ХранилищеНастроек",
}

// collectionDisplay is the singular Russian name of a collection for messages.
var collectionDisplay = map[string]string{
	"Catalogs": "Справочник", "Documents": "Документ", "DataProcessors": "Обработка", "Reports": "Отчет",
	"ChartsOfCharacteristicTypes": "ПланВидовХарактеристик", "ChartsOfAccounts": "ПланСчетов",
	"ChartsOfCalculationTypes": "ПланВидовРасчета", "ExchangePlans": "ПланОбмена",
	"BusinessProcesses": "БизнесПроцесс", "Tasks": "Задача", "InformationRegisters": "РегистрСведений",
	"AccumulationRegisters": "РегистрНакопления", "AccountingRegisters": "РегистрБухгалтерии",
	"CalculationRegisters": "РегистрРасчета", "Constants": "Константа", "Enums": "Перечисление",
	"DocumentJournals": "ЖурналДокументов", "CommonModules": "ОбщийМодуль", "CommonForms": "ОбщаяФорма",
	"CommonCommands": "ОбщаяКоманда",
}

// managerByCollectionRu maps a manager collection of code (Документы) to its
// dump directory.
var managerByCollectionRu = map[string]string{
	"справочники": "Catalogs", "документы": "Documents", "обработки": "DataProcessors", "отчеты": "Reports",
	"планывидовхарактеристик": "ChartsOfCharacteristicTypes", "планысчетов": "ChartsOfAccounts",
	"планывидоврасчета": "ChartsOfCalculationTypes", "планыобмена": "ExchangePlans",
	"бизнеспроцессы": "BusinessProcesses", "задачи": "Tasks", "регистрысведений": "InformationRegisters",
	"регистрынакопления": "AccumulationRegisters", "регистрыбухгалтерии": "AccountingRegisters",
	"регистрырасчета": "CalculationRegisters", "константы": "Constants", "перечисления": "Enums",
	"журналыдокументов": "DocumentJournals",
}

// ClassifyPath reads the kind of a module and its owner from the tail of its
// path, the way DumpConfigToFiles lays a dump out. It works on paths inside a
// component and on loose files alike (an external data processor's sources).
func ClassifyPath(path string) (PathModule, bool) {
	s := strings.Split(filepath.ToSlash(path), "/")
	n := len(s)
	at := func(i int) string {
		if i < 0 || i >= n {
			return ""
		}
		return s[i]
	}
	last := strings.ToLower(at(n - 1))
	switch {
	case last == "module.bsl" && at(n-2) == "Form" && at(n-3) == "Ext" && at(n-5) == "Forms":
		return PathModule{Kind: KindForm, Collection: at(n - 7), Object: at(n - 6), Form: at(n - 4)}, true
	case last == "module.bsl" && at(n-2) == "Form" && at(n-3) == "Ext" && at(n-5) == "CommonForms":
		return PathModule{Kind: KindForm, Collection: "CommonForms", Object: at(n - 4), Form: at(n - 4)}, true
	case last == "module.bsl" && at(n-2) == "Ext" && at(n-4) == "CommonModules":
		return PathModule{Kind: KindCommon, Collection: "CommonModules", Object: at(n - 3)}, true
	case last == "module.bsl" && at(n-2) == "Ext" &&
		(at(n-4) == "HTTPServices" || at(n-4) == "WebServices" || at(n-4) == "IntegrationServices"):
		return PathModule{Kind: KindService, Collection: at(n - 4), Object: at(n - 3)}, true
	case last == "objectmodule.bsl" && at(n-2) == "Ext":
		return PathModule{Kind: KindObject, Collection: at(n - 4), Object: at(n - 3)}, true
	case last == "managermodule.bsl" && at(n-2) == "Ext":
		return PathModule{Kind: KindManager, Collection: at(n - 4), Object: at(n - 3)}, true
	case last == "recordsetmodule.bsl" && at(n-2) == "Ext":
		return PathModule{Kind: KindRecordSet, Collection: at(n - 4), Object: at(n - 3)}, true
	case last == "valuemanagermodule.bsl" && at(n-2) == "Ext":
		return PathModule{Kind: KindValueManager, Collection: at(n - 4), Object: at(n - 3)}, true
	case last == "commandmodule.bsl" && at(n-2) == "Ext" && at(n-4) == "CommonCommands":
		return PathModule{Kind: KindCommand, Collection: "CommonCommands", Object: at(n - 3)}, true
	case last == "commandmodule.bsl" && at(n-2) == "Ext" && at(n-4) == "Commands":
		return PathModule{Kind: KindCommand, Collection: at(n - 6), Object: at(n - 5)}, true
	case (last == "managedapplicationmodule.bsl" || last == "ordinaryapplicationmodule.bsl") && at(n-2) == "Ext":
		return PathModule{Kind: KindApplication}, true
	case last == "sessionmodule.bsl" && at(n-2) == "Ext":
		return PathModule{Kind: KindSession}, true
	case last == "externalconnectionmodule.bsl" && at(n-2) == "Ext":
		return PathModule{Kind: KindExternalConnection}, true
	}
	return PathModule{}, false
}

// ModuleInput says what module a draft is for. Path wins; Module (as find_api
// names it: ОбщегоНазначения, Справочники.Номенклатура) and Kind are the
// fallbacks for a draft that is not on disk yet.
type ModuleInput struct {
	Path   string
	Module string
	Kind   ModuleKind
}

// ResolveModule builds the module context of a draft. p may be nil: the kind
// and the context type still come from the path.
func ResolveModule(p *DiskProject, in ModuleInput, members MemberFunc) *ModuleContext {
	if members == nil {
		members = func(...string) map[string]bool { return nil }
	}
	if strings.TrimSpace(in.Path) != "" {
		return resolveByPath(p, in.Path, members)
	}
	if mod := strings.TrimSpace(in.Module); mod != "" && p != nil {
		if mc := resolveByName(p, mod, members); mc != nil {
			return mc
		}
	}
	mc := &ModuleContext{Kind: in.Kind, ContextNames: members(contextPrefixes(in.Kind, "")...)}
	mc.ContextComplete = !attributeKind(in.Kind)
	return mc
}

func resolveByPath(p *DiskProject, path string, members MemberFunc) *ModuleContext {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	var comp Component
	rel := filepath.ToSlash(abs)
	inComp := false
	if p != nil {
		comp, rel, inComp = p.ComponentFor(abs)
		if !inComp {
			rel = filepath.ToSlash(abs)
		}
	}
	pm, ok := ClassifyPath(rel)
	mc := &ModuleContext{Kind: pm.Kind}
	if !ok {
		mc.ContextNames = members(contextPrefixes(KindUnknown, "")...)
		if decls, read := readDecls(abs); read {
			mc.Methods = keysOf(decls)
		}
		return mc
	}
	mc.Display = displayName(pm)
	mc.Default = defaultAvailability(p, pm)
	typ := collectionTypes[pm.Collection]
	mc.ContextNames = members(contextPrefixes(pm.Kind, typ)...)
	if mc.ContextNames == nil {
		mc.ContextNames = map[string]bool{}
	}

	// The same module in every component: an extension module reaches the
	// methods of the module it extends, and the base module the methods the
	// extensions add.
	mc.Methods = map[string]bool{}
	if decls, read := readDecls(abs); read {
		for k := range decls {
			mc.Methods[k] = true
		}
	}
	if inComp {
		for _, c := range p.Components() {
			if decls, read := readDecls(filepath.Join(c.Root, filepath.FromSlash(rel))); read {
				for k := range decls {
					mc.Methods[k] = true
				}
			}
		}
	}

	switch pm.Kind {
	case KindObject:
		resolveObject(p, comp, inComp, pm, typ, mc, members)
	case KindForm:
		formXML := filepath.Join(filepath.Dir(filepath.Dir(abs)), "Form.xml")
		found := false
		add := func(path string) {
			if attrs, ok := readFormAttributes(path); ok {
				found = true
				for k := range attrs {
					mc.ContextNames[k] = true
				}
			}
		}
		add(formXML)
		if inComp {
			relForm := filepath.Join(filepath.Dir(filepath.Dir(filepath.FromSlash(rel))), "Form.xml")
			for _, c := range p.Components() {
				add(filepath.Join(c.Root, relForm))
			}
		}
		mc.ContextComplete = found
	default:
		mc.ContextComplete = true
	}
	if inComp && comp.Extension {
		mc.Extension = comp.ID
	}
	return mc
}

// resolveObject fills the object-module context: attributes of the object in
// every component, common attributes, and for a borrowed object the base
// attributes the extension did not borrow.
func resolveObject(p *DiskProject, comp Component, inComp bool, pm PathModule, typ string, mc *ModuleContext, members MemberFunc) {
	props := map[string]string{}
	for k := range members(typ + "Объект.") {
		props[k] = ""
	}
	if !inComp || typ == "" {
		mc.ObjectProps = props
		return
	}
	xmlRel := filepath.Join(pm.Collection, pm.Object+".xml")
	base := map[string]string{}
	own := map[string]string{}
	adopted := false
	found := false
	for _, c := range p.Components() {
		path := filepath.Join(c.Root, xmlRel)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		info := readMetaInfo(path)
		found = true
		for k, v := range info.attrs {
			mc.ContextNames[k] = true
			if !c.Extension {
				base[k] = v
			}
		}
		if samePath(c.Root, comp.Root) {
			adopted = info.adopted
			own = info.attrs
		}
	}
	for k := range p.commonAttributesLocked() {
		mc.ContextNames[k] = true
	}
	mc.ContextComplete = found
	if comp.Extension && adopted {
		mc.Unborrowed = map[string]string{}
		for k, v := range base {
			if _, ok := own[k]; !ok {
				mc.Unborrowed[k] = v
			}
		}
		for k, v := range own {
			props[k] = v
		}
	} else {
		for k, v := range base {
			props[k] = v
		}
		for k, v := range own {
			props[k] = v
		}
	}
	mc.ObjectProps = props
}

func (p *DiskProject) commonAttributesLocked() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.commonAttributes()
}

// resolveByName finds a module named as find_api names it.
func resolveByName(p *DiskProject, name string, members MemberFunc) *ModuleContext {
	parts := strings.SplitN(name, ".", 2)
	if len(parts) == 1 {
		for _, c := range p.Components() {
			if e, ok := commonModuleIndex(c.Root)[strings.ToLower(name)]; ok {
				return resolveByPath(p, filepath.Join(c.Root, "CommonModules", e.name, "Ext", "Module.bsl"), members)
			}
		}
		return nil
	}
	coll := managerByCollectionRu[strings.ToLower(parts[0])]
	if coll == "" {
		return nil
	}
	// Справочники.X names the object, not one of its modules: the context is
	// the union of the object and manager modules, attributes unknown.
	typ := collectionTypes[coll]
	mc := &ModuleContext{Kind: KindUnknown, Display: collectionDisplay[coll] + "." + parts[1], Methods: map[string]bool{}}
	mc.ContextNames = members(contextPrefixes(KindUnknown, typ)...)
	if mc.ContextNames == nil {
		mc.ContextNames = map[string]bool{}
	}
	for _, c := range p.Components() {
		dir := filepath.Join(c.Root, coll, parts[1], "Ext")
		for _, f := range []string{"ObjectModule.bsl", "ManagerModule.bsl", "RecordSetModule.bsl"} {
			if decls, read := readDecls(filepath.Join(dir, f)); read {
				for k := range decls {
					mc.Methods[k] = true
				}
			}
		}
		if info := readMetaInfo(filepath.Join(c.Root, coll, parts[1]+".xml")); info.attrs != nil {
			for k := range info.attrs {
				mc.ContextNames[k] = true
			}
		}
	}
	return mc
}

// defaultAvailability: where a method without a directive runs. The platform's
// default directive is &НаСервере; a common module runs where its flags say;
// the application module is the client's.
func defaultAvailability(p *DiskProject, pm PathModule) Availability {
	switch pm.Kind {
	case KindCommon:
		if p != nil {
			if cm, ok := p.CommonModule(pm.Object); ok && cm.Availability != (Availability{}) {
				return cm.Availability
			}
		}
		return Availability{Client: true, Server: true}
	case KindApplication:
		return Availability{Client: true}
	}
	return Availability{Server: true}
}

// contextPrefixes names the syntax-reference owners whose members a module of
// this kind reaches without a prefix. An unknown kind or type gets the union:
// a wider context only hides findings, it never invents one.
func contextPrefixes(kind ModuleKind, typ string) []string {
	form := []string{"ФормаКлиентскогоПриложения", "Расширение формы клиентского приложения для "}
	var types []string
	if typ != "" {
		types = []string{typ}
	} else {
		for _, t := range collectionTypes {
			types = append(types, t)
		}
	}
	var out []string
	add := func(suffix string) {
		for _, t := range types {
			out = append(out, t+suffix)
		}
	}
	switch kind {
	case KindObject:
		add("Объект.")
	case KindManager:
		add("Менеджер.")
	case KindRecordSet:
		add("НаборЗаписей.")
	case KindValueManager:
		out = append(out, "КонстантаМенеджерЗначения.")
	case KindForm:
		out = append(out, form...)
	case KindUnknown:
		add("Объект.")
		add("Менеджер.")
		add("НаборЗаписей.")
		out = append(out, "КонстантаМенеджерЗначения.")
		out = append(out, form...)
	}
	return out
}

func displayName(pm PathModule) string {
	switch pm.Kind {
	case KindCommon:
		return pm.Object
	case KindForm:
		if pm.Collection == "CommonForms" {
			return "ОбщаяФорма." + pm.Object
		}
		return objectDisplay(pm) + ".Форма." + pm.Form
	case KindObject:
		return objectDisplay(pm) + " (модуль объекта)"
	case KindManager:
		return objectDisplay(pm) + " (модуль менеджера)"
	case KindRecordSet:
		return objectDisplay(pm) + " (модуль набора записей)"
	case KindCommand:
		return objectDisplay(pm) + " (модуль команды)"
	}
	return objectDisplay(pm)
}

func objectDisplay(pm PathModule) string {
	if d := collectionDisplay[pm.Collection]; d != "" {
		return d + "." + pm.Object
	}
	return pm.Object
}

func keysOf(m map[string]MethodInfo) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
