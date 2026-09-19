package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// StatusItem — items[0] ответа index_status: наблюдаемое состояние индекса
// активного проекта (R91i: фаза, поколение, возраст, ETA, diagnostics, размеры
// эпох, кандидаты autodiscovery).
//
// Упрощение, назван явно: "счётчики" ограничены тем, что index.Service.Status
// реально выставляет сегодня (interfaces.md, «Из таска 09») — там нет
// агрегатов по символам/ссылкам/рёбрам, только Store (размеры/generation) и
// diagnostics/candidates. Живой count(*) по таблицам store тоже недоступен:
// internal/store.ReadTx экспортирует Meta/GenerationNumber/Blob/SourceFileID/
// NodeID/Validate — без агрегатов (interfaces.md, «Из таска 03»), а
// расширение этого контракта вне зоны тикета 10 (internal/store уже сдан).
// Единственный публично доступный счётчик по видам факта —
// ComponentResult.SymbolCount/FilesChanged/FilesRemoved из результата
// Reindex; LastReindexCounts несёт ИМЕННО ЭТО, и только когда reindex уже был
// вызван ЧЕРЕЗ ЭТОТ процесс (иначе nil, честно, а не нулями по умолчанию).
type StatusItem struct {
	Phase                  string                `json:"phase"`
	Project                domain.ProjectID      `json:"project"`
	Generation             domain.Generation     `json:"generation"`
	Epoch                  int                   `json:"epoch"`
	RebuildInProgress      bool                  `json:"rebuildInProgress"`
	NeedsFullRebuild       bool                  `json:"needsFullRebuild"`
	ValidatedAt            time.Time             `json:"validatedAt,omitempty"`
	AgeSeconds             float64               `json:"ageSeconds,omitempty"`
	ETASeconds             float64               `json:"etaSeconds,omitempty"`
	LastFullSeconds        float64               `json:"lastFullSeconds,omitempty"`
	LastIncrementalSeconds float64               `json:"lastIncrementalSeconds,omitempty"`
	EpochBytes             int64                 `json:"epochBytes"`
	WALBytes               int64                 `json:"walBytes"`
	Diagnostics            []domain.Diagnostic   `json:"diagnostics,omitempty"`
	DiagnosticsDigest      *DiagnosticsDigest    `json:"diagnosticsDigest,omitempty"`
	Candidates             []workspace.Candidate `json:"candidates,omitempty"`
	LastReindexCounts      *ReindexCounts        `json:"lastReindexCounts,omitempty"`
}

// diagnosticsSample — сколько диагностик уезжает агенту по умолчанию.
// Образец — notGrantingSample у rights_audit (cmd/mcp1c/inspect.go): сигнал
// несёт длина списка, а не сам список. На реальных проектах их десятки и
// сотни, и раньше они приезжали целиком на КАЖДЫЙ вызов
// index_status/reindex — контекст сессии за две-три строки пользы.
const diagnosticsSample = 10

