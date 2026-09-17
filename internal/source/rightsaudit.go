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

	dirs := s.roleDirs()
	fullName := objectType + "." + name
	type acc struct {
		grantedBy      []string
		unrestrictedBy []string
		rls            []RoleRLS
	}
	byRight := map[string]*acc{}
	allRights := s.cachedRoleRights()

	for _, role := range roles {
		dir, ok := dirs[strings.ToLower(role)]
		if !ok {
			out.UnknownRoles = append(out.UnknownRoles, role)
			continue
		}
		granted, listed, setForNew := roleRights(allRights, dir, fullName)
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
// a requested role matches regardless of the case it was typed in.
func (s *XMLSource) roleDirs() map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(s.root, "Roles"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out[strings.ToLower(e.Name())] = e.Name()
		}
	}
	return out
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

	rolesDir := filepath.Join(s.root, "Roles")
	entries, err := os.ReadDir(rolesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // no roles exported
		}
		return nil, err
	}

	allRights := s.cachedRoleRights()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out.TotalRoles++
		granted, listed, setForNew := roleRights(allRights, e.Name(), fullName)
		switch {
		case len(granted) > 0:
			out.Granting = append(out.Granting, RoleRights{Role: e.Name(), Rights: granted})
			for _, g := range granted {
				out.RightSummary[g.Name] = append(out.RightSummary[g.Name], e.Name())
			}
		case !listed && setForNew:
			out.Undetermined = append(out.Undetermined, e.Name())
		default:
			out.NotGranting = append(out.NotGranting, e.Name())
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
