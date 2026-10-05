package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// searchRootsFixture builds a base export and one extension next to it; both
// hold a module with the same marker line.
func searchRootsFixture(t *testing.T) *XMLSource {
	t.Helper()
	base, ext := t.TempDir(), t.TempDir()
	write := func(dir, rel, text string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write(base, "CommonModules/Общий/Ext/Module.bsl", "Процедура Базовая()\n\t// маркер\nКонецПроцедуры\n")
	write(ext, "CommonModules/Расш_Сервис/Ext/Module.bsl", "Процедура Расш_Своя()\n\t// маркер\nКонецПроцедуры\n")

	s := NewXMLSource(base)
	s.OtherComponents = func() []ComponentRoot { return []ComponentRoot{{Name: "addon", Dir: ext}} }
	return s
}

// TestSearchCoversExtensionRoots: the code of the project's extensions is the
// developer's own code, and a search over the base export alone never saw it.
// Extension hits come first: in a standard configuration the vendor modules
// would otherwise fill the limit before the extension is reached.
func TestSearchCoversExtensionRoots(t *testing.T) {
	s := searchRootsFixture(t)
	res, err := s.SearchCode(context.Background(), SearchParams{Query: "маркер"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("matches = %d, want 2: %+v", len(res.Matches), res.Matches)
	}
	first, second := res.Matches[0], res.Matches[1]
	if first.Component != "addon" || first.File != "CommonModules/Расш_Сервис/Ext/Module.bsl" || first.Procedure != "Расш_Своя" {
		t.Errorf("first match is not the extension one: %+v", first)
	}
	if second.Component != "" || second.File != "CommonModules/Общий/Ext/Module.bsl" {
		t.Errorf("base match changed shape: %+v", second)
	}
}

// TestSearchScopeAddressesComponent: scope narrows to one extension by its
// component id, and a path scope still works inside the base export.
func TestSearchScopeAddressesComponent(t *testing.T) {
	s := searchRootsFixture(t)
	ctx := context.Background()

	onlyExt, err := s.SearchCode(ctx, SearchParams{Query: "маркер", Scope: "addon/"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(onlyExt.Matches) != 1 || onlyExt.Matches[0].Component != "addon" {
		t.Errorf("scope=addon/ returned %+v, want the extension match only", onlyExt.Matches)
	}

	onlyBase, err := s.SearchCode(ctx, SearchParams{Query: "маркер", Scope: "CommonModules/Общий"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(onlyBase.Matches) != 1 || onlyBase.Matches[0].Component != "" {
		t.Errorf("scope by path returned %+v, want the base match only", onlyBase.Matches)
	}
}

// TestSearchLimitSpansRoots: the limit and the honest total count are shared by
// all roots.
func TestSearchLimitSpansRoots(t *testing.T) {
	s := searchRootsFixture(t)
	res, err := s.SearchCode(context.Background(), SearchParams{Query: "маркер", MaxResults: 1, Total: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Shown != 1 || !res.Truncated || res.TotalMatches != 2 {
		t.Errorf("shown=%d truncated=%v total=%d, want 1/true/2", res.Shown, res.Truncated, res.TotalMatches)
	}
}

// TestSearchScopeStartingWithComponentSkipsOtherRoots: a scope that starts with
// a component id is answered from that component alone. Walking a standard
// configuration for it took ten seconds on a real export and could only add a
// module whose path happens to contain the id.
func TestSearchScopeStartingWithComponentSkipsOtherRoots(t *testing.T) {
	s := searchRootsFixture(t)
	path := filepath.Join(s.root, "CommonModules", "addon", "Ext", "Module.bsl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("Процедура Тёзка()\n\t// маркер\nКонецПроцедуры\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, scope := range []string{"addon", "Addon/CommonModules"} {
		res, err := s.SearchCode(context.Background(), SearchParams{Query: "маркер", Scope: scope, Total: true})
		if err != nil {
			t.Fatalf("search scope=%s: %v", scope, err)
		}
		if res.TotalMatches != 1 || res.Matches[0].Component != "addon" {
			t.Errorf("scope=%s: %+v, want the extension match only", scope, res.Matches)
		}
	}
}
