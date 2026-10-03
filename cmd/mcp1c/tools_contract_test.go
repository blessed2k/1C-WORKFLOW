package main

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// Characterization/contract-тесты инструментов, зарегистрированных сегодня.
//
// Строгость D6 (docs/architecture-index.md §29): имя, входная схема (состав обязательных полей и
// типы), выходная схема, семантика полей, поведение при ошибке, порядок элементов.
// Новое поле допускается только как optional: список обязательных сверяется точно,
// а перечисленные поля — на присутствие, поэтому добавление необязательного поля
// тест не роняет, а изменение обязательности или типа — роняет.
//
// Таблица ниже — снимок, снятый с самого сервера (`newServer`) через `tools/list`,
// а не список по памяти: тест TestКонтрактСоставИнструментов сверяет её с реестром
// в обе стороны, поэтому новый или удалённый инструмент не может пройти незамеченным.

type режимИнструмента int

const (
	режимОффлайн режимИнструмента = 1 << iota
	режимLive
)

// контрактИнструмента — зафиксированный контракт одного инструмента.
type контрактИнструмента struct {
	имя               string
	режимы            режимИнструмента
	входОбязательные  []string
	входПоля          map[string]string
	выходОбязательные []string
	выходПоля         []string
}

var контрактыИнструментов = []контрактИнструмента{
	{
		имя:               "access_diagnose",
		режимы:            режимLive,
		входОбязательные:  []string{"user"},
		входПоля:          map[string]string{"base": "string", "object": "string", "user": "string"},
		выходОбязательные: []string{"base", "user", "steps", "verdict"},
		выходПоля:         []string{"base", "caveat", "nextStep", "object", "profiles", "roleNote", "roles", "steps", "user", "verdict"},
	},
	{
		имя:               "access_profiles",
		режимы:            режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"base": "string", "diff": "array", "includeRoles": "boolean", "profile": "string", "user": "string"},
		выходОбязательные: []string{"base", "settings"},
		выходПоля:         []string{"base", "diff", "note", "profiles", "settings", "user"},
	},
	{
		имя:               "analyze_query",
		режимы:            режимLive,
		входОбязательные:  []string{"text"},
		входПоля:          map[string]string{"base": "string", "text": "string"},
		выходОбязательные: []string{"base", "warnings", "count"},
		выходПоля:         []string{"base", "count", "warnings"},
	},
	{
		имя:               "bsl_syntax",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"limit": "integer", "owner": "string", "queries": "array", "query": "string"},
		выходОбязательные: []string{"count"},
		выходПоля:         []string{"count", "matches", "members", "note", "owner", "query", "results", "total", "truncated", "type"},
	},
	{
		имя:               "bsp_extension_points",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"query"},
		входПоля:          map[string]string{"limit": "integer", "query": "string"},
		выходОбязательные: []string{"query", "count", "points", "note"},
		выходПоля:         []string{"count", "note", "points", "query"},
	},
	{
		имя:               "check_sync",
		режимы:            режимLive,
		входОбязательные:  []string{"dumpPath"},
		входПоля:          map[string]string{"dumpPath": "string"},
		выходОбязательные: []string{"inSync", "configuration", "liveVersion", "dumpVersion", "versionMatch"},
		выходПоля:         []string{"configuration", "dumpVersion", "inSync", "liveVersion", "notComparedTypes", "note", "typeDiffs", "versionMatch"},
	},
	{
		имя:               "command_visibility",
		режимы:            режимОффлайн,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"all": "boolean", "role": "string", "rolesOnly": "boolean", "subsystem": "string"},
		выходОбязательные: []string{"commands", "count"},
		выходПоля:         []string{"commands", "count", "note", "role", "rolesMentioned", "subsystem"},
	},
	{
		имя:               "context_pack",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"name": "string", "type": "string", "withForms": "boolean", "withQuerySchema": "boolean"},
		выходОбязательные: []string{"object", "structure"},
		выходПоля:         []string{"forms", "modules", "object", "querySchema", "structure", "usages"},
	},
	{
		имя:               "data_health",
		режимы:            режимLive,
		входОбязательные:  []string{"objects"},
		входПоля:          map[string]string{"base": "string", "objects": "array"},
		выходОбязательные: []string{"base", "objects"},
		выходПоля:         []string{"base", "note", "objects"},
	},
	{
		имя:               "dump_diff",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"dumpB"},
		входПоля:          map[string]string{"dumpA": "string", "dumpB": "string", "withFormat": "boolean"},
		выходОбязательные: []string{"dumpA", "dumpB", "onlyInA", "onlyInB", "commonCount"},
		выходПоля:         []string{"commonCount", "configurationA", "configurationB", "dumpA", "dumpB", "format", "note", "onlyInA", "onlyInB"},
	},
	{
		имя:               "exchange_audit",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"objectType", "name"},
		входПоля:          map[string]string{"name": "string", "objectType": "string"},
		выходОбязательные: []string{"object", "inPlans"},
		выходПоля:         []string{"findings", "inPlans", "notInPlans", "object", "registrars"},
	},
	{
		имя:               "execute_query",
		режимы:            режимLive,
		входОбязательные:  []string{"text"},
		входПоля:          map[string]string{"base": "string", "limit": "integer", "params": "object", "text": "string"},
		выходОбязательные: []string{"base", "columns", "rows", "count"},
		выходПоля:         []string{"base", "columns", "count", "rows", "truncated"},
	},
	{
		имя:               "extension_context",
		режимы:            режимОффлайн,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"baseDump": "string"},
		выходОбязательные: []string{"extension", "interceptors"},
		выходПоля:         []string{"adopted", "extension", "interceptors", "own", "prefix", "purpose"},
	},
	{
		имя:               "find_api",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"query"},
		входПоля:          map[string]string{"query": "string", "limit": "integer"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "find_dependency_paths",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"fromType", "fromName", "toType", "toName"},
		входПоля:          map[string]string{"fromName": "string", "fromType": "string", "maxDepth": "integer", "maxPaths": "integer", "toName": "string", "toType": "string"},
		выходОбязательные: []string{"from", "to", "maxDepth", "found"},
		выходПоля:         []string{"found", "from", "maxDepth", "note", "paths", "to"},
	},
	{
		имя:               "find_impact",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"symbolUid": "string", "objectType": "string", "objectName": "string", "component": "string", "kinds": "array", "depth": "integer", "budget": "integer", "view": "string", "cursor": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "find_metadata_usages",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"name": "string", "type": "string", "withTemplates": "boolean"},
		выходОбязательные: []string{"object", "total"},
		выходПоля:         []string{"asType", "inRoles", "inTemplates", "object", "templatesNote", "total"},
	},
	{
		имя:               "find_queries_using",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"type": "string", "name": "string", "field": "string", "component": "string", "limit": "integer", "cursor": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "find_references",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"uid"},
		входПоля:          map[string]string{"uid": "string", "kinds": "array", "component": "string", "limit": "integer", "cursor": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "find_register_writes",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"register"},
		входПоля:          map[string]string{"register": "string", "modes": "string", "component": "string", "symbol": "string", "minConfidence": "number", "view": "string", "limit": "integer", "cursor": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "find_symbol",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"name"},
		входПоля:          map[string]string{"name": "string", "kind": "string", "module": "string", "component": "string", "limit": "integer", "cursor": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "form_impact",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"draftCode": "string", "draftExtension": "string", "draftPrefix": "string", "dumps": "array", "form": "string", "name": "string", "type": "string"},
		выходОбязательные: []string{"form", "sources", "conflicts", "guidance"},
		выходПоля:         []string{"conflicts", "form", "guidance", "sources"},
	},
	{
		имя:               "get_configuration_info",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{},
		выходОбязательные: []string{"name", "isExtension", "objectCounts"},
		выходПоля:         []string{"defaultRunMode", "extensionPurpose", "isExtension", "mode", "name", "namePrefix", "objectCounts", "platformVersion", "scriptVariant", "synonym", "uuid", "vendor", "version"},
	},
	{
		имя:              "get_context_for_task",
		режимы:           режимОффлайн | режимLive,
		входОбязательные: []string{"task"},
		входПоля: map[string]string{
			"task": "string", "project": "string", "componentHints": "array", "budgetChars": "integer",
			"budgetTokens": "integer", "focusHints": "array", "view": "string", "maxDepth": "integer",
			"includeCode": "string", "freshness": "string",
		},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "get_event_log",
		режимы:            режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"base": "string", "endDate": "string", "level": "string", "limit": "integer", "startDate": "string", "user": "string"},
		выходОбязательные: []string{"base", "entries", "count"},
		выходПоля:         []string{"base", "count", "entries", "truncated"},
	},
	{
		имя:               "get_form_handlers",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"type": "string", "name": "string", "form": "string", "component": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "get_form_structure",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"form": "string", "name": "string", "type": "string"},
		выходОбязательные: []string{"name"},
		выходПоля:         []string{"attributes", "commands", "handlers", "items", "name", "owner"},
	},
	{
		имя:               "get_metadata_tree",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"like": "string", "type": "string"},
		выходОбязательные: []string{"configuration", "totalObjects", "groups"},
		выходПоля:         []string{"configuration", "groups", "totalObjects"},
	},
	{
		имя:               "get_module_structure",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"module"},
		входПоля:          map[string]string{"module": "string", "component": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		// C3: posting_review влит сюда входом review, его ответ в поле review.
		имя:               "get_movements",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"name"},
		входПоля:          map[string]string{"name": "string", "review": "boolean"},
		выходОбязательные: []string{"document", "registers"},
		выходПоля:         []string{"document", "registers", "review"},
	},
	{
		имя:               "get_object",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"type": "string", "name": "string", "component": "string", "parts": "string", "view": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "get_object_structure",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"name": "string", "parts": "string", "type": "string"},
		выходОбязательные: []string{"type", "name"},
		выходПоля:         []string{"attributes", "commands", "forms", "name", "nextSteps", "other", "synonym", "tabularSections", "type", "uuid"},
	},
	{
		имя:               "get_predefined",
		режимы:            режимLive,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"base": "string", "name": "string", "type": "string"},
		выходОбязательные: []string{"base", "items", "count"},
		выходПоля:         []string{"base", "count", "items", "truncated"},
	},
	{
		имя:               "get_query_schema",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"name": "string", "type": "string"},
		выходОбязательные: []string{"tableName", "objectType", "name", "standardFields"},
		выходПоля:         []string{"name", "objectType", "ownFields", "standardFields", "tableName", "tabularSections", "virtualTables"},
	},
	{
		имя:               "get_subsystem",
		режимы:            режимLive,
		входОбязательные:  []string{"name"},
		входПоля:          map[string]string{"base": "string", "name": "string"},
		выходОбязательные: []string{"base", "name"},
		выходПоля:         []string{"base", "content", "name", "subsystems", "synonym"},
	},
	{
		имя:               "get_symbol",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"uid": "string", "module": "string", "name": "string", "component": "string", "includeBody": "boolean"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "index_status",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"project": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "list_bases",
		режимы:            режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{},
		выходОбязательные: []string{"count", "bases"},
		выходПоля:         []string{"bases", "count", "current"},
	},
	{
		имя:               "list_projects",
		режимы:            режимОффлайн,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"root": "string"},
		выходОбязательные: []string{"root", "count", "projects"},
		выходПоля:         []string{"count", "projects", "root"},
	},
	{
		имя:               "new_object_checklist",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"objectType", "name", "like"},
		входПоля:          map[string]string{"like": "string", "name": "string", "objectType": "string"},
		выходОбязательные: []string{"object", "like", "items", "done", "missing", "note"},
		выходПоля:         []string{"done", "items", "like", "missing", "note", "object"},
	},
	{
		имя:               "object_exists",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"name": "string", "type": "string"},
		выходОбязательные: []string{"exists", "object", "count"},
		выходПоля:         []string{"count", "exists", "object", "similar"},
	},
	{
		// Тикет 11 (v1-object-graph): тонкая обёртка над
		// ObjectGraphService.Radius — тот же конверт Response[T], что у
		// find_impact/find_register_writes, поэтому те же выходные поля.
		имя:              "object_graph",
		режимы:           режимОффлайн | режимLive,
		входОбязательные: []string{},
		входПоля: map[string]string{"objectId": "integer", "objectType": "string", "objectName": "string",
			"component": "string", "direction": "string", "kinds": "array", "depth": "integer",
			"minConfidence": "number", "project": "string", "limit": "integer", "cursor": "string",
			"view": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "query_advisor",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"text"},
		входПоля:          map[string]string{"text": "string"},
		выходОбязательные: []string{"warnings", "count"},
		выходПоля:         []string{"count", "tables", "warnings"},
	},
	{
		имя:               "reindex",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"mode": "string", "component": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "rights_audit",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"type", "name"},
		входПоля:          map[string]string{"includeNotGranting": "boolean", "name": "string", "profile": "string", "roles": "array", "type": "string"},
		выходОбязательные: []string{"object", "totalRoles", "granting"},
		выходПоля:         []string{"effective", "granting", "notGranting", "notGrantingCount", "note", "object", "profiles", "rightSummary", "totalRoles", "undetermined", "undeterminedCount"},
	},
	{
		имя:               "search_code",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"query"},
		входПоля:          map[string]string{"ignoreCase": "boolean", "maxResults": "integer", "query": "string", "regex": "boolean", "scope": "string", "total": "boolean"},
		выходОбязательные: []string{"query", "matches", "shown", "truncated"},
		выходПоля:         []string{"matches", "note", "query", "scope", "shown", "totalMatches", "truncated"},
	},
	{
		имя:               "server_info",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{},
		выходОбязательные: []string{"name", "version", "mode", "source", "client", "memory"},
		выходПоля:         []string{"client", "memory", "mode", "name", "source", "version"},
	},
	{
		имя:               "set_base",
		режимы:            режимLive,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"name": "string"},
		выходОбязательные: []string{"ok"},
		выходПоля:         []string{"message", "name", "ok", "url"},
	},
	{
		имя:               "set_dump",
		режимы:            режимОффлайн,
		входОбязательные:  []string{},
		входПоля:          map[string]string{"path": "string"},
		выходОбязательные: []string{"ok", "path"},
		выходПоля:         []string{"ageDays", "configuration", "exportedAt", "isExtension", "message", "ok", "path", "warning"},
	},
	{
		имя:               "trace_call_graph",
		режимы:            режимОффлайн | режимLive,
		входОбязательные:  []string{"uid"},
		входПоля:          map[string]string{"uid": "string", "direction": "string", "depth": "integer", "expandAmbiguous": "boolean", "limit": "integer", "cursor": "string"},
		выходОбязательные: []string{"generation", "stale", "items"},
		выходПоля:         []string{"generation", "stale", "warnings", "items", "totalCount", "nextCursor"},
	},
	{
		имя:               "validate_bsl",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"code"},
		входПоля:          map[string]string{"code": "string"},
		выходОбязательные: []string{"count", "findings", "checked"},
		выходПоля:         []string{"checked", "count", "findings"},
	},
	{
		имя:               "validate_query",
		режимы:            режимLive,
		входОбязательные:  []string{"text"},
		входПоля:          map[string]string{"base": "string", "text": "string"},
		выходОбязательные: []string{"base", "valid"},
		выходПоля:         []string{"base", "error", "valid"},
	},
	{
		имя:               "visibility_audit",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"objectType", "name"},
		входПоля:          map[string]string{"name": "string", "objectType": "string", "withRights": "boolean"},
		выходОбязательные: []string{"object", "subsystems", "rolesGranting"},
		выходПоля:         []string{"findings", "functionalOptions", "note", "object", "rolesGranting", "subsystems"},
	},
	{
		имя:               "write_path",
		режимы:            режимОффлайн,
		входОбязательные:  []string{"objectType", "name"},
		входПоля:          map[string]string{"name": "string", "objectType": "string"},
		выходОбязательные: []string{"object", "steps"},
		выходПоля:         []string{"object", "steps", "warnings"},
	},
}

