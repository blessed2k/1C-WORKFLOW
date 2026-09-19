package store

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// ReadTx — read-транзакция одного MCP-вызова. Все SQL инструмента идут через
// неё: WAL держит снапшот на всё время вызова, поэтому факты, spans и текст
// blob согласованы между собой по построению (18.1, 18.2).
//
// Транзакция живёт ровно один вызов. Удерживать её между вызовами запрещено:
// длинный читатель не даёт checkpoint-у переносить страницы, и WAL растёт
// (ADR-2 §8) — вместо «удержания поколения» контракт выбрал честный
// cursor_expired.
type ReadTx struct {
	ctx  context.Context
	c    *conn
	done bool
	// flush сбрасывает буферы пакетной вставки писателя (batch.go) перед
	// любым чтением, удалением и обновлением. nil у читателей.
	flush func() error
}

// WriteTx — write-транзакция BEGIN IMMEDIATE. Наследует все выборки ReadTx:
// писателю они нужны не меньше, чем читателю (найти identity, проверить факт).
type WriteTx struct {
	ReadTx
	// touched — хэши blob, которых коснулась транзакция. Учёт unreferenced_since
	// идёт только по ним: полный проход по blob поднимает с диска содержимое
	// всех файлов (раздел 15).
	touched map[string]struct{}
	// batches: буферы многострочных INSERT (issue #3, шаг 2).
	batches *txBatches
	// refIDs: id строк reference, выданные до их вставки.
	refIDs refIDs
}

// ErrTxDone — обращение к завершённой транзакции. Ловит самую дорогую ошибку
// использования: сохранённый *ReadTx, из которого читают после возврата
// соединения в пул, то есть уже с чужого снапшота.
var ErrTxDone = errors.New("транзакция уже завершена")

func (tx *ReadTx) check() error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	if tx.flush != nil {
		return tx.flush()
	}
	return nil
}

// checkNoFlush: check без сброса буферов пакетной вставки (batch.go). Сброс
// обязателен перед любым оператором, который читает буферизуемую таблицу или
// удаляет и обновляет строки, на которые буферизуемые строки ссылаются
// (каскад и SET NULL прошли бы мимо строк в буфере). Без сброса обходятся:
// вставки и upsert-ы в небуферизуемые таблицы (upsert не меняет первичный
// ключ, поэтому внешние ключи буферизуемых строк не задевает) и выборки из
// таблиц, которые в буфер не попадают (node, source_file, role). Сброс на
// каждой такой строке свёл бы пакет к одной строке.
func (tx *ReadTx) checkNoFlush() error {
	if tx.done {
		return ErrTxDone
	}
	return tx.ctx.Err()
}

// --- meta и поколение ---

// Meta читает служебное поле. Отсутствующий ключ — пустая строка без ошибки.
func (tx *ReadTx) Meta(key string) (string, error) {
	if err := tx.check(); err != nil {
		return "", err
	}
	v, err := tx.c.queryText(tx.ctx, `SELECT value FROM meta WHERE key=?`, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// GenerationNumber — счётчик meta.current_generation.
func (tx *ReadTx) GenerationNumber() (int64, error) {
	if err := tx.check(); err != nil {
		return 0, err
	}
	return tx.c.queryInt(tx.ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key=?`, metaCurrentGeneration)
}

// SetMeta пишет служебное поле.
func (tx *WriteTx) SetMeta(key, value string) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`, key, value)
}

// LogGeneration добавляет запись в журнал публикаций. Журнал нужен диагностике;
// чтение фактов от него не зависит.
func (tx *WriteTx) LogGeneration(createdAtUnix int64, kind, note string) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO generation_log(created_at,kind,note) VALUES(?,?,?)`,
		createdAtUnix, kind, note)
}

// Validate прогоняет инварианты раздела 15 и foreign_key_check и возвращает
// список нарушений. Пустой список — инварианты соблюдены.
func (tx *ReadTx) Validate() ([]string, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	v, err := validate(tx.ctx, tx.c)
	if err != nil {
		return nil, err
	}
	return validateFailures(v), nil
}

// --- компоненты ---

// Component — зарегистрированный компонент logical project.
type Component struct {
	ID         string
	Kind       string
	Root       string
	AppliesTo  string
	ApplyOrder int
	Display    string
}

// UpsertComponent регистрирует или обновляет компонент.
func (tx *WriteTx) UpsertComponent(c Component) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO component(id,kind,root,applies_to,apply_order,display)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, root=excluded.root,
		  applies_to=excluded.applies_to, apply_order=excluded.apply_order, display=excluded.display`,
		c.ID, c.Kind, c.Root, nullString(c.AppliesTo), c.ApplyOrder, c.Display)
}

