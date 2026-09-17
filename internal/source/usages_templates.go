package source

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TemplateUsage is one template that mentions the object: a data composition
// schema of a report, a registration-rules template of an exchange plan, or any
// other template stored as text.
type TemplateUsage struct {
	Owner    string `json:"owner" jsonschema:"owning object, e.g. Отчет.ABCXYZАнализНоменклатуры"`
	Template string `json:"template"`
	Kind     string `json:"kind" jsonschema:"схема компоновки данных, правила регистрации or макет"`
	Hits     int    `json:"hits" jsonschema:"how many times the object is mentioned"`
}

// reSchemaRoot recognises a data composition schema by its root element.
var reSchemaRoot = regexp.MustCompile(`(?i)<DataCompositionSchema|<Схема[Кк]омпоновки`)

// templateUsages finds the templates that mention the object. A data composition
// schema keeps its query as text, so a report built on the object shows up here
// and nowhere else: the schema is the single most common place a change to an
// attribute breaks something, and it was invisible to the type and rights scans.
func (s *XMLSource) templateUsages(ctx context.Context, objectType, name string) []TemplateUsage {
	// The forms an object is referred to by inside a query or a rules template.
	label := metadataLabel(objectType)
	needles := []string{
		label + "." + name, // Справочник.Номенклатура
		metadataKinds[objectType] + "Ссылка." + name, // СправочникСсылка.Номенклатура
		objectType + "." + name,                      // Catalog.Номенклатура
	}
	var out []TemplateUsage
	roots := []string{"Reports", "DataProcessors", "ExchangePlans", "Catalogs", "Documents", "CommonTemplates"}
	for _, root := range roots {
		dir := filepath.Join(s.root, root)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		owners := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				owners = append(owners, e.Name())
			}
		}
		sort.Strings(owners)
		for _, owner := range owners {
			if ctx.Err() != nil {
				return out
			}
			out = append(out, s.scanTemplatesOf(dir, root, owner, needles)...)
		}
	}
	return out
}

// scanTemplatesOf reads the templates of one owner and reports the matches.
func (s *XMLSource) scanTemplatesOf(dir, root, owner string, needles []string) []TemplateUsage {
	base := filepath.Join(dir, owner, "Templates")
	// CommonTemplates keep the template body one level up: the directory itself
	// is the template.
	if root == "CommonTemplates" {
		return matchTemplate(filepath.Join(dir, owner, "Ext"), "ОбщийМакет", owner, needles)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	ownerLabel := metadataLabel(strings.TrimSuffix(root, "s")) + "." + owner
	if root == "Reports" {
		ownerLabel = "Отчет." + owner
	}
	var out []TemplateUsage
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, tpl := range names {
		out = append(out, matchTemplate(filepath.Join(base, tpl, "Ext"), ownerLabel, tpl, needles)...)
	}
	return out
}

// matchTemplate reads the template body in extDir and reports a usage when the
// object is mentioned. Only text bodies are read: a table document (.mxl) is
// binary and is skipped rather than guessed at.
func matchTemplate(extDir, owner, template string, needles []string) []TemplateUsage {
	for _, file := range []string{"Template.xml", "Template.txt"} {
		data, err := os.ReadFile(filepath.Join(extDir, file))
		if err != nil {
			continue
		}
		text := string(stripBOM(data))
		hits := 0
		for _, n := range needles {
			hits += strings.Count(text, n)
		}
		if hits == 0 {
			continue
		}
		kind := "макет"
		switch {
		case reSchemaRoot.MatchString(text):
			kind = "схема компоновки данных"
		case strings.Contains(text, "<ПравилаРегистрации>"):
			kind = "правила регистрации"
		}
		return []TemplateUsage{{Owner: owner, Template: template, Kind: kind, Hits: hits}}
	}
	return nil
}
