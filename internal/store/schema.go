// Package store — персистентное хранилище индекса одного logical project:
// SQLite-файл на проект, схема раздела 15 архитектуры, эпохи с двухслотовым
// checksummed-указателем (ADR-2 §9), ограниченный пул читателей и единственный
// writer (ADR-2 §7).
//
// Публичная граница пакета: Open / Read / Write / Status / Close
// плюс типизированные выборки и вставки на *ReadTx и *WriteTx. SQL, PRAGMA
// lifecycle, формат указателя, файлы эпох, WAL/checkpoint и drain наружу не
// выходят и жить обязаны только здесь.
package store

// SchemaVersion — версия DDL, которую создаёт этот пакет. Несовместимость
// версии схемы или парсера = новая эпоха с полным rebuild из XML (раздел 15),
// а не попытка «как-нибудь открыть».
//
// 2 — слой у register_access и таблицы объектного графа (§2 спецификации В1).
// 3: DDL прежний; инкремент перестал обрывать указатели нетронутых файлов на
// пересоздаваемые узлы (ADR-037), а индексы версии 2 могли накопить такие
// обрывы и обязаны пересобраться (шаг миграции без DDL, needsFullRebuild).
// 4: DDL прежний; инкремент перестал сносить каскадом строки нетронутых файлов
// (права ролей, рёбра и бейджи объектного графа, ADR-038), тот же шаг без DDL.
// 5: факты HTTP-вызовов и HTTP-сервисов (http_call, http_endpoint), веха В2,
// ADR-039; таблицы пустые до полной пересборки.
// 6: DDL прежний; инкремент перестал оставлять module.owner_object_id висячим
// после удаления XML объекта при живом модуле (issue #14), тот же шаг без DDL.
const SchemaVersion = 6

// createScript — схема раздела 15 архитектуры целиком: единое пространство id в
// node, aspect-модель (module_context/module_code, form_declaration/form_structure),
// настоящие FK с классами ON DELETE, XOR-CHECK-и reference, resolution_dep,
// типизированные связи и FTS5.
//
// Классы ON DELETE (раздел 15) проставлены в комментариях у каждой FK, потому
// что выбор каскада здесь — архитектурное решение, а не деталь: ownership и
// containment сносят факт вместе с источником, soft target обнуляется и требует
// переразрешения, stable identity reference (blob) не трогается вовсе.
const createScript = createScriptBase + objectGraphTables + httpTables

