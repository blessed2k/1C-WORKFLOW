package source

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// copyFixture makes a writable copy of a fixture so a test can change it.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

// TestCacheSeesEditedFile is the property the whole cache stands on: an export
// edited between two calls must be read again. A cache that keeps stale metadata
// is worse than no cache, because the answer looks authoritative.
func TestCacheSeesEditedFile(t *testing.T) {
	root := copyFixture(t, "testdata/vis")
	s := NewXMLSource(root)
	ctx := context.Background()

	before, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if len(before.FunctionalOptions) == 0 {
		t.Fatal("fixture must have functional options")
	}

	// Rewrite an option so that it no longer covers the document.
	path := filepath.Join(root, "FunctionalOptions", "ИспользоватьЗаказы.xml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read option: %v", err)
	}
	edited := []byte(string(data))
	edited = []byte(replaceAll(string(edited), "Document.ЗаказКлиента", "Document.ДругойДокумент"))
	// The stamp is second-grained on some filesystems: move the time explicitly.
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatalf("write option: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	after, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit after edit: %v", err)
	}
	for _, fo := range after.FunctionalOptions {
		if fo.Name == "ИспользоватьЗаказы" {
			t.Errorf("the edited option is still reported: the cache did not notice the change")
		}
	}
}

// TestCacheSeesAddedFile pins the other half of the fingerprint: a new file
// changes the count even when no existing file was touched.
func TestCacheSeesAddedFile(t *testing.T) {
	root := copyFixture(t, "testdata/vis")
	s := NewXMLSource(root)
	ctx := context.Background()

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	added := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:v8="http://v8.1c.ru/8.1/data/core" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.21">
	<FunctionalOption uuid="9e555555-0000-0000-0000-000000000009">
		<Properties>
			<Name>НоваяОпция</Name>
			<Location>Constant.НоваяОпция</Location>
			<Content><xr:Object>Document.ЗаказКлиента</xr:Object></Content>
		</Properties>
	</FunctionalOption>
</MetaDataObject>`
	if err := os.WriteFile(filepath.Join(root, "FunctionalOptions", "НоваяОпция.xml"), []byte(added), 0o644); err != nil {
		t.Fatalf("add option: %v", err)
	}

	after, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit after add: %v", err)
	}
	found := false
	for _, fo := range after.FunctionalOptions {
		if fo.Name == "НоваяОпция" {
			found = true
		}
	}
	if !found {
		t.Error("an option added after the first call was not picked up")
	}
}

// TestCacheConcurrent runs the tools that share cached collections at the same
// time; with -race this is what proves the shared values are only read.
func TestCacheConcurrent(t *testing.T) {
	s := NewXMLSource("testdata/vis")
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", true); err != nil {
				t.Errorf("VisibilityAudit: %v", err)
			}
			if _, err := s.WritePath(ctx, "Document", "ЗаказКлиента"); err != nil {
				t.Errorf("WritePath: %v", err)
			}
		}()
	}
	wg.Wait()
}

// replaceAll is strings.ReplaceAll spelled out, so the test file does not import
// strings for a single call.
func replaceAll(s, old, new string) string {
	out := ""
	for {
		i := indexOf(s, old)
		if i < 0 {
			return out + s
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// fakeClock is the cache's clock under test: TTL behaviour driven by hand, so
// that the test says what it means instead of sleeping and hoping.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// fakeTicker replaces the sweep tick, so a test can make the background sweeper
// run a pass now rather than in a minute.
type fakeTicker struct {
	ch chan time.Time
}

func (f *fakeTicker) newTicker(time.Duration) (<-chan time.Time, func()) {
	return f.ch, func() {}
}

// tick makes the sweeper run one pass and fails the test if nobody is listening.
func (f *fakeTicker) tick(t *testing.T) {
	t.Helper()
	select {
	case f.ch <- time.Time{}:
	case <-time.After(2 * time.Second):
		t.Fatal("no sweeper picked up the tick")
	}
}

// idle reports whether the sweeper ignores a tick, which is how a test sees that
// no goroutine is left running.
func (f *fakeTicker) noSweeper(t *testing.T) bool {
	t.Helper()
	select {
	case f.ch <- time.Time{}:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

// useTestCache swaps the process-wide cache for an empty one with a hand-driven
// clock and tick, and puts the real one back afterwards: cache tests must not
// see each other's entries, nor leave a sweeper behind.
func useTestCache(t *testing.T) (*fakeClock, *fakeTicker) {
	t.Helper()
	clock := &fakeClock{at: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)}
	ticker := &fakeTicker{ch: make(chan time.Time)}
	previous := exportCache
	c := newDumpCache()
	c.now = clock.now
	c.newTicker = ticker.newTicker
	exportCache = c
	t.Cleanup(func() {
		// Closing the tick channel ends any sweeper still parked on it.
		close(ticker.ch)
		exportCache = previous
	})
	return clock, ticker
}

// waitFor spins until cond holds; used to wait for the sweeper goroutine to
// finish a pass, never to let wall time pass for the cache itself.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// entryIdle returns the reported idle time of one cached collection.
func entryIdle(t *testing.T, stats CacheStats, kind string) time.Duration {
	t.Helper()
	for _, e := range stats.Entries {
		if e.Kind == kind {
			return e.Idle
		}
	}
	t.Fatalf("no %q entry in the snapshot", kind)
	return 0
}

// TestCacheHitRefreshesLastAccess pins R03: a collection that is being read is
// live, and the snapshot must say so. Without this, the sweeper would drop
// exactly the entries the session keeps using.
func TestCacheHitRefreshesLastAccess(t *testing.T) {
	clock, _ := useTestCache(t)
	s := NewXMLSource("testdata/vis")
	ctx := context.Background()

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	clock.advance(5 * time.Minute)
	if got := entryIdle(t, CacheSnapshot(), "options"); got != 5*time.Minute {
		t.Errorf("idle after 5m without a call = %v, want 5m", got)
	}

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit again: %v", err)
	}
	if got := entryIdle(t, CacheSnapshot(), "options"); got != 0 {
		t.Errorf("idle right after a cache hit = %v, want 0", got)
	}
}

// TestCacheExpiresIdleEntry pins R04: an entry nobody has asked for in longer
// than the TTL leaves the cache on its own, with no tool call involved. The
// clock is driven by hand — a sleeping test would either take minutes or flake.
func TestCacheExpiresIdleEntry(t *testing.T) {
	clock, ticker := useTestCache(t)
	ConfigureCache(2*time.Minute, 0)
	s := NewXMLSource("testdata/vis")

	if _, err := s.VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	before := CacheSnapshot()
	if len(before.Entries) == 0 {
		t.Fatal("the call left nothing in the cache")
	}
	if before.TotalBytes <= 0 {
		t.Errorf("total estimate = %d, want a positive number", before.TotalBytes)
	}

	clock.advance(3 * time.Minute)
	ticker.tick(t)

	waitFor(t, "the idle entries to be dropped", func() bool {
		return len(CacheSnapshot().Entries) == 0
	})
	if got := CacheSnapshot().TotalBytes; got != 0 {
		t.Errorf("total estimate after expiry = %d, want 0", got)
	}
}

// TestCacheAnswerSurvivesExpiry pins R06: after the cache has forgotten a
// collection, the next call re-reads the export from cold and must answer
// exactly as it did while the entry was warm. A cache that changes the answer by
// expiring is worse than no cache.
func TestCacheAnswerSurvivesExpiry(t *testing.T) {
	clock, ticker := useTestCache(t)
	ConfigureCache(time.Minute, 0)
	ctx := context.Background()

	vis := NewXMLSource("testdata/vis")
	rights := NewXMLSource("testdata/rights")

	visBefore, err := vis.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	rightsBefore, err := rights.RightsAudit(ctx, "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if len(CacheSnapshot().Entries) == 0 {
		t.Fatal("the calls left nothing in the cache")
	}

	clock.advance(2 * time.Minute)
	ticker.tick(t)
	waitFor(t, "the cache to empty", func() bool {
		return len(CacheSnapshot().Entries) == 0
	})

	visAfter, err := vis.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit after expiry: %v", err)
	}
	if !reflect.DeepEqual(visBefore, visAfter) {
		t.Errorf("visibility_audit answered differently after a cold re-read:\nbefore %+v\nafter  %+v", visBefore, visAfter)
	}
	rightsAfter, err := rights.RightsAudit(ctx, "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit after expiry: %v", err)
	}
	if !reflect.DeepEqual(rightsBefore, rightsAfter) {
		t.Errorf("rights_audit answered differently after a cold re-read:\nbefore %+v\nafter  %+v", rightsBefore, rightsAfter)
	}
}

// TestCacheExpiryOnMissingCollection covers half of R06.1: the collection
// directory can be gone by the time the entry expires. The cold re-read then
// behaves as it always did for a missing directory — an empty collection, not a
// panic — and the empty result is cached like any other.
func TestCacheExpiryOnMissingCollection(t *testing.T) {
	clock, ticker := useTestCache(t)
	ConfigureCache(time.Minute, 0)
	ctx := context.Background()

	root := copyFixture(t, "testdata/vis")
	s := NewXMLSource(root)
	before, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if len(before.FunctionalOptions) == 0 {
		t.Fatal("fixture must have functional options")
	}

	if err := os.RemoveAll(filepath.Join(root, "FunctionalOptions")); err != nil {
		t.Fatalf("remove collection: %v", err)
	}
	clock.advance(2 * time.Minute)
	ticker.tick(t)
	waitFor(t, "the cache to empty", func() bool {
		return len(CacheSnapshot().Entries) == 0
	})

	report, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit on a vanished collection: %v", err)
	}
	if len(report.FunctionalOptions) != 0 {
		t.Errorf("a vanished collection reported %d functional options, want none", len(report.FunctionalOptions))
	}
}

// entryBytes returns the size estimate the snapshot reports for one collection.
func entryBytes(t *testing.T, stats CacheStats, kind string) int64 {
	t.Helper()
	for _, e := range stats.Entries {
		if e.Kind == kind {
			return e.Bytes
		}
	}
	t.Fatalf("no %q entry in the snapshot", kind)
	return 0
}

// cachedKinds lists what the cache holds, in snapshot order.
func cachedKinds(stats CacheStats) []string {
	out := make([]string, 0, len(stats.Entries))
	for _, e := range stats.Entries {
		out = append(out, e.Kind)
	}
	return out
}

// TestCacheEvictsOldestOverLimit pins R08: what goes when the ceiling is
// exceeded is the collection nobody has asked for longest, not the one that just
// arrived — evicting the fresh entry would mean rebuilding it on the next call.
func TestCacheEvictsOldestOverLimit(t *testing.T) {
	clock, _ := useTestCache(t)
	ctx := context.Background()

	if _, err := NewXMLSource("testdata/vis").VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	options := entryBytes(t, CacheSnapshot(), "options")

	// A ceiling exactly the size of what is already cached: sitting at the limit
	// is not over it, so nothing may be dropped yet.
	ConfigureCache(0, options)
	if got := cachedKinds(CacheSnapshot()); len(got) != 1 || got[0] != "options" {
		t.Fatalf("cache at exactly the ceiling holds %v, want [options]", got)
	}

	clock.advance(time.Minute)
	if _, err := NewXMLSource("testdata/rights").RightsAudit(ctx, "Catalog", "Товары"); err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}

	stats := CacheSnapshot()
	if got := cachedKinds(stats); len(got) != 1 || got[0] != "roles" {
		t.Fatalf("after an insert over the ceiling the cache holds %v, want [roles]", got)
	}
	if stats.TotalBytes != entryBytes(t, stats, "roles") {
		t.Errorf("total = %d after eviction, want the size of the one surviving entry (%d)",
			stats.TotalBytes, entryBytes(t, stats, "roles"))
	}
	if stats.LimitBytes != options {
		t.Errorf("snapshot reports ceiling %d, want %d", stats.LimitBytes, options)
	}
}

// TestCacheKeepsEntryLargerThanLimit pins R08.1: a collection that is on its own
// bigger than the whole ceiling must not evict itself. Eviction stops at the
// last entry, otherwise every insert would clear the cache and cache nothing.
func TestCacheKeepsEntryLargerThanLimit(t *testing.T) {
	clock, _ := useTestCache(t)
	ctx := context.Background()
	ConfigureCache(0, 1) // one byte: nothing real ever fits

	if _, err := NewXMLSource("testdata/vis").VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	stats := CacheSnapshot()
	if got := cachedKinds(stats); len(got) != 1 || got[0] != "options" {
		t.Fatalf("an entry larger than the ceiling left %v, want it kept as [options]", got)
	}
	if stats.TotalBytes <= stats.LimitBytes {
		t.Fatalf("fixture is too small for this test: %d bytes fits under the %d byte ceiling",
			stats.TotalBytes, stats.LimitBytes)
	}

	clock.advance(time.Minute)
	if _, err := NewXMLSource("testdata/rights").RightsAudit(ctx, "Catalog", "Товары"); err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if got := cachedKinds(CacheSnapshot()); len(got) != 1 || got[0] != "roles" {
		t.Errorf("after a second oversized insert the cache holds %v, want the newest alone as [roles]", got)
	}
}

// TestCacheSizeEstimatedOnceAtInsert pins the cost of the estimate: it is walked
// when the value is built and never again. Re-walking a parsed collection on
// every tick would turn "deflate while idle" into permanent background work.
func TestCacheSizeEstimatedOnceAtInsert(t *testing.T) {
	_, ticker := useTestCache(t)
	var walks atomic.Int64
	measure := exportCache.sizeOf
	exportCache.sizeOf = func(v any) int64 {
		walks.Add(1)
		return measure(v)
	}
	ConfigureCache(10*time.Minute, 0)
	ctx := context.Background()
	s := NewXMLSource("testdata/vis")

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if got := walks.Load(); got != 1 {
		t.Fatalf("size walked %d times for one insert, want 1", got)
	}
	size := entryBytes(t, CacheSnapshot(), "options")

	// The channel is unbuffered, so the second tick is only taken once the first
	// sweep has returned: after this, two full sweeps have happened.
	ticker.tick(t)
	ticker.tick(t)
	if got := walks.Load(); got != 1 {
		t.Errorf("size walked %d times after two sweeps, want 1", got)
	}

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit again: %v", err)
	}
	if got := walks.Load(); got != 1 {
		t.Errorf("size walked %d times after a cache hit, want 1", got)
	}
	if got := entryBytes(t, CacheSnapshot(), "options"); got != size {
		t.Errorf("reported size changed from %d to %d without a rebuild", size, got)
	}
}

// TestCacheSweeperLifecycle pins R19 and R28i: the goroutine exists only while
// there is something to expire. An idle process has no sweeper at all, and the
// next insert has to bring one back.
func TestCacheSweeperLifecycle(t *testing.T) {
	clock, ticker := useTestCache(t)
	ConfigureCache(time.Minute, 0)
	if exportCache.sweeperRunning() {
		t.Fatal("an empty cache started a sweeper")
	}

	ctx := context.Background()
	s := NewXMLSource("testdata/vis")
	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if !exportCache.sweeperRunning() {
		t.Fatal("an insert into an empty cache did not start a sweeper")
	}

	clock.advance(2 * time.Minute)
	ticker.tick(t)
	waitFor(t, "the cache to empty", func() bool {
		return len(CacheSnapshot().Entries) == 0
	})
	waitFor(t, "the sweeper to exit", func() bool {
		return !exportCache.sweeperRunning()
	})
	if !ticker.noSweeper(t) {
		t.Error("a tick was still picked up after the cache emptied")
	}

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit after expiry: %v", err)
	}
	if !exportCache.sweeperRunning() {
		t.Fatal("a later insert did not start the sweeper again")
	}
	ticker.tick(t) // the restarted goroutine is the one listening now
}

// TestCacheExpiryLeavesHandedOutValueAlone pins R04.1: a call that already holds
// a cached collection keeps reading its own reference. Expiry unlinks the entry
// from the map and stops there.
//
// Both shapes the cache stores are checked, because they fail differently. The
// caller of a slice holds its own header, so only a write into the shared backing
// array reaches it — that is what the element-by-element comparison catches. The
// caller of a map holds the map itself, so clearing or refilling it to reuse the
// allocation is visible as well. Between them, an implementation that "cleans up"
// the value it drops cannot stay green.
func TestCacheExpiryLeavesHandedOutValueAlone(t *testing.T) {
	clock, ticker := useTestCache(t)
	ConfigureCache(time.Minute, 0)

	options := NewXMLSource("testdata/vis").cachedFunctionalOptions()
	if len(options) == 0 {
		t.Fatal("fixture must have functional options")
	}
	names := make([]string, 0, len(options))
	for _, o := range options {
		names = append(names, o.name)
	}

	roles := NewXMLSource("testdata/rights").cachedRoleRights()
	if len(roles) == 0 {
		t.Fatal("fixture must have roles")
	}
	roleCount := len(roles)
	const knownRole = "ПолныеПрава"
	if _, ok := roles[knownRole]; !ok {
		t.Fatalf("fixture must have the %s role", knownRole)
	}

	clock.advance(2 * time.Minute)
	ticker.tick(t)
	waitFor(t, "the cache to empty", func() bool {
		return len(CacheSnapshot().Entries) == 0
	})

	if len(options) != len(names) {
		t.Fatalf("the handed-out slice has %d options after expiry, had %d", len(options), len(names))
	}
	for i, o := range options {
		if o.name != names[i] {
			t.Errorf("option %d changed under the caller: %q became %q", i, names[i], o.name)
		}
	}
	if len(roles) != roleCount {
		t.Errorf("the handed-out map has %d roles after expiry, had %d", len(roles), roleCount)
	}
	if _, ok := roles[knownRole]; !ok {
		t.Errorf("the %s role vanished from the map the caller is still reading", knownRole)
	}
}

// TestCacheExpiryOnVanishedExportRoot covers the other half of R06.1: the whole
// export root, not just one collection, can be gone when the entry expires.
//
// The expectation is not "an empty report": it is whatever the server did before
// this change, measured on the code without it (a detached worktree at HEAD, the
// same warm-then-remove sequence). That run answered: no report, the *fs.PathError
// of the object file the tool opens first, fs.ErrNotExist, no panic — and the
// same error, word for word, from a source that had never cached anything. Expiry
// must put the process back exactly into that cold state.
func TestCacheExpiryOnVanishedExportRoot(t *testing.T) {
	clock, ticker := useTestCache(t)
	ConfigureCache(time.Minute, 0)
	ctx := context.Background()

	root := copyFixture(t, "testdata/vis")
	s := NewXMLSource(root)
	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove export root: %v", err)
	}

	clock.advance(2 * time.Minute)
	ticker.tick(t)
	waitFor(t, "the cache to empty", func() bool {
		return len(CacheSnapshot().Entries) == 0
	})

	// A panic here fails the test on its own; the assertions below are about the
	// error being the same one as before the cache learned to forget.
	report, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if report != nil {
		t.Errorf("a vanished export root produced a report %+v, want none", report)
	}
	if err == nil {
		t.Fatal("a vanished export root produced no error; before this change it returned one")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %v does not report a missing file, unlike the error before this change", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("error %v is not a *fs.PathError, unlike the error before this change", err)
	}
	wantPath := filepath.Join(root, "Documents", "ЗаказКлиента.xml")
	if pathErr.Op != "open" || pathErr.Path != wantPath {
		t.Errorf("error is %s %q, want open %q — the object file the tool opens first", pathErr.Op, pathErr.Path, wantPath)
	}

	// The same call from a source that never cached anything: the two must not
	// diverge, or "cold start after expiry" would be a different code path.
	_, coldErr := NewXMLSource(root).VisibilityAudit(ctx, "Document", "ЗаказКлиента", false)
	if coldErr == nil || coldErr.Error() != err.Error() {
		t.Errorf("after expiry the answer is %v, a never-warmed source answers %v", err, coldErr)
	}
}

// subsOfSize builds a collection of the shape the cache really stores: count
// subscriptions whose three text fields hold textBytes bytes each. The expected
// numbers below are read off this shape, never off estimateSize itself.
func subsOfSize(count, textBytes int) []parsedSubscription {
	text := string(make([]byte, textBytes))
	out := make([]parsedSubscription, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, parsedSubscription{name: text, event: text, handler: text})
	}
	return out
}

// TestEstimateSizeCountsBytesOfTheValue is what the ceiling stands on: the
// estimate has to be a number of bytes that follows the value, not a constant
// and not a count of elements. R07 and R08 are only worth something if this
// number moves with the data.
//
// The bounds come from the shape of the value: three text fields of a known
// length per subscription, plus generous room for headers and padding. One
// number cannot satisfy both rows, and neither can a count.
func TestEstimateSizeCountsBytesOfTheValue(t *testing.T) {
	const (
		textBytes  = 1000 // bytes in every text field of one subscription
		textFields = 3    // name, event, handler
		perElement = 256  // room for string headers, the source field, padding
		perValue   = 1024 // room for the slice header and the interface box
	)
	for _, count := range []int{1, 64} {
		payload := int64(count * textFields * textBytes)
		got := estimateSize(subsOfSize(count, textBytes))
		if got < payload {
			t.Errorf("%d subscriptions carrying %d bytes of text estimated at %d: the text is not counted",
				count, payload, got)
		}
		if ceiling := payload + int64(count)*perElement + perValue; got > ceiling {
			t.Errorf("%d subscriptions carrying %d bytes of text estimated at %d, above the %d the shape allows",
				count, payload, got, ceiling)
		}
	}
}

// deepNode is a chain deeper than the walk goes, and a cycle when it is closed.
type deepNode struct {
	text string
	next *deepNode
}

// TestEstimateSizeStopsAtWalkDepth pins the honest limit of the estimate: the
// walk is depth-bounded, so a value nested deeper than sizeWalkDepth is
// under-counted rather than walked forever. That is the price of the bound, and
// a cycle — which no parsed collection has today, and nothing prevents tomorrow
// — must return a number instead of eating the stack.
func TestEstimateSizeStopsAtWalkDepth(t *testing.T) {
	const (
		nodes     = 100
		textBytes = 1000
	)
	text := string(make([]byte, textBytes))
	head := &deepNode{text: text}
	last := head
	for i := 1; i < nodes; i++ {
		last.next = &deepNode{text: text}
		last = last.next
	}
	full := int64(nodes * textBytes)

	chain := estimateSize(head)
	if chain <= 0 {
		t.Fatalf("a chain of %d nodes estimated at %d, want a positive number", nodes, chain)
	}
	if chain >= full {
		t.Errorf("a chain of %d nodes estimated at %d: the walk is expected to stop at depth %d and report less than the %d bytes held",
			nodes, chain, sizeWalkDepth, full)
	}

	// Closing the ring: without the depth bound this never returns.
	last.next = head
	if cyclic := estimateSize(head); cyclic != chain {
		t.Errorf("a cyclic value estimated at %d, the same value estimated at %d before the ring was closed",
			cyclic, chain)
	}
}

// TestCacheReadPastTTLRebuilds pins the read side of the TTL: an entry idle for
// longer than the TTL is a miss, even when the sweeper has not come round yet.
// The sweeper wakes once per sweepPeriod (TTL/4, never oftener than a minute),
// so without this a read in between would be answered from an entry the TTL had
// already retired, and the configured TTL would be a floor rather than a promise.
func TestCacheReadPastTTLRebuilds(t *testing.T) {
	clock, _ := useTestCache(t)
	var inserts atomic.Int64
	measure := exportCache.sizeOf
	exportCache.sizeOf = func(v any) int64 {
		inserts.Add(1)
		return measure(v)
	}
	ConfigureCache(2*time.Minute, 0)
	ctx := context.Background()
	s := NewXMLSource("testdata/vis")

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if got := inserts.Load(); got != 1 {
		t.Fatalf("the first call built %d collections, want 1", got)
	}

	// Past the TTL, and deliberately without a tick: the entry is stale but the
	// sweeper has not seen it, which is exactly the window this test is about.
	clock.advance(3 * time.Minute)
	if got := len(CacheSnapshot().Entries); got != 1 {
		t.Fatalf("the cache holds %d entries before the read, want the stale one still there", got)
	}

	if _, err := s.VisibilityAudit(ctx, "Document", "ЗаказКлиента", false); err != nil {
		t.Fatalf("VisibilityAudit past the TTL: %v", err)
	}
	if got := inserts.Load(); got != 2 {
		t.Errorf("%d collections built in total, want 2: the read past the TTL must rebuild, not hit", got)
	}
	if got := entryIdle(t, CacheSnapshot(), "options"); got != 0 {
		t.Errorf("idle after the rebuild = %v, want 0", got)
	}
}
