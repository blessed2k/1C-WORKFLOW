package index

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// План публикации (issue #3, шаг 3). Публикация файла делится на две части:
//
//   - planFile/planLinks: чистые функции. Строят строки файла на ключах
//     идентичности (identity_key), без id и без SQLite, поэтому идут в пуле
//     параллельно и проверяются без store (plan_test.go);
//   - publishFiles и функции publishXxx (publish.go, publishderive.go,
//     publishmeta2.go, publishforms.go): единственный писатель применяет
//     план, переводит ключи в id через txNodeCache и пишет строки. ADR-014 «одна
//     транзакция, один писатель» не меняется: SQL по-прежнему идёт из одной
//     горутины, в той же транзакции и в том же порядке файлов.
//
// Проходов по-прежнему два и по той же причине: ссылки и производные факты
// ссылаются на символы и объекты ДРУГИХ файлов той же транзакции, поэтому
// planLinks применяется только после того, как применены planFile всех
// файлов. Всё, что зависит от состояния store (найден ли узел цели, id уже
// вставленных строк), решает писатель, а не план.

// planContext: то, что плану нужно знать о компоненте. Только чтение: env
// безопасен для параллельного чтения (кэш builtins под мьютексом), корпус во
// время публикации не меняется.
type planContext struct {
	project   domain.ProjectID
	component domain.ComponentID
	layer     domain.Layer
	env       resolve.Env
	resolved  map[string][]resolvedRef
}

func planContextOf(in publishInput) planContext {
	return planContext{project: in.project, component: in.component, layer: in.layer,
		env: in.env, resolved: in.resolve}
}

// filePlan: проход 1 одного файла, identity-факты, которые не ссылаются на
// символы и объекты других файлов.
type filePlan struct {
	rel         string
	size        int64
	mtimeNS     int64
	contentHash string
	diagnostics []domain.Diagnostic

	object       *objectPlan
	module       *modulePlan
	form         *formStructurePlan
	subscription *handlerPlan[store.EventSubscription]
	// roleRights: у файла есть права роли; сами права публикует проход 2.
	roleRights bool
	// httpEndpoints: методы HTTP-сервиса из его XML (FileID ставит писатель).
	httpEndpoints []store.HTTPEndpoint
}

// objectPlan: объект метаданных и всё, что публикуется вместе с ним. Id
// объекта (ObjectID, OwnerObjectID, FileID) проставляет писатель.
type objectPlan struct {
	key          string
	row          store.MetadataObject
	members      []store.MetadataMember
	formDecls    []store.Form
	scheduledJob *handlerPlan[store.ScheduledJob]
	role         *store.Role
	// commonModule: identity модуля общего модуля и его module_context.
	commonModule      *store.Module
	commonModuleProps string
}

// handlerPlan: строка с обработчиком, чей id символа ищется писателем по
// ключу. handlerKey пуст, если обработчик не разрешён.
type handlerPlan[T any] struct {
	row        T
	handlerKey string
}

// modulePlan: identity BSL-модуля, его символы и параметры.
type modulePlan struct {
	module  store.Module
	symbols []symbolPlan
	// dupDiagnostics: повторные объявления одного symbol_uid в файле.
	dupDiagnostics []domain.Diagnostic
	// methodKeys[i]: identity_key символа i-го метода mod.Methods. Повтор
	// имени получает ключ первого объявления: у них один symbol_uid.
	methodKeys []string
}

type symbolPlan struct {
	row    store.Symbol
	params []store.Parameter
}

// formStructurePlan: форма из Form.xml, её элементы и команды.
type formStructurePlan struct {
	key      string
	ownerKey string // ключ объекта-владельца, пусто если путь его не называет
	row      store.Form
	elements []store.FormElement
	commands []store.FormCommand
}

