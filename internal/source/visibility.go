package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// VisibilityReport answers the question that follows "нет прав": why a user does
// not see this object, this attribute or this command. Rights are only one of
// three reasons. The other two are a functional option that is switched off and
// an object that belongs to no subsystem at all, and neither of them is visible
// in the object itself.
type VisibilityReport struct {
	Object            string                `json:"object"`
	Subsystems        []SubsystemPlacement  `json:"subsystems"`
	FunctionalOptions []FunctionalOptionRef `json:"functionalOptions,omitempty"`
	RolesGranting     int                   `json:"rolesGranting" jsonschema:"how many roles grant any right on the object; -1 when not requested"`
	Findings          []VisibilityIssue     `json:"findings,omitempty"`
	Note              string                `json:"note,omitempty"`
}

// SubsystemPlacement is one subsystem the object is placed in.
type SubsystemPlacement struct {
	Path               string `json:"path" jsonschema:"full path, e.g. Продажи.ОптовыеПродажи"`
	InCommandInterface bool   `json:"inCommandInterface" jsonschema:"the subsystem AND all its ancestors are included in the command interface"`
}

// FunctionalOptionRef is one functional option that controls the object or a
// part of it.
type FunctionalOptionRef struct {
	Name         string `json:"name"`
	Synonym      string `json:"synonym,omitempty"`
	Location     string `json:"location" jsonschema:"where the value is stored: a constant or an attribute"`
	Scope        string `json:"scope" jsonschema:"what exactly the option controls: the object itself, an attribute, a command, a tabular section or a whole subsystem"`
	Parametrised bool   `json:"parametrised,omitempty" jsonschema:"the value is stored per parameter (by warehouse, organisation and so on), there is no single value to look at"`
	Privileged   bool   `json:"privilegedGetMode,omitempty" jsonschema:"the value is read in privileged mode"`
}

// VisibilityIssue is one reason the object may be invisible.
type VisibilityIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Where   string `json:"where,omitempty"`
}

// xmlFunctionalOption is the root of FunctionalOptions/<Name>.xml.
type xmlFunctionalOption struct {
	Object struct {
		Properties struct {
			Name              string     `xml:"Name"`
			Synonym           xmlSynonym `xml:"Synonym"`
			Location          string     `xml:"Location"`
			PrivilegedGetMode string     `xml:"PrivilegedGetMode"`
			Content           struct {
				Objects []string `xml:"Object"`
			} `xml:"Content"`
		} `xml:"Properties"`
	} `xml:"FunctionalOption"`
}

// xmlSubsystem is the root of Subsystems/<Name>.xml (and of a nested subsystem).
type xmlSubsystem struct {
	Object struct {
		Properties struct {
			Name                      string `xml:"Name"`
			IncludeInCommandInterface string `xml:"IncludeInCommandInterface"`
			Content                   struct {
				Items []string `xml:"Item"`
			} `xml:"Content"`
		} `xml:"Properties"`
		ChildObjects struct {
			Subsystems []string `xml:"Subsystem"`
		} `xml:"ChildObjects"`
	} `xml:"Subsystem"`
}

