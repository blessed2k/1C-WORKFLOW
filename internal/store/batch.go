package store

import (
	"context"
	"fmt"
	"strings"
)

// Пакетная вставка (issue #3, шаг 2). Полная пересборка пишет миллионы строк
// ссылок, зависимостей разрешения и рёбер вызовов; одна строка на INSERT
// платит полный обход движка SQLite на каждую. Строки листовых таблиц (на них
// не ссылается ни одна таблица, кроме таких же буферизуемых) копятся в буфере
// и уходят многострочным INSERT.
//
// Видимость не меняется: любой оператор, который читает буферизуемую таблицу
// или удаляет и обновляет строки, на которые она ссылается, идёт через
// ReadTx.check и сначала сбрасывает буферы (правило в doc-комментарии
// checkNoFlush). Внешние ключи проверяются немедленно, поэтому родительский
// буфер (reference) сбрасывается раньше дочерних.

// batchRows: строк в одном многострочном INSERT. 128 строк по 17 колонок это
// 2176 параметров, далеко от лимита SQLite на число переменных.
const batchRows = 128

// batchCol: колонка пакета и то, как её значение берётся из строки. Имя
// колонки и порядок аргументов выводятся из одного описания, поэтому
// разойтись не могут; со схемой их сверяет TestBatchSpecsMatchSchema.
type batchCol[R any] struct {
	name string
	val  func(*R) any
}

// batchSpec: таблица и её колонки в порядке INSERT.
type batchSpec[R any] struct {
	table string
	cols  []batchCol[R]
}

func (s batchSpec[R]) colNames() []string {
	out := make([]string, len(s.cols))
	for i, c := range s.cols {
		out[i] = c.name
	}
	return out
}

// flusher: буфер любой таблицы, как его видят txBatches и родители.
type flusher interface {
	flush(ctx context.Context, c *conn) error
	tableName() string
	columns() []string
}

// insertBatch: буфер одной таблицы.
type insertBatch[R any] struct {
	spec    batchSpec[R]
	head    string // "INSERT INTO t(a,b) VALUES"
	one     string // "(?,?)"
	args    []any
	rows    int
	parents []flusher // сбрасываются раньше этого буфера
	full    string    // текст на batchRows строк, собирается один раз
}

// newBatch создаёт буфер и регистрирует его в all: порядок all это порядок
// создания, а порядок сброса родителей раньше детей держат parents.
func newBatch[R any](all *[]flusher, spec batchSpec[R], parents ...flusher) *insertBatch[R] {
	n := len(spec.cols)
	b := &insertBatch[R]{
		spec: spec,
		head: "INSERT INTO " + spec.table + "(" + strings.Join(spec.colNames(), ",") + ") VALUES",
		one:  "(" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")",
		args: make([]any, 0, batchRows*n), parents: parents,
	}
	*all = append(*all, b)
	return b
}

func (b *insertBatch[R]) tableName() string { return b.spec.table }
func (b *insertBatch[R]) columns() []string { return b.spec.colNames() }

func (b *insertBatch[R]) text(n int) string {
	if n == batchRows && b.full != "" {
		return b.full
	}
	var sb strings.Builder
	sb.Grow(len(b.head) + n*(len(b.one)+1))
	sb.WriteString(b.head)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(b.one)
	}
	s := sb.String()
	if n == batchRows {
		b.full = s
	}
	return s
}

// add кладёт строку; полный буфер уходит сразу.
func (b *insertBatch[R]) add(ctx context.Context, c *conn, row R) error {
	for _, col := range b.spec.cols {
		b.args = append(b.args, col.val(&row))
	}
	b.rows++
	if b.rows < batchRows {
		return nil
	}
	return b.flush(ctx, c)
}

// flush отправляет накопленное. Полный пакет идёт через кэш подготовленных
// выражений (текст один на таблицу), хвост переменной длины мимо кэша.
func (b *insertBatch[R]) flush(ctx context.Context, c *conn) error {
	if b.rows == 0 {
		return nil
	}
	for _, p := range b.parents {
		if err := p.flush(ctx, c); err != nil {
			return err
		}
	}
	var err error
	if b.rows == batchRows {
		err = c.exec(ctx, b.text(batchRows), b.args...)
	} else {
		_, err = c.sc.ExecContext(ctx, b.text(b.rows), b.args...)
	}
	b.args = b.args[:0]
	b.rows = 0
	if err != nil {
		return fmt.Errorf("пакетная вставка в %s: %w", b.spec.table, err)
	}
	return nil
}

