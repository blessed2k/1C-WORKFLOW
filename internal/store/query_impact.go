package store

import (
	"database/sql"
	"fmt"
)

// Read-запросы для find_impact (таск 13, обратный BFS по типизированному
// dependency graph, архитектура §19/§21). Отдельный файл — не tx.go — по той
// же причине, по которой cmd/mcp1c/idx_*.go не делят один файл на несколько
// тасков: internal/app здесь не единственный потребитель store в эту волну
// (таски 11/12 работают параллельно над своими инструментами), и общий файл
// стал бы точкой конфликта. Ничего из tx.go/schema.go этот файл не меняет —
// только новые SELECT поверх уже существующей схемы раздела 15.
//
// Отклонение от исходного плана тикета 13 (интерфейсы.md, «Из таска 03» и
// зона тикета 13 называют только internal/app/impact*.go и
// cmd/mcp1c/idx_impact.go): store.ReadTx на момент начала этого таска не
// экспортирует НИ ОДНОГО метода чтения фактов графа (Meta/GenerationNumber/
// Blob/SourceFileID/NodeID/Validate — и всё). find_impact без обратного
// BFS по call_edge/reference/handler_binding/register_access/role_right/
// dependency_edge реализовать нельзя в принципе, а «SQL вне internal/store
// запрещён» (RuleSQLOnlyInStore, internal/arch) не оставляет выбора, кроме
// как положить недостающее чтение сюда. Смотри контракт тикета 13 —
// «Отклонения от плана» — это то же самое ограничение, скорее всего, задевает
// find_symbol/find_references (тикет 11) и find_register_writes (тикет 12);
// они не тронуты этим файлом и вольны завести свой.

// Виды рёбер, которые понимает обратный BFS find_impact. query_reference
// сюда намеренно не входит: таск 09 не публикует эту таблицу (interfaces.md,
// «Из таска 09» и долг, переданный тикету 12) — она остаётся пустой, и
// IncomingEdges не может отдать то, чего в store физически нет.
const (
	ImpactKindCallEdge       = "call_edge"
	ImpactKindReference      = "reference"
	ImpactKindHandlerBinding = "handler_binding"
	ImpactKindRegisterAccess = "register_access"
	ImpactKindRoleRight      = "role_right"
	ImpactKindDependencyEdge = "dependency_edge"
)

// ImpactEdgeKinds — полный список видов рёбер, поддержанных IncomingEdges,
// в фиксированном порядке (используется как умолчание, когда вызывающий не
// сузил выбор).
var ImpactEdgeKinds = []string{
	ImpactKindCallEdge, ImpactKindReference, ImpactKindHandlerBinding,
	ImpactKindRegisterAccess, ImpactKindRoleRight, ImpactKindDependencyEdge,
}

// ImpactEdge — одно ВХОДЯЩЕЕ ребро узла, переданного в IncomingEdges:
// FromNodeID — то, что зависит от искомого узла через ребро вида Kind.
//
// FromNodeKind — "symbol" | "metadata_object" | "metadata_member" | "form" |
// "role". Роль (role) — единственное исключение из единого пространства id
// node (раздел 15 схемы: «role не является node-сущностью»): FromNodeID для
// FromNodeKind="role" — это role.id, а НЕ node.id, у него своё пространство
// значений. Вызывающий код обязан различать узлы парой (FromNodeKind,
// FromNodeID), а не одним FromNodeID, иначе role.id=5 и symbol.id=5 —
// разные вещи с одинаковым числом — схлопнутся в один посещённый узел.
type ImpactEdge struct {
	Kind          string
	Detail        string
	FromNodeKind  string
	FromNodeID    int64
	FromComponent string
	FromDisplay   string
	Resolution    string
	Confidence    float64
	Layer         string
}

// Разрешение корня обхода в internal/app/impact.go НЕ заводит здесь новых
// примитивов: символ адресуется через уже существующий tx.NodeID(uid) —
// identity_key символа равен его uid (internal/index/identity.go:
// symbolIdentityKey, тот же приём, которым read_symbol.go, таск 11,
// комментирует свой SymbolByID); объект метаданных — через
// tx.MetadataObjectsByName (readmeta.go, таск 12). Дублировать оба под
// новым именем здесь означало бы третий способ найти то же самое.

