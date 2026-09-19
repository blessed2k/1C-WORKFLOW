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

// WritePathReport is the static execution path of writing one object: what the
// platform runs, in which order, and what silently joins in through event
// subscriptions. Subscriptions are the blind spot this report exists for — an
// object module read on its own never shows them.
type WritePathReport struct {
	Object   string         `json:"object"`
	Steps    []WriteStep    `json:"steps"`
	Warnings []WriteWarning `json:"warnings,omitempty"`
}

// WriteStep is one thing that runs, in platform order.
type WriteStep struct {
	Order  int    `json:"order"`
	Event  string `json:"event" jsonschema:"e.g. ПередЗаписью, ОбработкаПроведения"`
	Kind   string `json:"kind" jsonschema:"objectModule, subscription, movements or exchangeRegistration"`
	Source string `json:"source" jsonschema:"what runs, e.g. ПодпискаНаСобытие.X -> ОбщийМодуль.М.Обработчик"`
	Detail string `json:"detail,omitempty"`
}

// WriteWarning is a defect this path is prone to. Every rule here is either a
// platform guarantee that is absent (subscription order) or a check that is
// missing from code that will run under data exchange.
type WriteWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Where   string `json:"where,omitempty"`
}

// writeEventOrder is the platform sequence of object-module events when a
// document is written with posting. Verified against independent sources:
// ОбработкаПроведения runs AFTER ПриЗаписи, not before it, and number/code
// assignment happens inside Записать() between ПередЗаписью and ПриЗаписи.
// Events outside a write (Filling, OnCopy, BeforeDelete, form events) are a
// different path and are deliberately not reported here.
var writeEventOrder = []string{
	"FillCheckProcessing",
	"BeforeWrite",
	"OnSetNewNumber",
	"OnSetNewCode",
	"OnWrite",
	"Posting",
	"UndoPosting",
}

// writeEventRu names the events of writeEventOrder.
var writeEventRu = map[string]string{
	"FillCheckProcessing": "ОбработкаПроверкиЗаполнения",
	"BeforeWrite":         "ПередЗаписью",
	"OnSetNewNumber":      "ПриУстановкеНовогоНомера",
	"OnSetNewCode":        "ПриУстановкеНовогоКода",
	"OnWrite":             "ПриЗаписи",
	"Posting":             "ОбработкаПроведения",
	"UndoPosting":         "ОбработкаУдаленияПроведения",
}

// recordSetTypes are the metadata types whose write is a record set write.
var recordSetTypes = map[string]bool{
	"InformationRegister": true, "AccumulationRegister": true,
	"AccountingRegister": true, "CalculationRegister": true,
	"Sequence": true, "Recalculation": true,
}

// objectSuffix maps a metadata type to the type name subscriptions use as their
// source. Verified against the УТ export: registers and sequences subscribe as
// record sets, constants as value managers, everything else as objects.
func objectSuffix(objectType string) string {
	switch {
	case recordSetTypes[objectType]:
		return objectType + "RecordSet"
	case objectType == "Constant":
		return "ConstantValueManager"
	default:
		return objectType + "Object"
	}
}

// canonicalObjectType restores the exported spelling of a metadata type, so a
// lower-cased argument does not silently produce a half-filled report (the
// filesystem is case-insensitive on macOS, the switches in the code are not).
func canonicalObjectType(objectType string) string {
	for known := range queryPrefix {
		if strings.EqualFold(known, objectType) {
			return known
		}
	}
	for known := range recordSetTypes {
		if strings.EqualFold(known, objectType) {
			return known
		}
	}
	if strings.EqualFold(objectType, "Constant") {
		return "Constant"
	}
	return objectType
}

// moduleFile returns the name of the module file that holds the write handlers
// of an object type. Registers keep them in RecordSetModule.bsl and constants in
// ValueManagerModule.bsl, not in ObjectModule.bsl.
func moduleFile(objectType string) string {
	switch {
	case recordSetTypes[objectType]:
		return "RecordSetModule.bsl"
	case objectType == "Constant":
		return "ValueManagerModule.bsl"
	default:
		return "ObjectModule.bsl"
	}
}

// subscription is one parsed EventSubscription file.
type subscription struct {
	name    string
	event   string
	handler string // CommonModule.Модуль.Процедура
}

