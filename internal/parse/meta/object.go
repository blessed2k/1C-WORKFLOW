package meta

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// parseMetadataObject разбирает файл-корень объекта метаданных: справочник,
// документ, регистр, общий модуль, роль (только имя — права в отдельном
// файле), регламентное задание, EPF/ERF-корень и любой другой вид,
// перечисленный под <MetaDataObject> выгрузки.
func parseMetadataObject(relPath string, src []byte) (Facts, []domain.Diagnostic) {
	var root xmlObjectRoot
	if diags := unmarshalXML(relPath, src, &root); diags != nil {
		return Facts{}, diags
	}
	obj := root.Object
	mtype := obj.XMLName.Local
	name := obj.Properties.Name

	var diags []domain.Diagnostic
	if strings.TrimSpace(name) == "" {
		diags = append(diags, domain.Diagnostic{
			Code:     DiagEmptyName,
			Severity: domain.SeverityWarning,
			Message:  "объект метаданных " + mtype + " без имени",
			File:     relPath,
		})
	}

	facts := Facts{
		Object: &MetadataObjectFact{
			MType:       mtype,
			UUID:        obj.UUID,
			NameNorm:    domain.NormalizeName(name),
			NameDisplay: name,
			Synonym:     obj.Properties.Synonym.ru(),
			Comment:     obj.Properties.Comment,
			Props:       scalarProps(obj.Properties.Extra),
		},
	}

	ownerBase := strings.TrimSuffix(domain.NormalizeModulePath(relPath), ".xml")
	walkChildObjects(obj.ChildObjects.Items, "", &facts, ownerBase)

	switch mtype {
	case "Document":
		facts.Document = &DocumentFact{RegisterRecords: registerRecordsFrom(obj.Properties)}
	case "HTTPService":
		// RootURL остаётся и среди Props: карточка объекта показывает его
		// как прежде.
		facts.HTTPService = httpServiceFrom(facts.Object.Props["RootURL"], obj.ChildObjects.Items)
	case "CommonModule":
		facts.ModuleRegistry = moduleRegistryFrom(obj.Properties)
	case "ScheduledJob":
		facts.ScheduledJob = &ScheduledJobFact{
			NameNorm:    domain.NormalizeName(name),
			NameDisplay: name,
			MethodRaw:   obj.Properties.MethodName,
			Use:         obj.Properties.Use.Value,
			Predefined:  obj.Properties.Predefined.Value,
		}
	case "Role":
		facts.Role = &RoleFact{NameNorm: domain.NormalizeName(name), NameDisplay: name}
	case "CommonForm":
		// Общая форма самоидентична: объект и есть форма, без отдельной
		// ссылки-владельца. Ключ совпадает с тем, что даёт Form.xml того же
		// пути (formKeyFromStructurePath), потому что "Ext/Form.xml" под ним
		// не добавляет "/Forms/": один и тот же ownerBase с обеих сторон.
		facts.FormDecls = append(facts.FormDecls, FormDeclFact{
			Key:         ownerBase,
			NameNorm:    domain.NormalizeName(name),
			NameDisplay: name,
		})
	}

	return facts, diags
}

// walkChildObjects обходит <ChildObjects> объекта (или табличной части) и
// раскладывает элементы по категориям Facts. parentNorm — нормализованное имя
// владеющей табличной части (пусто на верхнем уровне).
func walkChildObjects(items []xmlChildElem, parentNorm string, facts *Facts, ownerBase string) {
	for _, e := range items {
		kind := e.XMLName.Local
		name := e.name()
		switch kind {
		case "Form":
			facts.FormDecls = append(facts.FormDecls, FormDeclFact{
				Key:         ownerBase + "/Forms/" + name,
				NameNorm:    domain.NormalizeName(name),
				NameDisplay: name,
			})
		case "Command":
			facts.Commands = append(facts.Commands, ObjectCommandFact{
				NameNorm:    domain.NormalizeName(name),
				NameDisplay: name,
			})
		case "TabularSection", "StandardTabularSection":
			facts.Members = append(facts.Members, MetadataMemberFact{
				Kind:        kind,
				NameNorm:    domain.NormalizeName(name),
				NameDisplay: name,
				ParentNorm:  parentNorm,
			})
			if e.ChildObjects != nil {
				walkChildObjects(e.ChildObjects.Items, domain.NormalizeName(name), facts, ownerBase)
			}
		default:
			// Реквизит, ресурс, измерение, значение перечисления, реквизит
			// адресации задачи, макет-ссылка и подобные: одна и та же форма
			// факта для всех, различаемая только Kind.
			facts.Members = append(facts.Members, MetadataMemberFact{
				Kind:        kind,
				NameNorm:    domain.NormalizeName(name),
				NameDisplay: name,
				Types:       e.typeList(),
				Indexed:     e.indexed(),
				ParentNorm:  parentNorm,
			})
		}
	}
}