// DiagnosticsDigest — дозировка диагностик ответа index_status/reindex:
// полное число, разбивка по кодам и признак урезания. Приезжает всегда,
// когда диагностики есть, — в том числе когда список отдан целиком: агенту
// нужна разбивка по кодам независимо от того, урезали список или нет.
type DiagnosticsDigest struct {
	TotalCount int            `json:"totalCount"`
	ShownCount int            `json:"shownCount"`
	ByCode     map[string]int `json:"byCode,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
	Note       string         `json:"note,omitempty"`
}

// doseDiagnostics возвращает список диагностик, урезанный до
// diagnosticsSample, и его дайджест. full=true отдаёт список целиком —
// дайджест при этом никуда не девается, меняется только Truncated/Note.
//
// Пустой вход — nil-дайджест: дозировать нечего, и лишнего поля в ответе
// быть не должно.
func doseDiagnostics(diags []domain.Diagnostic, full bool) ([]domain.Diagnostic, *DiagnosticsDigest) {
	if len(diags) == 0 {
		return nil, nil
	}
	digest := &DiagnosticsDigest{TotalCount: len(diags), ShownCount: len(diags), ByCode: map[string]int{}}
	for _, d := range diags {
		digest.ByCode[d.Code]++
	}
	if full || len(diags) <= diagnosticsSample {
		return diags, digest
	}
	digest.ShownCount = diagnosticsSample
	digest.Truncated = true
	digest.Note = fmt.Sprintf(
		"diagnostics: показаны %d из %d, полный список — includeAllDiagnostics=true",
		diagnosticsSample, digest.TotalCount)
	return append([]domain.Diagnostic(nil), diags[:diagnosticsSample]...), digest
}

// ReindexCounts — то же самое, что каждый ComponentResult.{FilesChanged,
// FilesRemoved,SymbolCount} последнего Reindex, просуммированное по всем
// компонентам.
type ReindexCounts struct {
	FilesChanged int `json:"filesChanged"`
	FilesRemoved int `json:"filesRemoved"`
	Symbols      int `json:"symbols"`
}

// StatusInput — вход инструмента index_status. Единственное поле —
// явный запрос полного списка диагностик; по умолчанию их дозирует
// doseDiagnostics.
type StatusInput struct {
	IncludeAllDiagnostics bool `json:"includeAllDiagnostics,omitempty"`
}

// ReindexInput — вход инструмента reindex (тикет 10 п.6).
type ReindexInput struct {
	// IncludeAllDiagnostics отдаёт диагностики целиком, без урезания до
	// diagnosticsSample. Дайджест приезжает в обоих случаях.
	IncludeAllDiagnostics bool `json:"includeAllDiagnostics,omitempty"`

	// Mode — "incremental" (по умолчанию) или "full".
	Mode string `json:"mode,omitempty"`
	// Component сужает reindex до одного компонента манифеста; пусто — все.
	Component string `json:"component,omitempty"`
	// ProjectRoot — каталог с 1c-project.json. Если задан и такого корня ещё
	// нет в registry, reindex сначала регистрирует проект и делает его
	// активным (Projects.EnsureProjectActive), и только потом продолжает как
	// обычный reindex — единственный сегодня production-путь попасть в
	// registry без ручной правки .mcp1c/registry.json.
	ProjectRoot string `json:"projectRoot,omitempty"`
}

// ReindexResultItem — items[0] ответа reindex: итог, счётчики, новое поколение.
//
// Diagnostics здесь — ЕДИНСТВЕННОЕ место диагностик в ответе (R34). Раньше
// одна и та же запись приезжала дважды: в ReindexComponentResult.Diagnostics
// и тут же в общем списке. Победил общий список, а не элемент компонента, по
// двум причинам: дозировать (счётчик, выборка, разбивка по кодам) можно
// только там, где список один, а привязка к компоненту не теряется — она
// переехала в domain.Diagnostic.Component, который заполняется при сведении.
type ReindexResultItem struct {
	Mode       string                   `json:"mode"`
	Generation domain.Generation        `json:"generation"`
	DurationMS int64                    `json:"durationMs"`
	Components []ReindexComponentResult `json:"components"`
	// Stages — разбивка DurationMS по этапам конвейера (index.StageTiming).
	// Сумма DurationMS всех строк равна DurationMS выше — см. doc-комментарий
	// index.stageAccum.stages.
	Stages            []ReindexStageResult `json:"stages,omitempty"`
	Diagnostics       []domain.Diagnostic  `json:"diagnostics,omitempty"`
	DiagnosticsDigest *DiagnosticsDigest   `json:"diagnosticsDigest,omitempty"`
}

// ReindexStageResult — один этап конвейера в MCP-ответе reindex.
type ReindexStageResult struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"durationMs"`
}

// ReindexComponentResult — итог одного компонента внутри Reindex: счётчики,
// без диагностик (см. doc ReindexResultItem — они живут одним списком в
// элементе ответа, с проставленным Component).
type ReindexComponentResult struct {
	Component    domain.ComponentID `json:"component"`
	FilesChanged int                `json:"filesChanged"`
	FilesRemoved int                `json:"filesRemoved"`
	Symbols      int                `json:"symbols"`
}

// IndexStatusService — сервис за index_status и reindex (interfaces.md:
// internal/app.IndexStatusService, по одному методу на инструмент).
type IndexStatusService struct {
	projects *Projects
	now      func() time.Time

	mu          sync.Mutex
	lastReindex map[domain.ProjectID]lastReindexState
}

type lastReindexState struct {
	counts ReindexCounts
}

