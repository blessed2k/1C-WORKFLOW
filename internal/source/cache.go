package source

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"
)

// The export does not change while a request runs, and rarely between requests:
// it is a directory of files that a developer re-exports now and then. Every
// tool of this server, however, re-reads and re-parses the same directories on
// every call — 1087 role files for rights, 308 subscriptions for the write path,
// 560 functional options for visibility — which is where the seconds go and why
// a sweep over the whole configuration is impossible.
//
// This cache keeps the parsed collections per export directory and rebuilds one
// when the directory changes. It is process-wide on purpose: XMLSource is
// recreated per call (dumpState.source), so a cache living inside it would never
// be reused.
//
// It is also not eternal: a collection nobody has asked for in a while is dead
// weight in a process that outlives the session that warmed it. Idle entries are
// dropped by a lazy sweeper (ConfigureCache), and the sum of their size
// estimates is kept under a ceiling.
var exportCache = newDumpCache()

// newDumpCache builds the cache with expiry and eviction switched off. Until the
// process configures them, the cache behaves exactly as it did before it learned
// to forget: the defaults belong to the command line, not here.
func newDumpCache() *dumpCache {
	return &dumpCache{
		entries:   map[string]*cacheEntry{},
		now:       time.Now,
		newTicker: realTicker,
		sizeOf:    estimateSize,
	}
}

type dumpCache struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry
	// total is the sum of the entries' size estimates, maintained incrementally:
	// re-adding it up on every tick would turn "deflate while idle" into constant
	// work, which is the opposite of the point.
	total int64

	ttl   time.Duration
	limit int64

	// Time and the sweep tick are injected so that a test can drive both by hand.
	// A TTL test written around time.Sleep is either slow or flaky, and in the
	// process these are the real thing, at no cost.
	now       func() time.Time
	newTicker func(time.Duration) (<-chan time.Time, func())
	sizeOf    func(any) int64

	// sweeping says whether a sweeper goroutine is alive. It is cleared under the
	// same lock that finds the cache empty, so an insert either sees a live
	// sweeper or is the one that starts the next one — never both, never neither.
	sweeping bool
}

type cacheEntry struct {
	root       string
	kind       string
	stamp      stamp
	value      any
	bytes      int64
	lastAccess time.Time
}

// CacheStats is an immutable summary of the export cache: numbers about the
// cached values, never the values themselves.
type CacheStats struct {
	TTL        time.Duration
	LimitBytes int64
	TotalBytes int64
	Entries    []CacheEntryStat
}

// CacheEntryStat describes one cached collection.
type CacheEntryStat struct {
	Root  string        // export root the entry belongs to
	Kind  string        // collection kind: subscriptions, options, roles
	Bytes int64         // estimated size
	Idle  time.Duration // since last access
}

// ConfigureCache sets the process-wide idle TTL and memory ceiling of the export
// cache. A non-positive ttl disables expiry; a non-positive limit disables
// eviction. Both are process-wide by design: the key of an entry (export root
// plus collection kind) has no say in them.
func ConfigureCache(ttl time.Duration, limitBytes int64) {
	c := exportCache
	c.mu.Lock()
	c.ttl = ttl
	c.limit = limitBytes
	c.cleanLocked()
	start, period := c.ensureSweeperLocked()
	c.mu.Unlock()
	if start {
		go c.sweepLoop(period)
	}
}

// CacheSnapshot returns an immutable summary of the export cache. Entries come
// out in a stable order: the summary is read by an agent, and a list that
// reshuffles itself between two calls reads as a change that never happened.
func CacheSnapshot() CacheStats {
	c := exportCache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	out := CacheStats{
		TTL:        c.ttl,
		LimitBytes: c.limit,
		TotalBytes: c.total,
		Entries:    make([]CacheEntryStat, 0, len(c.entries)),
	}
	for _, e := range c.entries {
		out.Entries = append(out.Entries, CacheEntryStat{
			Root:  e.root,
			Kind:  e.kind,
			Bytes: e.bytes,
			Idle:  now.Sub(e.lastAccess),
		})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Root != out.Entries[j].Root {
			return out.Entries[i].Root < out.Entries[j].Root
		}
		return out.Entries[i].Kind < out.Entries[j].Kind
	})
	return out
}

// putLocked stores an entry and keeps the total in step with it.
func (c *dumpCache) putLocked(key string, e *cacheEntry) {
	if old, ok := c.entries[key]; ok {
		c.total -= old.bytes
	}
	c.entries[key] = e
	c.total += e.bytes
}

// idleTooLong is the TTL deadline itself, asked as of now. The sweeper and the
// read path both go through it, so the two can never disagree about when an
// entry is past its time.
func (c *dumpCache) idleTooLong(e *cacheEntry, now time.Time) bool {
	return c.ttl > 0 && now.Sub(e.lastAccess) > c.ttl
}

// expireLocked drops every entry nobody has asked for in longer than the TTL.
func (c *dumpCache) expireLocked() {
	if c.ttl <= 0 {
		return
	}
	now := c.now()
	for key, e := range c.entries {
		if c.idleTooLong(e, now) {
			c.total -= e.bytes
			delete(c.entries, key)
		}
	}
}

