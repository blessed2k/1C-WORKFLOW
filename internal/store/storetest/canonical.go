package storetest

import (
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// CanonicalDump снимает содержимое ЗАКОММИЧЕННОЙ эпохи построчно в виде, не
// зависящем от id (issue #3: сверка «инкремент == чистая пересборка» по
// таблицам SQLite, а не по слепку в памяти).
//
// Правило перевода id одно на все таблицы и выводится из схемы, а не из
// списка в тесте:
//   - id узла (node и все таблицы, чей id ссылается на node) заменяется его
//     identity_key;
//   - id строки остальных таблиц с INTEGER PRIMARY KEY заменяется
//     содержимым самой строки (все прочие колонки, уже переведённые): так
//     reference, source_file, role, object_data_edge адресуются естественным
//     ключом, и их дети сверяются по нему;
//   - любая колонка-внешний ключ переводится правилом целевой таблицы, как и
//     мягкая ссылка без REFERENCES (колонка *_object_id, id узла объекта).
//
// Не сверяются: meta (поколение, время валидации), generation_log, служебные
// таблицы FTS5 (раскладка сегментов зависит от порядка вставки), а у blob
// только образы без метки unreferenced_since: инкремент законно держит образы
// прежнего содержимого до GC по TTL, чистая пересборка их не знает.
func CanonicalDump(epochPath string) (map[string][]string, error) {
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(epochPath)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	d := &dumper{db: db, keys: map[string]map[int64]string{}, fks: map[string]map[string]string{}}
	tables, err := d.strings(`SELECT name FROM sqlite_master WHERE type='table'
		AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'fts_symbols_%'
		AND name NOT IN ('meta','generation_log') ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(tables))
	for _, t := range tables {
		rows, err := d.rows(t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t, err)
		}
		sort.Strings(rows)
		out[t] = rows
	}
	return out, nil
}

type dumper struct {
	db   *sql.DB
	keys map[string]map[int64]string  // таблица -> id -> естественный ключ
	fks  map[string]map[string]string // таблица -> колонка -> целевая таблица
}

func (d *dumper) strings(q string, args ...any) ([]string, error) {
	rs, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rs.Err()
}

func (d *dumper) foreignKeys(table string) (map[string]string, error) {
	if m, ok := d.fks[table]; ok {
		return m, nil
	}
	rs, err := d.db.Query(`SELECT "from", "table" FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	m := map[string]string{}
	for rs.Next() {
		var from, to string
		if err := rs.Scan(&from, &to); err != nil {
			return nil, err
		}
		m[from] = to
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	// Мягкие ссылки без REFERENCES (module.owner_object_id,
	// form.owner_object_id): id узла объекта метаданных, переводятся так же.
	cols, err := d.strings(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		if _, ok := m[c]; !ok && strings.HasSuffix(c, "_object_id") {
			m[c] = "node"
		}
	}
	d.fks[table] = m
	return m, nil
}

// keyMap: id -> естественный ключ строки таблицы (строится один раз).
func (d *dumper) keyMap(table string) (map[int64]string, error) {
	if m, ok := d.keys[table]; ok {
		return m, nil
	}
	m := map[int64]string{}
	if table == "node" {
		rs, err := d.db.Query(`SELECT id, identity_key FROM node`)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		for rs.Next() {
			var id int64
			var k string
			if err := rs.Scan(&id, &k); err != nil {
				return nil, err
			}
			m[id] = "node:" + k
		}
		d.keys[table] = m
		return m, rs.Err()
	}
	fks, err := d.foreignKeys(table)
	if err != nil {
		return nil, err
	}
	if fks["id"] == "node" {
		d.keys[table], err = d.keyMap("node")
		return d.keys[table], err
	}
	ids, rows, err := d.translated(table)
	if err != nil {
		return nil, err
	}
	for i, id := range ids {
		m[id] = table + "{" + rows[i] + "}"
	}
	d.keys[table] = m
	return m, nil
}

// rows: строки таблицы с переведёнными id, без собственного id.
func (d *dumper) rows(table string) ([]string, error) {
	fks, err := d.foreignKeys(table)
	if err != nil {
		return nil, err
	}
	ids, rows, err := d.translated(table)
	if err != nil {
		return nil, err
	}
	if fks["id"] == "node" || table == "node" {
		nm, err := d.keyMap("node")
		if err != nil {
			return nil, err
		}
		for i, id := range ids {
			rows[i] = nm[id] + " " + rows[i]
		}
	}
	return rows, nil
}

// translated читает таблицу и переводит внешние ключи. ids: собственный id
// строки (rowid у таблиц без INTEGER PRIMARY KEY), он в строку не входит.
func (d *dumper) translated(table string) ([]int64, []string, error) {
	fks, err := d.foreignKeys(table)
	if err != nil {
		return nil, nil, err
	}
	cols, err := d.strings(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, nil, err
	}
	var sel []string
	for _, c := range cols {
		if c == "id" || (table == "blob" && c == "unreferenced_since") {
			continue
		}
		sel = append(sel, `"`+c+`"`)
	}
	where := ""
	if table == "blob" {
		where = " WHERE unreferenced_since IS NULL"
	}
	q := `SELECT rowid, ` + strings.Join(sel, ",") + ` FROM "` + table + `"` + where
	if table == "fts_symbols" {
		q = `SELECT rowid, rowid, name, signature, doc FROM fts_symbols`
		sel = []string{"symbol_id", "name", "signature", "doc"}
		fks = map[string]string{"symbol_id": "symbol"}
	}
	rs, err := d.db.Query(q)
	if err != nil {
		return nil, nil, err
	}
	type raw struct {
		id   int64
		vals []any
	}
	var raws []raw
	for rs.Next() {
		vals := make([]any, len(sel))
		ptrs := make([]any, len(sel)+1)
		var id int64
		ptrs[0] = &id
		for i := range vals {
			ptrs[i+1] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			rs.Close()
			return nil, nil, err
		}
		raws = append(raws, raw{id, vals})
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, nil, err
	}
	ids := make([]int64, len(raws))
	rows := make([]string, len(raws))
	for ri, r := range raws {
		ids[ri] = r.id
		parts := make([]string, len(sel))
		for i, c := range sel {
			name := strings.Trim(c, `"`)
			v := r.vals[i]
			if target, ok := fks[name]; ok && v != nil && target != "blob" && target != "component" {
				m, err := d.keyMap(target)
				if err != nil {
					return nil, nil, err
				}
				id, _ := v.(int64)
				k, found := m[id]
				if !found {
					k = fmt.Sprintf("ВИСЯЧИЙ %s#%v", target, v)
				}
				parts[i] = name + "=" + k
				continue
			}
			parts[i] = fmt.Sprintf("%s=%v", name, printable(v))
		}
		rows[ri] = strings.Join(parts, "|")
	}
	return ids, rows, nil
}

func printable(v any) any {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return fmt.Sprintf("%x", x)
	}
	return v
}
