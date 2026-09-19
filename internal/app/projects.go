package app

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Projects resolves the active logical project (workspace.Registry —
// interfaces.md «Из таска 02») into an open store+index pipeline pair, one
// per project, opened lazily on first use and cached for the life of the
// process (index.Service owns debounce/corpus state that is expensive to
// throw away between calls — this mirrors how the offline dumpState in
// cmd/mcp1c keeps its source alive, just one layer deeper).
//
// workspaceRoot is the same directory the existing --projects-root flag
// already names (docs/architecture-index.md §8: «Workspace: каталог, заданный
// --projects-root»): .mcp1c/registry.json and every project's SQLite epoch
// files live under it, independent of where the project's own sources are
// checked out (workspace.ProjectEntry.Root points there separately).
//
// An empty workspaceRoot is a legitimate, common state — the server started
// without --projects-root — and is handled without touching the filesystem:
// Active always answers no_active_project with a hint that names the flag.
type Projects struct {
	workspaceRoot string
	registry      *workspace.Registry
	builtins      *syntax.Index
	idxCfg        index.Config

	// registryErr: почему реестр не открылся (NewProjectsUnavailable).
	registryErr error

	mu     sync.Mutex
	opened map[domain.ProjectID]*openProject
	// closed: Close уже прошёл, новых проектов не открываем (иначе фоновый
	// прогрев после Close открыл бы store, который никто не закроет).
	closed bool
	// warmWG: фоновые открытия проекта из SetDump (прогрев, ADR-036); Close
	// их дожидается.
	warmWG sync.WaitGroup

	// activeMu охраняет active: одно состояние процесса «активный проект»
	// (активная выгрузка + индексный проект), см. activeproject.go.
	activeMu sync.Mutex
	active   activeState
}

// openProject is one project's live store+pipeline pair.
type openProject struct {
	Entry    workspace.ProjectEntry
	Manifest workspace.Manifest
	Store    *store.Store
	Service  *index.Service
}

// DefaultIndexConfig — стандартные tunables index.Service для инструментов,
// подключённых через NewProjects. Обёртка вокруг index.DefaultConfig нужна,
// чтобы cmd/mcp1c мог собрать deps, ни разу не написав в своём исходнике имя
// типа internal/index (RuleCmdThroughApp, internal/arch): вызывающему не
// требуется называть тип, если результат идёт через :=.
func DefaultIndexConfig() index.Config {
	cfg := index.DefaultConfig()
	cfg.GraphTunables = currentGraphTunables()
	return cfg
}

// graphTunables — пороги атрибуции объектного графа, разобранные из флагов
// -graph-* процессом. Процессная переменная здесь по той же причине, что и у
// кэша выгрузки (source.ConfigureCache): флаги разбираются один раз в
// parseFlags, а index.Config собирается ниже по стеку, в реестре индексных
// инструментов, который контрактом закрыт для правок. Нулевое значение —
// «пороги по умолчанию»: их подставляет сам internal/resolve.
//
// Мьютекс — не от сегодняшнего сценария (запись одна, в newServer, до начала
// обслуживания), а от того, что читателей у переменной много и приходят они из
// разных горутин: DefaultIndexConfig зовётся при каждом открытии проекта. Без
// него «сегодня безопасно» держалось бы на порядке вызовов, который в коде
// ничем не выражен, а сам прецедент (source.ConfigureCache) синхронизирован.
var (
	graphTunablesMu sync.RWMutex
	graphTunables   resolve.ObjectEdgeTunables
)

// ConfigureGraphTunables принимает пороги атрибуции в числах, а не типом
// internal/resolve: cmd/mcp1c не имеет права называть этот пакет
// (RuleCmdThroughApp). Ноль в любом параметре означает «умолчание».
func ConfigureGraphTunables(chainDepth, hubFanIn int, depthPenalty, hubPenalty float64) {
	graphTunablesMu.Lock()
	defer graphTunablesMu.Unlock()
	graphTunables = resolve.ObjectEdgeTunables{
		ChainDepth: chainDepth, HubFanIn: hubFanIn,
		DepthPenalty: depthPenalty, HubPenalty: hubPenalty,
	}
}

// currentGraphTunables читает пороги под той же блокировкой.
func currentGraphTunables() resolve.ObjectEdgeTunables {
	graphTunablesMu.RLock()
	defer graphTunablesMu.RUnlock()
	return graphTunables
}

// NewProjects builds the resolver. It never fails on an empty workspaceRoot;
// with a non-empty one it opens (or creates) the local registry, which is a
// read of a possibly-absent file — no directory is created until something is
// actually registered (workspace.OpenRegistry).
func NewProjects(workspaceRoot string, builtins *syntax.Index, idxCfg index.Config) (*Projects, error) {
	p := &Projects{
		workspaceRoot: strings.TrimSpace(workspaceRoot),
		builtins:      builtins,
		idxCfg:        idxCfg,
		opened:        map[domain.ProjectID]*openProject{},
	}
	if p.workspaceRoot == "" {
		return p, nil
	}
	reg, err := workspace.OpenRegistry(p.workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("реестр проектов %s: %w", p.workspaceRoot, err)
	}
	p.registry = reg
	return p, nil
}

// NewProjectsUnavailable строит Projects без реестра для сервера, у которого
// реестр в workspaceRoot не открылся: состояние активной выгрузки всё равно
// живёт здесь, а индексные инструменты отвечают no_active_project с причиной.
func NewProjectsUnavailable(workspaceRoot string, cause error) *Projects {
	return &Projects{
		workspaceRoot: strings.TrimSpace(workspaceRoot),
		registryErr:   cause,
		opened:        map[domain.ProjectID]*openProject{},
	}
}

// noActiveProjectError explains why there is no active project, distinguishing
// "no workspace configured at all" from "workspace configured, nothing active
// in it yet" — the hint differs.
func (p *Projects) noActiveProjectError() *Error {
	if p.registryErr != nil {
		return NewError(CodeNoActiveProject,
			"нет активного индексного проекта",
			fmt.Sprintf("реестр проектов не открылся (%v): индексные инструменты недоступны, пока реестр не исправлен и сервер не перезапущен", p.registryErr))
	}
	if p.workspaceRoot == "" {
		return NewError(CodeNoActiveProject,
			"нет активного индексного проекта",
			"сервер запущен без --projects-root: индексные инструменты недоступны, пока каталог workspace не задан")
	}
	return NewError(CodeNoActiveProject,
		"нет активного индексного проекта",
		"вызовите reindex с параметром projectRoot=<каталог с 1c-project.json> — это единственный сегодня production-путь зарегистрировать проект и сделать его активным; повторный reindex без projectRoot дальше работает как обычно")
}

// Active resolves the process's active project (activeproject.go: bound to
// the active dump, or picked by the start rule) into an open store+pipeline
// pair. Returns *Error{Code: CodeNoActiveProject} (as error) when none is
// selected — callers should let it propagate as-is, it is already actionable.
func (p *Projects) Active(ctx context.Context) (*openProject, error) {
	if p.registry == nil {
		return nil, p.noActiveProjectError()
	}
	id, dump, note := p.activeProjectID()
	if id == "" {
		if dump != "" {
			return nil, unboundDumpError(dump, note)
		}
		return nil, p.noActiveProjectError()
	}
	entry, ok := p.registry.Project(id)
	if !ok {
		return nil, p.noActiveProjectError()
	}
	return p.open(ctx, entry)
}

// ByRoot resolves the project registered at root into an open store+pipeline
// pair WITHOUT touching which project is active in the registry — a real
// one-off override (spec.md §8, D3): unlike index_status's project=, which
// only COMPARES against Active (cmd/mcp1c/idx_status.go), object_graph's
// project= (taск 11) actually has to read a DIFFERENT project's data while
// every other caller in the same process keeps seeing the usual active one.
// root has the same meaning as reindex's projectRoot — the directory holding
// 1c-project.json — and is resolved through the identical Abs+EvalSymlinks
// normalization EnsureProjectActive used when it stored the entry, so the
// same directory compares equal regardless of how it is spelled this time.
func (p *Projects) ByRoot(ctx context.Context, root string) (*openProject, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("project: корень не задан")
	}
	if p.registry == nil {
		return nil, p.noActiveProjectError()
	}
	real, canonErr := canonicalRoot(root)
	if canonErr != nil {
		return nil, NewError(CodeNotFound, fmt.Sprintf("проект с корнем %q не найден", root),
			fmt.Sprintf("%v; каталог должен существовать, зарегистрируйте проект через reindex(projectRoot=...)", canonErr))
	}
	id, ok := p.findByRoot(real)
	if !ok {
		return nil, NewError(CodeNotFound, fmt.Sprintf("проект с корнем %q не зарегистрирован", real),
			"зарегистрируйте его через reindex(projectRoot=...), затем повторите вызов с project=")
	}
	entry, ok := p.registry.Project(id)
	if !ok {
		return nil, NewError(CodeNotFound, fmt.Sprintf("проект %s пропал из реестра между поиском и открытием", id), "")
	}
	// open() кэширует по entry.ID и не трогает ActiveProject — активный
	// проект реестра остаётся тем, чем был до этого вызова.
	return p.open(ctx, entry)
}

