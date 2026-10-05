package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RightsAudit is the object-centric view of role rights: which roles grant what
// (with RLS conditions) and which roles give no access at all.
type RightsAudit struct {
	Object       string              `json:"object" jsonschema:"e.g. Справочник.Товары"`
	TotalRoles   int                 `json:"totalRoles" jsonschema:"number of roles in the export"`
	Granting     []RoleRights        `json:"granting" jsonschema:"roles granting at least one right"`
	NotGranting  []string            `json:"notGranting,omitempty" jsonschema:"roles granting nothing on this object"`
	RightSummary map[string][]string `json:"rightSummary,omitempty" jsonschema:"right name -> roles granting it"`
	Undetermined []string            `json:"undetermined,omitempty" jsonschema:"roles that do not list the object but have setForNewObjects: the export does not record their rights here"`
	Profiles     []ProfileGrant      `json:"profiles,omitempty" jsonschema:"BSP access-group profiles that reach this object through their roles"`
	Note         string              `json:"note,omitempty"`
}

// ProfileGrant is a BSP access-group profile that grants access to the object
// through one or more of its roles. It answers the question the per-role view
// leaves open: how does a real user end up with this right.
type ProfileGrant struct {
	Profile string   `json:"profile"`
	Roles   []string `json:"roles" jsonschema:"the profile's roles that grant something here"`
	Module  string   `json:"module" jsonschema:"common module the profile was parsed from"`
}

// RoleRights is one role's granted rights on the object.
type RoleRights struct {
	Role   string         `json:"role"`
	Rights []GrantedRight `json:"rights"`
	// Component is set for a role that exists only in an extension.
	Component string `json:"component,omitempty" jsonschema:"project component (extension) that defines the role; absent for a role of the main configuration"`
	// Extensions lists the extensions that borrow a base role and add rights on
	// this object: Rights already holds them.
	Extensions []string `json:"extensions,omitempty" jsonschema:"extensions that add rights on this object to a role of the main configuration"`
}

// GrantedRight is one granted right, with its RLS restrictions when present.
type GrantedRight struct {
	Name string         `json:"name"`
	RLS  []RLSCondition `json:"rls,omitempty" jsonschema:"data access restrictions"`
}

// RLSCondition is one restriction: the fields it scopes (empty = whole object)
// and the condition text.
type RLSCondition struct {
	Fields    []string `json:"fields,omitempty" jsonschema:"restricted fields; empty means the whole object"`
	Condition string   `json:"condition"`
}

// EffectiveRights is what a user holding a set of roles actually gets on one
// object. It is not the per-role view stacked: rights are unioned across roles,
// and so are RLS restrictions, which is where the per-role view misleads.
type EffectiveRights struct {
	Object       string           `json:"object"`
	Roles        []string         `json:"roles" jsonschema:"the role set the answer is computed for"`
	Profile      string           `json:"profile,omitempty" jsonschema:"BSP access-group profile the roles came from"`
	Rights       []EffectiveRight `json:"rights"`
	NoRights     []string         `json:"rolesGrantingNothing,omitempty" jsonschema:"roles of the set that grant nothing on this object"`
	Undetermined []string         `json:"undetermined,omitempty" jsonschema:"roles that do not list the object but have setForNewObjects: their rights here are not in the export"`
	UnknownRoles []string         `json:"unknownRoles,omitempty" jsonschema:"requested roles that the export has no such role for"`
	Note         string           `json:"note,omitempty"`
}

// EffectiveRight is one right as the platform resolves it over the role set.
type EffectiveRight struct {
	Name           string    `json:"name"`
	GrantedBy      []string  `json:"grantedBy"`
	Restricted     bool      `json:"restricted" jsonschema:"true only when EVERY granting role restricts the right by RLS"`
	UnrestrictedBy []string  `json:"unrestrictedBy,omitempty" jsonschema:"roles granting the right with no RLS at all"`
	RLS            []RoleRLS `json:"rls,omitempty" jsonschema:"restrictions per role; the platform OR-es them"`
	Note           string    `json:"note,omitempty" jsonschema:"filled when one role cancels another role's restrictions"`
}

