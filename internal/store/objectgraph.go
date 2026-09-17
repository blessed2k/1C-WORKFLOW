package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Файл — типизированная запись объектного графа (§2 спецификации В1): рёбра
// между объектами метаданных, их файловые зависимости и бейджи узлов. SQL
// живёт только здесь (RuleSQLOnlyInStore).
//
// Узлами графа являются ТОЛЬКО объекты метаданных (решение D6): FromObjectID и
// ToObjectID — это id из metadata_object, и ни модуля, ни символа тут быть не
// может.

// Виды рёбер object_data_edge (CHECK схемы). Значения перечислены здесь, чтобы
// вызывающему не приходилось повторять строковые литералы схемы.
const (
	EdgeWritesRegister = "writes-register"
	EdgeReadsRegister  = "reads-register"
	EdgeWritesDeclared = "writes-declared"
	EdgeReadsQuery     = "reads-query"
	EdgeRefAttribute   = "ref-attribute"
	EdgeCreates        = "creates"
)

// Происхождение ребра: код-факт против декларации в метаданных. Отдельная ось
// от kind: kind отвечает «что за связь», provenance — «откуда мы это знаем».
const (
	EdgeProvenanceCode     = "code"
	EdgeProvenanceDeclared = "metadata-declared"
)

// ErrEdgeWithoutFiles — попытка вставить ребро, не назвав ни одного файла, от
// которого оно зависит. Такое ребро неудаляемо: инкрементальная пересборка
// ищет рёбра по файлам (DeleteObjectEdgesForFiles), и ребро без зависимостей
// переживёт любую переиндексацию своего источника. Отказ на вставке дешевле
// того же нарушения, найденного инвариантом на validate-шаге.
var ErrEdgeWithoutFiles = errors.New("объектное ребро без файловых зависимостей")

// ObjectDataEdge — ребро объектного графа. Mode и InTransaction переносятся из
// строки register_access, породившей факт, и осмысленны только у кодовых
// регистровых рёбер; у остальных остаются пустыми.
type ObjectDataEdge struct {
	FromObjectID  int64
	ToObjectID    int64
	Kind          string // writes-register|reads-register|writes-declared|reads-query|ref-attribute|creates
	Layer         string // base либо id компонента-расширения; пусто = base
	Provenance    string // code|metadata-declared
	Confidence    float64
	Mode          string // read|write|movement|clear; пусто — NULL
	InTransaction *bool
	Evidence      string // цепочка атрибуции, сериализованная вызывающим
	// FileIDs — файлы, от которых зависит ребро: цепочка атрибуции пересекает
	// несколько файлов, одного origin_file_id не хватает. Обязателен непустым.
	FileIDs []int64
}

