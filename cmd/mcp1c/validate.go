package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/validate"
)

type validateBSLInput struct {
	Code       string `json:"code" jsonschema:"the BSL you just wrote (module or procedure)"`
	Module     string `json:"module,omitempty" jsonschema:"the module this code belongs to, as find_api names it (ОбщегоНазначения, Справочники.Номенклатура): its own methods are then not offered as ready-made ones"`
	Path       string `json:"path,omitempty" jsonschema:"the .bsl file the code is (or goes) in, absolute or relative to the dump: gives the module kind (form, object, common...), its object and extension; the form-module and borrowed-attribute checks need it"`
	ModuleKind string `json:"moduleKind,omitempty" jsonschema:"when there is no path yet: common, object, manager, form, recordset, valuemanager, command"`
}

type validateBSLOutput struct {
	Count    int                `json:"count"`
	Findings []validate.Finding `json:"findings"`
	// ReadyMethodsNote comes before the list it qualifies: the list is a lexical
	// hint and most of it is unrelated to the procedure it stands next to.
	ReadyMethodsNote string                `json:"readyMethodsNote,omitempty"`
	ReadyMethods     []app.ReadyMethodItem `json:"readyMethods,omitempty" jsonschema:"possible ready-made methods of БСП and the configuration per procedure of the draft: a lexical hint, never counted as a finding"`
	Checked          []string              `json:"checked" jsonschema:"what was actually verified, so that an empty result is not read as more than it is"`
}

// readyMethodsFinder names, for the procedures declared in a draft, ready-made
// methods of the program interface close to them by words. Declared here, on
// the consumer side: the test hands in a stub instead of a whole indexed
// project.
type readyMethodsFinder interface {
	ReadyMethods(ctx context.Context, in app.ReadyMethodsInput) (app.Response[app.ReadyMethodsReport], error)
}

// registerValidateBSL wires validate_bsl: the finished-draft counterpart of
// get_query_schema and bsl_syntax.
//
// Those two must be called while writing, against the writer's own certainty,
// and that is a contest they lose — this session wrote ten queries and three
// handlers without consulting either. A check that runs on a completed text has
// the opposite trigger ("written — now verify"), which fires by itself; that is
// why syntax checkers and validators get called without being asked. So the same
// knowledge is offered again, at the moment the model will actually reach for it.
//
// The same trigger carries the question the writer skips before writing: is
// there a ready-made method already. For every procedure the draft declares
// the answer names the closest methods of БСП and the configuration.
func registerValidateBSL(server *mcp.Server, provide func() source.ConfigSource, idx syntaxCorpus, ready readyMethodsFinder) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_bsl",
		Description: validateBSLDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in validateBSLInput) (*mcp.CallToolResult, validateBSLOutput, error) {
		if strings.TrimSpace(in.Code) == "" {
			return nil, validateBSLOutput{}, errNoCode
		}
		// Half of what this tool proves comes from the platform reference. With a
		// corpus it cannot read, a clean report would mean "nothing was checked",
		// which is the one answer a validator must never give quietly.
		if err := syntaxCorpusErr(idx); err != nil {
			return nil, validateBSLOutput{}, err
		}
		var checked []string

		var meta validate.MetadataLookup
		var project *validate.DiskProject
		if src := provide(); src != nil {
			project = projectOfSource(src)
			if names, err := projectMetadataNames(ctx, src, project); err == nil {
				meta = func(mdType, name string) bool { return names[mdType+"."+strings.ToLower(name)] }
				checked = append(checked, "ссылки на метаданные — по активной выгрузке и её расширениям")
			}
		}
		if meta == nil {
			checked = append(checked, "ссылки на метаданные НЕ проверены: нет активной выгрузки (set_dump)")
		}
		path := in.Path
		if project != nil && path != "" {
			path = resolveDraftPath(project, path)
		}
		env := validateEnv(idx, meta, project)
		env.Module = validate.ResolveModule(project, validate.ModuleInput{
			Path: path, Module: in.Module, Kind: validate.ModuleKind(strings.ToLower(strings.TrimSpace(in.ModuleKind))),
		}, idx.MemberNames)
		checked = append(checked, checkedNotes(env)...)

		findings := validate.Check(in.Code, env)
		out := validateBSLOutput{Count: len(findings), Findings: findings}
		out.ReadyMethods, out.ReadyMethodsNote, out.Checked = readyMethodsFor(ctx, ready, in, checked)
		return nil, out, nil
	})
}

