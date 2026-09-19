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

// ExchangeAuditReport answers the support question that costs the most time:
// "почему объект не ушёл в обмен". The answer is always one of a few facts, and
// all of them are in the export: the object is not in the plan's content, or it
// is there with autoRecord denied and nobody registers it, or it is registered
// but by a subscription nobody remembered about.
type ExchangeAuditReport struct {
	Object     string          `json:"object"`
	InPlans    []ExchangePlan  `json:"inPlans"`
	NotInPlans []string        `json:"notInPlans,omitempty" jsonschema:"plans that do not include the object at all"`
	Registrars []Registrar     `json:"registrars,omitempty" jsonschema:"subscriptions that register changes when the object is written"`
	Findings   []ExchangeIssue `json:"findings,omitempty"`
}

// ExchangePlan is one plan the object belongs to.
type ExchangePlan struct {
	Plan         string `json:"plan"`
	AutoRecord   bool   `json:"autoRecord" jsonschema:"true when the platform registers changes itself"`
	Distributed  bool   `json:"distributed" jsonschema:"РИБ: plan with a distributed infobase"`
	RegisteredBy string `json:"registeredBy,omitempty" jsonschema:"what registers changes for this plan: a subscription, the ППД rules, or empty when nothing was found"`
}

// Registrar is a place that registers changes of this object: a subscription
// whose handler reaches ПланыОбмена.ЗарегистрироватьИзменения, or the ППД
// registration rules of a plan.
type Registrar struct {
	Kind         string `json:"kind" jsonschema:"subscription or registrationRules"`
	Subscription string `json:"subscription,omitempty"`
	Event        string `json:"event,omitempty"`
	Handler      string `json:"handler,omitempty"`
	Plan         string `json:"plan,omitempty" jsonschema:"the plan this registrar covers, when it names one"`
}

// ExchangeIssue is one reason the object may not reach the other side.
type ExchangeIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Where   string `json:"where,omitempty"`
}

// xmlExchangePlanObject is the root of ExchangePlans/<Name>.xml.
type xmlExchangePlanObject struct {
	Object struct {
		Properties struct {
			Name                string `xml:"Name"`
			DistributedInfoBase string `xml:"DistributedInfoBase"`
		} `xml:"Properties"`
	} `xml:"ExchangePlan"`
}

