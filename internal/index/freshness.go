package index

import (
	"context"
	"fmt"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// EnsureFresh реализует freshness policy §18.5: мало изменений — синхронный
// инкремент перед ответом; много — фоновая полная пересборка (через debounce,
// §18.5) + ответ из last known good с warning. allow-stale отдаёт ответ сразу
// с причиной устаревания; require-fresh ждёт в пределах deadline либо
// возвращает ErrIndexNotFresh с прогрессом — устаревшее под видом свежего не
// отдаётся никогда (R33.1).
func (s *Service) EnsureFresh(ctx context.Context, policy Policy) (Freshness, error) {
	deadline := policy.Deadline
	if deadline <= 0 {
		deadline = s.cfg.RequireFreshDeadline
	}
	requireFresh := policy.Mode == PolicyRequireFresh
	deadlineAt := s.now().Add(deadline)

	for {
		if s.isRebuilding() {
			if !requireFresh {
				return s.staleFreshness(ctx, ReasonRebuildInProgress), nil
			}
		} else if needsFull, err := s.needsFullRebuild(ctx); err != nil {
			return Freshness{}, err
		} else if needsFull {
			// Признак «структура новая, данных нет» сильнее любого счётчика
			// изменившихся файлов: совпадение size/mtime говорит лишь о том,
			// что выгрузка не менялась, — а в индексе после смены версии схемы
			// таблицы пусты и слой у всех фактов дефолтный. Исход тот же, что у
			// «много изменений», и по той же причине: снять признак может
			// только полная пересборка, инкремент его не снимает.
			s.scheduleBackgroundFull("индекс не наполнен после смены версии схемы")
			if !requireFresh {
				return s.staleFreshness(ctx, ReasonRebuildRequired), nil
			}
		} else {
			work, err := s.precheckWorkload(ctx)
			if err != nil {
				return Freshness{}, err
			}
			if work.changed == 0 {
				// Гидратированные записи изменением НЕ считаются (R14):
				// на диске ничего не трогали, пайплайн не запускается, и
				// дочитывать их незачем — ради этого случая гидратация и
				// сделана.
				return s.currentFreshness(ctx)
			}
			// Порог считает ОБЪЁМ предстоящей работы, а не число изменённых
			// на диске файлов (D01): запуск пайплайна дочитывает ещё и все
			// гидратированные записи, и после рестарта одна правка на диске
			// означала бы синхронный разбор всего проекта в ответе на один
			// вызов инструмента (на крупной конфигурации — минуты). Вызывающий такого ждать
			// не обязан: работа уходит в фон, ответ идёт из last known good.
			if work.pending <= s.cfg.SmallChangeFileLimit {
				if _, err := s.Reindex(ctx, ModeIncremental, ""); err != nil {
					return Freshness{}, err
				}
				return s.currentFreshness(ctx)
			}
			s.scheduleBackgroundFull(fmt.Sprintf(
				"изменённых файлов: %d, предстоит разобрать: %d", work.changed, work.pending))
			if !requireFresh {
				return s.staleFreshness(ctx, ReasonRebuildScheduled), nil
			}
		}

		if s.now().After(deadlineAt) {
			return Freshness{}, &ErrIndexNotFresh{Progress: s.progressDescription()}
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return Freshness{}, ctx.Err()
		}
	}
}

// needsFullRebuild — стоит ли на индексе признак полной пересборки, который
// хранилище выставляет структурной миграции схемы и снимает только
// завершённым store.Rebuild. Читается из Status, а не кэшируется: признак
// снимается в другом потоке (фоновая пересборка), и кэш здесь означал бы
// «требую reindex» ещё долго после того, как reindex прошёл.
func (s *Service) needsFullRebuild(ctx context.Context) (bool, error) {
	st, err := s.st.Status(ctx)
	if err != nil {
		return false, err
	}
	return st.NeedsFullRebuild, nil
}

func (s *Service) isRebuilding() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.rebuilding
}

func (s *Service) setRebuilding(v bool) {
	s.stateMu.Lock()
	s.rebuilding = v
	if v {
		s.rebuildStartedAt = s.now()
	} else {
		s.rebuildStartedAt = time.Time{}
	}
	s.stateMu.Unlock()
}

func (s *Service) progressDescription() string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.rebuilding {
		return "полная пересборка выполняется: " + s.lastRebuildReason
	}
	return "пересборка запланирована (debounce): " + s.lastRebuildReason
}

// scheduleBackgroundFull откладывает фоновую полную пересборку до тишины
// изменений (debounce, §18.5): повторные вызовы во время массовой
// перевыгрузки лишь отодвигают момент запуска, публикация не идёт кусками.
func (s *Service) scheduleBackgroundFull(reason string) {
	s.stateMu.Lock()
	s.lastRebuildReason = reason
	s.stateMu.Unlock()
	s.deb.trigger()
}

