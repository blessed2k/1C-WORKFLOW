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
// TTL/MaxAge свежести, полный reindex уже сделан.
func snapshotFixture(t *testing.T, id string) (*Projects, *openProject, *snapshotClock) {
	t.Helper()
	clock := &snapshotClock{now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	p, op := newBSLFixtureProjectCfg(t, domain.ProjectID(id), index.Config{
		Now:             clock.Now,
		FreshnessTTL:    snapshotTestTTL,
		FreshnessMaxAge: snapshotTestMaxAge,
	})
	return p, op, clock
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
// из прошлой проверки без ожидания, а обход диска уходит в фон; следующий
// вызов после его завершения видит правку.
func TestSnapshotRefreshesInBackgroundAfterTTL(t *testing.T) {
	p, op, clock := snapshotFixture(t, "snap-bg")
	probes := snapshotProbes(t, p)
	findSymbol := probes[0]

	editFixtureModule(t, op)
	clock.advance(snapshotTestTTL + time.Second)

	ctx := context.Background()
	stale, _, err := findSymbol.call(ctx)
	if err != nil {
		t.Fatalf("find_symbol: %v", err)
	}
	if stale {
		t.Fatalf("первый вызов после TTL обязан ответить из прошлой проверки (stale=false), фоновая ещё не могла закончиться до ответа")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stale, warned, err := findSymbol.call(ctx)
		if err != nil {
			t.Fatalf("find_symbol: %v", err)
		}
		if stale && warned {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("фоновая проверка за 5 с так и не отметила правку: stale=%v warned=%v", stale, warned)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
