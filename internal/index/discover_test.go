package index

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFixtureFiles создаёт файлы rel->содержимое под root для теста обхода.
func writeFixtureFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
}

func relPaths(files []discoveredFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.relPath)
	}
	sort.Strings(out)
	return out
}

// TestDiscoverComponentSkipsBinaryArtifacts закрывает файловую половину списка
// неиндексируемого: workspace.SkipFile получает реальное место в обходе index:
// расширения вроде .cf/.log отсекаются им внутри workspace.IsIgnored, до
// того как discoverComponent вообще увидит файл. Тест красит ровно то место:
// уберите вызов IsIgnored (или сам SkipFile) — .cf/.log попадут в выдачу.
func TestDiscoverComponentSkipsBinaryArtifacts(t *testing.T) {
	root := t.TempDir()
	writeFixtureFiles(t, root, map[string]string{
		"CommonModules/X/Ext/Module.bsl": "Процедура А() КонецПроцедуры",
		"backup.cf":                      "binary",
		"logs/server.log":                "log line",
		"Configuration.xml":              "<xml/>",
	})

	got, err := discoverComponent(root, nil, nil)
	if err != nil {
		t.Fatalf("discoverComponent: %v", err)
	}
	want := []string{"CommonModules/X/Ext/Module.bsl", "Configuration.xml"}
	if diff := relPaths(got); !equalStrings(diff, want) {
		t.Fatalf("discoverComponent = %v, want %v", diff, want)
	}
}

// TestDiscoverComponentIncludeExclude проверяет, что discoverComponent
// применяет include/exclude манифеста через workspace.MatchPath — тот же
// матчер, что валидирует шаблоны при загрузке манифеста.
func TestDiscoverComponentIncludeExclude(t *testing.T) {
	root := t.TempDir()
	writeFixtureFiles(t, root, map[string]string{
		"CommonModules/X/Ext/Module.bsl":  "1",
		"Catalogs/Y/Ext/ObjectModule.bsl": "2",
		"tests/yaxunit/Test.bsl":          "3",
	})

	got, err := discoverComponent(root, []string{"CommonModules/**"}, nil)
	if err != nil {
		t.Fatalf("discoverComponent: %v", err)
	}
	want := []string{"CommonModules/X/Ext/Module.bsl"}
	if diff := relPaths(got); !equalStrings(diff, want) {
		t.Fatalf("include-only discoverComponent = %v, want %v", diff, want)
	}

	got, err = discoverComponent(root, nil, []string{"tests/**"})
	if err != nil {
		t.Fatalf("discoverComponent: %v", err)
	}
	want = []string{"Catalogs/Y/Ext/ObjectModule.bsl", "CommonModules/X/Ext/Module.bsl"}
	if diff := relPaths(got); !equalStrings(diff, want) {
		t.Fatalf("exclude discoverComponent = %v, want %v", diff, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