// EnsureProjectActive registers a project rooted at projectRoot (the
// directory holding 1c-project.json) if the registry does not already know
// this root, and makes it the active project either way.
//
// This closes a real production gap: before this method existed, nothing
// under cmd/mcp1c ever called workspace.Registry.Upsert/SetActiveProject —
// those were reachable only from test helpers (impact_testsupport.go) — so a
// fresh user had no way to make any indexed tool work without hand-editing
// .mcp1c/registry.json. reindex's optional ProjectRoot input (cmd/mcp1c/idx_status.go)
// is the one caller today.
//
// Returns registered=true when this call actually added a new registry
// entry (as opposed to just re-activating one that was already there) — the
// caller uses that to pick mode=full over the usual incremental default,
// since an index for a brand-new registration cannot exist yet.
func (p *Projects) EnsureProjectActive(_ context.Context, projectRoot string) (registered bool, err error) {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return false, fmt.Errorf("projectRoot не задан")
	}
	if p.registry == nil {
		// workspaceRoot пуст — сервер запущен без --projects-root, регистрировать некуда.
		return false, p.noActiveProjectError()
	}

	// Путь безопасности (упрощение, названо явно): полноценный
	// workspace.SafeJoin проверяет rel-путь относительно уже существующего
	// корня, а projectRoot — независимый каталог выгрузки где угодно на
	// диске, тем же приёмом, что и workspace.RegisterTemporary для
	// set_dump (interfaces.md, «Из таска 02»). Вместо SafeJoin —
	// абсолютизация и раскрытие симлинков: несуществующий каталог отсеивается
	// здесь, отсутствие 1c-project.json — чуть ниже, в LoadManifest.
	real, canonErr := canonicalRoot(root)
	if canonErr != nil {
		return false, fmt.Errorf("каталог проекта %q: %w", root, canonErr)
	}

	if id, ok := p.findByRoot(real); ok {
		if err := p.registry.SetActiveProject(id); err != nil {
			return false, fmt.Errorf("активация проекта %s: %w", id, err)
		}
		if entry, ok := p.registry.Project(id); ok {
			p.activateInProcess(entry, nil)
		}
		return false, nil
	}

	// Манифест здесь не через p.open()/workspace.LoadManifest дважды подряд:
	// именно этот вызов решает, какой id присвоить новой записи registry —
	// сам open() (ниже) перечитает манифест ещё раз при первом Active, это
	// его собственная, отдельная обязанность (кэш по entry.ID).
	manifest, manifestErr := workspace.LoadManifest(real)
	if manifestErr != nil {
		return false, fmt.Errorf("регистрация проекта %s: манифест не прочитан: %w", real, manifestErr)
	}

	id := manifest.Project
	if id.Validate() != nil {
		id = workspace.SuggestProjectID(real)
	}

	entry := workspace.ProjectEntry{ID: id, Root: real, ManifestPath: manifest.Path}
	if err := p.registry.Upsert(entry); err != nil {
		return false, fmt.Errorf("регистрация проекта %s: %w", real, err)
	}
	if err := p.registry.SetActiveProject(id); err != nil {
		return false, fmt.Errorf("активация проекта %s: %w", id, err)
	}
	p.activateInProcess(entry, &manifest)
	return true, nil
}

