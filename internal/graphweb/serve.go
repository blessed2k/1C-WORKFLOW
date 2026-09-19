package graphweb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Serve запускает HTTP-сервер поверх уже открытого ln (только 127.0.0.1 —
// это гарантирует вызывающий, cmd/mcp1c/graph.go, привязкой самого ln) и
// блокируется, пока не произойдёт одно из двух: сервер упал сам (например,
// ln закрыт извне) или ctx отменён (SIGINT/SIGTERM — дело вызывающего). На
// отмену ctx даётся graceful shutdown с shutdownTimeout на завершение уже
// идущих запросов; Serve возвращается, только когда http.Server.Serve
// реально завершилась — так вызывающий не считает процесс погашенным, пока
// горутина сервера ещё жива (висящих goroutine не
// остаётся).
func Serve(ctx context.Context, ln net.Listener, handler http.Handler, shutdownTimeout time.Duration) error {
	srv := &http.Server{Handler: handler}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	<-errCh // дождаться реального возврата srv.Serve — гарантия «без висящих goroutine»
	return shutdownErr
}