// xmlSubscription is the root of EventSubscriptions/<Name>.xml. A source is
// given either as <v8:Type> (one concrete type) or as <v8:TypeSet>: a bare kind
// like "DocumentObject", which fires for EVERY document in the configuration, or
// a DefinedType that expands to a list of types. Reading only <v8:Type> loses
// 128 of the 308 subscriptions in УТ, including every "all documents" one.
type xmlSubscription struct {
	Object struct {
		Properties struct {
			Name    string        `xml:"Name"`
			Source  xmlSourceType `xml:"Source"`
			Event   string        `xml:"Event"`
			Handler string        `xml:"Handler"`
		} `xml:"Properties"`
	} `xml:"EventSubscription"`
}

// xmlSourceType holds both source forms of a subscription.
type xmlSourceType struct {
	Types    []string `xml:"Type"`
	TypeSets []string `xml:"TypeSet"`
}

// xmlDefinedType is the root of DefinedTypes/<Name>.xml.
type xmlDefinedType struct {
	Object struct {
		Properties struct {
			Type xmlType `xml:"Type"`
		} `xml:"Properties"`
	} `xml:"DefinedType"`
}

// xmlExchangeContent is the root of ExchangePlans/<Name>/Ext/Content.xml.
type xmlExchangeContent struct {
	Items []struct {
		Metadata   string `xml:"Metadata"`
		AutoRecord string `xml:"AutoRecord"`
	} `xml:"Item"`
}