// RoleRLS groups one role's restrictions on a right.
type RoleRLS struct {
	Role       string         `json:"role"`
	Conditions []RLSCondition `json:"conditions"`
}

// EffectiveRights resolves the rights a role set gives on one object. Roles may
// be listed directly and/or taken from a BSP access-group profile.
//
// The platform combines roles by OR, and that applies to RLS too: a role that
// grants a right without a restriction makes the restrictions of the other roles
// irrelevant. This is the failure the per-object view cannot show — a profile
// gets one extra "view everything" role and the carefully written RLS of the
// other roles stops limiting anything.
func (s *XMLSource) EffectiveRights(_ context.Context, objectType, name string, roles []string, profile string) (*EffectiveRights, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}

	out := &EffectiveRights{Object: metadataLabel(objectType) + "." + name, Rights: []EffectiveRight{}}
	if profile != "" {
		p, available, err := s.findAccessProfile(profile)
		if err != nil {
			return nil, err
		}
		if p == nil {
			if len(available) == 0 {
				return nil, fmt.Errorf("профили групп доступа не найдены: ни в одном общем модуле выгрузки нет Роли.Добавить(...) — либо это не БСП-конфигурация, либо профили поставляются иначе")
			}
			return nil, fmt.Errorf("профиль %q не найден; в выгрузке разобраны профили: %s", profile, strings.Join(available, ", "))
		}
		out.Profile = profile
		roles = append(roles, p.Roles...)
	}

	roles = dedupeStrings(roles)
	if len(roles) == 0 {
		return nil, fmt.Errorf("нужен непустой набор ролей: параметр roles и/или profile")
	}

	layers := s.roleLayers()
	dirs := s.roleDirs()
	fullName := objectType + "." + name
	type acc struct {
		grantedBy      []string
		unrestrictedBy []string
		rls            []RoleRLS
	}
	byRight := map[string]*acc{}

	for _, role := range roles {
		dir, ok := dirs[strings.ToLower(role)]
		if !ok {
			out.UnknownRoles = append(out.UnknownRoles, role)
			continue
		}
		granted, listed, setForNew, _ := layeredRoleRights(layers, dir, fullName)
		if len(granted) == 0 {
			if !listed && setForNew {
				out.Undetermined = append(out.Undetermined, dir)
			} else {
				out.NoRights = append(out.NoRights, dir)
			}
			continue
		}
		for _, g := range granted {
			a := byRight[g.Name]
			if a == nil {
				a = &acc{}
				byRight[g.Name] = a
			}
			a.grantedBy = append(a.grantedBy, dir)
			if len(g.RLS) == 0 {
				a.unrestrictedBy = append(a.unrestrictedBy, dir)
			} else {
				a.rls = append(a.rls, RoleRLS{Role: dir, Conditions: g.RLS})
			}
		}
	}

	names := make([]string, 0, len(byRight))
	for n := range byRight {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := byRight[n]
		r := EffectiveRight{
			Name:           n,
			GrantedBy:      a.grantedBy,
			Restricted:     len(a.unrestrictedBy) == 0 && len(a.rls) > 0,
			UnrestrictedBy: a.unrestrictedBy,
			RLS:            a.rls,
		}
		if len(a.unrestrictedBy) > 0 && len(a.rls) > 0 {
			restricting := make([]string, 0, len(a.rls))
			for _, x := range a.rls {
				restricting = append(restricting, x.Role)
			}
			r.Note = "Права ролей складываются по ИЛИ: " + strings.Join(a.unrestrictedBy, ", ") +
				" даёт право " + n + " без ограничений, поэтому RLS ролей " + strings.Join(restricting, ", ") +
				" в этом наборе не ограничивает ничего."
		}
		out.Rights = append(out.Rights, r)
	}

	out.Roles = roles
	sort.Strings(out.NoRights)
	sort.Strings(out.Undetermined)
	sort.Strings(out.UnknownRoles)
	if len(out.Undetermined) > 0 {
		out.Note = "Роли " + strings.Join(out.Undetermined, ", ") + " не перечисляют объект в Rights.xml, " +
			"но у них включено «устанавливать права для новых объектов» (setForNewObjects): их права на этом объекте " +
			"выгрузка не фиксирует, набор ниже посчитан без них."
	}
	return out, nil
}