// параметрыРежима — опции запуска сервера для каждого режима регистрации.
// Оффлайн берётся без выгрузки: состав инструментов от неё не зависит, а тест
// не должен зависеть от наличия каталога на диске. Реестр проектов (без него
// индексные инструменты не регистрируются) добавляет инструментыСервера.
func параметрыРежима(режим режимИнструмента) options {
	if режим == режимLive {
		return options{baseURL: "http://localhost/base/hs/mcp1c"}
	}
	return options{}
}

// инструментыСервера поднимает сервер в памяти и возвращает объявленные им
// инструменты по именам. Это шов 2 (реальный транспорт), только без stdio.
func инструментыСервера(t *testing.T, режим режимИнструмента) map[string]*mcp.Tool {
	t.Helper()
	opts := параметрыРежима(режим)
	opts.projectsRoot = t.TempDir()
	сессия := сессияКлиента(t, opts)
	список, err := сессия.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	по_именам := map[string]*mcp.Tool{}
	for _, инструмент := range список.Tools {
		по_именам[инструмент.Name] = инструмент
	}
	return по_именам
}

// сессияКлиента подключает клиента к серверу через транспорт в памяти.
func сессияКлиента(t *testing.T, opts options) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	if opts.syntaxIndex == "" {
		// Индекс синтаксиса живёт вне репозитория; контракт проверяется на
		// синтетической фикстуре, а не на файле машины, где идёт прогон.
		opts.syntaxIndex = syntaxtest.FixtureFile(t)
	}
	клиентский, серверный := mcp.NewInMemoryTransports()
	серверная, err := newServer(opts).Connect(ctx, серверный, nil)
	if err != nil {
		t.Fatalf("подключение сервера: %v", err)
	}
	t.Cleanup(func() { серверная.Close() })
	клиент := mcp.NewClient(&mcp.Implementation{Name: "contract-test", Version: "0"}, nil)
	сессия, err := клиент.Connect(ctx, клиентский, nil)
	if err != nil {
		t.Fatalf("подключение клиента: %v", err)
	}
	t.Cleanup(func() { сессия.Close() })
	return сессия
}

