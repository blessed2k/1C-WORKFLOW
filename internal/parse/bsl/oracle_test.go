package bsl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// oracleSymbol/oracleDoc зеркалят структуру сырых ответов bsl-language-server
// (document_symbols), сохранённых в testdata/oracle/*.json. Нужны три поля,
// поэтому структура своя, без зависимости от клиента language server.
type oracleSymbol struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Detail   *string        `json:"detail"`
	Children []oracleSymbol `json:"children"`
}

type oracleDoc struct {
	File    string         `json:"file"`
	Symbols []oracleSymbol `json:"symbols"`
}

// oracleFixturesDir — записанные ответы bsl-language-server лежат в
// testdata/oracle пакета.
func oracleFixturesDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("testdata", "oracle")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("фикстуры оракула недоступны: %v", err)
	}
	return dir
}

// oracleMethods собирает счётчик параметров по имени метода из ответа
// bsl-language-server; detail вида "(А, Б?, В?)" — знак вопроса помечает
// необязательный параметр и на счёт не влияет.
func oracleMethods(nodes []oracleSymbol) map[string]int {
	out := map[string]int{}
	var walk func([]oracleSymbol)
	walk = func(ns []oracleSymbol) {
		for _, n := range ns {
			if n.Kind == "Method" {
				detail := "()"
				if n.Detail != nil {
					detail = strings.TrimSpace(*n.Detail)
				}
				inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(detail, "("), ")"))
				count := 0
				if inner != "" {
					count = len(strings.Split(inner, ","))
				}
				out[n.Name] = count
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	return out
}

// TestParseOracleCompare сверяет продакшн-парсер (Parse из этого пакета) с
// независимым oracle (bsl-language-server 1.0.3, document_symbols) —
// golden-требование §29 docs/architecture-index.md.
//
// Корпус — три файла реальной выгрузки УТ, для которых заранее записан ответ
// оракула (testdata/oracle/*.json). Путь к выгрузке — ONEC_DUMP/MCP1C_SPIKE_DUMP, тест скипается
// без неё же переменной, которой требует dumpRoot в corpus_test.go.
func TestParseOracleCompare(t *testing.T) {
	root := dumpRoot(t)
	dir := oracleFixturesDir(t)

	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("в %s нет ответов оракула: %v", dir, err)
	}
	sort.Strings(entries)

	var totalOracle, totalMine, totalDiff int
	for _, path := range entries {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var doc oracleDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		rel := strings.TrimPrefix(doc.File, "${ONEC_DUMP}")
		rel = strings.TrimPrefix(rel, "/")

		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s: исходник %s недоступен: %v", path, rel, err)
		}

		oracle := oracleMethods(doc.Symbols)
		totalOracle += len(oracle)

		mod, _ := Parse(src, Options{File: rel})
		mine := map[string]int{}
		for _, m := range mod.Methods {
			mine[m.Name] = len(m.Params)
		}
		totalMine += len(mine)

		var onlyOracle, onlyMine, paramDiff []string
		for name := range oracle {
			if _, ok := mine[name]; !ok {
				onlyOracle = append(onlyOracle, name)
			}
		}
		for name := range mine {
			if _, ok := oracle[name]; !ok {
				onlyMine = append(onlyMine, name)
			}
		}
		for name, n := range oracle {
			if m, ok := mine[name]; ok && m != n {
				paramDiff = append(paramDiff, fmt.Sprintf("%s: оракул=%d парсер=%d", name, n, m))
			}
		}
		diff := len(onlyOracle) + len(onlyMine) + len(paramDiff)
		totalDiff += diff

		if diff > 0 {
			sort.Strings(onlyOracle)
			sort.Strings(onlyMine)
			sort.Strings(paramDiff)
			t.Errorf("%s: методовУОракула=%d методовУПарсера=%d толькоУОракула=%v толькоУПарсера=%v расхожденияПоПараметрам=%v",
				rel, len(oracle), len(mine), onlyOracle, onlyMine, paramDiff)
		} else {
			t.Logf("%s: методовУОракула=%d методовУПарсера=%d совпало", rel, len(oracle), len(mine))
		}
	}

	t.Logf("итого: методовУОракула=%d методовУПарсера=%d расхождений=%d", totalOracle, totalMine, totalDiff)
}
