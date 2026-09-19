package source

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// reAnnotation matches an interceptor annotation line. BSL keywords are
// case-insensitive; English synonyms are legal even in Russian configurations.
var reAnnotation = regexp.MustCompile(`(?i)^\s*&(` + wordAlt(interceptorKinds...) + `)\("([^"]+)"\)`)

// reMethodHead matches a procedure/function header line and captures the name.
var reMethodHead = regexp.MustCompile(`(?i)^\s*(?:Асинх\s+|Async\s+)?(Процедура|Функция|Procedure|Function)\s+([\p{L}\d_]+)`)

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

	// Interceptors: scan every .bsl module of the extension.
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
				ic.Original = originalMethod(filepath.Join(baseDump, filepath.FromSlash(rel)), ic.Target)
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

// originalMethod extracts the text of method name from the module at path,
// including immediately preceding directive lines (&НаСервере, ...). Returns ""
// when the file or method is missing. Method names are case-insensitive in 1C.
func originalMethod(path, name string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(stripBOM(data)), "\n")
	start := -1
	for i, line := range lines {
		if h := reMethodHead.FindStringSubmatch(line); h != nil && strings.EqualFold(h[2], name) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	// Include preceding directive lines.
	first := start
	for first > 0 {
		prev := strings.TrimSpace(lines[first-1])
		if strings.HasPrefix(prev, "&") {
			first--
			continue
		}
		break
	}
	for i := start; i < len(lines); i++ {
		if isMethodEnd(lines[i]) {
			return strings.Join(lines[first:i+1], "\n")
		}
	}
	return strings.Join(lines[first:], "\n")
}

// isMethodEnd reports whether the line ends a method: КонецПроцедуры /
// КонецФункции (or English), optionally followed by ";" or a trailing comment.
func isMethodEnd(line string) bool {
	up := strings.ToUpper(strings.TrimSpace(strings.TrimRight(line, "\r")))
	for _, kw := range []string{"КОНЕЦПРОЦЕДУРЫ", "КОНЕЦФУНКЦИИ", "ENDPROCEDURE", "ENDFUNCTION"} {
		if rest, ok := strings.CutPrefix(up, kw); ok {
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ";"))
			if rest == "" || strings.HasPrefix(rest, "//") {
				return true
			}
		}
	}
	return false
}
