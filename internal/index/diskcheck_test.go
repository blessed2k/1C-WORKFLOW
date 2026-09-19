package index

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Тесты этого файла идут мимо шва internal/app сознательно: число обходов
// диска и поведение Close посреди обхода снаружи сервиса не наблюдаемы, их
// видно только через подмену самого обхода (поле walk).

// blockingWalk подменяет обход диска: считает вызовы, сообщает о входе и
// ждёт release либо отмены контекста.
type blockingWalk struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func newBlockingWalk() *blockingWalk {
	return &blockingWalk{entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (b *blockingWalk) walk(ctx context.Context) (precheckWork, error) {
	b.calls.Add(1)
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return precheckWork{}, nil
	case <-ctx.Done():
		return precheckWork{}, ctx.Err()
	}
}

func newDiskCheckService(t *testing.T) *Service {
	t.Helper()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	// Без полной сборки на эпохе стоит признак пересборки, и до обхода
	// диска CachedFreshness не доходит. Исход прогона сбрасывается: кэш
	// холодный, как у свежего процесса.
	if _, err := svc.Reindex(context.Background(), ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	svc.invalidateDiskCheck()
	return svc
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("за 5 с не дождались: %s", what)
	}
}

// TestCachedFreshnessSingleflight: N одновременных вызовов при холодном
// кэше дают ровно один обход диска, остальные ждут его исход.
func TestCachedFreshnessSingleflight(t *testing.T) {
	svc := newDiskCheckService(t)
	t.Cleanup(func() { svc.Close() })
	bw := newBlockingWalk()
	svc.walk = bw.walk

	const n = 8
	var started, done sync.WaitGroup
	errs := make(chan error, n)
	started.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			started.Done()
			f, err := svc.CachedFreshness(context.Background())
			if err == nil && !f.Fresh {
				err = errors.New("исход обхода без изменений, а ответ не свежий")
			}
			errs <- err
		}()
	}
	started.Wait()
	waitSignal(t, bw.entered, "вход в обход")
	close(bw.release)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("CachedFreshness: %v", err)
		}
	}
	if got := bw.calls.Load(); got != 1 {
		t.Fatalf("обходов диска %d, want ровно 1", got)
	}
}

// TestCloseInterruptsBackgroundWalk: Close посреди фонового обхода не
// висит, обход отменяется, после Close синхронный путь отвечает ошибкой, а
// не обходит закрытый сервис.
func TestCloseInterruptsBackgroundWalk(t *testing.T) {
	svc := newDiskCheckService(t)
	bw := newBlockingWalk()
	svc.walk = bw.walk

	svc.WarmFreshness()
	waitSignal(t, bw.entered, "вход фонового обхода")

	closed := make(chan struct{})
	go func() {
		svc.Close()
		close(closed)
	}()
	waitSignal(t, closed, "возврат Close посреди обхода")

	if _, err := svc.CachedFreshness(context.Background()); !errors.Is(err, ErrServiceClosed) {
		t.Fatalf("CachedFreshness после Close: err=%v, want ErrServiceClosed", err)
	}
	if got := bw.calls.Load(); got != 1 {
		t.Fatalf("обходов %d, want 1: после Close новых быть не должно", got)
	}
}

// TestCloseInterruptsWaitForOpMu: фоновый обход, который ещё ждёт opMu
// (идёт reindex), Close не задерживает.
func TestCloseInterruptsWaitForOpMu(t *testing.T) {
	svc := newDiskCheckService(t)
	bw := newBlockingWalk()
	svc.walk = bw.walk

	svc.opMu.Lock()
	svc.WarmFreshness()

	closed := make(chan struct{})
	go func() {
		svc.Close()
		close(closed)
	}()
	waitSignal(t, closed, "возврат Close, пока обход ждёт opMu")
	svc.opMu.Unlock()

	if got := bw.calls.Load(); got != 0 {
		t.Fatalf("обходов %d, want 0: закрытый сервис не обходит диск", got)
	}
}