// validateBSLDescription is the tool description: what is checked, and what a
// path adds. It names what stays out of reach, so a clean report is not read as
// more than it is.
const validateBSLDescription = "Checks BSL you just wrote against the configuration with its extensions (read from the dump on disk, " +
	"not from the index) and the platform reference. Findings: metadata that does not exist (Справочники.X); platform calls " +
	"with a wrong argument count (СтрШаблон and Мин/Макс take a variable count) and a literal СтрШаблон template whose %N " +
	"do not match the values; a call without a module that nothing declares (not the draft, not the module, not the " +
	"form/object context, not the platform, not an export of a global common module); ОбщийМодуль.Метод( where the module " +
	"has no such export method (extension methods included); an assignment, loop variable or Перем-less write to a name " +
	"owned by a common module or a global property (Пользователи =, Документы =: fails at run time); &НаКлиентеНаСервере " +
	"in a form module; in an extension's object module a bare name of a base attribute the extension did not borrow; in " +
	"an object module a loop counter named as an attribute (Для Номер = ...); УПОРЯДОЧИТЬ ПО ПРЕДСТАВЛЕНИЕ() or by its " +
	"alias. Pass path (the .bsl file, absolute or relative to the dump): it tells the module kind, its object and " +
	"extension; without it the module-specific checks do not run and context-dependent findings come as medium. " +
	"moduleKind (form, object, common...) stands in for a file not written yet. For the procedures the draft declares " +
	"(event handlers aside), readyMethods lists possible ready-made methods of БСП and the configuration found by the " +
	"words of the name and comment: a lexical hint, most entries are unrelated; an empty list proves nothing, find_api " +
	"searches by the task. Not checked: types, client/server availability, methods of objects other than common modules, " +
	"query fields. Call it after writing a module or procedure, before applying it. Offline; it neither compiles nor runs " +
	"the code; findings are only what it can prove."

// validateEnv builds the platform and project lookups the checks lean on.
func validateEnv(idx syntaxCorpus, meta validate.MetadataLookup, project *validate.DiskProject) validate.Env {
	env := validate.Env{
		Meta: meta,
		Method: func(name string) (validate.Method, bool) {
			e, ok := idx.GlobalMethod(name)
			if !ok {
				return validate.Method{}, false
			}
			req, max, known := syntax.Arity(e)
			return validate.Method{Signature: e.Signature, Required: req, Total: max, Variadic: max < 0, Unknown: !known}, true
		},
		GlobalProperty: func(name string) (validate.Availability, bool) {
			e, ok := idx.GlobalProperty(name)
			if !ok {
				return validate.Availability{}, false
			}
			return availabilityOf(e.Availability), true
		},
		GlobalMethodNames: idx.GlobalMethodNames,
	}
	if project != nil {
		env.Project = project
	}
	return env
}

// availabilityOf reads the reference's availability line ("Сервер, толстый
// клиент, внешнее соединение"): the thin or web client, the server. A name
// only the thick client has counts as neither.
func availabilityOf(s string) validate.Availability {
	var a validate.Availability
	for _, item := range strings.Split(strings.ToLower(s), ",") {
		switch strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(item), ".")) {
		case "сервер":
			a.Server = true
		case "тонкий клиент", "веб-клиент":
			a.Client = true
		}
	}
	return a
}

// projectOfSource builds the disk project over the active dump and the other
// components of its manifest. A live source has no files: nil.
func projectOfSource(src source.ConfigSource) *validate.DiskProject {
	xs, ok := src.(*source.XMLSource)
	if !ok {
		return nil
	}
	root, others := xs.Roots()
	comps := []validate.Component{{ID: "dump", Root: root, Extension: validate.IsExtensionDump(root)}}
	for _, o := range others {
		comps = append(comps, validate.Component{ID: o.Name, Root: o.Dir, Extension: validate.IsExtensionDump(o.Dir)})
	}
	return validate.NewDiskProject(comps)
}

