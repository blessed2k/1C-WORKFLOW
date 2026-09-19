package syntax

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
)

// MemberLimit is the default cap of the members listing. The largest real owners
// are the global context (~630 entries) and the picture library (~300); every
// ordinary type fits well under this (p99 is ~60), so a type is listed whole
// and only those few are cut, with Truncated and Total saying so.
const MemberLimit = 150

// summaryRunes caps the one-line summary of a compact member: the first line
// of a description is ~35 characters at the median and runs to thousands in the
// worst case.
const summaryRunes = 200

// Member is the compact form of an owner's member: enough to pick the right
// one and write the call, without the parameters and full description, which a
// query+owner lookup returns for the one member actually needed.
type Member struct {
	NameRu    string `json:"nameRu"`
	NameEn    string `json:"nameEn,omitempty"`
	Kind      string `json:"kind" jsonschema:"constructor, method, property, event or another entry kind"`
	Signature string `json:"signature,omitempty"`
	Type      string `json:"type,omitempty" jsonschema:"return type of a method, value type of a property"`
	Summary   string `json:"summary,omitempty" jsonschema:"first line of the description"`
}

// TypeInfo tells that a looked-up name is itself an owner (a platform type):
// how many members it has and how to create it, since "how do I create one" is
// the usual question behind looking a type up.
type TypeInfo struct {
	Name         string   `json:"name"`
	Members      int      `json:"members" jsonschema:"number of members of the type in the reference"`
	Constructors []Member `json:"constructors,omitempty"`
	Hint         string   `json:"hint"`
}

// Lookup is one answer of the reference: name matches, or an owner's members,
// plus what the query turned out to be.
type Lookup struct {
	Owner     string             // resolved owner, when the lookup was restricted to one
	Matches   []onec.SyntaxEntry // full entries matching the name
	Members   []Member           // compact members, when an owner was asked for without a name
	Total     int                // members of the owner before the cap
	Truncated bool               // Members were cut at the limit
	Type      *TypeInfo          // the query is itself an owner's name
	Note      string             // user-facing explanation, e.g. an unknown owner
}

type ownerStat struct {
	name  string
	count int
}

// ownerIndex groups the entries by owner, case-insensitively, once.
func (ix *Index) ownerIndex() map[string]ownerStat {
	ix.ensure()
	ix.ownersOnce.Do(func() {
		ix.owners = map[string]ownerStat{}
		for _, e := range ix.entries {
			if e.Owner == "" {
				continue
			}
			k := strings.ToLower(e.Owner)
			st := ix.owners[k]
			st.name = e.Owner
			st.count++
			ix.owners[k] = st
		}
	})
	return ix.owners
}

// owner resolves an owner name case-insensitively to its spelling in the
// reference. Only Russian owner names exist in the index.
func (ix *Index) owner(name string) (ownerStat, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ownerStat{}, false
	}
	st, ok := ix.ownerIndex()[n]
	return st, ok
}

// splitOwner reads "Type.Member". Owner names may contain dots themselves
// ("ВедущиеВидыРасчета.<Имя плана видов расчета>"), and so may a few member
// names ("_0..._9"), so every dot is tried from the right and the split holds
// only where the left part is a known owner.
func (ix *Index) splitOwner(q string) (ownerStat, string, bool) {
	for i := strings.LastIndex(q, "."); i > 0; i = strings.LastIndex(q[:i], ".") {
		if st, ok := ix.owner(q[:i]); ok {
			return st, strings.TrimSpace(q[i+1:]), true
		}
	}
	return ownerStat{}, "", false
}

