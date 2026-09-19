package graphweb

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// projectQueryParam — общий query-параметр всех маршрутов, кроме
// /api/projects: выбор проекта, когда --project указан несколько раз (spec
// §6, история 48). Пути раздела 8.1 не меняются — они не несут проект в
// самом пути, — поэтому адресация «пара проект+id» реализована query-
// параметром, а не сегментом пути.
const projectQueryParam = "project"

// Handler — HTTP-обработчик путей раздела 8.1 поверх набора открытых
// проектов. Строится один раз в cmd/mcp1c/graph.go на весь жизненный цикл
// процесса; per-request свежесть данных обеспечивает не Handler, а
// ProjectHandle.Open (см. doc.go).
type Handler struct {
	projects []ProjectHandle
	// locks[i] держится от открытия проекта projects[i] до его закрытия в
	// рамках одного запроса. Каждый запрос открывает индекс заново (doc.go),
	// и на реальной выгрузке десяток одновременных открытий одного проекта
	// ловил «database is locked» на соединении писателя: карта после
	// раскрытия нескольких узлов шлёт карточки и соседей пачкой. Запрос
	// короткий (единицы миллисекунд на ut_demo), очередь из них для
	// localhost-инструмента дешевле, чем 500 на части карточек.
	locks []sync.Mutex
}

// NewHandler строит http.Handler поверх заданных проектов. Порядок в срезе
// определяет порядок в ответе /api/projects — тот же, в котором перечислены
// флаги --project.
func NewHandler(projects []ProjectHandle) http.Handler {
	h := &Handler{projects: append([]ProjectHandle(nil), projects...), locks: make([]sync.Mutex, len(projects))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", h.handleProjects)
	// view=raw|effective|diff у node, neighbors, radius и godnodes: режимы
	// «до и после расширений» (веха В3, internal/app/objectgraph_view.go).
	mux.HandleFunc("GET /api/node/{id}", h.handleNode)
	mux.HandleFunc("GET /api/neighbors/{id}", h.handleNeighbors)
	mux.HandleFunc("GET /api/radius/{id}", h.handleRadius)
	mux.HandleFunc("GET /api/godnodes", h.handleGodNodes)
	mux.HandleFunc("GET /api/edge/{id}/evidence", h.handleEdgeEvidence)
	mux.HandleFunc("GET /api/search", h.handleSearch)
	registerAssets(mux) // тикет 10: SPA — GET / и GET /assets/cytoscape.min.js (assets.go)
	return mux
}

// resolveProject выбирает целевой ProjectHandle по query-параметру project.
// Без параметра: единственный открытый проект — выбирается им самим; больше
// одного — actionable-ошибка с перечнем (spec §6: «запрос без проекта при
// нескольких открытых даёт ошибку с перечнем, а не тихий выбор первого»).
func (h *Handler) resolveProject(r *http.Request) (*ProjectHandle, *app.Error) {
	want := strings.TrimSpace(r.URL.Query().Get(projectQueryParam))
	if want == "" {
		switch len(h.projects) {
		case 0:
			return nil, app.NewError(app.CodeNoActiveProject,
				"ни один проект не открыт",
				"запустите mcp1c graph с хотя бы одним --project")
		case 1:
			return &h.projects[0], nil
		default:
			return nil, app.NewError(codeAmbiguousProject,
				"открыто несколько проектов, нужен параметр project=",
				"доступные проекты: "+h.projectIDList())
		}
	}
	for i := range h.projects {
		if string(h.projects[i].ID) == want {
			return &h.projects[i], nil
		}
	}
	return nil, app.NewError(codeUnknownProject,
		fmt.Sprintf("проект %q не открыт", want),
		"доступные проекты: "+h.projectIDList())
}

func (h *Handler) projectIDList() string {
	ids := make([]string, len(h.projects))
	for i, p := range h.projects {
		ids[i] = string(p.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// openGraphService открывает СВЕЖИЙ ObjectGraphService для ph (см. doc.go)
// и отдаёт функцию его закрытия — вызывающий обязан defer её сразу после
// получения, до сериализации ответа.
func (h *Handler) openGraphService(r *http.Request, ph *ProjectHandle) (*app.ObjectGraphService, func(), *app.Error) {
	unlock := h.lock(ph)
	p, err := ph.Open(r.Context())
	if err != nil {
		unlock()
		var aerr *app.Error
		if errors.As(err, &aerr) {
			return nil, nil, aerr
		}
		return nil, nil, app.NewError(app.CodeNoActiveProject,
			fmt.Sprintf("проект %s: %v", ph.Root, err),
			"проверьте, что каталог существует и был проиндексирован")
	}
	return app.NewObjectGraphService(p, ph.RadiusNodesCap), func() { p.Close(); unlock() }, nil
}

// lock берёт замок проекта ph (ph указывает в h.projects, его отдаёт
// resolveProject) и возвращает функцию снятия.
func (h *Handler) lock(ph *ProjectHandle) func() {
	for i := range h.projects {
		if &h.projects[i] == ph {
			h.locks[i].Lock()
			return h.locks[i].Unlock
		}
	}
	return func() {}
}

// parsePathID разбирает числовой id из пути ({id} в /api/node/{id} и
// соседях) — все id графа приходят из предыдущего ответа как числа, текст
// сюда попасть не может, кроме как через руками собранный URL.
func parsePathID(r *http.Request) (int64, *app.Error) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest(fmt.Sprintf("id %q не число", raw), "id — целое число из предыдущего ответа")
	}
	return id, nil
}

func splitCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// queryInt/queryFloat читают числовой query-параметр, молча откатываясь на
// ноль при отсутствии или нечисловом значении — ObjectGraphService трактует
// ноль как «умолчание сервиса», это тот же контракт, что у Limit/Depth в
// internal/app/objectgraph.go (clampLimit, defaultRadiusDepth). Присылать
// отдельную ошибку на "limit=abc" избыточно: результат — тот же дефолт, что
// и на отсутствующем параметре, разницы для клиента нет.
func queryInt(r *http.Request, key string) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return 0
	}
	return n
}

