package index

import (
	"sync"
	"time"
)

// debouncer откладывает fn до тишины quiet после последнего trigger (§18.5:
// debounce против неатомарного DumpConfigToFiles). Повторный trigger до
// срабатывания перезапускает таймер — массовая перевыгрузка публикуется
// целиком, а не кусками. wg считает РОВНО ОДИН отложенный запуск на «пачку»
// trigger-ов (armed переходит false->true один раз за пачку), поэтому
// wait() из Close дожидается фактического запуска, а не количества вызовов
// trigger.
type debouncer struct {
	mu    sync.Mutex
	quiet time.Duration
	timer *time.Timer
	armed bool
	fn    func()
	wg    sync.WaitGroup
}

func newDebouncer(quiet time.Duration, fn func()) *debouncer {
	return &debouncer{quiet: quiet, fn: fn}
}

// trigger (пере)запускает таймер тишины.
func (d *debouncer) trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.armed {
		d.armed = true
		d.wg.Add(1)
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.quiet, d.fire)
}

func (d *debouncer) fire() {
	d.mu.Lock()
	d.armed = false
	d.mu.Unlock()
	defer d.wg.Done()
	d.fn()
}

// stop отменяет отложенный запуск, если он ещё не начался.
func (d *debouncer) stop() {
	d.mu.Lock()
	if d.timer != nil {
		if d.timer.Stop() && d.armed {
			d.armed = false
			d.mu.Unlock()
			d.wg.Done()
			return
		}
	}
	d.mu.Unlock()
}

// wait дожидается уже начавшегося запуска fn (если он есть).
func (d *debouncer) wait() { d.wg.Wait() }
