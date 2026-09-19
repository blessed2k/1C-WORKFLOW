package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Файл: типизированная выборка объектного графа для сервиса графа:
// соседи узла, страничность, бейджи, evidence ребра и топ перегруженных узлов.
// SQL живёт только здесь (RuleSQLOnlyInStore).

// Направления обхода. Пустое значение читается как «оба».
const (
	EdgeDirectionOut  = "out"
	EdgeDirectionIn   = "in"
	EdgeDirectionBoth = "both"
)

// Оси сортировки god-node. Пустое значение читается как «сумма».
const (
	GodNodeByFanIn  = "fan-in"
	GodNodeByFanOut = "fan-out"
	GodNodeByTotal  = "total"
)

// ObjectDataEdgeRow — строка object_data_edge как она лежит в индексе. Имена
// объектов вызывающий берёт отдельно (MetadataObjectByID): ребро само по себе
// знает только id.
type ObjectDataEdgeRow struct {
	ID            int64
	FromObjectID  int64
	ToObjectID    int64
	Kind          string
	Layer         string
	Provenance    string
	Confidence    float64
	Mode          string
	InTransaction *bool
	Evidence      string
}

// ObjectEdgeFilter — фильтр соседей узла. ObjectID обязателен: без него это
// был бы полный скан таблицы рёбер на реальной выгрузке.
type ObjectEdgeFilter struct {
	ObjectID int64
	// ObjectIDs, когда не пуст, заменяет ObjectID: рёбра любой из строк.
	// Нужен режимам карты «до и после расширений» (веха В3): заимствованный
	// объект лежит в индексе отдельной строкой на каждый слой, и рёбра
	// расширения висят на строке расширения, а не базы. Список короткий
	// (по строке на компонент), поэтому идёт плейсхолдерами.
	ObjectIDs     []int64
	Direction     string   // out|in|both; пусто — both
	Kinds         []string // пусто — любой вид ребра
	Layer         string   // пусто — любой слой
	ExcludeLayer  string   // непусто: рёбра всех слоёв, кроме этого
	MinConfidence float64
	AfterID       int64
	Limit         int
}

// edgeSelector — какие рёбра вообще берутся в расчёт: вид, слой, порог
// достоверности. Отдельный тип, потому что условие обязано быть ОДНО на все
// выборки графа: god-node считает по тем же рёбрам, что показывает карта, и
// держать эту гарантию на совпадении двух рукописных условий нельзя — они
// разъезжаются на первом же новом фильтре.
type edgeSelector struct {
	kinds         []string
	layer         string
	excludeLayer  string
	minConfidence float64
}

// where отдаёт хвост условия (начинается с AND: вызывающий ставит перед ним
// либо своё условие, либо 1=1).
func (s edgeSelector) where() (string, []any) { return s.whereOn("") }

// whereOn: то же условие с префиксом таблицы рёбер (alias + "."), когда в
// запросе есть соединение с metadata_object и layer без префикса двусмыслен.
func (s edgeSelector) whereOn(prefix string) (string, []any) {
	q := ``
	var args []any
	if len(s.kinds) > 0 {
		q += ` AND ` + prefix + `kind IN ` + inClause(len(s.kinds))
		for _, k := range s.kinds {
			args = append(args, k)
		}
	}
	if s.layer != "" {
		q += ` AND ` + prefix + `layer = ?`
		args = append(args, s.layer)
	}
	if s.excludeLayer != "" {
		q += ` AND ` + prefix + `layer <> ?`
		args = append(args, s.excludeLayer)
	}
	if s.minConfidence > 0 {
		q += ` AND ` + prefix + `confidence >= ?`
		args = append(args, s.minConfidence)
	}
	return q, args
}

func (f ObjectEdgeFilter) selector() edgeSelector {
	return edgeSelector{kinds: f.Kinds, layer: f.Layer, excludeLayer: f.ExcludeLayer, minConfidence: f.MinConfidence}
}

func (f GodNodeFilter) selector() edgeSelector {
	return edgeSelector{kinds: f.Kinds, layer: f.Layer, minConfidence: f.MinConfidence}
}

