package index

import (
	"context"
	"time"
)

// diskCheck: исход последнего обхода диска (precheck): сколько файлов
// разошлись с индексом и когда обход начат. valid=false: исхода нет
// (процесс ещё не проверял, или прогон по одному компоненту его обесценил).
type diskCheck struct {
	valid   bool
	at      time.Time
	changed int
}

// diskRefreshTimeout: потолок фонового обхода. На ut_demo обход идёт около
// 3 с; предел нужен только от зависшей файловой системы.
const diskRefreshTimeout = 2 * time.Minute

// CachedFreshness: дешёвый источник признака stale для индексных
// инструментов (ADR-036). В отличие от EnsureFresh ничего не запускает: ни
// инкремента, ни фоновой пересборки, индекс не меняется. Порядок:
//
//  1. идёт полная пересборка -> stale "full-rebuild-in-progress";
//  2. на эпохе признак полной пересборки -> stale "full-rebuild-required";
//  3. исход обхода диска моложе FreshnessTTL -> берётся как есть;
//  4. моложе FreshnessMaxAge -> берётся как есть, а новый обход уходит в фон
//     (один на сервис);
//  5. иначе (и до первой проверки в процессе) обход идёт синхронно.
//
// Исход с changed > 0 даёт stale "files-changed-on-disk". Ошибку обхода
// вызывающий обязан показать как «свежесть не проверена», а не как «свежо».
func (s *Service) CachedFreshness(ctx context.Context) (Freshness, error) {
	if s.isRebuilding() {
		return Freshness{Fresh: false, Reason: "full-rebuild-in-progress"}, nil
	}
	if s.st.NeedsFullRebuild() {
		return Freshness{Fresh: false, Reason: "full-rebuild-required"}, nil
	}

	now := s.now()
	s.diskMu.Lock()
	check := s.disk
	age := now.Sub(check.at)
	usable := check.valid && age < s.cfg.FreshnessMaxAge
	if usable && age >= s.cfg.FreshnessTTL {
		s.startDiskRefreshLocked()
	}
	s.diskMu.Unlock()

	if !usable {
		work, err := s.precheckWorkload(ctx)
		if err != nil {
			return Freshness{}, err
		}
		check = diskCheck{valid: true, at: now, changed: work.changed}
		age = 0
	}
	f := Freshness{Fresh: check.changed == 0, ChangedFiles: check.changed, CheckAgeSeconds: age.Seconds()}
	if !f.Fresh {
		f.Reason = "files-changed-on-disk"
	}
	return f, nil
}

// recordDiskCheck запоминает исход обхода, начатого в момент at. Более
// старый исход не затирает более новый: обходы и прогоны сериализованы
// opMu, но фоновый обход может закончиться позже прогона, начатого после
// него.
func (s *Service) recordDiskCheck(at time.Time, changed int) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if s.disk.valid && at.Before(s.disk.at) {
		return
	}
	s.disk = diskCheck{valid: true, at: at, changed: changed}
}

// invalidateDiskCheck: исход обхода больше ничего не говорит о диске
// (прогон по одному компоненту, упавший прогон): следующий вызов проверит
// синхронно.
func (s *Service) invalidateDiskCheck() {
	s.diskMu.Lock()
	s.disk = diskCheck{}
	s.diskMu.Unlock()
}

// startDiskRefreshLocked поднимает фоновый обход, если он ещё не идёт и
// сервис не закрыт. Вызывается под diskMu: так Add у WaitGroup не
// разъедется с Wait в stopDiskRefresh.
func (s *Service) startDiskRefreshLocked() {
	if s.diskRefreshing || s.diskClosed {
		return
	}
	s.diskRefreshing = true
	s.diskWG.Add(1)
	go func() {
		defer s.diskWG.Done()
		defer func() {
			s.diskMu.Lock()
			s.diskRefreshing = false
			s.diskMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), diskRefreshTimeout)
		defer cancel()
		// Ошибка фонового обхода не теряется молча: прошлый исход стареет
		// дальше, и после FreshnessMaxAge обход пойдёт синхронно, где
		// ошибка дойдёт до вызывающего.
		_, _ = s.precheckWorkload(ctx)
	}()
}

// stopDiskRefresh запрещает новые фоновые обходы и ждёт текущий: после
// Close сервис не трогает ни store, ни диск.
func (s *Service) stopDiskRefresh() {
	s.diskMu.Lock()
	s.diskClosed = true
	s.diskMu.Unlock()
	s.diskWG.Wait()
}
