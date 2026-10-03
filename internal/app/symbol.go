// Символьные инструменты: find_symbol, get_symbol,
// get_module_structure. GraphService (find_references, trace_call_graph)
// живёт рядом в graph.go — оба файла делят resolveModuleFileID,
// componentIDs, clampLimit и построители resource URI.
package app

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

const (
	defaultSymbolLimit = 50
	maxSymbolLimit     = 200
	// inlineBodyMaxChars — потолок инлайна тела символа. Больше — усечение +
	// resource link на полный текст (архитектура §21: «крупные артефакты
	// возвращаются как resource links вместо инлайна»).
	inlineBodyMaxChars = 4000
)

// SymbolItem — один символ в выдаче find_symbol/get_module_structure и
// заголовок get_symbol (архитектура §21: «uid, module, signature, span,
// export, directive»).
type SymbolItem struct {
	UID       string             `json:"uid"`
	Component domain.ComponentID `json:"component"`
	Module    string             `json:"module"`
	Kind      string             `json:"kind"`
	Name      string             `json:"name"`
	Signature string             `json:"signature,omitempty"`
	Span      domain.Span        `json:"span"`
	Export    bool               `json:"export"`
	Async     bool               `json:"async,omitempty"`
	Directive string             `json:"directive,omitempty"`
	// Intercepts — перехватчики этого символа в расширениях, применяющихся
	// к его компоненту (view=effective). Заполняется только
	// get_symbol и get_module_structure (find_symbol не платит цену
	// повторного разбора модулей расширений за каждую строку списка —
	// упрощение, см. docs/tools-index.md).
	Intercepts []InterceptItem `json:"intercepts,omitempty"`
}

// ParameterItem: параметр символа со значением по умолчанию.
type ParameterItem struct {
	Name       string `json:"name"`
	ByVal      bool   `json:"byVal"`
	HasDefault bool   `json:"hasDefault"`
	Default    string `json:"default,omitempty"`
}

// SymbolDetail — items[0] ответа get_symbol: символ + параметры + тело
// (усечённое, с resource link на полное) + признак расхождения с диском.
type SymbolDetail struct {
	SymbolItem
	Parameters       []ParameterItem `json:"parameters,omitempty"`
	Body             string          `json:"body,omitempty"`
	BodyTruncated    bool            `json:"bodyTruncated,omitempty"`
	BodyResourceURI  string          `json:"bodyResourceUri"`
	StaleAgainstDisk bool            `json:"staleAgainstDisk,omitempty"`
}

// ModuleStructureItem — items[0] ответа get_module_structure: символы,
// переменные и счётчики БЕЗ текста модуля (§21: «модуль целиком не
// возвращается никогда»).
//
// Regions: области модуля, в которых лежит хотя бы один символ, путём от
// внешней к внутренней ("ПрограммныйИнтерфейс/Данные"), в порядке появления
// символов. Область без символов в список не попадает: путь хранится у
// символа (symbol.region), отдельной таблицы областей в индексе нет.
type ModuleStructureItem struct {
	Module        string             `json:"module"`
	Component     domain.ComponentID `json:"component"`
	Kind          string             `json:"kind"`
	Symbols       []SymbolItem       `json:"symbols"`
	Variables     []SymbolItem       `json:"variables,omitempty"`
	Regions       []string           `json:"regions,omitempty"`
	SymbolCount   int                `json:"symbolCount"`
	VariableCount int                `json:"variableCount"`
}

// FindSymbolInput — вход find_symbol. View принят для единообразия входа
// индексных инструментов, но не влияет на выдачу, см.
// doc-комментарий SymbolItem.Intercepts.
type FindSymbolInput struct {
	Name      string
	Kind      string
	Module    string
	Component string
	View      string
	Limit     int
	Cursor    string
}

// GetSymbolInput — вход get_symbol: либо UID, либо пара Module+Name.
type GetSymbolInput struct {
	UID         string
	Module      string
	Name        string
	Component   string
	View        string
	IncludeBody bool
}