// --- blob и файлы ---

// HashContent — content hash образа файла. Один алгоритм на весь индекс: и
// fingerprint, и адресация blob, и URI ресурса ссылаются на одно и то же число.
func HashContent(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PreparedBlob: образ файла, уже захэшированный и сжатый вне писателя
// (issue #3, шаг 1). SHA-256 и deflate идут в пуле разбора параллельно, а
// единственный писатель только кладёт готовые байты. Поля закрыты: собрать
// образ можно только через PrepareBlob, поэтому хэш и сжатые байты не могут
// разойтись (адресация по содержимому держится на этом).
type PreparedBlob struct {
	hash   string
	size   int64
	packed []byte
}

// PrepareBlob считает хэш и сжимает образ. Чистая функция, годна для
// вызова из любого числа горутин.
func PrepareBlob(data []byte) (PreparedBlob, error) {
	packed, err := deflate(data)
	if err != nil {
		return PreparedBlob{}, err
	}
	return PreparedBlob{hash: HashContent(data), size: int64(len(data)), packed: packed}, nil
}

// Hash: content hash исходного (несжатого) образа, тот же, что HashContent.
func (b PreparedBlob) Hash() string { return b.hash }

// Size: размер исходного образа в байтах.
func (b PreparedBlob) Size() int64 { return b.size }

// PutBlob кладёт ТОЧНЫЙ байтовый образ файла в content-addressed хранилище и
// возвращает его хэш. Дедупликация по хэшу: повторное появление того же
// содержимого не пишет данные заново и снимает метку unreferenced_since (18.2).
func (tx *WriteTx) PutBlob(data []byte) (string, error) {
	if err := tx.check(); err != nil {
		return "", err
	}
	b, err := PrepareBlob(data)
	if err != nil {
		return "", err
	}
	return tx.PutPreparedBlob(b)
}

// PutPreparedBlob кладёт образ, подготовленный PrepareBlob, с тем же
// контрактом, что PutBlob. Нулевое значение (образ не подготовлен)
// отвергается: пустой файл имеет хэш, а пустой хэш значит ошибку вызывающего.
func (tx *WriteTx) PutPreparedBlob(b PreparedBlob) (string, error) {
	if err := tx.checkNoFlush(); err != nil {
		return "", err
	}
	if b.hash == "" {
		return "", errors.New("образ файла не подготовлен: пустой хэш")
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO blob(content_hash,size,compressed_size,unreferenced_since,data)
		VALUES(?,?,?,NULL,?) ON CONFLICT(content_hash) DO NOTHING`,
		b.hash, b.size, int64(len(b.packed)), b.packed); err != nil {
		return "", err
	}
	tx.touched[b.hash] = struct{}{}
	return b.hash, nil
}

// Blob отдаёт распакованный образ файла. Тела и фрагменты режутся ИЗ НЕГО, а не
// из живого файла: старый span к новому содержимому неприменим по построению.
func (tx *ReadTx) Blob(hash string) ([]byte, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	packed, err := tx.c.queryBlob(tx.ctx, `SELECT data FROM blob WHERE content_hash=?`, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("blob %s не найден: образ вычищен по TTL", hash)
	}
	if err != nil {
		return nil, err
	}
	return inflate(packed)
}

// SourceFile — запись об исходном файле компонента.
type SourceFile struct {
	ComponentID   string
	RelPath       string // канонический путь относительно корня компонента
	Size          int64
	MtimeNS       int64
	ContentHash   string
	ParserVersion int
}

// InsertSourceFile регистрирует файл и возвращает его id.
func (tx *WriteTx) InsertSourceFile(f SourceFile) (int64, error) {
	if err := tx.checkNoFlush(); err != nil {
		return 0, err
	}
	id, err := tx.c.execInsert(tx.ctx, `INSERT INTO source_file(component_id,rel_path,size,mtime_ns,content_hash,parser_version)
		VALUES(?,?,?,?,?,?)`, f.ComponentID, f.RelPath, f.Size, f.MtimeNS, f.ContentHash, f.ParserVersion)
	if err != nil {
		return 0, err
	}
	tx.touched[f.ContentHash] = struct{}{}
	return id, nil
}

// SourceFileID находит файл по компоненту и относительному пути.
func (tx *ReadTx) SourceFileID(componentID, relPath string) (int64, bool, error) {
	if err := tx.checkNoFlush(); err != nil {
		return 0, false, err
	}
	id, err := tx.c.queryInt(tx.ctx, `SELECT id FROM source_file WHERE component_id=? AND rel_path=?`,
		componentID, relPath)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// DeleteSourceFiles удаляет файлы вместе с их фактами и аспектами (шаг (2)
// раздела 15) и выполняет обязательный шаг (1b) для ссылок, чьи цели исчезают.
//
// Порядок не факультативен: ON DELETE SET NULL — это UPDATE, обязанный пройти
// XOR-CHECK, и срабатывание его на resolved-строке валит транзакцию целиком.
// Поэтому такие ссылки переводятся в unresolved ДО удаления. Более широкий
// affected set (по resolution_dep, включая негативные lookup-ы) считает
// вызывающий: только он знает дельту имён.
func (tx *WriteTx) DeleteSourceFiles(ids ...int64) error {
	if err := tx.check(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	in := inClause(len(ids))
	args := make([]any, 0, len(ids)*3)
	for _, id := range ids {
		args = append(args, id)
	}
	// Хэши снимаются ДО удаления: после каскада их уже не прочитать.
	rows, err := tx.c.query(tx.ctx, `SELECT content_hash FROM source_file WHERE id IN `+in, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		tx.touched[h] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// (1b)
	if err := tx.c.exec(tx.ctx, `UPDATE reference SET resolution='unresolved', target_class=NULL,
		 target_symbol_id=NULL, target_object_id=NULL, platform_key=NULL
		 WHERE resolution='resolved' AND (
		   target_symbol_id IN (SELECT id FROM symbol WHERE origin_file_id IN `+in+`)
		   OR target_object_id IN (SELECT id FROM metadata_object WHERE file_id IN `+in+`))`,
		append(append([]any{}, args...), args...)...); err != nil {
		return err
	}
	// FTS5 живёт без FK: строки удаляются явно, по rowid = symbol.id.
	if err := tx.c.exec(tx.ctx, `DELETE FROM fts_symbols WHERE rowid IN
		(SELECT id FROM symbol WHERE origin_file_id IN `+in+`)`, args...); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `DELETE FROM source_file WHERE id IN `+in, args...)
}

// SetReferencesUnresolved — шаг (1b) для affected set, посчитанного вызывающим
// (18.4). Отдельный метод, потому что удаление файлов покрывает только те цели,
// которые исчезают вместе с ним.
func (tx *WriteTx) SetReferencesUnresolved(refIDs ...int64) error {
	if err := tx.check(); err != nil {
		return err
	}
	if len(refIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(refIDs))
	for _, id := range refIDs {
		args = append(args, id)
	}
	return tx.c.exec(tx.ctx, `UPDATE reference SET resolution='unresolved', target_class=NULL,
		 target_symbol_id=NULL, target_object_id=NULL, platform_key=NULL
		 WHERE resolution='resolved' AND id IN `+inClause(len(refIDs)), args...)
}

// --- node и составные identity ---

// ensureNode находит или создаёт стабильную ЛОГИЧЕСКУЮ identity по
// identity_key. Узел переживает изменение любого из своих файлов и удаляется
// только reconciliation-шагом, когда не осталось ни одного аспекта-источника.
func (tx *WriteTx) ensureNode(kind, componentID, identityKey string) (int64, error) {
	if err := tx.checkNoFlush(); err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO node(kind,component_id,identity_key) VALUES(?,?,?)
		ON CONFLICT(identity_key) DO NOTHING`, kind, componentID, identityKey); err != nil {
		return 0, err
	}
	var id int64
	var haveKind string
	err := tx.c.queryRow(tx.ctx, `SELECT id, kind FROM node WHERE identity_key=?`, identityKey).
		Scan(&id, &haveKind)
	if err != nil {
		return 0, err
	}
	if haveKind != kind {
		return 0, fmt.Errorf("identity %q уже занята узлом вида %q, запрошен %q", identityKey, haveKind, kind)
	}
	return id, nil
}

// NodeID возвращает id узла по его identity_key.
func (tx *ReadTx) NodeID(identityKey string) (int64, bool, error) {
	if err := tx.checkNoFlush(); err != nil {
		return 0, false, err
	}
	id, err := tx.c.queryInt(tx.ctx, `SELECT id FROM node WHERE identity_key=?`, identityKey)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Module — identity модуля. Свойства и код приезжают аспектами из своих файлов.
type Module struct {
	IdentityKey   string
	ComponentID   string
	Kind          string
	OwnerObjectID int64
	NameNorm      string
	NameDisplay   string
}

// EnsureModule создаёт или переиспользует identity модуля.
func (tx *WriteTx) EnsureModule(m Module) (int64, error) {
	id, err := tx.ensureNode("module", m.ComponentID, m.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO module(id,component_id,kind,owner_object_id,name_norm,name_display)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, owner_object_id=excluded.owner_object_id,
		  name_norm=excluded.name_norm, name_display=excluded.name_display`,
		id, m.ComponentID, m.Kind, nullID(m.OwnerObjectID), m.NameNorm, m.NameDisplay); err != nil {
		return 0, err
	}
	return id, nil
}

// PutModuleContext привязывает аспект свойств модуля к его XML-файлу.
func (tx *WriteTx) PutModuleContext(moduleID, fileID int64, propsJSON string) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO module_context(module_id,file_id,props) VALUES(?,?,?)
		ON CONFLICT(module_id) DO UPDATE SET file_id=excluded.file_id, props=excluded.props`,
		moduleID, fileID, propsJSON)
}

// PutModuleCode привязывает аспект кода модуля к его BSL-файлу.
func (tx *WriteTx) PutModuleCode(moduleID, fileID int64) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO module_code(module_id,file_id) VALUES(?,?)
		ON CONFLICT(module_id) DO UPDATE SET file_id=excluded.file_id`, moduleID, fileID)
}

// Symbol — символ BSL со своим span.
type Symbol struct {
	IdentityKey  string
	ComponentID  string
	UID          string
	ModuleID     int64
	OriginFileID int64
	Kind         string
	NameNorm     string
	NameDisplay  string
	IsExport     bool
	Directive    string
	IsAsync      bool
	Span         domain.Span
	Signature    string
	DocFirstLine string
	Region       string
}

// InsertSymbol вставляет символ и его строку полнотекстового индекса.
func (tx *WriteTx) InsertSymbol(s Symbol) (int64, error) {
	id, err := tx.ensureNode("symbol", s.ComponentID, s.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO symbol(id,uid,module_id,origin_file_id,kind,name_norm,name_display,
		 is_export,directive,is_async,byte_start,byte_end,start_line,start_col,end_line,end_col,
		 signature,doc_first_line,region)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, s.UID, s.ModuleID, s.OriginFileID, s.Kind, s.NameNorm, s.NameDisplay,
		boolInt(s.IsExport), nullString(s.Directive), boolInt(s.IsAsync),
		s.Span.StartByte, s.Span.EndByte, s.Span.StartLine, s.Span.StartCol, s.Span.EndLine, s.Span.EndCol,
		nullString(s.Signature), nullString(s.DocFirstLine), nullString(s.Region)); err != nil {
		return 0, err
	}
	// rowid FTS-строки = symbol.id: удаление символа стоит один DELETE by rowid.
	if err := tx.batches.fts.add(tx.ctx, tx.c, ftsRow{id, s}); err != nil {
		return 0, err
	}
	return id, nil
}

// Parameter — параметр символа с его значением по умолчанию.
type Parameter struct {
	Ord         int
	Name        string
	ByVal       bool
	DefaultExpr string
}

// InsertParameter добавляет параметр символа.
func (tx *WriteTx) InsertParameter(symbolID int64, p Parameter) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.parameter.add(tx.ctx, tx.c, paramRow{symbolID, p})
}

// MetadataObject — объект метаданных из XML.
type MetadataObject struct {
	IdentityKey string
	ComponentID string
	UUID        string
	MType       string
	NameNorm    string
	NameDisplay string
	Synonym     string
	FileID      int64
	PropsJSON   string
	Layer       string
}

// EnsureMetadataObject создаёт или обновляет объект метаданных.
func (tx *WriteTx) EnsureMetadataObject(o MetadataObject) (int64, error) {
	id, err := tx.ensureNode("metadata_object", o.ComponentID, o.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO metadata_object(id,component_id,uuid,mtype,name_norm,name_display,
		 synonym,file_id,props,layer) VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET uuid=excluded.uuid, mtype=excluded.mtype, name_norm=excluded.name_norm,
		   name_display=excluded.name_display, synonym=excluded.synonym, file_id=excluded.file_id,
		   props=excluded.props, layer=excluded.layer`,
		id, o.ComponentID, nullString(o.UUID), o.MType, o.NameNorm, o.NameDisplay,
		nullString(o.Synonym), o.FileID, nullString(o.PropsJSON), layerOrBase(o.Layer)); err != nil {
		return 0, err
	}
	return id, nil
}

