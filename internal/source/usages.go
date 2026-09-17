package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// hasRefType lists metadata types that are referable by a "…Ссылка" type and so
// can appear as another object's field type.
var hasRefType = map[string]bool{
	"Catalog":                    true,
	"Document":                   true,
	"Enum":                       true,
	"ChartOfCharacteristicTypes": true,
	"ChartOfAccounts":            true,
	"ChartOfCalculationTypes":    true,
	"ExchangePlan":               true,
	"BusinessProcess":            true,
	"Task":                       true,
}

// MetadataUsages reports where an object is used across the offline export: as a
// reference type in other objects' fields, and in role rights. It scans the XML
// export, so it is O(number of objects); intended for on-demand impact analysis
// before changing or removing an object.
func (s *XMLSource) MetadataUsages(ctx context.Context, objectType, name string, withTemplates bool) (*UsageReport, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	prefix := queryPrefix[objectType]
	if prefix == "" {
		prefix = objectType
	}
	report := &UsageReport{Object: prefix + "." + name}

	if hasRefType[objectType] {
		refType := metadataKinds[objectType] + "Ссылка." + name
		usages, err := s.findTypeUsages(refType)
		if err != nil {
			return nil, err
		}
		report.AsType = usages
	}

	roleUsages, err := s.findRoleUsages(objectType + "." + name)
	if err != nil {
		return nil, err
	}
	report.InRoles = roleUsages

	// Scanning templates means reading every report schema of the configuration
	// (about 4 s on УТ against 250 ms without), so it is asked for, not assumed.
	if withTemplates {
		report.InTemplates = s.templateUsages(ctx, objectType, name)
	} else {
		report.TemplatesNote = "макеты и схемы компоновки не просматривались: вызови с withTemplates=true перед изменением реквизита или удалением объекта, отчёты ломаются именно там"
	}

	report.Total = len(report.AsType) + len(report.InRoles) + len(report.InTemplates)
	return report, nil
}

// findTypeUsages walks every configuration object and collects the fields whose
// type matches refType (e.g. "СправочникСсылка.Контрагенты").
func (s *XMLSource) findTypeUsages(refType string) ([]TypeUsage, error) {
	cfg, err := s.readConfiguration()
	if err != nil {
		return nil, err
	}

	var usages []TypeUsage
	for _, obj := range cfg.ChildObjects.Items {
		ownerType := obj.XMLName.Local
		ownerName := obj.Name
		path := filepath.Join(s.root, folderForType(ownerType), ownerName+".xml")
		var root xmlObjectRoot
		if err := readXML(path, &root); err != nil {
			continue // not every child type has an object file with fields
		}
		ownerLabel := metadataLabel(ownerType) + "." + ownerName
		for _, ch := range root.Object.ChildObjects.Items {
			switch ch.XMLName.Local {
			case "Attribute", "Dimension", "Resource":
				if typesContain(ch.typeList(), refType) {
					usages = append(usages, TypeUsage{Object: ownerLabel, Field: ch.name(), Kind: roleName(ch.XMLName.Local)})
				}
			case "TabularSection":
				if ch.ChildObjects == nil {
					continue
				}
				for _, sub := range ch.ChildObjects.Items {
					if sub.XMLName.Local == "Attribute" && typesContain(sub.typeList(), refType) {
						usages = append(usages, TypeUsage{
							Object: ownerLabel,
							Field:  ch.name() + "." + sub.name(),
							Kind:   "Реквизит ТЧ",
						})
					}
				}
			}
		}
	}
	return usages, nil
}

// findRoleUsages scans Roles/<name>/Ext/Rights.xml for granted rights on the
// object identified by its English full name (e.g. "Catalog.Контрагенты").
func (s *XMLSource) findRoleUsages(fullName string) ([]RoleUsage, error) {
	rolesDir := filepath.Join(s.root, "Roles")
	entries, err := os.ReadDir(rolesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no roles exported
		}
		return nil, err
	}

	var usages []RoleUsage
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(rolesDir, e.Name(), "Ext", "Rights.xml")
		var rights xmlRights
		if err := readXML(path, &rights); err != nil {
			continue
		}
		for _, obj := range rights.Objects {
			if obj.Name != fullName {
				continue
			}
			var granted []string
			for _, r := range obj.Rights {
				if r.Value == "true" {
					granted = append(granted, r.Name)
				}
			}
			if len(granted) > 0 {
				usages = append(usages, RoleUsage{Role: e.Name(), Rights: granted})
			}
		}
	}
	return usages, nil
}

// metadataLabel returns the Russian type prefix for a metadata type, or the type
// itself when it is not mapped.
func metadataLabel(objectType string) string {
	if ru := queryPrefix[objectType]; ru != "" {
		return ru
	}
	if ru := extraLabels[objectType]; ru != "" {
		return ru
	}
	return objectType
}

// extraLabels names the metadata types that never appear in a query and so are
// missing from queryPrefix, but do appear in reports of this server.
var extraLabels = map[string]string{
	"Report": "Отчет", "DataProcessor": "Обработка", "Constant": "Константа",
	"CommonCommand": "ОбщаяКоманда", "CommonForm": "ОбщаяФорма", "Subsystem": "Подсистема",
	"DocumentJournal": "ЖурналДокументов", "CommonModule": "ОбщийМодуль",
	"FunctionalOption": "ФункциональнаяОпция", "EventSubscription": "ПодпискаНаСобытие",
	"ScheduledJob": "РегламентноеЗадание", "Sequence": "Последовательность",
}

// roleName maps a field kind (Attribute/Dimension/Resource) to Russian.
func roleName(kind string) string {
	switch kind {
	case "Attribute":
		return "Реквизит"
	case "Dimension":
		return "Измерение"
	case "Resource":
		return "Ресурс"
	default:
		return kind
	}
}

// typesContain reports whether any raw type russifies to want.
func typesContain(raw []string, want string) bool {
	for _, t := range raw {
		if russifyType(t) == want {
			return true
		}
	}
	return false
}
