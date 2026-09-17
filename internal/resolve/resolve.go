package resolve

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Диагностики этого пакета (архитектура §19.1).
const (
	// DiagNotExported — квалифицированный вызов попал в неэкспортный символ
	// общего модуля: находка, а не потерянная ссылка (ref остаётся resolved).
	DiagNotExported = "not-exported"
	// DiagContextMismatch — вызов недоступен в контексте цели (клиент зовёт
	// Server-only модуль без ServerCall и т.п.): статическое приближение,
	// ребро не блокируется.
	DiagContextMismatch = "context-mismatch"
)

// Фиксированные уровни confidence для случаев, где факт не от точного
// парсера, а вывод резолвера над контекстом (упрощение: фиксированные
// уровни, не формула — тот же приём, что parse/bsl.ConfidenceRegisterDirect
// и parse/query.confidencePartial).
const (
	// ConfidenceDynamic — имя вычисляется в рантайме (§19.1 п.5): вызов
	// через переменную, квалификатор не опознан как общий модуль компонента.
	ConfidenceDynamic domain.Confidence = 0.3
	// ConfidenceContextMismatch — цель найдена статически точно, но
	// недоступна в контексте вызывающего: сама находка точна, а вывод о
	// несовместимости — статическое приближение.
	ConfidenceContextMismatch domain.Confidence = 0.7
)

// CallEdgeKind — вид ребра графа вызовов (архитектура §15/§19.1).
type CallEdgeKind string

const (
	CallLocal        CallEdgeKind = "local"
	CallCommonModule CallEdgeKind = "common-module"
	CallGlobalCommon CallEdgeKind = "global-common"
	CallManager      CallEdgeKind = "manager"
	CallPlatform     CallEdgeKind = "platform"
	CallDynamic      CallEdgeKind = "dynamic"
)

// Candidate — один кандидат ambiguous-разрешения (reference_candidate).
type Candidate struct {
	TargetClass domain.TargetClass
	TargetUID   domain.SymbolUID // заполнен при TargetClass == symbol
	TargetKey   string           // заполнен при TargetClass != symbol
	Rank        int
	Reason      string
}

// Result — исход Resolve: состояние разрешения плюс всё, что из него следует
// для reference/call_edge/reference_candidate/resolution_dep. Строится как
// значение (не сохраняет ссылок на Env), собирается в store-строки таском 09.
type Result struct {
	Resolution  domain.Resolution
	TargetClass domain.TargetClass
	TargetUID   domain.SymbolUID // при TargetClass == symbol
	TargetKey   string           // при TargetClass в {metadata, platform}
	Confidence  domain.Confidence

	// CallKind — вид ребра графа вызовов для этой ссылки. Пуст, если raw —
	// не форма вызова (не используется вне DeriveCallEdges).
	CallKind CallEdgeKind

	// Candidates заполнен ТОЛЬКО при Resolution == Ambiguous (минимум 2).
	Candidates []Candidate

	Diagnostics []domain.Diagnostic

	// ConsultedKeys — ВСЕ ключи разрешения, которые проверялись при разборе
	// этой ссылки, включая пустые результаты (§18.4): основа resolution_dep.
	ConsultedKeys []KeyHash
}

// Resolve разрешает одну сырую ссылку по порядку §19.1 (пять пунктов).
// Чистая функция: результат зависит только от raw и env.
func Resolve(raw RawRef, env Env) Result {
	switch raw.Form {
	case FormQualifiedManager:
		return resolveManagerCall(raw, env)
	case FormQualifiedModule:
		return resolveModuleCall(raw, env)
	default:
		return resolveUnqualified(raw, env)
	}
}

// resolveManagerCall — §19.1 п.2: Справочники.X.Метод( и синонимы. Только
// экспортные символы менеджерного модуля X.
func resolveManagerCall(raw RawRef, env Env) Result {
	res := Result{Confidence: domain.ConfidenceExact}
	key := resolutionKey{
		Component: env.component, Layer: env.layer, Scope: ScopeManagerModule,
		Owner: raw.ManagerMType + "\x00" + raw.ManagerObjectNameNorm, Name: raw.NameNorm,
	}
	res.ConsultedKeys = []KeyHash{key.Hash()}

	mod, ok := env.ManagerModule(raw.ManagerMType, raw.ManagerObjectNameNorm)
	if !ok {
		res.Resolution = domain.ResolutionUnresolved
		return res
	}

	var matches []domain.Symbol
	for _, s := range mod.Symbols {
		if !s.Export || !isCallable(s.Kind) || s.NameNorm != raw.NameNorm {
			continue
		}
		matches = append(matches, s)
	}
	matches = dedupeByUID(matches)
	switch len(matches) {
	case 0:
		res.Resolution = domain.ResolutionUnresolved
	case 1:
		res.Resolution = domain.ResolutionResolved
		res.TargetClass = domain.TargetSymbol
		res.TargetUID = matches[0].UID
		res.CallKind = CallManager
	default:
		res.Resolution = domain.ResolutionAmbiguous
		res.Candidates = candidatesFromSymbols(matches, "manager-module-duplicate-export")
	}
	return res
}