// planFile строит проход 1 файла. Ошибка только от сериализации свойств.
func planFile(pc planContext, rel string, rec *fileRecord) (*filePlan, error) {
	p := &filePlan{rel: rel, size: rec.size, mtimeNS: rec.mtimeNS, contentHash: rec.contentHash,
		diagnostics: rec.diagnostics, roleRights: rec.metaFacts.RoleRights != nil}
	if rec.metaFacts.Object != nil {
		op, err := planMetadataObject(pc, rel, rec)
		if err != nil {
			return nil, err
		}
		p.object = op
	}
	if rec.bslModule != nil {
		p.module = planModuleSymbols(pc, rel, rec)
	}
	if rec.metaFacts.FormStructure != nil {
		p.form = planFormStructure(pc, rel, rec)
	}
	if rec.metaFacts.Subscription != nil {
		p.subscription = planEventSubscription(pc, rec)
	}
	if svc := rec.metaFacts.HTTPService; svc != nil {
		p.httpEndpoints = planHTTPEndpoints(pc, svc)
	}
	return p, nil
}

// planHTTPEndpoints раскладывает HTTP-сервис в строки http_endpoint: по
// строке на метод шаблона, шаблон без методов строкой с пустым методом.
func planHTTPEndpoints(pc planContext, svc *meta.HTTPServiceFact) []store.HTTPEndpoint {
	var out []store.HTTPEndpoint
	for _, tpl := range svc.Templates {
		base := store.HTTPEndpoint{RootURL: svc.RootURL, TemplateName: tpl.NameDisplay,
			Template: tpl.Template, Layer: layerName(pc.layer)}
		if len(tpl.Methods) == 0 {
			out = append(out, base)
			continue
		}
		for _, m := range tpl.Methods {
			row := base
			row.MethodName, row.HTTPMethod, row.Handler = m.NameDisplay, m.HTTPMethod, m.Handler
			out = append(out, row)
		}
	}
	return out
}

func planMetadataObject(pc planContext, rel string, rec *fileRecord) (*objectPlan, error) {
	obj := rec.metaFacts.Object
	objKey := metadataObjectIdentityKey(pc.component, obj.MType, obj.NameNorm)
	propsJSON, err := json.Marshal(obj.Props)
	if err != nil {
		return nil, fmt.Errorf("props %s: %w", rel, err)
	}
	op := &objectPlan{key: objKey, row: store.MetadataObject{
		IdentityKey: objKey, ComponentID: string(pc.component), UUID: obj.UUID, MType: obj.MType,
		NameNorm: obj.NameNorm, NameDisplay: obj.NameDisplay, Synonym: obj.Synonym,
		PropsJSON: string(propsJSON), Layer: layerName(pc.layer),
	}}
	for _, m := range rec.metaFacts.Members {
		typesJSON, err := json.Marshal(m.Types)
		if err != nil {
			return nil, fmt.Errorf("member types %s: %w", rel, err)
		}
		op.members = append(op.members, store.MetadataMember{
			IdentityKey: metadataMemberIdentityKey(objKey, m), ComponentID: string(pc.component),
			Kind: m.Kind, NameNorm: m.NameNorm, NameDisplay: m.NameDisplay, TypesJSON: string(typesJSON),
		})
	}
	for _, fd := range rec.metaFacts.FormDecls {
		op.formDecls = append(op.formDecls, store.Form{
			IdentityKey: formIdentityKey(pc.component, fd.Key), ComponentID: string(pc.component),
			NameNorm: fd.NameNorm, NameDisplay: fd.NameDisplay,
		})
	}
	if obj.MType == "ScheduledJob" && rec.metaFacts.ScheduledJob != nil {
		j := rec.metaFacts.ScheduledJob
		uid, res := resolveModuleHandler(pc.env, j.MethodRaw)
		op.scheduledJob = &handlerPlan[store.ScheduledJob]{
			row: store.ScheduledJob{
				ComponentID: string(pc.component), NameNorm: j.NameNorm, NameDisplay: j.NameDisplay,
				MethodNameNorm: domain.NormalizeName(j.MethodRaw),
				Use:            j.Use, Predefined: j.Predefined,
				Resolution: string(res), Layer: layerName(pc.layer),
			},
			handlerKey: resolvedSymbolKey(uid, res),
		}
	}
	if obj.MType == "Role" && rec.metaFacts.Role != nil {
		op.role = &store.Role{
			ComponentID: string(pc.component), NameNorm: rec.metaFacts.Role.NameNorm,
			NameDisplay: rec.metaFacts.Role.NameDisplay, Layer: layerName(pc.layer),
		}
	}
	if obj.MType == "CommonModule" && rec.metaFacts.ModuleRegistry != nil {
		// Та же строка module, что напишет публикация Module.bsl этого общего
		// модуля: EnsureModule безусловный upsert по всем колонкам, поэтому
		// обе точки идут через moduleRecord (см. publishMetadataObject).
		m := moduleRecord(pc.component, commonModuleBSLPath(obj.NameDisplay), bsl.ModuleInfo{
			Kind: bsl.ModuleCommon, OwnerType: "CommonModules",
			OwnerName: obj.NameDisplay, OwnerNameNorm: obj.NameNorm,
		}, 0)
		props, err := json.Marshal(rec.metaFacts.ModuleRegistry)
		if err != nil {
			return nil, fmt.Errorf("module_context props %s: %w", rel, err)
		}
		op.commonModule, op.commonModuleProps = &m, string(props)
	}
	return op, nil
}

