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

// FormImpact answers "who else changes this form programmatically, and what
// breaks if I add my code" across the base configuration and every extension.
// Programmatic form changes from several sources silently interfere: attribute
// collections get rebuilt, element names collide, actions get overridden.
type FormImpact struct {
	Form      string         `json:"form" jsonschema:"e.g. Документ.ЗаказКлиента.ФормаДокумента"`
	Sources   []FormModifier `json:"sources" jsonschema:"modules changing the form, in execution order (base first, then extensions)"`
	Conflicts []FormConflict `json:"conflicts"`
	Guidance  []string       `json:"guidance" jsonschema:"rules for safely adding your own code to THIS form, derived from what is already there"`
}

// DraftOptions describes code that is about to be written but is not in any
// export yet. It is analysed as an extra source, so its conflicts with the real
// ones are found before the code is applied.
type DraftOptions struct {
	Code      string `json:"code" jsonschema:"BSL of the form module you are about to write (interceptors and all)"`
	Extension string `json:"extension,omitempty" jsonschema:"name of your extension; defaults to a placeholder"`
	Prefix    string `json:"prefix,omitempty" jsonschema:"NamePrefix of your extension, to check element naming"`
}

// FormModifier is one module that changes the form programmatically.
type FormModifier struct {
	Dump        string     `json:"dump" jsonschema:"export root this module came from"`
	Extension   string     `json:"extension,omitempty" jsonschema:"extension name; empty for the base configuration"`
	Prefix      string     `json:"prefix,omitempty" jsonschema:"extension NamePrefix"`
	Module      string     `json:"module" jsonschema:"module path relative to its export root"`
	Interceptor string     `json:"interceptor,omitempty" jsonschema:"annotation, e.g. После(ПриСозданииНаСервере)"`
	Kind        string     `json:"kind,omitempty" jsonschema:"interceptor kind: Перед, После, Вместо, ИзменениеИКонтроль"`
	Target      string     `json:"target,omitempty" jsonschema:"intercepted base method"`
	Method      string     `json:"method,omitempty" jsonschema:"interceptor procedure name"`
	HasContinue bool       `json:"hasContinue,omitempty" jsonschema:"ПродолжитьВызов found in an Вместо interceptor (the base method still runs)"`
	Changes     []FormEdit `json:"changes"`
}

// FormEdit is one programmatic change of the form.
type FormEdit struct {
	Kind   string `json:"kind" jsonschema:"ИзменитьРеквизиты, ИзменитьРеквизитыСУдалением, ДобавитьРеквизит, ДобавитьКолонку, ДобавитьЭлемент, ДобавитьКоманду, УдалитьЭлемент, ПереместитьЭлемент, СменитьРодителя, УстановитьДействие, БСП_ПодключаемыеКоманды, БСП_Печать"`
	Target string `json:"target,omitempty" jsonschema:"element/attribute name when known"`
	Line   int    `json:"line"`
	Text   string `json:"text" jsonschema:"source line"`
}

// FormConflict is one predicted interference between sources.
type FormConflict struct {
	Code       string   `json:"code"`
	Severity   string   `json:"severity" jsonschema:"high, medium or low"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion"`
	Involved   []string `json:"involved" jsonschema:"modules involved"`
}

// Form-change regexes. BSL is case-insensitive; Cyrillic needs \p{L}.
// b is the left word boundary: \b is ASCII-only in RE2 and never matches next
// to Cyrillic, so every pattern guards its left side explicitly.
const b = `(?:^|[^\p{L}\d_])`