// resolveModuleCall — §19.1 п.1: Q.Метод( с одной точкой. Если Q не
// опознан как общий модуль компонента, это не рождает unresolved: такая
// синтаксическая форма покрывает и вызов метода переменной/объекта
// (Результат.Количество()) — п.5, dynamic.
func resolveModuleCall(raw RawRef, env Env) Result {
	mod, ok := env.CommonModuleByName(raw.QualifierNorm)
	if !ok {
		return Result{Resolution: domain.ResolutionDynamic, Confidence: ConfidenceDynamic}
	}

	res := Result{Confidence: domain.ConfidenceExact}
	key := resolutionKey{
		Component: env.component, Layer: env.layer, Scope: ScopeModuleExport,
		Owner: raw.QualifierNorm, Name: raw.NameNorm,
	}
	res.ConsultedKeys = []KeyHash{key.Hash()}

	var matches []domain.Symbol
	for _, s := range mod.Symbols {
		if !isCallable(s.Kind) || s.NameNorm != raw.NameNorm {
			continue
		}
		matches = append(matches, s)
	}
	matches = dedupeByUID(matches)
	switch len(matches) {
	case 0:
		res.Resolution = domain.ResolutionUnresolved
		return res
	case 1:
		sym := matches[0]
		res.Resolution = domain.ResolutionResolved
		res.TargetClass = domain.TargetSymbol
		res.TargetUID = sym.UID
		res.CallKind = CallCommonModule
		if !sym.Export {
			res.Diagnostics = append(res.Diagnostics, notExportedDiagnostic(raw, env))
		}
		callerCtx := callerContext(raw.CallerDirective, raw.CallerModuleKind)
		availClient, availServer := commonModuleAvailability(mod.Registry)
		if !contextAvailable(callerCtx, availClient, availServer) {
			res.Diagnostics = append(res.Diagnostics, contextMismatchDiagnostic(raw, env, callerCtx))
			res.Confidence = ConfidenceContextMismatch
		}
		return res
	default:
		res.Resolution = domain.ResolutionAmbiguous
		res.CallKind = CallCommonModule
		res.Candidates = candidatesFromSymbols(matches, "common-module-duplicate-method")
		return res
	}
}

// resolveUnqualified — §19.1 п.3-5: местный модуль -> глобальные общие
// модули (по контексту) -> platform builtins -> unresolved.
func resolveUnqualified(raw RawRef, env Env) Result {
	res := Result{Confidence: domain.ConfidenceExact}
	callerCtx := callerContext(raw.CallerDirective, raw.CallerModuleKind)

	// (а) местный модуль — не консультирует ничего дальше при находке:
	// затенение локальным символом абсолютно, как в самом языке.
	localKey := resolutionKey{
		Component: env.component, Layer: env.layer, Scope: ScopeLocalModule,
		Owner: raw.CallerModulePath, Name: raw.NameNorm,
	}
	res.ConsultedKeys = append(res.ConsultedKeys, localKey.Hash())
	if mod, ok := env.Module(raw.CallerModulePath); ok {
		var matches []domain.Symbol
		for _, s := range mod.Symbols {
			if isCallable(s.Kind) && s.NameNorm == raw.NameNorm {
				matches = append(matches, s)
			}
		}
		matches = dedupeByUID(matches)
		switch len(matches) {
		case 1:
			res.Resolution = domain.ResolutionResolved
			res.TargetClass = domain.TargetSymbol
			res.TargetUID = matches[0].UID
			res.CallKind = CallLocal
			return res
		default:
			if len(matches) > 1 {
				res.Resolution = domain.ResolutionAmbiguous
				res.CallKind = CallLocal
				res.Candidates = candidatesFromSymbols(matches, "local-module-duplicate-method")
				return res
			}
		}
	}

	// (б) глобальные общие модули, отфильтрованные по контексту вызова.
	globalKey := resolutionKey{Component: env.component, Layer: env.layer, Scope: ScopeGlobalCommon, Name: raw.NameNorm}
	res.ConsultedKeys = append(res.ConsultedKeys, globalKey.Hash())
	var globalMatches []domain.Symbol
	var globalReasons []string
	for _, gm := range env.GlobalModules() {
		availClient, availServer := commonModuleAvailability(gm.Registry)
		if !contextAvailable(callerCtx, availClient, availServer) {
			continue
		}
		for _, s := range gm.Symbols {
			if s.Export && isCallable(s.Kind) && s.NameNorm == raw.NameNorm {
				globalMatches = append(globalMatches, s)
				globalReasons = append(globalReasons, "global-common-module-name-collision:"+gm.NameNorm)
			}
		}
	}
	if len(globalMatches) == 1 {
		res.Resolution = domain.ResolutionResolved
		res.TargetClass = domain.TargetSymbol
		res.TargetUID = globalMatches[0].UID
		res.CallKind = CallGlobalCommon
		return res
	}
	if len(globalMatches) > 1 {
		res.Resolution = domain.ResolutionAmbiguous
		res.CallKind = CallGlobalCommon
		for i, s := range globalMatches {
			res.Candidates = append(res.Candidates, Candidate{TargetClass: domain.TargetSymbol, TargetUID: s.UID, Rank: i + 1, Reason: globalReasons[i]})
		}
		return res
	}

	// (в) platform builtins.
	platformKey := resolutionKey{Component: env.component, Layer: env.layer, Scope: ScopePlatform, Name: raw.NameNorm}
	res.ConsultedKeys = append(res.ConsultedKeys, platformKey.Hash())
	if entry, ok := env.lookupBuiltin(raw.NameNorm); ok && builtinAvailable(callerCtx, entry) {
		res.Resolution = domain.ResolutionResolved
		res.TargetClass = domain.TargetPlatform
		res.TargetKey = platformKeyFor(entry)
		res.CallKind = CallPlatform
		return res
	}

	// (г) ничего не подошло.
	res.Resolution = domain.ResolutionUnresolved
	// упрощение: для исчерпанного unqualified-поиска (проверены местный
	// модуль, глобальные общие модули и platform — все безрезультатно)
	// call_edge.kind фиксированного значения в §15 нет; ближайший
	// применимый — global-common (самая широкая из проверенных областей).
	res.CallKind = CallGlobalCommon
	return res
}

