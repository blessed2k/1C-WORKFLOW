// Package syntaxtest gives tests a platform syntax index without the platform
// install. Fixture is a small hand-written corpus (testdata/corpus.json): a
// couple of dozen entries in the shape syntaxgen produces, enough for the lookups the
// tests exercise and nothing copied from the platform reference. RealOrSkip is
// for tests that run against a real configuration export and need the real
// index the user generated.
package syntaxtest

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

//go:embed testdata/corpus.json
var corpus []byte

// Fixture returns the synthetic index.
func Fixture(t testing.TB) *syntax.Index {
	t.Helper()
	ix, err := syntax.Parse(corpus)
	if err != nil {
		t.Fatalf("syntaxtest: разбор фикстуры: %v", err)
	}
	return ix
}

// FixtureFile writes the synthetic corpus to a temporary file and returns its
// path, for code that takes the index as a file (the server flag, NewLazy).
func FixtureFile(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "syntax-index.json")
	if err := os.WriteFile(path, corpus, 0o600); err != nil {
		t.Fatalf("syntaxtest: запись фикстуры: %v", err)
	}
	return path
}

// RealPath is where a real-export test looks for the generated index:
// MCP_1C_SYNTAX_INDEX, then syntax.DefaultPath.
func RealPath() string {
	if v := strings.TrimSpace(os.Getenv(syntax.EnvPath)); v != "" {
		return v
	}
	return syntax.DefaultPath()
}

// RealOrSkip loads the generated index, skipping the test when there is none:
// a real-export run without it would resolve platform calls differently from
// a normal run. A file that exists but does not parse fails the test.
func RealOrSkip(t testing.TB) *syntax.Index {
	t.Helper()
	ix, err := syntax.LoadFile(RealPath())
	if errors.Is(err, syntax.ErrNotFound) {
		t.Skipf("индекс синтаксиса платформы не найден (%s), тест на реальной выгрузке пропущен", RealPath())
	}
	if err != nil {
		t.Fatalf("индекс синтаксиса: %v", err)
	}
	return ix
}