// VisibilityAudit reports what can hide the object from a user.
func (s *XMLSource) VisibilityAudit(ctx context.Context, objectType, name string, withRights bool) (*VisibilityReport, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	if strings.ContainsAny(objectType+name, `/\`) || strings.Contains(objectType+name, "..") {
		return nil, fmt.Errorf("object type and name must be metadata names, not paths")
	}
	objectType = canonicalObjectType(objectType)
	if err := readXML(filepath.Join(s.root, folderForType(objectType), name+".xml"), &xmlObjectRoot{}); err != nil {
		return nil, err
	}
	full := objectType + "." + name
	placements, err := s.subsystemsOf(ctx, full)
	if err != nil {
		// A cancelled scan must not be reported as "the object is nowhere".
		return nil, err
	}
	options, err := s.functionalOptionsOf(ctx, full)
	if err != nil {
		return nil, err
	}
	out := &VisibilityReport{
		Object:            metadataLabel(objectType) + "." + name,
		Subsystems:        placements,
		FunctionalOptions: append(options, s.subsystemOptions(ctx, placements)...),
	}
	// Rights cost more than the rest of the report together (1087 role files in
	// УТ, about a second), and rights_audit answers that question in full, so the
	// count is opt-in and the report says when it was not taken.
	out.RolesGranting = -1
	if withRights {
		if audit, err := s.RightsAudit(ctx, objectType, name); err == nil {
			out.RolesGranting = len(audit.Granting)
		}
	}
	out.Findings = visibilityFindings(out, objectType)
	out.Note = "видимость в интерфейсе требует трёх вещей: объект размещён в подсистеме, попадающей в командный интерфейс; включена хотя бы одна из функциональных опций, в состав которых он входит (у нескольких опций логика ИЛИ, а объект без опций виден всегда); у роли пользователя есть право. Права проверяют первыми, хотя чаще виноваты опция или раздел."
	return out, nil
}

// subsystemsOf returns the subsystems the object is placed in, each with the
// full path and whether that placement actually reaches the command interface.
//
// The flag matters more than the placement: in УТ 333 subsystems of 462 have
// IncludeInCommandInterface=false, and 774 objects of 1443 have no placement
// that reaches the interface at all. A report that lists two placements and says
// nothing else answers "где объект" but not "почему его не видно".
func (s *XMLSource) subsystemsOf(ctx context.Context, full string) ([]SubsystemPlacement, error) {
	out := []SubsystemPlacement{}
	root := filepath.Join(s.root, "Subsystems")
	entries, err := os.ReadDir(root)
	if err != nil {
		return out, nil // no subsystems in this export
	}
	var walk func(dir, prefix string, names []string, ancestorsVisible bool) error
	walk = func(dir, prefix string, names []string, ancestorsVisible bool) error {
		for _, n := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			var sub xmlSubsystem
			if err := readXML(filepath.Join(dir, n+".xml"), &sub); err != nil {
				continue
			}
			path := n
			if prefix != "" {
				path = prefix + "." + n
			}
			// An empty flag means the default, which is "included".
			visible := ancestorsVisible && !strings.EqualFold(strings.TrimSpace(sub.Object.Properties.IncludeInCommandInterface), "false")
			for _, item := range sub.Object.Properties.Content.Items {
				if strings.EqualFold(strings.TrimSpace(item), full) {
					out = append(out, SubsystemPlacement{Path: path, InCommandInterface: visible})
					break
				}
			}
			if kids := sub.Object.ChildObjects.Subsystems; len(kids) > 0 {
				if err := walk(filepath.Join(dir, n, "Subsystems"), path, kids, visible); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var top []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".xml") {
			top = append(top, strings.TrimSuffix(e.Name(), ".xml"))
		}
	}
	sort.Strings(top)
	if err := walk(root, "", top, true); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// functionalOptionsOf returns the functional options whose content includes the
// object or one of its attributes. An entry is "Document.X" for the object
// itself and "Document.X.Attribute.Y" (or a tabular section path) for a part of
// it: the second form is why an attribute can vanish while the object stays.
func (s *XMLSource) functionalOptionsOf(ctx context.Context, full string) ([]FunctionalOptionRef, error) {
	var out []FunctionalOptionRef
	for _, p := range s.cachedFunctionalOptions() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// No break: one option routinely controls the object in several places at
		// once (the object itself plus a handful of its attributes). Stopping at
		// the first match dropped 32% of the content in УТ, and where the "whole
		// object" entry happened to come second, the report claimed the object
		// stayed visible while the option actually hides it.
		for _, obj := range p.content {
			scope, ok := optionScope(strings.TrimSpace(obj), full)
			if !ok {
				continue
			}
			out = append(out, FunctionalOptionRef{
				Name:         p.name,
				Synonym:      p.synonym,
				Location:     russifyMetadataRef(p.location),
				Parametrised: !strings.HasPrefix(p.location, "Constant."),
				Scope:        scope,
				Privileged:   p.privileged,
			})
		}
	}
	return out, nil
}

// subsystemOptions reports the options that switch off a whole subsystem the
// object is placed in. In УТ 42 subsystems are controlled this way and 2645
// objects sit under them: without this, a user whose whole section disappeared
// (базовая версия, отключённый раздел) gets a list of attribute options and no
// reason at all.
func (s *XMLSource) subsystemOptions(ctx context.Context, placements []SubsystemPlacement) []FunctionalOptionRef {
	var out []FunctionalOptionRef
	seen := map[string]bool{}
	for _, p := range placements {
		// Every level of the path can be switched off on its own.
		parts := strings.Split(p.Path, ".")
		for i := range parts {
			ref := "Subsystem." + strings.Join(parts[:i+1], ".Subsystem.")
			opts, err := s.functionalOptionsOf(ctx, ref)
			if err != nil {
				return out
			}
			for _, o := range opts {
				key := o.Name + "|" + p.Path
				if seen[key] {
					continue
				}
				seen[key] = true
				o.Scope = "раздел " + strings.Join(parts[:i+1], ".")
				out = append(out, o)
			}
		}
	}
	return out
}

// scopeWholeObject is the scope value that means the option hides the object
// itself rather than a part of it. Findings are split by it, so it is a constant
// rather than a phrase repeated in two files.
const scopeWholeObject = "объект целиком"

// partKind names the kind of a content entry. A functional option controls not
// only attributes: in УТ its content also holds 241 commands, 234 dimensions,
// 226 resources and 159 whole tabular sections, and calling all of those
// "реквизит" is the difference between "a field disappeared" and "a command
// disappeared".
var partKind = map[string]string{
	"Attribute":      "реквизит",
	"Command":        "команда",
	"Dimension":      "измерение",
	"Resource":       "ресурс",
	"TabularSection": "табличная часть",
	"Form":           "форма",
	"Template":       "макет",
	"Subsystem":      "подсистема",
}

// optionScope reports whether a content entry concerns the object, and what part
// of it.
func optionScope(entry, full string) (string, bool) {
	if strings.EqualFold(entry, full) {
		return scopeWholeObject, true
	}
	rest, ok := cutPrefixFold(entry, full+".")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, ".")
	// A tabular section attribute is "TabularSection.Т.Attribute.Х": name it by
	// the pair, everything else by its own kind.
	if len(parts) == 4 && strings.EqualFold(parts[0], "TabularSection") && strings.EqualFold(parts[2], "Attribute") {
		return "реквизит табличной части " + parts[1] + "." + parts[3], true
	}
	if len(parts) == 2 {
		if kind, ok := partKind[parts[0]]; ok {
			return kind + " " + parts[1], true
		}
		return parts[0] + " " + parts[1], true
	}
	return "часть объекта: " + rest, true
}

// cutPrefixFold is strings.CutPrefix, case-insensitive.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// russifyMetadataRef turns "Constant.X" into "Константа.X" for the location of a
// functional option value; unknown kinds are returned as they are.
func russifyMetadataRef(ref string) string {
	parts := strings.Split(ref, ".")
	for i, p := range parts {
		switch {
		case i == 0 && p == "Constant":
			parts[i] = "Константа"
		case i == 0 && queryPrefix[p] != "":
			parts[i] = queryPrefix[p]
		case partKind[p] != "":
			// Attribute / Resource stay English otherwise: the reference points at
			// where to look for the value, so it has to read as a path.
			parts[i] = partKindRef[p]
		}
	}
	return strings.Join(parts, ".")
}

// partKindRef names a path segment inside a metadata reference.
var partKindRef = map[string]string{
	"Attribute": "Реквизит", "Resource": "Ресурс", "Dimension": "Измерение",
	"TabularSection": "ТабличнаяЧасть", "Command": "Команда", "Form": "Форма",
	"Template": "Макет", "Subsystem": "Подсистема",
}

// firstN joins up to n items, saying how many were left out.
func firstN(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" и ещё %d", len(items)-n)
}

// visibilityFindings turns the collected facts into the reasons a user may not
// see the object.
func visibilityFindings(rep *VisibilityReport, objectType string) []VisibilityIssue {
	var out []VisibilityIssue

	reachable := 0
	for _, p := range rep.Subsystems {
		if p.InCommandInterface {
			reachable++
		}
	}
	interactive := map[string]bool{
		"Catalog": true, "Document": true, "Report": true, "DataProcessor": true,
		"ChartOfCharacteristicTypes": true, "ChartOfAccounts": true, "ChartOfCalculationTypes": true,
		"BusinessProcess": true, "Task": true, "DocumentJournal": true, "ExchangePlan": true,
	}
	if interactive[objectType] {
		switch {
		case len(rep.Subsystems) == 0:
			out = append(out, VisibilityIssue{
				Code:    "NotInAnySubsystem",
				Message: "объект не входит ни в одну подсистему: в командном интерфейсе его не будет, открыть можно только программно или из другой формы",
			})
		case reachable == 0:
			var paths []string
			for _, p := range rep.Subsystems {
				paths = append(paths, p.Path)
			}
			out = append(out, VisibilityIssue{
				Code:    "NotInCommandInterface",
				Message: "объект размещён только в подсистемах, которые сами (или их родители) не включены в командный интерфейс: в разделах программы его не видно",
				Where:   strings.Join(paths, ", "),
			})
		}
	}

	var whole, sections, parts []string
	for _, fo := range rep.FunctionalOptions {
		switch {
		case fo.Scope == scopeWholeObject:
			whole = append(whole, fo.Name)
		case strings.HasPrefix(fo.Scope, "раздел "):
			sections = append(sections, fo.Name+" ("+fo.Scope+")")
		default:
			parts = append(parts, fo.Name+" ("+fo.Scope+")")
		}
	}
	// Several options over one object work as OR: the object is visible while at
	// least one of them is on, and an object with no options at all is always
	// visible. Stating this as AND (as the first version did) inverts the answer
	// on every object covered by more than one option, and in УТ that is 132 of
	// them, including the panels switched on by any of "НеИспользоватьНесколько…".
	if len(whole) > 0 {
		out = append(out, VisibilityIssue{
			Code:    "HiddenByFunctionalOption",
			Message: fmt.Sprintf("видимость объекта целиком управляется функциональными опциями (%d шт.): объект виден, пока включена ХОТЯ БЫ ОДНА из них, и пропадает, когда выключены все. Где смотреть значение - поле location", len(whole)),
			Where:   firstN(whole, 8),
		})
	}
	if len(sections) > 0 {
		out = append(out, VisibilityIssue{
			Code:    "SectionHiddenByFunctionalOption",
			Message: "раздел, в котором размещён объект, сам управляется функциональной опцией: при её выключении пропадает весь раздел вместе с объектом",
			Where:   firstN(sections, 5),
		})
	}
	if len(parts) > 0 {
		out = append(out, VisibilityIssue{
			Code:    "PartHiddenByFunctionalOption",
			Message: fmt.Sprintf("функциональные опции управляют отдельными частями объекта (%d шт.: реквизиты, команды, табличные части): объект остаётся видимым, а часть исчезает. Полный список в functionalOptions", len(parts)),
			Where:   firstN(parts, 5),
		})
	}
	if rep.RolesGranting == 0 {
		out = append(out, VisibilityIssue{
			Code:    "NoRoleGrantsRights",
			Message: "ни одна роль не даёт прав на объект: доступ есть только у ролей с установкой прав для новых объектов, см. rights_audit",
		})
	}
	return out
}