func queryFloat(r *http.Request, key string) float64 {
	f, err := strconv.ParseFloat(r.URL.Query().Get(key), 64)
	if err != nil {
		return 0
	}
	return f
}

func (h *Handler) handleNode(w http.ResponseWriter, r *http.Request) {
	ph, aerr := h.resolveProject(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	id, aerr := parsePathID(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	gs, closeFn, aerr := h.openGraphService(r, ph)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	defer closeFn()
	resp, err := gs.Node(r.Context(), app.NodeInput{Target: app.ObjectTarget{ObjectID: id}, View: r.URL.Query().Get("view")})
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleNeighbors(w http.ResponseWriter, r *http.Request) {
	ph, aerr := h.resolveProject(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	id, aerr := parsePathID(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	gs, closeFn, aerr := h.openGraphService(r, ph)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	defer closeFn()
	q := r.URL.Query()
	in := app.NeighborsInput{
		ObjectID:      id,
		Direction:     q.Get("dir"),
		Kinds:         splitCSV(q.Get("kinds")),
		Layer:         q.Get("layer"),
		View:          q.Get("view"),
		MinConfidence: queryFloat(r, "minConfidence"),
		Limit:         queryInt(r, "limit"),
		Cursor:        q.Get("cursor"),
	}
	resp, err := gs.Neighbors(r.Context(), in)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleRadius(w http.ResponseWriter, r *http.Request) {
	ph, aerr := h.resolveProject(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	id, aerr := parsePathID(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	gs, closeFn, aerr := h.openGraphService(r, ph)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	defer closeFn()
	q := r.URL.Query()
	in := app.RadiusInput{
		Target:        app.ObjectTarget{ObjectID: id},
		Direction:     q.Get("dir"),
		Depth:         queryInt(r, "depth"),
		Kinds:         splitCSV(q.Get("kinds")),
		Layer:         q.Get("layer"),
		View:          q.Get("view"),
		MinConfidence: queryFloat(r, "minConfidence"),
		Limit:         queryInt(r, "limit"),
		Cursor:        q.Get("cursor"),
	}
	resp, err := gs.Radius(r.Context(), in)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleGodNodes(w http.ResponseWriter, r *http.Request) {
	ph, aerr := h.resolveProject(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	gs, closeFn, aerr := h.openGraphService(r, ph)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	defer closeFn()
	q := r.URL.Query()
	in := app.GodNodesInput{
		Kinds:         splitCSV(q.Get("kinds")),
		Layer:         q.Get("layer"),
		View:          q.Get("view"),
		MinConfidence: queryFloat(r, "minConfidence"),
		MTypes:        splitCSV(q.Get("type")),
		By:            q.Get("metric"),
		Limit:         queryInt(r, "top"),
	}
	resp, err := gs.GodNodes(r.Context(), in)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleEdgeEvidence(w http.ResponseWriter, r *http.Request) {
	ph, aerr := h.resolveProject(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	id, aerr := parsePathID(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	gs, closeFn, aerr := h.openGraphService(r, ph)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	defer closeFn()
	resp, err := gs.EdgeEvidence(r.Context(), app.EdgeEvidenceInput{EdgeID: id})
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSearch: поиск объекта по части имени: точка входа на карту без
// знания числового id. Пустой q отсекается здесь, до открытия проекта:
// это ошибка запроса, а не состояние индекса.
func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		respondErr(w, badRequest("пустая строка поиска: нужен параметр q", "передайте часть имени объекта, например /api/search?q=Заказ"))
		return
	}
	ph, aerr := h.resolveProject(r)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	gs, closeFn, aerr := h.openGraphService(r, ph)
	if aerr != nil {
		respondErr(w, aerr)
		return
	}
	defer closeFn()
	resp, err := gs.Search(r.Context(), app.SearchInput{Query: q, Limit: queryInt(r, "limit")})
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ProjectListItem — items[] ответа /api/projects: id/корень плюс свежесть
// индекса (spec §6, история 48: «/api/projects перечисляет все со
// свежестью индекса»). Поля зеркалят app.StatusItem (internal/app/
// indexstatus.go) — то подмножество, что имеет смысл вне контекста одного
// активного проекта; RebuildInProgress/ETA/diagnostics там же несут смысл,
// которого здесь нет (reindex этого процесса графweb не запускает).
type ProjectListItem struct {
	Project          domain.ProjectID  `json:"project"`
	Root             string            `json:"root"`
	Generation       domain.Generation `json:"generation"`
	NeedsFullRebuild bool              `json:"needsFullRebuild"`
	ValidatedAt      time.Time         `json:"validatedAt,omitempty"`
	AgeSeconds       float64           `json:"ageSeconds,omitempty"`
}

// handleProjects агрегирует Response[StatusItem] каждого проекта в один
// список — единственный маршрут, отвечающий сразу за все открытые проекты,
// поэтому единственный, где внешний конверт (Generation/Stale) не несёт
// осмысленного значения одного проекта: агрегат из N эпох не сводится к
// одному числу. Упрощение, названо явно: внешний Generation/Stale здесь
// всегда нулевые, реальные данные — в каждом items[i].
func (h *Handler) handleProjects(w http.ResponseWriter, r *http.Request) {
	items := make([]ProjectListItem, 0, len(h.projects))
	var warnings []app.Warning
	for i := range h.projects {
		ph := &h.projects[i]
		unlock := h.lock(ph)
		p, err := ph.Open(r.Context())
		if err != nil {
			unlock()
			warnings = append(warnings, app.Warning{
				Code:    "project_unavailable",
				Message: fmt.Sprintf("проект %s (%s) недоступен: %v", ph.ID, ph.Root, err),
			})
			continue
		}
		resp, err := app.NewIndexStatusService(p).Status(r.Context(), app.StatusInput{})
		p.Close()
		unlock()
		if err != nil {
			warnings = append(warnings, app.Warning{
				Code:    "project_unavailable",
				Message: fmt.Sprintf("проект %s (%s): %v", ph.ID, ph.Root, err),
			})
			continue
		}
		if len(resp.Items) == 0 {
			continue
		}
		st := resp.Items[0]
		items = append(items, ProjectListItem{
			Project: ph.ID, Root: ph.Root, Generation: st.Generation,
			NeedsFullRebuild: st.NeedsFullRebuild, ValidatedAt: st.ValidatedAt, AgeSeconds: st.AgeSeconds,
		})
	}
	writeJSON(w, http.StatusOK, app.Response[ProjectListItem]{Items: items, TotalCount: len(items), Warnings: warnings})
}
