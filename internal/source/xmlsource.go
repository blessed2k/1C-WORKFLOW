package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

const defaultMaxResults = 100

// XMLSource reads a configuration from an offline 1C XML export directory (the
// output of DumpConfigToFiles), rooted at the directory holding Configuration.xml.
type XMLSource struct {
	root string

	// OtherComponents names the export roots of the remaining components of the
	// same project, in practice its extensions: code search and the rights
	// tools cover them together with root. Asked when such a tool runs, so the
	// caller's manifest is not read on every call. Nil means the main export
	// alone.
	OtherComponents func() []ComponentRoot
}

// ComponentRoot is the export root of one project component next to the main
// configuration.
type ComponentRoot struct {
	Name string
	Dir  string
}

// NewXMLSource returns a source backed by the XML export at root.
func NewXMLSource(root string) *XMLSource {
	return &XMLSource{root: root}
}

// otherComponents returns the roots of the project's remaining components.
func (s *XMLSource) otherComponents() []ComponentRoot {
	if s.OtherComponents == nil {
		return nil
	}
	return s.OtherComponents()
}

// Close implements ConfigSource; the XML source holds no resources.
func (s *XMLSource) Close() error { return nil }

func (s *XMLSource) readConfiguration() (*xmlConfiguration, error) {
	var root xmlConfigRoot
	if err := readXML(filepath.Join(s.root, "Configuration.xml"), &root); err != nil {
		return nil, err
	}
	return &root.Configuration, nil
}

// ConfigurationInfo implements ConfigSource.
func (s *XMLSource) ConfigurationInfo(_ context.Context) (*ConfigurationInfo, error) {
	cfg, err := s.readConfiguration()
	if err != nil {
		return nil, err
	}
	p := cfg.Properties

	counts := make(map[string]int)
	for _, obj := range cfg.ChildObjects.Items {
		counts[obj.XMLName.Local]++
	}

	return &ConfigurationInfo{
		Name:             p.Name,
		Synonym:          p.Synonym.ru(),
		UUID:             cfg.UUID,
		Vendor:           p.Vendor,
		Version:          p.Version,
		ScriptVariant:    p.ScriptVariant,
		DefaultRunMode:   p.DefaultRunMode,
		IsExtension:      p.ConfigurationExtensionPurpose != "",
		ExtensionPurpose: p.ConfigurationExtensionPurpose,
		NamePrefix:       p.NamePrefix,
		ObjectCounts:     counts,
	}, nil
}

// MetadataTree implements ConfigSource.
func (s *XMLSource) MetadataTree(_ context.Context) (*MetadataTree, error) {
	cfg, err := s.readConfiguration()
	if err != nil {
		return nil, err
	}

	byType := make(map[string][]string)
	total := 0
	for _, obj := range cfg.ChildObjects.Items {
		byType[obj.XMLName.Local] = append(byType[obj.XMLName.Local], obj.Name)
		total++
	}

	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)

	groups := make([]MetadataGroup, 0, len(types))
	for _, t := range types {
		names := byType[t]
		sort.Strings(names)
		groups = append(groups, MetadataGroup{Type: t, Objects: names})
	}

	return &MetadataTree{
		Configuration: cfg.Properties.Name,
		TotalObjects:  total,
		Groups:        groups,
	}, nil
}

// folderForType returns the export subdirectory that holds objects of a type.
// Known kinds come from the shared dictionary domain.MetaKinds; an unknown kind
// keeps the naive type+"s" fallback this layer always had.
func folderForType(objectType string) string {
	if k, ok := domain.MetaKindByMType(objectType); ok {
		return k.DumpDir
	}
	return objectType + "s"
}

// explainMissing turns "file not found" into an answer about the export that
// was actually asked. A stale set_dump from another project otherwise reads as
// "this object does not exist", which sends the caller looking for the wrong
// problem: the object is fine, the active export is not the one they meant.
func (s *XMLSource) explainMissing(what string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	name := ""
	if cfg, cfgErr := s.readConfiguration(); cfgErr == nil {
		name = cfg.Properties.Name
	}
	if name != "" {
		return fmt.Errorf("%s не найден в активной выгрузке %s (конфигурация %q). Если это не тот проект: list_projects, затем set_dump", what, s.root, name)
	}
	return fmt.Errorf("%s не найден в активной выгрузке %s. Если это не тот проект: list_projects, затем set_dump", what, s.root)
}

