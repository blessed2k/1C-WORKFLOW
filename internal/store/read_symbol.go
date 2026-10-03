package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Read-сторона символьных/графовых инструментов. ReadTx до неё
// нёс только Meta/GenerationNumber/Blob/SourceFileID/NodeID/Validate
// — этого достаточно для index_status, но find_symbol/get_symbol/
// get_module_structure/find_references/trace_call_graph читают таблицы
// symbol/parameter/module/reference/reference_candidate/call_edge, для
// которых до сих пор не было ни одной типизированной выборки (расширение
// контракта ReadTx принадлежит тому инструменту, которому оно физически
// нужно первым). SQL живёт
// здесь, как и требует правило пакета: cmd/mcp1c и internal/app SQL не пишут.

// Generation читает текущее поколение индекса (epoch.number) той же
// read-транзакцией, что и остальные выборки вызова — используется каждым
// инструментом-фасадом для заполнения Response.Generation без второго похода
// в store вне транзакции.
func (tx *ReadTx) Generation() (domain.Generation, error) {
	if err := tx.check(); err != nil {
		return "", err
	}
	epochStr, err := tx.Meta(metaEpoch)
	if err != nil {
		return "", err
	}
	epoch, err := strconv.ParseUint(epochStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("meta %s=%q: %w", metaEpoch, epochStr, err)
	}
	num, err := tx.GenerationNumber()
	if err != nil {
		return "", err
	}
	return domain.NewGeneration(epoch, uint64(num)), nil
}

// SourceFileRow — строка source_file, нужная вызывающим целиком (content_hash
// для чтения тела из blob и для сверки staleAgainstDisk, rel_path/component
// для resource URI onec://src/...).
type SourceFileRow struct {
	ID          int64
	ComponentID string
	RelPath     string
	ContentHash string
	Size        int64
}

// SourceFileByID читает метаданные файла по его store-id (обратная сторона
// SourceFileID: там — путь -> id, здесь — id -> путь+hash).
func (tx *ReadTx) SourceFileByID(id int64) (SourceFileRow, bool, error) {
	if err := tx.check(); err != nil {
		return SourceFileRow{}, false, err
	}
	var r SourceFileRow
	err := tx.c.sc.QueryRowContext(tx.ctx,
		`SELECT id, component_id, rel_path, content_hash, size FROM source_file WHERE id=?`, id).
		Scan(&r.ID, &r.ComponentID, &r.RelPath, &r.ContentHash, &r.Size)
	if err == sql.ErrNoRows {
		return SourceFileRow{}, false, nil
	}
	if err != nil {
		return SourceFileRow{}, false, err
	}
	return r, true, nil
}

// SymbolRow — символ (процедура/функция/переменная модуля), спроецированный
// из symbol JOIN module JOIN source_file: вызывающему нужен путь модуля и
// component_id, а не только module_id/origin_file_id.
type SymbolRow struct {
	ID           int64
	UID          string
	ComponentID  string
	ModulePath   string
	OriginFileID int64
	Kind         string
	NameNorm     string
	NameDisplay  string
	IsExport     bool
	IsAsync      bool
	Directive    string
	Span         domain.Span
	Signature    string
	DocFirstLine string
	Region       string
}

const symbolSelectColumns = `s.id, s.uid, mo.component_id, sf.rel_path, s.origin_file_id, s.kind, s.name_norm,
	s.name_display, s.is_export, s.is_async, s.directive,
	s.byte_start, s.byte_end, s.start_line, s.start_col, s.end_line, s.end_col,
	s.signature, s.doc_first_line, s.region`

const symbolFromJoin = `symbol s JOIN module mo ON mo.id=s.module_id JOIN source_file sf ON sf.id=s.origin_file_id`