// Строки пакетов, которым мало публичного типа строки (нужен id владельца).
type (
	refRow struct {
		id int64
		r  Reference
	}
	candidateRow struct {
		refID, nodeID int64
		rank          int
		reason        string
	}
	depRow struct {
		key   string
		refID int64
	}
	paramRow struct {
		symbolID int64
		p        Parameter
	}
	ftsRow struct {
		id int64
		s  Symbol
	}
)

func col[R any](name string, val func(*R) any) batchCol[R] { return batchCol[R]{name, val} }

var (
	referenceSpec = batchSpec[refRow]{"reference", []batchCol[refRow]{
		col("id", func(r *refRow) any { return r.id }),
		col("file_id", func(r *refRow) any { return r.r.FileID }),
		col("from_symbol_id", func(r *refRow) any { return nullID(r.r.FromSymbolID) }),
		col("kind", func(r *refRow) any { return r.r.Kind }),
		col("qualifier_norm", func(r *refRow) any { return nullString(r.r.QualifierNorm) }),
		col("name_norm", func(r *refRow) any { return r.r.NameNorm }),
		col("resolution", func(r *refRow) any { return r.r.Resolution }),
		col("target_class", func(r *refRow) any { return nullString(r.r.TargetClass) }),
		col("target_symbol_id", func(r *refRow) any { return nullID(r.r.TargetSymbolID) }),
		col("target_object_id", func(r *refRow) any { return nullID(r.r.TargetObjectID) }),
		col("platform_key", func(r *refRow) any { return nullString(r.r.PlatformKey) }),
		col("confidence", func(r *refRow) any { return r.r.Confidence }),
		col("layer", func(r *refRow) any { return layerOrBase(r.r.Layer) }),
		col("byte_start", func(r *refRow) any { return r.r.Span.StartByte }),
		col("byte_end", func(r *refRow) any { return r.r.Span.EndByte }),
		col("start_line", func(r *refRow) any { return r.r.Span.StartLine }),
		col("start_col", func(r *refRow) any { return r.r.Span.StartCol }),
	}}
	candidateSpec = batchSpec[candidateRow]{"reference_candidate", []batchCol[candidateRow]{
		col("ref_id", func(r *candidateRow) any { return r.refID }),
		col("target_node_id", func(r *candidateRow) any { return r.nodeID }),
		col("rank", func(r *candidateRow) any { return r.rank }),
		col("reason", func(r *candidateRow) any { return nullString(r.reason) }),
	}}
	resolutionDepSpec = batchSpec[depRow]{"resolution_dep", []batchCol[depRow]{
		col("key_hash", func(r *depRow) any { return r.key }),
		col("ref_id", func(r *depRow) any { return r.refID }),
	}}
	callEdgeSpec = batchSpec[CallEdge]{"call_edge", []batchCol[CallEdge]{
		col("caller_id", func(e *CallEdge) any { return e.CallerID }),
		col("callee_id", func(e *CallEdge) any { return nullID(e.CalleeID) }),
		col("callee_name_norm", func(e *CallEdge) any { return e.CalleeNameNorm }),
		col("qualifier_norm", func(e *CallEdge) any { return nullString(e.QualifierNorm) }),
		col("kind", func(e *CallEdge) any { return e.Kind }),
		col("resolution", func(e *CallEdge) any { return e.Resolution }),
		col("confidence", func(e *CallEdge) any { return e.Confidence }),
		col("ref_id", func(e *CallEdge) any { return e.RefID }),
	}}
	parameterSpec = batchSpec[paramRow]{"parameter", []batchCol[paramRow]{
		col("symbol_id", func(r *paramRow) any { return r.symbolID }),
		col("ord", func(r *paramRow) any { return r.p.Ord }),
		col("name", func(r *paramRow) any { return r.p.Name }),
		col("by_val", func(r *paramRow) any { return boolInt(r.p.ByVal) }),
		col("default_expr", func(r *paramRow) any { return nullString(r.p.DefaultExpr) }),
	}}
	ftsSpec = batchSpec[ftsRow]{"fts_symbols", []batchCol[ftsRow]{
		col("rowid", func(r *ftsRow) any { return r.id }),
		col("name", func(r *ftsRow) any { return r.s.NameDisplay }),
		col("signature", func(r *ftsRow) any { return r.s.Signature }),
		col("doc", func(r *ftsRow) any { return r.s.DocFirstLine }),
	}}
	diagnosticSpec = batchSpec[Diagnostic]{"diagnostic", []batchCol[Diagnostic]{
		col("file_id", func(d *Diagnostic) any { return nullID(d.FileID) }),
		col("component_id", func(d *Diagnostic) any { return d.ComponentID }),
		col("severity", func(d *Diagnostic) any { return d.Severity }),
		col("code", func(d *Diagnostic) any { return d.Code }),
		col("message", func(d *Diagnostic) any { return d.Message }),
		col("byte_start", func(d *Diagnostic) any { return d.Span.StartByte }),
		col("byte_end", func(d *Diagnostic) any { return d.Span.EndByte }),
		col("start_line", func(d *Diagnostic) any { return d.Span.StartLine }),
		col("start_col", func(d *Diagnostic) any { return d.Span.StartCol }),
	}}
	registerAccessSpec = batchSpec[RegisterAccess]{"register_access", []batchCol[RegisterAccess]{
		col("file_id", func(a *RegisterAccess) any { return a.FileID }),
		col("symbol_id", func(a *RegisterAccess) any { return nullID(a.SymbolID) }),
		col("object_id", func(a *RegisterAccess) any { return nullID(a.ObjectID) }),
		col("register_name_norm", func(a *RegisterAccess) any { return a.RegisterNameNorm }),
		col("mode", func(a *RegisterAccess) any { return a.Mode }),
		col("in_transaction", func(a *RegisterAccess) any {
			if a.InTransaction == nil {
				return nil
			}
			return boolInt(*a.InTransaction)
		}),
		col("static", func(a *RegisterAccess) any { return boolInt(a.Static) }),
		col("confidence", func(a *RegisterAccess) any { return a.Confidence }),
		col("byte_start", func(a *RegisterAccess) any { return a.Span.StartByte }),
		col("byte_end", func(a *RegisterAccess) any { return a.Span.EndByte }),
		col("layer", func(a *RegisterAccess) any { return layerOrBase(a.Layer) }),
	}}
	queryReferenceSpec = batchSpec[QueryReference]{"query_reference", []batchCol[QueryReference]{
		col("query_id", func(r *QueryReference) any { return r.QueryID }),
		col("kind", func(r *QueryReference) any { return r.Kind }),
		col("name_norm", func(r *QueryReference) any { return r.NameNorm }),
		col("object_id", func(r *QueryReference) any { return nullID(r.ObjectID) }),
		col("member_id", func(r *QueryReference) any { return nullID(r.MemberID) }),
		col("span_start", func(r *QueryReference) any { return r.SpanStart }),
		col("span_end", func(r *QueryReference) any { return r.SpanEnd }),
	}}
	handlerBindingSpec = batchSpec[HandlerBinding]{"handler_binding", []batchCol[HandlerBinding]{
		col("form_id", func(b *HandlerBinding) any { return b.FormID }),
		col("source", func(b *HandlerBinding) any { return b.Source }),
		col("event", func(b *HandlerBinding) any { return b.Event }),
		col("handler_name_norm", func(b *HandlerBinding) any { return b.HandlerNameNorm }),
		col("handler_symbol_id", func(b *HandlerBinding) any { return nullID(b.HandlerSymbolID) }),
		col("origin_file_id", func(b *HandlerBinding) any { return b.OriginFileID }),
		col("resolution", func(b *HandlerBinding) any { return b.Resolution }),
	}}
	roleRightSpec = batchSpec[RoleRight]{"role_right", []batchCol[RoleRight]{
		col("role_id", func(r *RoleRight) any { return r.RoleID }),
		col("object_id", func(r *RoleRight) any { return nullID(r.ObjectID) }),
		col("object_name_norm", func(r *RoleRight) any { return r.ObjectNameNorm }),
		col("right_name", func(r *RoleRight) any { return r.RightName }),
		col("value", func(r *RoleRight) any { return boolInt(r.Value) }),
		col("rls", func(r *RoleRight) any { return nullString(r.RLS) }),
		col("set_for_new_objects", func(r *RoleRight) any { return boolInt(r.SetForNewObject) }),
		col("origin_file_id", func(r *RoleRight) any { return r.OriginFileID }),
	}}
	dependencyEdgeSpec = batchSpec[DependencyEdge]{"dependency_edge", []batchCol[DependencyEdge]{
		col("kind", func(e *DependencyEdge) any { return e.Kind }),
		col("from_node", func(e *DependencyEdge) any { return e.FromNode }),
		col("to_node", func(e *DependencyEdge) any { return e.ToNode }),
		col("origin_file_id", func(e *DependencyEdge) any { return e.OriginFileID }),
		col("confidence", func(e *DependencyEdge) any { return e.Confidence }),
		col("layer", func(e *DependencyEdge) any { return layerOrBase(e.Layer) }),
	}}
)