const createScriptBase = `
CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);

-- Журнал публикаций для диагностики; чтение фактов от него не зависит.
CREATE TABLE generation_log(
  id INTEGER PRIMARY KEY,
  created_at INTEGER NOT NULL,
  kind TEXT NOT NULL,
  note TEXT);

CREATE TABLE component(
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  root TEXT NOT NULL,
  applies_to TEXT,
  apply_order INTEGER,
  display TEXT);

-- Порядок колонок значим: чтобы прочитать колонку, SQLite проходит по строке
-- все предыдущие, включая overflow-страницы BLOB. data стоит последней, все
-- служебные колонки — до неё, иначе учёт unreferenced_since поднимает с диска
-- содержимое всех файлов.
CREATE TABLE blob(
  content_hash TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  compressed_size INTEGER NOT NULL,
  unreferenced_since INTEGER,
  data BLOB NOT NULL);

CREATE TABLE source_file(
  id INTEGER PRIMARY KEY,
  component_id TEXT NOT NULL REFERENCES component(id) ON DELETE CASCADE,
  rel_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL,
  -- [stable identity reference] жизнью blob управляет TTL-GC (18.2), каскада быть не должно
  content_hash TEXT NOT NULL REFERENCES blob(content_hash) ON DELETE RESTRICT,
  parser_version INTEGER NOT NULL,
  UNIQUE(component_id, rel_path));

-- Стабильная ЛОГИЧЕСКАЯ identity: без origin_file_id, переживает изменение
-- любого из своих файлов и удаляется reconciliation-шагом (5a/5b раздела 15).
CREATE TABLE node(
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  component_id TEXT NOT NULL REFERENCES component(id) ON DELETE CASCADE,
  identity_key TEXT NOT NULL UNIQUE);

CREATE TABLE module(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  component_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  owner_object_id INTEGER,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL);

-- Аспект свойств модуля из XML (Global, Server, ServerCall, Privileged, ...).
CREATE TABLE module_context(
  module_id INTEGER PRIMARY KEY REFERENCES module(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  props TEXT NOT NULL);

-- Аспект кода: привязка identity модуля к его Module.bsl.
CREATE TABLE module_code(
  module_id INTEGER PRIMARY KEY REFERENCES module(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE);

CREATE TABLE symbol(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  uid TEXT NOT NULL,
  module_id INTEGER NOT NULL REFERENCES module(id) ON DELETE CASCADE,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  is_export INTEGER NOT NULL,
  directive TEXT,
  is_async INTEGER NOT NULL DEFAULT 0,
  byte_start INTEGER NOT NULL,
  byte_end INTEGER NOT NULL,
  start_line INTEGER NOT NULL,
  start_col INTEGER NOT NULL,
  end_line INTEGER NOT NULL,
  end_col INTEGER NOT NULL,
  signature TEXT,
  doc_first_line TEXT,
  region TEXT);

CREATE TABLE parameter(
  symbol_id INTEGER NOT NULL REFERENCES symbol(id) ON DELETE CASCADE,
  ord INTEGER NOT NULL,
  name TEXT NOT NULL,
  by_val INTEGER NOT NULL,
  default_expr TEXT,
  PRIMARY KEY(symbol_id, ord));

CREATE TABLE metadata_object(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  component_id TEXT NOT NULL,
  uuid TEXT,
  mtype TEXT NOT NULL,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  synonym TEXT,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  props TEXT,
  layer TEXT NOT NULL);

CREATE TABLE metadata_member(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  object_id INTEGER NOT NULL REFERENCES metadata_object(id) ON DELETE CASCADE,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  types TEXT,
  indexed INTEGER,
  parent_member INTEGER REFERENCES metadata_member(id) ON DELETE CASCADE);

CREATE TABLE form(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  owner_object_id INTEGER,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL);

-- Пустая форма без form_element всё равно имеет источники через эти два аспекта.
CREATE TABLE form_declaration(
  form_id INTEGER PRIMARY KEY REFERENCES form(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE);

CREATE TABLE form_structure(
  form_id INTEGER PRIMARY KEY REFERENCES form(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE);

CREATE TABLE form_element(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  form_id INTEGER NOT NULL REFERENCES form(id) ON DELETE CASCADE,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  etype TEXT,
  data_path TEXT);

CREATE TABLE form_command(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  form_id INTEGER NOT NULL REFERENCES form(id) ON DELETE CASCADE,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  action_norm TEXT);

CREATE TABLE query(
  id INTEGER PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  symbol_id INTEGER NOT NULL REFERENCES symbol(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  byte_start INTEGER NOT NULL,
  byte_end INTEGER NOT NULL,
  staticity TEXT NOT NULL,
  text TEXT NOT NULL,
  confidence REAL NOT NULL);

CREATE TABLE query_reference(
  id INTEGER PRIMARY KEY,
  query_id INTEGER NOT NULL REFERENCES query(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  name_norm TEXT NOT NULL,
  object_id INTEGER REFERENCES metadata_object(id) ON DELETE SET NULL,
  member_id INTEGER REFERENCES metadata_member(id) ON DELETE SET NULL,
  span_start INTEGER,
  span_end INTEGER);

CREATE TABLE register_access(
  id INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  symbol_id INTEGER REFERENCES symbol(id) ON DELETE CASCADE,
  object_id INTEGER REFERENCES metadata_object(id) ON DELETE SET NULL,
  register_name_norm TEXT NOT NULL,
  mode TEXT NOT NULL,
  in_transaction INTEGER,
  static INTEGER NOT NULL,
  confidence REAL NOT NULL,
  byte_start INTEGER NOT NULL,
  byte_end INTEGER NOT NULL,
  -- Слой факта: 'base' либо id компонента-расширения, та же семантика, что у
  -- dependency_edge.layer. Без него raw и effective неразличимы (D11 в docs/architecture-graph.md).
  layer TEXT NOT NULL DEFAULT 'base',
  CHECK(mode IN ('read','write','movement','clear')));

-- XOR-автомат раздела 15: resolution и target_class — независимые оси,
-- target_class определён ТОЛЬКО при resolved и требует ровно одну цель.
-- ON DELETE SET NULL на цели — это UPDATE, обязанный пройти CHECK: срабатывание
-- на resolved-строке означает пропущенный шаг (1b) и громко валит транзакцию.
CREATE TABLE reference(
  id INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  from_symbol_id INTEGER REFERENCES symbol(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  qualifier_norm TEXT,
  name_norm TEXT NOT NULL,
  resolution TEXT NOT NULL,
  target_class TEXT,
  target_symbol_id INTEGER REFERENCES symbol(id) ON DELETE SET NULL,
  target_object_id INTEGER REFERENCES metadata_object(id) ON DELETE SET NULL,
  platform_key TEXT,
  confidence REAL NOT NULL,
  layer TEXT NOT NULL,
  byte_start INTEGER NOT NULL, byte_end INTEGER NOT NULL,
  start_line INTEGER NOT NULL, start_col INTEGER NOT NULL,
  CHECK(resolution IN ('resolved','ambiguous','unresolved','dynamic')),
  CHECK((resolution = 'resolved') = (target_class IS NOT NULL)),
  CHECK(resolution != 'resolved' OR
        (target_class='symbol'   AND target_symbol_id IS NOT NULL
                                 AND target_object_id IS NULL AND platform_key IS NULL) OR
        (target_class='metadata' AND target_object_id IS NOT NULL
                                 AND target_symbol_id IS NULL AND platform_key IS NULL) OR
        (target_class='platform' AND platform_key IS NOT NULL
                                 AND target_symbol_id IS NULL AND target_object_id IS NULL)),
  CHECK(resolution = 'resolved' OR
        (target_symbol_id IS NULL AND target_object_id IS NULL AND platform_key IS NULL)));

-- Кандидаты ambiguous-разрешения; инвариант validate-шага: у ambiguous
-- МИНИМУМ 2 кандидата (один кандидат обязан стать resolved).
CREATE TABLE reference_candidate(
  ref_id INTEGER NOT NULL REFERENCES reference(id) ON DELETE CASCADE,
  target_node_id INTEGER NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  rank INTEGER NOT NULL,
  reason TEXT,
  PRIMARY KEY(ref_id, target_node_id));

-- Обратный индекс ключей разрешения (18.4): какие references консультировали
-- ключ, ВКЛЮЧАЯ пустые результаты.
CREATE TABLE resolution_dep(
  key_hash TEXT NOT NULL,
  ref_id INTEGER NOT NULL REFERENCES reference(id) ON DELETE CASCADE);

CREATE TABLE call_edge(
  id INTEGER PRIMARY KEY,
  caller_id INTEGER NOT NULL REFERENCES symbol(id) ON DELETE CASCADE,
  callee_id INTEGER REFERENCES symbol(id) ON DELETE SET NULL,
  callee_name_norm TEXT NOT NULL,
  qualifier_norm TEXT,
  kind TEXT NOT NULL,
  resolution TEXT NOT NULL,
  confidence REAL NOT NULL,
  ref_id INTEGER NOT NULL REFERENCES reference(id) ON DELETE CASCADE);

CREATE TABLE handler_binding(
  id INTEGER PRIMARY KEY,
  form_id INTEGER NOT NULL REFERENCES form(id) ON DELETE CASCADE,
  source TEXT NOT NULL,
  event TEXT NOT NULL,
  handler_name_norm TEXT NOT NULL,
  handler_symbol_id INTEGER REFERENCES symbol(id) ON DELETE SET NULL,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  resolution TEXT NOT NULL,
  CHECK(resolution IN ('resolved','ambiguous','unresolved','dynamic')));

CREATE TABLE event_subscription(
  id INTEGER PRIMARY KEY,
  component_id TEXT NOT NULL REFERENCES component(id) ON DELETE CASCADE,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  source_name_norm TEXT,
  event TEXT NOT NULL,
  handler_name_norm TEXT NOT NULL,
  handler_symbol_id INTEGER REFERENCES symbol(id) ON DELETE SET NULL,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  resolution TEXT NOT NULL,
  layer TEXT NOT NULL,
  CHECK(resolution IN ('resolved','ambiguous','unresolved','dynamic')));

CREATE TABLE scheduled_job(
  id INTEGER PRIMARY KEY,
  component_id TEXT NOT NULL REFERENCES component(id) ON DELETE CASCADE,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  method_name_norm TEXT NOT NULL,
  handler_symbol_id INTEGER REFERENCES symbol(id) ON DELETE SET NULL,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  use_flag INTEGER NOT NULL,
  predefined INTEGER NOT NULL,
  resolution TEXT NOT NULL,
  layer TEXT NOT NULL,
  CHECK(resolution IN ('resolved','ambiguous','unresolved','dynamic')));

-- Роль не является node-сущностью (список узловых таблиц раздела 15 закрыт):
-- её identity — имя внутри компонента, а источник один, свой XML.
CREATE TABLE role(
  id INTEGER PRIMARY KEY,
  component_id TEXT NOT NULL REFERENCES component(id) ON DELETE CASCADE,
  name_norm TEXT NOT NULL,
  name_display TEXT NOT NULL,
  object_id INTEGER REFERENCES metadata_object(id) ON DELETE SET NULL,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  layer TEXT NOT NULL,
  UNIQUE(component_id, name_norm));

-- Права читаются по ИЛИ-логике с учётом set_for_new_objects: отсутствие строки
-- НЕ означает отсутствия доступа, поэтому дефолт хранится явно.
-- Расхождение с §15 (владелец-identity RESTRICT) сделано осознанно и по тому же
-- основанию, по которому containment переведено на CASCADE: RESTRICT
-- здесь заблокировал бы одиночный DELETE FROM source_file, когда каскад сносит
-- роль и её права в одной операции — а Rights.xml всегда удаляется целиком.
CREATE TABLE role_right(
  id INTEGER PRIMARY KEY,
  role_id INTEGER NOT NULL REFERENCES role(id) ON DELETE CASCADE,
  object_id INTEGER REFERENCES metadata_object(id) ON DELETE SET NULL,
  object_name_norm TEXT NOT NULL,
  right_name TEXT NOT NULL,
  value INTEGER NOT NULL,
  rls TEXT,
  set_for_new_objects INTEGER NOT NULL,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE);

CREATE TABLE dependency_edge(
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  from_node INTEGER NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  to_node INTEGER NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  origin_file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  confidence REAL NOT NULL,
  layer TEXT NOT NULL);

CREATE TABLE diagnostic(
  id INTEGER PRIMARY KEY,
  file_id INTEGER REFERENCES source_file(id) ON DELETE CASCADE,
  component_id TEXT NOT NULL,
  severity TEXT NOT NULL,
  code TEXT NOT NULL,
  message TEXT NOT NULL,
  byte_start INTEGER,
  byte_end INTEGER,
  start_line INTEGER,
  start_col INTEGER);

-- rowid FTS-строки = symbol.id, поэтому удаление символа стоит один DELETE by rowid.
-- FTS5-таблица транзакционна: read-транзакция вызова видит её согласованно с фактами.
CREATE VIRTUAL TABLE fts_symbols USING fts5(name, signature, doc, tokenize='unicode61');
`

