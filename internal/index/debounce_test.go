package index

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestDebouncerCollapsesBurstIntoOneRun — §18.5: массовая перевыгрузка
// (пачка trigger-ов подряд) обязана дать РОВНО ОДИН запуск fn, после тишины
// от ПОСЛЕДНЕГО trigger, а не один запуск на каждый trigger («не публикуется
// по кускам»).
func TestDebouncerCollapsesBurstIntoOneRun(t *testing.T) {
	var runs int32
	done := make(chan struct{})
	d := newDebouncer(30*time.Millisecond, func() {
		atomic.AddInt32(&runs, 1)
		close(done)
	})

	// Пачка из 5 trigger-ов с интервалом меньше quiet: каждый должен
	// отодвигать срабатывание, а не порождать свой запуск.
	for i := 0; i < 5; i++ {
		d.trigger()
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("debouncer не сработал")
	}
	// Небольшая пауза, чтобы поймать случайный повторный запуск, если
	// таймер был запущен более одного раза.
	time.Sleep(60 * time.Millisecond)

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Errorf("runs = %d, want 1 — пачка изменений опубликовалась кусками", got)
	}
}

// TestDebouncerStopBeforeFire — stop() до срабатывания отменяет запуск.
func TestDebouncerStopBeforeFire(t *testing.T) {
	var runs int32
	d := newDebouncer(30*time.Millisecond, func() { atomic.AddInt32(&runs, 1) })
	d.trigger()
	d.stop()
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Errorf("runs = %d, want 0 — stop() обязан отменить ещё не начавшийся запуск", got)
	}
	d.wait() // не должен блокироваться: armed снят в stop()
}
