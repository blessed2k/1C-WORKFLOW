package syntax

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
)

// TestLazyDefersParseUntilFirstUse pins the point of the whole change: building
// the index must not touch the corpus, and the first lookup must.
func TestLazyDefersParseUntilFirstUse(t *testing.T) {
	parses := 0
	ix := newLazy(func() ([]onec.SyntaxEntry, error) {
		parses++
		return []onec.SyntaxEntry{{NameRu: "ЗначениеЗаполнено", NameEn: "ValueIsFilled"}}, nil
	})

	if parses != 0 {
		t.Fatalf("corpus parsed %d times before any lookup, want 0", parses)
	}

	if got := ix.Search("ЗначениеЗаполнено", 5); len(got) != 1 {
		t.Fatalf("Search after lazy init returned %d entries, want 1", len(got))
	}
	if parses != 1 {
		t.Fatalf("corpus parsed %d times after first lookup, want 1", parses)
	}

	// Further lookups reuse the parsed corpus.
	ix.Search("ValueIsFilled", 5)
	ix.Count()
	ix.GlobalMethod("ЗначениеЗаполнено")
	if parses != 1 {
		t.Fatalf("corpus parsed %d times after repeated lookups, want 1", parses)
	}
}

// TestLazyServesTheFileCorpus checks the deferred parse against the fixture
// file. The expectations are facts written into the fixture, not a second
// computation of the same answer.
func TestLazyServesTheFileCorpus(t *testing.T) {
	lazy := NewLazy(fixturePath)
	if err := lazy.Err(); err != nil {
		t.Fatalf("NewLazy corpus: %v", err)
	}
	if lazy.Count() < 10 {
		t.Fatalf("lazy corpus too small: %d entries", lazy.Count())
	}
	// An exact name match ranks ahead of prefix and substring hits.
	if hits := lazy.Search("ЗначениеЗаполнено", 10); len(hits) == 0 || hits[0].NameRu != "ЗначениеЗаполнено" {
		t.Fatalf("exact match should rank first; got %v", names(hits))
	}
	e, ok := lazy.GlobalMethod("СтрНайти")
	if !ok {
		t.Fatalf("СтрНайти should resolve as a global-context method through the lazy index")
	}
	if e.NameEn != "StrFind" {
		t.Errorf("СтрНайти English name = %q, want StrFind", e.NameEn)
	}
	// A method of a type is not a global-context method.
	if _, ok := lazy.GlobalMethod("Найти"); ok {
		t.Errorf("Найти (метод Массив) не должен находиться как метод глобального контекста")
	}
}

// TestLazyMissingFileIsAnError: without the index file the server still
// starts; the lazy index reports ErrNotFound when asked and answers lookups
// with nothing.
func TestLazyMissingFileIsAnError(t *testing.T) {
	ix := NewLazy(filepath.Join(t.TempDir(), "absent.json.gz"))
	if err := ix.Err(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Err() = %v, want ErrNotFound", err)
	}
	if got := ix.Search("СтрНайти", 5); len(got) != 0 {
		t.Errorf("Search without an index returned %d entries", len(got))
	}
}

// TestLazyParsesOnceUnderConcurrency: many callers racing on a cold index must
// still produce one parse, and all of them must see the parsed corpus.
func TestLazyParsesOnceUnderConcurrency(t *testing.T) {
	var parses atomic.Int64
	ix := newLazy(func() ([]onec.SyntaxEntry, error) {
		parses.Add(1)
		time.Sleep(5 * time.Millisecond) // widen the window a single Once must close
		return []onec.SyntaxEntry{
			{NameRu: "СтрНайти", NameEn: "StrFind", Kind: "method", Owner: "Глобальный контекст"},
		}, nil
	})

	const callers = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	counts := make([]int, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			counts[i] = len(ix.Search("СтрНайти", 5))
		}()
	}
	close(start)
	wg.Wait()

	if n := parses.Load(); n != 1 {
		t.Fatalf("corpus parsed %d times under %d concurrent lookups, want 1", n, callers)
	}
	for i, c := range counts {
		if c != 1 {
			t.Fatalf("caller %d saw %d entries, want 1", i, c)
		}
	}
}

// TestLazyParseFailureStaysAnError: a corpus that cannot be parsed must surface
// as a tool-level error, and lookups must degrade to empty rather than panic.
func TestLazyParseFailureStaysAnError(t *testing.T) {
	boom := errors.New("gzip: invalid header")
	parses := 0
	ix := newLazy(func() ([]onec.SyntaxEntry, error) {
		parses++
		return nil, boom
	})

	if got := ix.Err(); !errors.Is(got, boom) {
		t.Fatalf("Err() = %v, want %v", got, boom)
	}
	if got := ix.Search("СтрНайти", 5); len(got) != 0 {
		t.Errorf("Search on a broken corpus returned %d entries, want none", len(got))
	}
	if got := ix.Count(); got != 0 {
		t.Errorf("Count on a broken corpus = %d, want 0", got)
	}
	if _, ok := ix.GlobalMethod("СтрНайти"); ok {
		t.Errorf("GlobalMethod on a broken corpus reported a match")
	}
	// The corpus file does not repair itself between calls, so a failed parse
	// is not retried on every lookup.
	if parses != 1 {
		t.Errorf("broken corpus parsed %d times, want 1", parses)
	}
	if got := ix.Err(); !errors.Is(got, boom) {
		t.Errorf("Err() on a second call = %v, want %v", got, boom)
	}
}

// TestNewLazyReadsTheFileOnFirstUse: NewLazy must not touch the file at
// construction. The file appears only after NewLazy returned, and the first
// lookup still sees it — an eager read would have failed with ErrNotFound.
func TestNewLazyReadsTheFileOnFirstUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "syntax-index.json")
	ix := NewLazy(path)

	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ix.Err(); err != nil {
		t.Fatalf("Err() after the file appeared: %v", err)
	}
	if len(ix.Search("СтрНайти", 5)) == 0 {
		t.Fatalf("Search found nothing in a corpus written after NewLazy")
	}
}
