package main

import (
	"os"
	"path/filepath"
	"testing"
)

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// TestWriteBatches: ответы моделей переживают повторный запуск только при том
// же входе и том же задании; иначе остаток прежнего запуска это ошибка, а
// clean удаляет его всегда.
func TestWriteBatches(t *testing.T) {
	rows := []writerIn{{ID: "t1", Snippet: "а"}, {ID: "t2", Snippet: "б"}, {ID: "t3", Snippet: "в"}}
	const prompt = "задание"

	dir := t.TempDir()
	n, err := writeBatches(dir, prompt, rows, 2, false)
	if err != nil || n != 2 || !exists(dir, "in-01.jsonl") || !exists(dir, "in-02.jsonl") || !exists(dir, "PROMPT.md") {
		t.Fatalf("первая запись: пачек %d, ошибка %v", n, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out-01.jsonl"), []byte("ответ"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Тот же вход и то же задание: ответ остаётся.
	if _, err := writeBatches(dir, prompt, rows, 2, false); err != nil || !exists(dir, "out-01.jsonl") {
		t.Errorf("повторный запуск с тем же входом: ошибка %v, ответ на месте: %v", err, exists(dir, "out-01.jsonl"))
	}
	// Другое задание при том же входе: ответы написаны по старому заданию.
	if _, err := writeBatches(dir, "новое задание", rows, 2, false); err == nil {
		t.Errorf("смена задания при оставшихся ответах прошла без ошибки")
	}
	// Другой вход: ошибка без clean, с clean ответы удалены.
	changed := append([]writerIn(nil), rows...)
	changed[0].Snippet = "другой фрагмент"
	if _, err := writeBatches(dir, prompt, changed, 2, false); err == nil {
		t.Errorf("другой вход при оставшихся ответах прошёл без ошибки")
	}
	if _, err := writeBatches(dir, prompt, changed, 2, true); err != nil || exists(dir, "out-01.jsonl") {
		t.Errorf("clean: ошибка %v, ответ остался: %v", err, exists(dir, "out-01.jsonl"))
	}
	// clean удаляет ответы и при том же входе: флаг делает то, что обещает.
	if err := os.WriteFile(filepath.Join(dir, "out-01.jsonl"), []byte("ответ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBatches(dir, prompt, changed, 2, true); err != nil || exists(dir, "out-01.jsonl") {
		t.Errorf("clean при том же входе: ошибка %v, ответ остался: %v", err, exists(dir, "out-01.jsonl"))
	}
	// Меньше пачек, чем лежит: лишняя пачка прежнего запуска уходит с clean.
	if n, err := writeBatches(dir, prompt, changed[:1], 2, true); err != nil || n != 1 || exists(dir, "in-02.jsonl") {
		t.Errorf("вход короче прежнего: пачек %d, ошибка %v, старая пачка осталась: %v", n, err, exists(dir, "in-02.jsonl"))
	}
	if _, err := writeBatches(dir, prompt, rows, 0, false); err == nil {
		t.Errorf("нулевой размер пачки принят")
	}
}

// TestTasksOptionsValidate: отрицательные квоты и неположительный размер
// пачки отвергаются до работы с индексом.
func TestTasksOptionsValidate(t *testing.T) {
	good := tasksOptions{Core: 1, Rest: 0, Other: 5, Seed: 1, Batch: 10}
	if err := good.validate(); err != nil {
		t.Errorf("годные параметры отвергнуты: %v", err)
	}
	for name, bad := range map[string]tasksOptions{
		"отрицательная квота": {Core: -1, Batch: 10},
		"нулевая пачка":       {Core: 1, Batch: 0},
	} {
		if err := bad.validate(); err == nil {
			t.Errorf("%s: параметры приняты", name)
		}
	}
}
