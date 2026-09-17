package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// exeSuffix — расширение исполняемого файла на текущей платформе. Windows
// опознаёт исполняемый файл по расширению: собранный в "mcp1c" без ".exe"
// бинарник exec.Command не запускает вовсе ("executable file not found in
// %PATH%"), и e2e-тесты stdio падали на этом ещё до первого байта протокола.
// Проверка по runtime.GOOS, а не отдельный файл с суффиксом платформы: это
// одна константа тестовой обвязки, а не платформенная реализация со своей
// сигнатурой (как rss_*.go).
var exeSuffix = func() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}()

// buildMCP1C собирает бинарник один раз для теста-вызывающего и отдаёт путь
// к нему — общая обвязка для main_dispatch_test.go, stdio_live_test.go и
// stdout_contract_test.go.
func buildMCP1C(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mcp1c"+exeSuffix)
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("сборка сервера: %v\n%s", err, out)
	}
	return bin
}

// TestMainDispatchDefaultsToStdio — обратная совместимость (spec §6,
// критерий тикета 09): запуск БЕЗ подкоманды `graph` обязан вести себя
// РОВНО как раньше — поднять stdio MCP-сервер, а не graph-режим. Проверяется
// через реальный запущенный процесс (не только парсинг флагов): если бы
// диспетчер в main() перепутал условие, этот тест зафиксировал бы зависший
// SDK-хендшейк, а не только код возврата.
func TestMainDispatchDefaultsToStdio(t *testing.T) {
	bin := buildMCP1C(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "dispatch-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin)}, nil)
	if err != nil {
		t.Fatalf("подключение по stdio: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "server_info" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("server_info не зарегистрирован — сервер запущен не в обычном режиме")
	}
}

// TestMainDispatchGraphSubcommand — обратная сторона того же диспетчера:
// первый аргумент "graph" действительно уводит в graph-режим (а не
// игнорируется как неизвестный флаг stdio-сервера). Без --project graph-режим
// обязан отказать быстро с понятным сообщением, а не зависнуть в ожидании
// stdio MCP-хендшейка, которого никто не пришлёт.
func TestMainDispatchGraphSubcommand(t *testing.T) {
	bin := buildMCP1C(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "graph")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("mcp1c graph без --project завершился успешно, ожидался отказ; вывод: %s", out)
	}
	if ctx.Err() != nil {
		t.Fatalf("процесс не завершился за отведённое время — похоже, диспетчер не ушёл в graph-режим и завис на stdio")
	}
	if !strings.Contains(string(out), "--project") {
		t.Errorf("вывод не называет флаг --project: %s", out)
	}
}
