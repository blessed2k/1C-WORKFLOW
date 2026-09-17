package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ChecklistReport compares a new object with an existing one of the same kind
// and lists everywhere the existing object is registered. Creating an object in
// 1C is the easy half: the half that gets forgotten is registering it in the
// subsystems, roles, functional options, exchange plans, journals and criteria,
// and every one of those omissions shows up days later as "у пользователя не
// видно", "не ушло в обмен", "нет в журнале".
type ChecklistReport struct {
	Object  string          `json:"object"`
	Like    string          `json:"like" jsonschema:"the existing object taken as the reference"`
	Items   []ChecklistItem `json:"items"`
	Done    int             `json:"done"`
	Missing int             `json:"missing"`
	Note    string          `json:"note"`
}

// ChecklistItem is one place where the reference object is registered. Status
// compares the two registrations by what they have in common, so a place every
// object of the kind lands in does not read as done.
type ChecklistItem struct {
	Place     string   `json:"place" jsonschema:"e.g. Подсистемы, Роли, Планы обмена"`
	Reference []string `json:"reference,omitempty" jsonschema:"how the reference object is registered there"`
	Actual    []string `json:"actual,omitempty" jsonschema:"how the new object is registered there"`
	MissingIn []string `json:"missingIn,omitempty" jsonschema:"registrations of the reference the new object did not reach: this is the to-do list"`
	Status    string   `json:"status" jsonschema:"готово, частично or не сделано, measured against the places the reference is registered in"`
	Hint      string   `json:"hint,omitempty"`
}

// Checklist statuses. There is deliberately no "частично": comparing counts
// invites false alarms, because a new object does not need every functional
// option or every role of the object it is modelled on. The report states
// presence and shows both lists, and judging the difference is the human's job.
const (
	statusDone    = "готово"
	statusPartial = "частично"
	statusMissing = "не сделано"
)

