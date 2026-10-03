package store

import (
	"database/sql"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// ExportedMethodRow — экспортный метод модуля вместе с тем, что нужно, чтобы
// назвать его вызов: вид и имя модуля, объект-владелец, свойства общего модуля.
type ExportedMethodRow struct {
	SymbolID     int64
	UID          string
	ComponentID  string
	ModulePath   string
	ModuleKind   string
	ModuleName   string
	Kind         string
	NameDisplay  string
	DocFirstLine string
	// Doc: полный комментарий метода; пуст у индекса, собранного до схемы 8.
	Doc       string
	Region    string
	StartLine int
	// OwnerMType/OwnerName пусты, когда владелец модуля не определён.
	OwnerMType string
	OwnerName  string
	// ModuleProps — JSON свойств общего модуля (module_context.props); пусто у
	// остальных видов модулей.
	ModuleProps string
}

// ExportedMethodsInRegions читает экспортные процедуры и функции модулей
// перечисленных видов, чей путь областей начинается с одной из topRegions:
// сама область либо любая вложенная в неё.
//
// упрощение: имя области сравнивается как записано в модуле, хотя
// идентификаторы BSL регистронезависимы: у кириллицы SQLite регистр не
// сворачивает, и область "программныйинтерфейс" отбор не найдёт. Путь снятия:
// хранить рядом нормализованный путь областей и отбирать по нему.
func (tx *ReadTx) ExportedMethodsInRegions(moduleKinds, topRegions []string) ([]ExportedMethodRow, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if len(moduleKinds) == 0 || len(topRegions) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(moduleKinds)+2*len(topRegions))
	for _, k := range moduleKinds {
		args = append(args, k)
	}
	regionCond := make([]string, 0, len(topRegions))
	for _, r := range topRegions {
		regionCond = append(regionCond, `s.region = ? OR s.region LIKE ? ESCAPE '\'`)
		args = append(args, r, escapeLike(r+domain.RegionPathSeparator)+`%`)
	}
	rows, err := tx.c.query(tx.ctx, `SELECT s.id, s.uid, mo.component_id, sf.rel_path, mo.kind, mo.name_display,
			s.kind, s.name_display, s.doc_first_line, s.doc, s.region, s.start_line,
			o.mtype, o.name_display, mc.props
		FROM symbol s
		JOIN module mo ON mo.id = s.module_id
		JOIN source_file sf ON sf.id = s.origin_file_id
		LEFT JOIN metadata_object o ON o.id = mo.owner_object_id
		LEFT JOIN module_context mc ON mc.module_id = mo.id
		WHERE s.is_export = 1 AND s.kind IN ('procedure','function')
			AND mo.kind IN (`+placeholders(len(moduleKinds))+`)
			AND (`+strings.Join(regionCond, " OR ")+`)
		ORDER BY s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportedMethodRow
	for rows.Next() {
		var r ExportedMethodRow
		var docFirst, doc, region, ownerMType, ownerName, props sql.NullString
		if err := rows.Scan(&r.SymbolID, &r.UID, &r.ComponentID, &r.ModulePath, &r.ModuleKind, &r.ModuleName,
			&r.Kind, &r.NameDisplay, &docFirst, &doc, &region, &r.StartLine,
			&ownerMType, &ownerName, &props); err != nil {
			return nil, err
		}
		r.DocFirstLine, r.Doc, r.Region = docFirst.String, doc.String, region.String
		r.OwnerMType, r.OwnerName, r.ModuleProps = ownerMType.String, ownerName.String, props.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// placeholders возвращает "?,?,?" на n параметров.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// ExternalCallCount считает, сколько раз символ зовут из других модулей:
// вызовы внутри своего модуля о востребованности метода снаружи не говорят.
// Запрос идёт по индексу callee_id и стоит столько, сколько у символа вызовов.
func (tx *ReadTx) ExternalCallCount(symbolID int64) (int, error) {
	if err := tx.check(); err != nil {
		return 0, err
	}
	rows, err := tx.c.query(tx.ctx, `
		SELECT COUNT(*)
		FROM call_edge e
		JOIN symbol caller ON caller.id = e.caller_id
		JOIN symbol callee ON callee.id = e.callee_id
		WHERE e.callee_id = ? AND caller.module_id != callee.module_id`, symbolID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// ExternalCallSite находит одно место, где символ зовут из другого модуля:
// файл и байтовое смещение начала ссылки на метод. Условие «другой модуль» то
// же, что у ExternalCallCount. Берётся первое по порядку индексации: пример
// один и тот же от вызова к вызову в пределах поколения индекса.
func (tx *ReadTx) ExternalCallSite(symbolID int64) (fileID int64, byteStart int, ok bool, err error) {
	if err := tx.check(); err != nil {
		return 0, 0, false, err
	}
	rows, err := tx.c.query(tx.ctx, `
		SELECT r.file_id, r.byte_start
		FROM call_edge e
		JOIN reference r ON r.id = e.ref_id
		JOIN symbol caller ON caller.id = e.caller_id
		JOIN symbol callee ON callee.id = e.callee_id
		WHERE e.callee_id = ? AND caller.module_id != callee.module_id
		ORDER BY e.id LIMIT 1`, symbolID)
	if err != nil {
		return 0, 0, false, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&fileID, &byteStart); err != nil {
			return 0, 0, false, err
		}
		return fileID, byteStart, true, rows.Err()
	}
	return 0, 0, false, rows.Err()
}
