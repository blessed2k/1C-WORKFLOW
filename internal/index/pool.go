package index

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// parseTask — один файл, ожидающий разбора: relPath для identity, absPath —
// откуда читать байты.
type parseTask struct {
	relPath string
	absPath string
}

// parseResult — исход разбора одного файла (запись + сырые байты для blob)
// или ошибка чтения с диска.
type parseResult struct {
	rec *fileRecord
	raw []byte
	err error
}

// runParsePool разбирает tasks параллельно через bounded worker pool (§17
// п.3: по числу ядер, лимит через workers). Отмена по ctx: воркеры проверяют
// ctx.Err() перед каждым файлом и не начинают новых чтений после отмены —
// уже стартовавшие дочитываются, но новых не открывается. Возвращает то, что
// успело разобраться, и саму ошибку отмены (вызывающий решает, что делать:
// частичный результат отменённого прохода в публикацию не идёт).
func runParsePool(ctx context.Context, workers int, tasks []parseTask) ([]parseResult, error) {
	if workers <= 0 {
		workers = 1
	}
	if len(tasks) == 0 {
		return nil, nil
	}

	in := make(chan parseTask)
	out := make(chan parseResult, len(tasks))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range in {
				if ctx.Err() != nil {
					out <- parseResult{err: ctx.Err()}
					continue
				}
				data, err := os.ReadFile(t.absPath)
				if err != nil {
					out <- parseResult{err: fmt.Errorf("чтение %s: %w", t.relPath, err)}
					continue
				}
				out <- parseResult{rec: parseOneFile(t.relPath, data), raw: data}
			}
		}()
	}

	go func() {
		defer close(in)
		for _, t := range tasks {
			select {
			case in <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(out)
	}()

	results := make([]parseResult, 0, len(tasks))
	var firstErr error
	for r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		results = append(results, r)
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return results, firstErr
}