// runBackgroundFull — тело отложенного полного rebuild. Вызывается debouncer-ом
// из собственной горутины: store.Rebuild продолжает отдавать читателям старую
// эпоху (last known good) всё время сборки (§18.1), поэтому здесь достаточно
// пометить rebuilding=true на наблюдаемое время работы.
func (s *Service) runBackgroundFull() {
	s.setRebuilding(true)
	defer s.setRebuilding(false)

	s.opMu.Lock()
	defer s.opMu.Unlock()
	// Фон работает с фоновым контекстом с щедрым таймаутом: вызывающий,
	// запустивший scheduleBackgroundFull, к этому моменту уже мог уйти —
	// отменять пересборку по его ctx было бы неверно (rebuild служит ВСЕМ
	// последующим запросам, не только тому, что его инициировал).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := s.reindexLocked(ctx, ModeFull, ""); err != nil {
		s.stateMu.Lock()
		s.lastDiagnostics = append(s.lastDiagnostics, domain.Diagnostic{
			Code: "index_rebuild_failed", Severity: domain.SeverityError, Message: err.Error(),
		})
		s.stateMu.Unlock()
	}
}

// precheckWork — исход lazy precheck: что видно на диске и во что это
// обойдётся. changed — файлы, которые новые, изменились по size/mtime,
// разобраны прошлой версией парсера или пропали: по нему решается, нужен ли
// прогон вообще. pending — сколько файлов прогон реально прочитает и
// разберёт: к changed добавляются гидратированные записи, которые пайплайн
// обязан дочитать (см. правило toRead в runComponent). Две величины, потому
// что после перезапуска они расходятся на порядки, и путать их — значит
// уводить вызывающего в блокирующий разбор всего проекта (D01).
type precheckWork struct {
	changed int
	pending int
}

// precheckChangedCount — сколько файлов изменилось НА ДИСКЕ. Тонкая обёртка
// над precheckWorkload для вызывающих, которым объём работы не нужен.
func (s *Service) precheckChangedCount(ctx context.Context) (int, error) {
	work, err := s.precheckWorkload(ctx)
	return work.changed, err
}

// precheckWorkload — lazy precheck (§18.5): сколько файлов новые,
// изменились по size/mtime или пропали, без чтения содержимого. Использует
// discoverComponentMeta, а не discoverComponent: тот же полный стат каждого
// файла компонента (freshness-гарантия не ослаблена — ни один файл не
// пропускается), но без workspace.SafeJoin на файл, чей absPath здесь
// никому не нужен (см. комментарий discoverComponentMeta) — на ut_demo это
// снимает ~40% времени вызова.
func (s *Service) precheckWorkload(ctx context.Context) (precheckWork, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	start := s.now()
	work, err := s.precheckWorkloadLocked(ctx)
	if err == nil {
		// Любой полный обход, откуда бы его ни позвали (EnsureFresh или
		// CachedFreshness), и есть самый свежий исход для признака stale.
		s.recordDiskCheck(start, work.changed)
	}
	return work, err
}

func (s *Service) precheckWorkloadLocked(ctx context.Context) (precheckWork, error) {
	var work precheckWork
	for _, id := range s.sortedComponentIDs() {
		if ctx.Err() != nil {
			return work, ctx.Err()
		}
		c, err := s.componentByID(id)
		if err != nil {
			return precheckWork{}, err
		}
		discovered, err := discoverComponentMeta(ctx, c.AbsRoot, c.Include, c.Exclude)
		if err != nil {
			return precheckWork{}, err
		}
		byPath := make(map[string]discoveredMeta, len(discovered))
		for _, d := range discovered {
			byPath[d.relPath] = d
		}
		// Корпус восстанавливается из индекса при первом обращении
		// (ADR-028): без этого после перезапуска процесса карта пуста и
		// изменившимися объявляются ВСЕ файлы компонента.
		corpus := s.ensureHydrated(ctx, id)
		for _, d := range discovered {
			old, known := corpus.files[d.relPath]
			if !known || old.parserVersion != ParserVersion ||
				old.size != d.size || old.mtimeNS != d.mtimeNS {
				work.changed++
				work.pending++
				continue
			}
			if old.hydrated {
				// Сам файл не менялся, но прогон его прочитает: без фактов
				// корпуса не собрать окружение резолвера. В changed не идёт —
				// иначе рестарт без правок планировал бы пересборку (R14).
				work.pending++
			}
		}
		for rel := range corpus.files {
			if _, ok := byPath[rel]; !ok {
				work.changed++
				work.pending++
			}
		}
	}
	return work, nil
}

func (s *Service) currentFreshness(ctx context.Context) (Freshness, error) {
	st, err := s.st.Status(ctx)
	if err != nil {
		return Freshness{}, err
	}
	return Freshness{Fresh: true, Generation: st.Generation}, nil
}

func (s *Service) staleFreshness(ctx context.Context, reason StaleReason) Freshness {
	st, err := s.st.Status(ctx)
	f := Freshness{Fresh: false, Reason: reason}
	if err == nil {
		f.Generation = st.Generation
		if !st.ValidatedAt.IsZero() {
			f.AgeSeconds = s.now().Sub(st.ValidatedAt).Seconds()
		}
	}
	return f
}