var (
	reChangeAttrs = regexp.MustCompile(`(?i)` + b + `ИзменитьРеквизиты\s*\(([^)]*)`)
	reElemAdd     = regexp.MustCompile(`(?i)` + b + `Элементы\.(?:Добавить|Вставить)\s*\(\s*"([^"]+)"`)
	reElemDelete  = regexp.MustCompile(`(?i)` + b + `Элементы\.Удалить\s*\(\s*([\p{L}\d_."]+)`)
	reElemMove    = regexp.MustCompile(`(?i)` + b + `Элементы\.Переместить\s*\(\s*([\p{L}\d_."]+)`)
	// Assignment only: BSL uses "=" for comparison too, so require the statement
	// to start with the target (Если X.Родитель = Y Тогда is not a change).
	reSetParent    = regexp.MustCompile(`(?i)^\s*([\p{L}\d_.]+)\.Родитель\s*=[^=]`)
	reSetAction    = regexp.MustCompile(`(?i)` + b + `([\p{L}\d_."\[\]]+)\.УстановитьДействие\s*\(\s*"([^"]+)"`)
	rePluggableCmd = regexp.MustCompile(`(?i)` + b + `ПодключаемыеКоманды\s*\.`)
	rePrintMgmt    = regexp.MustCompile(`(?i)` + b + `(УправлениеПечатью|ПечатьОбъектов)\s*\.`)
	// Only the constructor head is matched: the argument list is read with
	// balanced parentheses, because the second argument is itself a call
	// (Новый ОписаниеТипов(...)) and a regex would cut it at the wrong comma.
	reAddedAttr    = regexp.MustCompile(`(?i)` + b + `Новый\s+РеквизитФормы\s*\(`)
	reCommandAdd   = regexp.MustCompile(`(?i)` + b + `Команды\.Добавить\s*\(\s*"([^"]+)"`)
	reContinueCall = regexp.MustCompile(`(?i)` + b + `ПродолжитьВызов\s*\(`)
	reProcCall     = regexp.MustCompile(`(?i)` + b + `([\p{L}\d_]+)\s*\(`)
)

// FormImpact builds the report. dumps are export roots to scan (base
// configuration and extensions); when empty, only s.root is used. ownerType and
// ownerName identify the object, form its form name; for a common form pass
// ownerType "CommonForm" and the form name as ownerName.
func (s *XMLSource) FormImpact(_ context.Context, ownerType, ownerName, form string, dumps []string, draft *DraftOptions) (*FormImpact, error) {
	if ownerType == "" || ownerName == "" {
		return nil, fmt.Errorf("owner type and name are required")
	}
	rel, label, err := formModulePath(ownerType, ownerName, form)
	if err != nil {
		return nil, err
	}
	roots := normalizeDumps(dumps)
	if len(roots) == 0 {
		roots = normalizeDumps([]string{s.root})
	}

	out := &FormImpact{Form: label, Sources: []FormModifier{}, Conflicts: []FormConflict{}}
	// notices are findings about the request itself rather than about the form;
	// they are appended after the conflict rules have run.
	var notices []FormConflict
	var unreadable []string
	scanned := 0
	for _, root := range roots {
		// A path that is not a readable directory was not scanned at all: an
		// empty result for it must not look like "the form is clean".
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			unreadable = append(unreadable, root)
			continue
		}
		scanned++
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue // this export does not touch the form
		}
		module := string(stripBOM(data))
		ext, prefix := extensionInfo(root)
		for _, mod := range buildModifiers(module, root, ext, prefix, rel) {
			if len(mod.Changes) > 0 || mod.Kind != "" {
				out.Sources = append(out.Sources, mod)
			}
		}
	}
	if scanned == 0 {
		notices = append(notices, FormConflict{
			Code:     "NoSourcesScanned",
			Severity: "high",
			Message: fmt.Sprintf("Ни один из переданных путей не прочитан — каталога нет: %s. Пустой результат означает, что сканировать было нечего, а не что форма чистая.",
				strings.Join(unreadable, ", ")),
			Suggestion: "Проверить dumps: нужен существующий каталог выгрузки конфигурации или расширения.",
		})
	}
	// Base configuration first, then extensions: that is the execution order.
	sort.SliceStable(out.Sources, func(i, j int) bool {
		return out.Sources[i].Extension == "" && out.Sources[j].Extension != ""
	})

	// The draft joins as one more source, under its own synthetic dump so that
	// it is never confused with a real export.
	if draft != nil && strings.TrimSpace(draft.Code) != "" {
		name := draft.Extension
		if name == "" {
			name = draftExtensionName
		}
		added := 0
		for _, mod := range buildModifiers(draft.Code, draftDump, name, draft.Prefix, draftModule) {
			if len(mod.Changes) > 0 || mod.Kind != "" {
				out.Sources = append(out.Sources, mod)
				added++
			}
		}
		// Draft edits are charged to the interceptor that reaches them, so a
		// draft without an annotation yields no source at all. Silently dropping
		// it would answer as if the draft did not exist.
		if added == 0 {
			notices = append(notices, FormConflict{
				Code:     "DraftNotAnalysed",
				Severity: "medium",
				Message:  "В draftCode не найден перехватчик (&Перед/&После/&Вместо) — черновик не проанализирован и в sources не попал. Ниже описаны только существующие источники.",
				Suggestion: `Поставить над процедурой черновика аннотацию перехвата, например &После("ПриСозданииНаСервере").`,
			})
		}
	}

	out.Conflicts = append(detectFormConflicts(out.Sources), notices...)
	out.Guidance = formGuidance(out.Sources, draft, scanned > 0)
	return out, nil
}