// Lookup answers one request. owner (or the "Type.Member" form of query)
// restricts the match to that owner's members; owner with an empty query lists
// the members in compact form, capped at limit (MemberLimit when not positive).
// Without an owner the name search is exactly Search, and a query that is an
// owner's name also gets Type: its member count and constructors.
func (ix *Index) Lookup(query, owner string, limit int) Lookup {
	ix.ensure()
	q := strings.TrimSpace(query)
	o := strings.TrimSpace(owner)
	var st ownerStat
	if o != "" {
		var ok bool
		if st, ok = ix.owner(o); !ok {
			return Lookup{Note: ix.unknownOwnerNote(o)}
		}
	} else if s, member, ok := ix.splitOwner(q); ok {
		st, q = s, member
	}

	if st.name != "" {
		if q == "" {
			members, total := ix.members(st.name, limit)
			return Lookup{Owner: st.name, Members: members, Total: total, Truncated: len(members) < total}
		}
		return Lookup{Owner: st.name, Matches: ix.search(q, st.name, limit)}
	}

	res := Lookup{Matches: ix.search(q, "", limit)}
	if st, ok := ix.owner(q); ok {
		res.Type = &TypeInfo{
			Name:         st.name,
			Members:      st.count,
			Constructors: ix.constructors(st.name),
			Hint: fmt.Sprintf("«%s» это тип. Все члены компактно: owner=%q; один член с параметрами: query=%q.",
				st.name, st.name, st.name+".<Член>"),
		}
	}
	return res
}

// unknownOwnerNote says the owner is not in the reference and names a few that
// contain the given text, so the next call can be the right one.
func (ix *Index) unknownOwnerNote(o string) string {
	n := strings.ToLower(o)
	var like []string
	for k, st := range ix.ownerIndex() {
		if strings.Contains(k, n) {
			like = append(like, st.name)
		}
	}
	sort.Strings(like)
	note := fmt.Sprintf("владелец «%s» в справке платформы не найден (имя владельца ищется по-русски, точно, без учёта регистра)", o)
	if len(like) > 5 {
		like = like[:5]
	}
	if len(like) > 0 {
		note += "; похожие: " + strings.Join(like, ", ")
	}
	return note
}

// kindOrder puts what a caller usually needs first: how to create the object,
// then what to call on it, then what to read, then what it raises.
var kindOrder = map[string]int{"constructor": 0, "method": 1, "property": 2, "event": 3}

func kindRank(kind string) int {
	if r, ok := kindOrder[kind]; ok {
		return r
	}
	return len(kindOrder)
}

// members lists an owner's members compactly, by kind and then by name, and
// returns how many there are in total.
func (ix *Index) members(owner string, limit int) ([]Member, int) {
	if limit <= 0 {
		limit = MemberLimit
	}
	var all []Member
	for _, e := range ix.entries {
		if e.Owner == owner {
			all = append(all, compact(e))
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if ri, rj := kindRank(all[i].Kind), kindRank(all[j].Kind); ri != rj {
			return ri < rj
		}
		return all[i].NameRu < all[j].NameRu
	})
	total := len(all)
	if total > limit {
		all = all[:limit]
	}
	return all, total
}

func (ix *Index) constructors(owner string) []Member {
	var out []Member
	for _, e := range ix.entries {
		if e.Owner == owner && e.Kind == "constructor" {
			out = append(out, compact(e))
		}
	}
	return out
}

// ctorID is the internal page id syntaxgen leaves as a constructor's English
// name (ctor182); it names nothing a caller could write.
var ctorID = regexp.MustCompile(`^ctor\d+$`)

// compact reduces an entry to a Member. The reference keeps a property's value
// type as the first description line ("Тип: Число.") and a method's return type
// as the first line of Returns; both become Type, and the summary is the first
// description line that is not that type line.
func compact(e onec.SyntaxEntry) Member {
	m := Member{NameRu: e.NameRu, NameEn: e.NameEn, Kind: e.Kind, Signature: e.Signature}
	if e.Kind == "constructor" && ctorID.MatchString(m.NameEn) {
		m.NameEn = ""
	}
	if t, ok := typeLine(firstLine(e.Returns)); ok {
		m.Type = t
	}
	for _, line := range strings.Split(e.Description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if t, ok := typeLine(line); ok {
			if m.Type == "" {
				m.Type = t
			}
			continue
		}
		m.Summary = clip(line, summaryRunes)
		break
	}
	return m
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// typeLine reads "Тип: СтрокаТаблицыЗначений, Неопределено." as the type list.
func typeLine(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "Тип:")
	if !ok {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimSpace(rest), "."), true
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
