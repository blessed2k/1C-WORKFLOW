package bsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// dumpEnvVars — переменные окружения, из которых берётся путь к реальной
// выгрузке. Имя ONEC_DUMP общее для всех real-dump тестов, путь не хардкодится.
var dumpEnvVars = []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"}

// dumpRoot возвращает корень реальной выгрузки или сообщает, что переменная
// не задана — тест скипается, а не падает.
func dumpRoot(t *testing.T) string {
	t.Helper()
	for _, env := range dumpEnvVars {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if _, err := os.Stat(v); err != nil {
				t.Skipf("%s указывает на недоступный путь: %v", env, err)
			}
			return v
		}
	}
	t.Skip("переменная окружения ONEC_DUMP не задана — прогон на реальной выгрузке пропущен")
	return ""
}

// findBSLFiles собирает пути всех .bsl под корнем выгрузки.
func findBSLFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // недоступный подкаталог не должен ронять прогон
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".bsl") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("обход выгрузки: %v", err)
	}
	return out
}

// countClosingLines считает строки, начинающиеся с закрывающего ключевого
// слова метода. Независимый от парсера ориентир: строится посимвольно,
// без единого обращения к Parse. У каждого закрытого метода такая строка
// ровно одна, поэтому число найденных методов не может быть заметно меньше
// или больше числа строк закрытия.
func countClosingLines(src []byte) int {
	keywords := []string{"конецпроцедуры", "конецфункции", "endprocedure", "endfunction"}
	n := 0
	for _, line := range strings.Split(string(src), "\n") {
		s := strings.ToLower(strings.TrimSpace(line))
		for _, kw := range keywords {
			if !strings.HasPrefix(s, kw) {
				continue
			}
			rest := strings.TrimSpace(s[len(kw):])
			if rest == "" || rest == ";" || strings.HasPrefix(rest, "//") || strings.HasPrefix(rest, "; //") {
				n++
			}
			break
		}
	}
	return n
}

// TestParseКорпусРеальнойВыгрузки прогоняет парсер на всём BSL реальной
// выгрузки (ONEC_DUMP). Проверяются два свойства, которые осмысленны на
// 12+ тысячах файлов и не требуют оракула: парсер не паникует ни на одном
// файле, и любой выданный span удовлетворяет контракту §18.3. Число найденных
// методов сверяется с независимым от парсера ориентиром countClosingLines.
//
// Скипается, если ONEC_DUMP не задана. Путь к выгрузке в тест не зашит.
func TestParseКорпусРеальнойВыгрузки(t *testing.T) {
	root := dumpRoot(t)
	paths := findBSLFiles(t, root)
	if len(paths) == 0 {
		t.Fatal("в выгрузке не найдено ни одного .bsl")
	}

	type panicInfo struct {
		path string
		val  interface{}
	}
	var (
		panics            []panicInfo
		methods, closings int64
		bytesTotal        int64
		filesWithDiags    int
		incomplete        int
	)

	start := time.Now()
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			continue // недоступный файл не должен ронять прогон
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		bytesTotal += int64(len(src))
		closings += int64(countClosingLines(src))

		mod, diags, panicVal := parseRecovering(src, Options{File: filepath.ToSlash(rel)})
		if panicVal != nil {
			panics = append(panics, panicInfo{path: path, val: panicVal})
			continue
		}
		checkSpans(t, src, mod, diags, path)

		if len(diags) > 0 {
			filesWithDiags++
		}
		for _, m := range mod.Methods {
			methods++
			if !m.Complete {
				incomplete++
			}
		}
	}
	elapsed := time.Since(start)

	mbPerSec := float64(bytesTotal) / (1 << 20) / elapsed.Seconds()
	filesPerSec := float64(len(paths)) / elapsed.Seconds()
	t.Logf("корпус: файлов=%d байт=%d методов=%d строк_закрытия=%d файлов_с_диагностиками=%d незакрытых=%d время=%s скорость=%.1f МБ/с (%.0f файлов/с)",
		len(paths), bytesTotal, methods, closings, filesWithDiags, incomplete, elapsed, mbPerSec, filesPerSec)

	// Критерий приёмки: «ноль паник» — это утверждение, а не строка в логе.
	if len(panics) != 0 {
		for _, p := range panics {
			t.Errorf("паника при разборе %s: %v", p.path, p.val)
		}
	}

	// Независимый ориентир (без участия парсера): методов не может быть
	// меньше числа строк закрытия (иначе объявления теряются) и не может
	// быть заметно больше (иначе объявления выдумываются). Верхняя граница
	// взята с запасом 2%: закрывающее слово изредка стоит не первым в строке
	// («А = Б; КонецПроцедуры» и подобное), и ориентир такие случаи недосчитывает.
	if methods < closings {
		t.Errorf("методов %d меньше, чем строк закрытия %d: разбор теряет объявления", methods, closings)
	}
	if upper := closings + closings/50; methods > upper {
		t.Errorf("методов %d больше верхней границы %d при %d строках закрытия: разбор выдумывает объявления",
			methods, upper, closings)
	}
	if methods == 0 {
		t.Error("на выгрузке не найдено ни одного метода")
	}
}

// parseRecovering вызывает Parse, перехватывая панику, чтобы прогон корпуса
// не останавливался на первом сломанном файле — все файлы должны быть
// проверены, а не только файлы до первой паники.
func parseRecovering(src []byte, opts Options) (mod *Module, diags []domain.Diagnostic, panicVal interface{}) {
	defer func() {
		if r := recover(); r != nil {
			panicVal = r
		}
	}()
	mod, diags = Parse(src, opts)
	return
}
