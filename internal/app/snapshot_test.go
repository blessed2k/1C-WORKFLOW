package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
)

// snapshotClock: подменяемые часы index.Service: TTL свежести проверяется
// сдвигом времени, а не time.Sleep (то же правило, что у кэша выгрузки).
type snapshotClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *snapshotClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *snapshotClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

const (
	snapshotTestTTL    = 30 * time.Second
	snapshotTestMaxAge = 5 * time.Minute
)

// snapshotFixture: проект bslFixtureFiles с подменёнными часами и явными
// TTL/MaxAge свежести, полный reindex уже сделан. checks получает момент
// каждого завершённого обхода диска (index.Config.OnDiskCheck): сигнал
// вместо опроса через time.Sleep.
func snapshotFixture(t *testing.T, id string) (*Projects, *openProject, *snapshotClock) {
	t.Helper()
	p, op, clock, _ := snapshotFixtureChecks(t, id)
	return p, op, clock
}

func snapshotFixtureChecks(t *testing.T, id string) (*Projects, *openProject, *snapshotClock, <-chan time.Time) {
	t.Helper()
	clock := &snapshotClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	checks := make(chan time.Time, 64)
	p, op := newBSLFixtureProjectCfg(t, domain.ProjectID(id), snapshotConfig(clock, checks))
	return p, op, clock, checks
}

// snapshotConfig: tunables index.Service тестов снимка; checks, если не nil,
// получает момент каждого завершённого обхода (неблокирующая отправка).
func snapshotConfig(clock *snapshotClock, checks chan time.Time) index.Config {
	cfg := index.Config{
		Now:             clock.Now,
		FreshnessTTL:    snapshotTestTTL,
		FreshnessMaxAge: snapshotTestMaxAge,
	}
	if checks != nil {
		cfg.OnDiskCheck = func(at time.Time) {
			select {
			case checks <- at:
			default:
			}
		}
	}
	return cfg
}

// waitDiskCheckSince ждёт обход, снятый не раньше since (прогрев при
// открытии проекта шлёт свои, более ранние, сигналы).
func waitDiskCheckSince(t *testing.T, checks <-chan time.Time, since time.Time) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case at := <-checks:
			if !at.Before(since) {
				return
			}
		case <-deadline:
			t.Fatalf("за 5 с не пришёл обход диска, снятый не раньше %s", since)
		}
	}
}

// editFixtureModule правит модуль фикстуры на диске мимо reindex: размер
// меняется, так что precheck видит правку независимо от разрешения mtime.
func editFixtureModule(t *testing.T, op *openProject) {
	t.Helper()
	comp, _ := op.Manifest.Component("cfg")
	abs := filepath.Join(comp.AbsRoot, filepath.FromSlash(fixtureModulePath))
	body := "\nФункция Помощь() Экспорт\n\tВозврат \"правка на диске после индексации\";\nКонецФункции\n"
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// staleProbe: один вызов индексного инструмента: признак stale ответа и
// есть ли в нём предупреждение stale_index.
type staleProbe struct {
	name string
	call func(ctx context.Context) (stale bool, warned bool, err error)
}

// snapshotProbes: три инструмента критерия issue #2: find_symbol,
// get_object, find_references. uid берётся из find_symbol до правки.
func snapshotProbes(t *testing.T, p *Projects) []staleProbe {
	t.Helper()
	ctx := context.Background()
	symSvc := NewSymbolService(p)
	found, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("FindSymbol setup: items=%+v err=%v", found.Items, err)
	}
	uid := found.Items[0].UID
	metaSvc := NewMetadataService(p)
	graphSvc := NewGraphService(p)
	return []staleProbe{
		{"find_symbol", func(ctx context.Context) (bool, bool, error) {
			r, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
			return r.Stale, hasWarning(r.Warnings, "stale_index"), err
		}},
		{"get_object", func(ctx context.Context) (bool, bool, error) {
			r, err := metaSvc.GetObject(ctx, GetObjectInput{Type: "Catalog", Name: "Товары"})
			return r.Stale, hasWarning(r.Warnings, "stale_index"), err
		}},
		{"find_references", func(ctx context.Context) (bool, bool, error) {
			r, err := graphSvc.FindReferences(ctx, FindReferencesInput{UID: uid})
			return r.Stale, hasWarning(r.Warnings, "stale_index"), err
		}},
	}
}