// ObjectStructure implements ConfigSource.
func (s *XMLSource) ObjectStructure(_ context.Context, objectType, name string) (*ObjectStructure, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	path := filepath.Join(s.root, folderForType(objectType), name+".xml")
	var root xmlObjectRoot
	if err := readXML(path, &root); err != nil {
		return nil, s.explainMissing(objectType+"."+name, err)
	}
	obj := root.Object

	out := &ObjectStructure{
		Type:    obj.XMLName.Local,
		Name:    obj.Properties.Name,
		Synonym: obj.Properties.Synonym.ru(),
		UUID:    obj.UUID,
	}
	other := make(map[string][]string)

	for _, ch := range obj.ChildObjects.Items {
		switch ch.XMLName.Local {
		case "Attribute", "Dimension", "Resource":
			out.Attributes = append(out.Attributes, Field{Name: ch.name(), Kind: ch.XMLName.Local, Type: russifyTypes(ch.typeList())})
		case "TabularSection":
			ts := TabularSection{Name: ch.name()}
			if ch.ChildObjects != nil {
				for _, sub := range ch.ChildObjects.Items {
					if sub.XMLName.Local == "Attribute" {
						ts.Attributes = append(ts.Attributes, Field{Name: sub.name(), Kind: "Attribute", Type: russifyTypes(sub.typeList())})
					}
				}
			}
			out.TabularSections = append(out.TabularSections, ts)
		case "Form":
			out.Forms = append(out.Forms, ch.name())
		case "Command":
			out.Commands = append(out.Commands, ch.name())
		default:
			t := ch.XMLName.Local
			other[t] = append(other[t], ch.name())
		}
	}
	if len(other) > 0 {
		out.Other = other
	}
	return out, nil
}

// FormStructure implements ConfigSource.
func (s *XMLSource) FormStructure(_ context.Context, ownerType, ownerName, formName string) (*FormStructure, error) {
	path, owner, display, err := s.formPath(ownerType, ownerName, formName)
	if err != nil {
		return nil, err
	}
	var form xmlForm
	if err := readXML(path, &form); err != nil {
		return nil, s.explainMissing(strings.TrimSpace(owner+"."+display), err)
	}

	out := &FormStructure{Name: display, Owner: owner}

	for _, e := range form.Events.Events {
		out.Handlers = append(out.Handlers, FormHandler{Source: "Form", Event: e.Name, Handler: strings.TrimSpace(e.Handler)})
	}

	for _, a := range form.Attributes.Attributes {
		fa := FormAttribute{Name: a.Name, Type: russifyTypes(a.Type.Types), Main: a.Main}
		for _, c := range a.Columns.Columns {
			fa.Columns = append(fa.Columns, Field{Name: c.Name, Type: russifyTypes(c.Type.Types)})
		}
		out.Attributes = append(out.Attributes, fa)
	}

	walkItems(&form.ChildItems, out)

	for _, c := range form.Commands.Commands {
		out.Commands = append(out.Commands, FormCommand{Name: c.Name, Action: strings.TrimSpace(c.Action)})
	}

	return out, nil
}

// walkItems flattens the form item tree in layout order, collecting items and
// their event handlers.
func walkItems(items *xmlItems, out *FormStructure) {
	if items == nil {
		return
	}
	for _, it := range items.Items {
		out.Items = append(out.Items, FormItem{
			Name:     it.Name,
			Kind:     it.XMLName.Local,
			DataPath: strings.TrimSpace(it.DataPath),
			Command:  strings.TrimSpace(it.CommandName),
		})
		for _, e := range it.Events.Events {
			out.Handlers = append(out.Handlers, FormHandler{Source: it.Name, Event: e.Name, Handler: strings.TrimSpace(e.Handler)})
		}
		walkItems(it.ChildItems, out)
	}
}

// formPath resolves the Form.xml path, owner label and display name for a form.
func (s *XMLSource) formPath(ownerType, ownerName, formName string) (path, owner, display string, err error) {
	if ownerType == "" || ownerName == "" {
		return "", "", "", fmt.Errorf("owner type and name are required")
	}
	if ownerType == "CommonForm" {
		return filepath.Join(s.root, "CommonForms", ownerName, "Ext", "Form.xml"), "CommonForm", ownerName, nil
	}
	if formName == "" {
		return "", "", "", fmt.Errorf("form name is required for %s.%s", ownerType, ownerName)
	}
	return filepath.Join(s.root, folderForType(ownerType), ownerName, "Forms", formName, "Ext", "Form.xml"),
		ownerType + "." + ownerName, formName, nil
}