// MetadataMember — реквизит, ресурс, измерение, табличная часть и её реквизит.
type MetadataMember struct {
	IdentityKey  string
	ComponentID  string
	ObjectID     int64
	OriginFileID int64
	Kind         string
	NameNorm     string
	NameDisplay  string
	TypesJSON    string
	Indexed      bool
	ParentMember int64
}

// InsertMetadataMember добавляет член объекта метаданных.
func (tx *WriteTx) InsertMetadataMember(m MetadataMember) (int64, error) {
	id, err := tx.ensureNode("metadata_member", m.ComponentID, m.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO metadata_member(id,object_id,origin_file_id,kind,name_norm,
		 name_display,types,indexed,parent_member) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, m.ObjectID, m.OriginFileID, m.Kind, m.NameNorm, m.NameDisplay,
		nullString(m.TypesJSON), boolInt(m.Indexed), nullID(m.ParentMember)); err != nil {
		return 0, err
	}
	return id, nil
}

// Form — identity формы. Источники приезжают аспектами: объявление в XML
// владельца и структура из Form.xml; модуль формы — отдельный module-узел.
type Form struct {
	IdentityKey   string
	ComponentID   string
	OwnerObjectID int64
	NameNorm      string
	NameDisplay   string
}

// EnsureForm создаёт или переиспользует identity формы.
func (tx *WriteTx) EnsureForm(f Form) (int64, error) {
	id, err := tx.ensureNode("form", f.ComponentID, f.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO form(id,owner_object_id,name_norm,name_display) VALUES(?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET owner_object_id=excluded.owner_object_id,
		  name_norm=excluded.name_norm, name_display=excluded.name_display`,
		id, nullID(f.OwnerObjectID), f.NameNorm, f.NameDisplay); err != nil {
		return 0, err
	}
	return id, nil
}

// PutFormDeclaration привязывает аспект объявления формы к XML владельца.
func (tx *WriteTx) PutFormDeclaration(formID, fileID int64) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO form_declaration(form_id,file_id) VALUES(?,?)
		ON CONFLICT(form_id) DO UPDATE SET file_id=excluded.file_id`, formID, fileID)
}

// PutFormStructure привязывает аспект структуры формы к Form.xml.
func (tx *WriteTx) PutFormStructure(formID, fileID int64) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO form_structure(form_id,file_id) VALUES(?,?)
		ON CONFLICT(form_id) DO UPDATE SET file_id=excluded.file_id`, formID, fileID)
}

// FormElement — элемент формы.
type FormElement struct {
	IdentityKey  string
	ComponentID  string
	FormID       int64
	OriginFileID int64
	NameNorm     string
	NameDisplay  string
	EType        string
	DataPath     string
}

// InsertFormElement добавляет элемент формы.
func (tx *WriteTx) InsertFormElement(e FormElement) (int64, error) {
	id, err := tx.ensureNode("form_element", e.ComponentID, e.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO form_element(id,form_id,origin_file_id,name_norm,name_display,etype,data_path)
		VALUES(?,?,?,?,?,?,?)`, id, e.FormID, e.OriginFileID, e.NameNorm, e.NameDisplay,
		nullString(e.EType), nullString(e.DataPath)); err != nil {
		return 0, err
	}
	return id, nil
}