// indexScript — вторичные индексы раздела 15. Индекс по source_file(content_hash)
// обязателен, а не желателен: по нему идёт и fingerprint, и весь учёт blob, без
// него проверка «есть ли ещё ссылки на этот хэш» вырождается в полный скан
// source_file на каждую строку blob.
const indexScript = indexScriptBase + objectGraphIndexes + httpIndexes

const indexScriptBase = `
CREATE INDEX idx_sf_hash ON source_file(content_hash);
CREATE INDEX idx_sf_component ON source_file(component_id);
CREATE INDEX idx_symbol_name ON symbol(name_norm);
CREATE INDEX idx_symbol_module ON symbol(module_id);
CREATE INDEX idx_symbol_file ON symbol(origin_file_id);
CREATE INDEX idx_symbol_uid ON symbol(uid);
CREATE INDEX idx_mo_name ON metadata_object(name_norm);
CREATE INDEX idx_mo_file ON metadata_object(file_id);
CREATE INDEX idx_mm_object ON metadata_member(object_id);
CREATE INDEX idx_mm_file ON metadata_member(origin_file_id);
CREATE INDEX idx_mm_parent ON metadata_member(parent_member);
CREATE INDEX idx_ce_callee ON call_edge(callee_id);
CREATE INDEX idx_ce_name ON call_edge(callee_name_norm, qualifier_norm);
CREATE INDEX idx_ce_caller ON call_edge(caller_id);
CREATE INDEX idx_ce_ref ON call_edge(ref_id);
CREATE INDEX idx_ref_target_symbol ON reference(target_symbol_id);
CREATE INDEX idx_ref_target_object ON reference(target_object_id);
CREATE INDEX idx_ref_file ON reference(file_id);
CREATE INDEX idx_ref_from ON reference(from_symbol_id);
CREATE INDEX idx_ref_name ON reference(name_norm, qualifier_norm);
CREATE INDEX idx_rc_target ON reference_candidate(target_node_id);
CREATE INDEX idx_rdep_key ON resolution_dep(key_hash);
CREATE INDEX idx_rdep_ref ON resolution_dep(ref_id);
CREATE INDEX idx_ra_name ON register_access(register_name_norm, mode);
CREATE INDEX idx_ra_file ON register_access(file_id);
CREATE INDEX idx_ra_symbol ON register_access(symbol_id);
CREATE INDEX idx_ra_object ON register_access(object_id);
CREATE INDEX idx_de_from ON dependency_edge(from_node);
CREATE INDEX idx_de_to ON dependency_edge(to_node);
CREATE INDEX idx_de_origin ON dependency_edge(origin_file_id);
CREATE INDEX idx_mc_file ON module_context(file_id);
CREATE INDEX idx_mcode_file ON module_code(file_id);
CREATE INDEX idx_param_symbol ON parameter(symbol_id);
CREATE INDEX idx_query_symbol ON query(symbol_id);
CREATE INDEX idx_query_file ON query(file_id);
CREATE INDEX idx_qref_query ON query_reference(query_id);
CREATE INDEX idx_qref_object ON query_reference(object_id);
CREATE INDEX idx_qref_member ON query_reference(member_id);
CREATE INDEX idx_fd_file ON form_declaration(file_id);
CREATE INDEX idx_fs_file ON form_structure(file_id);
CREATE INDEX idx_fe_form ON form_element(form_id);
CREATE INDEX idx_fe_file ON form_element(origin_file_id);
CREATE INDEX idx_fcmd_form ON form_command(form_id);
CREATE INDEX idx_fcmd_file ON form_command(origin_file_id);
CREATE INDEX idx_hb_form ON handler_binding(form_id);
CREATE INDEX idx_hb_symbol ON handler_binding(handler_symbol_id);
CREATE INDEX idx_hb_file ON handler_binding(origin_file_id);
CREATE INDEX idx_es_file ON event_subscription(origin_file_id);
CREATE INDEX idx_es_source ON event_subscription(source_name_norm);
CREATE INDEX idx_es_symbol ON event_subscription(handler_symbol_id);
CREATE INDEX idx_sj_file ON scheduled_job(origin_file_id);
CREATE INDEX idx_sj_symbol ON scheduled_job(handler_symbol_id);
CREATE INDEX idx_role_file ON role(file_id);
CREATE INDEX idx_rr_role ON role_right(role_id);
CREATE INDEX idx_rr_object ON role_right(object_id, right_name);
CREATE INDEX idx_rr_name ON role_right(object_name_norm, right_name);
CREATE INDEX idx_rr_file ON role_right(origin_file_id);
CREATE INDEX idx_diag_file ON diagnostic(file_id);
CREATE INDEX idx_node_kind ON node(kind);
CREATE INDEX idx_blob_unref ON blob(unreferenced_since) WHERE unreferenced_since IS NOT NULL;
`

