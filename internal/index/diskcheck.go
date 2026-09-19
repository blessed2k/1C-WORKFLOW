package index

import (
	"context"
	"errors"
	"time"
)

// StaleReason: почему ответ индекса назван устаревшим. Одни и те же строки
// видят EnsureFresh (get_context_for_task) и CachedFreshness (остальные
// индексные инструменты), и app строит по ним текст предупреждения.
type StaleReason string

const (
	// ReasonFilesChanged: обход диска нашёл файлы, разошедшиеся с индексом.
	ReasonFilesChanged StaleReason = "files-changed-on-disk"
	// ReasonRebuildInProgress: идёт фоновая полная пересборка.
	ReasonRebuildInProgress StaleReason = "full-rebuild-in-progress"
	// ReasonRebuildRequired: на эпохе признак полной пересборки.
	ReasonRebuildRequired StaleReason = "full-rebuild-required"
	// ReasonRebuildScheduled: EnsureFresh запланировал фоновую пересборку.
	ReasonRebuildScheduled StaleReason = "background-rebuild-scheduled"
)

// ErrServiceClosed: сервис закрыт, обходить диск и читать store он больше
// не будет. Вызывающий показывает это как «свежесть не проверена».
var ErrServiceClosed = errors.New("index: сервис закрыт")

// diskCheck: исход последнего обхода диска (precheck), сколько файлов
// разошлись с индексом и когда обход начат. valid=false: исхода нет
// (процесс ещё не проверял, или прогон по одному компоненту его обесценил).
type diskCheck struct {
	valid   bool
	at      time.Time
	changed int
}

// diskFlight: один идущий обход, общий для всех, кому нужен его исход
// (singleflight). done закрывается после записи at/changed/err.
type diskFlight struct {
	done    chan struct{}
	at      time.Time
	changed int
	err     error
}

// diskRefreshTimeout: потолок одного обхода. На ut_demo обход идёт около
// 3 с; предел нужен только от зависшей файловой системы.
const diskRefreshTimeout = 2 * time.Minute

// CachedFreshness: дешёвый источник признака stale для индексных
// инструментов (ADR-036). В отличие от EnsureFresh ничего не запускает: ни
// инкремента, ни фоновой пересборки, индекс не меняется. Порядок:
//
//  1. идёт полная пересборка: stale ReasonRebuildInProgress;
//  2. на эпохе признак полной пересборки: stale ReasonRebuildRequired;
//  3. исход обхода диска моложе FreshnessTTL: берётся как есть;
//  4. моложе FreshnessMaxAge: берётся как есть, а новый обход уходит в фон;
//  5. иначе (и до первой проверки в процессе) вызов ждёт обход. Обход один
//     на сервис: параллельные вызовы, прогрев и фоновое обновление ждут один
//     и тот же (diskFlight).
//
// Исход с changed > 0 даёт stale ReasonFilesChanged. Ошибку (в том числе
// ErrServiceClosed) вызывающий обязан показать как «свежесть не проверена»,
// а не как «свежо».
func (s *Service) CachedFreshness(ctx context.Context) (Freshness, error) {
	if s.isRebuilding() {
		return Freshness{Fresh: false, Reason: ReasonRebuildInProgress}, nil
	}
	if s.st.NeedsFullRebuild() {
		return Freshness{Fresh: false, Reason: ReasonRebuildRequired}, nil
	}

	now := s.now()
	s.diskMu.Lock()
	if s.diskClosed {
		s.diskMu.Unlock()
		return Freshness{}, ErrServiceClosed
	}
	check := s.disk
	age := now.Sub(check.at)
	if check.valid && age < s.cfg.FreshnessMaxAge {
		if age >= s.cfg.FreshnessTTL {
			s.startFlightLocked()
		}
		s.diskMu.Unlock()
		return freshnessOf(check.changed, age), nil
	}
	fl := s.startFlightLocked()
	s.diskMu.Unlock()

	select {
	case <-fl.done:
	case <-ctx.Done():
		return Freshness{}, ctx.Err()
	}
	if fl.err != nil {
		return Freshness{}, fl.err
	}
	age = s.now().Sub(fl.at)
	if age < 0 {
		age = 0
	}
	return freshnessOf(fl.changed, age), nil
}

func freshnessOf(changed int, age time.Duration) Freshness {
	f := Freshness{Fresh: changed == 0, ChangedFiles: changed, CheckAgeSeconds: age.Seconds()}
	if !f.Fresh {
		f.Reason = ReasonFilesChanged
	}
	return f
}