// FormCommand — команда формы.
type FormCommand struct {
	IdentityKey  string
	ComponentID  string
	FormID       int64
	OriginFileID int64
	NameNorm     string
	NameDisplay  string
	ActionNorm   string
}

// InsertFormCommand добавляет команду формы.
func (tx *WriteTx) InsertFormCommand(c FormCommand) (int64, error) {
	id, err := tx.ensureNode("form_command", c.ComponentID, c.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO form_command(id,form_id,origin_file_id,name_norm,name_display,action_norm)
		VALUES(?,?,?,?,?,?)`, id, c.FormID, c.OriginFileID, c.NameNorm, c.NameDisplay,
		nullString(c.ActionNorm)); err != nil {
		return 0, err
	}
	return id, nil
}

// HandlerBinding — привязка обработчика к событию формы или элемента.
// Объявленный, но не найденный обработчик остаётся здесь с resolution=unresolved:
// пустая выдача скрыла бы ровно тот случай, ради которого инструмент и зовут.
type HandlerBinding struct {
	FormID          int64
	Source          string
	Event           string
	HandlerNameNorm string
	HandlerSymbolID int64
	OriginFileID    int64
	Resolution      string
}

// InsertHandlerBinding добавляет привязку обработчика.
func (tx *WriteTx) InsertHandlerBinding(b HandlerBinding) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.handlerBinding.add(tx.ctx, tx.c, b)
}

// Reference — ссылка со своим состоянием разрешения. Инварианты XOR-автомата
// (target_class только у resolved, ровно одна цель своего класса) закреплены
// CHECK-ами схемы: нарушение валит транзакцию, а не портит данные тихо.
type Reference struct {
	FileID         int64
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

// InsertReference добавляет ссылку и возвращает её id. Строка уходит в буфер
// пакетной вставки, id выдаётся сразу (см. refIDs).
func (tx *WriteTx) InsertReference(r Reference) (int64, error) {
	if err := tx.checkNoFlush(); err != nil {
		return 0, err
	}
	id, err := tx.refIDs.take(tx.ctx, tx.c)
	if err != nil {
		return 0, err
	}
	if err := tx.batches.reference.add(tx.ctx, tx.c, refRow{id, r}); err != nil {
		return 0, err
	}
	return id, nil
}

// InsertReferenceCandidate добавляет кандидата ambiguous-разрешения. Инвариант
// validate-шага: у ambiguous их минимум два — один кандидат обязан стать resolved.
func (tx *WriteTx) InsertReferenceCandidate(refID, targetNodeID int64, rank int, reason string) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.candidate.add(tx.ctx, tx.c, candidateRow{refID, targetNodeID, rank, reason})
}

// InsertResolutionDep регистрирует ключ, который консультировала ссылка.
// Регистрируются ВСЕ консультированные ключи, включая негативные: без них
// инкремент не узнает, что появившееся имя меняет уже разрешённую ссылку (18.4).
func (tx *WriteTx) InsertResolutionDep(keyHash string, refID int64) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.resolutionDep.add(tx.ctx, tx.c, depRow{keyHash, refID})
}

// CallEdge — ребро графа вызовов, привязанное к своей ссылке.
type CallEdge struct {
	CallerID       int64
	CalleeID       int64
	CalleeNameNorm string
	QualifierNorm  string
	Kind           string
	Resolution     string
	Confidence     float64
	RefID          int64
}

// InsertCallEdge добавляет ребро графа вызовов.
func (tx *WriteTx) InsertCallEdge(e CallEdge) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.callEdge.add(tx.ctx, tx.c, e)
}

// Query — текст запроса 1С внутри символа.
type Query struct {
	IdentityKey string
	ComponentID string
	SymbolID    int64
	FileID      int64
	Span        domain.Span
	Staticity   string // static|partial|dynamic
	Text        string
	Confidence  float64
}

// InsertQuery добавляет запрос.
func (tx *WriteTx) InsertQuery(q Query) (int64, error) {
	id, err := tx.ensureNode("query", q.ComponentID, q.IdentityKey)
	if err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO query(id,symbol_id,file_id,byte_start,byte_end,staticity,text,confidence)
		VALUES(?,?,?,?,?,?,?,?)`, id, q.SymbolID, q.FileID, q.Span.StartByte, q.Span.EndByte,
		q.Staticity, q.Text, q.Confidence); err != nil {
		return 0, err
	}
	return id, nil
}