// roleDirs maps a lower-cased role name to its directory name in the export, so
// a requested role matches regardless of the case it was typed in. Roles of the
// project's extensions are included.
func (s *XMLSource) roleDirs() map[string]string {
	out := map[string]string{}
	roles, err := s.projectRoles()
	if err != nil {
		return out
	}
	for _, r := range roles {
		out[strings.ToLower(r.name)] = r.name
	}
	return out
}

// roleLayer holds the parsed roles of one export root: the main configuration
// (component empty) or an extension.
type roleLayer struct {
	component string
	rights    map[string]xmlRights
}

// roleLayers returns the roles of the main export followed by those of the
// project's other components.
func (s *XMLSource) roleLayers() []roleLayer {
	layers := []roleLayer{{rights: s.cachedRoleRights()}}
	for _, c := range s.otherComponents() {
		if rights := NewXMLSource(c.Dir).cachedRoleRights(); len(rights) > 0 {
			layers = append(layers, roleLayer{component: c.Name, rights: rights})
		}
	}
	return layers
}

// projectRole names one role of the project and the component that defines it
// (empty for the main configuration).
type projectRole struct {
	name      string
	component string
}

// projectRoles lists the roles of the main export in directory order, then the
// roles that exist only in an extension. A role an extension borrows is the
// base role: it is listed once.
func (s *XMLSource) projectRoles() ([]projectRole, error) {
	var out []projectRole
	seen := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(s.root, "Roles"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, projectRole{name: e.Name()})
			seen[e.Name()] = true
		}
	}
	for _, layer := range s.roleLayers()[1:] {
		names := make([]string, 0, len(layer.rights))
		for name := range layer.rights {
			if !seen[name] {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, projectRole{name: name, component: layer.component})
			seen[name] = true
		}
	}
	return out, nil
}

// layeredRoleRights is roleRights over every layer: what the role grants on
// fullName where it is defined plus what each later extension adds to it.
// extensions names those extensions. setForNew is the flag of the layer that
// defines the role.
func layeredRoleRights(layers []roleLayer, role, fullName string) (granted []GrantedRight, listed, setForNew bool, extensions []string) {
	defined := false
	for _, layer := range layers {
		if _, ok := layer.rights[role]; !ok {
			continue
		}
		g, l, sfn := roleRights(layer.rights, role, fullName)
		if !defined {
			defined, setForNew = true, sfn
		} else if len(g) > 0 {
			extensions = append(extensions, layer.component)
		}
		listed = listed || l
		granted = mergeGranted(granted, g)
	}
	return granted, listed, setForNew, extensions
}

// mergeGranted adds the rights of one more layer of the same role. Layers
// combine by OR like roles do: a right granted without a restriction by any
// layer is unrestricted, otherwise the restrictions add up.
func mergeGranted(dst, add []GrantedRight) []GrantedRight {
	for _, g := range add {
		at := -1
		for i := range dst {
			if dst[i].Name == g.Name {
				at = i
				break
			}
		}
		switch {
		case at < 0:
			dst = append(dst, g)
		case len(dst[at].RLS) == 0:
		case len(g.RLS) == 0:
			dst[at].RLS = nil
		default:
			dst[at].RLS = append(dst[at].RLS, g.RLS...)
		}
	}
	return dst
}

// roleNames is the set of exported role names, lower-cased.
func (s *XMLSource) roleNames() map[string]bool {
	out := map[string]bool{}
	for lower := range s.roleDirs() {
		out[lower] = true
	}
	return out
}