// WritePath builds the write path for one object.
func (s *XMLSource) WritePath(ctx context.Context, objectType, name string) (*WritePathReport, error) {
	if objectType == "" || name == "" {
		return nil, fmt.Errorf("object type and name are required")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") ||
		strings.ContainsAny(objectType, `/\`) || strings.Contains(objectType, "..") {
		return nil, fmt.Errorf("object type and name must be metadata names, not paths")
	}
	// Metadata types are matched case-insensitively everywhere else in the server,
	// so the canonical spelling is restored once, here.
	objectType = canonicalObjectType(objectType)
	var root xmlObjectRoot
	if err := readXML(filepath.Join(s.root, folderForType(objectType), name+".xml"), &root); err != nil {
		return nil, err
	}
	label := metadataLabel(objectType) + "." + name
	out := &WritePathReport{Object: label, Steps: []WriteStep{}}

	subs, err := s.subscriptionsFor(ctx, objectSuffix(objectType)+"."+name)
	if err != nil {
		return nil, err
	}
	handlers := objectModuleHandlers(filepath.Join(s.root, folderForType(objectType), name, "Ext", moduleFile(objectType)))
	// Handler modules of the subscriptions, parsed once per call: БСП points
	// dozens of subscriptions at the same large common modules.
	mods := moduleCache{}

	order := 0
	add := func(event, kind, source, detail string) {
		order++
		out.Steps = append(out.Steps, WriteStep{Order: order, Event: event, Kind: kind, Source: source, Detail: detail})
	}

	for _, event := range writeEventOrder {
		ru := writeEventRu[event]
		// The object module handler runs before subscriptions on the same event.
		if handlers[ru] {
			add(ru, "objectModule", metadataLabel(objectType)+"Объект."+name, "процедура "+ru+" модуля объекта")
		}
		evSubs := subs[event]
		mutating := 0
		for _, sub := range evSubs {
			detail := ""
			if mutatesData(cleanBSL(s.handlerBody(mods, sub.handler))) {
				detail = "меняет данные источника"
				mutating++
			}
			add(ru, "subscription", "ПодпискаНаСобытие."+sub.name+" -> "+sub.handler, detail)
		}
		// The undefined order only matters when more than one handler actually
		// changes data: in a БСП configuration nearly every event carries a stack
		// of read-only registration handlers, and warning about those is noise.
		if mutating > 1 {
			out.Warnings = append(out.Warnings, WriteWarning{
				Code:    "SubscriptionOrder",
				Message: fmt.Sprintf("на событие %s подписано %d обработчиков, меняющих данные источника (всего подписок %d); порядок их выполнения платформой не гарантируется, результат зависит от того, какой отработает последним", ru, mutating, len(evSubs)),
				Where:   ru,
			})
		}
		// Movements belong to posting and are reported inside it.
		if event == "Posting" && objectType == "Document" {
			rep, err := s.Movements(ctx, name)
			switch {
			case err != nil:
				out.Warnings = append(out.Warnings, WriteWarning{
					Code:    "MovementsUnavailable",
					Message: "состав движений прочитать не удалось, шаги движений в отчёте отсутствуют: " + err.Error(),
					Where:   label,
				})
			default:
				for _, r := range rep.Registers {
					var parts []string
					if r.Declared {
						parts = append(parts, "объявлен в составе движений")
					} else {
						parts = append(parts, "используется в коде, но НЕ объявлен в составе движений")
					}
					if r.WriteFlag {
						parts = append(parts, "Записывать = Истина найдено")
					}
					if len(r.FieldsSet) > 0 {
						parts = append(parts, "поля из кода: "+strings.Join(r.FieldsSet, ", "))
					}
					add(ru, "movements", r.Register, strings.Join(parts, "; "))
				}
			}
		}
	}

	// Only the events of the write path are judged: the same object also carries
	// subscriptions on deletion and on form events, and they are a different path.
	writeSubs := map[string][]subscription{}
	for _, event := range writeEventOrder {
		if list, ok := subs[event]; ok {
			writeSubs[event] = list
		}
	}
	out.Warnings = append(out.Warnings, s.subscriptionCodeWarnings(mods, writeSubs)...)
	exchangeSteps, exchangeWarnings := s.exchangeRegistration(objectType, name, s.registersChanges(mods, writeSubs))
	out.Steps = append(out.Steps, exchangeSteps...)
	for i := range out.Steps {
		out.Steps[i].Order = i + 1
	}
	out.Warnings = append(out.Warnings, exchangeWarnings...)
	return out, nil
}

// subscriptionsFor returns the subscriptions whose Source includes sourceType,
// grouped by event. Source entries carry a namespace prefix that varies between
// exports (cfg:, d5p1: and so on), so only the part after ":" is compared.
func (s *XMLSource) subscriptionsFor(ctx context.Context, sourceType string) (map[string][]subscription, error) {
	out := map[string][]subscription{}
	kind, _, _ := strings.Cut(sourceType, ".")
	definedCache := map[string][]string{}
	for _, sub := range s.cachedSubscriptions() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.sourceMatches(sub.source, sourceType, kind, definedCache) {
			out[sub.event] = append(out[sub.event], subscription{name: sub.name, event: sub.event, handler: sub.handler})
		}
	}
	return out, nil
}

// sourceMatches reports whether a subscription source designates this object.
// A source matches when it names the object exactly (DocumentObject.ЗаказКлиента),
// names the bare kind (DocumentObject: every document), or is a DefinedType whose
// expansion contains either.
func (s *XMLSource) sourceMatches(src xmlSourceType, sourceType, kind string, cache map[string][]string) bool {
	for _, raw := range src.Types {
		if strings.EqualFold(stripNamespace(raw), sourceType) {
			return true
		}
	}
	for _, raw := range src.TypeSets {
		set := stripNamespace(raw)
		if strings.EqualFold(set, sourceType) || strings.EqualFold(set, kind) {
			return true
		}
		name, ok := strings.CutPrefix(set, "DefinedType.")
		if !ok {
			continue
		}
		for _, t := range s.definedTypeMembers(name, cache) {
			if strings.EqualFold(t, sourceType) || strings.EqualFold(t, kind) {
				return true
			}
		}
	}
	return false
}

// definedTypeMembers returns the type list of a DefinedType, cached per call.
// Nested defined types are not expanded: 1C does not allow a defined type to
// list another one as a member.
func (s *XMLSource) definedTypeMembers(name string, cache map[string][]string) []string {
	if list, ok := cache[name]; ok {
		return list
	}
	var dt xmlDefinedType
	list := []string{}
	if err := readXML(filepath.Join(s.root, "DefinedTypes", name+".xml"), &dt); err == nil {
		for _, raw := range dt.Object.Properties.Type.Types {
			list = append(list, stripNamespace(raw))
		}
	}
	cache[name] = list
	return list
}

// stripNamespace drops the "cfg:"-style prefix of a type identifier.
func stripNamespace(raw string) string {
	if _, rest, ok := strings.Cut(strings.TrimSpace(raw), ":"); ok {
		return rest
	}
	return strings.TrimSpace(raw)
}

// subscriptionCodeWarnings reads each handler's body and reports the two defects
// that a handler running on someone else's write is prone to.
func (s *XMLSource) subscriptionCodeWarnings(mods moduleCache, subs map[string][]subscription) []WriteWarning {
	var out []WriteWarning
	seen := map[string]bool{}
	events := make([]string, 0, len(subs))
	for e := range subs {
		events = append(events, e)
	}
	sort.Strings(events)
	for _, e := range events {
		for _, sub := range subs[e] {
			if seen[sub.handler] {
				continue
			}
			seen[sub.handler] = true
			body := cleanBSL(s.handlerBody(mods, sub.handler))
			if body == "" {
				continue
			}
			// The check is only expected of a handler that changes data. Exchange
			// machinery of БСП (registration handlers) touches no business data and
			// must run during a load, so demanding the check there is pure noise.
			if mutatesData(body) && !strings.Contains(body, "ОбменДанными.Загрузка") {
				out = append(out, WriteWarning{
					Code:    "NoExchangeLoadCheck",
					Message: "обработчик меняет данные и не проверяет Источник.ОбменДанными.Загрузка: при загрузке из обмена бизнес-логика отработает поверх присланных значений",
					Where:   sub.handler,
				})
			}
			if reSourceWrite.MatchString(body) {
				out = append(out, WriteWarning{
					Code:    "WriteInsideHandler",
					Message: "обработчик записывает источник события внутри его же записи: это повторный вход в запись, вплоть до зацикливания",
					Where:   sub.handler,
				})
			}
		}
	}
	return out
}

// reSourceWrite matches writing the event source back inside a handler, which
// re-enters the write it is running under. The parameter is named Источник by
// the platform's own handler template, so it is matched by name.
var reSourceWrite = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])Источник\.Записать\s*\(`)

// Mutation evidence. In BSL "=" is both assignment and comparison, so an
// assignment is only recognised at the start of a statement: "Если Источник.Х =
// Истина Тогда" is a comparison and must not count as a change.
var (
	reAssignStmt   = regexp.MustCompile(`(?i)^\s*([\p{L}\d_]+)\.[\p{L}\d_.\[\]]+\s*=[^=]`)
	reCollectionOp = regexp.MustCompile(`(?i)(?:^|[^\p{L}\d_])([\p{L}\d_]+)\.[\p{L}\d_.]*\.?(?:Добавить|Очистить|Удалить|Вставить|Загрузить)\s*\(`)
	reAliasOf      = regexp.MustCompile(`(?i)^\s*([\p{L}\d_]+)\s*=\s*Источник\s*;`)
)

// cleanBSL strips line comments and string literals, so that a rule never fires
// on commented-out or quoted code.
func cleanBSL(body string) string {
	if body == "" {
		return ""
	}
	var b strings.Builder
	for _, raw := range strings.Split(body, "\n") {
		line := stripLineComment(strings.TrimRight(raw, "\r"))
		b.WriteString(reStringLiteral.ReplaceAllString(line, `""`))
		b.WriteByte('\n')
	}
	return b.String()
}

var reStringLiteral = regexp.MustCompile(`"[^"]*"`)

// mutatesData reports whether a handler changes the data being written, rather
// than only reading it or registering the object for exchange. Recognised: an
// assignment into the source or into an alias of it, a collection operation on
// it, and writing it. NOT recognised: mutation inside another procedure the
// source is merely passed to — that needs cross-module analysis and would flag
// most of БСП, so this rule stays deliberately narrow and under-reports.
func mutatesData(body string) bool {
	if body == "" {
		return false
	}
	aliases := map[string]bool{"источник": true}
	for _, line := range strings.Split(body, "\n") {
		if m := reAliasOf.FindStringSubmatch(line); m != nil {
			aliases[strings.ToLower(m[1])] = true
			continue
		}
		if m := reAssignStmt.FindStringSubmatch(line); m != nil && aliases[strings.ToLower(m[1])] {
			return true
		}
		if m := reCollectionOp.FindStringSubmatch(line); m != nil && aliases[strings.ToLower(m[1])] {
			return true
		}
		if reSourceWrite.MatchString(line) {
			return true
		}
	}
	return false
}

// objectModuleHandlers returns which of the write events the object module
// actually implements, keyed by the Russian event name.
func objectModuleHandlers(path string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	wanted := map[string]string{} // lower(ru name) -> ru name
	for _, ru := range writeEventRu {
		wanted[strings.ToLower(ru)] = ru
	}
	for _, m := range parseDeclarations(stripBOM(data)).Methods {
		if ru, ok := wanted[strings.ToLower(m.Name)]; ok {
			out[ru] = true
		}
	}
	return out
}

// handlerBody returns the text of a "CommonModule.Модуль.Процедура" handler.
// mods keeps the modules parsed during the call.
func (s *XMLSource) handlerBody(mods moduleCache, handler string) string {
	parts := strings.Split(handler, ".")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "CommonModule") {
		return ""
	}
	return originalMethod(mods, filepath.Join(s.root, "CommonModules", parts[1], "Ext", "Module.bsl"), parts[2])
}

// exchangeRegistration reports the exchange plans the object belongs to.
// registersChanges reports whether some subscription on the write path already
// registers the object for exchange. In a БСП configuration that is the norm:
// AutoRecord=Deny is set on almost everything (9025 entries against 244 in УТ)
// precisely because the ОбменДанными* subscriptions do the registration.
func (s *XMLSource) registersChanges(mods moduleCache, subs map[string][]subscription) bool {
	for _, list := range subs {
		for _, sub := range list {
			if strings.Contains(cleanBSL(s.handlerBody(mods, sub.handler)), "ЗарегистрироватьИзменения") {
				return true
			}
		}
	}
	return false
}

func (s *XMLSource) exchangeRegistration(objectType, name string, registered bool) ([]WriteStep, []WriteWarning) {
	var steps []WriteStep
	var manual []string // plans where registration is left to code
	entries, err := os.ReadDir(filepath.Join(s.root, "ExchangePlans"))
	if err != nil {
		return nil, nil
	}
	want := objectType + "." + name
	plans := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			plans = append(plans, e.Name())
		}
	}
	sort.Strings(plans)
	for _, plan := range plans {
		var content xmlExchangeContent
		if err := readXML(filepath.Join(s.root, "ExchangePlans", plan, "Ext", "Content.xml"), &content); err != nil {
			continue
		}
		for _, item := range content.Items {
			if !strings.EqualFold(strings.TrimSpace(item.Metadata), want) {
				continue
			}
			auto := item.AutoRecord == "Allow"
			detail := "авторегистрация запрещена: регистрация изменений должна выполняться кодом"
			if auto {
				detail = "авторегистрация разрешена: платформа регистрирует изменение сама"
			}
			steps = append(steps, WriteStep{
				Event:  "РегистрацияИзменений",
				Kind:   "exchangeRegistration",
				Source: "ПланОбмена." + plan,
				Detail: detail,
			})
			if !auto {
				manual = append(manual, plan)
			}
			break
		}
	}
	if len(manual) == 0 || registered {
		return steps, nil
	}
	// A plan built on Конвертацию данных registers the object by its ППД rules,
	// with no line of code involved: "авторегистрация запрещена" there means the
	// platform applies the rules, not that somebody must write ЗарегистрироватьИзменения.
	// exchange_audit already reads those rules; not asking the same reader here
	// turned an object that does reach the other side into a categorical warning
	// that it never will.
	byRules := map[string]bool{}
	for _, r := range s.registrationRules(objectType, name, manual) {
		byRules[r.Plan] = true
	}
	if len(byRules) > 0 {
		for i, st := range steps {
			if st.Kind == "exchangeRegistration" && byRules[strings.TrimPrefix(st.Source, "ПланОбмена.")] {
				steps[i].Detail = "авторегистрация запрещена, но объект покрыт правилами регистрации ППД: регистрирует платформа"
			}
		}
		left := manual[:0:0]
		for _, plan := range manual {
			if !byRules[plan] {
				left = append(left, plan)
			}
		}
		manual = left
	}
	if len(manual) == 0 {
		return steps, nil
	}
	// One warning, not one per plan: a typical БСП configuration puts an object
	// into a dozen plans, and a dozen identical lines drown everything else.
	return steps, []WriteWarning{{
		Code:    "ManualExchangeRegistration",
		Message: fmt.Sprintf("объект входит в %d план(ов) обмена с запретом авторегистрации, и среди подписок пути записи нет ни одной, вызывающей ЗарегистрироватьИзменения: без явной регистрации кодом объект не уйдёт в обмен", len(manual)),
		Where:   "ПланыОбмена: " + strings.Join(manual, ", "),
	}}
}
