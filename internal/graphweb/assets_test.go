package graphweb_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
)

// wantCytoscapeSHA256 — контрольная сумма cytoscape.js 3.34.1 (MIT), как
// записана в internal/graphweb/assets/vendor/cytoscape-LICENSE.txt. Значение
// продублировано здесь намеренно: ожидаемое значение теста не должно
// приходить из кода под тестом (см. контракт таска), а из независимо
// зафиксированного факта — SHA-256 источника, который проверял оркестратор
// при загрузке файла.
const wantCytoscapeSHA256 = "5141892eb19898946e5af8300e14cec15a63a22186a4ca56d76819a91e2a3fe6"

// TestSPARootServesHTML: GET / отдаёт HTML-страницу SPA с верным
// Content-Type. Сервер поднят БЕЗ единого проекта — маршрут статики не
// должен зависеть от того, есть ли открытые проекты (критерий «сервер не
// падает при отсутствии данных»).
func TestSPARootServesHTML(t *testing.T) {
	h := graphweb.NewHandler(nil)
	rr := doRawGET(t, h, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html prefix", ct)
	}
	if !strings.Contains(rr.Body.String(), "<html") {
		t.Fatalf("тело ответа не похоже на HTML: %.200s", rr.Body.String())
	}
}

// TestAppJSServed — /app.js отдаётся с JS Content-Type, тоже без открытых
// проектов.
func TestAppJSServed(t *testing.T) {
	h := graphweb.NewHandler(nil)
	rr := doRawGET(t, h, "/app.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, want javascript", ct)
	}
	if rr.Body.Len() == 0 {
		t.Fatalf("app.js пуст")
	}
}

// TestVendoredCytoscapeIsRealLibrary: критерий приёмки
// «cytoscape.min.js версии 3.34.1 лежит vendored-файлом... версия
// и происхождение записаны». Три независимых проверки, что go:embed
// действительно содержит НАСТОЯЩУЮ библиотеку нужной версии, а не заглушку:
// контрольная сумма, версия и правообладатель в шапке файла, и сам маршрут
// отдаёт её с JS Content-Type.
func TestVendoredCytoscapeIsRealLibrary(t *testing.T) {
	h := graphweb.NewHandler(nil)
	rr := doRawGET(t, h, "/assets/cytoscape.min.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %.200s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, want javascript", ct)
	}
	body := rr.Body.Bytes()
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if got != wantCytoscapeSHA256 {
		t.Fatalf("sha256 = %s, want %s (вендоренный файл подменён или повреждён)", got, wantCytoscapeSHA256)
	}
	head := string(body[:minInt(400, len(body))])
	if !strings.Contains(head, "Cytoscape Consortium") {
		t.Fatalf("в шапке файла нет упоминания правообладателя MIT-лицензии: %.200s", head)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// doRawGET — как doGET в handler_test.go, но без разбора тела как envelope:
// маршруты статики не несут app.Response[T].
func doRawGET(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestAssetsViewSwitchAndLegend: веха В3 (issue #5). Интерфейс несёт
// переключатель raw/effective/diff, легенда объясняет стиль рёбер
// расширения, а app.js действительно шлёт view в API и различает рёбра
// расширения классами, на которые опирается легенда.
func TestAssetsViewSwitchAndLegend(t *testing.T) {
	h := graphweb.NewHandler(nil)
	html := doRawGET(t, h, "/").Body.String()
	for _, want := range []string{
		`data-view="raw"`, `data-view="effective"`, `data-view="diff"`,
		`id="legendLayers"`, `swatch ext-added`, `swatch ext-same`, `var(--ext)`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html без %s", want)
		}
	}
	js := doRawGET(t, h, "/app.js").Body.String()
	for _, want := range []string{
		`view: state.view`, `selector: "edge.ext"`, `selector: "edge.added"`,
		`"ext" : ""`, `"added" : ""`, `extensionEdges`,
		// поколение карты: ответы, запрошенные до смены режима, отбрасываются
		`state.gen++`, `if (gen !== state.gen) return;`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js без %s", want)
		}
	}
}
