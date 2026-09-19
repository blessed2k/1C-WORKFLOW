package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr перенаправляет os.Stderr на время fn и отдаёт то, что было
// напечатано. runGraph пишет диагностику через fmt.Fprintf(os.Stderr, ...),
// а не возвращает текст — это единственный способ проверить его снаружи, не
// трогая приватные детали функции.
func captureStderr(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	code := fn()
	os.Stderr = old
	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return code, buf.String()
}

// TestRunGraphRequiresAtLeastOneProject: без --project команда обязана
// отказать до того, как коснётся сети или диска (spec §6, критерий тикета
// 09 — флаг --project обязателен).
func TestRunGraphRequiresAtLeastOneProject(t *testing.T) {
	code, stderr := captureStderr(t, func() int { return runGraph(nil) })
	if code == 0 {
		t.Fatalf("код возврата = 0, ожидался ненулевой")
	}
	if !strings.Contains(stderr, "--project") {
		t.Errorf("stderr не называет флаг --project: %q", stderr)
	}
}

// TestRunGraphRejectsNonLoopbackListen — R17: «слушает только 127.0.0.1,
// привязка к 0.0.0.0 невозможна». Проверяется ДО net.Listen: попытка реально
// открыть 0.0.0.0 в тесте была бы и небезопасной, и не нужна — достаточно
// доказать, что процесс отказывается пробовать.
func TestRunGraphRejectsNonLoopbackListen(t *testing.T) {
	code, stderr := captureStderr(t, func() int {
		return runGraph([]string{"--project", t.TempDir(), "--listen", "0.0.0.0:0"})
	})
	if code == 0 {
		t.Fatalf("код возврата = 0, ожидался ненулевой")
	}
	if !strings.Contains(stderr, "127.0.0.1") {
		t.Errorf("stderr не называет разрешённый адрес 127.0.0.1: %q", stderr)
	}
}

// TestRunGraphRejectsListenWithoutPort — --listen без порта (net.SplitHostPort
// не разбирается) — тоже понятная ошибка, а не паника или зависание.
func TestRunGraphRejectsListenWithoutPort(t *testing.T) {
	code, stderr := captureStderr(t, func() int {
		return runGraph([]string{"--project", t.TempDir(), "--listen", "127.0.0.1"})
	})
	if code == 0 {
		t.Fatalf("код возврата = 0, ожидался ненулевой")
	}
	if !strings.Contains(stderr, "--listen") {
		t.Errorf("stderr не называет флаг --listen: %q", stderr)
	}
}

// TestRunGraphNonexistentProjectDir — R21.1: каталог проекта не существует —
// сообщение называет путь, ничего не создаётся на диске (workspace.OpenRegistry
// не должен получить шанс сделать mkdir по опечатке).
func TestRunGraphNonexistentProjectDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "нет-такого-каталога")
	code, stderr := captureStderr(t, func() int {
		return runGraph([]string{"--project", missing})
	})
	if code == 0 {
		t.Fatalf("код возврата = 0, ожидался ненулевой")
	}
	if !strings.Contains(stderr, missing) {
		t.Errorf("stderr не называет путь %q: %q", missing, stderr)
	}
	// R21.1 требует не только путь, но и ЧТО ИМЕННО не найдено: сообщение не
	// должно быть тем же generic «нет активного проекта, вызовите reindex»,
	// что и TestRunGraphMissingIndexNamesReindex — иначе пользователь попробует
	// reindex на опечатке в пути вместо того, чтобы её исправить.
	if strings.Contains(stderr, "reindex") {
		t.Errorf("сообщение для несуществующего каталога ссылается на reindex, будто каталог существует, но не проиндексирован: %q", stderr)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Errorf("несуществующий каталог был создан — это скрыло бы опечатку при повторном запуске")
	}
}

// TestRunGraphMissingIndexNamesReindex — spec §6: «индекса нет — ошибка с
// точной командой reindex и ненулевым кодом возврата, ничего не
// индексируя». Каталог существует (проходит R21.1), но в нём никогда не
// был собран индекс — noActiveProjectError уже называет reindex в подсказке
// (internal/app/projects.go), это транспортный тест на то, что runGraph
// действительно доводит это сообщение до пользователя и останавливается ДО
// net.Listen.
func TestRunGraphMissingIndexNamesReindex(t *testing.T) {
	root := t.TempDir() // существует, но пуст — 1c-project.json нет, реестр пуст
	code, stderr := captureStderr(t, func() int {
		return runGraph([]string{"--project", root})
	})
	if code == 0 {
		t.Fatalf("код возврата = 0, ожидался ненулевой")
	}
	if !strings.Contains(stderr, "reindex") {
		t.Errorf("stderr не называет команду reindex: %q", stderr)
	}
}

// TestRunGraphDumpDirInsteadOfWorkspace: каталог выгрузки или проекта в
// -project (там Configuration.xml или 1c-project.json, но нет
// .mcp1c/registry.json) даёт сообщение про корень workspace с примером, а не
// no_active_project с советом reindex. И ничего не создаёт в выгрузке.
func TestRunGraphDumpDirInsteadOfWorkspace(t *testing.T) {
	for _, marker := range []string{"Configuration.xml", "1c-project.json"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, marker), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			code, stderr := captureStderr(t, func() int {
				return runGraph([]string{"-project", root, "-no-open"})
			})
			if code == 0 {
				t.Fatalf("код возврата = 0, ожидался ненулевой")
			}
			for _, want := range []string{marker, "корень workspace", ".mcp1c", "--projects-root", "Пример: mcp1c graph -project"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr не содержит %q: %q", want, stderr)
				}
			}
			if strings.Contains(stderr, "no_active_project") || strings.Contains(stderr, "reindex") {
				t.Errorf("для каталога выгрузки выдан совет про reindex: %q", stderr)
			}
			if _, err := os.Stat(filepath.Join(root, ".mcp1c")); err == nil {
				t.Errorf("в каталоге выгрузки создан .mcp1c")
			}
		})
	}
}

// TestDedupeRoots: два одинаковых --project (в том числе через разные
// представления одного и того же пути) не должны дать /api/projects
// задвоенную запись под одним и тем же ID.
func TestDedupeRoots(t *testing.T) {
	dir := t.TempDir()
	got := dedupeRoots([]string{dir, dir, filepath.Join(dir, "..", filepath.Base(dir))})
	if len(got) != 1 {
		t.Fatalf("dedupeRoots(%v) = %v, ожидался один элемент", []string{dir, dir}, got)
	}
}