// NewIndexStatusService строит сервис поверх общего резолвера проектов.
func NewIndexStatusService(projects *Projects) *IndexStatusService {
	return &IndexStatusService{
		projects:    projects,
		now:         time.Now,
		lastReindex: map[domain.ProjectID]lastReindexState{},
	}
}

// Status отвечает на index_status: фаза, поколение, возраст, счётчики
// последнего reindex этого процесса, ETA, diagnostics, размеры эпох,
// кандидаты autodiscovery (R91i).
// Диагностики дозируются doseDiagnostics: по умолчанию первые
// diagnosticsSample плюс дайджест, целиком — по in.IncludeAllDiagnostics.
func (s *IndexStatusService) Status(ctx context.Context, in StatusInput) (Response[StatusItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[StatusItem]{}, err
	}

	// Та же проверка свежести, что у остальных индексных инструментов
	// (ADR-036): правка на диске и идущая пересборка дают stale_index.
	// Транзакции index_status не открывает, поэтому ReadSnapshot ему не
	// нужен, а источник свежести общий.
	snap := snapshotFreshness(ctx, op)

	st, err := op.Service.Status(ctx)
	if err != nil {
		return Response[StatusItem]{}, fmt.Errorf("проект %s: index_status: %w", op.Entry.ID, err)
	}

	diags, digest := doseDiagnostics(st.LastDiagnostics, in.IncludeAllDiagnostics)

	item := StatusItem{
		Phase:                  string(st.Phase),
		Project:                op.Entry.ID,
		Generation:             st.Store.Generation,
		Epoch:                  st.Store.Epoch,
		RebuildInProgress:      st.RebuildInProgress,
		NeedsFullRebuild:       st.Store.NeedsFullRebuild,
		ValidatedAt:            st.Store.ValidatedAt,
		ETASeconds:             st.ETA.Seconds(),
		LastFullSeconds:        st.LastFullDuration.Seconds(),
		LastIncrementalSeconds: st.LastIncremental.Seconds(),
		EpochBytes:             st.Store.DBBytes,
		WALBytes:               st.Store.WALBytes,
		Diagnostics:            diags,
		DiagnosticsDigest:      digest,
		Candidates:             st.Candidates,
		LastReindexCounts:      s.lastCounts(op.Entry.ID),
	}
	if !st.Store.ValidatedAt.IsZero() {
		item.AgeSeconds = s.now().Sub(st.Store.ValidatedAt).Seconds()
	}

	resp := Response[StatusItem]{
		Generation: st.Store.Generation,
		Items:      []StatusItem{item},
		TotalCount: 1,
	}
	if st.Store.NeedsFullRebuild {
		// Второе предупреждение рядом с общим stale_index: код
		// needs_full_rebuild старше ADR-036, его могут читать клиенты.
		resp.Stale = true
		resp.Warnings = append(resp.Warnings, Warning{
			Code:    "needs_full_rebuild",
			Message: "индекс проекта требует полной пересборки",
			Hint:    "вызовите reindex mode=full",
		})
	}
	if st.Store.DBUnavailable {
		resp.Warnings = append(resp.Warnings, Warning{
			Code:    "db_unavailable",
			Message: "не удалось арендовать читателя store для части полей статуса (пул исчерпан)",
			Hint:    "повторите вызов",
		})
	}
	return withSnapshot(resp, snap), nil
}