// SearchCode implements ConfigSource by scanning every .bsl module under the root
// and under OtherComponents. The other roots go first: they hold the project's own
// code, and in a standard configuration vendor modules would fill the limit
// before an extension is reached.
func (s *XMLSource) SearchCode(ctx context.Context, params SearchParams) (*SearchResult, error) {
	if strings.TrimSpace(params.Query) == "" {
		return nil, fmt.Errorf("empty search query")
	}
	limit := params.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}

	matcher, err := newMatcher(params)
	if err != nil {
		return nil, err
	}

	result := &SearchResult{Query: params.Query, Scope: params.Scope, Matches: []SearchMatch{}}
	scope := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(params.Scope), "\\", "/"))
	total := 0

	roots := append(s.otherComponents(), ComponentRoot{Dir: s.root})

	var walkErr error
	for _, root := range roots {
		walkErr = s.searchRoot(ctx, root, scope, matcher, limit, params.Total, result, &total)
		if walkErr != nil {
			break
		}
	}
	if walkErr != nil && walkErr != errStopWalk {
		return nil, walkErr
	}
	result.Shown = len(result.Matches)
	if params.Total {
		result.TotalMatches = total
		if result.Truncated {
			result.Note = fmt.Sprintf("показано %d из %d совпадений — сузьте scope или поднимите maxResults", result.Shown, total)
		}
	} else if result.Truncated {
		result.Note = fmt.Sprintf("показаны первые %d совпадений, поиск остановлен на лимите; точное число — total=true, меньше шума — scope=<путь>", result.Shown)
	}
	return result, nil
}

// searchRoot scans one export root into result. A named root is a component
// next to the main export: its matches carry the name, and scope sees its
// modules as <name>/<path>.
func (s *XMLSource) searchRoot(ctx context.Context, root ComponentRoot, scope string, matcher func(string) bool, limit int, countAll bool, result *SearchResult, total *int) error {
	return filepath.WalkDir(root.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".bsl") {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		rel, _ := filepath.Rel(root.Dir, path)
		rel = filepath.ToSlash(rel)
		scoped := rel
		if root.Name != "" {
			scoped = root.Name + "/" + rel
		}
		if scope != "" && !strings.Contains(strings.ToLower(scoped), scope) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		text := string(stripBOM(data))
		procedure := ""
		for i, line := range strings.Split(text, "\n") {
			line = strings.TrimRight(line, "\r")
			if name := declaredRoutine(line); name != "" {
				procedure = name
			}
			if matcher(line) {
				*total++
				if len(result.Matches) < limit {
					result.Matches = append(result.Matches, SearchMatch{
						File: rel, Line: i + 1, Text: line, Procedure: procedure, Component: root.Name,
					})
					continue
				}
				result.Truncated = true
				if !countAll {
					// Without total=true the walk stops at the limit, so the
					// count would be a lie; say so instead of implying a number.
					return errStopWalk
				}
			}
		}
		return nil
	})
}

// routineDecl matches a Процедура/Функция declaration, including an Экспорт
// suffix and the English keywords.
var routineDecl = regexp.MustCompile(`(?i)^\s*(?:Процедура|Функция|Procedure|Function)\s+([\p{L}\p{N}_]+)`)

// declaredRoutine returns the routine name declared on this line, if any.
func declaredRoutine(line string) string {
	m := routineDecl.FindStringSubmatch(line)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// errStopWalk stops WalkDir once the result limit is reached.
var errStopWalk = fmt.Errorf("stop walk")

// newMatcher builds a per-line predicate for the given search parameters.
func newMatcher(params SearchParams) (func(string) bool, error) {
	if params.Regex {
		expr := params.Query
		if params.IgnoreCase {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %w", err)
		}
		return re.MatchString, nil
	}

	if params.IgnoreCase {
		needle := strings.ToLower(params.Query)
		return func(line string) bool {
			return strings.Contains(strings.ToLower(line), needle)
		}, nil
	}
	needle := params.Query
	return func(line string) bool {
		return strings.Contains(line, needle)
	}, nil
}
