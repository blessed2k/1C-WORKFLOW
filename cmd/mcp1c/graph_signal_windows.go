//go:build windows

package main

import (
	"os"
	"syscall"
)

// additionalShutdownSignals — та же сигнатура и то же тело, что у
// graph_signal_unix.go: syscall.SIGTERM существует и на Windows, и рантайм
// Go переводит в него CTRL_CLOSE_EVENT/CTRL_LOGOFF_EVENT/CTRL_SHUTDOWN_EVENT
// (runtime/os_windows.go) — «kill гасит процесс так же корректно, как
// Ctrl+C» выполняется на боевой машине без единой отдельной
// строки кода. Платформенный файл здесь не потому, что поведение разное, а
// по конвенции проекта: syscall живёт только в файле с суффиксом платформы
// или build-тегом (CLAUDE.md, образец cmd/mcp1c/rss_*.go), а тело — то же.
func additionalShutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM}
}
