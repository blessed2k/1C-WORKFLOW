// Package validate checks finished BSL against the real configuration and the
// platform syntax reference.
//
// It exists because of how the other tools actually get used. Tools that fire on
// a completed artefact ("the file is written — check it") get called on their
// own; tools that require catching an intention ("I am about to write a query —
// fetch the schema first") lose to the writer's own certainty and stay unused.
// So the same knowledge is offered again at the point where the trigger works:
// on the finished text, before it is applied.
//
// Every check here must be provable from the sources. A false positive costs
// more than a miss: findings that turn out wrong teach the reader to skip the
// whole report, and then the real ones go unread too. Where a check cannot see
// everything it needs (the module of a draft is unknown, the configuration is
// not at hand) it either stays silent or lowers its severity to medium and says
// what it could not see.
package validate

import (
	"regexp"
	"sort"
	"strings"
)

// Finding is one problem proved against the configuration or the platform index.
type Finding struct {
	Code       string `json:"code"`
	Severity   string `json:"severity" jsonschema:"high, medium or low"`
	Line       int    `json:"line"`
	Text       string `json:"text,omitempty" jsonschema:"the source line"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// MetadataLookup reports whether an object of the given metadata type exists in
// the active configuration.
type MetadataLookup func(metadataType, name string) bool

// Method describes a global-context platform method as the syntax reference
// knows it.
type Method struct {
	Signature string
	Required  int  // parameters the platform marks обязательный
	Total     int  // positions the signature declares
	Variadic  bool // the last parameter takes any number of values (Мин, Макс): no upper bound
	Unknown   bool // the reference does not let the count be read: the method exists, arity is not judged
}

// MethodLookup returns a global-context method by its exact Russian or English
// name. Anything that is not such a method must return ok=false: a local
// procedure or a BSP common-module call must never be judged against a platform
// signature.
type MethodLookup func(name string) (Method, bool)

// Env is everything the checks may lean on. Each part may be missing; a check
// whose source is missing does not run (the caller says so in its report).
type Env struct {
	Meta   MetadataLookup
	Method MethodLookup
	// GlobalProperty reports a property of the global context (Документы,
	// Метаданные, ПараметрыСеанса): read-only names, and where they exist: on
	// the thin client, on the server. A name the context does not have is a
	// plain variable there (Документы = ... in a &НаКлиенте procedure is legal).
	GlobalProperty func(name string) (avail Availability, ok bool)
	// Module is what the draft belongs to; nil when unknown.
	Module *ModuleContext
	// Project resolves common modules of the configuration and its
	// extensions; nil when there is no project at hand.
	Project Project
	// GlobalMethodNames lists platform global methods for "did you mean".
	GlobalMethodNames func() []string
}

// ModuleKind is the kind of module a draft is written for.
type ModuleKind string

// Module kinds. KindUnknown means the draft came without a path or a kind.
const (
	KindUnknown            ModuleKind = ""
	KindCommon             ModuleKind = "common"
	KindObject             ModuleKind = "object"
	KindManager            ModuleKind = "manager"
	KindForm               ModuleKind = "form"
	KindRecordSet          ModuleKind = "recordset"
	KindValueManager       ModuleKind = "valuemanager"
	KindCommand            ModuleKind = "command"
	KindApplication        ModuleKind = "application"
	KindSession            ModuleKind = "session"
	KindExternalConnection ModuleKind = "externalconnection"
	KindService            ModuleKind = "service"
)

// Availability is where a name exists or a method runs: on the thin client, on
// the server.
type Availability struct {
	Client bool
	Server bool
}

func (a Availability) meets(b Availability) bool {
	return (a.Client && b.Client) || (a.Server && b.Server)
}

// ModuleContext is what is known about the module a draft belongs to.
type ModuleContext struct {
	Kind ModuleKind
	// Default is where a method without a compilation directive runs: the
	// server in form and object modules (&НаСервере is the default), the
	// common module's own flags, the client in the application module.
	Default Availability
	// Display names the module in messages: «модуль объекта Документ.X».
	Display string
	// Methods are the lower-cased names of procedures and functions of the
	// same module outside the draft: the module file on disk and the same
	// module in the other components (an extension module reaches the
	// methods of the module it extends). Nil: the module was not read.
	Methods map[string]bool
	// ContextNames are lower-cased names reachable without a prefix from the
	// module's own context: members of the form or object type, attributes of
	// the object or of the form, common attributes.
	ContextNames map[string]bool
	// ContextComplete says ContextNames include the attributes (the object
	// description or Form.xml was read), so a bare name outside them is not an
	// attribute.
	ContextComplete bool
	// Unborrowed: in an object module of an object borrowed into an
	// extension, the attributes and tabular sections of the base object that
	// the extension did not borrow (lower-cased -> as written). The module
	// does not see them by bare name.
	Unborrowed map[string]string
	// ObjectProps: in an object module, the standard and own attributes the
	// module reaches by bare name (lower-cased -> as written).
	ObjectProps map[string]string
	// Extension names the extension the module belongs to, if any.
	Extension string
}

// Project resolves common modules across the configuration and its extensions.
type Project interface {
	// CommonModule returns a common module by name.
	CommonModule(name string) (CommonModule, bool)
	// GlobalExport reports an export method of a global common module: one
	// that is called without the module name.
	GlobalExport(name string) bool
	// Complete reports that the configuration itself is part of the project:
	// without it a name missing from the extensions proves nothing.
	Complete() bool
}

// CommonModule is a common module as the project knows it, merged over the
// components that define or borrow it.
type CommonModule struct {
	Name   string
	Global bool
	// Where the module's name is visible: where it is compiled, plus the
	// client for a server module with «Вызов сервера». Elsewhere the name is
	// free for a variable.
	Availability Availability
	// Runs is where the module's own code runs: a method without a
	// directive inside it runs there.
	Runs Availability
	// Methods: lower-cased name -> the method; Export is true when any
	// component declares it with Экспорт.
	Methods map[string]MethodInfo
	// Complete: the module text was read where the module is defined (the
	// configuration, or an extension's own module), so a missing method is
	// provably missing.
	Complete bool
}

// MethodInfo is a procedure or function of a module.
type MethodInfo struct {
	Name   string
	Export bool
}

// managerCollections maps a manager collection to the metadata type behind it.
// Only unambiguous ones are listed: a wrong mapping would invent errors.
var managerCollections = map[string]string{
	"Справочники":             "Catalog",
	"Документы":               "Document",
	"РегистрыСведений":        "InformationRegister",
	"РегистрыНакопления":      "AccumulationRegister",
	"РегистрыБухгалтерии":     "AccountingRegister",
	"РегистрыРасчета":         "CalculationRegister",
	"Перечисления":            "Enum",
	"Обработки":               "DataProcessor",
	"Отчеты":                  "Report",
	"ПланыВидовХарактеристик": "ChartOfCharacteristicTypes",
	"ПланыСчетов":             "ChartOfAccounts",
	"ПланыВидовРасчета":       "ChartOfCalculationTypes",
	"БизнесПроцессы":          "BusinessProcess",
	"Задачи":                  "Task",
	"ПланыОбмена":             "ExchangePlan",
	"Константы":               "Constant",
}

const b = `(?:^|[^\p{L}\d_.])`

var (
	reManagerRef = regexp.MustCompile(b + `(` + collectionsAlt() + `)\.([\p{L}\d_]+)`)
	reCallHead   = regexp.MustCompile(b + `([\p{L}_][\p{L}\d_]*)\s*\(`)
)

func collectionsAlt() string {
	names := make([]string, 0, len(managerCollections))
	for k := range managerCollections {
		names = append(names, k)
	}
	// Longest first: РегистрыСведений must win over a shorter prefix.
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	return strings.Join(names, "|")
}

// BSL checks the module text against metadata and the platform signatures
// only: the checks that need no module or project context.
func BSL(code string, meta MetadataLookup, method MethodLookup) []Finding {
	return Check(code, Env{Meta: meta, Method: method})
}

// Check runs every check the environment allows and returns the findings
// ordered by line.
func Check(code string, env Env) []Finding {
	m := analyze(code)
	c := &checker{m: m, env: env}
	out := []Finding{}

	for i := 0; i < len(m.lines); i++ {
		l := m.lines[i]
		if l.kind != lineCode {
			continue
		}
		logical, consumed := joinCall(m.lines, i)
		raw := strings.TrimSpace(l.raw)
		out = append(out, c.checkManagerRefs(logical, raw, l.no)...)
		out = append(out, c.checkCalls(logical, raw, l.no)...)
		i += consumed
	}
	out = append(out, c.checkCommonModuleCalls()...)
	out = append(out, c.checkAssignments()...)
	out = append(out, c.checkFormDirectives()...)
	out = append(out, c.checkUnborrowed()...)
	out = append(out, c.checkQueries()...)

	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return dedup(out)
}

func dedup(fs []Finding) []Finding {
	seen := map[string]bool{}
	out := fs[:0]
	for _, f := range fs {
		k := f.Code + "\x00" + itoa(f.Line) + "\x00" + f.Message
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

type checker struct {
	m   *module
	env Env
}

// execOf says where a method of the draft runs: its directive, else the
// module's default (&НаСервере in form and object modules), else both.
func (c *checker) execOf(md *proc) Availability {
	if md != nil {
		switch md.directive {
		case "наклиенте", "atclient":
			return Availability{Client: true}
		case "насервере", "atserver", "насерверебезконтекста", "atservernocontext":
			return Availability{Server: true}
		case "наклиентенасерверебезконтекста", "atclientatservernocontext", "наклиентенасервере", "atclientatserver":
			return Availability{Client: true, Server: true}
		}
	}
	if mc := c.env.Module; mc != nil && mc.Default != (Availability{}) {
		return mc.Default
	}
	return Availability{Client: true, Server: true}
}

// checkManagerRefs proves a reference against the configuration: Справочники.X
// where X is not in the metadata is a name that cannot resolve at runtime —
// a typo, or an object renamed out from under the code.
//
// Not a reference: a method of the collection itself (Документы.ТипВсеСсылки()),
// and a local variable that shares the collection's name (on the client
// Документы is no global name, and Документы.Добавить(...) is a variable's).
func (c *checker) checkManagerRefs(line, raw string, lineNo int) []Finding {
	meta := c.env.Meta
	if meta == nil {
		return nil
	}
	md := c.m.methodAt(lineNo)
	var out []Finding
	for _, loc := range reManagerRef.FindAllStringSubmatchIndex(line, -1) {
		collection, name := line[loc[2]:loc[3]], line[loc[4]:loc[5]]
		mdType := managerCollections[collection]
		if mdType == "" || meta(mdType, name) {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(line[loc[5]:], " \t"), "(") {
			continue
		}
		lower := strings.ToLower(collection)
		if c.m.isLocal(md, lower) || (md != nil && md.assigned[lower]) {
			continue
		}
		if c.env.GlobalProperty != nil {
			if avail, ok := c.env.GlobalProperty(collection); ok && !avail.meets(c.execOf(md)) {
				continue
			}
		}
		out = append(out, Finding{
			Code:     "UnknownMetadataObject",
			Severity: "high",
			Line:     lineNo,
			Text:     raw,
			Message: "В конфигурации нет объекта " + collection + "." + name +
				" — обращение не разрешится в рантайме.",
			Suggestion: "Проверить имя по get_metadata_tree: объект переименован, отсутствует в этой конфигурации или опечатка.",
		})
	}
	return out
}

// checkCalls judges the calls without a module in front: the argument count of
// a platform method, and a name that nothing in reach declares.
func (c *checker) checkCalls(line, raw string, lineNo int) []Finding {
	var out []Finding
	for _, loc := range reCallHead.FindAllStringSubmatchIndex(line, -1) {
		name := line[loc[2]:loc[3]]
		lower := strings.ToLower(name)
		if isKeyword(name) || afterNew(line[:loc[2]]) || afterDot(line[:loc[2]]) {
			continue
		}
		if c.m.declared[lower] != nil {
			continue
		}
		var sig Method
		var platform bool
		if c.env.Method != nil {
			sig, platform = c.env.Method(name)
		}
		if platform {
			if f, ok := c.arity(name, sig, line[loc[1]:], raw, lineNo); ok {
				out = append(out, f)
			}
			if strings.EqualFold(name, "СтрШаблон") || strings.EqualFold(name, "StrTemplate") {
				out = append(out, c.checkStrTemplate(line[loc[1]:], raw, lineNo)...)
			}
			continue
		}
		if f, ok := c.unknownCall(name, raw, lineNo); ok {
			out = append(out, f)
		}
	}
	return out
}

// arity compares a call against the platform signature. Only global-context
// methods are judged, and only when the module does not define a procedure of
// the same name — everything else could be a common-module or local call.
func (c *checker) arity(name string, sig Method, rest, raw string, lineNo int) (Finding, bool) {
	if sig.Unknown {
		return Finding{}, false
	}
	// rest starts just past the opening parenthesis.
	args, ok := countArgs(rest)
	if !ok {
		return Finding{}, false // call is cut off or unbalanced: no reliable count
	}
	switch {
	case args < sig.Required:
		return Finding{
			Code:     "WrongArgCount",
			Severity: "high",
			Line:     lineNo,
			Text:     raw,
			Message: name + " вызван с " + plural(args) + ", обязательных — " +
				itoa(sig.Required) + ". Сигнатура: " + sig.Signature,
			Suggestion: "Сверить вызов с сигнатурой: bsl_syntax (query=" + name + ").",
		}, true
	case !sig.Variadic && args > sig.Total:
		return Finding{
			Code:     "WrongArgCount",
			Severity: "high",
			Line:     lineNo,
			Text:     raw,
			Message: name + " вызван с " + plural(args) + ", в сигнатуре параметров — " +
				itoa(sig.Total) + ". Сигнатура: " + sig.Signature,
			Suggestion: "Сверить вызов с сигнатурой: bsl_syntax (query=" + name + ").",
		}, true
	}
	return Finding{}, false
}

// unknownCall: a call without a module in front reaches the module's own
// methods, the methods of its context (form, object, manager), the platform's
// global methods and the export methods of global common modules. A name none
// of them declares does not compile: «Процедура или функция с указанным именем
// не определена».
func (c *checker) unknownCall(name, raw string, lineNo int) (Finding, bool) {
	p := c.env.Project
	if p == nil || !p.Complete() || c.env.Method == nil {
		return Finding{}, false
	}
	lower := strings.ToLower(name)
	mc := c.env.Module
	if mc != nil && (mc.Methods[lower] || mc.ContextNames[lower]) {
		return Finding{}, false
	}
	if p.GlobalExport(name) {
		return Finding{}, false
	}
	f := Finding{
		Code:     "UnknownGlobalFunction",
		Severity: "high",
		Line:     lineNo,
		Text:     raw,
		Message: name + "() не объявлен ни в черновике, ни в модуле, ни в глобальных общих модулях конфигурации и расширений, " +
			"и это не метод глобального контекста платформы: модуль не скомпилируется («Процедура или функция с указанным именем не определена»).",
		Suggestion: "Проверить имя по bsl_syntax (query=" + name + ") и find_api.",
	}
	if near := nearNames(name, c.platformNames(), 3); len(near) > 0 {
		f.Suggestion = "Похожие методы платформы: " + strings.Join(near, ", ") + ". Сверить по bsl_syntax."
	}
	if mc == nil || mc.Methods == nil {
		f.Severity = "medium"
		f.Message += " Модуль черновика не известен: если метод объявлен в том же модуле вне черновика, передайте path."
	}
	return f, true
}

func (c *checker) platformNames() []string {
	if c.env.GlobalMethodNames == nil {
		return nil
	}
	return c.env.GlobalMethodNames()
}

// joinCall joins continuation lines while parentheses stay open, so a call split
// across lines is still counted as one.
func joinCall(lines []srcLine, i int) (string, int) {
	text := lines[i].code
	depth := strings.Count(text, "(") - strings.Count(text, ")")
	consumed := 0
	for depth > 0 && i+consumed+1 < len(lines) && consumed < 20 {
		consumed++
		next := lines[i+consumed]
		if next.kind != lineCode && next.kind != lineBlank {
			continue
		}
		text += " " + strings.TrimSpace(next.code)
		depth += strings.Count(next.code, "(") - strings.Count(next.code, ")")
	}
	return text, consumed
}

// countArgs counts top-level arguments of a call whose opening parenthesis has
// already been consumed. Empty parentheses are zero arguments; a skipped
// optional argument (Метод(А, , Б)) still occupies its position.
func countArgs(s string) (int, bool) {
	depth, n := 1, 1
	empty := true
	for _, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
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
		default:
			if depth == 1 && !isSpace(r) {
				empty = false
			}
		}
	}
	return 0, false // unbalanced: say nothing rather than guess
}

// splitArgs returns the top-level arguments of a call whose opening
// parenthesis has been consumed.
func splitArgs(s string) ([]string, bool) {
	depth := 1
	var out []string
	from := 0
	for i, r := range s {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
			if depth == 0 {
				out = append(out, s[from:i])
				return out, true
			}
		case ',':
			if depth == 1 {
				out = append(out, s[from:i])
				from = i + 1
			}
		}
	}
	return nil, false
}

// keywords are BSL constructs that look like a call but are not one, plus the
// type conversion functions the reference files under language operators.
var keywords = map[string]bool{}

func init() {
	for _, k := range []string{
		"если", "тогда", "иначе", "иначеесли", "конецесли", "цикл", "конеццикла",
		"для", "пока", "каждого", "из", "по", "процедура", "функция", "конецпроцедуры",
		"конецфункции", "возврат", "новый", "попытка", "исключение", "конецпопытки",
		"и", "или", "не", "экспорт", "перем", "знач", "выполнить", "вычислить",
		"вызватьисключение", "ждать", "асинх", "перейти", "продолжить", "прервать",
		"добавитьобработчик", "удалитьобработчик", "истина", "ложь", "неопределено",
		"строка", "число", "дата", "булево", "тип",
		"if", "then", "else", "elsif", "endif", "do", "enddo", "for", "while", "each", "in", "to",
		"procedure", "function", "endprocedure", "endfunction", "return", "new", "try",
		"except", "endtry", "and", "or", "not", "export", "var", "val", "execute", "eval",
		"raise", "await", "async", "goto", "continue", "break", "addhandler", "removehandler",
		"true", "false", "undefined", "null", "string", "number", "date", "boolean", "type",
	} {
		keywords[k] = true
	}
}

// isKeyword filters BSL constructs that look like a call but are not one.
func isKeyword(name string) bool { return keywords[strings.ToLower(name)] }

// afterNew reports that the text before a name ends with Новый: the name is a
// constructor, not a call.
func afterNew(before string) bool {
	w := strings.ToLower(lastWord(strings.TrimRight(before, " \t")))
	return w == "новый" || w == "new"
}

// afterDot reports that the name is a member: ". Имя(" with spaces counts too.
func afterDot(before string) bool {
	t := strings.TrimRight(before, " \t")
	return strings.HasSuffix(t, ".")
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	if neg {
		return "-" + string(d)
	}
	return string(d)
}

func plural(n int) string {
	s := itoa(n) + " аргумент"
	switch {
	case n%10 == 1 && n%100 != 11:
		return s + "ом"
	default:
		return s + "ами"
	}
}
