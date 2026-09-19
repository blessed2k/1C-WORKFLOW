package store

import "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// Файл: факты HTTP (веха В2, решение D10, ADR-039): исходящие вызовы
// HTTPСоединение в коде (http_call) и методы HTTP-сервисов в метаданных
// (http_endpoint). Сшивка вызовов с сервисами идёт при чтении и через
// границу индексов (сервис лежит в индексе другой базы), поэтому здесь
// только хранение и выборки. SQL живёт только здесь (RuleSQLOnlyInStore).

// httpTables: таблицы фактов HTTP. Обе держатся за свой файл (каскад по
// file_id), вызов ещё и за метод (symbol того же файла): узлов чужих файлов
// они не касаются, и снимок ADR-037/038 им не нужен. Объект-сервис не
// хранится ссылкой: это metadata_object того же XML (file_id), и чтение
// находит его соединением, а не FK, которую пришлось бы возвращать при
// переопубликации.
const httpTables = `
CREATE TABLE http_endpoint(
  id INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  root_url TEXT NOT NULL,
  template_name TEXT NOT NULL,
  template TEXT NOT NULL,
  method_name TEXT NOT NULL,
  http_method TEXT NOT NULL,
  handler TEXT NOT NULL,
  layer TEXT NOT NULL);

CREATE TABLE http_call(
  id INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL REFERENCES source_file(id) ON DELETE CASCADE,
  symbol_id INTEGER REFERENCES symbol(id) ON DELETE CASCADE,
  verb TEXT NOT NULL,
  host TEXT NOT NULL,
  host_static INTEGER NOT NULL,
  path TEXT NOT NULL,
  path_kind TEXT NOT NULL,
  path_suffix TEXT NOT NULL,
  path_anchored INTEGER NOT NULL,
  dynamic_reason TEXT NOT NULL,
  confidence REAL NOT NULL,
  byte_start INTEGER NOT NULL,
  byte_end INTEGER NOT NULL,
  start_line INTEGER NOT NULL,
  layer TEXT NOT NULL,
  CHECK(path_kind IN ('static','prefix','dynamic')));
`

const httpIndexes = `
CREATE INDEX idx_http_endpoint_file ON http_endpoint(file_id);
CREATE INDEX idx_http_call_file ON http_call(file_id);
`

// HTTPEndpoint: метод шаблона URL HTTP-сервиса. Шаблон без методов
// хранится строкой с пустыми MethodName/HTTPMethod/Handler: он адресуем по
// пути, хоть и не обрабатывает ни одного метода.
type HTTPEndpoint struct {
	FileID       int64
	RootURL      string
	TemplateName string
	Template     string
	MethodName   string
	HTTPMethod   string
	Handler      string
	Layer        string
}

