package index

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestReadsDuringRebuildSeeOldEpoch: во время полной пересборки
// сервер продолжает отвечать из last known good. testMidRunHook блокирует
// пайплайн ПОСРЕДИ ещё не закоммиченной транзакции store.Rebuild — ровно
// тот момент, когда store физически не может быть смешан со старым
// поколением (18.1: указатель переключается только после validate +
// checkpoint + close). Параллельный Read обязан видеть старое поколение.
func TestReadsDuringRebuildSeeOldEpoch(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) #1: %v", err)
	}
	genBefore, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	release := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	testMidRunHook = func() {
		once.Do(func() { close(reached) })
		<-release
	}
	t.Cleanup(func() { testMidRunHook = nil })

	rebuildDone := make(chan error, 1)
	go func() {
		_, err := svc.Reindex(ctx, ModeFull, "")
		rebuildDone <- err
	}()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("testMidRunHook не сработал за 5с")
	}

	// Пока rebuild не опубликован, чтение обязано видеть старое поколение.
	genDuring, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status во время rebuild: %v", err)
	}
	if genDuring.GenerationNumber != genBefore.GenerationNumber || genDuring.Epoch != genBefore.Epoch {
		t.Errorf("во время rebuild статус изменился: было %+v, стало %+v", genBefore, genDuring)
	}
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		_, ok, err := tx.SourceFileID("cfg", "CommonModules/УтилитыОбщие/Ext/Module.bsl")
		if err != nil {
			return err
		}
		if !ok {
			t.Errorf("файл, проиндексированный ДО rebuild, не виден во время rebuild")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read во время rebuild: %v", err)
	}

	close(release)
	if err := <-rebuildDone; err != nil {
		t.Fatalf("Reindex(full) #2: %v", err)
	}

	genAfter, err := st.Status(ctx)
	if err != nil {
		t.Fatalf("Status после rebuild: %v", err)
	}
	if genAfter.Epoch == genBefore.Epoch {
		t.Errorf("после rebuild эпоха не сменилась: было %d, стало %d", genBefore.Epoch, genAfter.Epoch)
	}
}
