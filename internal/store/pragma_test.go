package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Классы PRAGMA не должны пересекаться: иначе «где её выполнять» перестаёт быть
// однозначным, а ошибка класса не видна на глаз — база открывается и работает,
// просто не в том режиме.
func TestPragmaClassesAreDisjoint(t *testing.T) {
	creation, persistent, conn := creationPragmas(), persistentPragmas(), connectionPragmas(5000)
	if len(creation) == 0 || len(persistent) == 0 || len(conn) == 0 {
		t.Fatalf("пустой класс pragma: creation=%v persistent=%v conn=%v", creation, persistent, conn)
	}
	seen := map[string]string{}
	for _, list := range []struct {
		class string
		items []string
	}{{"creation", creation}, {"persistent", persistent}, {"connection", conn}} {
		for _, p := range list.items {
			name := pragmaName(p)
			if other, dup := seen[name]; dup {
				t.Errorf("pragma %q объявлена и в %s, и в %s", name, other, list.class)
			}
			seen[name] = list.class
		}
	}
	// Классы по существу — из ADR-2 §6.1-6.3, а не из порядка в коде.
	if seen["auto_vacuum"] != "creation" {
		t.Errorf("auto_vacuum отнесён к %q, а он creation-only", seen["auto_vacuum"])
	}
	if seen["journal_mode"] != "persistent" {
		t.Errorf("journal_mode отнесён к %q, а он persistent", seen["journal_mode"])
	}
	for _, want := range []string{"synchronous", "foreign_keys", "busy_timeout", "temp_store", "cache_size"} {
		if seen[want] != "connection" {
			t.Errorf("%s отнесён к %q: на читателе он не будет выставлен", want, seen[want])
		}
	}
}

// Последовательность при СОЗДАНИИ: auto_vacuum строго до journal_mode.
// journal_mode=WAL пишет заголовок базы, после чего auto_vacuum молча остаётся
// нулём — ровно эта ошибка всплыла на первом прогоне фазы 0A.
func TestCreatePragmaOrder(t *testing.T) {
	list := createPragmaList(5000)
	idx := func(name string) int {
		return slices.IndexFunc(list, func(p string) bool { return pragmaName(p) == name })
	}
	av, jm := idx("auto_vacuum"), idx("journal_mode")
	if av < 0 || jm < 0 {
		t.Fatalf("в списке создания нет auto_vacuum или journal_mode: %v", list)
	}
	if av > jm {
		t.Fatalf("auto_vacuum (%d) идёт после journal_mode (%d): заголовок уже записан, "+
			"и auto_vacuum молча останется нулём", av, jm)
	}
}

// Открытие СУЩЕСТВУЮЩЕЙ базы не переустанавливает creation-only и persistent:
// на непустой базе auto_vacuum игнорируется, а journal_mode уже сохранён в
// файле. Пул читателей открывает соединения именно этим путём.
func TestOpenPragmaListHasNoCreationOrPersistent(t *testing.T) {
	open := openPragmaList(5000)
	for _, p := range append(creationPragmas(), persistentPragmas()...) {
		name := pragmaName(p)
		if slices.ContainsFunc(open, func(q string) bool { return pragmaName(q) == name }) {
			t.Errorf("при открытии существующей базы выполняется %q, хотя это не per-connection", name)
		}
	}
	if len(open) != len(connectionPragmas(5000)) {
		t.Errorf("список открытия %v не совпадает с per-connection %v", open, connectionPragmas(5000))
	}
}

// Проверка «заявлено против фактически» (ADR-2 §6.4). Ожидаемые значения взяты
// из ADR, а не из кода: WAL, foreign_keys=1, synchronous=NORMAL(1),
// auto_vacuum=INCREMENTAL(2), busy_timeout из настроек, cache_size -65536.
// Читаются они с СОЕДИНЕНИЯ ЧИТАТЕЛЯ — пропуск per-connection pragma на
// читателе проявился бы редко и не там, где его стали бы искать.
func TestStatusReportsActualPragmasOnReaderConnection(t *testing.T) {
	s := openTestStore(t, Options{})
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"foreign_keys": "1",
		"temp_store":   "2", // MEMORY
		"cache_size":   "-65536",
		"auto_vacuum":  "2", // INCREMENTAL
		"busy_timeout": itoa(int(DefaultBusyTimeout.Milliseconds())),
	}
	for name, expect := range want {
		got := st.Pragmas[name]
		if !strings.EqualFold(got, expect) {
			t.Errorf("pragma %s фактически %q, заявлено %q", name, got, expect)
		}
	}
	if st.Pragmas["page_size"] == "" {
		t.Error("page_size не попал в отчёт: фактические значения обязаны быть наблюдаемыми")
	}
}

// busy_timeout настраивается, и настройка обязана доезжать до читателей:
// именно они ждут блокировку, пока писатель держит транзакцию.
func TestBusyTimeoutOptionReachesReaders(t *testing.T) {
	s := openTestStore(t, Options{BusyTimeout: 7777 * 1e6})
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Pragmas["busy_timeout"] != "7777" {
		t.Errorf("busy_timeout на читателе %q, ожидалось 7777", st.Pragmas["busy_timeout"])
	}
}