// GetModuleStructureInput — вход get_module_structure.
type GetModuleStructureInput struct {
	Module    string
	Component string
	View      string
}

// SymbolService — сервис за find_symbol, get_symbol, get_module_structure и их
// resource-выдачами (по одному сервису на группу связанных
// инструментов, здесь — три метода плюс два resource-хелпера).
type SymbolService struct{ projects *Projects }

// NewSymbolService строит сервис поверх общего резолвера проектов.
func NewSymbolService(p *Projects) *SymbolService { return &SymbolService{projects: p} }

// clampLimit нормализует лимит страницы: <=0 -> default, > max -> max.
func clampLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

// componentFromInput проверяет component (если передан) против манифеста
// активного проекта — общий шаг всех символьных/графовых инструментов.
func componentFromInput(op *openProject, raw string) (string, *Error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	cid := domain.ComponentID(raw)
	if _, ok := op.Manifest.Component(cid); !ok {
		return "", ComponentNotRegisteredError(cid, componentIDs(op.Manifest)).WithProject(op.Entry.ID)
	}
	return string(cid), nil
}

// resolveModuleFileID разрешает путь модуля в store file id тем же способом,
// каким store.SourceFileID уже адресует файлы: (component, rel_path). Когда
// component не сужен, перебираются все компоненты активного проекта — так
// module без component остаётся рабочим входом, а не требует знания id.
func resolveModuleFileID(tx *store.ReadTx, manifest workspace.Manifest, componentFilter, modulePath string) (fileID int64, componentID string, ok bool, err error) {
	path := domain.NormalizeModulePath(modulePath)
	try := func(id domain.ComponentID) (int64, bool, error) {
		return tx.SourceFileID(string(id), path)
	}
	if componentFilter != "" {
		fid, fok, ferr := try(domain.ComponentID(componentFilter))
		return fid, componentFilter, fok, ferr
	}
	for _, c := range manifest.Components {
		fid, fok, ferr := try(c.ID)
		if ferr != nil {
			return 0, "", false, ferr
		}
		if fok {
			return fid, string(c.ID), true, nil
		}
	}
	return 0, "", false, nil
}

func symbolItemFromRow(r store.SymbolRow) SymbolItem {
	return SymbolItem{
		UID: r.UID, Component: domain.ComponentID(r.ComponentID), Module: r.ModulePath,
		Kind: r.Kind, Name: r.NameDisplay, Signature: r.Signature, Span: r.Span,
		Export: r.IsExport, Async: r.IsAsync, Directive: r.Directive,
	}
}

// symbolCursorKey и splitSymbolCursorKey — keyset-курсор find_symbol:
// "name_norm\x00id", тот же приём, что srcResourceURI использует для spans.
func symbolCursorKey(nameNorm string, id int64) string {
	return nameNorm + "\x00" + strconv.FormatInt(id, 10)
}

func splitSymbolCursorKey(key string) (name string, id int64) {
	if key == "" {
		return "", 0
	}
	name, idStr, _ := strings.Cut(key, "\x00")
	id, _ = strconv.ParseInt(idStr, 10, 64)
	return name, id
}

