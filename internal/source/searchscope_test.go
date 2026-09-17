package source

import (
	"context"
	"strings"
	"testing"
)

// TestSearchScopeNarrowsToPath: without a scope, a search over a standard
// configuration returns mostly vendor modules and the caller's own object drowns.
func TestSearchScopeNarrowsToPath(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	ctx := context.Background()

	all, err := s.SearchCode(ctx, SearchParams{Query: "Процедура", IgnoreCase: true})
	if err != nil {
		t.Fatalf("unscoped search: %v", err)
	}
	scoped, err := s.SearchCode(ctx, SearchParams{Query: "Процедура", IgnoreCase: true, Scope: "CommonModules"})
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	if scoped.Scope != "CommonModules" {
		t.Errorf("result does not echo the scope it ran under: %q", scoped.Scope)
	}
	if len(scoped.Matches) > len(all.Matches) {
		t.Errorf("scoped search returned more than the unscoped one (%d vs %d)", len(scoped.Matches), len(all.Matches))
	}
	for _, m := range scoped.Matches {
		if !strings.Contains(m.File, "CommonModules") {
			t.Errorf("match outside the scope: %s", m.File)
		}
	}
}

// TestSearchReportsEnclosingProcedure: file:line says where a hit is, the
// procedure name says what it belongs to — the thing you need before opening a
// module of several thousand lines.
func TestSearchReportsEnclosingProcedure(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	res, err := s.SearchCode(context.Background(), SearchParams{Query: "Возврат", IgnoreCase: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Matches) == 0 {
		t.Skip("fixture has no matching lines")
	}
	for _, m := range res.Matches {
		if m.Procedure != "" {
			return
		}
	}
	t.Errorf("no match carried its enclosing procedure: %+v", res.Matches)
}

// TestSearchTruncationIsHonest: stopping at the limit must not be reported as a
// count, and total=true must give the real number.
func TestSearchTruncationIsHonest(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	ctx := context.Background()

	limited, err := s.SearchCode(ctx, SearchParams{Query: "Процедура", IgnoreCase: true, MaxResults: 1})
	if err != nil {
		t.Fatalf("limited search: %v", err)
	}
	if limited.Shown != 1 {
		t.Fatalf("Shown = %d, want 1", limited.Shown)
	}
	if !limited.Truncated {
		t.Skip("fixture has a single match; truncation not exercised")
	}
	if limited.TotalMatches != 0 {
		t.Errorf("a stopped scan must not claim a total, got %d", limited.TotalMatches)
	}
	if !strings.Contains(limited.Note, "total=true") {
		t.Errorf("note does not point at total=true: %q", limited.Note)
	}

	counted, err := s.SearchCode(ctx, SearchParams{Query: "Процедура", IgnoreCase: true, MaxResults: 1, Total: true})
	if err != nil {
		t.Fatalf("counted search: %v", err)
	}
	if counted.TotalMatches <= counted.Shown {
		t.Errorf("total=true did not count past the limit: total %d, shown %d", counted.TotalMatches, counted.Shown)
	}
}

func TestDeclaredRoutine(t *testing.T) {
	cases := map[string]string{
		"Процедура ЗаполнитьТаблицу(Пар) Экспорт": "ЗаполнитьТаблицу",
		"  Функция СуммаДокумента() Экспорт":      "СуммаДокумента",
		"Procedure DoWork()":                      "DoWork",
		"// Процедура в комментарии":              "",
		"Возврат Истина;":                         "",
	}
	for line, want := range cases {
		if got := declaredRoutine(line); got != want {
			t.Errorf("declaredRoutine(%q) = %q, want %q", line, got, want)
		}
	}
}