func scanSymbolRow(rows *sql.Rows) (SymbolRow, error) {
	var r SymbolRow
	var isExport, isAsync int64
	var directive, signature, doc, region sql.NullString
	err := rows.Scan(&r.ID, &r.UID, &r.ComponentID, &r.ModulePath, &r.OriginFileID, &r.Kind, &r.NameNorm,
		&r.NameDisplay, &isExport, &isAsync, &directive,
		&r.Span.StartByte, &r.Span.EndByte, &r.Span.StartLine, &r.Span.StartCol, &r.Span.EndLine, &r.Span.EndCol,
		&signature, &doc, &region)
	if err != nil {
		return SymbolRow{}, err
	}
	r.IsExport, r.IsAsync = isExport != 0, isAsync != 0
	r.Directive, r.Signature, r.DocFirstLine, r.Region = directive.String, signature.String, doc.String, region.String
	return r, nil
}

// SymbolByID читает один символ по его store-id (получаемому, например, через
// NodeID(uid) — symbolIdentityKey(uid) равен самому uid, поэтому NodeID уже
// умеет резолвить uid -> symbol.id без нового примитива).
func (tx *ReadTx) SymbolByID(id int64) (SymbolRow, bool, error) {
	if err := tx.check(); err != nil {
		return SymbolRow{}, false, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT `+symbolSelectColumns+` FROM `+symbolFromJoin+` WHERE s.id=?`, id)
	if err != nil {
		return SymbolRow{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return SymbolRow{}, false, rows.Err()
	}
	r, err := scanSymbolRow(rows)
	if err != nil {
		return SymbolRow{}, false, err
	}
	return r, true, rows.Err()
}

// SymbolByFileAndName ищет символ по файлу его модуля и точному нормальному
// имени — вторая (не-uid) форма адресации get_symbol: «module+name».
func (tx *ReadTx) SymbolByFileAndName(fileID int64, nameNorm string) (SymbolRow, bool, error) {
	if err := tx.check(); err != nil {
		return SymbolRow{}, false, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT `+symbolSelectColumns+` FROM `+symbolFromJoin+`
		WHERE s.origin_file_id=? AND s.name_norm=?`, fileID, nameNorm)
	if err != nil {
		return SymbolRow{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return SymbolRow{}, false, rows.Err()
	}
	r, err := scanSymbolRow(rows)
	if err != nil {
		return SymbolRow{}, false, err
	}
	return r, true, rows.Err()
}

// SymbolsByOriginFile перечисляет все символы (методы и переменные), прямо
// определённые в файле — get_module_structure: тело модуля НЕ читается,
// только уже разобранные факты, отсортированные по месту в файле.
func (tx *ReadTx) SymbolsByOriginFile(fileID int64) ([]SymbolRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT `+symbolSelectColumns+` FROM `+symbolFromJoin+`
		WHERE s.origin_file_id=? ORDER BY s.byte_start, s.id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolRow
	for rows.Next() {
		r, err := scanSymbolRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ModuleRow — identity-строка модуля (module JOIN module_code): владелец,
// вид, отображаемое имя.
type ModuleRow struct {
	ID            int64
	ComponentID   string
	Kind          string
	OwnerObjectID int64
	NameNorm      string
	NameDisplay   string
}

// ModuleByFile находит модуль по file_id его аспекта кода (module_code) —
// тот же файл, что отдаёт SourceFileID(component, modulePath).
func (tx *ReadTx) ModuleByFile(fileID int64) (ModuleRow, bool, error) {
	if err := tx.check(); err != nil {
		return ModuleRow{}, false, err
	}
	var r ModuleRow
	var ownerID sql.NullInt64
	err := tx.c.sc.QueryRowContext(tx.ctx, `SELECT mo.id, mo.component_id, mo.kind, mo.owner_object_id,
		mo.name_norm, mo.name_display FROM module mo JOIN module_code mc ON mc.module_id=mo.id
		WHERE mc.file_id=?`, fileID).Scan(&r.ID, &r.ComponentID, &r.Kind, &ownerID, &r.NameNorm, &r.NameDisplay)
	if err == sql.ErrNoRows {
		return ModuleRow{}, false, nil
	}
	if err != nil {
		return ModuleRow{}, false, err
	}
	r.OwnerObjectID = ownerID.Int64
	return r, true, nil
}

// ModuleFilesByOwnerObject — файлы кода ВСЕХ модулей, принадлежащих объекту
// метаданных (module.owner_object_id): у одного объекта их несколько (модуль
// объекта, модуль менеджера, модули форм и команд). Обратная сторона
// ModuleByFile.
//
// Нужна публикации объектного графа (internal/index): счётчик бейджа
// object_badge ЗАМЕЩАЕТСЯ, а не складывается, поэтому пересборка владельца
// обязана видеть все его строки register_access целиком, а не только те,
// что пришли из изменившегося файла. Без этой выборки «сколько у объекта
// неприписанных записей» пересчитывался бы по части модулей и выглядел бы
// уменьшившейся дырой, а не недосчитанной.
func (tx *ReadTx) ModuleFilesByOwnerObject(objectID int64) ([]int64, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT mc.file_id FROM module mo JOIN module_code mc ON mc.module_id=mo.id
		WHERE mo.owner_object_id=? ORDER BY mc.file_id`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SymbolSearch — параметры find_symbol: имя (подстрока по умолчанию — так
// один вызов покрывает и точное совпадение, и подстроку), необязательные
// kind/component/module (последний — как origin_file_id, уже разрешённый
// вызывающим через SourceFileID), keyset-курсор (AfterName, AfterID).
type SymbolSearch struct {
	NameNorm     string
	Kind         string
	ComponentID  string
	ModuleFileID int64
	AfterName    string
	AfterID      int64
	Limit        int
}

// FindSymbols ищет символы по подстроке нормализованного имени с опциональными
// фильтрами, упорядоченно по (name_norm, id) — стабильный порядок для
// keyset-пагинации. Возвращает не больше Limit строк.
func (tx *ReadTx) FindSymbols(q SymbolSearch) ([]SymbolRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	var where []string
	var args []any
	if q.NameNorm != "" {
		where = append(where, "s.name_norm LIKE ?")
		args = append(args, "%"+q.NameNorm+"%")
	}
	if q.Kind != "" {
		where = append(where, "s.kind=?")
		args = append(args, q.Kind)
	}
	if q.ComponentID != "" {
		where = append(where, "mo.component_id=?")
		args = append(args, q.ComponentID)
	}
	if q.ModuleFileID != 0 {
		where = append(where, "s.origin_file_id=?")
		args = append(args, q.ModuleFileID)
	}
	if q.AfterName != "" {
		where = append(where, "(s.name_norm > ? OR (s.name_norm = ? AND s.id > ?))")
		args = append(args, q.AfterName, q.AfterName, q.AfterID)
	}
	sqlText := `SELECT ` + symbolSelectColumns + ` FROM ` + symbolFromJoin
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY s.name_norm, s.id LIMIT ?"
	args = append(args, q.Limit)
	rows, err := tx.c.query(tx.ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolRow
	for rows.Next() {
		r, err := scanSymbolRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ParameterRow — параметр символа со своим значением по умолчанию.
type ParameterRow struct {
	Ord         int
	Name        string
	ByVal       bool
	DefaultExpr string
}

// SymbolDoc читает полный комментарий символа. Пусто: комментария нет либо
// индекс собран до схемы 8. В общий набор колонок символа комментарий не
// входит: он длинный, а списки символов его не показывают.
func (tx *ReadTx) SymbolDoc(symbolID int64) (string, error) {
	if err := tx.check(); err != nil {
		return "", err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT doc FROM symbol WHERE id=?`, symbolID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var doc sql.NullString
	if rows.Next() {
		if err := rows.Scan(&doc); err != nil {
			return "", err
		}
	}
	return doc.String, rows.Err()
}

// SymbolParameters читает параметры символа по порядку (ord).
func (tx *ReadTx) SymbolParameters(symbolID int64) ([]ParameterRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx,
		`SELECT ord, name, by_val, default_expr FROM parameter WHERE symbol_id=? ORDER BY ord`, symbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ParameterRow
	for rows.Next() {
		var p ParameterRow
		var byVal int64
		var def sql.NullString
		if err := rows.Scan(&p.Ord, &p.Name, &byVal, &def); err != nil {
			return nil, err
		}
		p.ByVal, p.DefaultExpr = byVal != 0, def.String
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReferenceRow — ссылка вместе с местом (компонент/модуль файла-источника),
// нужным для группировки find_references по модулю. Координаты неполны по
// схеме раздела 15: reference хранит только byte_start/byte_end/start_line/
// start_col (end_line/end_col не персистентны: упрощение схемы).
// EndLine/EndCol здесь достраиваются равными Start*, что валидно для
// domain.Span.Validate(), но не является настоящим концом фрагмента.
type ReferenceRow struct {
	ID             int64
	FileID         int64
	ComponentID    string
	ModulePath     string
	FromSymbolID   int64
	Kind           string
	QualifierNorm  string
	NameNorm       string
	Resolution     string
	TargetClass    string
	TargetSymbolID int64
	TargetObjectID int64
	PlatformKey    string
	Confidence     float64
	Layer          string
	Span           domain.Span
}

const referenceSelectColumns = `r.id, r.file_id, sf.component_id, sf.rel_path, r.from_symbol_id, r.kind,
	r.qualifier_norm, r.name_norm, r.resolution, r.target_class, r.target_symbol_id, r.target_object_id,
	r.platform_key, r.confidence, r.layer, r.byte_start, r.byte_end, r.start_line, r.start_col`

const referenceFromJoin = `reference r JOIN source_file sf ON sf.id=r.file_id`

func scanReferenceRow(rows *sql.Rows) (ReferenceRow, error) {
	var r ReferenceRow
	var fromSymbolID, targetSymbolID, targetObjectID sql.NullInt64
	var qualifier, targetClass, platformKey sql.NullString
	err := rows.Scan(&r.ID, &r.FileID, &r.ComponentID, &r.ModulePath, &fromSymbolID, &r.Kind,
		&qualifier, &r.NameNorm, &r.Resolution, &targetClass, &targetSymbolID, &targetObjectID,
		&platformKey, &r.Confidence, &r.Layer,
		&r.Span.StartByte, &r.Span.EndByte, &r.Span.StartLine, &r.Span.StartCol)
	if err != nil {
		return ReferenceRow{}, err
	}
	r.FromSymbolID, r.TargetSymbolID, r.TargetObjectID = fromSymbolID.Int64, targetSymbolID.Int64, targetObjectID.Int64
	r.QualifierNorm, r.TargetClass, r.PlatformKey = qualifier.String, targetClass.String, platformKey.String
	// см. doc-комментарий ReferenceRow: конец фрагмента не персистентен.
	r.Span.EndLine, r.Span.EndCol = r.Span.StartLine, r.Span.StartCol
	return r, nil
}

// ReferenceByID читает одну ссылку по id — используется trace_call_graph для
// объяснения шага (место вызова) по call_edge.ref_id.
func (tx *ReadTx) ReferenceByID(id int64) (ReferenceRow, bool, error) {
	if err := tx.check(); err != nil {
		return ReferenceRow{}, false, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT `+referenceSelectColumns+` FROM `+referenceFromJoin+` WHERE r.id=?`, id)
	if err != nil {
		return ReferenceRow{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ReferenceRow{}, false, rows.Err()
	}
	r, err := scanReferenceRow(rows)
	if err != nil {
		return ReferenceRow{}, false, err
	}
	return r, true, rows.Err()
}

// ReferenceSearch — параметры find_references: цель (символ), опциональные
// kinds/component, keyset-курсор по id (references вставляются по одной в
// монотонно растущий rowid внутри своей write-транзакции — id уже даёт
// стабильный порядок без отдельного sort key).
type ReferenceSearch struct {
	TargetSymbolID int64
	Kinds          []string
	ComponentID    string
	AfterID        int64
	Limit          int
}

// FindReferences отдаёт ссылки на символ, упорядоченные по id.
func (tx *ReadTx) FindReferences(q ReferenceSearch) ([]ReferenceRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	where := []string{"r.target_symbol_id=?"}
	args := []any{q.TargetSymbolID}
	if len(q.Kinds) > 0 {
		where = append(where, "r.kind IN "+inClause(len(q.Kinds)))
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if q.ComponentID != "" {
		where = append(where, "sf.component_id=?")
		args = append(args, q.ComponentID)
	}
	if q.AfterID != 0 {
		where = append(where, "r.id>?")
		args = append(args, q.AfterID)
	}
	sqlText := `SELECT ` + referenceSelectColumns + ` FROM ` + referenceFromJoin +
		` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY r.id LIMIT ?`
	args = append(args, q.Limit)
	rows, err := tx.c.query(tx.ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReferenceRow
	for rows.Next() {
		r, err := scanReferenceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReferenceCandidateRow — один кандидат ambiguous-разрешения ссылки.
type ReferenceCandidateRow struct {
	TargetNodeID int64
	Rank         int
	Reason       string
}

// ReferenceCandidates читает кандидатов ambiguous-ссылки, упорядоченно по rank.
func (tx *ReadTx) ReferenceCandidates(refID int64) ([]ReferenceCandidateRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx,
		`SELECT target_node_id, rank, reason FROM reference_candidate WHERE ref_id=? ORDER BY rank`, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReferenceCandidateRow
	for rows.Next() {
		var c ReferenceCandidateRow
		var reason sql.NullString
		if err := rows.Scan(&c.TargetNodeID, &c.Rank, &reason); err != nil {
			return nil, err
		}
		c.Reason = reason.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// CallEdgeRow — ребро графа вызовов. CalleeID==0 значит «нет разрешённого
// символа-цели» (ambiguous/dynamic/unresolved, либо platform — тогда
// идентичность несёт присоединённая через RefID reference.platform_key).
type CallEdgeRow struct {
	ID             int64
	CallerID       int64
	CalleeID       int64
	CalleeNameNorm string
	QualifierNorm  string
	Kind           string
	Resolution     string
	Confidence     float64
	RefID          int64
}

const callEdgeSelectColumns = `id, caller_id, callee_id, callee_name_norm, qualifier_norm, kind, resolution, confidence, ref_id`

func scanCallEdgeRow(rows *sql.Rows) (CallEdgeRow, error) {
	var e CallEdgeRow
	var calleeID sql.NullInt64
	var qualifier sql.NullString
	err := rows.Scan(&e.ID, &e.CallerID, &calleeID, &e.CalleeNameNorm, &qualifier, &e.Kind, &e.Resolution,
		&e.Confidence, &e.RefID)
	if err != nil {
		return CallEdgeRow{}, err
	}
	e.CalleeID, e.QualifierNorm = calleeID.Int64, qualifier.String
	return e, nil
}

// CallEdgesFrom — рёбра, у которых caller_id=symbolID (что этот символ зовёт;
// direction=callees).
func (tx *ReadTx) CallEdgesFrom(symbolID int64) ([]CallEdgeRow, error) {
	return tx.queryCallEdges(`caller_id`, symbolID)
}

// CallEdgesTo — рёбра, у которых callee_id=symbolID (кто зовёт этот символ;
// direction=callers).
func (tx *ReadTx) CallEdgesTo(symbolID int64) ([]CallEdgeRow, error) {
	return tx.queryCallEdges(`callee_id`, symbolID)
}

func (tx *ReadTx) queryCallEdges(column string, symbolID int64) ([]CallEdgeRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx,
		`SELECT `+callEdgeSelectColumns+` FROM call_edge WHERE `+column+`=? ORDER BY id`, symbolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CallEdgeRow
	for rows.Next() {
		e, err := scanCallEdgeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