// ExchangeAudit reports how one object participates in data exchange.
func (s *XMLSource) ExchangeAudit(ctx context.Context, objectType, name string) (*ExchangeAuditReport, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	if strings.ContainsAny(name+objectType, `/\`) || strings.Contains(name+objectType, "..") {
		return nil, fmt.Errorf("object type and name must be metadata names, not paths")
	}
	objectType = canonicalObjectType(objectType)
	if err := readXML(filepath.Join(s.root, folderForType(objectType), name+".xml"), &xmlObjectRoot{}); err != nil {
		return nil, err
	}
	out := &ExchangeAuditReport{
		Object:  metadataLabel(objectType) + "." + name,
		InPlans: []ExchangePlan{},
	}

	entries, err := os.ReadDir(filepath.Join(s.root, "ExchangePlans"))
	if err != nil {
		return nil, fmt.Errorf("no ExchangePlans in the export: %w", err)
	}
	plans := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			plans = append(plans, e.Name())
		}
	}
	sort.Strings(plans)

	want := objectType + "." + name
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var content xmlExchangeContent
		if err := readXML(filepath.Join(s.root, "ExchangePlans", plan, "Ext", "Content.xml"), &content); err != nil {
			// A plan without a content file includes nothing; that is a fact, not
			// an error, and it is exactly what the "not in plans" list is for.
			out.NotInPlans = append(out.NotInPlans, plan)
			continue
		}
		found := false
		for _, item := range content.Items {
			if strings.EqualFold(strings.TrimSpace(item.Metadata), want) {
				out.InPlans = append(out.InPlans, ExchangePlan{
					Plan:        plan,
					AutoRecord:  strings.EqualFold(strings.TrimSpace(item.AutoRecord), "Allow"),
					Distributed: s.planIsDistributed(plan),
				})
				found = true
				break
			}
		}
		if !found {
			out.NotInPlans = append(out.NotInPlans, plan)
		}
	}

	registrars, err := s.registrarsFor(ctx, objectType, name, plans)
	if err != nil {
		// A cancelled context must not be reported as "nobody registers this".
		return nil, err
	}
	out.Registrars = append(registrars, s.registrationRules(objectType, name, plans)...)
	// Per-plan coverage is the actual answer to "почему не ушёл": it stays visible
	// even where the finding is deliberately silent.
	for i, p := range out.InPlans {
		if p.AutoRecord {
			out.InPlans[i].RegisteredBy = "авторегистрация платформы"
			continue
		}
		for _, r := range out.Registrars {
			if r.Plan != p.Plan {
				continue
			}
			if r.Kind == "registrationRules" {
				out.InPlans[i].RegisteredBy = "правила регистрации ППД"
			} else {
				out.InPlans[i].RegisteredBy = "подписка " + r.Subscription
			}
			break
		}
	}
	out.Findings = exchangeFindings(out)
	return out, nil
}

// reRuleType matches a type inside the ППД registration rules template.
var reRuleType = regexp.MustCompile(`<Тип>([^<]+)</Тип>`)

// registrationRules reports the plans whose ППД registration rules cover this
// object. The rules ship inside the export as a machine-readable template, and
// they are the answer for the plans built on Конвертацию данных: in УТ seven
// plans carry them, with 2081 content entries in Полный alone.
func (s *XMLSource) registrationRules(objectType, name string, plans []string) []Registrar {
	want := []string{
		metadataLabel(objectType) + "Ссылка." + name,
		metadataLabel(objectType) + "." + name,
		objectType + "Ref." + name,
	}
	var out []Registrar
	for _, plan := range plans {
		data, err := os.ReadFile(filepath.Join(s.root, "ExchangePlans", plan, "Templates", "ПравилаРегистрации", "Ext", "Template.txt"))
		if err != nil {
			continue
		}
		text := string(stripBOM(data))
		for _, m := range reRuleType.FindAllStringSubmatch(text, -1) {
			matched := false
			for _, w := range want {
				if strings.EqualFold(strings.TrimSpace(m[1]), w) {
					matched = true
					break
				}
			}
			if matched {
				out = append(out, Registrar{Kind: "registrationRules", Plan: plan})
				break
			}
		}
	}
	return out
}

// planIsDistributed reports whether the plan is a distributed infobase (РИБ),
// where the whole configuration travels and the rules are different.
func (s *XMLSource) planIsDistributed(plan string) bool {
	var obj xmlExchangePlanObject
	if err := readXML(filepath.Join(s.root, "ExchangePlans", plan+".xml"), &obj); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(obj.Object.Properties.DistributedInfoBase), "true")
}

// reRegisterCall matches the platform call that actually registers a change.
// A bare "ЗарегистрироватьИзменения" also matches procedure NAMES: five handlers
// in УТ are called ЗарегистрироватьИзменения* and register nothing.
var reRegisterCall = regexp.MustCompile(`(?i)(?:ПланыОбмена|ExchangePlans)\.ЗарегистрироватьИзменения\s*\(`)

// reModuleCall matches a call into another common module: Модуль.Метод(.
var reModuleCall = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_.])([\p{L}\d_]+)\.([\p{L}\d_]+)\s*\(`)

// reLocalCall matches a call to a method of the same module: Метод(. БСП wraps
// registration in a local method, so following only Модуль.Метод( stops one step
// short of ПланыОбмена.ЗарегистрироватьИзменения.
var reLocalCall = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_.])([\p{L}\d_]+)\s*\(`)

// bslKeywords are the words a local-call regex would otherwise chase.
// reRegistrationName matches a method name that is about registering changes.
// Following every call was correct but cost 2.4 s per request; following only the
// calls that say what they do costs a tenth of that and finds the same chains,
// because БСП names them ЗарегистрироватьИзменениеОбъекта,
// МеханизмРегистрацииОбъектов… all the way down.
var reRegistrationName = regexp.MustCompile(`(?i)(?:Регистрац|Зарегистрир|Регистрир)`)

// bslKeywords are the words a local-call regex would otherwise chase.
var bslKeywords = map[string]bool{
	"если": true, "иначеесли": true, "пока": true, "для": true, "новый": true,
	"возврат": true, "не": true, "и": true, "или": true, "тогда": true,
	"процедура": true, "функция": true, "вызватьисключение": true, "выполнить": true,
}

// registrationDepth is how far a handler is followed into the modules it calls.
// БСП wraps registration twice: подписка -> обёртка библиотеки -> механизм ППД ->
// ПланыОбмена.ЗарегистрироватьИзменения. Stopping at the handler body itself
// missed 32 real registrars of 308 subscriptions in УТ.
const registrationDepth = 4

// registrarsFor finds what registers changes of this object: the subscriptions
// whose handlers reach the registration call, and the ППД registration rules of
// the plans. Subscription matching is the one from write_path, so a subscription
// bound to a bare type set or to a defined type is found too.
func (s *XMLSource) registrarsFor(ctx context.Context, objectType, name string, plans []string) ([]Registrar, error) {
	subs, err := s.subscriptionsFor(ctx, objectSuffix(objectType)+"."+name)
	if err != nil {
		return nil, err
	}
	var out []Registrar
	events := make([]string, 0, len(subs))
	for e := range subs {
		events = append(events, e)
	}
	sort.Strings(events)
	mods := moduleCache{}     // handler modules, read and parsed once per call
	seen := map[string]bool{} // handler -> already judged
	for _, e := range events {
		for _, sub := range subs[e] {
			if seen[sub.handler] {
				continue
			}
			seen[sub.handler] = true
			text, reaches := s.reachesRegistration(sub.handler, registrationDepth, mods, map[string]bool{})
			if !reaches {
				continue
			}
			out = append(out, Registrar{
				Kind:         "subscription",
				Subscription: sub.name,
				Event:        writeEventName(e),
				Handler:      sub.handler,
				Plan:         planMentioned(text, plans),
			})
		}
	}
	return out, nil
}

// reachesRegistration reports whether a method registers changes, following the
// calls it makes up to depth. Returns the collected text so that the plan a
// handler works for can be recognised in it.
func (s *XMLSource) reachesRegistration(handler string, depth int, mods moduleCache, visited map[string]bool) (string, bool) {
	if depth <= 0 || visited[handler] {
		return "", false
	}
	visited[handler] = true
	body := s.methodBody(mods, handler)
	if body == "" {
		return "", false
	}
	if reRegisterCall.MatchString(body) {
		return body, true
	}
	text := body
	for _, m := range reModuleCall.FindAllStringSubmatch(body, -1) {
		module, method := m[1], m[2]
		if strings.EqualFold(module, "ПланыОбмена") || strings.EqualFold(module, "Метаданные") {
			continue
		}
		if !reRegistrationName.MatchString(method) && !reRegistrationName.MatchString(module) {
			continue
		}
		inner, ok := s.reachesRegistration("CommonModule."+module+"."+method, depth-1, mods, visited)
		if ok {
			return text + "\n" + inner, true
		}
	}
	// Local calls, resolved inside the module the handler lives in.
	if parts := strings.Split(handler, "."); len(parts) == 3 {
		for _, m := range reLocalCall.FindAllStringSubmatch(body, -1) {
			if bslKeywords[strings.ToLower(m[1])] || !reRegistrationName.MatchString(m[1]) {
				continue
			}
			inner, ok := s.reachesRegistration("CommonModule."+parts[1]+"."+m[1], depth-1, mods, visited)
			if ok {
				return text + "\n" + inner, true
			}
		}
	}
	return text, false
}

// methodBody returns the body of a "CommonModule.Модуль.Метод" reference without
// its declaration, so that the method NAME cannot be mistaken for a call. The
// declaration may be wrapped over several lines: the parser knows where the
// body starts.
func (s *XMLSource) methodBody(mods moduleCache, handler string) string {
	mod, m, ok := mods.commonMethod(s.root, handler)
	if !ok {
		return ""
	}
	return cleanBSL(bodyText(mod, m))
}

// planMentioned returns the exchange plan named in the text, if exactly one is.
// БСП passes the plan name to the registration wrapper as a string, which is the
// only link between a subscription and the plan it serves.
func planMentioned(text string, plans []string) string {
	found := ""
	for _, p := range plans {
		if strings.Contains(text, p) {
			if found != "" {
				return "" // ambiguous: do not guess
			}
			found = p
		}
	}
	return found
}

// writeEventName gives the Russian name of an event, falling back to the
// exported spelling for events outside the write path (deletion, form events).
func writeEventName(event string) string {
	if ru, ok := writeEventRu[event]; ok {
		return ru
	}
	return event
}

// exchangeFindings turns the collected facts into the reasons an object may not
// reach the other side.
func exchangeFindings(rep *ExchangeAuditReport) []ExchangeIssue {
	var out []ExchangeIssue
	if len(rep.InPlans) == 0 {
		out = append(out, ExchangeIssue{
			Code:    "NotInAnyPlan",
			Message: "объект не входит в состав ни одного плана обмена: в обмен он не уйдёт",
		})
		return out
	}
	// A registrar that names no plan covers an unknown one, so it silences the
	// finding everywhere: claiming a defect on a guess is worse than staying quiet.
	covered := map[string]bool{}
	global := false
	for _, r := range rep.Registrars {
		if r.Plan == "" {
			global = true
		}
		covered[r.Plan] = true
	}
	var manual []string
	for _, p := range rep.InPlans {
		if !p.AutoRecord && !global && !covered[p.Plan] {
			manual = append(manual, p.Plan)
		}
	}
	if len(manual) > 0 {
		out = append(out, ExchangeIssue{
			Code:    "NoRegistration",
			Message: fmt.Sprintf("в %d план(ах) обмена авторегистрация запрещена, и для них не найдено ни подписки, доходящей до ПланыОбмена.ЗарегистрироватьИзменения, ни правил регистрации ППД: изменения могут не регистрироваться", len(manual)),
			Where:   strings.Join(manual, ", "),
		})
	}
	return out
}
