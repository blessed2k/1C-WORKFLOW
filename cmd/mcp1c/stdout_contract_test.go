package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Шов 2 в чистом виде: собранный сервер поднимается подпроцессом на stdio, и
// проверяется, что каждый байт его stdout — кадр JSON-RPC, а всё, что сервер
// хочет сказать людям, уходит в stderr. История 59 спецификации (R58, R59):
// один посторонний байт в stdout ломает протокол у любого клиента.
//
// Тест сам говорит по протоколу, без SDK: SDK по построению пишет корректные
// кадры, и проверка через него доказывала бы только саму себя.

// кадрJSONRPC — минимальный разбор строки stdout: этого достаточно, чтобы
// отличить кадр протокола от постороннего вывода.
type кадрJSONRPC struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func TestКонтрактЧистотыStdout(t *testing.T) {
	двоичный := собратьСервер(t)
	выгрузка, err := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "dump"))
	if err != nil {
		t.Fatalf("путь к фикстуре: %v", err)
	}

	запросы := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"contract-stdio","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"server_info","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_object_structure","arguments":{"type":"Catalog","name":"Товары"}}}`,
		// Ошибка инструмента: диагностика обязана уехать клиенту кадром, а не в stdout текстом.
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_object_structure","arguments":{"type":"Catalog","name":"НетТакогоСправочника"}}}`,
		// Ошибка протокола: несуществующий инструмент.
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"нет_такого_инструмента","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"resources/list","params":{}}`,
		`{"jsonrpc":"2.0","id":8,"method":"prompts/list","params":{}}`,
		// Индексный инструмент без активного проекта: actionable-ошибка тем же
		// путём, что и остальные — ни байта в stdout мимо кадра JSON-RPC.
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"index_status","arguments":{}}}`,
	}

	вывод, ошибки := прогнатьСессию(t, exec.Command(двоичный, "--dump", выгрузка), запросы, []int{1, 2, 3, 4, 5, 6, 7, 8, 9})

	кадры := разобратьКадры(t, вывод)
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		if _, есть := кадры[id]; !есть {
			t.Errorf("нет ответа на запрос id=%s; получено %d кадров", id, len(кадры))
		}
	}
	if кадры["6"].Error == nil && кадры["6"].Result == nil {
		t.Error("вызов несуществующего инструмента остался без ответа")
	}
	if ошибки != "" {
		t.Logf("stderr сервера: %.400s", ошибки)
	}
}

// TestКонтрактЛогиВStderr: сервер, которому дали негодный файл баз, обязан
// сказать об этом в stderr и не запачкать stdout.
func TestКонтрактЛогиВStderr(t *testing.T) {
	двоичный := собратьСервер(t)
	несуществующий := filepath.Join(t.TempDir(), "bases.json")

	запросы := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"contract-stdio","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}

	вывод, ошибки := прогнатьСессию(t, exec.Command(двоичный, "--bases", несуществующий), запросы, []int{1, 2})

	разобратьКадры(t, вывод)
	if !strings.Contains(ошибки, serverName) {
		t.Errorf("сообщение о негодном файле баз не попало в stderr; stderr = %q", ошибки)
	}
	if strings.Contains(вывод, несуществующий) {
		t.Errorf("путь из сообщения об ошибке оказался в stdout: %.200s", вывод)
	}
}

// собратьСервер собирает бинарь так же, как его получает пользователь.
// Сама сборка — общая с main_dispatch_test.go (buildMCP1C): расширение
// исполняемого файла на Windows обязано быть учтено в одном месте.
func собратьСервер(t *testing.T) string {
	t.Helper()
	return buildMCP1C(t)
}

// прогнатьСессию скармливает серверу запросы по stdin и возвращает весь его
// stdout и stderr. Чтение идёт, пока не придут ответы на все ожидаемые id
// (порядок ответов не гарантирован), потом stdin закрывается и дочитывается
// хвост: «каждый байт» — это в том числе хвост.
func прогнатьСессию(t *testing.T, cmd *exec.Cmd, запросы []string, ожидаемыеID []int) (string, string) {
	t.Helper()
	ввод, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	вывод, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	var буферОшибок bytes.Buffer
	cmd.Stderr = &буферОшибок
	if err := cmd.Start(); err != nil {
		t.Fatalf("запуск сервера: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	for _, запрос := range запросы {
		if _, err := io.WriteString(ввод, запрос+"\n"); err != nil {
			t.Fatalf("запись запроса: %v", err)
		}
	}

	ждём := map[string]bool{}
	for _, id := range ожидаемыеID {
		ждём[strconv.Itoa(id)] = true
	}

	собранный := make(chan string, 1)
	go func() {
		var накопленный bytes.Buffer
		чтение := bufio.NewScanner(вывод)
		чтение.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for чтение.Scan() {
			накопленный.Write(чтение.Bytes())
			накопленный.WriteByte('\n')
			delete(ждём, идКадра(чтение.Bytes()))
			if len(ждём) == 0 {
				break
			}
		}
		// stdin закрыт — сервер завершает сессию; хвост stdout тоже часть контракта.
		_ = ввод.Close()
		хвост, _ := io.ReadAll(вывод)
		накопленный.Write(хвост)
		собранный <- накопленный.String()
	}()

	select {
	case текст := <-собранный:
		_ = cmd.Wait()
		return текст, буферОшибок.String()
	case <-time.After(60 * time.Second):
		t.Fatal("сервер не ответил за 60 с")
		return "", ""
	}
}

// идКадра достаёт id ответа; у уведомлений и мусора его нет.
func идКадра(строка []byte) string {
	var кадр кадрJSONRPC
	if json.Unmarshal(строка, &кадр) != nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(string(кадр.ID)), `"`)
}

// разобратьКадры проверяет, что весь stdout состоит из кадров JSON-RPC, и
// возвращает их по id.
func разобратьКадры(t *testing.T, вывод string) map[string]кадрJSONRPC {
	t.Helper()
	if вывод == "" {
		t.Fatal("сервер не написал в stdout ни одного кадра")
	}
	if !strings.HasSuffix(вывод, "\n") {
		t.Errorf("stdout обрывается на неполной строке: %.200s", хвостСтроки(вывод))
	}
	кадры := map[string]кадрJSONRPC{}
	for номер, строка := range strings.Split(вывод, "\n") {
		if строка == "" {
			continue
		}
		var кадр кадрJSONRPC
		if err := json.Unmarshal([]byte(строка), &кадр); err != nil {
			t.Fatalf("строка %d stdout — не JSON (%v): %.200s", номер+1, err, строка)
		}
		if кадр.JSONRPC != "2.0" {
			t.Fatalf("строка %d stdout — не кадр JSON-RPC 2.0: %.200s", номер+1, строка)
		}
		if кадр.Method == "" && кадр.Result == nil && кадр.Error == nil {
			t.Fatalf("строка %d stdout — кадр без method, result и error: %.200s", номер+1, строка)
		}
		if len(кадр.ID) > 0 {
			кадры[strings.Trim(string(кадр.ID), `"`)] = кадр
		}
	}
	return кадры
}

func хвостСтроки(текст string) string {
	if i := strings.LastIndex(текст, "\n"); i >= 0 {
		return текст[i+1:]
	}
	return текст
}