// roleRights returns the rights one role grants on fullName (English full name),
// with their RLS restrictions. listed reports whether the role's Rights.xml
// mentions the object at all, and setForNew whether the role is configured to
// cover objects it does not list: without those two an empty result reads as
// "no access", which is wrong for roles like ПолныеПрава.
// The parsed rights of all roles are passed in rather than fetched here: this
// runs once per role, and checking the cache freshness a thousand times per
// audit costs more than the reading it was meant to save.
func roleRights(all map[string]xmlRights, roleDir, fullName string) (granted []GrantedRight, listed, setForNew bool) {
	rights, ok := all[roleDir]
	if !ok {
		return nil, false, false
	}
	setForNew = rights.SetForNewObjects == "true"
	var out []GrantedRight
	for _, obj := range rights.Objects {
		if obj.Name != fullName {
			continue
		}
		listed = true
		for _, r := range obj.Rights {
			if r.Value != "true" {
				continue
			}
			gr := GrantedRight{Name: r.Name}
			for _, rst := range r.Restrictions {
				c := strings.TrimSpace(rst.Condition)
				if c == "" && len(rst.Fields) == 0 {
					continue
				}
				gr.RLS = append(gr.RLS, RLSCondition{Fields: rst.Fields, Condition: c})
			}
			out = append(out, gr)
		}
	}
	return out, listed, setForNew
}

// dedupeStrings keeps the first occurrence of each value, comparing
// case-insensitively but preserving what was written.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

// RightsAudit scans every role of the export for rights on one object,
// identified by metadata type and name.
func (s *XMLSource) RightsAudit(_ context.Context, objectType, name string) (*RightsAudit, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	prefix := queryPrefix[objectType]
	if prefix == "" {
		prefix = objectType
	}
	out := &RightsAudit{
		Object:       prefix + "." + name,
		Granting:     []RoleRights{},
		RightSummary: map[string][]string{},
	}
	fullName := objectType + "." + name

	roles, err := s.projectRoles()
	if err != nil {
		return nil, err
	}
	if len(roles) == 0 {
		return out, nil // no roles exported
	}

	layers := s.roleLayers()
	for _, role := range roles {
		out.TotalRoles++
		granted, listed, setForNew, extensions := layeredRoleRights(layers, role.name, fullName)
		switch {
		case len(granted) > 0:
			out.Granting = append(out.Granting, RoleRights{Role: role.name, Rights: granted, Component: role.component, Extensions: extensions})
			for _, g := range granted {
				out.RightSummary[g.Name] = append(out.RightSummary[g.Name], role.name)
			}
		case !listed && setForNew:
			out.Undetermined = append(out.Undetermined, role.name)
		default:
			out.NotGranting = append(out.NotGranting, role.name)
		}
	}
	sort.Strings(out.NotGranting)
	sort.Strings(out.Undetermined)
	if len(out.Undetermined) > 0 {
		out.Note = "Роли " + strings.Join(out.Undetermined, ", ") + " не перечисляют объект в Rights.xml, " +
			"но у них включено «устанавливать права для новых объектов» (setForNewObjects): выгрузка не фиксирует, " +
			"что именно они дают на этом объекте, проверять в конфигураторе или на живой базе. Так выгружаются роли " +
			"с правами по умолчанию, поэтому «нет в списке» не равно «нет доступа»."
	}
	out.Profiles = s.profilesGranting(out.Granting)
	return out, nil
}

// profilesGranting maps the granting roles back to the BSP profiles that carry
// them. Profiles that share no role with the granting set are left out.
func (s *XMLSource) profilesGranting(granting []RoleRights) []ProfileGrant {
	if len(granting) == 0 {
		return nil
	}
	profiles, err := s.AccessProfiles()
	if err != nil || len(profiles) == 0 {
		return nil
	}

	grantingSet := make(map[string]bool, len(granting))
	for _, g := range granting {
		grantingSet[strings.ToLower(g.Role)] = true
	}

	var out []ProfileGrant
	for _, p := range profiles {
		var roles []string
		for _, r := range p.Roles {
			if grantingSet[strings.ToLower(r)] {
				roles = append(roles, r)
			}
		}
		if len(roles) == 0 {
			continue
		}
		label := p.Name
		if label == "" {
			label = p.Description
		}
		out = append(out, ProfileGrant{Profile: label, Roles: roles, Module: p.Module})
	}
	return out
}