// QueryReference — таблица, поле, параметр или ВТ, использованные запросом.
type QueryReference struct {
	QueryID   int64
	Kind      string
	NameNorm  string
	ObjectID  int64
	MemberID  int64
	SpanStart int64
	SpanEnd   int64
}

// InsertQueryReference добавляет использование в запросе.
func (tx *WriteTx) InsertQueryReference(r QueryReference) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.queryReference.add(tx.ctx, tx.c, r)
}

// RegisterAccess — доступ к регистру: режим, транзакционность, статичность,
// слой. Layer — 'base' либо id компонента-расширения, та же семантика, что у
// dependency_edge: без него raw и effective неразличимы (D11). Пусто = base.
type RegisterAccess struct {
	FileID           int64
	SymbolID         int64
	ObjectID         int64
	RegisterNameNorm string
	Mode             string // read|write|movement|clear
	InTransaction    *bool
	Static           bool
	Confidence       float64
	Span             domain.Span
	Layer            string
}

// InsertRegisterAccess добавляет доступ к регистру.
func (tx *WriteTx) InsertRegisterAccess(a RegisterAccess) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.registerAccess.add(tx.ctx, tx.c, a)
}

// EventSubscription — подписка на событие. SourceKind различает голый вид
// источника и ОпределяемыйТип: обе формы обязаны попасть в индекс.
type EventSubscription struct {
	ComponentID     string
	NameNorm        string
	NameDisplay     string
	SourceKind      string
	SourceNameNorm  string
	Event           string
	HandlerNameNorm string
	HandlerSymbolID int64
	OriginFileID    int64
	Resolution      string
	Layer           string
}

