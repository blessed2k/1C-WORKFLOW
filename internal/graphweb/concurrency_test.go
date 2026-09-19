package graphweb_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
)

// TestHandlerSerializesProjectOpens: каждый запрос открывает индекс заново
// (doc.go), и на реальной выгрузке десяток одновременных открытий одного
// проекта ловил «database is locked (261)» на соединении писателя: карта
// после раскрытия нескольких узлов просит карточки и соседей пачкой.
// Открытия одного проекта обязаны идти по одному, а все ответы быть 200.
func TestHandlerSerializesProjectOpens(t *testing.T) {
	tp := newTestProject(t, "graphweb-serial")
	o1, o2, o3, _ := seedTwoNodesWithEdge(t, tp)

	var inside, maxInside int32
	h := tp.handle
	open := h.Open
	h.Open = func(ctx context.Context) (*app.Projects, error) {
		n := atomic.AddInt32(&inside, 1)
		for {
			m := atomic.LoadInt32(&maxInside)
			if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inside, -1)
		return open(ctx)
	}
	handler := graphweb.NewHandler([]graphweb.ProjectHandle{h})

	paths := []string{"/api/search?q=o", "/api/godnodes", "/api/projects"}
	for i := 0; i < 5; i++ {
		for _, id := range []int64{o1, o2, o3} {
			paths = append(paths, fmt.Sprintf("/api/node/%d", id), fmt.Sprintf("/api/neighbors/%d", id))
		}
	}
	var wg sync.WaitGroup
	errs := make(chan string, len(paths))
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
			if rr.Code != http.StatusOK {
				errs <- fmt.Sprintf("%s: %d %s", p, rr.Code, rr.Body.String())
			}
		}(p)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if maxInside > 1 {
		t.Errorf("одновременных открытий проекта: %d, ожидалось не больше одного", maxInside)
	}
}