// Synthetic identifiers for draft code: it has no export root or module path.
const (
	draftDump          = "<черновик>"
	draftModule        = "<черновик>"
	draftExtensionName = "ваш черновик"
)

// formModulePath returns the form module path relative to an export root and a
// human label for the form.
func formModulePath(ownerType, ownerName, form string) (rel, label string, err error) {
	if ownerType == "CommonForm" {
		return filepath.ToSlash(filepath.Join("CommonForms", ownerName, "Ext", "Form", "Module.bsl")),
			"ОбщаяФорма." + ownerName, nil
	}
	if form == "" {
		return "", "", fmt.Errorf("form name is required for %s.%s", ownerType, ownerName)
	}
	return filepath.ToSlash(filepath.Join(folderForType(ownerType), ownerName, "Forms", form, "Ext", "Form", "Module.bsl")),
		metadataLabel(ownerType) + "." + ownerName + "." + form, nil
}

// extensionInfo reads an export's Configuration.xml and returns the extension
// name and prefix; both empty for a base configuration.
func extensionInfo(root string) (name, prefix string) {
	var cfg xmlConfigRoot
	if err := readXML(filepath.Join(root, "Configuration.xml"), &cfg); err != nil {
		return "", ""
	}
	if cfg.Configuration.Properties.ConfigurationExtensionPurpose == "" {
		return "", ""
	}
	return cfg.Configuration.Properties.Name, cfg.Configuration.Properties.NamePrefix
}

// normalizeDumps makes export roots comparable (absolute, cleaned) and drops
// duplicates: the same root passed twice (or as "path" and "path/") would
// otherwise look like two sources conflicting with each other.
func normalizeDumps(dumps []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dumps {
		if d == "" {
			continue
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			abs = filepath.Clean(d)
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// procedure is one procedure/function of a module with its own edits and the
// module-local procedures it calls.
type procedure struct {
	name      string
	kind      string // interceptor kind, empty for a plain procedure
	target    string // intercepted method
	continues bool   // ПродолжитьВызов present
	edits     []FormEdit
	calls     []string
}

// splitModule cuts a module into procedures, attributing every edit to the
// procedure it sits in and recording module-local calls, so that edits made in
// helper procedures can be charged to the interceptor that reaches them.
func splitModule(module string) []procedure {
	lines := strings.Split(module, "\n")
	var out []procedure
	var cur *procedure
	pendingKind, pendingTarget := "", ""

	for i := 0; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], "\r")
		line := stripLineComment(raw)
		trimmed := strings.TrimSpace(line)

		if m := reAnnotation.FindStringSubmatch(raw); m != nil {
			pendingKind, pendingTarget = canonicalKind[strings.ToLower(m[1])], m[2]
			continue
		}
		if h := reMethodHead.FindStringSubmatch(line); h != nil {
			out = append(out, procedure{name: h[2], kind: pendingKind, target: pendingTarget})
			cur = &out[len(out)-1]
			pendingKind, pendingTarget = "", ""
			continue
		}
		if cur == nil || trimmed == "" {
			continue
		}
		if isMethodEnd(line) {
			cur = nil
			continue
		}
		// A call may span lines: join until parentheses balance, so that a name
		// literal on the next line is still seen.
		logical, consumed := joinLogicalLine(lines, i)
		if consumed > 0 {
			i += consumed
		}
		cur.edits = append(cur.edits, parseLineEdits(logical, i+1)...)
		if reContinueCall.MatchString(logical) {
			cur.continues = true
		}
		for _, c := range reProcCall.FindAllStringSubmatch(logical, -1) {
			cur.calls = append(cur.calls, strings.ToLower(c[1]))
		}
	}
	return out
}