// InsertEventSubscription добавляет подписку на событие.
func (tx *WriteTx) InsertEventSubscription(s EventSubscription) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO event_subscription(component_id,name_norm,name_display,source_kind,
		source_name_norm,event,handler_name_norm,handler_symbol_id,origin_file_id,resolution,layer)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		s.ComponentID, s.NameNorm, s.NameDisplay, s.SourceKind, nullString(s.SourceNameNorm),
		s.Event, s.HandlerNameNorm, nullID(s.HandlerSymbolID), s.OriginFileID,
		s.Resolution, layerOrBase(s.Layer))
}

// ScheduledJob — регламентное задание и его метод.
type ScheduledJob struct {
	ComponentID     string
	NameNorm        string
	NameDisplay     string
	MethodNameNorm  string
	HandlerSymbolID int64
	OriginFileID    int64
	Use             bool
	Predefined      bool
	Resolution      string
	Layer           string
}

// InsertScheduledJob добавляет регламентное задание.
func (tx *WriteTx) InsertScheduledJob(j ScheduledJob) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO scheduled_job(component_id,name_norm,name_display,method_name_norm,
		handler_symbol_id,origin_file_id,use_flag,predefined,resolution,layer) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		j.ComponentID, j.NameNorm, j.NameDisplay, j.MethodNameNorm, nullID(j.HandlerSymbolID),
		j.OriginFileID, boolInt(j.Use), boolInt(j.Predefined), j.Resolution, layerOrBase(j.Layer))
}

