package graphweb_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
)

// decodeEnvelope разбирает тело ответа как generic-конверт app.Response[T]
// без называния типа T (graphweb_test не должен знать о внутренних DTO
// internal/app — это ровно то, что делает эту проверку тестом транспорта, а
// не тестом графа): достаточно total/warnings/items как json.RawMessage.
type envelope struct {
	Generation string            `json:"generation"`
	Stale      bool              `json:"stale"`
	Warnings   []json.RawMessage `json:"warnings"`
	Items      []json.RawMessage `json:"items"`
	TotalCount int               `json:"totalCount"`
	NextCursor string            `json:"nextCursor"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func doGET(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var env envelope
	if rr.Code < 300 {
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode body %s: %v\nbody: %s", path, err, rr.Body.String())
		}
	}
	return rr, env
}

func doGETErr(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, apiError) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var aerr apiError
	if err := json.Unmarshal(rr.Body.Bytes(), &aerr); err != nil {
		t.Fatalf("decode error body %s: %v\nbody: %s", path, err, rr.Body.String())
	}
	return rr, aerr
}

// TestHandlerNodeFoundAndNotFound: маршрут /api/node/{id} отвечает 200 с
// карточкой узла, когда объект есть, и 404 actionable-ошибкой, когда его
// нет — это транспортная сериализация NotFound из app.ObjectGraphService.Node
// в HTTP-статус, а не поведение самого графа (то уже проверено таском 08).
func TestHandlerNodeFoundAndNotFound(t *testing.T) {
	tp := newTestProject(t, "graphweb-node")
	o1, _, _, _ := seedTwoNodesWithEdge(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rr, env := doGET(t, h, fmt.Sprintf("/api/node/%d", o1))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(env.Items) != 1 || env.TotalCount != 1 {
		t.Fatalf("ожидался ровно один узел: %+v", env)
	}

	rrMiss, aerr := doGETErr(t, h, "/api/node/999999")
	if rrMiss.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rrMiss.Code, rrMiss.Body.String())
	}
	if aerr.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", aerr.Code)
	}
}

// TestHandlerNeighborsEmptyVsNotFound — критическое различение из тикета
// (R22.1/§43): объект БЕЗ соседей отвечает 200 с пустым items и total=0
// (это не ошибка), а объект, которого вообще нет, отвечает 404. Спутать их
// значило бы, что SPA не может отличить «граф пуст» от «опечатка в id».
func TestHandlerNeighborsEmptyVsNotFound(t *testing.T) {
	tp := newTestProject(t, "graphweb-neighbors")
	_, _, o3, _ := seedTwoNodesWithEdge(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rrEmpty, env := doGET(t, h, fmt.Sprintf("/api/neighbors/%d", o3))
	if rrEmpty.Code != http.StatusOK {
		t.Fatalf("объект без соседей: status = %d, want 200; body = %s", rrEmpty.Code, rrEmpty.Body.String())
	}
	if env.TotalCount != 0 || len(env.Items) != 0 {
		t.Fatalf("объект без соседей: ожидался пустой список, получили %+v", env)
	}

	rrMissing, aerr := doGETErr(t, h, "/api/neighbors/999999")
	if rrMissing.Code != http.StatusNotFound {
		t.Fatalf("несуществующий объект: status = %d, want 404; body = %s", rrMissing.Code, rrMissing.Body.String())
	}
	if aerr.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", aerr.Code)
	}
}

// TestHandlerNeighborsReturnsEdge — маршрутизация запроса в
// ObjectGraphService.Neighbors действительно доходит с корректными query-
// параметрами: узел с одним ребром отвечает totalCount=1.
func TestHandlerNeighborsReturnsEdge(t *testing.T) {
	tp := newTestProject(t, "graphweb-neighbors-hit")
	o1, _, _, _ := seedTwoNodesWithEdge(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rr, env := doGET(t, h, fmt.Sprintf("/api/neighbors/%d?dir=out", o1))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if env.TotalCount != 1 || len(env.Items) != 1 {
		t.Fatalf("ожидалось одно ребро, получили %+v", env)
	}
}

// edgeID разбирает id одного элемента конверта (EdgeItem/RadiusEdgeItem
// сериализуются с полем "id").
func edgeID(t *testing.T, raw json.RawMessage) int64 {
	t.Helper()
	var v struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode edge id: %v", err)
	}
	return v.ID
}

// TestHandlerNeighborsCursorPagesThroughDistinctResults — cursor из query
// действительно доходит до NeighborsInput.Cursor и обратно (ревью качества:
// проброс не был проверен ни одним тестом, мутация «обнулить cursor в обоих
// местах» оставляла весь пакет зелёным). Три ребра, limit=1: первая страница
// отдаёт nextCursor и одно ребро, вторая (с этим cursor= в query) отдаёт
// ДРУГОЕ ребро — если бы cursor терялся, вторая страница повторила бы первую.
func TestHandlerNeighborsCursorPagesThroughDistinctResults(t *testing.T) {
	tp := newTestProject(t, "graphweb-neighbors-cursor")
	root, edges := seedNodeWithManyEdges(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rr1, env1 := doGET(t, h, fmt.Sprintf("/api/neighbors/%d?dir=out&limit=1", root))
	if rr1.Code != http.StatusOK {
		t.Fatalf("страница 1: status = %d, body = %s", rr1.Code, rr1.Body.String())
	}
	if len(env1.Items) != 1 || env1.TotalCount != len(edges) {
		t.Fatalf("страница 1: ожидался один элемент из %d, получили %+v", len(edges), env1)
	}
	if env1.NextCursor == "" {
		t.Fatalf("страница 1: nextCursor пуст, хотя рёбер %d, limit=1", len(edges))
	}
	firstID := edgeID(t, env1.Items[0])

	path2 := fmt.Sprintf("/api/neighbors/%d?dir=out&limit=1&cursor=%s", root, url.QueryEscape(env1.NextCursor))
	rr2, env2 := doGET(t, h, path2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("страница 2: status = %d, body = %s", rr2.Code, rr2.Body.String())
	}
	if len(env2.Items) != 1 {
		t.Fatalf("страница 2: ожидался один элемент, получили %+v", env2)
	}
	secondID := edgeID(t, env2.Items[0])

	if secondID == firstID {
		t.Fatalf("страница 2 повторила страницу 1 (id=%d) — cursor не пробрасывается в NeighborsInput.Cursor", firstID)
	}
	for _, want := range []int64{firstID, secondID} {
		found := false
		for _, e := range edges {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("id %d не из засеянных рёбер %v", want, edges)
		}
	}
}

// TestHandlerRadiusCursorPagesThroughDistinctResults — тот же проброс,
// маршрут /api/radius: RadiusInput.Cursor кодирует индекс уже посчитанного
// (и обрезанного потолком) списка рёбер, а не id, но контракт для клиента
// тот же — вторая страница обязана отличаться от первой.
func TestHandlerRadiusCursorPagesThroughDistinctResults(t *testing.T) {
	tp := newTestProject(t, "graphweb-radius-cursor")
	root, edges := seedNodeWithManyEdges(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rr1, env1 := doGET(t, h, fmt.Sprintf("/api/radius/%d?dir=out&depth=1&limit=1", root))
	if rr1.Code != http.StatusOK {
		t.Fatalf("страница 1: status = %d, body = %s", rr1.Code, rr1.Body.String())
	}
	if len(env1.Items) != 1 || env1.TotalCount != len(edges) {
		t.Fatalf("страница 1: ожидался один элемент из %d, получили %+v", len(edges), env1)
	}
	if env1.NextCursor == "" {
		t.Fatalf("страница 1: nextCursor пуст, хотя рёбер %d, limit=1", len(edges))
	}
	firstID := edgeID(t, env1.Items[0])

	path2 := fmt.Sprintf("/api/radius/%d?dir=out&depth=1&limit=1&cursor=%s", root, url.QueryEscape(env1.NextCursor))
	rr2, env2 := doGET(t, h, path2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("страница 2: status = %d, body = %s", rr2.Code, rr2.Body.String())
	}
	if len(env2.Items) != 1 {
		t.Fatalf("страница 2: ожидался один элемент, получили %+v", env2)
	}
	secondID := edgeID(t, env2.Items[0])

	if secondID == firstID {
		t.Fatalf("страница 2 повторила страницу 1 (id=%d) — cursor не пробрасывается в RadiusInput.Cursor", firstID)
	}
}

// TestHandlerRadiusTruncationWarningReachesHTTPResponse: признак обрезания
// радиуса (Warnings с кодом truncated), который ObjectGraphService.Radius
// уже вычисляет (таск 08), обязан дойти до HTTP-тела ответа неискажённым —
// именно это швом транспорта и рискует потеряться (сериализация неполного
// среза, забытое поле). RadiusNodesCap=1 гарантированно упирается в потолок
// на фикстуре с двумя рёбрами и тремя узлами.
func TestHandlerRadiusTruncationWarningReachesHTTPResponse(t *testing.T) {
	tp := newTestProject(t, "graphweb-radius-trunc")
	o1, _, _, _ := seedTwoNodesWithEdge(t, tp)
	ph := tp.withRadiusCap(1)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{ph})

	rr, env := doGET(t, h, fmt.Sprintf("/api/radius/%d?depth=2", o1))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(env.Warnings) == 0 {
		t.Fatalf("ожидалось предупреждение об обрезании радиуса, получили %+v", env)
	}
	var w struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(env.Warnings[0], &w); err != nil {
		t.Fatalf("decode warning: %v", err)
	}
	if w.Code != "truncated" {
		t.Fatalf("warning.code = %q, want truncated", w.Code)
	}
}

// TestHandlerGodNodesAndEdgeEvidence — оставшиеся два маршрута раздела 8.1:
// проверяется только то, что запрос доходит и сериализуется (200), детали
// ранжирования и evidence — зона таска 08.
func TestHandlerGodNodesAndEdgeEvidence(t *testing.T) {
	tp := newTestProject(t, "graphweb-godnodes")
	_, _, _, edgeID := seedTwoNodesWithEdge(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rrGod, envGod := doGET(t, h, "/api/godnodes?metric=fan-in&top=5")
	if rrGod.Code != http.StatusOK {
		t.Fatalf("godnodes: status = %d, body = %s", rrGod.Code, rrGod.Body.String())
	}
	if len(envGod.Items) == 0 {
		t.Fatalf("godnodes: ожидался хотя бы один узел, получили %+v", envGod)
	}

	rrEv, envEv := doGET(t, h, fmt.Sprintf("/api/edge/%d/evidence", edgeID))
	if rrEv.Code != http.StatusOK {
		t.Fatalf("evidence: status = %d, body = %s", rrEv.Code, rrEv.Body.String())
	}
	if len(envEv.Items) != 1 {
		t.Fatalf("evidence: ожидался один элемент, получили %+v", envEv)
	}

	rrEvMiss, aerr := doGETErr(t, h, "/api/edge/999999/evidence")
	if rrEvMiss.Code != http.StatusNotFound {
		t.Fatalf("evidence: status = %d, want 404", rrEvMiss.Code)
	}
	if aerr.Code != "not_found" {
		t.Fatalf("evidence: code = %q, want not_found", aerr.Code)
	}
}

// TestHandlerGodNodesUnknownMetric — неверное значение metric — ошибка ввода
// (400 с кодом invalid_argument), а не 500 internal: неизвестная ось доезжала
// до store обычной ошибкой и попадала в запасной путь respondErr.
func TestHandlerGodNodesUnknownMetric(t *testing.T) {
	tp := newTestProject(t, "graphweb-godnodes-metric")
	seedTwoNodesWithEdge(t, tp)
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rr, aerr := doGETErr(t, h, "/api/godnodes?top=5&metric=writers")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if aerr.Code != "invalid_argument" {
		t.Fatalf("code = %q, want invalid_argument", aerr.Code)
	}
	for _, want := range []string{"fan-in", "fan-out", "total"} {
		if !strings.Contains(aerr.Hint, want) {
			t.Fatalf("hint = %q, want перечисление допустимых осей (нет %q)", aerr.Hint, want)
		}
	}

	// Валидная ось и вовсе не переданная (умолчание total) — по-прежнему 200.
	for _, path := range []string{"/api/godnodes?top=5&metric=fan-in", "/api/godnodes?top=5"} {
		rrOK, envOK := doGET(t, h, path)
		if rrOK.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, body = %s", path, rrOK.Code, rrOK.Body.String())
		}
		if len(envOK.Items) == 0 {
			t.Fatalf("GET %s: ожидался хотя бы один узел, получили %+v", path, envOK)
		}
	}
}

// TestHandlerBadPathID: id, который не парсится как число, — 400, а не 500
// и не пропуск в ObjectGraphService (что дало бы неинформативную панику или
// objectId=0).
func TestHandlerBadPathID(t *testing.T) {
	tp := newTestProject(t, "graphweb-badid")
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tp.handle})

	rr, aerr := doGETErr(t, h, "/api/node/not-a-number")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if aerr.Code != "bad_request" {
		t.Fatalf("code = %q, want bad_request", aerr.Code)
	}
}

// TestHandlerProjectSelection — история 48: несколько --project.
// /api/projects перечисляет оба; без project= запрос отвечает ошибкой со
// списком (не тихим выбором первого); с project=<неизвестный> — тоже
// ошибкой; с валидным project= — маршрутизирует к правильному проекту, и
// данные разных проектов не смешиваются (у второго проекта другой узел).
func TestHandlerProjectSelection(t *testing.T) {
	tpA := newTestProject(t, "graphweb-multi-a")
	oA, _, _, _ := seedTwoNodesWithEdgeNamed(t, tpA, "-A")
	tpB := newTestProject(t, "graphweb-multi-b")
	_, _, _, _ = seedTwoNodesWithEdgeNamed(t, tpB, "-B")
	h := graphweb.NewHandler([]graphweb.ProjectHandle{tpA.handle, tpB.handle})

	rrList, envList := doGET(t, h, "/api/projects")
	if rrList.Code != http.StatusOK {
		t.Fatalf("projects: status = %d, body = %s", rrList.Code, rrList.Body.String())
	}
	if len(envList.Items) != 2 {
		t.Fatalf("projects: ожидалось 2 проекта, получили %+v", envList)
	}

	rrAmbiguous, aerr := doGETErr(t, h, fmt.Sprintf("/api/node/%d", oA))
	if rrAmbiguous.Code != http.StatusBadRequest {
		t.Fatalf("без project= при двух открытых: status = %d, want 400; body = %s", rrAmbiguous.Code, rrAmbiguous.Body.String())
	}
	if aerr.Code != "ambiguous_project" {
		t.Fatalf("code = %q, want ambiguous_project", aerr.Code)
	}
	for _, want := range []string{"graphweb-multi-a", "graphweb-multi-b"} {
		if !strings.Contains(aerr.Hint, want) {
			t.Errorf("hint %q не называет проект %q", aerr.Hint, want)
		}
	}

	rrUnknown, aerrUnknown := doGETErr(t, h, fmt.Sprintf("/api/node/%d?project=nope", oA))
	if rrUnknown.Code != http.StatusBadRequest || aerrUnknown.Code != "unknown_project" {
		t.Fatalf("неизвестный project=: status=%d code=%q", rrUnknown.Code, aerrUnknown.Code)
	}

	// project=A видит узел A с его собственным именем ("O1-A"): данные
	// проекта B не должны просочиться в ответ, адресованный project=A —
	// даже если store id этого узла в проекте B оказался бы тем же числом
	// (независимые автоинкременты двух разных SQLite-файлов, см.
	// seedTwoNodesWithEdgeNamed).
	rrA, envA := doGET(t, h, fmt.Sprintf("/api/node/%d?project=graphweb-multi-a", oA))
	if rrA.Code != http.StatusOK {
		t.Fatalf("project=A/node A: status = %d, body = %s", rrA.Code, rrA.Body.String())
	}
	var nodeA struct {
		NameDisplay string `json:"nameDisplay"`
	}
	if err := json.Unmarshal(envA.Items[0], &nodeA); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if nodeA.NameDisplay != "O1-A" {
		t.Fatalf("project=A отдал nameDisplay=%q, want O1-A (похоже на утечку из другого проекта)", nodeA.NameDisplay)
	}

	// Тот же самый id, но под project=B: либо объекта с таким id там нет
	// (404), либо он есть и это ДРУГОЙ узел (свой "-B", не "-A") — в любом
	// случае ответ project=B никогда не должен показать имя из project=A.
	rrCross, aerrCross := doGETErr(t, h, fmt.Sprintf("/api/node/%d?project=graphweb-multi-b", oA))
	switch rrCross.Code {
	case http.StatusNotFound:
		if aerrCross.Code != "not_found" {
			t.Fatalf("code = %q, want not_found", aerrCross.Code)
		}
	case http.StatusOK:
		var envCross envelope
		if err := json.Unmarshal(rrCross.Body.Bytes(), &envCross); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var nodeCross struct {
			NameDisplay string `json:"nameDisplay"`
		}
		if err := json.Unmarshal(envCross.Items[0], &nodeCross); err != nil {
			t.Fatalf("decode node: %v", err)
		}
		if nodeCross.NameDisplay == "O1-A" {
			t.Fatalf("project=B вернул nameDisplay=%q — данные проекта A утекли в ответ project=B", nodeCross.NameDisplay)
		}
	default:
		t.Fatalf("неожиданный статус %d под project=B", rrCross.Code)
	}
}
