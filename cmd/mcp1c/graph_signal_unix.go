//go:build unix

package main

import (
	"os"
	"syscall"
)

// additionalShutdownSignals — что, кроме os.Interrupt, обязано гасить
// graph-режим: kill (SIGTERM) завершает процесс
// так же корректно, как Ctrl+C. syscall.SIGTERM портируем: тот же символ
// существует и на unix, и на Windows (см. graph_signal_windows.go — ТЕЛО
// этого файла идентично, платформенный файл здесь по конвенции проекта
// «одна и та же сигнатура во всех вариантах», образец cmd/mcp1c/rss_*.go, а
// не потому что syscall на Windows значит что-то другое), поэтому запрет
// build-tag («никакой mac-специфики… боевое применение
// на Windows») сюда не относится — здесь ветки НЕТ, оба файла ведут себя
// одинаково на своей платформе. CheckPortability (internal/arch) требует
// сам импорт "syscall" держать в файле с суффиксом платформы или
// build-тегом — этот файл им и является.
func additionalShutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM}
}