// FindSymbol ищет символы по подстроке имени с опциональными фильтрами
// (точное имя: частный случай подстроки, отдельного режима не заведено).
func (s *SymbolService) FindSymbol(ctx context.Context, in FindSymbolInput) (Response[SymbolItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[SymbolItem]{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Response[SymbolItem]{}, NewError(CodeNotFound, "find_symbol требует непустой name",
			"передайте точное имя символа или его подстроку").WithProject(op.Entry.ID)
	}
	if in.Kind != "" && !domain.SymbolKind(in.Kind).Valid() {
		return Response[SymbolItem]{}, NewError(CodeNotFound, fmt.Sprintf("kind %q неизвестен", in.Kind),
			"допустимые значения: procedure, function, variable").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[SymbolItem]{}, cerr
	}
	limit := clampLimit(in.Limit, defaultSymbolLimit, maxSymbolLimit)
	nameNorm := domain.NormalizeName(name)
	paramsKey := fmt.Sprintf("n=%s&k=%s&m=%s&c=%s&l=%d", nameNorm, in.Kind, in.Module, componentID, limit)

	type txResult struct {
		rows []store.SymbolRow
		gen  domain.Generation
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen
		key, derr := DecodeCursor(in.Cursor, gen, paramsKey)
		if derr != nil {
			return out, derr
		}
		afterName, afterID := splitSymbolCursorKey(key)

		var moduleFileID int64
		if strings.TrimSpace(in.Module) != "" {
			fid, _, ok, merr := resolveModuleFileID(tx, op.Manifest, componentID, in.Module)
			if merr != nil {
				return out, merr
			}
			if !ok {
				return out, NotFoundError("модуль", in.Module, nil).WithProject(op.Entry.ID).WithGeneration(gen)
			}
			moduleFileID = fid
		}

		rows, ferr := tx.FindSymbols(store.SymbolSearch{
			NameNorm: nameNorm, Kind: in.Kind, ComponentID: componentID, ModuleFileID: moduleFileID,
			AfterName: afterName, AfterID: afterID, Limit: limit + 1,
		})
		if ferr != nil {
			return out, ferr
		}
		out.rows = rows
		return out, nil
	})
	if err != nil {
		return Response[SymbolItem]{}, err
	}

	hasMore := len(res.rows) > limit
	if hasMore {
		res.rows = res.rows[:limit]
	}
	items := make([]SymbolItem, len(res.rows))
	for i, r := range res.rows {
		items[i] = symbolItemFromRow(r)
	}
	resp := Response[SymbolItem]{Generation: res.gen, Items: items, TotalCount: len(items)}
	if hasMore {
		last := res.rows[len(res.rows)-1]
		resp.NextCursor = EncodeCursor(res.gen, symbolCursorKey(last.NameNorm, last.ID), paramsKey)
	}
	return withSnapshot(resp, snap), nil
}

