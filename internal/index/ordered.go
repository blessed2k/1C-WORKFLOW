package index

import "sync"

// orderedWindow: сколько готовых планов может опережать писателя. Окно
// ограничивает память: планы строятся быстрее, чем пишутся, и без окна пул
// успел бы построить планы всего корпуса раньше, чем писатель применит
// первый.
const orderedWindow = 256

// runOrdered строит plan(i) для i в [0, n) параллельно в workers горутинах и
// отдаёт результаты apply строго по порядку i, в горутине вызывающего:
// писатель один (ADR-014), и порядок файлов в транзакции прежний. Ошибка
// плана или apply останавливает выдачу новых заданий; уже начатые планы
// дорабатывают и отбрасываются. Паника в плане не роняет процесс из чужой
// горутины: она пересылается и повторяется у вызывающего, где её видит
// recover писателя (store.runWriteTx). При любом выходе, включая панику в
// apply, раздатчик и воркеры останавливаются до возврата.
func runOrdered[T any](workers, n int, plan func(i int) (T, error), apply func(i int, v T) error) error {
	if n == 0 {
		return nil
	}
	if workers <= 0 {
		workers = 1
	}
	type result struct {
		v     T
		err   error
		panic any
	}
	slots := make([]chan result, n)
	for i := range slots {
		slots[i] = make(chan result, 1)
	}
	jobs := make(chan int)
	window := make(chan struct{}, orderedWindow)
	done := make(chan struct{})

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				slots[i] <- func() (r result) {
					defer func() {
						if p := recover(); p != nil {
							r = result{panic: p}
						}
					}()
					v, err := plan(i)
					return result{v: v, err: err}
				}()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobs)
		for i := 0; i < n; i++ {
			select {
			case window <- struct{}{}:
			case <-done:
				return
			}
			select {
			case jobs <- i:
			case <-done:
				return
			}
		}
	}()
	// Остановка раздатчика и воркеров идёт через defer: паника в apply
	// (писатель, горутина вызывающего) иначе миновала бы её, и раздатчик с
	// воркерами повисли бы навсегда.
	defer func() {
		close(done)
		wg.Wait()
	}()

	for i := 0; i < n; i++ {
		r := <-slots[i]
		<-window
		if r.panic != nil {
			panic(r.panic)
		}
		if r.err != nil {
			return r.err
		}
		if err := apply(i, r.v); err != nil {
			return err
		}
	}
	return nil
}