// cleanLocked is the one cleanup of the map, and every writer goes through it:
// first everything past its TTL, then, if the total is still over the ceiling,
// the entries nobody has touched for longest. Expiry and eviction share the lock
// and the pass on purpose — two independent cleanups over one map are two races
// and two different answers to "what is left".
func (c *dumpCache) cleanLocked() {
	c.expireLocked()
	c.evictLocked()
}

// evictLocked brings the total back under the ceiling by dropping the entries
// nobody has touched for longest. It always leaves one entry standing: an entry
// larger than the whole ceiling would otherwise evict the cache down to nothing
// and then evict itself, once per insert, forever.
func (c *dumpCache) evictLocked() {
	if c.limit <= 0 {
		return
	}
	for c.total > c.limit && len(c.entries) > 1 {
		var oldestKey string
		var oldest *cacheEntry
		for key, e := range c.entries {
			if oldest == nil || e.lastAccess.Before(oldest.lastAccess) {
				oldestKey, oldest = key, e
			}
		}
		c.total -= oldest.bytes
		delete(c.entries, oldestKey)
	}
}

// ensureSweeperLocked claims the right to start a sweeper and reports the period
// it should run at. Expiry is the only reason to have a goroutine at all: the
// ceiling is enforced on insert, where the growth happens.
func (c *dumpCache) ensureSweeperLocked() (start bool, period time.Duration) {
	if c.sweeping || c.ttl <= 0 || len(c.entries) == 0 {
		return false, 0
	}
	c.sweeping = true
	return true, c.sweepPeriod()
}

// sweepPeriod checks four times per TTL, but never more than once a minute:
// finding nothing to drop is still work, and the cost of noticing an idle entry
// a minute late is a minute of memory.
func (c *dumpCache) sweepPeriod() time.Duration {
	period := c.ttl / 4
	if period < time.Minute {
		period = time.Minute
	}
	return period
}

// sweepLoop is the whole background story: it exists only while there is
// something to expire and returns as soon as the cache is empty, so an idle
// process has no goroutine at all rather than one that wakes up to do nothing.
//
// The period is fixed when the sweeper starts. A TTL changed under a running
// sweeper therefore takes full effect from the next start; entries still expire
// on the correct deadline, at most one old period late.
func (c *dumpCache) sweepLoop(period time.Duration) {
	tick, stop := c.newTicker(period)
	defer stop()
	for range tick {
		if !c.sweep() {
			return
		}
	}
	c.mu.Lock()
	c.sweeping = false
	c.mu.Unlock()
}

// sweep runs one pass and reports whether the sweeper should stay alive.
func (c *dumpCache) sweep() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanLocked()
	if len(c.entries) == 0 || c.ttl <= 0 {
		c.sweeping = false
		return false
	}
	return true
}

// sweeperRunning reports whether a sweeper goroutine is alive.
func (c *dumpCache) sweeperRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sweeping
}

// realTicker is the production tick source.
func realTicker(period time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(period)
	return t.C, t.Stop
}

// estimateSize approximates how much memory a cached value holds. An exact size
// of an `any` is not available without walking the heap, and the number is only
// ever compared with the ceiling, so a depth-bounded walk counting headers,
// string bytes and elements is enough — and it is paid once, at insert.
func estimateSize(v any) int64 {
	if v == nil {
		return 0
	}
	return sizeOfValue(reflect.ValueOf(v), sizeWalkDepth)
}

// sizeWalkDepth also serves as the cycle guard: a parsed collection is a tree,
// but nothing stops a future value from pointing back at itself.
const sizeWalkDepth = 12

func sizeOfValue(v reflect.Value, depth int) int64 {
	if !v.IsValid() {
		return 0
	}
	return int64(v.Type().Size()) + indirectSize(v, depth)
}

// indirectSize counts what a value owns beyond its own flat footprint, which its
// type size already covers.
func indirectSize(v reflect.Value, depth int) int64 {
	if depth <= 0 || !v.IsValid() {
		return 0
	}
	switch v.Kind() {
	case reflect.String:
		return int64(v.Len())
	case reflect.Slice:
		if v.IsNil() {
			return 0
		}
		size := int64(v.Len()) * int64(v.Type().Elem().Size())
		for i := 0; i < v.Len(); i++ {
			size += indirectSize(v.Index(i), depth-1)
		}
		return size
	case reflect.Array:
		var size int64
		for i := 0; i < v.Len(); i++ {
			size += indirectSize(v.Index(i), depth-1)
		}
		return size
	case reflect.Map:
		if v.IsNil() {
			return 0
		}
		pair := int64(v.Type().Key().Size()) + int64(v.Type().Elem().Size())
		size := int64(v.Len()) * pair
		iter := v.MapRange()
		for iter.Next() {
			size += indirectSize(iter.Key(), depth-1) + indirectSize(iter.Value(), depth-1)
		}
		return size
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return 0
		}
		return sizeOfValue(v.Elem(), depth-1)
	case reflect.Struct:
		var size int64
		for i := 0; i < v.NumField(); i++ {
			size += indirectSize(v.Field(i), depth-1)
		}
		return size
	}
	return 0
}