// разобратьСхему приводит JSON-схему к паре «обязательные поля» / «поле -> тип».
// Схема разбирается через JSON, а не через типы jsonschema-go: контракт — это то,
// что уходит клиенту по проводу.
func разобратьСхему(t *testing.T, схема any) (map[string]bool, map[string]string) {
	t.Helper()
	сырая, err := json.Marshal(схема)
	if err != nil {
		t.Fatalf("сериализация схемы: %v", err)
	}
	var разобранная struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(сырая, &разобранная); err != nil {
		t.Fatalf("разбор схемы: %v", err)
	}
	обязательные := map[string]bool{}
	for _, поле := range разобранная.Required {
		обязательные[поле] = true
	}
	поля := map[string]string{}
	for имя, тело := range разобранная.Properties {
		поля[имя] = типПоля(t, тело)
	}
	return обязательные, поля
}

// типПоля вытаскивает тип поля схемы. Nullable-поля go-sdk описывает как
// ["null","array"]; для контракта значим сам тип, а не допустимость null.
func типПоля(t *testing.T, тело json.RawMessage) string {
	t.Helper()
	var поле struct {
		Type any `json:"type"`
	}
	if err := json.Unmarshal(тело, &поле); err != nil {
		t.Fatalf("разбор поля схемы: %v", err)
	}
	switch тип := поле.Type.(type) {
	case string:
		return тип
	case []any:
		for _, вариант := range тип {
			if строка, ок := вариант.(string); ок && строка != "null" {
				return строка
			}
		}
		return ""
	default:
		// Поле без явного type (например, произвольный объект параметров).
		return "object"
	}
}