func isCallable(k domain.SymbolKind) bool {
	return k == domain.SymbolProcedure || k == domain.SymbolFunction
}

// dedupeByUID схлопывает символы с одинаковым uid, оставляя первый по тексту
// файла. Нужен там, где кандидаты набираются из ОДНОГО модуля: uid =
// hash(project, component, module_path, name_norm) и span в него сознательно
// не входит (§14), поэтому два объявления одного имени под взаимоисключающими
// ветками #Если/#Иначе (обычное и управляемое приложение, толстый и веб-клиент)
// дают ДВА символа парсера и ОДИН символ индекса — publish уже оставляет
// первое объявление и пишет index_duplicate_symbol_uid.
//
// Резолвер обязан видеть ту же картину. Иначе вызов такого имени становится
// ambiguous с двумя кандидатами на одну и ту же строку symbol, и публикация
// падает на UNIQUE constraint reference_candidate(ref_id, target_node_id):
// найдено на реальной выгрузке: функция общей формы, объявленная
// дважды под «#Если Не ВебКлиент Тогда ... #Иначе».
func dedupeByUID(symbols []domain.Symbol) []domain.Symbol {
	if len(symbols) < 2 {
		return symbols
	}
	seen := make(map[domain.SymbolUID]bool, len(symbols))
	out := symbols[:0:0]
	for _, s := range symbols {
		if seen[s.UID] {
			continue
		}
		seen[s.UID] = true
		out = append(out, s)
	}
	return out
}

func candidatesFromSymbols(symbols []domain.Symbol, reason string) []Candidate {
	out := make([]Candidate, 0, len(symbols))
	for i, s := range symbols {
		out = append(out, Candidate{TargetClass: domain.TargetSymbol, TargetUID: s.UID, Rank: i + 1, Reason: reason})
	}
	return out
}

func notExportedDiagnostic(raw RawRef, env Env) domain.Diagnostic {
	return domain.Diagnostic{
		Code:      DiagNotExported,
		Severity:  domain.SeverityWarning,
		Message:   "вызов " + raw.Qualifier + "." + raw.NameDisplay + " ведёт на неэкспортный метод",
		Component: env.component,
		File:      raw.CallerModulePath,
		Span:      raw.Span,
	}
}

func contextMismatchDiagnostic(raw RawRef, env Env, callerCtx CallContext) domain.Diagnostic {
	return domain.Diagnostic{
		Code:      DiagContextMismatch,
		Severity:  domain.SeverityWarning,
		Message:   "вызов " + raw.Qualifier + "." + raw.NameDisplay + " недоступен в контексте вызывающего (" + string(callerCtx) + ")",
		Component: env.component,
		File:      raw.CallerModulePath,
		Span:      raw.Span,
	}
}

// platformKeyFor строит идентичность builtin для reference.platform_key:
// русское имя (канонический вид справочника платформы) в NormalizeName, с
// откатом на английское, если русского почему-то нет.
func platformKeyFor(e builtinEntry) string {
	name := e.NameRu
	if name == "" {
		name = e.NameEn
	}
	return "platform:" + domain.NormalizeName(name)
}
