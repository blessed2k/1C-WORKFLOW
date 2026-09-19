package workspace_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestSafeJoinОтбиваетВыходИзКорня: сервер не выходит
// за workspace. Проверяются все три способа выйти: относительный подъём, путь
// в обход корня и симлинк, ведущий наружу.
func TestSafeJoinОтбиваетВыходИзКорня(t *testing.T) {
	корень := t.TempDir()
	снаружи := t.TempDir()

	if err := os.MkdirAll(filepath.Join(корень, "CommonModules", "Ext"), 0o755); err != nil {
		t.Fatalf("подготовка корня: %v", err)
	}
	if err := os.WriteFile(filepath.Join(снаружи, "secret.txt"), []byte("не для индекса"), 0o644); err != nil {
		t.Fatalf("подготовка каталога снаружи: %v", err)
	}

	// Симлинк внутри корня, ведущий наружу: файловая система разрешит чтение,
	// значит проверку обязан сделать SafeJoin.
	симлинк := filepath.Join(корень, "наружу")
	if err := os.Symlink(снаружи, симлинк); err != nil {
		t.Skipf("симлинки недоступны в этой среде: %v", err)
	}

	отказы := []struct {
		name string
		rel  string
	}{
		{"подъём выше корня", filepath.Join("..", "чужое")},
		{"подъём через существующий подкаталог", filepath.Join("CommonModules", "..", "..", "чужое")},
		{"абсолютный путь", filepath.Join(снаружи, "secret.txt")},
		{"симлинк наружу", "наружу"},
		{"файл через симлинк наружу", filepath.Join("наружу", "secret.txt")},
	}
	for _, tc := range отказы {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspace.SafeJoin(корень, tc.rel)
			if err == nil {
				t.Fatalf("SafeJoin(%q) вернул %q без ошибки", tc.rel, got)
			}
			if !errors.Is(err, workspace.ErrPathOutsideWorkspace) {
				t.Fatalf("SafeJoin(%q): ошибка %v, ожидалась ErrPathOutsideWorkspace", tc.rel, err)
			}
			if !strings.Contains(err.Error(), "path_outside_workspace") {
				t.Errorf("текст ошибки не называет код: %v", err)
			}
		})
	}

	// Каталог из t.TempDir() на macOS лежит под симлинком /var -> /private/var,
	// поэтому ожидаемые пути строятся от разрешённого корня.
	разрешённыйКорень, err := filepath.EvalSymlinks(корень)
	if err != nil {
		t.Fatalf("EvalSymlinks(корень): %v", err)
	}

	разрешено := []struct {
		name string
		rel  string
		ожид string
	}{
		{"существующий подкаталог", filepath.Join("CommonModules", "Ext"), filepath.Join(разрешённыйКорень, "CommonModules", "Ext")},
		{"ещё не созданный файл", filepath.Join("CommonModules", "Ext", "Module.bsl"), filepath.Join(разрешённыйКорень, "CommonModules", "Ext", "Module.bsl")},
		{"сам корень", ".", разрешённыйКорень},
		{"путь со слешами как в манифесте", "CommonModules/Ext", filepath.Join(разрешённыйКорень, "CommonModules", "Ext")},
	}
	for _, tc := range разрешено {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspace.SafeJoin(корень, tc.rel)
			if err != nil {
				t.Fatalf("SafeJoin(%q): %v", tc.rel, err)
			}
			if got != tc.ожид {
				t.Errorf("SafeJoin(%q) = %q, ожидалось %q", tc.rel, got, tc.ожид)
			}
		})
	}
}