func expectStale(t *testing.T, probes []staleProbe, want bool, when string) {
	t.Helper()
	ctx := context.Background()
	for _, pr := range probes {
		stale, warned, err := pr.call(ctx)
		if err != nil {
			t.Fatalf("%s %s: %v", pr.name, when, err)
		}
		if stale != want || warned != want {
			t.Errorf("%s %s: stale=%v, warning stale_index=%v, want оба %v", pr.name, when, stale, warned, want)
		}
	}
}

// TestSnapshotStaleAfterDiskEdit: критерий issue #2: правка файла выгрузки
// после индексации даёт stale:true у find_symbol, get_object и
// find_references. Проверка по возрасту кэша выше MaxAge идёт синхронно,
// поэтому исход детерминирован.
func TestSnapshotStaleAfterDiskEdit(t *testing.T) {
	p, op, clock := snapshotFixture(t, "snap-stale")
	probes := snapshotProbes(t, p)

	expectStale(t, probes, false, "сразу после reindex")

	editFixtureModule(t, op)
	clock.advance(snapshotTestMaxAge + time.Second)

	expectStale(t, probes, true, "после правки файла на диске")
}

// TestSnapshotCachedWithinTTL: контракт TTL: в пределах TTL обход диска не
// повторяется, ответ опирается на прошлую проверку. Правка, сделанная внутри
// окна, видна только после его истечения. Это и есть цена бюджета 20 мс,
// она названа в ADR-036.
func TestSnapshotCachedWithinTTL(t *testing.T) {
	p, op, clock := snapshotFixture(t, "snap-ttl")
	probes := snapshotProbes(t, p)

	editFixtureModule(t, op)
	clock.advance(snapshotTestTTL / 2)
	expectStale(t, probes, false, "внутри TTL после правки")

	clock.advance(snapshotTestMaxAge)
	expectStale(t, probes, true, "после истечения MaxAge")
}

// TestSnapshotRefreshesInBackgroundAfterTTL: между TTL и MaxAge ответ идёт
// из прошлой проверки без ожидания, а обход диска уходит в фон; вызов после
// его завершения (сигнал OnDiskCheck) видит правку.
func TestSnapshotRefreshesInBackgroundAfterTTL(t *testing.T) {
	p, op, clock, checks := snapshotFixtureChecks(t, "snap-bg")
	probes := snapshotProbes(t, p)
	findSymbol := probes[0]

	editFixtureModule(t, op)
	clock.advance(snapshotTestTTL + time.Second)
	triggered := clock.Now()

	ctx := context.Background()
	stale, _, err := findSymbol.call(ctx)
	if err != nil {
		t.Fatalf("find_symbol: %v", err)
	}
	if stale {
		t.Fatalf("первый вызов после TTL обязан ответить из прошлой проверки (stale=false)")
	}

	waitDiskCheckSince(t, checks, triggered)
	expectStale(t, probes, true, "после фонового обхода")
}

// TestSnapshotFreshAfterReindex: reindex сам сверяет корпус с диском, после
// него прошлый исход проверки недействителен: stale снимается без ожидания
// TTL.
func TestSnapshotFreshAfterReindex(t *testing.T) {
	p, op, clock := snapshotFixture(t, "snap-reindex")
	probes := snapshotProbes(t, p)

	editFixtureModule(t, op)
	clock.advance(snapshotTestMaxAge + time.Second)
	expectStale(t, probes, true, "после правки")

	if _, err := op.Service.Reindex(context.Background(), index.ModeIncremental, ""); err != nil {
		t.Fatalf("Reindex(incremental): %v", err)
	}
	expectStale(t, probes, false, "после инкремента")
}

