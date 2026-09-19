package source

import (
	"context"
	"os"
	"path/filepath"
)

// ContextPack aggregates everything needed to work on one object in a single
// call: its structure, the exported interface of its object/manager modules, and
// where it is used. It composes ObjectStructure and MetadataUsages so a client
// gets a coherent slice in one round-trip instead of five to eight.
type ContextPack struct {
	Object      string            `json:"object" jsonschema:"e.g. Справочник.Товары"`
	Structure   *ObjectStructure  `json:"structure"`
	Modules     []ModuleInterface `json:"modules,omitempty"`
	Usages      *UsageReport      `json:"usages,omitempty"`
	QuerySchema *QuerySchema      `json:"querySchema,omitempty" jsonschema:"query-language view: table name, exact field names, virtual tables"`
	Forms       []FormBrief       `json:"forms,omitempty" jsonschema:"one summary per form: how much is on it and which handlers it has"`
}

// ContextPackOptions turns on the parts that are off by default. Both cost extra
// reads (the query schema re-reads the object; the form summaries read every form
// file, and forms of a real configuration are large), so they are asked for only
// when the task actually needs them.
type ContextPackOptions struct {
	QuerySchema bool // the object as the query language sees it
	Forms       bool // a summary of every form of the object
}

// FormBrief summarises one form without dumping its whole structure: how much is
// on it and which module procedures its events call. That is enough to pick the
// form worth opening with get_form_structure and to see where the logic lives.
type FormBrief struct {
	Name          string   `json:"name"`
	Attributes    int      `json:"attributes"`
	Items         int      `json:"items"`
	Commands      int      `json:"commands"`
	Handlers      []string `json:"handlers,omitempty" jsonschema:"module procedures bound to form or item events"`
	HandlersTotal int      `json:"handlersTotal,omitempty" jsonschema:"number of distinct handlers; the ones above the cap are not listed"`
	Missing       bool     `json:"missing,omitempty" jsonschema:"the form is declared in metadata but absent from the export"`
}

// maxBriefHandlers caps the handler list of one form. A form of a real
// configuration can carry a hundred; the total is still reported, so the cap
// never reads as "that is all there is".
const maxBriefHandlers = 20

// ModuleInterface is the exported interface of one module.
type ModuleInterface struct {
	Kind    string   `json:"kind" jsonschema:"ObjectModule or ManagerModule"`
	Path    string   `json:"path" jsonschema:"module path relative to the export root"`
	Exports []string `json:"exports" jsonschema:"exported procedure/function headers"`
}

// ContextPack builds the aggregate for one object.
func (s *XMLSource) ContextPack(ctx context.Context, objectType, name string, opts ContextPackOptions) (*ContextPack, error) {
	structure, err := s.ObjectStructure(ctx, objectType, name)
	if err != nil {
		return nil, err
	}

	prefix := queryPrefix[objectType]
	if prefix == "" {
		prefix = objectType
	}
	pack := &ContextPack{Object: prefix + "." + name, Structure: structure}

	for _, m := range []struct{ kind, file string }{
		{"ObjectModule", "ObjectModule.bsl"},
		{"ManagerModule", "ManagerModule.bsl"},
	} {
		rel := filepath.Join(folderForType(objectType), name, "Ext", m.file)
		data, err := os.ReadFile(filepath.Join(s.root, rel))
		if err != nil {
			continue // module not present
		}
		pack.Modules = append(pack.Modules, ModuleInterface{
			Kind:    m.kind,
			Path:    filepath.ToSlash(rel),
			Exports: extractExports(stripBOM(data)),
		})
	}

	if usages, err := s.MetadataUsages(ctx, objectType, name, false); err == nil {
		pack.Usages = usages
	}

	if opts.QuerySchema {
		if schema, err := s.QuerySchema(ctx, objectType, name); err == nil {
			pack.QuerySchema = schema
		}
	}
	if opts.Forms {
		for _, form := range structure.Forms {
			pack.Forms = append(pack.Forms, s.formBrief(ctx, objectType, name, form))
		}
	}
	return pack, nil
}

// formBrief summarises one form. A form listed in the object's metadata but
// missing from the export is reported as such rather than skipped: the gap
// itself is worth seeing (partial export, form of an extension, and so on).
func (s *XMLSource) formBrief(ctx context.Context, objectType, name, form string) FormBrief {
	structure, err := s.FormStructure(ctx, objectType, name, form)
	if err != nil {
		return FormBrief{Name: form, Missing: true}
	}

	brief := FormBrief{
		Name:       form,
		Attributes: len(structure.Attributes),
		Items:      len(structure.Items),
		Commands:   len(structure.Commands),
	}
	seen := map[string]bool{}
	for _, h := range structure.Handlers {
		if h.Handler == "" || seen[h.Handler] {
			continue
		}
		seen[h.Handler] = true
		brief.HandlersTotal++
		if len(brief.Handlers) < maxBriefHandlers {
			brief.Handlers = append(brief.Handlers, h.Handler)
		}
	}
	return brief
}

// extractExports returns the headers of exported procedures/functions of a BSL
// module, e.g. "Процедура НайтиКонтрагента(ИНН)". The declarations come from the
// parser, so a header wrapped over several lines, a comment inside it and the
// English spelling (Procedure ... Export) read the same as a Russian one-liner.
func extractExports(src []byte) []string {
	mod := parseDeclarations(src)
	var out []string
	for _, m := range mod.Methods {
		if m.Export {
			out = append(out, declarationOf(mod, m).header())
		}
	}
	return out
}