// scalarProps собирает плоские скалярные свойства объекта, захваченные
// xmlProperties.Extra, в карту "имя элемента -> текст". Свойства с непустым
// внутренним содержимым (StandardAttributes, Owners, RegisterRecords, ...) не
// дают chardata и в карту не попадают — это осознанное ограничение: их разбор
// не нужен ни одному критерию приёмки этого таска.
func scalarProps(extra []xmlScalarProp) map[string]string {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]string, len(extra))
	for _, p := range extra {
		v := strings.TrimSpace(p.Value)
		if v == "" {
			continue
		}
		if _, exists := out[p.XMLName.Local]; !exists {
			out[p.XMLName.Local] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// registerRecordsFrom собирает движения, декларированные метаданными
// документа. Возвращает непустой (пусть и нулевой длины) срез: у документа без
// движений список пустой, а не отсутствующий.
func registerRecordsFrom(p xmlProperties) []string {
	out := make([]string, 0, len(p.RegisterRecords.Items))
	for _, item := range p.RegisterRecords.Items {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// moduleRegistryFrom извлекает свойства общего модуля ИЗ РЕАЛЬНЫХ полей XML —
// критерий приёмки таска: internal/source/extpoints.go выводит клиент/сервер
// по имени модуля, этот пакет обязан читать факт.
func moduleRegistryFrom(p xmlProperties) *ModuleRegistryFact {
	return &ModuleRegistryFact{
		Global:                    p.Global.Value,
		Server:                    p.Server.Value,
		ClientManagedApplication:  p.ClientManagedApplication.Value,
		ClientOrdinaryApplication: p.ClientOrdinaryApplication.Value,
		ExternalConnection:        p.ExternalConnection.Value,
		ServerCall:                p.ServerCall.Value,
		Privileged:                p.Privileged.Value,
		ReturnValuesReuse:         p.ReturnValuesReuse,
	}
}

// httpServiceFrom собирает корневой URL, шаблоны и методы HTTP-сервиса
// (веха В2): это принимающая сторона сшивки HTTP-вызовов между базами.
// Метод без обработчика или шаблон без методов не теряются: сервис всё равно
// адресуем по пути, а пустое место видно по полям.
func httpServiceFrom(rootURL string, items []xmlChildElem) *HTTPServiceFact {
	f := &HTTPServiceFact{RootURL: strings.TrimSpace(rootURL)}
	for _, e := range items {
		if e.XMLName.Local != "URLTemplate" || e.Props == nil {
			continue
		}
		tpl := HTTPTemplateFact{
			NameDisplay: e.Props.Name,
			Template:    strings.TrimSpace(e.Props.Template),
		}
		if e.ChildObjects != nil {
			for _, m := range e.ChildObjects.Items {
				if m.XMLName.Local != "Method" || m.Props == nil {
					continue
				}
				tpl.Methods = append(tpl.Methods, HTTPMethodFact{
					NameDisplay: m.Props.Name,
					HTTPMethod:  strings.ToUpper(strings.TrimSpace(m.Props.HTTPMethod)),
					Handler:     strings.TrimSpace(m.Props.Handler),
				})
			}
		}
		f.Templates = append(f.Templates, tpl)
	}
	return f
}