// objectGraphTables — таблицы объектного графа (§2 спецификации В1, контракт
// полей раздела 6.2, имена дословно). Узлами графа являются ТОЛЬКО объекты
// метаданных (решение D6, docs/architecture-graph.md): FK стоят на
// metadata_object, модулю или символу в object_data_edge места нет.
//
// Тот же текст исполняется двумя путями: как часть createScript на новой эпохе
// и как шаг migrations на эпохе версии 1. Общая константа вместо двух копий —
// чтобы мигрированная база не расходилась со свежесозданной.
const objectGraphTables = `
CREATE TABLE object_data_edge(
  id INTEGER PRIMARY KEY,
  from_object_id INTEGER NOT NULL REFERENCES metadata_object(id) ON DELETE CASCADE,
  to_object_id   INTEGER NOT NULL REFERENCES metadata_object(id) ON DELETE CASCADE,
  kind           TEXT NOT NULL,
  layer          TEXT NOT NULL,
  provenance     TEXT NOT NULL,
  confidence     REAL NOT NULL,
  mode           TEXT,
  in_transaction INTEGER,
  evidence       TEXT NOT NULL,
  CHECK(kind IN ('writes-register','reads-register','writes-declared',
                 'reads-query','ref-attribute','creates')),
  CHECK(provenance IN ('code','metadata-declared')));

-- От каких файлов зависит ребро: цепочка атрибуции пересекает несколько файлов,
-- одного origin_file_id не хватает. Каскад по file_id снимает строки вместе с
-- файлом, поэтому пересборка обязана идти через DeleteObjectEdgesForFiles ДО
-- удаления файлов, иначе ребро остаётся без единой зависимости и становится
-- неудаляемым (инвариант object_data_edge_without_dep ловит именно это).
CREATE TABLE object_data_edge_dep(
  edge_id INTEGER NOT NULL REFERENCES object_data_edge(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE);

-- Бейджи узла: дыра атрибуции видима.
CREATE TABLE object_badge(
  object_id INTEGER NOT NULL REFERENCES metadata_object(id) ON DELETE CASCADE,
  badge     TEXT NOT NULL,
  layer     TEXT NOT NULL,
  count     INTEGER NOT NULL,
  PRIMARY KEY(object_id, badge, layer));
`