// IncomingEdges — один шаг обратного BFS find_impact: все непосредственные
// входящие рёбра узла (toKind, toID) среди выбранных kinds (пусто —
// ImpactEdgeKinds целиком). toKind ограничивает, какие таблицы физически
// могут на него ссылаться (симметрично схеме раздела 15): call_edge/
// handler_binding целятся только в symbol, register_access/role_right —
// только в metadata_object, reference — в symbol или (когда-нибудь) в
// metadata_object, dependency_edge — в любой узел общего пространства id.
func (tx *ReadTx) IncomingEdges(toKind string, toID int64, kinds []string) ([]ImpactEdge, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	want := edgeKindSet(kinds)
	var out []ImpactEdge

	switch toKind {
	case "symbol":
		if want[ImpactKindCallEdge] {
			edges, err := tx.callEdgesInto(toID)
			if err != nil {
				return nil, fmt.Errorf("call_edge: %w", err)
			}
			out = append(out, edges...)
		}
		if want[ImpactKindReference] {
			edges, err := tx.referencesIntoSymbol(toID)
			if err != nil {
				return nil, fmt.Errorf("reference: %w", err)
			}
			out = append(out, edges...)
		}
		if want[ImpactKindHandlerBinding] {
			edges, err := tx.handlerBindingsInto(toID)
			if err != nil {
				return nil, fmt.Errorf("handler_binding: %w", err)
			}
			out = append(out, edges...)
		}
	case "metadata_object":
		if want[ImpactKindReference] {
			edges, err := tx.referencesIntoObject(toID)
			if err != nil {
				return nil, fmt.Errorf("reference: %w", err)
			}
			out = append(out, edges...)
		}
		if want[ImpactKindRegisterAccess] {
			edges, err := tx.registerAccessesInto(toID)
			if err != nil {
				return nil, fmt.Errorf("register_access: %w", err)
			}
			out = append(out, edges...)
		}
		if want[ImpactKindRoleRight] {
			edges, err := tx.roleRightsInto(toID)
			if err != nil {
				return nil, fmt.Errorf("role_right: %w", err)
			}
			out = append(out, edges...)
		}
	}
	// dependency_edge адресует общее пространство node — работает для
	// любого toKind, у которого toID является node.id (все, кроме role,
	// у которой своё пространство id и на которую generic-рёбра не ссылаются).
	if want[ImpactKindDependencyEdge] && toKind != "role" {
		edges, err := tx.dependencyEdgesInto(toID)
		if err != nil {
			return nil, fmt.Errorf("dependency_edge: %w", err)
		}
		out = append(out, edges...)
	}
	return out, nil
}

func edgeKindSet(kinds []string) map[string]bool {
	if len(kinds) == 0 {
		kinds = ImpactEdgeKinds
	}
	set := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	return set
}

