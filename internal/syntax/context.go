package syntax

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
)

// globalOwner is how the reference names the global context.
const globalOwner = "Глобальный контекст"

// globalNames maps the lower-cased Russian and English names of global-context
// methods and properties to their entries. A validator asks for every call in
// every checked file, and a scan of ~23k entries per call made a run over a
// repository quadratic; the map is built once per corpus.
type globalNames struct {
	methods map[string]int
	props   map[string]int
}

// memberCache keeps MemberNames answers: the set for one owner prefix list is
// asked once per checked file.
type memberCache struct {
	mu   sync.Mutex
	sets map[string]map[string]bool
}

func (ix *Index) globals() *globalNames {
	ix.ensure()
	ix.globalsOnce.Do(func() {
		g := &globalNames{methods: map[string]int{}, props: map[string]int{}}
		for i, e := range ix.entries {
			if e.Owner != globalOwner {
				continue
			}
			var m map[string]int
			switch e.Kind {
			case "method":
				m = g.methods
			case "property":
				m = g.props
			default:
				continue
			}
			for _, n := range []string{e.NameRu, e.NameEn} {
				k := strings.ToLower(strings.TrimSpace(n))
				if k == "" || strings.Contains(k, "<") {
					continue
				}
				if _, seen := m[k]; !seen {
					m[k] = i
				}
			}
		}
		ix.global = g
	})
	return ix.global
}

// GlobalProperty returns the global-context property with exactly this Russian
// or English name: Документы, Метаданные, ПараметрыСеанса. These names are
// read-only from code: an assignment to one fails at run time.
func (ix *Index) GlobalProperty(name string) (onec.SyntaxEntry, bool) {
	g := ix.globals()
	i, ok := g.props[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return onec.SyntaxEntry{}, false
	}
	return ix.entries[i], true
}

// GlobalMethodNames lists the Russian names of the global-context methods, for
// "did you mean" hints.
func (ix *Index) GlobalMethodNames() []string {
	g := ix.globals()
	out := make([]string, 0, len(g.methods))
	seen := map[int]bool{}
	for _, i := range g.methods {
		if seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, ix.entries[i].NameRu)
	}
	return out
}

// MemberNames returns the lower-cased Russian and English names of every member
// (methods, properties, events) of the owners named by prefixes: an owner
// matches a prefix when it equals it or starts with it, so "ДокументОбъект."
// covers "ДокументОбъект.<Имя документа>". Placeholder members ("<Имя
// реквизита>") are left out. This is what a module may reach without a prefix
// besides the global context: the form's own methods in a form module, the
// object's in an object module.
func (ix *Index) MemberNames(prefixes ...string) map[string]bool {
	ix.ensure()
	key := strings.Join(prefixes, "\x00")
	ix.memberSets.mu.Lock()
	defer ix.memberSets.mu.Unlock()
	if set, ok := ix.memberSets.sets[key]; ok {
		return set
	}
	set := map[string]bool{}
	for _, e := range ix.entries {
		if e.Owner == "" || !ownerMatches(e.Owner, prefixes) {
			continue
		}
		for _, n := range []string{e.NameRu, e.NameEn} {
			k := strings.ToLower(strings.TrimSpace(n))
			if k == "" || strings.Contains(k, "<") {
				continue
			}
			set[k] = true
		}
	}
	if ix.memberSets.sets == nil {
		ix.memberSets.sets = map[string]map[string]bool{}
	}
	ix.memberSets.sets[key] = set
	return set
}

func ownerMatches(owner string, prefixes []string) bool {
	for _, p := range prefixes {
		if owner == p || strings.HasPrefix(owner, p) {
			return true
		}
	}
	return false
}

var (
	reSigParam    = regexp.MustCompile(`<([^>]+)>`)
	reParamHeader = regexp.MustCompile(`<([^>]+)>\s*\((обязательный|необязательный)\)`)
	reRangeParam  = regexp.MustCompile(`^\D*(\d+)\s*-\s*\D*(\d+)$`)
)

// Arity reads how many arguments a method accepts from its signature and the
// per-parameter marks of the reference. max is -1 when the last parameter takes
// any number of values (Мин(<Значение1,...,ЗначениеN>)); a parameter written as
// a range (СтрШаблон(<Шаблон>, <Значение1-Значение10>)) counts as that many
// positions. ok is false when the signature and the parameter list disagree
// (ПолучитьДанныеВыбора describes fields of a structure parameter with the same
// marks): a count read from such an entry would be invented.
func Arity(e onec.SyntaxEntry) (required, max int, ok bool) {
	sig := reSigParam.FindAllStringSubmatch(e.Signature, -1)
	heads := reParamHeader.FindAllStringSubmatch(e.Params, -1)
	if len(sig) != len(heads) {
		return 0, 0, false
	}
	for i, h := range heads {
		name := strings.TrimSpace(h[1])
		if !strings.EqualFold(name, strings.TrimSpace(sig[i][1])) {
			return 0, 0, false
		}
		if h[2] == "обязательный" {
			required++
		}
		switch {
		case strings.Contains(name, "...") || strings.Contains(name, "…") || strings.Contains(name, ","):
			if i != len(heads)-1 {
				return 0, 0, false
			}
			return required, -1, true
		case reRangeParam.MatchString(name):
			m := reRangeParam.FindStringSubmatch(name)
			from, _ := strconv.Atoi(m[1])
			to, _ := strconv.Atoi(m[2])
			if to < from || i != len(heads)-1 {
				return 0, 0, false
			}
			max += to - from + 1
		default:
			max++
		}
	}
	if exampleDisagrees(e, required, max) {
		return 0, 0, false
	}
	return required, max, true
}

// exampleDisagrees reports that the entry's own example calls the method with
// an argument count the signature does not allow. The reference keeps one
// syntax variant per page (ПолучитьНавигационнуюСсылку has three: by object,
// by attribute, by external data), and the example is where the others show
// up: ПолучитьНавигационнуюСсылку(Объект.Ссылка) next to a signature with
// three mandatory parameters. Such a count is not judged at all.
func exampleDisagrees(e onec.SyntaxEntry, required, max int) bool {
	if e.Example == "" {
		return false
	}
	for _, name := range []string{e.NameRu, e.NameEn} {
		if name == "" {
			continue
		}
		rest := e.Example
		for {
			i := strings.Index(rest, name+"(")
			if i < 0 {
				break
			}
			before := rest[:i]
			rest = rest[i+len(name)+1:]
			if r, _ := utf8.DecodeLastRuneInString(before); r != utf8.RuneError && (r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
				continue // a longer name, or a method of an object
			}
			n, ok := exampleArgs(rest)
			if !ok {
				continue
			}
			if n < required || (max >= 0 && n > max) {
				return true
			}
		}
	}
	return false
}

// exampleArgs counts the top-level arguments of a call in example text whose
// opening parenthesis is consumed; string literals are skipped.
func exampleArgs(s string) (int, bool) {
	depth, n, empty, inStr := 1, 1, true, false
	for _, r := range s {
		if inStr {
			if r == '"' {
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
			empty = false
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				if empty && n == 1 {
					return 0, true
				}
				return n, true
			}
		case ',':
			if depth == 1 {
				n++
			}
		case ' ', '\t', '\n', '\r':
		default:
			if depth == 1 {
				empty = false
			}
		}
	}
	return 0, false
}