// resolvedSymbolKey: ключ узла символа-обработчика, только для разрешённого.
func resolvedSymbolKey(uid domain.SymbolUID, res domain.Resolution) string {
	if res != domain.ResolutionResolved {
		return ""
	}
	return symbolIdentityKey(uid)
}

func planModuleSymbols(pc planContext, rel string, rec *fileRecord) *modulePlan {
	mod := rec.bslModule
	mp := &modulePlan{module: moduleRecord(pc.component, rel, rec.moduleInfo, 0),
		methodKeys: make([]string, len(mod.Methods))}
	symbols := buildSymbols(pc.project, pc.component, pc.layer, rel, mod)
	// Тексты диагностик ниже те же, что были до плана (они уходят в store и
	// в ответ reindex), длинное тире записано escape-последовательностью.
	//
	// Дедупликация по symbol_uid внутри файла (см. publishModuleSymbols):
	// побеждает первое объявление, повтор даёт диагностику и не получает
	// своей строки symbol, его вызовы резолвятся на ту же identity.
	seen := make(map[domain.SymbolUID]bool, len(symbols))
	for i, sym := range symbols {
		key := symbolIdentityKey(sym.UID)
		if i < len(mod.Methods) {
			mp.methodKeys[i] = key
		}
		if seen[sym.UID] {
			mp.dupDiagnostics = append(mp.dupDiagnostics, domain.Diagnostic{
				Code: "index_duplicate_symbol_uid", Severity: domain.SeverityWarning,
				Message: fmt.Sprintf("имя %q объявлено в файле повторно (вероятно, взаимоисключающие ветки #Если) \u2014 учтено первое объявление", sym.NameDisplay),
				File:    rel, Span: sym.Span,
			})
			continue
		}
		seen[sym.UID] = true
		region, doc := symbolRegionAndDoc(mod, i)
		sp := symbolPlan{row: store.Symbol{
			IdentityKey: key, ComponentID: string(pc.component), UID: string(sym.UID),
			Kind: string(sym.Kind), NameNorm: sym.NameNorm,
			NameDisplay: sym.NameDisplay, IsExport: sym.Export, Directive: sym.Directive, IsAsync: sym.Async,
			Span: sym.Span, Signature: signatureOf(sym),
			DocFirstLine: doc, Region: region,
		}}
		for _, prm := range sym.Params {
			sp.params = append(sp.params, store.Parameter{
				Ord: prm.Index, Name: prm.NameDisplay, ByVal: prm.ByValue, DefaultExpr: prm.Default,
			})
		}
		mp.symbols = append(mp.symbols, sp)
	}
	return mp
}