// TestКонтрактСоставИнструментов сверяет таблицу с реестром в обе стороны:
// пропавший инструмент — потеря контракта, лишний — контракт без фиксации.
func TestКонтрактСоставИнструментов(t *testing.T) {
	for _, режим := range []struct {
		имя  string
		флаг режимИнструмента
	}{{"offline", режимОффлайн}, {"live", режимLive}} {
		t.Run(режим.имя, func(t *testing.T) {
			объявленные := инструментыСервера(t, режим.флаг)
			ожидаемые := map[string]bool{}
			for _, контракт := range контрактыИнструментов {
				if контракт.режимы&режим.флаг != 0 {
					ожидаемые[контракт.имя] = true
				}
			}
			for имя := range ожидаемые {
				if объявленные[имя] == nil {
					t.Errorf("инструмент %q из контракта не зарегистрирован в режиме %s", имя, режим.имя)
				}
			}
			for имя := range объявленные {
				if !ожидаемые[имя] {
					t.Errorf("инструмент %q зарегистрирован в режиме %s, но его контракт не зафиксирован: "+
						"добавьте его в контрактыИнструментов", имя, режим.имя)
				}
			}
			t.Logf("режим %s: инструментов %d", режим.имя, len(объявленные))
		})
	}
}

// TestКонтрактВходныхСхем: состав обязательных полей совпадает точно, типы
// зафиксированных полей не менялись. Новое необязательное поле допустимо.
func TestКонтрактВходныхСхем(t *testing.T) {
	проверитьСхемы(t, func(t *testing.T, контракт контрактИнструмента, инструмент *mcp.Tool) {
		обязательные, поля := разобратьСхему(t, инструмент.InputSchema)
		сверитьОбязательные(t, "вход", контракт.имя, контракт.входОбязательные, обязательные)
		for имя, тип := range контракт.входПоля {
			фактический, есть := поля[имя]
			if !есть {
				t.Errorf("%s: входное поле %q пропало из схемы", контракт.имя, имя)
				continue
			}
			if фактический != тип {
				t.Errorf("%s: входное поле %q сменило тип: было %q, стало %q",
					контракт.имя, имя, тип, фактический)
			}
		}
	})
}