// callEdgesInto — символы, вызывающие calleeID (call_edge.callee_id).
func (tx *ReadTx) callEdgesInto(calleeID int64) ([]ImpactEdge, error) {
	rows, err := tx.c.query(tx.ctx, `
		SELECT ce.caller_id, n.component_id, m.name_display, s.name_display,
		       ce.kind, ce.resolution, ce.confidence
		FROM call_edge ce
		JOIN symbol s ON s.id = ce.caller_id
		JOIN module m ON m.id = s.module_id
		JOIN node n ON n.id = ce.caller_id
		WHERE ce.callee_id = ?`, calleeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImpactEdge
	for rows.Next() {
		var e ImpactEdge
		var moduleDisplay, symDisplay string
		if err := rows.Scan(&e.FromNodeID, &e.FromComponent, &moduleDisplay, &symDisplay,
			&e.Detail, &e.Resolution, &e.Confidence); err != nil {
			return nil, err
		}
		e.Kind = ImpactKindCallEdge
		e.FromNodeKind = "symbol"
		e.FromDisplay = moduleDisplay + "." + symDisplay
		out = append(out, e)
	}
	return out, rows.Err()
}

// referencesIntoSymbol — ссылки, чья цель (target_symbol_id) — symbolID.
func (tx *ReadTx) referencesIntoSymbol(symbolID int64) ([]ImpactEdge, error) {
	return tx.referencesInto(`r.target_symbol_id = ? AND r.from_symbol_id IS NOT NULL`, symbolID)
}

// referencesIntoObject — ссылки, чья цель (target_object_id) — objectID.
//
// Честно: на сегодня всегда пустой список. publishReference
// (internal/index/publish.go) заполняет только ветки target_class="symbol" и
// "platform" — target_object_id не проставляется НИ РАЗУ ни при какой
// ссылке, хотя резолвер (internal/resolve) умеет отдавать
// domain.TargetMetadata. Это разрыв публикации, обнаруженный этим тикетом, а
// не изобретённое ограничение find_impact: чинить internal/index — вне зоны
// тикета 13 (пакет уже «сдан» таском 09, явного допуска трогать его здесь
// нет, в отличие от тикета 12 и query_reference). Запрос написан правильно
// на случай, если публикация когда-нибудь заполнит эту колонку.
func (tx *ReadTx) referencesIntoObject(objectID int64) ([]ImpactEdge, error) {
	return tx.referencesInto(`r.target_object_id = ? AND r.from_symbol_id IS NOT NULL`, objectID)
}

func (tx *ReadTx) referencesInto(where string, arg int64) ([]ImpactEdge, error) {
	rows, err := tx.c.query(tx.ctx, `
		SELECT r.from_symbol_id, n.component_id, m.name_display, s.name_display,
		       r.kind, r.resolution, r.confidence
		FROM reference r
		JOIN symbol s ON s.id = r.from_symbol_id
		JOIN module m ON m.id = s.module_id
		JOIN node n ON n.id = r.from_symbol_id
		WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImpactEdge
	for rows.Next() {
		var e ImpactEdge
		var moduleDisplay, symDisplay string
		if err := rows.Scan(&e.FromNodeID, &e.FromComponent, &moduleDisplay, &symDisplay,
			&e.Detail, &e.Resolution, &e.Confidence); err != nil {
			return nil, err
		}
		e.Kind = ImpactKindReference
		e.FromNodeKind = "symbol"
		e.FromDisplay = moduleDisplay + "." + symDisplay
		out = append(out, e)
	}
	return out, rows.Err()
}

// handlerBindingsInto — формы, привязавшие handlerSymbolID как обработчик
// события (handler_binding.handler_symbol_id). handler_binding не несёт
// собственной колонки confidence — привязка приезжает из точного разбора
// Form.xml (parser-xml), поэтому Confidence=1 у любой resolved-строки,
// возвращённой этим запросом (WHERE уже требует конкретный handler_symbol_id,
// то есть resolution='resolved' по построению публикации, таск 09).
func (tx *ReadTx) handlerBindingsInto(handlerSymbolID int64) ([]ImpactEdge, error) {
	rows, err := tx.c.query(tx.ctx, `
		SELECT hb.form_id, n.component_id, f.name_display, hb.source, hb.event, hb.resolution
		FROM handler_binding hb
		JOIN form f ON f.id = hb.form_id
		JOIN node n ON n.id = hb.form_id
		WHERE hb.handler_symbol_id = ?`, handlerSymbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImpactEdge
	for rows.Next() {
		var e ImpactEdge
		var source, event string
		if err := rows.Scan(&e.FromNodeID, &e.FromComponent, &e.FromDisplay, &source, &event, &e.Resolution); err != nil {
			return nil, err
		}
		e.Kind = ImpactKindHandlerBinding
		e.FromNodeKind = "form"
		e.Detail = source + ":" + event
		e.Confidence = 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// registerAccessesInto — символы, читающие/пишущие регистр objectID
// (register_access.object_id).
func (tx *ReadTx) registerAccessesInto(objectID int64) ([]ImpactEdge, error) {
	rows, err := tx.c.query(tx.ctx, `
		SELECT ra.symbol_id, n.component_id, m.name_display, s.name_display, ra.mode, ra.static, ra.confidence
		FROM register_access ra
		JOIN symbol s ON s.id = ra.symbol_id
		JOIN module m ON m.id = s.module_id
		JOIN node n ON n.id = ra.symbol_id
		WHERE ra.object_id = ? AND ra.symbol_id IS NOT NULL`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImpactEdge
	for rows.Next() {
		var e ImpactEdge
		var moduleDisplay, symDisplay, mode string
		var static int64
		if err := rows.Scan(&e.FromNodeID, &e.FromComponent, &moduleDisplay, &symDisplay,
			&mode, &static, &e.Confidence); err != nil {
			return nil, err
		}
		e.Kind = ImpactKindRegisterAccess
		e.FromNodeKind = "symbol"
		e.FromDisplay = moduleDisplay + "." + symDisplay
		if static != 0 {
			e.Detail = mode + " (static)"
		} else {
			e.Detail = mode + " (dynamic)"
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// roleRightsInto — роли с правом на objectID (role_right.object_id). Роль —
// не node (раздел 15 схемы), поэтому FromNodeID здесь — role.id в СВОЁМ
// пространстве id, не node.id; см. doc-комментарий ImpactEdge.
func (tx *ReadTx) roleRightsInto(objectID int64) ([]ImpactEdge, error) {
	rows, err := tx.c.query(tx.ctx, `
		SELECT rr.role_id, ro.component_id, ro.name_display, rr.right_name, rr.value, rr.rls, rr.set_for_new_objects
		FROM role_right rr
		JOIN role ro ON ro.id = rr.role_id
		WHERE rr.object_id = ?`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImpactEdge
	for rows.Next() {
		var e ImpactEdge
		var right string
		var value, setForNew int64
		var rls sql.NullString
		if err := rows.Scan(&e.FromNodeID, &e.FromComponent, &e.FromDisplay, &right, &value, &rls, &setForNew); err != nil {
			return nil, err
		}
		e.Kind = ImpactKindRoleRight
		e.FromNodeKind = "role"
		e.Confidence = 1
		detail := right + "=" + boolRu(value != 0)
		if rls.Valid && rls.String != "" {
			detail += " (RLS)"
		}
		if setForNew != 0 {
			detail += " [умолчание для новых]"
		}
		e.Detail = detail
		out = append(out, e)
	}
	return out, rows.Err()
}

// dependencyEdgesInto — generic-рёбра, целящиеся в toID (dependency_edge.to_node).
//
// Честно: сегодня в store лежит только kind="field-typed-by", и его
// from_node всегда metadata_member (единственный производитель —
// resolve.DeriveDependencyEdges, interfaces.md «Из таска 08»/«Из таска 09»).
// Отображаемое имя поэтому строится ТОЛЬКО для случая
// FromNodeKind="metadata_member" (реквизит/измерение/ресурс владеющего
// объекта). Если появится ребро с другим from_node.kind, FromDisplay
// придётся расширить — запрос уже возвращает n.kind, чтобы вызывающий код
// (internal/app) хотя бы не потерял сам факт, даже без красивого имени.
func (tx *ReadTx) dependencyEdgesInto(toID int64) ([]ImpactEdge, error) {
	rows, err := tx.c.query(tx.ctx, `
		SELECT de.kind, de.from_node, n.kind, n.component_id, de.confidence, de.layer,
		       mm.name_display, mo.mtype, mo.name_display
		FROM dependency_edge de
		JOIN node n ON n.id = de.from_node
		LEFT JOIN metadata_member mm ON mm.id = de.from_node
		LEFT JOIN metadata_object mo ON mo.id = mm.object_id
		WHERE de.to_node = ?`, toID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImpactEdge
	for rows.Next() {
		var e ImpactEdge
		var memberDisplay, objMType, objDisplay sql.NullString
		if err := rows.Scan(&e.Detail, &e.FromNodeID, &e.FromNodeKind, &e.FromComponent, &e.Confidence, &e.Layer,
			&memberDisplay, &objMType, &objDisplay); err != nil {
			return nil, err
		}
		e.Kind = ImpactKindDependencyEdge
		if e.FromNodeKind == "metadata_member" && memberDisplay.Valid {
			if objDisplay.Valid {
				e.FromDisplay = objMType.String + " " + objDisplay.String + "." + memberDisplay.String
			} else {
				e.FromDisplay = memberDisplay.String
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func boolRu(v bool) string {
	if v {
		return "да"
	}
	return "нет"
}