// Role — роль конфигурации или расширения.
type Role struct {
	ComponentID string
	NameNorm    string
	NameDisplay string
	ObjectID    int64
	FileID      int64
	Layer       string
}

// EnsureRole создаёт или обновляет роль и возвращает её id.
func (tx *WriteTx) EnsureRole(r Role) (int64, error) {
	if err := tx.checkNoFlush(); err != nil {
		return 0, err
	}
	if err := tx.c.exec(tx.ctx, `INSERT INTO role(component_id,name_norm,name_display,object_id,file_id,layer)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(component_id,name_norm) DO UPDATE SET name_display=excluded.name_display,
		  object_id=excluded.object_id, file_id=excluded.file_id, layer=excluded.layer`,
		r.ComponentID, r.NameNorm, r.NameDisplay, nullID(r.ObjectID), r.FileID, layerOrBase(r.Layer)); err != nil {
		return 0, err
	}
	return tx.c.queryInt(tx.ctx, `SELECT id FROM role WHERE component_id=? AND name_norm=?`,
		r.ComponentID, r.NameNorm)
}

// RoleRight — право роли на объект. SetForNewObjects хранится явно: Rights.xml
// содержит отклонения от умолчаний, и «нет строки» НЕ означает «нет доступа».
type RoleRight struct {
	RoleID          int64
	ObjectID        int64
	ObjectNameNorm  string
	RightName       string
	Value           bool
	RLS             string
	SetForNewObject bool
	OriginFileID    int64
}

