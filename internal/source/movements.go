package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MovementsReport describes which registers a document posts to: the declared
// RegisterRecords metadata cross-checked with what the object module actually
// does (Движения.X usage, Записывать flag, fields assigned on the record).
type MovementsReport struct {
	Document  string             `json:"document"`
	Registers []RegisterMovement `json:"registers"`
}

// RegisterMovement is one register the document touches.
type RegisterMovement struct {
	Register   string   `json:"register" jsonschema:"e.g. РегистрНакопления.ТоварыНаСкладах"`
	Declared   bool     `json:"declared" jsonschema:"listed in the document's RegisterRecords metadata"`
	UsedInCode bool     `json:"usedInCode" jsonschema:"Движения.<X> appears in the object module"`
	WriteFlag  bool     `json:"writeFlag" jsonschema:"Движения.<X>.Записывать = Истина found"`
	FieldsSet  []string `json:"fieldsSet,omitempty" jsonschema:"record fields assigned in code"`
}

// Record-field assignments stay line-based: the parser reports register
// accesses, not assignments. Case-insensitive (BSL is), Cyrillic needs \p{L}.
var (
	reMovAssign = regexp.MustCompile(`^\s*([\p{L}\d_]+)\.([\p{L}\d_]+)\s*=[^=]`)
	reAnyAssign = regexp.MustCompile(`^\s*([\p{L}\d_]+)\s*=[^=]`)
)

// movCollectionMethods are methods of the record-set collection (НаборыДвижений)
// that must not be mistaken for register names.
var movCollectionMethods = map[string]bool{
	"записать": true, "найти": true, "получить": true, "количество": true, "индекс": true,
	"write": true, "find": true, "get": true, "count": true, "indexof": true,
}

// regInfo accumulates per-register facts.
type regInfo struct {
	full   string
	decl   bool
	used   bool
	write  bool
	fields map[string]bool
}

// Movements builds the report for one document.
func (s *XMLSource) Movements(_ context.Context, name string) (*MovementsReport, error) {
	if name == "" {
		return nil, fmt.Errorf("document name is required")
	}
	var root xmlObjectRoot
	if err := readXML(filepath.Join(s.root, "Documents", name+".xml"), &root); err != nil {
		return nil, err
	}
	out := &MovementsReport{Document: "Документ." + name, Registers: []RegisterMovement{}}

	regs := map[string]*regInfo{}    // key: lower(full name)
	shortIdx := map[string][]*regInfo{} // key: lower(short name)
	ensure := func(full string) *regInfo {
		k := strings.ToLower(full)
		if r, ok := regs[k]; ok {
			return r
		}
		r := &regInfo{full: full, fields: map[string]bool{}}
		regs[k] = r
		return r
	}
	// byShort resolves a short register name from code to the declared registers
	// with that name (all of them, if several kinds share it), or registers a
	// standalone entry when nothing was declared.
	byShort := func(short string) []*regInfo {
		k := strings.ToLower(short)
		if list, ok := shortIdx[k]; ok && len(list) > 0 {
			return list
		}
		r := ensure(short)
		shortIdx[k] = []*regInfo{r}
		return shortIdx[k]
	}

	// Declared movements from metadata ("AccumulationRegister.X" -> russified).
	for _, item := range root.Object.Properties.RegisterRecords.Items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		full, short := item, item
		if typ, nm, ok := strings.Cut(item, "."); ok {
			short = nm
			full = metadataLabel(typ) + "." + nm
		}
		r := ensure(full)
		r.decl = true
		k := strings.ToLower(short)
		shortIdx[k] = append(shortIdx[k], r)
	}

	// Code facts from the object module (optional). Which registers are used
	// and flagged comes from the parser, in both spellings (Движения and
	// RegisterRecords), with comments and query texts already out of the way.
	// Record fields are then collected by a line-by-line pass with
	// variable-to-register bindings, so fields do not leak between registers
	// when one variable is reused, and commented-out code is ignored.
	if data, err := os.ReadFile(filepath.Join(s.root, "Documents", name, "Ext", "ObjectModule.bsl")); err == nil {
		text := string(stripBOM(data))
		bindAt := map[int]movementUse{} // line -> "Запись = Движения.X.Добавить()"
		for _, u := range movementUses(parseModule(text)) {
			if u.isCollectionMethod() {
				continue
			}
			for _, r := range byShort(u.register) {
				r.used = true
				if u.setsWriteFlag() {
					r.write = true
				}
			}
			if u.boundVar != "" {
				bindAt[u.line] = u
			}
		}

		binding := map[string]string{} // lower(var) -> short register name
		for i, raw := range strings.Split(text, "\n") {
			line := stripLineComment(strings.TrimRight(raw, "\r"))

			if u, ok := bindAt[i+1]; ok {
				binding[strings.ToLower(u.boundVar)] = u.register
			} else if m := reAnyAssign.FindStringSubmatch(line); m != nil {
				delete(binding, strings.ToLower(m[1])) // variable rebound elsewhere
			}
			if m := reMovAssign.FindStringSubmatch(line); m != nil {
				if short, ok := binding[strings.ToLower(m[1])]; ok {
					for _, r := range byShort(short) {
						r.fields[m[2]] = true
					}
				}
			}
		}
	}

	keys := make([]string, 0, len(regs))
	for k := range regs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := regs[k]
		fields := make([]string, 0, len(r.fields))
		for f := range r.fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		out.Registers = append(out.Registers, RegisterMovement{
			Register: r.full, Declared: r.decl, UsedInCode: r.used, WriteFlag: r.write, FieldsSet: fields,
		})
	}
	return out, nil
}

// stripLineComment cuts a trailing //-comment that is outside string literals.
// BSL escapes a quote inside a string by doubling it, which this simple toggle
// handles correctly for the purpose of finding an unquoted "//".
func stripLineComment(line string) string {
	inString := false
	for i := 0; i+1 < len(line); i++ {
		switch line[i] {
		case '"':
			inString = !inString
		case '/':
			if !inString && line[i+1] == '/' {
				return line[:i]
			}
		}
	}
	return line
}