// GetSymbol читает один символ точно: по uid либо по паре module+name.
// Тело режется из blob В ТОЙ ЖЕ read-транзакции, что и остальные
// выборки (§18.2): живой файл читается отдельно, только чтобы сравнить hash
// для staleAgainstDisk, и не участвует в построении тела.
func (s *SymbolService) GetSymbol(ctx context.Context, in GetSymbolInput) (Response[SymbolDetail], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[SymbolDetail]{}, err
	}
	uid := strings.TrimSpace(in.UID)
	module := strings.TrimSpace(in.Module)
	name := strings.TrimSpace(in.Name)
	if uid == "" && (module == "" || name == "") {
		return Response[SymbolDetail]{}, NewError(CodeNotFound, "get_symbol требует uid либо пару module+name",
			"передайте uid из find_symbol, либо оба поля module и name").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[SymbolDetail]{}, cerr
	}
	view, verr := parseView(in.View)
	if verr != nil {
		return Response[SymbolDetail]{}, verr.WithProject(op.Entry.ID)
	}

	type txResult struct {
		row           store.SymbolRow
		params        []store.ParameterRow
		gen           domain.Generation
		body          string
		bodyTruncated bool
		contentHash   string
		relPath       string
		intercepts    []resolve.Intercept
		warn          []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen

		var row store.SymbolRow
		var found bool
		if uid != "" {
			id, ok, nerr := tx.NodeID(uid)
			if nerr != nil {
				return out, nerr
			}
			if ok {
				r, sok, serr := tx.SymbolByID(id)
				if serr != nil {
					return out, serr
				}
				row, found = r, sok
			}
		} else {
			fid, _, ok, merr := resolveModuleFileID(tx, op.Manifest, componentID, module)
			if merr != nil {
				return out, merr
			}
			if ok {
				r, sok, serr := tx.SymbolByFileAndName(fid, domain.NormalizeName(name))
				if serr != nil {
					return out, serr
				}
				row, found = r, sok
			}
		}
		if !found {
			what := uid
			if what == "" {
				what = module + "." + name
			}
			return out, NotFoundError("символ", what, nil).WithProject(op.Entry.ID).WithGeneration(gen)
		}
		out.row = row

		params, perr := tx.SymbolParameters(row.ID)
		if perr != nil {
			return out, perr
		}
		out.params = params

		sf, sfOK, sferr := tx.SourceFileByID(row.OriginFileID)
		if sferr != nil {
			return out, sferr
		}
		if !sfOK {
			return out, fmt.Errorf("get_symbol: source_file %d символа %s отсутствует в store", row.OriginFileID, row.UID)
		}
		out.contentHash, out.relPath = sf.ContentHash, sf.RelPath

		if view == domain.ViewEffective {
			ics, warn, ierr := effectiveIntercepts(tx, domain.ComponentID(row.ComponentID), row.ModulePath)
			if ierr != nil {
				return out, ierr
			}
			out.warn = warn
			for _, ic := range ics {
				if ic.TargetNameNorm == row.NameNorm {
					out.intercepts = append(out.intercepts, ic)
				}
			}
			for _, c := range resolve.DetectInsteadConflicts(out.intercepts) {
				out.warn = append(out.warn, interceptConflictWarning(c))
			}
		}

		if in.IncludeBody {
			blob, berr := tx.Blob(sf.ContentHash)
			if berr != nil {
				return out, berr
			}
			if row.Span.StartByte >= 0 && row.Span.EndByte <= len(blob) && row.Span.StartByte <= row.Span.EndByte {
				text := string(blob[row.Span.StartByte:row.Span.EndByte])
				if len(text) > inlineBodyMaxChars {
					out.body, out.bodyTruncated = text[:inlineBodyMaxChars], true
				} else {
					out.body = text
				}
			}
		}
		return out, nil
	})
	if err != nil {
		return Response[SymbolDetail]{}, err
	}

	stale := fileDiffersOnDisk(op, res.row.ComponentID, res.relPath, res.contentHash)

	item := SymbolDetail{
		SymbolItem:       symbolItemFromRow(res.row),
		BodyResourceURI:  symbolResourceURI(op.Entry.ID, res.row.UID, res.gen),
		StaleAgainstDisk: stale,
	}
	item.Intercepts = interceptItems(res.intercepts)
	for _, p := range res.params {
		item.Parameters = append(item.Parameters, ParameterItem{
			Name: p.Name, ByVal: p.ByVal, HasDefault: p.DefaultExpr != "", Default: p.DefaultExpr,
		})
	}
	if in.IncludeBody {
		item.Body, item.BodyTruncated = res.body, res.bodyTruncated
	}

	resp := Response[SymbolDetail]{Generation: res.gen, Items: []SymbolDetail{item}, TotalCount: 1, Warnings: res.warn}
	if item.BodyTruncated {
		resp.Warnings = append(resp.Warnings, Warning{
			Code:    "body_truncated",
			Message: fmt.Sprintf("тело обрезано до %d символов", inlineBodyMaxChars),
			Hint:    "заберите полный текст по bodyResourceUri: " + item.BodyResourceURI,
		})
	}
	if stale {
		resp.Stale = true
		resp.Warnings = append(resp.Warnings, Warning{
			Code:    "staleAgainstDisk",
			Message: "файл на диске отличается от последней индексированной версии; ответ построен по индексу (blob), не по диску",
			Hint:    "вызовите reindex, если нужна свежая версия",
		})
	}
	return withSnapshot(resp, snap), nil
}

// fileDiffersOnDisk сравнивает hash последней индексированной версии файла с
// текущим содержимым на диске. Отсутствие возможности прочитать файл (удалён,
// вне workspace) НЕ считается расхождением — сверить нечем, ответ всё равно
// из blob.
func fileDiffersOnDisk(op *openProject, componentID, relPath, indexedHash string) bool {
	if relPath == "" || indexedHash == "" {
		return false
	}
	comp, ok := op.Manifest.Component(domain.ComponentID(componentID))
	if !ok || comp.AbsRoot == "" {
		return false
	}
	abs, err := workspace.SafeJoin(comp.AbsRoot, relPath)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	return store.HashContent(data) != indexedHash
}