// joinLogicalLine joins lines starting at i while parentheses stay unbalanced,
// returning the joined text and how many extra lines it consumed.
func joinLogicalLine(lines []string, i int) (string, int) {
	text := stripLineComment(strings.TrimRight(lines[i], "\r"))
	depth := strings.Count(text, "(") - strings.Count(text, ")")
	consumed := 0
	for depth > 0 && i+consumed+1 < len(lines) && consumed < 20 {
		consumed++
		next := stripLineComment(strings.TrimRight(lines[i+consumed], "\r"))
		text += " " + strings.TrimSpace(next)
		depth += strings.Count(next, "(") - strings.Count(next, ")")
	}
	return text, consumed
}

// buildModifiers turns a module into modifiers: one per interceptor for an
// extension (carrying the edits reachable from it), or one whole-module entry
// for the base configuration.
func buildModifiers(module, root, ext, prefix, rel string) []FormModifier {
	procs := splitModule(module)
	byName := map[string]*procedure{}
	for i := range procs {
		byName[strings.ToLower(procs[i].name)] = &procs[i]
	}

	// reachable collects edits from a procedure and everything it calls.
	var reachable func(p *procedure, seen map[string]bool) ([]FormEdit, bool)
	reachable = func(p *procedure, seen map[string]bool) ([]FormEdit, bool) {
		if p == nil || seen[strings.ToLower(p.name)] {
			return nil, false
		}
		seen[strings.ToLower(p.name)] = true
		edits := append([]FormEdit{}, p.edits...)
		cont := p.continues
		for _, c := range p.calls {
			if sub, subCont := reachable(byName[c], seen); sub != nil || subCont {
				edits = append(edits, sub...)
				cont = cont || subCont
			}
		}
		return edits, cont
	}

	base := FormModifier{Dump: root, Extension: ext, Prefix: prefix, Module: rel}
	if ext == "" {
		for _, p := range procs {
			base.Changes = append(base.Changes, p.edits...)
		}
		return []FormModifier{base}
	}

	var out []FormModifier
	for i := range procs {
		if procs[i].kind == "" {
			continue // helper, charged to its interceptor
		}
		edits, cont := reachable(&procs[i], map[string]bool{})
		m := base
		m.Kind, m.Target, m.Method = procs[i].kind, procs[i].target, procs[i].name
		m.Interceptor = procs[i].kind + "(" + procs[i].target + ")"
		m.HasContinue = cont
		m.Changes = edits
		out = append(out, m)
	}
	return out
}

// isFormDataObject reports whether the name addresses the form's data object
// (Объект) rather than a form element. Its Родитель is the data hierarchy, so
// assigning it is not a programmatic form change.
func isFormDataObject(name string) bool {
	low := strings.ToLower(name)
	return low == "объект" || strings.HasPrefix(low, "объект.")
}

