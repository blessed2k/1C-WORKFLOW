package index

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Mode — режим Reindex (§18.5): ручной форс инкремента либо полной пересборки.
type Mode string

const (
	ModeIncremental Mode = "incremental"
	ModeFull        Mode = "full"
)

// ComponentResult — итог пайплайна одного компонента.
type ComponentResult struct {
	Component    domain.ComponentID
	FilesChanged int
	FilesRemoved int
	SymbolCount  int
	Diagnostics  []domain.Diagnostic
	// Counts — сколько строк каждого вида факта реально вставлено в store
	// этим прогоном (публикация, не in-memory резолюция) — белый ящик для
	// тестов, ловящих пропажу факта на публикации (см. publish.go, ts.counts).
	Counts publishCounts
}

// Result — итог Reindex.
type Result struct {
	Mode       Mode
	Generation domain.Generation
	Duration   time.Duration
	Components []ComponentResult
	// Stages — разбивка Duration по этапам конвейера (stagetiming.go).
	// Сумма DurationMS всех строк всегда равна Duration в миллисекундах —
	// см. doc-комментарий stageAccum.stages.
	Stages []StageTiming
}

// Phase — текущая фаза пайплайна (R91i: index_status).
type Phase string

const (
	PhaseIdle       Phase = "idle"
	PhaseIndexing   Phase = "indexing"
	PhaseDebouncing Phase = "debouncing"
)

// Status — наблюдаемое состояние индекса для index_status (R91i).
type Status struct {
	Phase             Phase
	Store             store.StoreStatus
	RebuildInProgress bool
	LastFullDuration  time.Duration
	LastIncremental   time.Duration
	// ETA — грубая оценка оставшегося времени текущей полной пересборки:
	// длительность прошлого full минус уже прошедшее время этого прогона.
	// 0, если пересборка не идёт или прошлого full ещё не было (честно
	// «неизвестно», а не выдуманное число) — см. doc-комментарий Status().
	ETA             time.Duration
	LastDiagnostics []domain.Diagnostic
	Candidates      []workspace.Candidate
}

// Policy: политика свежести запроса (§18.5).
type Policy struct {
	// Mode — "allow-stale" (по умолчанию) или "require-fresh".
	Mode string
	// Deadline переопределяет Config.RequireFreshDeadline для этого вызова.
	Deadline time.Duration
}

const (
	PolicyAllowStale   = "allow-stale"
	PolicyRequireFresh = "require-fresh"
)

// Freshness — исход EnsureFresh.
type Freshness struct {
	Fresh      bool
	Generation domain.Generation
	AgeSeconds float64
	Reason     StaleReason
	// ChangedFiles и CheckAgeSeconds заполняет только CachedFreshness:
	// сколько файлов на диске разошлись с индексом и сколько секунд назад
	// снят этот исход обхода (ADR-036).
	ChangedFiles    int
	CheckAgeSeconds float64
}

// ErrIndexNotFresh — require-fresh не дождался публикации в пределах deadline
// (stale под видом fresh не возвращается никогда), поэтому вызывающий
// получает честную ошибку с прогрессом вместо устаревшего ответа.
type ErrIndexNotFresh struct {
	Progress string
}

func (e *ErrIndexNotFresh) Error() string {
	return fmt.Sprintf("index_not_fresh: %s", e.Progress)
}