// InsertHTTPEndpoint добавляет метод HTTP-сервиса.
func (tx *WriteTx) InsertHTTPEndpoint(e HTTPEndpoint) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO http_endpoint(file_id,root_url,template_name,template,
		method_name,http_method,handler,layer) VALUES(?,?,?,?,?,?,?,?)`,
		e.FileID, e.RootURL, e.TemplateName, e.Template, e.MethodName, e.HTTPMethod, e.Handler, layerOrBase(e.Layer))
}

// HTTPCall: исходящий HTTP-вызов в коде.
type HTTPCall struct {
	domain.HTTPTarget
	FileID     int64
	SymbolID   int64 // 0: вызов вне метода
	Confidence float64
	Span       domain.Span
	Layer      string
}

// InsertHTTPCall добавляет HTTP-вызов.
func (tx *WriteTx) InsertHTTPCall(c HTTPCall) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO http_call(file_id,symbol_id,verb,host,host_static,path,path_kind,
		path_suffix,path_anchored,dynamic_reason,confidence,byte_start,byte_end,start_line,layer)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.FileID, nullID(c.SymbolID), c.Verb, c.Host, boolInt(c.HostStatic), c.Path, string(c.PathKind), c.PathSuffix,
		boolInt(c.PathAnchored), c.DynamicReason, c.Confidence, c.Span.StartByte, c.Span.EndByte, c.Span.StartLine, layerOrBase(c.Layer))
}

// HTTPEndpointRow: метод сервиса вместе с объектом-сервисом.
type HTTPEndpointRow struct {
	ID int64
	HTTPEndpoint
	ServiceID      int64 // metadata_object HTTP-сервиса; 0, если объекта нет
	ServiceName    string
	ComponentID    string
	HandlerFile    string // rel_path XML сервиса
	ServiceDisplay string
}

// HTTPEndpoints: все методы HTTP-сервисов проекта.
func (tx *ReadTx) HTTPEndpoints() ([]HTTPEndpointRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT e.id, e.file_id, e.root_url, e.template_name, e.template,
			e.method_name, e.http_method, e.handler, e.layer,
			COALESCE(o.id,0), COALESCE(o.name_norm,''), COALESCE(o.name_display,''), f.component_id, f.rel_path
		FROM http_endpoint e
		JOIN source_file f ON f.id = e.file_id
		LEFT JOIN metadata_object o ON o.file_id = e.file_id AND o.mtype = 'HTTPService'
		ORDER BY e.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HTTPEndpointRow
	for rows.Next() {
		var r HTTPEndpointRow
		if err := rows.Scan(&r.ID, &r.FileID, &r.RootURL, &r.TemplateName, &r.Template,
			&r.MethodName, &r.HTTPMethod, &r.Handler, &r.Layer,
			&r.ServiceID, &r.ServiceName, &r.ServiceDisplay, &r.ComponentID, &r.HandlerFile); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HTTPCallRow: вызов вместе с местом в коде и модулем, в котором он
// написан.
type HTTPCallRow struct {
	ID int64
	HTTPCall
	RelPath     string
	ComponentID string
	SymbolName  string
	// ModuleKind и ModuleOwnerID: модуль файла вызова и его объект-владелец
	// (module.owner_object_id); 0, если владельца нет.
	ModuleKind    string
	ModuleOwnerID int64
}

// HTTPCalls: все HTTP-вызовы проекта.
func (tx *ReadTx) HTTPCalls() ([]HTTPCallRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT c.id, c.file_id, COALESCE(c.symbol_id,0), c.verb, c.host, c.host_static,
			c.path, c.path_kind, c.path_suffix, c.path_anchored, c.dynamic_reason, c.confidence, c.byte_start, c.byte_end, c.start_line, c.layer,
			f.rel_path, f.component_id, COALESCE(s.name_display,''),
			COALESCE(m.kind,''), COALESCE(m.owner_object_id,0)
		FROM http_call c
		JOIN source_file f ON f.id = c.file_id
		LEFT JOIN symbol s ON s.id = c.symbol_id
		LEFT JOIN module_code mc ON mc.file_id = c.file_id
		LEFT JOIN module m ON m.id = mc.module_id
		ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HTTPCallRow
	for rows.Next() {
		var r HTTPCallRow
		var hostStatic, anchored int
		if err := rows.Scan(&r.ID, &r.FileID, &r.SymbolID, &r.Verb, &r.Host, &hostStatic,
			&r.Path, &r.PathKind, &r.PathSuffix, &anchored, &r.DynamicReason, &r.Confidence, &r.Span.StartByte, &r.Span.EndByte, &r.Span.StartLine, &r.Layer,
			&r.RelPath, &r.ComponentID, &r.SymbolName, &r.ModuleKind, &r.ModuleOwnerID); err != nil {
			return nil, err
		}
		r.HostStatic, r.PathAnchored = hostStatic != 0, anchored != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// CallersOfSymbols: вызывающие каждого из символов (обратные рёбра call_edge,
// как CallEdgesTo) одним запросом на весь список. Порядок внутри символа тот
// же, что у CallEdgesTo (по id ребра): обход атрибуции режет список по
// потолку, и срез обязан быть тем же, что при точечном чтении.
func (tx *ReadTx) CallersOfSymbols(symbolIDs []int64) (map[int64][]CallEdgeRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	out := make(map[int64][]CallEdgeRow, len(symbolIDs))
	if len(symbolIDs) == 0 {
		return out, nil
	}
	rows, err := tx.c.query(tx.ctx, `SELECT `+callEdgeSelectColumns+` FROM call_edge
		WHERE callee_id IN (SELECT value FROM json_each(?)) ORDER BY id`, int64ListJSON(symbolIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanCallEdgeRow(rows)
		if err != nil {
			return nil, err
		}
		out[e.CalleeID] = append(out[e.CalleeID], e)
	}
	return out, rows.Err()
}

// SymbolModuleOwner: файл символа и модуль этого файла с объектом-владельцем.
type SymbolModuleOwner struct {
	SymbolID      int64
	FileID        int64
	ModuleKind    string // пусто, если у файла нет модуля
	OwnerObjectID int64
}

// SymbolModuleOwners: то же, что SymbolByID + ModuleByFile, одним запросом на
// список символов. Символа нет в индексе: его нет и в ответе.
func (tx *ReadTx) SymbolModuleOwners(symbolIDs []int64) (map[int64]SymbolModuleOwner, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	out := make(map[int64]SymbolModuleOwner, len(symbolIDs))
	if len(symbolIDs) == 0 {
		return out, nil
	}
	rows, err := tx.c.query(tx.ctx, `SELECT s.id, s.origin_file_id, COALESCE(mo.kind,''), COALESCE(mo.owner_object_id,0)
		FROM symbol s
		LEFT JOIN module_code mc ON mc.file_id = s.origin_file_id
		LEFT JOIN module mo ON mo.id = mc.module_id
		WHERE s.id IN (SELECT value FROM json_each(?))`, int64ListJSON(symbolIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r SymbolModuleOwner
		if err := rows.Scan(&r.SymbolID, &r.FileID, &r.ModuleKind, &r.OwnerObjectID); err != nil {
			return nil, err
		}
		out[r.SymbolID] = r
	}
	return out, rows.Err()
}