// formAttrArgs returns the top-level arguments of every РеквизитФормы(...)
// constructor in the line: РеквизитФормы(Имя, Тип, ПутьКРодителю, Заголовок).
func formAttrArgs(line string) [][]string {
	var out [][]string
	for _, loc := range reAddedAttr.FindAllStringIndex(line, -1) {
		open := strings.LastIndex(line[loc[0]:loc[1]], "(")
		if open < 0 {
			continue
		}
		args, ok := balancedArgs(line[loc[0]+open:])
		if !ok || len(args) == 0 {
			continue
		}
		out = append(out, args)
	}
	return out
}

// balancedArgs reads an argument list starting at its opening parenthesis and
// splits the top-level arguments, ignoring commas inside nested calls and
// string literals.
func balancedArgs(s string) ([]string, bool) {
	depth, inStr := 0, false
	var args []string
	var cur strings.Builder
	for _, r := range s {
		switch {
		case r == '"':
			inStr = !inStr
			cur.WriteRune(r)
		case inStr:
			cur.WriteRune(r)
		case r == '(':
			depth++
			if depth > 1 {
				cur.WriteRune(r)
			}
		case r == ')':
			depth--
			if depth == 0 {
				return append(args, strings.TrimSpace(cur.String())), true
			}
			cur.WriteRune(r)
		case r == ',' && depth == 1:
			args = append(args, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return nil, false // unbalanced: the call is cut off, nothing reliable to say
}

// parseLineEdits finds programmatic form changes in one logical line.
func parseLineEdits(line string, lineNo int) []FormEdit {
	var out []FormEdit
	if strings.TrimSpace(line) == "" {
		return nil
	}
	add := func(kind, target string) {
		out = append(out, FormEdit{Kind: kind, Target: target, Line: lineNo, Text: strings.TrimSpace(line)})
	}
	for _, m := range reChangeAttrs.FindAllStringSubmatch(line, -1) {
		// Two arguments means attributes are being REMOVED: that is the only way
		// ИзменитьРеквизиты drops anything, and only what is passed explicitly.
		if strings.Contains(m[1], ",") {
			add("ИзменитьРеквизитыСУдалением", strings.TrimSpace(m[1]))
		} else {
			add("ИзменитьРеквизиты", "")
		}
	}
	for _, m := range reElemAdd.FindAllStringSubmatch(line, -1) {
		add("ДобавитьЭлемент", m[1])
	}
	for _, args := range formAttrArgs(line) {
		name := strings.Trim(strings.TrimSpace(args[0]), `"`)
		if name == "" {
			continue
		}
		// A third argument is the path to the owning attribute: this is a column
		// of a value table, living in that attribute's namespace. Columns cannot
		// collide with other extensions and need no prefix of their own, so they
		// are kept apart from top-level form attributes.
		if len(args) >= 3 && strings.TrimSpace(args[2]) != "" {
			add("ДобавитьКолонку", name)
			continue
		}
		add("ДобавитьРеквизит", name)
	}
	for _, m := range reCommandAdd.FindAllStringSubmatch(line, -1) {
		add("ДобавитьКоманду", m[1])
	}
	for _, m := range reElemDelete.FindAllStringSubmatch(line, -1) {
		add("УдалитьЭлемент", strings.Trim(m[1], `"`))
	}
	for _, m := range reElemMove.FindAllStringSubmatch(line, -1) {
		add("ПереместитьЭлемент", strings.Trim(m[1], `"`))
	}
	for _, m := range reSetParent.FindAllStringSubmatch(line, -1) {
		// Объект.Родитель is the DATA object's hierarchy parent (the catalog
		// folder the item sits in), not a form element: the form's main
		// attribute is not an element and cannot be reparented.
		if isFormDataObject(m[1]) {
			continue
		}
		add("СменитьРодителя", m[1])
	}
	for _, m := range reSetAction.FindAllStringSubmatch(line, -1) {
		add("УстановитьДействие", m[2])
	}
	if rePluggableCmd.MatchString(line) {
		add("БСП_ПодключаемыеКоманды", "")
	}
	if rePrintMgmt.MatchString(line) {
		add("БСП_Печать", "")
	}
	return out
}