func planFormStructure(pc planContext, rel string, rec *fileRecord) *formStructurePlan {
	fs := rec.metaFacts.FormStructure
	formKey := formIdentityKey(pc.component, fs.Key)
	fp := &formStructurePlan{key: formKey}
	if mtype, nameNorm, ok := formOwnerFromPath(rel); ok {
		fp.ownerKey = metadataObjectIdentityKey(pc.component, mtype, nameNorm)
	}
	nameDisplay := formNameFromPath(rel)
	fp.row = store.Form{IdentityKey: formKey, ComponentID: string(pc.component),
		NameNorm: domain.NormalizeName(nameDisplay), NameDisplay: nameDisplay}
	for i, e := range fs.Elements {
		fp.elements = append(fp.elements, store.FormElement{
			IdentityKey: formKey + "\x00element\x00" + itoaIndex(i) + "\x00" + e.NameNorm,
			ComponentID: string(pc.component),
			NameNorm:    e.NameNorm, NameDisplay: e.NameDisplay, EType: e.EType, DataPath: e.DataPath,
		})
	}
	for i, c := range fs.Commands {
		fp.commands = append(fp.commands, store.FormCommand{
			IdentityKey: formKey + "\x00command\x00" + itoaIndex(i) + "\x00" + c.NameNorm,
			ComponentID: string(pc.component),
			NameNorm:    c.NameNorm, NameDisplay: c.NameDisplay, ActionNorm: c.ActionNorm,
		})
	}
	return fp
}

func planEventSubscription(pc planContext, rec *fileRecord) *handlerPlan[store.EventSubscription] {
	s := rec.metaFacts.Subscription
	uid, res := resolveModuleHandler(pc.env, s.HandlerRaw)
	sourceKind, sourceNameNorm := "", ""
	if len(s.Sources) > 0 {
		sourceKind = string(s.Sources[0].Kind)
		sourceNameNorm = domain.NormalizeName(s.Sources[0].Name)
	}
	return &handlerPlan[store.EventSubscription]{
		row: store.EventSubscription{
			ComponentID: string(pc.component), NameNorm: s.NameNorm, NameDisplay: s.NameDisplay,
			SourceKind: sourceKind, SourceNameNorm: sourceNameNorm, Event: s.Event,
			HandlerNameNorm: domain.NormalizeName(s.HandlerRaw),
			Resolution:      string(res), Layer: layerName(pc.layer),
		},
		handlerKey: resolvedSymbolKey(uid, res),
	}
}

// linkPlan: проход 2 одного файла, ссылки и производные факты.
type linkPlan struct {
	rel string

	// Владелец модуля (publishModuleOwner): либо диагностика неизвестной
	// коллекции, либо ключ объекта-владельца, либо ничего.
	ownerDiagnostic *domain.Diagnostic
	ownerKey        string
	ownerModule     store.Module

	refs           []refPlan
	registerAccess []registerAccessPlan
	httpCalls      []httpCallPlan
	queries        []queryPlan
	handlers       *handlerBindingsPlan
	roleRights     *roleRightsPlan
}

// refPlan: одна ссылка. Разрешена ли цель-символ, решает писатель: узла цели
// может не оказаться в store, и тогда ссылка честно становится unresolved.
type refPlan struct {
	callerKey  string // ключ символа метода-владельца, пусто вне метода
	row        store.Reference
	targetKey  string // ключ символа цели для resolved/symbol, иначе пусто
	candidates []candidatePlan
	consulted  []resolve.KeyHash
	callKind   string // вид ребра вызова (пусто: ребра нет, если нет владельца)
}

type candidatePlan struct {
	key    string
	rank   int
	reason string
}

type registerAccessPlan struct {
	symbolKey string
	objectKey string // пусто, если объект не разрешён
	row       store.RegisterAccess
}

type httpCallPlan struct {
	symbolKey string // ключ метода вызова; пусто вне метода
	row       store.HTTPCall
}

type queryPlan struct {
	row  store.Query // SymbolID и FileID ставит писатель
	refs []queryRefPlan
	// symbolKey: ключ метода-владельца. Запрос вне метода не публикуется.
	symbolKey string
}

type queryRefPlan struct {
	objectKey string
	memberKey string
	row       store.QueryReference
}

type handlerBindingsPlan struct {
	formKey  string
	bindings []handlerPlan[store.HandlerBinding]
}

type roleRightsPlan struct {
	role    store.Role
	objects []roleObjectPlan
}

type roleObjectPlan struct {
	objectKey string // пусто, если имя объекта не разбирается
	rights    []store.RoleRight
}

