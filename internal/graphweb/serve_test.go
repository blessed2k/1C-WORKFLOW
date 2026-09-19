package graphweb_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
)

// TestServeStopsOnContextCancelWithoutHangingGoroutine: Ctrl+C
// (в проде — отменённый ctx) обязан погасить сервер, и Serve обязан
// вернуться только ПОСЛЕ того, как http.Server.Serve реально завершилась —
// иначе вызывающий (cmd/mcp1c/graph.go) считает процесс погашенным, пока
// горутина сервера ещё жива. Слушатель реальный (net.Listen на 127.0.0.1:0):
// это тест жизненного цикла HTTP, а не тест маршрутов, поэтому пустой
// Handler (nil-срез проектов) — достаточная фикстура.
func TestServeStopsOnContextCancelWithoutHangingGoroutine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	handler := graphweb.NewHandler(nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- graphweb.Serve(ctx, ln, handler, time.Second) }()

	// Сервер действительно принимает соединения, прежде чем его гасить —
	// иначе тест мог бы «пройти» и на сервере, который никогда толком не
	// поднимался.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, getErr := http.Get("http://" + ln.Addr().String() + "/api/projects")
		if getErr == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("сервер не ответил до отмены контекста: %v", getErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve вернул ошибку после отмены ctx: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve не вернулась за 3с после отмены ctx — горутина сервера повисла")
	}

	// Порт действительно освобождён: новый Listen на тот же адрес обязан
	// пройти немедленно, будь это не так, срез Serve оставил бы висящий
	// listener/accept-цикл.
	ln2, err := net.Listen("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("порт не освободился после Serve: %v", err)
	}
	ln2.Close()
}