// Reindex отвечает на reindex: ручной форс инкремента или полной пересборки
// (тикет 10 п.6). Component, не описанный в манифесте активного проекта, —
// component_not_registered ДО обращения к index.Service (манифест уже
// загружен вместе с проектом, дважды спрашивать store незачем).
//
// ProjectRoot, если задан, обрабатывается ДО Active: EnsureProjectActive
// регистрирует проект (если такого корня в registry ещё нет) и делает его
// активным, потом Active видит уже активный проект как обычно. Пустой
// ProjectRoot ничего не меняет — поведение то же, что до этого поля.
func (s *IndexStatusService) Reindex(ctx context.Context, in ReindexInput) (Response[ReindexResultItem], error) {
	defaultMode := ""
	if root := strings.TrimSpace(in.ProjectRoot); root != "" {
		registered, err := s.projects.EnsureProjectActive(ctx, root)
		if err != nil {
			return Response[ReindexResultItem]{}, err
		}
		if registered {
			// Только что зарегистрированный проект ни разу не индексировался —
			// "инкремент" от несуществующего индекса не имеет смысла.
			defaultMode = string(index.ModeFull)
		}
	}

	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[ReindexResultItem]{}, err
	}

	modeIn := in.Mode
	if strings.TrimSpace(modeIn) == "" && defaultMode != "" {
		modeIn = defaultMode
	}
	mode, modeErr := parseMode(modeIn)
	if modeErr != nil {
		return Response[ReindexResultItem]{}, modeErr
	}

	var comp domain.ComponentID
	if strings.TrimSpace(in.Component) != "" {
		comp = domain.ComponentID(in.Component)
		if _, ok := op.Manifest.Component(comp); !ok {
			return Response[ReindexResultItem]{}, ComponentNotRegisteredError(comp, componentIDs(op.Manifest)).
				WithProject(op.Entry.ID)
		}
	}

	result, err := op.Service.Reindex(ctx, mode, comp)
	if err != nil {
		if fresh := FromIndexNotFresh(err); fresh != nil {
			return Response[ReindexResultItem]{}, fresh.WithProject(op.Entry.ID)
		}
		return Response[ReindexResultItem]{}, fmt.Errorf("проект %s: reindex: %w", op.Entry.ID, err)
	}
	s.recordCounts(op.Entry.ID, result)

	item := ReindexResultItem{
		Mode:       string(result.Mode),
		Generation: result.Generation,
		DurationMS: result.Duration.Milliseconds(),
	}
	for _, st := range result.Stages {
		item.Stages = append(item.Stages, ReindexStageResult{Name: st.Name, DurationMS: st.DurationMS})
	}
	var diags []domain.Diagnostic
	for _, c := range result.Components {
		item.Components = append(item.Components, ReindexComponentResult{
			Component:    c.Component,
			FilesChanged: c.FilesChanged,
			FilesRemoved: c.FilesRemoved,
			Symbols:      c.SymbolCount,
		})
		for _, d := range c.Diagnostics {
			// Компонент раньше читался из места в ответе; теперь список
			// один, поэтому принадлежность проставляется в самой записи.
			// Пайплайн его не заполняет (index/publish.go отдаёт component
			// только в store), пустое значение — не «неизвестно», а просто
			// не проставлено там.
			if d.Component == "" {
				d.Component = c.Component
			}
			diags = append(diags, d)
		}
	}
	item.Diagnostics, item.DiagnosticsDigest = doseDiagnostics(diags, in.IncludeAllDiagnostics)

	return Response[ReindexResultItem]{
		Generation: result.Generation,
		Items:      []ReindexResultItem{item},
		TotalCount: 1,
	}, nil
}

// parseMode проверяет mode инструмента reindex: пусто -> incremental
// (безопасное умолчание — полная пересборка не запускается неявно).
func parseMode(mode string) (index.Mode, error) {
	switch strings.TrimSpace(mode) {
	case "", string(index.ModeIncremental):
		return index.ModeIncremental, nil
	case string(index.ModeFull):
		return index.ModeFull, nil
	default:
		return "", fmt.Errorf("mode %q неизвестен, допустимы %q и %q", mode, index.ModeIncremental, index.ModeFull)
	}
}

// componentIDs перечисляет id компонентов манифеста.
func componentIDs(m workspace.Manifest) []domain.ComponentID {
	ids := make([]domain.ComponentID, len(m.Components))
	for i, c := range m.Components {
		ids[i] = c.ID
	}
	return ids
}

func (s *IndexStatusService) recordCounts(id domain.ProjectID, r index.Result) {
	var agg ReindexCounts
	for _, c := range r.Components {
		agg.FilesChanged += c.FilesChanged
		agg.FilesRemoved += c.FilesRemoved
		agg.Symbols += c.SymbolCount
	}
	s.mu.Lock()
	s.lastReindex[id] = lastReindexState{counts: agg}
	s.mu.Unlock()
}

func (s *IndexStatusService) lastCounts(id domain.ProjectID) *ReindexCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.lastReindex[id]
	if !ok {
		return nil
	}
	c := st.counts
	return &c
}