// GetModuleStructure отдаёт символы, переменные и счётчики модуля без его
// текста (архитектура §21: «модуль целиком не возвращается никогда»).
func (s *SymbolService) GetModuleStructure(ctx context.Context, in GetModuleStructureInput) (Response[ModuleStructureItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[ModuleStructureItem]{}, err
	}
	module := strings.TrimSpace(in.Module)
	if module == "" {
		return Response[ModuleStructureItem]{}, NewError(CodeNotFound, "get_module_structure требует module",
			"передайте путь модуля относительно корня компонента").WithProject(op.Entry.ID)
	}
	componentID, cerr := componentFromInput(op, in.Component)
	if cerr != nil {
		return Response[ModuleStructureItem]{}, cerr
	}
	view, verr := parseView(in.View)
	if verr != nil {
		return Response[ModuleStructureItem]{}, verr.WithProject(op.Entry.ID)
	}

	type txResult struct {
		symbols    []store.SymbolRow
		mod        store.ModuleRow
		modOK      bool
		gen        domain.Generation
		compID     string
		intercepts []resolve.Intercept
		warn       []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen

		fid, resolvedComponent, ok, merr := resolveModuleFileID(tx, op.Manifest, componentID, module)
		if merr != nil {
			return out, merr
		}
		if !ok {
			return out, NotFoundError("модуль", module, nil).WithProject(op.Entry.ID).WithGeneration(gen)
		}
		out.compID = resolvedComponent

		mod, modOK, moderr := tx.ModuleByFile(fid)
		if moderr != nil {
			return out, moderr
		}
		out.mod, out.modOK = mod, modOK

		symbols, serr := tx.SymbolsByOriginFile(fid)
		if serr != nil {
			return out, serr
		}
		out.symbols = symbols

		if view == domain.ViewEffective {
			ics, warn, ierr := effectiveIntercepts(tx, domain.ComponentID(resolvedComponent), domain.NormalizeModulePath(module))
			if ierr != nil {
				return out, ierr
			}
			out.intercepts, out.warn = ics, warn
			for _, c := range resolve.DetectInsteadConflicts(ics) {
				out.warn = append(out.warn, interceptConflictWarning(c))
			}
		}
		return out, nil
	})
	if err != nil {
		return Response[ModuleStructureItem]{}, err
	}

	byTarget := map[string][]resolve.Intercept{}
	for _, ic := range res.intercepts {
		byTarget[ic.TargetNameNorm] = append(byTarget[ic.TargetNameNorm], ic)
	}

	item := ModuleStructureItem{Module: domain.NormalizeModulePath(module), Component: domain.ComponentID(res.compID)}
	if res.modOK {
		item.Kind = res.mod.Kind
	}
	seenRegion := map[string]bool{}
	for _, r := range res.symbols {
		si := symbolItemFromRow(r)
		si.Intercepts = interceptItems(byTarget[r.NameNorm])
		if r.Kind == string(domain.SymbolVariable) {
			item.Variables = append(item.Variables, si)
		} else {
			item.Symbols = append(item.Symbols, si)
		}
		if r.Region != "" && !seenRegion[r.Region] {
			seenRegion[r.Region] = true
			item.Regions = append(item.Regions, r.Region)
		}
	}
	item.SymbolCount, item.VariableCount = len(item.Symbols), len(item.Variables)

	resp := Response[ModuleStructureItem]{Generation: res.gen, Items: []ModuleStructureItem{item}, TotalCount: 1, Warnings: res.warn}
	return withSnapshot(resp, snap), nil
}

// --- resource URI: onec://symbol/... и onec://src/... ---

// symbolResourceURI строит ссылку на полный текст символа (§21).
// Percent-encoding кириллицы — тем же приёмом, что существующий
// cmd/mcp1c/resources.go:parseObjectURI (RFC 6570 template expansion).
func symbolResourceURI(project domain.ProjectID, uid string, gen domain.Generation) string {
	return fmt.Sprintf("onec://symbol/%s/%s?gen=%s",
		url.PathEscape(string(project)), url.PathEscape(uid), url.QueryEscape(string(gen)))
}