// WarmFreshness запускает обход диска в фоне, если годного исхода нет:
// зовётся при открытии проекта, чтобы первый вызов инструмента ждал уже
// идущий обход, а не начинал свой (ADR-036).
func (s *Service) WarmFreshness() {
	now := s.now()
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if s.diskClosed {
		return
	}
	if s.disk.valid && now.Sub(s.disk.at) < s.cfg.FreshnessTTL {
		return
	}
	s.startFlightLocked()
}

// startFlightLocked отдаёт идущий обход или поднимает новый. Вызывается под
// diskMu: так Add у WaitGroup не разъедется с Wait в stopDiskRefresh. На
// закрытом сервисе отдаёт уже завершённый обход с ErrServiceClosed.
func (s *Service) startFlightLocked() *diskFlight {
	if s.flight != nil {
		return s.flight
	}
	fl := &diskFlight{done: make(chan struct{})}
	if s.diskClosed {
		fl.err = ErrServiceClosed
		close(fl.done)
		return fl
	}
	s.flight = fl
	s.diskWG.Add(1)
	go s.runFlight(fl)
	return fl
}

// runFlight: тело обхода в собственной горутине сервиса. Контекст от
// diskCtx, а не от вызывающего: обход нужен всем ждущим, и уход одного из
// них его не отменяет; отменяет только Close.
func (s *Service) runFlight(fl *diskFlight) {
	defer s.diskWG.Done()
	ctx, cancel := context.WithTimeout(s.diskCtx, diskRefreshTimeout)
	defer cancel()

	fl.at, fl.changed, fl.err = s.flightWalk(ctx)

	s.diskMu.Lock()
	s.flight = nil
	s.diskMu.Unlock()
	close(fl.done)
	if hook := s.cfg.OnDiskCheck; hook != nil {
		hook(fl.at)
	}
}

// flightWalk берёт opMu прерываемо, перепроверяет исход (пока ждали opMu,
// его мог записать прогон или EnsureFresh) и только тогда обходит диск.
func (s *Service) flightWalk(ctx context.Context) (time.Time, int, error) {
	if err := s.lockOp(ctx); err != nil {
		return time.Time{}, 0, err
	}
	defer s.opMu.Unlock()

	s.diskMu.Lock()
	closed, check := s.diskClosed, s.disk
	s.diskMu.Unlock()
	if closed {
		return time.Time{}, 0, ErrServiceClosed
	}
	now := s.now()
	if check.valid && now.Sub(check.at) < s.cfg.FreshnessTTL {
		return check.at, check.changed, nil
	}

	work, err := s.walk(ctx)
	if err != nil {
		return time.Time{}, 0, err
	}
	s.recordDiskCheck(now, work.changed)
	return now, work.changed, nil
}

// lockOp захватывает opMu, но ждать перестаёт по отмене ctx: Close не
// должен висеть за чужим reindex. Отказавшийся захват отпускается сразу,
// как только мьютекс достанется вспомогательной горутине.
func (s *Service) lockOp(ctx context.Context) error {
	if s.opMu.TryLock() {
		return nil
	}
	acquired := make(chan struct{})
	go func() {
		s.opMu.Lock()
		close(acquired)
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		go func() {
			<-acquired
			s.opMu.Unlock()
		}()
		return ctx.Err()
	}
}

// recordDiskCheck запоминает исход обхода, начатого в момент at. Более
// старый исход не затирает более новый.
func (s *Service) recordDiskCheck(at time.Time, changed int) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if s.disk.valid && at.Before(s.disk.at) {
		return
	}
	s.disk = diskCheck{valid: true, at: at, changed: changed}
}

// invalidateDiskCheck: исход обхода больше ничего не говорит о диске
// (прогон по одному компоненту, упавший прогон), следующий вызов ждёт
// новый обход.
func (s *Service) invalidateDiskCheck() {
	s.diskMu.Lock()
	s.disk = diskCheck{}
	s.diskMu.Unlock()
}

// stopDiskRefresh запрещает новые обходы, отменяет идущий и ждёт его конца:
// после Close сервис не трогает ни store, ни диск.
func (s *Service) stopDiskRefresh() {
	s.diskMu.Lock()
	s.diskClosed = true
	s.diskMu.Unlock()
	s.diskCancel()
	s.diskWG.Wait()
}
