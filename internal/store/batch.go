package store

import (
	"context"
	"fmt"
	"strings"
)

// Пакетная вставка (issue #3, шаг 2). Полная пересборка пишет миллионы строк
// ссылок, зависимостей разрешения и рёбер вызовов; одна строка на INSERT
// платит полный обход движка SQLite на каждую. Строки листовых таблиц (на них
// не ссылается ни одна таблица, кроме таких же листовых) копятся в буфере и
// уходят многострочным INSERT.
//
// Видимость не меняется: любой вызов транзакции, который читает, удаляет или
// обновляет (всё, что идёт через ReadTx.check), сначала сбрасывает буферы.
// Мимо сброса идут только вставки и выборки из таблиц, которые в буфер не
// попадают никогда (node, source_file, role): им буфер не виден по построению.
// Внешние ключи проверяются немедленно, поэтому родительский буфер (reference)
// сбрасывается раньше дочерних.

// batchRows: строк в одном многострочном INSERT. 128 строк по 16 колонок это
// 2048 параметров, далеко от лимита SQLite на число переменных.
const batchRows = 128

// insertBatch: буфер одной таблицы.
type insertBatch struct {
	head    string // "INSERT INTO t(a,b) VALUES"
	one     string // "(?,?)"
	ncol    int
	args    []any
	rows    int
	parents []*insertBatch // сбрасываются раньше этого буфера
	full    string         // текст на batchRows строк, собирается один раз
}

func newInsertBatch(table, cols string, parents ...*insertBatch) *insertBatch {
	ncol := strings.Count(cols, ",") + 1
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", ncol), ",") + ")"
	return &insertBatch{
		head: "INSERT INTO " + table + "(" + cols + ") VALUES", one: one, ncol: ncol,
		args: make([]any, 0, batchRows*ncol), parents: parents,
	}
}

func (b *insertBatch) text(n int) string {
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
func (b *insertBatch) add(ctx context.Context, c *conn, args ...any) error {
	if len(args) != b.ncol {
		return fmt.Errorf("%s: %d значений на %d колонок", b.head, len(args), b.ncol)
	}
	b.args = append(b.args, args...)
	b.rows++
	if b.rows < batchRows {
		return nil
	}
	return b.flush(ctx, c)
}

// flush отправляет накопленное. Полный пакет идёт через кэш подготовленных
// выражений (текст один на таблицу), хвост переменной длины мимо кэша.
func (b *insertBatch) flush(ctx context.Context, c *conn) error {
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
		return fmt.Errorf("пакетная вставка (%s): %w", b.head, err)
	}
	return nil
}

// txBatches: буферы одной write-транзакции в порядке сброса.
type txBatches struct {
	reference      *insertBatch
	candidate      *insertBatch
	resolutionDep  *insertBatch
	callEdge       *insertBatch
	parameter      *insertBatch
	fts            *insertBatch
	diagnostic     *insertBatch
	registerAccess *insertBatch
	queryReference *insertBatch
	handlerBinding *insertBatch
	roleRight      *insertBatch
	dependencyEdge *insertBatch
	all            []*insertBatch
	// nextRefID: следующий id строки reference. Id нужен вызывающему сразу
	// (кандидаты, зависимости, ребро вызова), а строка ещё в буфере, поэтому
	// он выдаётся здесь тем же правилом, что у SQLite без AUTOINCREMENT:
	// MAX(id)+1. Писатель один (ADR-014), и вставляет в reference только
	// InsertReference, поэтому счётчик не расходится с таблицей.
	nextRefID int64
}

func newTxBatches() *txBatches {
	ref := newInsertBatch("reference", "id,file_id,from_symbol_id,kind,qualifier_norm,name_norm,"+
		"resolution,target_class,target_symbol_id,target_object_id,platform_key,confidence,layer,"+
		"byte_start,byte_end,start_line,start_col")
	b := &txBatches{
		reference:      ref,
		candidate:      newInsertBatch("reference_candidate", "ref_id,target_node_id,rank,reason", ref),
		resolutionDep:  newInsertBatch("resolution_dep", "key_hash,ref_id", ref),
		callEdge:       newInsertBatch("call_edge", "caller_id,callee_id,callee_name_norm,qualifier_norm,kind,resolution,confidence,ref_id", ref),
		parameter:      newInsertBatch("parameter", "symbol_id,ord,name,by_val,default_expr"),
		fts:            newInsertBatch("fts_symbols", "rowid,name,signature,doc"),
		diagnostic:     newInsertBatch("diagnostic", "file_id,component_id,severity,code,message,byte_start,byte_end,start_line,start_col"),
		registerAccess: newInsertBatch("register_access", "file_id,symbol_id,object_id,register_name_norm,mode,in_transaction,static,confidence,byte_start,byte_end,layer"),
		queryReference: newInsertBatch("query_reference", "query_id,kind,name_norm,object_id,member_id,span_start,span_end"),
		handlerBinding: newInsertBatch("handler_binding", "form_id,source,event,handler_name_norm,handler_symbol_id,origin_file_id,resolution"),
		roleRight:      newInsertBatch("role_right", "role_id,object_id,object_name_norm,right_name,value,rls,set_for_new_objects,origin_file_id"),
		dependencyEdge: newInsertBatch("dependency_edge", "kind,from_node,to_node,origin_file_id,confidence,layer"),
	}
	b.all = []*insertBatch{b.reference, b.candidate, b.resolutionDep, b.callEdge, b.parameter, b.fts,
		b.diagnostic, b.registerAccess, b.queryReference, b.handlerBinding, b.roleRight, b.dependencyEdge}
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