// resolveDraftPath reads a relative path against the components: the first one
// where the file exists, else the main dump.
func resolveDraftPath(p *validate.DiskProject, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	comps := p.Components()
	for _, c := range comps {
		cand := filepath.Join(c.Root, path)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if len(comps) > 0 {
		return filepath.Join(comps[0].Root, path)
	}
	return path
}

// projectNote says what the project-wide checks leaned on.
func projectNote(env validate.Env) string {
	switch {
	case env.Project == nil:
		return "вызовы без модуля, ОбщийМодуль.Метод и присваивания общим модулям НЕ проверены: нет выгрузки на диске"
	case !env.Project.Complete():
		return "вызовы без модуля НЕ проверены: в проекте нет основной конфигурации, только расширения; " +
			"ОбщийМодуль.Метод проверены только для собственных модулей расширений"
	default:
		return "вызовы без модуля и ОбщийМодуль.Метод — по общим модулям конфигурации и расширений на диске; " +
			"присваивания — по общим модулям и свойствам глобального контекста"
	}
}

// checkedNotes says which of the context checks ran and on what.
func checkedNotes(env validate.Env) []string {
	var out []string
	out = append(out, "число аргументов вызовов глобального контекста — по справочнику платформы (с переменным числом параметров у СтрШаблон, Мин, Макс)")
	out = append(out, projectNote(env))
	if env.Module != nil {
		switch {
		case env.Module.Kind == validate.KindUnknown:
			out = append(out, "вид модуля не известен (нет path и moduleKind): проверки модуля формы, модуля объекта и заимствований не выполнялись, находки по контексту даны как medium")
		case env.Module.Display != "":
			note := "модуль черновика: " + env.Module.Display
			if env.Module.Extension != "" {
				note += ", расширение " + env.Module.Extension
			}
			if env.Module.Methods == nil {
				note += "; методы модуля вне черновика не прочитаны"
			}
			out = append(out, note)
		default:
			out = append(out, "вид модуля: "+string(env.Module.Kind)+" (без файла: методы модуля вне черновика не известны)")
		}
	}
	out = append(out, "запросы в строках: УПОРЯДОЧИТЬ ПО ПРЕДСТАВЛЕНИЕ и по его псевдониму")
	return out
}

const readyMethodsNote = "Подсказка по словам имени и комментария процедуры, не находка: большинство названных методов к процедуре не относятся. " +
	"Читайте назначение (summary; параметры даст get_symbol по uid) и заменяйте свой код вызовом, только когда метод делает то же самое."

// readyMethodsFor asks the index for ready-made methods and says in checked
// what was and was not looked up. A skipped lookup, a failed one and an empty
// one are three different answers, and none of them may read as "nothing ready
// exists".
func readyMethodsFor(ctx context.Context, ready readyMethodsFinder, in validateBSLInput, checked []string) ([]app.ReadyMethodItem, string, []string) {
	if ready == nil {
		return nil, "", checked
	}
	resp, err := ready.ReadyMethods(ctx, app.ReadyMethodsInput{Code: in.Code, Module: in.Module})
	if err != nil {
		// The error names its own cause and remedy (no project root, nothing
		// indexed, a read failure): repeat it instead of guessing.
		return nil, "", append(checked, "готовые методы НЕ искались: "+err.Error())
	}
	if len(resp.Items) == 0 {
		return nil, "", checked
	}
	rep := resp.Items[0]
	for _, w := range resp.Warnings {
		checked = append(checked, "готовые методы, предупреждение "+w.Code+": "+w.Message)
	}
	if resp.Stale {
		checked = append(checked, "готовые методы искались по индексу, который отстал от выгрузки")
	}
	if rep.Checked == 0 {
		return nil, "", append(checked, "готовые методы не искались: в черновике "+strconv.Itoa(rep.Declared)+
			" объявлений, все пропущены (обработчики событий, перехватчики, имена из одного слова без комментария)")
	}
	line := "готовые методы: объявлений в черновике " + strconv.Itoa(rep.Declared) + ", поиск шёл по " + strconv.Itoa(rep.Checked) +
		" (пропущено обработчиков событий и имён из одного слова: " + strconv.Itoa(rep.Skipped) + ")"
	if rep.NotChecked > 0 {
		line += ", ещё " + strconv.Itoa(rep.NotChecked) + " не проверено: сверх потолка на один вызов"
	}
	line += "; возможные готовые методы названы для " + strconv.Itoa(len(rep.Methods)) +
		". Поиск по словам: пустой список не значит, что готового метода нет (синонимы не сводятся), ищите find_api по задаче"
	checked = append(checked, line)
	if len(rep.Methods) == 0 {
		return nil, "", checked
	}
	return rep.Methods, readyMethodsNote, checked
}

var errNoCode = errUsage("pass code: the BSL to check")

type errUsage string

func (e errUsage) Error() string { return string(e) }

// projectMetadataNames is the metadata of every component: an extension's own
// object (Справочники.кубик_X) is not in the configuration's tree, and judging
// extension code against the configuration alone flags it as missing.
func projectMetadataNames(ctx context.Context, src source.ConfigSource, project *validate.DiskProject) (map[string]bool, error) {
	if project == nil {
		return metadataNames(ctx, src)
	}
	out := map[string]bool{}
	var firstErr error
	read := 0
	for _, c := range project.Components() {
		names, err := metadataNames(ctx, source.NewXMLSource(c.Root))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		read++
		for k := range names {
			out[k] = true
		}
	}
	if read == 0 {
		return nil, firstErr
	}
	return out, nil
}

// metadataNames flattens the configuration into a lookup set keyed by
// "Type.lowercasename".
func metadataNames(ctx context.Context, src source.ConfigSource) (map[string]bool, error) {
	tree, err := src.MetadataTree(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, g := range tree.Groups {
		for _, name := range g.Objects {
			out[g.Type+"."+strings.ToLower(name)] = true
		}
	}
	return out, nil
}