// TestКонтрактВыходныхСхем: клиент вправе рассчитывать на те же обязательные
// поля ответа и на присутствие зафиксированных полей.
func TestКонтрактВыходныхСхем(t *testing.T) {
	проверитьСхемы(t, func(t *testing.T, контракт контрактИнструмента, инструмент *mcp.Tool) {
		if инструмент.OutputSchema == nil {
			t.Fatalf("%s: выходная схема пропала, structuredContent больше не типизирован", контракт.имя)
		}
		обязательные, поля := разобратьСхему(t, инструмент.OutputSchema)
		сверитьОбязательные(t, "выход", контракт.имя, контракт.выходОбязательные, обязательные)
		for _, имя := range контракт.выходПоля {
			if _, есть := поля[имя]; !есть {
				t.Errorf("%s: выходное поле %q пропало из схемы", контракт.имя, имя)
			}
		}
	})
}

// TestКонтрактОписаний: описание — часть контракта инструмента, по нему модель
// решает, звать ли его. Пустое описание делает инструмент невидимым.
func TestКонтрактОписаний(t *testing.T) {
	проверитьСхемы(t, func(t *testing.T, контракт контрактИнструмента, инструмент *mcp.Tool) {
		if len([]rune(инструмент.Description)) < 40 {
			t.Errorf("%s: описание длиной %d символов — инструмент нечем выбрать",
				контракт.имя, len([]rune(инструмент.Description)))
		}
	})
}