// Service — пайплайн индексации одного logical project поверх internal/store
// (NewService/Status/Reindex/EnsureFresh).
type Service struct {
	st       *store.Store
	project  domain.ProjectID
	manifest workspace.Manifest
	builtins *syntax.Index
	cfg      Config
	now      func() time.Time

	// opMu сериализует и Reindex, и любое чтение/изменение corpora: доступ к
	// резидентному корпусу вне вставки в store.Write/Rebuild небезопасен
	// (map, не защищённая иначе), а сам store уже требует «один writer на
	// проект» — второй мьютекс здесь не сужает конкурентность сверх этого.
	opMu    sync.Mutex
	corpora map[domain.ComponentID]*componentCorpus

	stateMu           sync.Mutex
	rebuilding        bool
	rebuildStartedAt  time.Time
	lastFullDuration  time.Duration
	lastIncremental   time.Duration
	lastDiagnostics   []domain.Diagnostic
	lastRebuildReason string
	// reindexedHere — этот процесс сам довёл прогон пайплайна до конца, и
	// lastDiagnostics содержит ЕГО список. Отдельный флаг, а не «len(
	// lastDiagnostics) > 0»: в тот же список пишет ensureHydrated (hydrate.go),
	// и по непустоте прогон неотличим от одной info гидратации — именно так
	// Status и скрывал персистентные диагностики (см. его doc-комментарий).
	reindexedHere bool

	deb *debouncer

	// diskMu охраняет исход последнего обхода диска и идущий обход
	// (ADR-036): источник признака stale у индексных инструментов. Отдельный
	// мьютекс, а не stateMu и не opMu: под ним только чтение и запись полей.
	// Ждать приходится синхронному пути без годного исхода, и ждёт он общий
	// обход (diskFlight), а не мьютекс.
	diskMu     sync.Mutex
	disk       diskCheck
	flight     *diskFlight
	diskClosed bool
	diskWG     sync.WaitGroup
	// diskCtx живёт, пока сервис открыт: Close отменяет им идущий обход.
	diskCtx    context.Context
	diskCancel context.CancelFunc
	// walk: сам обход диска под opMu; подменяется только тестами пакета
	// (число обходов и Close посреди обхода снаружи не наблюдаемы).
	walk func(context.Context) (precheckWork, error)
}

// NewService создаёт пайплайн поверх уже открытого store.Store.
func NewService(st *store.Store, project domain.ProjectID, manifest workspace.Manifest, builtins *syntax.Index, cfg Config) *Service {
	cfg.fill()
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Service{
		st: st, project: project, manifest: manifest, builtins: builtins, cfg: cfg, now: cfg.Now,
		corpora: make(map[domain.ComponentID]*componentCorpus),
	}
	s.deb = newDebouncer(cfg.DebounceQuiet, s.runBackgroundFull)
	s.diskCtx, s.diskCancel = context.WithCancel(context.Background())
	s.walk = s.precheckWorkloadLocked
	return s
}

// Close останавливает ещё не начавшуюся отложенную пересборку и ждёт
// завершения уже начавшейся. Store не закрывает — им владеет вызывающий.
func (s *Service) Close() error {
	s.stopDiskRefresh()
	s.deb.stop()
	s.deb.wait()
	return nil
}

func (s *Service) componentByID(id domain.ComponentID) (workspace.Component, error) {
	c, ok := s.manifest.Component(id)
	if !ok {
		return workspace.Component{}, fmt.Errorf("компонент %q не зарегистрирован в манифесте", id)
	}
	return c, nil
}

