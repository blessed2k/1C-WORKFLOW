package source

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// BSP access-group profiles live in the infobase as catalog data, so an offline
// export knows them only through the code that supplies them: an overridable
// common module fills the profile descriptions on update.
//
// The parser deliberately keys on nothing version-specific. Between BSP 2.2 and
// 3.1 the procedure name changed (ЗаполнитьПоставляемыеПрофилиГруппДоступа ->
// ПриЗаполненииПоставляемыхПрофилейГруппДоступа), the constructor changed
// (НовоеОписаниеПрофиляГруппДоступа vs building the structure by hand), and
// industry configurations add profiles from their own modules. What did not
// change is that a role is added by name — .Роли.Добавить("ИмяРоли") — and the
// profile is identified by a string literal assigned to Имя/Идентификатор/
// Наименование. Every module carrying Роли.Добавить is scanned, so a version
// this parser has not seen shows up as zero profiles, never as wrong data.
var (
	reProfileRoleAdd  = regexp.MustCompile(`(?i)\.Роли\.Добавить\(\s*"([^"]+)"`)
	reProfileName     = regexp.MustCompile(`(?i)\.(?:Имя|Идентификатор)\s*=\s*"([^"]+)"`)
	reProfileDescNStr = regexp.MustCompile(`(?i)\.Наименование\s*=\s*НСтр\(\s*"ru\s*=\s*'([^']+)'`)
	reProfileDesc     = regexp.MustCompile(`(?i)\.Наименование\s*=\s*"([^"]+)"`)
)

// AccessProfile is one BSP access-group profile as declared in configuration code.
type AccessProfile struct {
	Name         string   `json:"name,omitempty" jsonschema:"profile identifier (Имя/Идентификатор)"`
	Description  string   `json:"description,omitempty" jsonschema:"human-readable name (Наименование)"`
	Roles        []string `json:"roles"`
	Module       string   `json:"module" jsonschema:"common module the profile was parsed from"`
	Procedure    string   `json:"procedure,omitempty" jsonschema:"procedure inside that module"`
	MissingRoles []string `json:"missingRoles,omitempty" jsonschema:"roles the profile references but the export has no such role"`
}

// AccessProfiles parses every BSP access-group profile it can find in the
// export's common modules and checks each referenced role against the exported
// roles: a profile pointing at a role that no longer exists is a finding in
// itself, and it doubles as a self-check of the parser.
func (s *XMLSource) AccessProfiles() ([]AccessProfile, error) {
	dir := filepath.Join(s.root, "CommonModules")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// A typical ERP export has thousands of common modules, so reading every one
	// of them to find two profile-supplying modules is wasteful. Names of such
	// modules always mention access or profiles (УправлениеДоступомПереопределяемый,
	// ПрофилиГруппДоступа...), so those are read first; the exhaustive scan is the
	// fallback for a configuration that names them otherwise.
	var likely, rest []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if strings.Contains(lower, "доступ") || strings.Contains(lower, "профил") {
			likely = append(likely, e.Name())
		} else {
			rest = append(rest, e.Name())
		}
	}

	known := s.roleNames()
	out := s.profilesFromModules(likely, known)
	if len(out) == 0 {
		out = s.profilesFromModules(rest, known)
	}
	return out, nil
}

// profilesFromModules parses the profiles declared by the given common modules.
func (s *XMLSource) profilesFromModules(modules []string, known map[string]bool) []AccessProfile {
	var out []AccessProfile
	for _, module := range modules {
		data, err := os.ReadFile(filepath.Join(s.root, "CommonModules", module, "Ext", "Module.bsl"))
		if err != nil {
			continue
		}
		text := string(stripBOM(data))
		if !strings.Contains(text, "Роли.Добавить") {
			continue // not a profile-supplying module, whatever the BSP version
		}
		for _, p := range parseAccessProfiles(text, module) {
			for _, r := range p.Roles {
				if !known[strings.ToLower(r)] {
					p.MissingRoles = append(p.MissingRoles, r)
				}
			}
			out = append(out, p)
		}
	}
	return out
}

// parseAccessProfiles extracts the profiles of one module's text. Roles belong
// to the nearest preceding profile name within the same procedure; roles with
// no name at all are grouped under the enclosing procedure, so nothing is
// silently dropped. The parser finds the procedures and their bodies, so a
// wrapped declaration or English keywords do not merge two procedures, and
// code quoted in a comment (a БСП usage example) is not read as a profile.
func parseAccessProfiles(text, module string) []AccessProfile {
	var out []AccessProfile
	var cur *AccessProfile
	flush := func() {
		if cur != nil && len(cur.Roles) > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}

	mod := parseDeclarations([]byte(text))
	for _, method := range mod.Methods {
		flush()
		for _, line := range strings.Split(bodyText(mod, method), "\n") {
			line = stripLineComment(line)
			name := reProfileName.FindStringSubmatch(line)
			desc := reProfileDescNStr.FindStringSubmatch(line)
			if desc == nil {
				desc = reProfileDesc.FindStringSubmatch(line)
			}
			role := reProfileRoleAdd.FindStringSubmatch(line)
			if name == nil && desc == nil && role == nil {
				continue
			}
			if name != nil && cur != nil && len(cur.Roles) > 0 {
				flush() // a new name: the previous profile is complete
			}
			if cur == nil {
				cur = &AccessProfile{Module: module, Procedure: method.Name}
			}
			switch {
			case name != nil:
				if cur.Name == "" {
					cur.Name = name[1]
				}
			case desc != nil:
				if cur.Description == "" {
					cur.Description = desc[1]
				}
			case !containsString(cur.Roles, role[1]):
				cur.Roles = append(cur.Roles, role[1])
			}
		}
	}
	flush()
	return out
}

// findAccessProfile resolves a profile by identifier or by human name,
// case-insensitively. The second return value lists what is available, so the
// caller can say what the valid names are instead of just failing.
func (s *XMLSource) findAccessProfile(name string) (*AccessProfile, []string, error) {
	profiles, err := s.AccessProfiles()
	if err != nil {
		return nil, nil, err
	}
	var available []string
	for _, p := range profiles {
		label := p.Name
		if label == "" {
			label = p.Description
		}
		available = append(available, label)
		if strings.EqualFold(p.Name, name) || strings.EqualFold(p.Description, name) {
			found := p
			sort.Strings(available)
			return &found, available, nil
		}
	}
	sort.Strings(available)
	return nil, available, nil
}
