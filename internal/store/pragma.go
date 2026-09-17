package store

import (
	"context"
	"fmt"
	"strings"
)

// PRAGMA делятся на три класса по времени жизни (ADR-2 §6), и путать их нельзя:
// ошибка класса не видна на глаз — база открывается и работает, просто не в том
// режиме, в каком считает документация.
//
//  1. creation-only: действует ТОЛЬКО на пустую БД без заголовка и до любого DDL.
//     auto_vacuum именно такой: journal_mode=WAL пишет заголовок, и выставленный
//     после него auto_vacuum молча остаётся нулём.
//  2. persistent database setting: пишется в файл один раз и переживает
//     переоткрытие. journal_mode=WAL такой — и установки недостаточно,
//     фактическое значение обязано быть проверено (§6.2).
//  3. per-connection: живёт ровно столько, сколько соединение, и обязано
//     выставляться на КАЖДОМ соединении, включая читателей (§6.3).

func creationPragmas() []string {
	return []string{"PRAGMA auto_vacuum=INCREMENTAL"}
}

func persistentPragmas() []string {
	return []string{"PRAGMA journal_mode=WAL"}
}

// connectionPragmas — per-connection список. cache_size задан на соединение,
// поэтому цена пула читателей линейна по его размеру (§6.3).
func connectionPragmas(busyTimeoutMillis int) []string {
	return []string{
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMillis),
		"PRAGMA temp_store=MEMORY",
		"PRAGMA cache_size=-65536",
	}
}

// createPragmaList — порядок для СОЗДАНИЯ новой БД: creation-only, затем
// persistent, затем per-connection. Обратный порядок молча оставляет
// auto_vacuum нулём.
func createPragmaList(busyTimeoutMillis int) []string {
	p := creationPragmas()
	p = append(p, persistentPragmas()...)
	return append(p, connectionPragmas(busyTimeoutMillis)...)
}

// openPragmaList — порядок для открытия СУЩЕСТВУЮЩЕЙ БД: только per-connection.
// На непустой базе auto_vacuum игнорируется, а journal_mode уже сохранён в файле;
// фактические значения persistent-настроек всё равно проверяются.
func openPragmaList(busyTimeoutMillis int) []string {
	return connectionPragmas(busyTimeoutMillis)
}

// verifyPersistentPragmas сверяет заявленное с фактическим (§6.2, §6.4):
// SQLite вправе отказать (сетевая ФС, режим только для чтения) и остаться в
// journal-режиме, и тогда все рассуждения о WAL относились бы не к той базе.
// Несовпадение — ошибка открытия, а не предупреждение.
func verifyPersistentPragmas(ctx context.Context, c *conn) error {
	mode, err := c.queryText(ctx, "PRAGMA journal_mode")
	if err != nil {
		return fmt.Errorf("journal_mode не прочитан: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("journal_mode фактически %q, а не WAL: гарантии снапшотов и конкурентности не действуют", mode)
	}
	return nil
}

// pragmaReport — pragma, фактические значения которых отдаёт Status.
// Полагаться на DSN-параметры драйвера нельзя, они различаются между драйверами
// (§6.4): значение читается обратно из соединения.
var pragmaReport = []string{
	"journal_mode", "synchronous", "foreign_keys", "busy_timeout",
	"temp_store", "cache_size", "page_size", "auto_vacuum",
	"wal_autocheckpoint", "locking_mode",
}

// readPragmas снимает фактические значения pragma для Status.
func readPragmas(ctx context.Context, c *conn) map[string]string {
	out := make(map[string]string, len(pragmaReport))
	for _, p := range pragmaReport {
		v, err := c.queryText(ctx, "PRAGMA "+p)
		if err != nil {
			out[p] = "ошибка: " + err.Error()
			continue
		}
		out[p] = v
	}
	return out
}

// pragmaName достаёт имя из строки вида `PRAGMA name=value`.
func pragmaName(stmt string) string {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stmt), "PRAGMA"))
	if i := strings.IndexAny(s, "=("); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}