// TestIndexStatusStaleLikeFindSymbol: index_status видит правку на диске так
// же, как find_symbol: тот же признак stale и тот же код stale_index.
func TestIndexStatusStaleLikeFindSymbol(t *testing.T) {
	p, op, clock := snapshotFixture(t, "snap-status")
	probes := snapshotProbes(t, p)
	statusSvc := NewIndexStatusService(p)
	indexStatus := staleProbe{"index_status", func(ctx context.Context) (bool, bool, error) {
		r, err := statusSvc.Status(ctx, StatusInput{})
		return r.Stale, hasWarning(r.Warnings, "stale_index"), err
	}}
	both := []staleProbe{probes[0], indexStatus}

	expectStale(t, both, false, "сразу после reindex")
	editFixtureModule(t, op)
	clock.advance(snapshotTestMaxAge + time.Second)
	expectStale(t, both, true, "после правки файла на диске")
}

// TestSnapshotInvalidatedByComponentReindex: reindex одного компонента
// ничего не знает об остальных и прошлый исход обесценивает. Правка в cfg,
// reindex только ext: без сброса исход внутри TTL сказал бы «свежо».
func TestSnapshotInvalidatedByComponentReindex(t *testing.T) {
	clock := &snapshotClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	p, op := newExtensionFixtureProjectCfg(t, "snap-comp", oneExtension(), snapshotConfig(clock, nil))
	symSvc := NewSymbolService(p)
	ctx := context.Background()
	findSymbol := staleProbe{"find_symbol", func(ctx context.Context) (bool, bool, error) {
		r, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Рассчитать"})
		return r.Stale, hasWarning(r.Warnings, "stale_index"), err
	}}
	expectStale(t, []staleProbe{findSymbol}, false, "сразу после reindex")

	comp, _ := op.Manifest.Component("cfg")
	writeFile(t, filepath.Join(comp.AbsRoot, filepath.FromSlash(catalogManagerModulePath)),
		"\nФункция Рассчитать() Экспорт\n\tВозврат 100500;\nКонецФункции\n")
	clock.advance(snapshotTestTTL / 2)
	expectStale(t, []staleProbe{findSymbol}, false, "внутри TTL, до reindex")

	if _, err := op.Service.Reindex(ctx, index.ModeIncremental, "ext"); err != nil {
		t.Fatalf("Reindex(ext): %v", err)
	}
	expectStale(t, []staleProbe{findSymbol}, true, "после reindex только ext")
}

// TestSnapshotInvalidatedByFailedReindex: упавший reindex обесценивает
// прошлый исход, следующий вызов ждёт новый обход и видит правку.
func TestSnapshotInvalidatedByFailedReindex(t *testing.T) {
	p, op, clock := snapshotFixture(t, "snap-failed")
	probes := snapshotProbes(t, p)

	editFixtureModule(t, op)
	clock.advance(snapshotTestTTL / 2)
	expectStale(t, probes, false, "внутри TTL, до упавшего reindex")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := op.Service.Reindex(canceled, index.ModeIncremental, ""); err == nil {
		t.Fatalf("Reindex с отменённым контекстом прошёл, а должен упасть")
	}
	expectStale(t, probes, true, "после упавшего reindex")
}

// TestSnapshotFreshnessCheckFailed: проверить свежесть не удалось (сервис
// индекса закрыт, store ещё читается): ответ есть, но stale:true и
// freshness_check_failed, а не «свежо».
func TestSnapshotFreshnessCheckFailed(t *testing.T) {
	p, op, _ := snapshotFixture(t, "snap-failcheck")
	symSvc := NewSymbolService(p)
	if err := op.Service.Close(); err != nil {
		t.Fatalf("Service.Close: %v", err)
	}
	r, err := symSvc.FindSymbol(context.Background(), FindSymbolInput{Name: "Помощь"})
	if err != nil {
		t.Fatalf("FindSymbol: %v", err)
	}
	if len(r.Items) != 1 {
		t.Fatalf("Items = %+v, want ответ из индекса несмотря на отказ проверки", r.Items)
	}
	if !r.Stale || !hasWarning(r.Warnings, "freshness_check_failed") {
		t.Fatalf("stale=%v warnings=%+v, want stale:true и freshness_check_failed", r.Stale, r.Warnings)
	}
}

// TestProjectOpenWarmsFreshness: открытие проекта само запускает обход
// диска, до первого вызова инструмента.
func TestProjectOpenWarmsFreshness(t *testing.T) {
	_, _, _, checks := snapshotFixtureChecks(t, "snap-warm")
	waitDiskCheckSince(t, checks, time.Time{})
}