// проверитьСхемы прогоняет проверку по каждому инструменту в каждом режиме,
// где он зарегистрирован.
func проверитьСхемы(t *testing.T, проверка func(*testing.T, контрактИнструмента, *mcp.Tool)) {
	t.Helper()
	for _, режим := range []struct {
		имя  string
		флаг режимИнструмента
	}{{"offline", режимОффлайн}, {"live", режимLive}} {
		объявленные := инструментыСервера(t, режим.флаг)
		for _, контракт := range контрактыИнструментов {
			if контракт.режимы&режим.флаг == 0 {
				continue
			}
			инструмент := объявленные[контракт.имя]
			if инструмент == nil {
				continue // отсутствие ловит TestКонтрактСоставИнструментов
			}
			t.Run(режим.имя+"/"+контракт.имя, func(t *testing.T) {
				проверка(t, контракт, инструмент)
			})
		}
	}
}

// сверитьОбязательные сравнивает множества обязательных полей. Порядок в схеме
// значения не имеет, состав — имеет: поле, ставшее обязательным, ломает клиентов.
func сверитьОбязательные(t *testing.T, сторона, инструмент string, ожидаемые []string, фактические map[string]bool) {
	t.Helper()
	ожидаемое := map[string]bool{}
	for _, поле := range ожидаемые {
		ожидаемое[поле] = true
		if !фактические[поле] {
			t.Errorf("%s: %s: поле %q перестало быть обязательным", инструмент, сторона, поле)
		}
	}
	var новые []string
	for поле := range фактические {
		if !ожидаемое[поле] {
			новые = append(новые, поле)
		}
	}
	sort.Strings(новые)
	for _, поле := range новые {
		t.Errorf("%s: %s: поле %q стало обязательным — несовместимое изменение, нужен versioned tool",
			инструмент, сторона, поле)
	}
}
