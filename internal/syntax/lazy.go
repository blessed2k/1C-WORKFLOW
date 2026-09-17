package syntax

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
)

// NewLazy returns an Index whose corpus is read from path and parsed on first use.
// A session that never asks for a syntax lookup never pays the ~23k-entry parse,
// and the caller does not have to remember an initialisation order: laziness is a
// property of the corpus, so it stays inside this package.
//
// The caller MUST check Err before the first lookup and answer with that error,
// the way it answers today when Load returns one. Search, Count and GlobalMethod
// keep their signatures, so a corpus that failed to parse reaches them as an empty
// result, indistinguishable from "not found" — Err is the only place the reason
// exists. A missing file is reported the same way (ErrNotFound), so the server
// starts without an index and only the tools that need it answer with an error.
func NewLazy(path string) *Index {
	return newLazy(func() ([]onec.SyntaxEntry, error) {
		ix, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		return ix.entries, nil
	})
}

// newLazy is the seam NewLazy is built on: the parse step is a parameter so the
// package can observe when — and how often — it runs.
func newLazy(parse func() ([]onec.SyntaxEntry, error)) *Index {
	return &Index{parse: parse}
}

// Err reports the corpus parse failure, parsing it first if that has not happened
// yet. A broken corpus stays an error the caller answers with, the way a nil index
// does today, and never a panic in the middle of a lookup.
func (ix *Index) Err() error {
	return ix.ensure()
}

// ensure parses the corpus exactly once. An eagerly built Index (Load) carries no
// parse step and passes straight through.
func (ix *Index) ensure() error {
	if ix.parse == nil {
		return nil
	}
	ix.once.Do(func() {
		entries, err := ix.parse()
		if err != nil {
			ix.parseErr = err
			return
		}
		ix.entries = entries
	})
	return ix.parseErr
}