// InsertRoleRight добавляет право роли.
func (tx *WriteTx) InsertRoleRight(r RoleRight) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.roleRight.add(tx.ctx, tx.c, r)
}

// DependencyEdge — связь без собственных атрибутов: подсистема содержит объект,
// поле типизировано объектом, тест ссылается на боевой символ и подобные.
type DependencyEdge struct {
	Kind         string
	FromNode     int64
	ToNode       int64
	OriginFileID int64
	Confidence   float64
	Layer        string
}

// InsertDependencyEdge добавляет generic-связь.
func (tx *WriteTx) InsertDependencyEdge(e DependencyEdge) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.dependencyEdge.add(tx.ctx, tx.c, e)
}

// Diagnostic — замечание парсера или резолвера.
type Diagnostic struct {
	FileID      int64
	ComponentID string
	Severity    string
	Code        string
	Message     string
	Span        domain.Span
}

// InsertDiagnostic добавляет диагностику.
func (tx *WriteTx) InsertDiagnostic(d Diagnostic) error {
	if err := tx.checkNoFlush(); err != nil {
		return err
	}
	return tx.batches.diagnostic.add(tx.ctx, tx.c, d)
}

// --- вспомогательное ---

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// layerOrBase: слой обязателен у каждого факта, и пустая строка здесь означала
// бы факт неизвестного происхождения. Умолчание — базовый слой конфигурации.
func layerOrBase(l string) string {
	if l == "" {
		return "base"
	}
	return l
}

func inClause(n int) string {
	b := make([]byte, 0, 2*n+2)
	b = append(b, '(')
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(append(b, ')'))
}

// flateWriters: сжиматели переиспользуются. flate.NewWriter выделяет около
// мегабайта таблиц на вызов, и с пулом разбора (issue #3, шаг 1) это десятки
// гигабайт мусора за полную пересборку, который раздувает пик RSS.
var flateWriters = sync.Pool{New: func() any {
	w, _ := flate.NewWriter(io.Discard, flate.DefaultCompression)
	return w
}}

func deflate(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(data)/4 + 64)
	w := flateWriters.Get().(*flate.Writer)
	w.Reset(&buf)
	_, err := w.Write(data)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	w.Reset(io.Discard)
	flateWriters.Put(w)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func inflate(packed []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(packed))
	defer r.Close()
	return io.ReadAll(r)
}