// findByRoot ищет в registry запись с точно таким же (уже приведённым к
// canonical форме) корнем.
func (p *Projects) findByRoot(root string) (domain.ProjectID, bool) {
	for _, e := range p.registry.RecentProjects() {
		if equalRootPath(e.Root, root) {
			return e.ID, true
		}
	}
	return "", false
}

// equalRootPath сравнивает пути с учётом регистрозависимости платформы:
// windows и darwin (APFS по умолчанию) регистр не различают. Регистрозависимый
// том на маке при этом даст ложное совпадение путей, отличающихся только регистром;
// такие каталоги в одном реестре считаем невероятными.
func equalRootPath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// open returns the cached pair for entry.ID, opening it on first use.
func (p *Projects) open(_ context.Context, entry workspace.ProjectEntry) (*openProject, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if op, ok := p.opened[entry.ID]; ok {
		return op, nil
	}
	if p.closed {
		return nil, fmt.Errorf("проект %s: реестр проектов уже закрыт", entry.ID)
	}
	manifest, err := workspace.LoadManifest(entry.Root)
	if err != nil {
		return nil, fmt.Errorf("проект %s: манифест %s: %w", entry.ID, entry.Root, err)
	}
	st, err := store.Open(p.workspaceRoot, store.Options{
		ProjectID:    entry.ID,
		StateDirName: workspace.RegistryDirName,
	})
	if err != nil {
		return nil, fmt.Errorf("проект %s: хранилище индекса: %w", entry.ID, err)
	}
	svc := index.NewService(st, entry.ID, manifest, p.builtins, p.idxCfg)
	op := &openProject{Entry: entry, Manifest: manifest, Store: st, Service: svc}
	p.opened[entry.ID] = op
	// Прогрев свежести (ADR-036): обход диска стартует в фоне при открытии,
	// и первый вызов инструмента ждёт уже идущий обход, а не начинает свой.
	svc.WarmFreshness()
	return op, nil
}

// Close shuts down every project opened during this process's lifetime.
// Store/Service ownership passes to Projects once opened (interfaces.md:
// index.Service does not own its store, but whoever constructs the pair does)
// — nothing else in the server currently calls this; it exists for tests and
// for a future graceful-shutdown hook in cmd/mcp1c/main.go (out of this
// ticket's zone: main.go is untouched here).
func (p *Projects) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.warmWG.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	var firstErr error
	for id, op := range p.opened {
		if err := op.Service.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("проект %s: остановка пайплайна: %w", id, err)
		}
		if err := op.Store.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("проект %s: закрытие хранилища: %w", id, err)
		}
	}
	p.opened = map[domain.ProjectID]*openProject{}
	return firstErr
}