// referencesResourceURI строит ссылку на полный список ссылок символа (§21).
func referencesResourceURI(project domain.ProjectID, uid string, gen domain.Generation) string {
	return fmt.Sprintf("onec://references/%s/%s?gen=%s",
		url.PathEscape(string(project)), url.PathEscape(uid), url.QueryEscape(string(gen)))
}

// srcResourceURI строит ссылку на точный фрагмент по content hash (§21).
func srcResourceURI(project domain.ProjectID, component, relPath, hash string, start, end int) string {
	return fmt.Sprintf("onec://src/%s/%s/%s?hash=%s&start=%d&end=%d",
		url.PathEscape(string(project)), url.PathEscape(component), pathEscapeSegments(relPath),
		url.QueryEscape(hash), start, end)
}

func pathEscapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// ParseSymbolResourceURI разбирает "onec://symbol/{project}/{uid}?gen=...".
func ParseSymbolResourceURI(uri string) (project, uid, gen string, ok bool) {
	return parseTwoSegmentResource(uri, "onec://symbol/")
}

// ParseReferencesResourceURI разбирает "onec://references/{project}/{uid}?gen=...".
func ParseReferencesResourceURI(uri string) (project, uid, gen string, ok bool) {
	return parseTwoSegmentResource(uri, "onec://references/")
}

func parseTwoSegmentResource(uri, prefix string) (a, b, gen string, ok bool) {
	rest, found := strings.CutPrefix(uri, prefix)
	if !found {
		return "", "", "", false
	}
	rest, query, _ := strings.Cut(rest, "?")
	a, b, found = strings.Cut(rest, "/")
	if !found || a == "" || b == "" {
		return "", "", "", false
	}
	a = pathUnescapeOrSame(a)
	b = pathUnescapeOrSame(b)
	gen = queryValue(query, "gen")
	return a, b, gen, true
}

// ParseSrcResourceURI разбирает
// "onec://src/{project}/{component}/{path}?hash=...&start=...&end=...".
func ParseSrcResourceURI(uri string) (project, component, relPath, hash string, start, end int, ok bool) {
	rest, found := strings.CutPrefix(uri, "onec://src/")
	if !found {
		return "", "", "", "", 0, 0, false
	}
	rest, query, _ := strings.Cut(rest, "?")
	project, rest, found = strings.Cut(rest, "/")
	if !found || project == "" {
		return "", "", "", "", 0, 0, false
	}
	component, relPath, found = strings.Cut(rest, "/")
	if !found || component == "" || relPath == "" {
		return "", "", "", "", 0, 0, false
	}
	project = pathUnescapeOrSame(project)
	component = pathUnescapeOrSame(component)
	segs := strings.Split(relPath, "/")
	for i, s := range segs {
		segs[i] = pathUnescapeOrSame(s)
	}
	relPath = strings.Join(segs, "/")
	hash = queryValue(query, "hash")
	start, _ = strconv.Atoi(queryValue(query, "start"))
	end, _ = strconv.Atoi(queryValue(query, "end"))
	return project, component, relPath, hash, start, end, true
}

func pathUnescapeOrSame(s string) string {
	if d, err := url.PathUnescape(s); err == nil {
		return d
	}
	return s
}

func queryValue(query, key string) string {
	values, err := url.ParseQuery(query)
	if err != nil {
		return ""
	}
	v := values.Get(key)
	if d, err := url.QueryUnescape(v); err == nil {
		return d
	}
	return v
}