// NewObjectChecklist builds the checklist for name against the reference object.
func (s *XMLSource) NewObjectChecklist(ctx context.Context, objectType, name, like string) (*ChecklistReport, error) {
	if objectType == "" || name == "" || like == "" {
		return nil, fmt.Errorf("object type, name and the reference object (like) are required")
	}
	for _, v := range []string{objectType, name, like} {
		if strings.ContainsAny(v, `/\`) || strings.Contains(v, "..") {
			return nil, fmt.Errorf("object type and names must be metadata names, not paths")
		}
	}
	objectType = canonicalObjectType(objectType)
	if strings.EqualFold(name, like) {
		return nil, fmt.Errorf("the reference object must differ from the object being checked")
	}
	// The exported spelling, not the caller's: role rights are matched by exact
	// name, and a lower-cased argument silently dropped the whole "Роли" place
	// while the case-insensitive filesystem let everything else pass.
	names := make([]string, 0, 2)
	for _, v := range []string{name, like} {
		var root xmlObjectRoot
		if err := readXML(filepath.Join(s.root, folderForType(objectType), v+".xml"), &root); err != nil {
			return nil, err
		}
		if n := root.Object.Properties.Name; n != "" {
			v = n
		}
		names = append(names, v)
	}
	name, like = names[0], names[1]
	label := metadataLabel(objectType)
	out := &ChecklistReport{
		Object: label + "." + name,
		Like:   label + "." + like,
		Note:   "список построен по тому, где зарегистрирован объект-образец: это не обязательные требования платформы, а то, что придётся повторить, чтобы новый объект вёл себя так же",
	}

	newFull, likeFull := objectType+"."+name, objectType+"."+like
	probes := []struct {
		place string
		hint  string
		fn    func(context.Context, string) ([]string, error)
	}{
		{"Подсистемы", "командный интерфейс: без подсистемы объект не появится в разделах", s.checklistSubsystems},
		{"Роли", "права: без роли объект недоступен даже администратору, если у роли нет установки прав для новых объектов", s.checklistRoles},
		{"Функциональные опции", "видимость: опция скрывает объект целиком или его реквизиты", s.checklistOptions},
		{"Планы обмена", "обмен: объекта нет в составе плана - он не уйдёт на другую сторону", s.checklistExchange},
		{"Подписки на события", "логика на запись: подписки на голый вид сработают сами, именные надо дополнить", s.checklistSubscriptions},
		{"Журналы документов", "журналы: документ не появится в общем журнале, пока не включён в состав", s.checklistJournals},
		{"Критерии отбора", "критерии отбора: связанные документы не найдутся по этому объекту", s.checklistCriteria},
	}
	// The probes read different parts of the export and do not touch each other,
	// so they run together: one of them (roles) reads a thousand files and would
	// otherwise decide the wall time of the whole report.
	type result struct {
		item ChecklistItem
		skip bool
		err  error
	}
	results := make([]result, len(probes))
	var wg sync.WaitGroup
	for i, probe := range probes {
		wg.Add(1)
		go func(i int, place, hint string, fn func(context.Context, string) ([]string, error)) {
			defer wg.Done()
			ref, err := fn(ctx, likeFull)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			if len(ref) == 0 {
				results[i] = result{skip: true} // the reference is not there either
				return
			}
			actual, err := fn(ctx, newFull)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			missing, status := checklistCompare(ref, actual)
			results[i] = result{item: ChecklistItem{
				Place: place, Reference: ref, Actual: actual,
				MissingIn: missing, Status: status, Hint: hint,
			}}
		}(i, probe.place, probe.hint, probe.fn)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		if !r.skip {
			out.Items = append(out.Items, r.item)
		}
	}
	for _, it := range out.Items {
		if it.Status == statusDone {
			out.Done++
		} else {
			out.Missing++
		}
	}
	return out, nil
}

// checklistCompare returns what the new object did not reach and the resulting
// status.
//
// Presence alone was not enough: a brand-new document already lands in the roles
// and subsystems every object of its kind lands in (ПолныеПрава,
// УдаленныйДоступOData, служебные подсистемы), and calling that "готово" is the
// opposite of what a checklist is for. The comparison is against the SAME places
// the reference is registered in, and what is missing is listed, because that
// list is the actual to-do.
func checklistCompare(reference, actual []string) ([]string, string) {
	have := make(map[string]bool, len(actual))
	for _, a := range actual {
		have[strings.ToLower(a)] = true
	}
	var missing []string
	for _, r := range reference {
		if !have[strings.ToLower(r)] {
			missing = append(missing, r)
		}
	}
	switch {
	case len(missing) == 0:
		return nil, statusDone
	case len(missing) == len(reference):
		return missing, statusMissing
	default:
		return missing, statusPartial
	}
}

func (s *XMLSource) checklistSubsystems(ctx context.Context, full string) ([]string, error) {
	placements, err := s.subsystemsOf(ctx, full)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(placements))
	for _, p := range placements {
		// The flag belongs in the answer: in УТ 112 documents of 280 are placed
		// only in subsystems that never reach the command interface, and a bare
		// path makes that look like a finished placement.
		if p.InCommandInterface {
			out = append(out, p.Path)
		} else {
			out = append(out, p.Path+" (не в командном интерфейсе)")
		}
	}
	return out, nil
}

func (s *XMLSource) checklistRoles(ctx context.Context, full string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	objectType, name, _ := strings.Cut(full, ".")
	// An unreadable Roles directory must not look like "the reference has no
	// roles either": that silently removes the place from the checklist.
	audit, err := s.RightsAudit(context.Background(), objectType, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no roles in this export
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(audit.Granting))
	for _, r := range audit.Granting {
		out = append(out, r.Role)
	}
	sort.Strings(out)
	return out, nil
}

func (s *XMLSource) checklistOptions(ctx context.Context, full string) ([]string, error) {
	opts, err := s.functionalOptionsOf(ctx, full)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, o := range opts {
		if seen[o.Name] {
			continue
		}
		seen[o.Name] = true
		out = append(out, o.Name)
	}
	sort.Strings(out)
	return out, nil
}

func (s *XMLSource) checklistExchange(ctx context.Context, full string) ([]string, error) {
	objectType, name, _ := strings.Cut(full, ".")
	rep, err := s.ExchangeAudit(ctx, objectType, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no exchange plans in this export: nothing to repeat
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rep.InPlans))
	for _, p := range rep.InPlans {
		out = append(out, p.Plan)
	}
	return out, nil
}

// checklistSubscriptions counts only the subscriptions that name the object,
// never those bound to a bare type set. A subscription on DocumentObject fires
// for every document in the configuration, including one created a minute ago:
// counting those made the place structurally unable to say "не сделано", while
// the named ones (22 for ЗаказКлиента in УТ) are exactly what has to be repeated.
func (s *XMLSource) checklistSubscriptions(ctx context.Context, full string) ([]string, error) {
	objectType, name, _ := strings.Cut(full, ".")
	want := objectSuffix(objectType) + "." + name
	definedCache := map[string][]string{}
	var out []string
	for _, sub := range s.cachedSubscriptions() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if namesObject(s, sub.source, want, definedCache) {
			out = append(out, sub.name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// namesObject reports whether a subscription source names this object, either
// directly or through a defined type, as opposed to covering its whole kind.
func namesObject(s *XMLSource, src xmlSourceType, want string, cache map[string][]string) bool {
	for _, raw := range src.Types {
		if strings.EqualFold(stripNamespace(raw), want) {
			return true
		}
	}
	for _, raw := range src.TypeSets {
		name, ok := strings.CutPrefix(stripNamespace(raw), "DefinedType.")
		if !ok {
			continue // a bare kind: fires for everything, nothing to repeat
		}
		for _, t := range s.definedTypeMembers(name, cache) {
			if strings.EqualFold(t, want) {
				return true
			}
		}
	}
	return false
}

func (s *XMLSource) checklistJournals(ctx context.Context, full string) ([]string, error) {
	return s.membersOf(ctx, "DocumentJournals", "RegisteredDocuments", full)
}

// checklistCriteria reads both halves of a filter criterion: Content lists the
// fields it searches by, and Type is the type of its value. A catalog is usually
// registered through Type alone (Справочник.Партнеры in ДокументыПоПартнеру),
// so reading Content only made the whole place disappear from the checklist.
func (s *XMLSource) checklistCriteria(ctx context.Context, full string) ([]string, error) {
	byContent, err := s.membersOf(ctx, "FilterCriteria", "Content", full)
	if err != nil {
		return nil, err
	}
	byType, err := s.membersOf(ctx, "FilterCriteria", "Type", full)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range append(byContent, byType...) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// membersOf returns the objects of kind folder whose named list mentions full,
// either as the object itself or as one of its fields.
func (s *XMLSource) membersOf(ctx context.Context, folder, list, full string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, folder))
	if err != nil {
		return nil, nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".xml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".xml"))
		}
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var root xmlMemberList
		if err := readXML(filepath.Join(s.root, folder, n+".xml"), &root); err != nil {
			continue
		}
		items := root.Object.Properties.Registered.Items
		if list == "Content" {
			items = root.Object.Properties.Content.Items
		}
		for _, item := range items {
			item = strings.TrimSpace(item)
			if strings.EqualFold(item, full) || func() bool { _, ok := cutPrefixFold(item, full+"."); return ok }() {
				out = append(out, n)
				break
			}
		}
	}
	return out, nil
}

// xmlMemberList reads the two list-shaped properties this checklist needs.
type xmlMemberList struct {
	Object struct {
		Properties struct {
			Registered struct {
				Items []string `xml:"Item"`
			} `xml:"RegisteredDocuments"`
			Content struct {
				Items []string `xml:"Item"`
			} `xml:"Content"`
		} `xml:"Properties"`
	} `xml:",any"`
}