// planLinks строит проход 2 файла. Чистая функция, как и planFile.
func planLinks(pc planContext, rel string, rec *fileRecord, methodKeys []string) *linkPlan {
	lp := &linkPlan{rel: rel}
	if rec.bslModule != nil {
		planModuleOwner(pc, rel, rec, lp)
		lp.refs = planModuleReferences(pc, rel, rec, methodKeys)
		lp.registerAccess = planRegisterAccess(pc, rec, methodKeys)
		lp.httpCalls = planHTTPCalls(pc, rec, methodKeys)
		lp.queries = planQueries(pc, rel, rec, methodKeys)
	}
	if fs := rec.metaFacts.FormStructure; fs != nil && len(fs.Handlers) > 0 {
		formModulePath := domain.NormalizeModulePath(strings.TrimSuffix(rel, "Form.xml") + "Form/Module.bsl")
		hp := &handlerBindingsPlan{formKey: formIdentityKey(pc.component, fs.Key)}
		for _, r := range resolve.DeriveHandlerBinding(formModulePath, fs.Handlers, pc.env) {
			hp.bindings = append(hp.bindings, handlerPlan[store.HandlerBinding]{
				row: store.HandlerBinding{Source: r.Source, Event: r.Event,
					HandlerNameNorm: r.HandlerNameNorm, Resolution: string(r.Resolution)},
				handlerKey: resolvedSymbolKey(r.HandlerUID, r.Resolution),
			})
		}
		lp.handlers = hp
	}
	if rr := rec.metaFacts.RoleRights; rr != nil {
		rp := &roleRightsPlan{role: store.Role{
			ComponentID: string(pc.component), NameNorm: rr.RoleNameNorm,
			NameDisplay: rr.RoleNameDisplay, Layer: layerName(pc.layer),
		}}
		for _, obj := range rr.Objects {
			op := roleObjectPlan{objectKey: roleObjectKey(pc.component, obj.ObjectNameRaw)}
			for _, right := range obj.Rights {
				op.rights = append(op.rights, store.RoleRight{
					ObjectNameNorm: domain.NormalizeName(obj.ObjectNameRaw),
					RightName:      right.Name, Value: right.Value, RLS: rlsToText(right.RLS),
					SetForNewObject: rr.SetForNewObjects,
				})
			}
			rp.objects = append(rp.objects, op)
		}
		lp.roleRights = rp
	}
	return lp
}

func planModuleOwner(pc planContext, rel string, rec *fileRecord, lp *linkPlan) {
	info := rec.moduleInfo
	if info.OwnerNameNorm == "" {
		return
	}
	mtype, known := ownerTypeToMType(info.OwnerType)
	if !known {
		lp.ownerDiagnostic = &domain.Diagnostic{
			Code: "index_module_owner_unknown_collection", Severity: domain.SeverityWarning,
			Message: fmt.Sprintf("коллекция выгрузки %q не переводится в вид объекта метаданных \u2014 владелец модуля не определён", info.OwnerType),
			File:    rel,
		}
		return
	}
	lp.ownerKey = metadataObjectIdentityKey(pc.component, mtype, info.OwnerNameNorm)
	lp.ownerModule = moduleRecord(pc.component, rel, info, 0)
}

func methodKey(methodKeys []string, m int) string {
	if m >= 0 && m < len(methodKeys) {
		return methodKeys[m]
	}
	return ""
}

func planModuleReferences(pc planContext, rel string, rec *fileRecord, methodKeys []string) []refPlan {
	refs := pc.resolved[rel]
	if len(refs) == 0 {
		return nil
	}
	methodOf := refCallMethodIndexes(rec.bslModule)
	out := make([]refPlan, 0, len(refs))
	for i, rr := range refs {
		var rp refPlan
		if i < len(methodOf) {
			rp.callerKey = methodKey(methodKeys, methodOf[i])
		}
		rp.row = store.Reference{
			Kind: "call", QualifierNorm: rr.raw.QualifierNorm, NameNorm: rr.raw.NameNorm,
			Resolution: string(rr.result.Resolution), Confidence: float64(rr.result.Confidence),
			Layer: layerName(pc.layer), Span: rr.raw.Span,
		}
		if rr.result.Resolution == domain.ResolutionResolved {
			switch rr.result.TargetClass {
			case domain.TargetSymbol:
				rp.targetKey = symbolIdentityKey(rr.result.TargetUID)
			case domain.TargetPlatform:
				rp.row.TargetClass = "platform"
				rp.row.PlatformKey = rr.result.TargetKey
			}
		}
		for ci, c := range rr.result.Candidates {
			if c.TargetClass != domain.TargetSymbol {
				continue
			}
			rp.candidates = append(rp.candidates, candidatePlan{key: symbolIdentityKey(c.TargetUID), rank: ci + 1, reason: c.Reason})
		}
		rp.consulted = rr.result.ConsultedKeys
		kind := rr.result.CallKind
		if kind == "" {
			// FormQualifiedModule на неопознанном квалификаторе не
			// проставляет CallKind (см. publishModuleReferences).
			kind = resolve.CallDynamic
		}
		rp.callKind = string(kind)
		out = append(out, rp)
	}
	return out
}