// edgeWhere собирает условие фильтра соседей: направление плюс общий отбор
// рёбер. Общее для выборки и счётчика — страница и total обязаны считаться по
// одному и тому же множеству.
func (f ObjectEdgeFilter) edgeWhere() (string, []any) {
	q := ``
	var args []any
	ids := f.ObjectIDs
	if len(ids) == 0 {
		ids = []int64{f.ObjectID}
	}
	in := inClause(len(ids))
	switch f.Direction {
	case EdgeDirectionOut:
		q += ` AND from_object_id IN ` + in
		args = appendIDs(args, ids)
	case EdgeDirectionIn:
		q += ` AND to_object_id IN ` + in
		args = appendIDs(args, ids)
	default:
		q += ` AND (from_object_id IN ` + in + ` OR to_object_id IN ` + in + `)`
		args = appendIDs(appendIDs(args, ids), ids)
	}
	sel, selArgs := f.selector().where()
	return q + sel, append(args, selArgs...)
}

func appendIDs(args []any, ids []int64) []any {
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

const objectEdgeColumns = `id, from_object_id, to_object_id, kind, layer, provenance, confidence,
	mode, in_transaction, evidence`

// ObjectDataEdges отдаёт рёбра узла по фильтру, не больше Limit+1 (та же
// пагинационная идиома, что RegisterAccesses: лишняя строка — признак
// следующей страницы, курсор считает вызывающий).
func (tx *ReadTx) ObjectDataEdges(f ObjectEdgeFilter) ([]ObjectDataEdgeRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	where, args := f.edgeWhere()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + objectEdgeColumns + ` FROM object_data_edge WHERE id > ?` + where + ` ORDER BY id LIMIT ?`
	args = append(append([]any{f.AfterID}, args...), limit+1)
	rows, err := tx.c.query(tx.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectDataEdgeRow
	for rows.Next() {
		r, err := scanObjectDataEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountObjectDataEdges — сколько рёбер попадает под фильтр целиком, без
// страницы: это total ответа, и считается он по тому же условию.
func (tx *ReadTx) CountObjectDataEdges(f ObjectEdgeFilter) (int64, error) {
	if err := tx.check(); err != nil {
		return 0, err
	}
	where, args := f.edgeWhere()
	return tx.c.queryInt(tx.ctx, `SELECT COUNT(*) FROM object_data_edge WHERE 1=1`+where, args...)
}

// ObjectEdgeKey: связь без учёта строки: откуда, куда, какой вид.
type ObjectEdgeKey struct {
	FromObjectID int64
	ToObjectID   int64
	Kind         string
}

// ObjectEdgeKeysInLayer отдаёт связи слоя layer из любой строки fromIDs в
// любую строку toIDs, одним запросом. Так режим diff узнаёт, у каких рёбер
// расширений есть базовый двойник, не спрашивая про каждое ребро отдельно.
// Списки уходят JSON-массивами через json_each (длина не ограничена), отбор
// идёт по idx_ode_from.
func (tx *ReadTx) ObjectEdgeKeysInLayer(fromIDs, toIDs []int64, layer string) ([]ObjectEdgeKey, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if len(fromIDs) == 0 || len(toIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.c.query(tx.ctx, `SELECT DISTINCT from_object_id, to_object_id, kind FROM object_data_edge
		WHERE from_object_id IN (SELECT value FROM json_each(?))
		  AND to_object_id IN (SELECT value FROM json_each(?))
		  AND layer = ?`, int64ListJSON(fromIDs), int64ListJSON(toIDs), layer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectEdgeKey
	for rows.Next() {
		var k ObjectEdgeKey
		if err := rows.Scan(&k.FromObjectID, &k.ToObjectID, &k.Kind); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AllObjectEdgeKinds: все виды рёбер объектного графа. Нужен выборкам по
// слою без фильтра вида: индекс idx_ode_kind_layer(kind, layer) работает,
// только когда вид задан, поэтому «любой вид» передаётся перечнем.
var AllObjectEdgeKinds = []string{
	EdgeWritesRegister, EdgeReadsRegister, EdgeWritesDeclared, EdgeReadsQuery, EdgeRefAttribute, EdgeCreates,
}

// ObjectDataEdgesInLayers отдаёт рёбра перечисленных слоёв по всей таблице,
// в порядке id, не больше limit+1 (лишняя строка: признак обрезания). Точка
// входа панели god-node в режиме diff: слои расширений вызывающий берёт из
// component. Условие kind IN (...) AND layer IN (...) ложится на
// idx_ode_kind_layer (закреплено TestObjectDataEdgesInLayersUsesIndex), а
// прежнее layer <> 'base' сканировало всю таблицу.
func (tx *ReadTx) ObjectDataEdgesInLayers(layers, kinds []string, minConfidence float64, limit int) ([]ObjectDataEdgeRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return nil, nil
	}
	q, args := objectDataEdgesInLayersQuery(layers, kinds, minConfidence, limit)
	rows, err := tx.c.query(tx.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectDataEdgeRow
	for rows.Next() {
		r, err := scanObjectDataEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func objectDataEdgesInLayersQuery(layers, kinds []string, minConfidence float64, limit int) (string, []any) {
	if len(kinds) == 0 {
		kinds = AllObjectEdgeKinds
	}
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + objectEdgeColumns + ` FROM object_data_edge WHERE kind IN ` + inClause(len(kinds)) +
		` AND layer IN ` + inClause(len(layers))
	var args []any
	for _, k := range kinds {
		args = append(args, k)
	}
	for _, l := range layers {
		args = append(args, l)
	}
	if minConfidence > 0 {
		q += ` AND confidence >= ?`
		args = append(args, minConfidence)
	}
	q += ` ORDER BY id LIMIT ?`
	return q, append(args, limit+1)
}

// ObjectDataEdgeByID читает ребро по id: точка входа evidence.
func (tx *ReadTx) ObjectDataEdgeByID(id int64) (ObjectDataEdgeRow, bool, error) {
	if err := tx.check(); err != nil {
		return ObjectDataEdgeRow{}, false, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT `+objectEdgeColumns+` FROM object_data_edge WHERE id = ?`, id)
	if err != nil {
		return ObjectDataEdgeRow{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ObjectDataEdgeRow{}, false, rows.Err()
	}
	r, err := scanObjectDataEdge(rows)
	if err != nil {
		return ObjectDataEdgeRow{}, false, err
	}
	return r, true, rows.Err()
}

// ObjectDataEdgeFiles — файлы, от которых зависит ребро, в порядке id. Для
// evidence это места, куда ведёт цепочка атрибуции: BSL-модули кодового ребра
// или XML владельца у декларированного.
func (tx *ReadTx) ObjectDataEdgeFiles(edgeID int64) ([]SourceFileRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT sf.id, sf.component_id, sf.rel_path, sf.content_hash, sf.size
		FROM object_data_edge_dep d JOIN source_file sf ON sf.id = d.file_id
		WHERE d.edge_id = ? ORDER BY sf.id`, edgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceFileRow
	for rows.Next() {
		var r SourceFileRow
		if err := rows.Scan(&r.ID, &r.ComponentID, &r.RelPath, &r.ContentHash, &r.Size); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ObjectBadges — бейджи узла в стабильном порядке. Пустой список означает, что
// дыр атрибуции у объекта не нашлось.
func (tx *ReadTx) ObjectBadges(objectID int64) ([]ObjectBadge, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	rows, err := tx.c.query(tx.ctx, `SELECT object_id, badge, layer, count FROM object_badge
		WHERE object_id = ? ORDER BY badge, layer`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectBadge
	for rows.Next() {
		var b ObjectBadge
		if err := rows.Scan(&b.ObjectID, &b.Badge, &b.Layer, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GodNodeRow — перегруженный узел: сколько рёбер входит и сколько выходит.
type GodNodeRow struct {
	ObjectID    int64
	MType       string
	NameDisplay string
	FanIn       int64
	FanOut      int64
}

// GodNodeFilter — фильтр панели god-node. Считается по тем же рёбрам, что
// показывает карта: иначе топ объяснял бы картину, которой на экране нет.
type GodNodeFilter struct {
	Kinds         []string
	Layer         string
	MinConfidence float64
	MTypes        []string // виды объектов; пусто — любые
	By            string   // fan-in|fan-out|total; пусто — total
	Limit         int
	// MergeLayers склеивает строки одного объекта из разных слоёв (тот же
	// вид и имя) в один узел и считает СВЯЗИ, а не строки рёбер: связь базы
	// и то же самое ребро расширения (те же объекты, тот же вид) это одна
	// связь. Режим effective карты. ObjectID строки ответа: одна из строк
	// объекта, канонический узел выбирает вызывающий.
	MergeLayers bool
}

// GodNodes отдаёт топ узлов по fan-in/fan-out. Ось сортировки разбирается
// switch-ем, а не подстановкой строки вызывающего в SQL. Суммы называются
// deg_in/deg_out, а не fan_in/fan_out: при одноимённой колонке подзапроса
// выражение в ORDER BY SQLite связывает с колонкой одной строки группы, а не
// с суммой (TestObjectGraphViewGodNodesTotalSumsGroup).
func (tx *ReadTx) GodNodes(f GodNodeFilter) ([]GodNodeRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	var order string
	switch f.By {
	case GodNodeByFanIn:
		order = `deg_in DESC, deg_out DESC`
	case GodNodeByFanOut:
		order = `deg_out DESC, deg_in DESC`
	case GodNodeByTotal, "":
		order = `(deg_in + deg_out) DESC`
	default:
		return nil, fmt.Errorf("неизвестная ось god-node: %q", f.By)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	q, args := godNodesQuery(f)
	if len(f.MTypes) > 0 {
		q += ` AND node_mtype IN ` + inClause(len(f.MTypes))
		for _, m := range f.MTypes {
			args = append(args, m)
		}
	}
	q += ` ORDER BY ` + order + `, node_id LIMIT ?`
	args = append(args, limit)

	rows, err := tx.c.query(tx.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GodNodeRow
	for rows.Next() {
		var r GodNodeRow
		if err := rows.Scan(&r.ObjectID, &r.MType, &r.NameDisplay, &r.FanIn, &r.FanOut); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// godNodesQuery строит выборку god-node до фильтра видов и сортировки:
// SELECT ... WHERE 1=1, колонки node_id, node_mtype, name, deg_in, deg_out.
// Агрегация идёт от рёбер, а не от объектов: перебирать всю metadata_object
// ради узлов без единого ребра нечем оправдать.
func godNodesQuery(f GodNodeFilter) (string, []any) {
	edgeWhere, edgeArgs := f.selector().where()
	if !f.MergeLayers {
		q := `SELECT * FROM (SELECT o.id AS node_id, o.mtype AS node_mtype, o.name_display, a.s_in AS deg_in, a.s_out AS deg_out FROM (
		SELECT object_id, SUM(fin) AS s_in, SUM(fout) AS s_out FROM (
		  SELECT to_object_id AS object_id, 1 AS fin, 0 AS fout FROM object_data_edge WHERE 1=1` + edgeWhere + `
		  UNION ALL
		  SELECT from_object_id AS object_id, 0 AS fin, 1 AS fout FROM object_data_edge WHERE 1=1` + edgeWhere + `
		) GROUP BY object_id) a
		JOIN metadata_object o ON o.id = a.object_id) WHERE 1=1`
		return q, append(append([]any{}, edgeArgs...), edgeArgs...)
	}
	relWhere, relArgs := f.selector().whereOn("e.")
	// Связь: пара объектов (вид и имя с обеих сторон) плюс вид ребра, без
	// строки и слоя. Узел: объект, id и имя берутся у любой его строки.
	q := `WITH rel AS (
		  SELECT DISTINCT fo.mtype AS fm, fo.name_norm AS fn, t.mtype AS tm, t.name_norm AS tn, e.kind
		  FROM object_data_edge e
		  JOIN metadata_object fo ON fo.id = e.from_object_id
		  JOIN metadata_object t ON t.id = e.to_object_id
		  WHERE 1=1` + relWhere + `)
		SELECT * FROM (SELECT
		  (SELECT MIN(o.id) FROM metadata_object o WHERE o.name_norm = a.n AND o.mtype = a.m) AS node_id,
		  a.m AS node_mtype,
		  (SELECT MIN(o.name_display) FROM metadata_object o WHERE o.name_norm = a.n AND o.mtype = a.m) AS name_display,
		  a.s_in AS deg_in, a.s_out AS deg_out
		FROM (SELECT m, n, SUM(fin) AS s_in, SUM(fout) AS s_out FROM (
		  SELECT tm AS m, tn AS n, 1 AS fin, 0 AS fout FROM rel
		  UNION ALL
		  SELECT fm, fn, 0, 1 FROM rel
		) GROUP BY m, n) a) WHERE 1=1`
	return q, relArgs
}

func scanObjectDataEdge(rows *sql.Rows) (ObjectDataEdgeRow, error) {
	var r ObjectDataEdgeRow
	var mode sql.NullString
	var inTx sql.NullInt64
	if err := rows.Scan(&r.ID, &r.FromObjectID, &r.ToObjectID, &r.Kind, &r.Layer, &r.Provenance,
		&r.Confidence, &mode, &inTx, &r.Evidence); err != nil {
		return ObjectDataEdgeRow{}, err
	}
	r.Mode = mode.String
	if inTx.Valid {
		b := inTx.Int64 != 0
		r.InTransaction = &b
	}
	return r, nil
}

// Ярусы поиска объекта по имени: точное совпадение, префикс, вхождение.
// Ярус отдаётся вызывающему, чтобы тот мог показать, почему объект стоит выше.
const (
	ObjectSearchExact    = 0
	ObjectSearchPrefix   = 1
	ObjectSearchContains = 2
)

// ObjectSearchRow: объект метаданных, найденный по подстроке имени, со
// степенями в объектном графе: сколько рёбер входит и сколько выходит.
// Степень считается по всем рёбрам, без фильтра карты: поиск отвечает на
// вопрос «с чего начать», а не «что сейчас на экране».
type ObjectSearchRow struct {
	MetadataObjectRow
	Tier   int
	FanIn  int64
	FanOut int64
}

// objectDegreeColumns: степени узла коррелированными подзапросами. Каждый
// идёт по своему индексу (idx_ode_to, idx_ode_from), поэтому стоит
// пропорционально числу найденных объектов, а не размеру таблицы рёбер.
const objectDegreeColumns = `(SELECT COUNT(*) FROM object_data_edge e WHERE e.to_object_id = o.id) AS fan_in,
	(SELECT COUNT(*) FROM object_data_edge e WHERE e.from_object_id = o.id) AS fan_out`

// escapeLike экранирует служебные символы LIKE, чтобы «_» в имени объекта
// искался как символ, а не как «любой один».
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// SearchObjectsByName ищет объекты метаданных по подстроке нормализованного
// имени. Порядок: точное совпадение, затем префикс, затем вхождение; внутри
// яруса сначала объекты, у которых есть хоть одно ребро (со справочником
// без связей на карте делать нечего), затем короче имя, затем по алфавиту.
// Отдаёт не больше limit строк; вызывающий, которому нужен признак «есть
// ещё», просит limit+1.
//
// nameNorm обязан быть уже нормализован (domain.NormalizeName): name_norm
// хранится в нижнем регистре, а LOWER в SQLite кириллицу не понижает.
func (tx *ReadTx) SearchObjectsByName(nameNorm string, limit int) ([]ObjectSearchRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if nameNorm == "" {
		return nil, fmt.Errorf("пустая строка поиска объекта")
	}
	if limit <= 0 {
		limit = 20
	}
	esc := escapeLike(nameNorm)
	q := `SELECT id, component_id, uuid, mtype, name_norm, name_display, synonym, file_id, props, layer,
		tier, fan_in, fan_out FROM (
		SELECT o.id, o.component_id, o.uuid, o.mtype, o.name_norm, o.name_display, o.synonym, o.file_id, o.props, o.layer,
			CASE WHEN o.name_norm = ? THEN 0 WHEN o.name_norm LIKE ? ESCAPE '\' THEN 1 ELSE 2 END AS tier,
			` + objectDegreeColumns + `
		FROM metadata_object o WHERE o.name_norm LIKE ? ESCAPE '\')
		ORDER BY tier, CASE WHEN fan_in + fan_out > 0 THEN 0 ELSE 1 END, length(name_norm), name_norm, mtype, component_id, id
		LIMIT ?`
	rows, err := tx.c.query(tx.ctx, q, nameNorm, esc+"%", "%"+esc+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObjectSearchRow
	for rows.Next() {
		var r ObjectSearchRow
		var uuid, synonym, props sql.NullString
		if err := rows.Scan(&r.ID, &r.ComponentID, &uuid, &r.MType, &r.NameNorm, &r.NameDisplay,
			&synonym, &r.FileID, &props, &r.Layer, &r.Tier, &r.FanIn, &r.FanOut); err != nil {
			return nil, err
		}
		r.UUID, r.Synonym, r.PropsJSON = uuid.String, synonym.String, props.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// ObjectDegree: сколько рёбер входит в узел и выходит из него, без фильтра
// карты. Карточка узла показывает это как «всего связей», чтобы было видно,
// раскрыт ли узел целиком.
func (tx *ReadTx) ObjectDegree(objectID int64) (fanIn, fanOut int64, err error) {
	if err := tx.check(); err != nil {
		return 0, 0, err
	}
	if fanIn, err = tx.c.queryInt(tx.ctx, `SELECT COUNT(*) FROM object_data_edge WHERE to_object_id = ?`, objectID); err != nil {
		return 0, 0, err
	}
	if fanOut, err = tx.c.queryInt(tx.ctx, `SELECT COUNT(*) FROM object_data_edge WHERE from_object_id = ?`, objectID); err != nil {
		return 0, 0, err
	}
	return fanIn, fanOut, nil
}