// stamp fingerprints a directory: the number of entries and the newest
// modification time among them. A changed file body moves its own mtime, and an
// added or removed file moves the count, so the pair catches both. Reading the
// stamp costs one ReadDir; rebuilding costs reading and parsing every file.
type stamp struct {
	count  int
	newest time.Time
}

// dirStamp returns the fingerprint of dir. A missing directory has the zero
// stamp, which is stable, so "no such directory" is cached like anything else.
func dirStamp(dir string) stamp {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return stamp{}
	}
	out := stamp{count: len(entries)}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if t := info.ModTime(); t.After(out.newest) {
			out.newest = t
		}
	}
	return out
}

// cached returns the value for kind under root, building it with build when the
// directory has changed since the last call. build must be pure: it may run
// concurrently with readers of other kinds, and it must not mutate the value it
// returns afterwards, because every caller shares it.
//
// A dropped entry is only unlinked from the map, never touched: a call that has
// already been handed a value keeps reading its own reference, expiry or not.
func cached[T any](root, kind, dir string, build func() T) T {
	key := root + "\x00" + kind
	now := dirStamp(dir)

	c := exportCache
	c.mu.Lock()
	// An entry past its TTL is a miss, not a hit: the sweeper only comes round
	// once per sweepPeriod, and serving from an expired entry in between would
	// make the configured TTL a floor rather than the promise it is.
	if e, ok := c.entries[key]; ok && e.stamp == now && !c.idleTooLong(e, c.now()) {
		v := e.value
		e.lastAccess = c.now()
		c.mu.Unlock()
		return v.(T)
	}
	c.mu.Unlock()

	// Built outside the lock: parsing a thousand files must not block the tools
	// working with other parts of the export. The size estimate is walked here
	// too, for the same reason.
	value := build()
	size := c.sizeOf(value)

	c.mu.Lock()
	c.putLocked(key, &cacheEntry{
		root: root, kind: kind, stamp: now,
		value: value, bytes: size, lastAccess: c.now(),
	})
	c.cleanLocked()
	start, period := c.ensureSweeperLocked()
	c.mu.Unlock()
	if start {
		go c.sweepLoop(period)
	}
	return value
}

// cachedSubscriptions returns every parsed subscription of the export.
func (s *XMLSource) cachedSubscriptions() []parsedSubscription {
	dir := filepath.Join(s.root, "EventSubscriptions")
	return cached(s.root, "subscriptions", dir, func() []parsedSubscription {
		names, err := xmlFilesIn(dir)
		if err != nil {
			return nil
		}
		out := make([]parsedSubscription, 0, len(names))
		for _, file := range names {
			var sub xmlSubscription
			if err := readXML(filepath.Join(dir, file), &sub); err != nil {
				continue
			}
			p := sub.Object.Properties
			out = append(out, parsedSubscription{
				name: p.Name, event: p.Event, handler: p.Handler, source: p.Source,
			})
		}
		return out
	})
}

// parsedSubscription is one subscription as read from the export.
type parsedSubscription struct {
	name    string
	event   string
	handler string
	source  xmlSourceType
}

// cachedFunctionalOptions returns every parsed functional option.
func (s *XMLSource) cachedFunctionalOptions() []parsedOption {
	dir := filepath.Join(s.root, "FunctionalOptions")
	return cached(s.root, "options", dir, func() []parsedOption {
		names, err := xmlFilesIn(dir)
		if err != nil {
			return nil
		}
		out := make([]parsedOption, 0, len(names))
		for _, file := range names {
			var fo xmlFunctionalOption
			if err := readXML(filepath.Join(dir, file), &fo); err != nil {
				continue
			}
			p := fo.Object.Properties
			out = append(out, parsedOption{
				name: p.Name, synonym: p.Synonym.ru(), location: p.Location,
				privileged: p.PrivilegedGetMode == "true", content: p.Content.Objects,
			})
		}
		return out
	})
}

// parsedOption is one functional option as read from the export.
type parsedOption struct {
	name       string
	synonym    string
	location   string
	privileged bool
	content    []string
}

// cachedRoleRights returns the parsed Rights.xml of every role, keyed by the
// role's directory name. This is the heaviest read of the whole server: 1087
// files on УТ, about a second, and three different tools do it.
func (s *XMLSource) cachedRoleRights() map[string]xmlRights {
	dir := filepath.Join(s.root, "Roles")
	return cached(s.root, "roles", dir, func() map[string]xmlRights {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		out := make(map[string]xmlRights, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			var rights xmlRights
			if err := readXML(filepath.Join(dir, e.Name(), "Ext", "Rights.xml"), &rights); err != nil {
				continue
			}
			out[e.Name()] = rights
		}
		return out
	})
}

// xmlFilesIn lists the .xml files of a directory, sorted, so that every scan
// built on it is deterministic.
func xmlFilesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".xml" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