// sortedComponentIDs — компоненты манифеста в детерминированном порядке
// (§18.7): сортировка входов.
func (s *Service) sortedComponentIDs() []domain.ComponentID {
	ids := make([]domain.ComponentID, 0, len(s.manifest.Components))
	for _, c := range s.manifest.Components {
		ids = append(ids, c.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (s *Service) corpusFor(id domain.ComponentID) *componentCorpus {
	c, ok := s.corpora[id]
	if !ok {
		c = newComponentCorpus(id)
		s.corpora[id] = c
	}
	return c
}

// Reindex — ручной форс индексации (§18.5). comp=="" — все компоненты
// манифеста. mode=full использует store.Rebuild (новая эпоха, §18.1);
// mode=incremental — store.Write (одна транзакция, §18.1).
func (s *Service) Reindex(ctx context.Context, mode Mode, comp domain.ComponentID) (Result, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.reindexLocked(ctx, mode, comp)
}

func (s *Service) reindexLocked(ctx context.Context, mode Mode, comp domain.ComponentID) (res Result, err error) {
	start := s.now()
	defer func() {
		switch {
		case err != nil:
			// Упавший прогон мог опубликовать часть или ничего: прошлый
			// исход обхода больше ничего не говорит о диске.
			s.invalidateDiskCheck()
		case comp == "":
			// Прогон по всем компонентам сам сверил корпус с диском: на
			// момент его старта расхождений нет. Время старта, а не конца:
			// правка во время прогона могла в него не попасть.
			s.recordDiskCheck(start, 0)
		default:
			// Прогон по одному компоненту про остальные ничего не знает.
			s.invalidateDiskCheck()
		}
	}()
	var ids []domain.ComponentID
	if comp != "" {
		if _, err := s.componentByID(comp); err != nil {
			return Result{}, err
		}
		ids = []domain.ComponentID{comp}
	} else {
		ids = s.sortedComponentIDs()
	}

	stages := newStageAccum()
	full := mode == ModeFull
	if !full {
		// Гидратация — ДО открытия write-транзакции (ADR-028): она сама
		// открывает read-транзакцию store, а полной пересборке корпус из
		// индекса не нужен — она строит его с нуля.
		hydrateStart := s.now()
		for _, id := range ids {
			s.ensureHydrated(ctx, id)
		}
		stages.mark(s.now, "hydrate", hydrateStart)
	}
	var results []ComponentResult
	runAll := func(tx *store.WriteTx) error {
		results = results[:0]
		for _, id := range ids {
			c, err := s.componentByID(id)
			if err != nil {
				return err
			}
			corpus := s.corpusFor(id)
			if full {
				// Сброс ЦЕЛИКОМ: keyIndex/totalRefs тоже, иначе они переживают
				// reset resolved и растут стухшими записями с прошлых
				// поколений (setResolved снимает старые записи только когда
				// находит старый corpus.resolved[rel], а после reset его нет).
				*corpus = *newComponentCorpus(id)
			}
			stats, err := runComponent(ctx, tx, s.project, c, corpus, s.builtins, s.cfg, full, stages)
			if err != nil {
				return fmt.Errorf("компонент %s: %w", id, err)
			}
			results = append(results, ComponentResult{
				Component: id, FilesChanged: stats.filesChanged, FilesRemoved: stats.filesRemoved,
				SymbolCount: stats.symbolCount, Diagnostics: stats.diagnostics, Counts: stats.counts,
			})
		}
		return nil
	}

	if full {
		err = s.st.Rebuild(ctx, runAll)
	} else {
		err = s.st.Write(ctx, runAll)
	}
	if err != nil {
		return Result{}, err
	}

	dur := s.now().Sub(start)
	st, statusErr := s.st.Status(ctx)
	var gen domain.Generation
	if statusErr == nil {
		gen = st.Generation
	}

	var diags []domain.Diagnostic
	for _, r := range results {
		diags = append(diags, r.Diagnostics...)
	}
	s.stateMu.Lock()
	if full {
		s.lastFullDuration = dur
	} else {
		s.lastIncremental = dur
	}
	s.lastDiagnostics = diags
	s.reindexedHere = true
	s.stateMu.Unlock()

	return Result{Mode: mode, Generation: gen, Duration: dur, Components: results, Stages: stages.stages(dur)}, nil
}

// diagnosticsFallbackLimit — верхняя граница числа диагностик, читаемых из
// store при пустом in-memory кэше (см. diagnosticsFromStore). Разово
// найденное на ut_demo число — десятки-сотни (docs/benchmarks.md: 47 на
// полном индексе) — лимит на два порядка выше, страховка от патологии, а не
// рабочий предел.
const diagnosticsFallbackLimit = 10000

// Status отдаёт наблюдаемое состояние (R91i): фаза, счётчики store, размеры
// эпох, последние diagnostics, кандидаты автообнаружения.
//
// LastDiagnostics приоритетно берётся из s.lastDiagnostics (диагностика
// Reindex, сделанного ЭТИМ процессом) — свежая, без лишнего похода в store.
// Если процесс ещё не индексировал сам (s.reindexedHere == false) — читается
// диагностика ТЕКУЩЕЙ активной эпохи прямо из store (diagnosticsFromStore):
// она персистится там каждым Reindex, в том числе прошлого процесса, но
// раньше никогда не читалась обратно (находка P7) — index_status свежего
// процесса молчал о diagnostics, которые физически уже лежали в SQLite.
// Ошибка чтения (например, store уже закрывается) не валит весь Status —
// пустой diagnostics лучше упавшего вызова, тот же принцип, что уже применён
// к errors.Is(err, store.ErrStoreClosed) выше.
//
// Персистентный список и диагностика гидратации СЛИВАЮТСЯ, а не замещают
// друг друга. Условием раньше была непустота s.lastDiagnostics, но в тот же
// список пишет ensureHydrated — и в ШТАТНОМ случае тоже (info
// index_corpus_not_hydrated, «компонент индексируется впервые»). Проект с
// двумя компонентами, где один ещё не индексировался, а у второго в эпохе
// лежат диагностики, на свежем процессе показывал одну info и скрывал
// реальные. Дублирования слияние не даёт: диагностики гидратации в store не
// персистятся (они не входят в Result прогона), а как только прогон в этом
// процессе доведён до конца, его список становится единственным источником
// и в store уже не ходим.
func (s *Service) Status(ctx context.Context) (Status, error) {
	st, err := s.st.Status(ctx)
	if err != nil && !errors.Is(err, store.ErrStoreClosed) {
		return Status{}, err
	}

	s.stateMu.Lock()
	rebuilding := s.rebuilding
	startedAt := s.rebuildStartedAt
	lastFull := s.lastFullDuration
	lastInc := s.lastIncremental
	diags := append([]domain.Diagnostic(nil), s.lastDiagnostics...)
	reindexedHere := s.reindexedHere
	s.stateMu.Unlock()

	if !reindexedHere {
		if fromStore, ferr := s.diagnosticsFromStore(ctx); ferr == nil {
			diags = append(fromStore, diags...)
		}
	}

	phase := PhaseIdle
	if rebuilding {
		phase = PhaseIndexing
	}

	// ETA — грубая оценка на основе среднего времени по прошлому full-
	// прогону (тело таска: «фазу, счётчики, ETA»). Без прошлого прогона
	// оценивать не на чем — 0, честно, не выдумка по нулевым данным.
	var eta time.Duration
	if rebuilding && lastFull > 0 && !startedAt.IsZero() {
		elapsed := s.now().Sub(startedAt)
		if elapsed < lastFull {
			eta = lastFull - elapsed
		}
	}

	var candidates []workspace.Candidate
	if all, discErr := workspace.Discover(s.manifest.Root); discErr == nil {
		for _, c := range all {
			if _, known := s.manifest.Component(c.SuggestedID); !known {
				candidates = append(candidates, c)
			}
		}
	}

	return Status{
		Phase: phase, Store: st, RebuildInProgress: rebuilding,
		LastFullDuration: lastFull, LastIncremental: lastInc, ETA: eta,
		LastDiagnostics: diags, Candidates: candidates,
	}, nil
}

// diagnosticsFromStore читает диагностику текущей активной эпохи из store
// (см. doc-комментарий Status): вызывается только пока этот процесс сам не
// довёл ни одного прогона до конца (s.reindexedHere == false). Отдельная read-транзакция, а не переиспользование той,
// что уже дала st выше, — s.st.Status(ctx) её сама открывает и закрывает,
// здесь она уже недоступна.
func (s *Service) diagnosticsFromStore(ctx context.Context) ([]domain.Diagnostic, error) {
	var out []domain.Diagnostic
	err := s.st.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Diagnostics(diagnosticsFallbackLimit)
		if err != nil {
			return err
		}
		out = make([]domain.Diagnostic, 0, len(rows))
		for _, r := range rows {
			out = append(out, domain.Diagnostic{
				Code: r.Code, Severity: domain.Severity(r.Severity), Message: r.Message,
				Component: domain.ComponentID(r.ComponentID), File: r.File, Span: r.Span,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