// txBatches: буферы одной write-транзакции.
type txBatches struct {
	reference      *insertBatch[refRow]
	candidate      *insertBatch[candidateRow]
	resolutionDep  *insertBatch[depRow]
	callEdge       *insertBatch[CallEdge]
	parameter      *insertBatch[paramRow]
	fts            *insertBatch[ftsRow]
	diagnostic     *insertBatch[Diagnostic]
	registerAccess *insertBatch[RegisterAccess]
	queryReference *insertBatch[QueryReference]
	handlerBinding *insertBatch[HandlerBinding]
	roleRight      *insertBatch[RoleRight]
	dependencyEdge *insertBatch[DependencyEdge]
	all            []flusher
}

func newTxBatches() *txBatches {
	b := &txBatches{}
	b.reference = newBatch(&b.all, referenceSpec)
	b.candidate = newBatch(&b.all, candidateSpec, b.reference)
	b.resolutionDep = newBatch(&b.all, resolutionDepSpec, b.reference)
	b.callEdge = newBatch(&b.all, callEdgeSpec, b.reference)
	b.parameter = newBatch(&b.all, parameterSpec)
	b.fts = newBatch(&b.all, ftsSpec)
	b.diagnostic = newBatch(&b.all, diagnosticSpec)
	b.registerAccess = newBatch(&b.all, registerAccessSpec)
	b.queryReference = newBatch(&b.all, queryReferenceSpec)
	b.handlerBinding = newBatch(&b.all, handlerBindingSpec)
	b.roleRight = newBatch(&b.all, roleRightSpec)
	b.dependencyEdge = newBatch(&b.all, dependencyEdgeSpec)
	return b
}

// flushAll сбрасывает все буферы, родителей раньше детей.
func (b *txBatches) flushAll(ctx context.Context, c *conn) error {
	for _, x := range b.all {
		if err := x.flush(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// refIDs выдаёт id строк reference. Id нужен вызывающему сразу (кандидаты,
// зависимости, ребро вызова), а строка ещё в буфере, поэтому он выдаётся здесь
// тем же правилом, что у SQLite для INTEGER PRIMARY KEY без AUTOINCREMENT:
// MAX(id)+1. Писатель один (ADR-014), а вставляет в reference только
// InsertReference (оба условия закреплены TestReferenceIDAllocationGuard),
// поэтому счётчик не расходится с таблицей.
type refIDs struct{ next int64 }

func (a *refIDs) take(ctx context.Context, c *conn) (int64, error) {
	if a.next == 0 {
		maxID, err := c.queryInt(ctx, `SELECT COALESCE(MAX(id),0) FROM reference`)
		if err != nil {
			return 0, err
		}
		a.next = maxID + 1
	}
	id := a.next
	a.next++
	return id, nil
}