// objectGraphIndexes — индексы §2. idx_odedep_file несёт инкрементальную
// пересборку (найти рёбра затронутых файлов), idx_odedep_edge — обратный ход
// evidence и каскад.
const objectGraphIndexes = `
CREATE INDEX idx_ode_from ON object_data_edge(from_object_id, kind);
CREATE INDEX idx_ode_to ON object_data_edge(to_object_id, kind);
CREATE INDEX idx_ode_kind_layer ON object_data_edge(kind, layer);
CREATE INDEX idx_odedep_file ON object_data_edge_dep(file_id);
CREATE INDEX idx_odedep_edge ON object_data_edge_dep(edge_id);
`

// nodeSubtypes — таблицы, PK которых равен node.id (раздел 15). Список закрыт:
// по нему строятся orphan-node sweep и его инвариант, и появление новой узловой
// таблицы обязано менять именно его, а не отдельные запросы в разных местах.
var nodeSubtypes = []struct{ kind, table string }{
	{"module", "module"},
	{"symbol", "symbol"},
	{"metadata_object", "metadata_object"},
	{"metadata_member", "metadata_member"},
	{"form", "form"},
	{"form_element", "form_element"},
	{"form_command", "form_command"},
	{"query", "query"},
}

// orphanNodeSQL — (5b) orphan-node sweep: node без subtype-строки своего kind.
// PRAGMA foreign_key_check таких не ловит: node без subtype-строки не нарушает FK.
func orphanNodeSQL(selectWhat string) string {
	s := "SELECT " + selectWhat + " FROM node n WHERE NOT EXISTS (\n"
	for i, st := range nodeSubtypes {
		if i > 0 {
			s += "  UNION ALL "
		} else {
			s += "  "
		}
		s += "SELECT 1 FROM " + st.table + " t WHERE t.id = n.id AND n.kind='" + st.kind + "'\n"
	}
	return s + ")"
}