// ResourceSymbolBody отдаёт полный (неусечённый) текст символа по uid — для
// onec://symbol/{project}/{uid}?gen=... (§21). gen пустой пропускает проверку
// поколения (агент мог не передать); несовпадающий gen -> resource_expired.
func (s *SymbolService) ResourceSymbolBody(ctx context.Context, projectArg, uid, genArg string) (string, error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return "", err
	}
	if projectArg != "" && string(op.Entry.ID) != projectArg {
		return "", NewError(CodeNotFound, fmt.Sprintf("проект %q сейчас не активен", projectArg),
			"активный проект: "+string(op.Entry.ID)).WithProject(op.Entry.ID)
	}
	uri := fmt.Sprintf("onec://symbol/%s/%s", projectArg, uid)
	text, err := readTx(ctx, op.Store, func(tx *store.ReadTx) (string, error) {
		gen, gerr := tx.Generation()
		if gerr != nil {
			return "", gerr
		}
		if genArg != "" && genArg != string(gen) {
			return "", ResourceExpiredError(uri, fmt.Sprintf("построен для generation %s, текущее — %s", genArg, gen)).
				WithProject(op.Entry.ID).WithGeneration(gen)
		}
		id, ok, nerr := tx.NodeID(uid)
		if nerr != nil {
			return "", nerr
		}
		if !ok {
			return "", ResourceExpiredError(uri, "символ больше не найден в индексе").WithProject(op.Entry.ID).WithGeneration(gen)
		}
		row, sok, serr := tx.SymbolByID(id)
		if serr != nil {
			return "", serr
		}
		if !sok {
			return "", ResourceExpiredError(uri, "символ больше не найден в индексе").WithProject(op.Entry.ID).WithGeneration(gen)
		}
		sf, sfOK, sferr := tx.SourceFileByID(row.OriginFileID)
		if sferr != nil {
			return "", sferr
		}
		if !sfOK {
			return "", fmt.Errorf("source_file %d символа %s отсутствует", row.OriginFileID, uid)
		}
		blob, berr := tx.Blob(sf.ContentHash)
		if berr != nil {
			return "", ResourceExpiredError(uri, "blob файла вычищен по TTL").WithProject(op.Entry.ID).WithGeneration(gen)
		}
		if row.Span.StartByte < 0 || row.Span.EndByte > len(blob) || row.Span.StartByte > row.Span.EndByte {
			return "", fmt.Errorf("span символа %s вне границ blob", uid)
		}
		return string(blob[row.Span.StartByte:row.Span.EndByte]), nil
	})
	return text, err
}

// ResourceSrcFragment отдаёт точный фрагмент по content hash — для
// onec://src/{project}/{component}/{path}?hash=...&start=...&end=... (§21).
// Адресация ИМЕННО по hash (content-addressed, TTL — §18.2), не по текущему
// файлу: несовпадение hash в индексе неважно, важно, жив ли ещё сам blob.
func (s *SymbolService) ResourceSrcFragment(ctx context.Context, projectArg, hash string, start, end int) (string, error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return "", err
	}
	if projectArg != "" && string(op.Entry.ID) != projectArg {
		return "", NewError(CodeNotFound, fmt.Sprintf("проект %q сейчас не активен", projectArg),
			"активный проект: "+string(op.Entry.ID)).WithProject(op.Entry.ID)
	}
	uri := fmt.Sprintf("onec://src/%s/...?hash=%s&start=%d&end=%d", projectArg, hash, start, end)
	if hash == "" || start < 0 || end < start {
		return "", NewError(CodeNotFound, "некорректные параметры ресурса onec://src",
			"нужны hash, start>=0, end>=start").WithProject(op.Entry.ID)
	}
	return readTx(ctx, op.Store, func(tx *store.ReadTx) (string, error) {
		gen, gerr := tx.Generation()
		if gerr != nil {
			return "", gerr
		}
		blob, berr := tx.Blob(hash)
		if berr != nil || len(blob) == 0 {
			return "", ResourceExpiredError(uri, "blob по этому hash больше не хранится (вычищен по TTL или сменилась эпоха)").
				WithProject(op.Entry.ID).WithGeneration(gen)
		}
		if end > len(blob) {
			return "", ResourceExpiredError(uri, "запрошенные границы выходят за пределы текущего blob").
				WithProject(op.Entry.ID).WithGeneration(gen)
		}
		return string(blob[start:end]), nil
	})
}
