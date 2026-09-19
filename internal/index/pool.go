package index

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// parseTask — один файл, ожидающий разбора: relPath для identity, absPath —
// откуда читать байты.
type parseTask struct {
	relPath string
	absPath string
}

// parseResult: исход разбора одного файла (запись + образ для blob, уже
// захэшированный и сжатый) или ошибка чтения с диска. Сырых байтов здесь
// нет: XML-файл после разбора больше никому не нужен, и держать его до
// публикации значило бы держать в памяти весь корпус (issue #3, шаг 1).
type parseResult struct {
	rec  *fileRecord
	blob store.PreparedBlob
	err  error
}

// runParsePool разбирает tasks параллельно через bounded worker pool (§17
// п.3: по числу ядер, лимит через workers). Отмена по ctx: воркеры проверяют
// ctx.Err() перед каждым файлом и не начинают новых чтений после отмены —
// уже стартовавшие дочитываются, но новых не открывается. Возвращает то, что
// успело разобраться, и саму ошибку отмены (вызывающий решает, что делать:
// частичный результат отменённого прохода в публикацию не идёт).
//
// blobSink, если задан, получает образ каждого разобранного файла прямо по
// мере готовности, в горутине вызывающего (писатель в это время свободен), и
// в результат образ уже не попадает: сжатые байты не копятся до конца
// разбора всего корпуса (issue #3, шаг 1). Ошибка приёмника становится
// ошибкой прохода, следующие образы ему уже не отдаются.
func runParsePool(ctx context.Context, workers int, tasks []parseTask, blobSink func(store.PreparedBlob) error) ([]parseResult, error) {
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
				rec, blob, err := prepareFile(t.relPath, data)
				if err != nil {
					out <- parseResult{err: fmt.Errorf("образ %s: %w", t.relPath, err)}
					continue
				}
				out <- parseResult{rec: rec, blob: blob}
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
		if blobSink != nil {
			if firstErr == nil {
				if err := blobSink(r.blob); err != nil {
					firstErr = fmt.Errorf("образ %s: %w", r.rec.relPath, err)
				}
			}
			r.blob = store.PreparedBlob{}
		}
		results = append(results, r)
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return results, firstErr
}