// validateQueries — инварианты раздела 15, которые PRAGMA foreign_key_check не
// ловит. Каждый запрос обязан вернуть 0.
var validateQueries = map[string]string{
	"orphan_nodes": orphanNodeSQL("COUNT(*)"),
	// (5a) identity без единого аспекта-источника.
	"module_without_aspect": `
		SELECT COUNT(*) FROM module m
		WHERE NOT EXISTS(SELECT 1 FROM module_context c WHERE c.module_id=m.id)
		  AND NOT EXISTS(SELECT 1 FROM module_code    c WHERE c.module_id=m.id)`,
	"form_without_aspect": `
		SELECT COUNT(*) FROM form f
		WHERE NOT EXISTS(SELECT 1 FROM form_declaration d WHERE d.form_id=f.id)
		  AND NOT EXISTS(SELECT 1 FROM form_structure   s WHERE s.form_id=f.id)`,
	// subtype-строка с node чужого kind.
	"subtype_kind_mismatch": `
		SELECT (SELECT COUNT(*) FROM symbol          s JOIN node n ON n.id=s.id WHERE n.kind!='symbol')
		     + (SELECT COUNT(*) FROM module          m JOIN node n ON n.id=m.id WHERE n.kind!='module')
		     + (SELECT COUNT(*) FROM metadata_object o JOIN node n ON n.id=o.id WHERE n.kind!='metadata_object')
		     + (SELECT COUNT(*) FROM metadata_member e JOIN node n ON n.id=e.id WHERE n.kind!='metadata_member')
		     + (SELECT COUNT(*) FROM form            f JOIN node n ON n.id=f.id WHERE n.kind!='form')
		     + (SELECT COUNT(*) FROM form_element    x JOIN node n ON n.id=x.id WHERE n.kind!='form_element')
		     + (SELECT COUNT(*) FROM form_command    c JOIN node n ON n.id=c.id WHERE n.kind!='form_command')
		     + (SELECT COUNT(*) FROM query           q JOIN node n ON n.id=q.id WHERE n.kind!='query')`,
	// ambiguous обязан иметь минимум 2 кандидата.
	"ambiguous_without_candidates": `
		SELECT COUNT(*) FROM reference r WHERE r.resolution='ambiguous'
		  AND (SELECT COUNT(*) FROM reference_candidate c WHERE c.ref_id=r.id) < 2`,
	// XOR-автомат (дублирует CHECK-и: ловит строки, вставленные до их появления).
	"reference_xor_violation": `
		SELECT COUNT(*) FROM reference
		WHERE ((resolution='resolved') != (target_class IS NOT NULL))
		   OR (resolution!='resolved' AND (target_symbol_id IS NOT NULL
		        OR target_object_id IS NOT NULL OR platform_key IS NOT NULL))`,
	// call_edge, потерявшее свой reference.
	"call_edge_without_ref": `
		SELECT COUNT(*) FROM call_edge c
		WHERE NOT EXISTS(SELECT 1 FROM reference r WHERE r.id=c.ref_id)`,
	// blob со ссылками, но помеченный как неиспользуемый.
	"blob_marked_but_referenced": `
		SELECT COUNT(*) FROM blob b WHERE b.unreferenced_since IS NOT NULL
		  AND EXISTS(SELECT 1 FROM source_file f WHERE f.content_hash=b.content_hash)`,
	// blob без метки и без ссылок: такой не попадёт под TTL-GC и останется навсегда.
	"blob_unmarked_and_unreferenced": `
		SELECT COUNT(*) FROM blob b WHERE b.unreferenced_since IS NULL
		  AND NOT EXISTS(SELECT 1 FROM source_file f WHERE f.content_hash=b.content_hash)`,
	// Висячие рёбра и бейджи (ссылка на несуществующий metadata_object) здесь
	// НЕ проверяются: их полностью ловит pragma_foreign_key_check, который
	// validate прогоняет рядом, — в том числе строки, вставленные при
	// выключенном foreign_keys. Карта обещает инварианты, которых он не видит,
	// и дубль в ней стоил бы ровно столько же, сколько стоит доверие к её
	// заголовку.
	//
	// Ребро без единой файловой зависимости неудаляемо: пересборка ищет рёбра
	// по файлам, и такое ребро переживёт любую переиндексацию своего источника.
	"object_data_edge_without_dep": `
		SELECT COUNT(*) FROM object_data_edge e
		WHERE NOT EXISTS(SELECT 1 FROM object_data_edge_dep d WHERE d.edge_id=e.id)`,
}
