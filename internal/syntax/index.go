// Package syntax provides a searchable index of the 1C BSL/platform syntax
// reference. The index is extracted from the syntax help of the user's own
// platform install (see cmd/syntaxgen) and read from a file at runtime: the
// reference text belongs to the platform vendor and is not shipped with the
// server.
package syntax

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
)

// EnvPath names the environment variable with the index path. The server flag
// --syntax-index is stronger; DefaultPath is used when neither is set.
const EnvPath = "MCP_1C_SYNTAX_INDEX"

// GenerateHint is the command that builds the index, quoted in every error about
// a missing one so the user does not have to look it up.
const GenerateHint = "go run ./cmd/syntaxgen <каталог платформы>/shcntx_ru.hbk " +
	"<каталог платформы>/shlang_ru.hbk <каталог платформы>/shquery_ru.hbk"

// ErrNotFound marks an index file that does not exist. Callers tell it apart from
// a broken file with errors.Is.
var ErrNotFound = errors.New("индекс синтаксиса платформы не найден")

// DefaultPath is where syntaxgen writes the index and where the server looks for
// it when no path is given: <user config dir>/mcp1c/syntax-index.json.gz. Empty
// when the platform has no user config directory.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "mcp1c", "syntax-index.json.gz")
}

// Index is a searchable syntax reference. It is built either eagerly (LoadFile,
// Parse) or lazily (NewLazy); in the lazy form parse is what turns the file into
// entries, guarded by once, and every lookup goes through ensure first.
type Index struct {
	entries []onec.SyntaxEntry

	parse    func() ([]onec.SyntaxEntry, error)
	once     sync.Once
	parseErr error

	ownersOnce sync.Once
	owners     map[string]ownerStat // lower-cased owner name -> stat
}

// LoadFile reads the index from path. A missing file is an ErrNotFound carrying
// the path and the command that generates it.
func LoadFile(path string) (*Index, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: путь не задан (флаг --syntax-index или %s); сгенерируйте индекс: %s",
			ErrNotFound, EnvPath, GenerateHint)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s; сгенерируйте его: %s (путь задаётся флагом --syntax-index или %s)",
			ErrNotFound, path, GenerateHint, EnvPath)
	}
	if err != nil {
		return nil, fmt.Errorf("индекс синтаксиса %s: %w", path, err)
	}
	ix, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("индекс синтаксиса %s: %w", path, err)
	}
	return ix, nil
}

// Parse decodes an index: a JSON array of entries, gzip-compressed (what
// syntaxgen writes) or plain (hand-written fixtures).
func Parse(data []byte) (*Index, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		if data, err = io.ReadAll(gz); err != nil {
			return nil, err
		}
	}
	var entries []onec.SyntaxEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return &Index{entries: entries}, nil
}

// Count returns the number of entries.
func (ix *Index) Count() int {
	ix.ensure()
	return len(ix.entries)
}

// Search returns entries whose Russian or English name matches the query,
// ranked exact > prefix > substring (case-insensitive), capped at limit.
func (ix *Index) Search(query string, limit int) []onec.SyntaxEntry {
	ix.ensure()
	return ix.search(query, "", limit)
}

// search is Search restricted to one owner (exact, as stored) when owner is not
// empty.
func (ix *Index) search(query, owner string, limit int) []onec.SyntaxEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}

	type scored struct {
		e     onec.SyntaxEntry
		score int
	}
	var hits []scored
	for _, e := range ix.entries {
		if owner != "" && e.Owner != owner {
			continue
		}
		rank := matchRank(strings.ToLower(e.NameRu), q)
		if r := matchRank(strings.ToLower(e.NameEn), q); r > rank {
			rank = r
		}
		if rank > 0 {
			hits = append(hits, scored{e, rank*100 + importance(e)})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return len(hits[i].e.NameRu) < len(hits[j].e.NameRu)
	})

	out := make([]onec.SyntaxEntry, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		out = append(out, h.e)
	}
	return out
}

// GlobalMethod returns the global-context method with exactly this Russian or
// English name. Only the global context qualifies: a method of some object type
// shares its name with unrelated things, and judging a call against the wrong
// signature would invent an error. Not found means "nothing provable", not "does
// not exist" — a local or common-module procedure lands here too.
func (ix *Index) GlobalMethod(name string) (onec.SyntaxEntry, bool) {
	ix.ensure()
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return onec.SyntaxEntry{}, false
	}
	for _, e := range ix.entries {
		if e.Owner != "Глобальный контекст" || e.Kind != "method" {
			continue
		}
		if strings.ToLower(e.NameRu) == n || strings.ToLower(e.NameEn) == n {
			return e, true
		}
	}
	return onec.SyntaxEntry{}, false
}

// ParamCounts reads how many parameters a signature declares and how many of
// them the platform marks as mandatory. The reference spells this out per
// parameter ("<Значение> (обязательный)"), which is what makes an arity check
// provable rather than guessed.
func ParamCounts(e onec.SyntaxEntry) (required, total int) {
	required = strings.Count(e.Params, "(обязательный)")
	total = required + strings.Count(e.Params, "(необязательный)")
	return required, total
}

// importance boosts entries that are usually what you want when typing a bare
// name: language constructs, query keywords and global-context functions rank
// above the same name repeated as a method/property of specific object types.
func importance(e onec.SyntaxEntry) int {
	switch {
	case e.Kind == "operator" || e.Kind == "query":
		return 5
	case e.Owner == "Глобальный контекст":
		return 4
	case e.Kind == "type":
		return 3
	default:
		return 0
	}
}

// matchRank scores how a name matches the query: 3 exact, 2 prefix, 1 substring.
func matchRank(name, q string) int {
	switch {
	case name == q:
		return 3
	case strings.HasPrefix(name, q):
		return 2
	case strings.Contains(name, q):
		return 1
	default:
		return 0
	}
}
