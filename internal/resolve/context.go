package resolve

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// CallContext — контекст исполнения кода: клиент, сервер, оба (директива
// "&НаКлиентеНаСервере..." или отсутствие директивы в общем модуле) или
// неизвестно (нет данных — резолвер не блокирует по неизвестному контексту).
type CallContext string

const (
	ContextClient  CallContext = "client"
	ContextServer  CallContext = "server"
	ContextBoth    CallContext = "both"
	ContextUnknown CallContext = ""
)

// directiveContext переводит директиву компиляции метода (как она хранится
// в bsl.Method.Directive, ровно как написана в исходнике — русский и
// английский синтаксис) в контекст вызывающего. Пустая директива у метода
// без директивы обрабатывается отдельно, через вид модуля (callerContext).
func directiveContext(directive string) CallContext {
	d := strings.ToLower(strings.TrimSpace(directive))
	d = strings.TrimPrefix(d, "&")
	switch d {
	case "насервере", "atserver", "насерверебезконтекста", "atservernocontext":
		return ContextServer
	case "наклиенте", "atclient":
		return ContextClient
	case "наклиентенасерверебезконтекста", "atclientatservernocontext",
		"наклиентенасервере", "atclientatserver":
		return ContextBoth
	default:
		return ContextUnknown
	}
}

// serverOnlyModuleKinds — виды модулей, весь код которых исполняется
// исключительно на сервере (§19.1: «вид модуля — модуль объекта/менеджера/
// набора = сервер»), директив компиляции у процедур в них не бывает.
var serverOnlyModuleKinds = map[bsl.ModuleKind]bool{
	bsl.ModuleObject:             true,
	bsl.ModuleManager:            true,
	bsl.ModuleRecordSet:          true,
	bsl.ModuleValueManager:       true,
	bsl.ModuleSession:            true,
	bsl.ModuleExternalConnection: true,
}

// clientOnlyModuleKinds — виды модулей без директив, исполняемые на клиенте.
// Внутри модуля управляемого приложения отдельные процедуры МОГУТ нести
// &НаСервере — тогда directiveContext уже отработал раньше и сюда не дойдёт.
var clientOnlyModuleKinds = map[bsl.ModuleKind]bool{
	bsl.ModuleCommand:     true,
	bsl.ModuleApplication: true,
	bsl.ModuleForm:        true,
}

// callerContext определяет контекст вызывающего кода: по директиве метода,
// если она есть, иначе по виду модуля.
func callerContext(directive string, moduleKind bsl.ModuleKind) CallContext {
	if ctx := directiveContext(directive); ctx != ContextUnknown {
		return ctx
	}
	if serverOnlyModuleKinds[moduleKind] {
		return ContextServer
	}
	if clientOnlyModuleKinds[moduleKind] {
		return ContextClient
	}
	return ContextUnknown
}

// commonModuleAvailability сообщает, в каком контексте общий модуль
// доступен для ВЫЗОВА кодом из этого же компонента — по его собственным
// свойствам (parse/meta.ModuleRegistryFact, §19.1). ServerCall — «вызов
// сервера»: включает клиентскую доступность server-модуля без клиентских
// флагов (клиент вызывает его как RPC).
func commonModuleAvailability(reg *meta.ModuleRegistryFact) (client, server bool) {
	if reg == nil {
		return true, true // нет данных о свойствах — не блокируем разрешение
	}
	client = reg.ClientManagedApplication || reg.ClientOrdinaryApplication || reg.ServerCall
	server = reg.Server
	return client, server
}

// contextAvailable сообщает, доступна ли цель с данной доступностью
// (client/server) вызывающему в данном контексте. ContextUnknown и
// ContextBoth не блокируют ничего — при отсутствии данных о контексте
// резолвер не выдумывает диагностику.
func contextAvailable(caller CallContext, availClient, availServer bool) bool {
	switch caller {
	case ContextClient:
		return availClient
	case ContextServer:
		return availServer
	default:
		return true
	}
}

// builtinAvailable проверяет доступность platform-метода в контексте
// вызывающего по тексту Availability (свободный русский текст справочника
// платформы, internal/syntax): подстроки «клиент»/«сервер». При
// ContextUnknown/ContextBoth или пустой Availability ограничение не
// применяется — это единственный источник этих данных, а не эвристика по
// имени.
func builtinAvailable(caller CallContext, e builtinEntry) bool {
	if caller != ContextClient && caller != ContextServer {
		return true
	}
	avail := strings.ToLower(e.Availability)
	if avail == "" {
		return true
	}
	switch caller {
	case ContextClient:
		return strings.Contains(avail, "клиент")
	case ContextServer:
		return strings.Contains(avail, "сервер")
	default:
		return true
	}
}
