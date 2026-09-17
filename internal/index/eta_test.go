package index

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestStatusETADuringRebuild — тело таска требует «фазу, счётчики, ETA,
// диагностики» (§17). ETA — грубая оценка от длительности прошлого full:
// после первого full известен ориентир, во время следующего (искусственно
// удлинённого через testMidRunHook) фоновой пересборки ETA обязан быть
// положительным и не длиннее прошлой длительности.
func TestStatusETADuringRebuild(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)
	svc := NewService(st, "proj", testManifest(t, root), nil, Config{DebounceQuiet: time.Millisecond})
	t.Cleanup(func() { svc.Close() })

	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) #1: %v", err)
	}
	stBefore, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if stBefore.LastFullDuration <= 0 {
		t.Fatalf("LastFullDuration = %v, want > 0 после первого full", stBefore.LastFullDuration)
	}

	release := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	testMidRunHook = func() {
		once.Do(func() { close(reached) })
		<-release
	}
	t.Cleanup(func() { testMidRunHook = nil })

	// scheduleBackgroundFull — тот же путь, что EnsureFresh на массовых
	// изменениях (freshness.go): именно он ставит rebuilding=true и
	// rebuildStartedAt, от которых считается ETA.
	svc.scheduleBackgroundFull("test")

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("testMidRunHook не сработал за 5с")
	}

	stDuring, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status во время rebuild: %v", err)
	}
	if !stDuring.RebuildInProgress {
		t.Fatal("RebuildInProgress = false во время активной пересборки")
	}
	if stDuring.ETA <= 0 {
		t.Errorf("ETA = %v, want > 0 во время rebuild с известным LastFullDuration", stDuring.ETA)
	}
	if stDuring.ETA > stBefore.LastFullDuration {
		t.Errorf("ETA = %v больше прошлой длительности %v", stDuring.ETA, stBefore.LastFullDuration)
	}

	close(release)
	svc.deb.wait() // дожидается конца фоновой пересборки, запущенной scheduleBackgroundFull — ДО снятия хука (иначе гонка с ним, как в require-fresh-timeout)
	testMidRunHook = nil

	stAfter, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status после rebuild: %v", err)
	}
	if stAfter.ETA != 0 {
		t.Errorf("ETA после завершения rebuild = %v, want 0", stAfter.ETA)
	}
}