func planRegisterAccess(pc planContext, rec *fileRecord, methodKeys []string) []registerAccessPlan {
	results := resolve.DeriveRegisterAccess(rec.bslModule, pc.env)
	if len(results) == 0 {
		return nil
	}
	out := make([]registerAccessPlan, 0, len(results))
	for i, ra := range results {
		var p registerAccessPlan
		if i < len(rec.bslModule.RegisterAccesses) {
			p.symbolKey = methodKey(methodKeys, rec.bslModule.RegisterAccesses[i].Method)
		}
		if ra.ObjectResolved {
			p.objectKey = ra.ObjectKey
		}
		p.row = store.RegisterAccess{
			RegisterNameNorm: ra.RegisterNameNorm, Mode: string(ra.Mode),
			Static: ra.Static, Confidence: float64(ra.Confidence), Span: ra.Span,
			Layer: layerName(pc.layer),
		}
		out = append(out, p)
	}
	return out
}

func planHTTPCalls(pc planContext, rec *fileRecord, methodKeys []string) []httpCallPlan {
	calls := rec.bslModule.HTTPCalls
	if len(calls) == 0 {
		return nil
	}
	out := make([]httpCallPlan, len(calls))
	for i, c := range calls {
		out[i] = httpCallPlan{symbolKey: methodKey(methodKeys, c.Method), row: store.HTTPCall{
			HTTPTarget: c.HTTPTarget, Confidence: float64(c.Confidence), Span: c.Span,
			Layer: layerName(pc.layer),
		}}
	}
	return out
}

func planQueries(pc planContext, rel string, rec *fileRecord, methodKeys []string) []queryPlan {
	mod := rec.bslModule
	if len(mod.Queries) == 0 {
		return nil
	}
	out := make([]queryPlan, len(mod.Queries))
	for i, ql := range mod.Queries {
		out[i] = queryPlan{symbolKey: methodKey(methodKeys, ql.Method), row: store.Query{
			IdentityKey: fmt.Sprintf("%s\x00query\x00%d", moduleIdentityKey(pc.component, rel), i),
			ComponentID: string(pc.component),
			Span:        ql.Span, Staticity: string(ql.Staticity), Text: string(mod.Text(ql.Span)),
			Confidence: float64(ql.Confidence),
		}}
	}
	for _, g := range resolve.DeriveQueryReference(mod, pc.env) {
		if g.LiteralIndex < 0 || g.LiteralIndex >= len(out) {
			continue
		}
		q := &out[g.LiteralIndex]
		for _, r := range g.References {
			qr := queryRefPlan{row: store.QueryReference{
				Kind: string(r.Kind), NameNorm: r.NameNorm,
				SpanStart: int64(r.Span.StartByte), SpanEnd: int64(r.Span.EndByte),
			}}
			if r.ObjectResolved {
				qr.objectKey = r.ObjectKey
			}
			if r.MemberResolved {
				qr.memberKey = r.MemberKey
			}
			q.refs = append(q.refs, qr)
		}
	}
	return out
}

// roleObjectKey: ключ объекта из полного имени права роли («Справочник.Х» в
// английской форме выгрузки, «Catalog.X»). Пусто, если имя не разбирается.
func roleObjectKey(component domain.ComponentID, objectFullName string) string {
	parts := strings.SplitN(objectFullName, ".", 2)
	if len(parts) < 2 {
		return ""
	}
	return metadataObjectIdentityKey(component, parts[0], domain.NormalizeName(parts[1]))
}
