package source

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ExtensionContext describes a configuration extension (.cfe export): its own
// vs adopted (borrowed) objects and every method interceptor, optionally paired
// with the original method text from the base configuration export.
type ExtensionContext struct {
	Extension    string        `json:"extension"`
	Prefix       string        `json:"prefix,omitempty" jsonschema:"NamePrefix of the extension"`
	Purpose      string        `json:"purpose,omitempty"`
	Own          []string      `json:"own,omitempty" jsonschema:"objects created by the extension"`
	Adopted      []string      `json:"adopted,omitempty" jsonschema:"objects borrowed from the base configuration"`
	Interceptors []Interceptor `json:"interceptors" jsonschema:"method interceptors found in the extension modules"`
}

// Interceptor is one &Перед/&После/&Вместо/&ИзменениеИКонтроль handler.
type Interceptor struct {
	Module   string `json:"module" jsonschema:"module path relative to the extension export root"`
	Kind     string `json:"kind" jsonschema:"Перед, После, Вместо or ИзменениеИКонтроль"`
	Target   string `json:"target" jsonschema:"intercepted method name"`
	Method   string `json:"method" jsonschema:"interceptor procedure/function name"`
	Original string `json:"original,omitempty" jsonschema:"original method text from the base configuration (when baseDump is given and the method is found)"`
}

// interceptorKinds are the annotations of an extension method interceptor.
var interceptorKinds = []string{"Перед", "После", "Вместо", "ИзменениеИКонтроль"}

// canonicalKind normalizes an annotation keyword to its canonical Russian form.
var canonicalKind = func() map[string]string {
	out := map[string]string{}
	for _, kind := range interceptorKinds {
		for _, word := range bilingual(kind) {
			out[strings.ToLower(word)] = kind
		}
	}
	return out
}()

// ExtensionContext analyses the export at s.root as an extension. baseDump, when
// non-empty, is the base configuration export used to resolve the original text
// of intercepted methods (the module's relative path is identical in both).
func (s *XMLSource) ExtensionContext(_ context.Context, baseDump string) (*ExtensionContext, error) {
	cfg, err := s.readConfiguration()
	if err != nil {
		return nil, err
	}
	if cfg.Properties.ConfigurationExtensionPurpose == "" {
		return nil, fmt.Errorf("выгрузка %q не является расширением (нет ConfigurationExtensionPurpose)", cfg.Properties.Name)
	}

	out := &ExtensionContext{
		Extension:    cfg.Properties.Name,
		Prefix:       cfg.Properties.NamePrefix,
		Purpose:      cfg.Properties.ConfigurationExtensionPurpose,
		Interceptors: []Interceptor{},
	}

	// Own vs adopted: read each child object's XML and check ObjectBelonging.
	for _, obj := range cfg.ChildObjects.Items {
		if obj.XMLName.Local == "Language" {
			continue
		}
		label := metadataLabel(obj.XMLName.Local) + "." + obj.Name
		var root xmlObjectRoot
		if err := readXML(filepath.Join(s.root, folderForType(obj.XMLName.Local), obj.Name+".xml"), &root); err != nil {
			out.Own = append(out.Own, label) // unreadable/typeless: assume own
			continue
		}
		if root.Object.Properties.ObjectBelonging == "Adopted" {
			out.Adopted = append(out.Adopted, label)
		} else {
			out.Own = append(out.Own, label)
		}
	}

	// Interceptors: scan every .bsl module of the extension. A base module
	// intercepted several times is parsed once.
	bases := moduleCache{}
	walkErr := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".bsl") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.root, path)
		rel = filepath.ToSlash(rel)
		for _, ic := range parseInterceptors(stripBOM(data)) {
			ic.Module = rel
			if baseDump != "" {
				ic.Original = originalMethod(bases, filepath.Join(baseDump, filepath.FromSlash(rel)), ic.Target)
			}
			out.Interceptors = append(out.Interceptors, ic)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// parseInterceptors finds annotation+method pairs in a module. The parser
// reads every annotation of a method, wherever it stands: on its own line,
// after a directive, or on the line of the declaration itself.
func parseInterceptors(src []byte) []Interceptor {
	var out []Interceptor
	for _, m := range parseDeclarations(src).Methods {
		for _, a := range m.Annotations {
			kind, ok := canonicalKind[strings.ToLower(strings.TrimPrefix(a.Name, "&"))]
			if !ok || a.Arg == "" {
				continue // not an interceptor, or its target is not a name
			}
			out = append(out, Interceptor{Kind: kind, Target: a.Arg, Method: m.Name})
		}
	}
	return out
}

// originalMethod returns the text of method name from the module at path,
// whole lines from its directives (&НаСервере, ...) to its closing keyword. The
// parser finds the method, so a wrapped declaration or English keywords are
// read as any other. Returns "" when the file or the method is missing. mods
// keeps the modules parsed during the call.
func originalMethod(mods moduleCache, path, name string) string {
	mod := mods.module(path)
	m, ok := methodIn(mod, name)
	if !ok {
		return ""
	}
	return methodSource(mod, m)
}