// InsertObjectDataEdge добавляет ребро вместе с его файловыми зависимостями:
// одна операция, потому что ребро без зависимостей — дефект, а не промежуточное
// состояние. Повторы в FileIDs схлопываются.
func (tx *WriteTx) InsertObjectDataEdge(e ObjectDataEdge) (int64, error) {
	if err := tx.check(); err != nil {
		return 0, err
	}
	if len(e.FileIDs) == 0 {
		return 0, fmt.Errorf("%w: %s %d->%d", ErrEdgeWithoutFiles, e.Kind, e.FromObjectID, e.ToObjectID)
	}
	var inTx any
	if e.InTransaction != nil {
		inTx = boolInt(*e.InTransaction)
	}
	id, err := tx.c.execInsert(tx.ctx, `INSERT INTO object_data_edge(from_object_id,to_object_id,kind,layer,
		provenance,confidence,mode,in_transaction,evidence) VALUES(?,?,?,?,?,?,?,?,?)`,
		e.FromObjectID, e.ToObjectID, e.Kind, layerOrBase(e.Layer), e.Provenance, e.Confidence,
		nullString(e.Mode), inTx, e.Evidence)
	if err != nil {
		return 0, err
	}
	seen := make(map[int64]struct{}, len(e.FileIDs))
	for _, fileID := range e.FileIDs {
		if _, dup := seen[fileID]; dup {
			continue
		}
		seen[fileID] = struct{}{}
		if err := tx.c.exec(tx.ctx, `INSERT INTO object_data_edge_dep(edge_id,file_id) VALUES(?,?)`,
			id, fileID); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// ObjectBadge — бейдж узла: дыра атрибуции со счётчиком (has-dynamic и его
// соседи). Живёт на паре объект+слой, потому что в effective-слое счётчик
// другой.
type ObjectBadge struct {
	ObjectID int64
	Badge    string
	Layer    string
	Count    int64
}

// InsertObjectBadge ставит бейдж. Повторная вставка той же тройки
// (объект, бейдж, слой) ЗАМЕЩАЕТ счётчик, а не складывает: пересборка владельца
// считает бейджи заново и обязана получить итог, а не сумму двух прогонов.
func (tx *WriteTx) InsertObjectBadge(b ObjectBadge) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.c.exec(tx.ctx, `INSERT INTO object_badge(object_id,badge,layer,count) VALUES(?,?,?,?)
		ON CONFLICT(object_id,badge,layer) DO UPDATE SET count=excluded.count`,
		b.ObjectID, b.Badge, layerOrBase(b.Layer), b.Count)
}

// edgesDependingOnFiles — ЕДИНСТВЕННЫЙ отбор «рёбра, зависящие хотя бы от
// одного из этих файлов»: подзапрос и его аргументы. Им пользуются и удаление,
// и читалка состава, потому что всё сравнение «было — стало» в публикаторе
// держится на том, что эти два отбора совпадают. Два рукописных условия рядом
// разъехались бы на первой же правке, и разъезд был бы молчаливым.
//
// Список файлов приходит НЕОГРАНИЧЕННОЙ длины: пересборка всей выгрузки несёт
// сюда все её файлы (на ut_demo — 48 699). Плейсхолдер на файл упирался бы в
// лимит SQLite на число переменных в запросе (32 766 в текущих сборках, 999 в
// старых) и падал бы «too many SQL variables», поэтому список уходит ОДНИМ
// параметром — JSON-массивом, развёрнутым json_each. Отбор при этом остаётся
// одним запросом: батчинг пришлось бы разрезать по обе стороны сравнения
// «было — стало», а склеивать рёбра из порций — это и есть тот самый второй
// отбор, которого этот помощник и не даёт завестись.
func edgesDependingOnFiles(fileIDs []int64) (string, []any) {
	return `SELECT edge_id FROM object_data_edge_dep WHERE file_id IN (SELECT value FROM json_each(?))`,
		[]any{int64ListJSON(fileIDs)}
}

// int64ListJSON — список id как JSON-массив для json_each. Собирается вручную,
// а не через encoding/json: тип известен, ошибке взяться неоткуда, а список
// бывает в десятки тысяч элементов.
func int64ListJSON(ids []int64) string {
	var b strings.Builder
	b.Grow(2 + len(ids)*8)
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	b.WriteByte(']')
	return b.String()
}

// ObjectDataEdgeKey — ребро в виде, по которому ДВЕ публикации сравнимы между
// собой: id у пересобранного ребра новый, а связь та же самая. Режим и
// транзакционность в ключ не входят: вопрос, на который он отвечает, — «эта
// связь всё ещё есть», а не «это та же самая строка».
//
// Kind в ключе несущий: без него декларированное и кодовое ребро одной пары
// объектов схлопнулись бы в одну связь. Provenance не входит в ключ, и это
// безопасно ровно до тех пор, пока он
// ОДНОЗНАЧНО выводится из kind: writes-declared всегда metadata-declared,
// регистровые виды всегда code. В схеме этот инвариант ничем не выражен — там
// два независимых CHECK, — поэтому он записан здесь. Первое же ребро с другой
// парой вид/происхождение схлопнется в чужой ключ, и поймать это будет нечем:
// добавляя такой вид, добавь provenance сюда.
type ObjectDataEdgeKey struct {
	FromObjectID int64
	ToObjectID   int64
	Kind         string
	Layer        string
}

// ObjectDataEdgeDeps — ребро вместе со ВСЕМИ его файловыми зависимостями.
type ObjectDataEdgeDeps struct {
	ObjectDataEdgeKey
	FileIDs []int64
}

// ObjectDataEdgesDependingOnFiles перечисляет рёбра, которые снесёт
// DeleteObjectEdgesForFiles с теми же файлами, — ровно по тому же отбору, одним
// запросом. Живёт вплотную к удалению не по вкусу: два разных отбора «что
// удаляем» и «что удалили» разъехались бы на первой же правке, а сравнение
// «было — стало» строится именно на их совпадении.
//
// Вызывающий (internal/index) сравнивает этот состав с тем, что вернула
// атрибуция: разность — связи, которые снесены и в этом прогоне не
// восстановлены. Молчать о них нельзя, поэтому файлы отдаются целиком: по ним
// видно, была ли переопубликована вся цепочка (тогда связи действительно
// больше нет) или только её часть (тогда это устаревшая атрибуция).
func (tx *ReadTx) ObjectDataEdgesDependingOnFiles(fileIDs ...int64) ([]ObjectDataEdgeDeps, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if len(fileIDs) == 0 {
		return nil, nil
	}
	sel, args := edgesDependingOnFiles(fileIDs)
	rows, err := tx.c.query(tx.ctx, `SELECT e.id, e.from_object_id, e.to_object_id, e.kind, e.layer, d.file_id
		FROM object_data_edge e JOIN object_data_edge_dep d ON d.edge_id = e.id
		WHERE e.id IN (`+sel+`) ORDER BY e.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectDataEdgeDeps
	var curID int64
	for rows.Next() {
		var id, fileID int64
		var k ObjectDataEdgeKey
		if err := rows.Scan(&id, &k.FromObjectID, &k.ToObjectID, &k.Kind, &k.Layer, &fileID); err != nil {
			return nil, err
		}
		if len(out) == 0 || id != curID {
			out = append(out, ObjectDataEdgeDeps{ObjectDataEdgeKey: k})
			curID = id
		}
		last := &out[len(out)-1]
		last.FileIDs = append(last.FileIDs, fileID)
	}
	return out, rows.Err()
}

// DeleteObjectEdgesForFiles удаляет рёбра, зависящие хотя бы от одного из
// переданных файлов, вместе с их строками object_data_edge_dep (каскадом по
// edge_id). Ребро сносится целиком, даже если остальные его файлы не менялись:
// цепочка атрибуции пересобирается заново, а не чинится по частям.
//
// Вызывать ОБЯЗАТЕЛЬНО до DeleteSourceFiles: каскад по file_id снимет строки
// зависимостей вместе с файлом и оставит ребро без единой зависимости, то есть
// неудаляемым (инвариант object_data_edge_without_dep).
func (tx *WriteTx) DeleteObjectEdgesForFiles(fileIDs ...int64) error {
	if err := tx.check(); err != nil {
		return err
	}
	if len(fileIDs) == 0 {
		return nil
	}
	sel, args := edgesDependingOnFiles(fileIDs)
	return tx.c.exec(tx.ctx, `DELETE FROM object_data_edge WHERE id IN (`+sel+`)`, args...)
}

// DeleteObjectBadges снимает все бейджи перечисленных объектов. Бейдж не
// привязан к файлу (его поля закрыты контрактом §2), поэтому пересборка
// владельца чистит его по объекту: иначе счётчик прошлого прогона остался бы
// висеть навсегда.
func (tx *WriteTx) DeleteObjectBadges(objectIDs ...int64) error {
	if err := tx.check(); err != nil {
		return err
	}
	if len(objectIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(objectIDs))
	for _, id := range objectIDs {
		args = append(args, id)
	}
	return tx.c.exec(tx.ctx, `DELETE FROM object_badge WHERE object_id IN `+inClause(len(objectIDs)), args...)
}
