package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dumpEnvVars — переменные окружения, из которых берётся путь к реальной
// выгрузке. Имена совпадают с internal/parse/bsl/corpus_test.go, путь в код
// не зашит.
var dumpEnvVars = []string{"ONEC_DUMP", "MCP1C_SPIKE_DUMP"}

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

func findBSLFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
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

// --- извлечение текстов запросов из .bsl (только для этого теста) ---
//
// Это не второй парсер запросов: ниже декодируется исключительно BSL-синтаксис
// строкового литерала (экранирование "" и построчная склейка "|"), плюс
// небольшая эвристика конкатенации "литерал + нелитерал + литерал", которая
// заменяет нелитеральный операнд на GapMarker — контракт, который Parse и так
// обязан понимать. Сам текст запроса (ВЫБРАТЬ/ИЗ/...) этим кодом не
// разбирается вообще: он идёт в Parse как есть.

// decodeBSLStringLiteral декодирует BSL-строковый литерал, начинающийся с '"'
// на позиции start: снимает экранирование двойной кавычки и склейку строк по
// "|". ok=false — литерал не закрыт до конца файла (разбор всё равно
// продолжается лучшим известным результатом).
func decodeBSLStringLiteral(src []byte, start int) (text string, end int, ok bool) {
	var buf strings.Builder
	i := start + 1
	for i < len(src) {
		if src[i] == '"' {
			if i+1 < len(src) && src[i+1] == '"' {
				buf.WriteByte('"')
				i += 2
				continue
			}
			return stripLineContinuation(buf.String()), i + 1, true
		}
		buf.WriteByte(src[i])
		i++
	}
	return stripLineContinuation(buf.String()), i, false
}

// stripLineContinuation снимает ведущее "|" (и пробелы перед ним) с каждой
// строки, кроме первой — так BSL склеивает многострочный строковый литерал в
// одну строку текста запроса во время выполнения.
func stripLineContinuation(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " \t")
		if strings.HasPrefix(trimmed, "|") {
			lines[i] = trimmed[len("|"):]
		}
	}
	return strings.Join(lines, "\n")
}

// skipBSLNoise пропускает пробелы, переводы строк и "//"-комментарии BSL.
func skipBSLNoise(src []byte, i int) int {
	for i < len(src) {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		break
	}
	return i
}

// skipNonLiteralOperand пропускает один операнд конкатенации, который не
// является строковым литералом (переменная, вызов функции, выражение в
// скобках), до ближайшего '+' или ';' на той же глубине скобок, до перевода
// строки вне скобок или до конца файла.
func skipNonLiteralOperand(src []byte, i int) int {
	depth := 0
	for i < len(src) {
		switch src[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		case '+':
			if depth == 0 {
				return i
			}
		case ';':
			if depth == 0 {
				return i
			}
		case '"':
			_, end, _ := decodeBSLStringLiteral(src, i)
			i = end
			continue
		case '\n':
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return i
}

// extractQueryTexts находит в исходнике BSL строковые литералы, похожие на
// текст запроса 1С (содержат ВЫБРАТЬ/SELECT), и склеивает их с последующими
// операндами конкатенации: литерал — как есть, нелитеральный операнд —
// GapMarker'ом. Результат — реалистичный вход для Parse: static, когда весь
// текст оказался одним литералом, иначе с зафиксированными дырами.
func extractQueryTexts(src []byte) []string {
	var out []string
	i := 0
	for i < len(src) {
		if src[i] != '"' {
			i++
			continue
		}
		chunk, end, _ := decodeBSLStringLiteral(src, i)
		low := strings.ToLower(chunk)
		if !strings.Contains(low, "выбрать") && !strings.Contains(low, "select") {
			i = end
			continue
		}
		var text strings.Builder
		text.WriteString(chunk)
		pos := end
		for {
			j := skipBSLNoise(src, pos)
			if j >= len(src) || src[j] != '+' {
				break
			}
			j = skipBSLNoise(src, j+1)
			if j < len(src) && src[j] == '"' {
				nextChunk, nextEnd, _ := decodeBSLStringLiteral(src, j)
				text.WriteString(nextChunk)
				pos = nextEnd
				continue
			}
			text.WriteString(GapMarker)
			pos = skipNonLiteralOperand(src, j)
		}
		out = append(out, text.String())
		i = end
	}
	return out
}

// independentParamCount считает "&Идентификатор" в text без обращения к
// лексеру query — простым байтовым сканом, игнорирующим содержимое двойных
// кавычек (без учёта экранирования "": для оценочного независимого ориентира
// этого достаточно, редкий случай "&" в самом литерале строки запроса даёт
// расхождение на единицы при тысячах совпадений). Независимая от Parse
// реализация — сверка, а не тавтология.
func independentParamCount(text string) int {
	n := 0
	inString := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c != '&' {
			continue
		}
		if i+1 < len(text) {
			r := rune(text[i+1])
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r > 127 {
				n++
			}
		}
	}
	return n
}

// TestParseКорпусРеальнойВыгрузки прогоняет Parse по текстам запросов,
// извлечённым из всех .bsl реальной выгрузки (ONEC_DUMP). Два свойства
// утверждаются, а не логируются: разбор не паникует ни на одном тексте, и
// суммарное число найденных параметров сходится с independentParamCount —
// независимым от Parse ориентиром, который может разойтись, если сканирование
// параметров сломано.
//
// Скипается, если ONEC_DUMP не задана. Путь к выгрузке в тест не зашит.
func TestParseКорпусРеальнойВыгрузки(t *testing.T) {
	root := dumpRoot(t)
	paths := findBSLFiles(t, root)
	if len(paths) == 0 {
		t.Fatal("в выгрузке не найдено ни одного .bsl")
	}

	type panicInfo struct {
		file string
		text string
		val  interface{}
	}
	var (
		panics                             []panicInfo
		queries, tables, fields, tempCount int
		wantParams, gotParams              int
		staticCount, partialCount          int
		filesWithQueries                   int
	)

	start := time.Now()
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		texts := extractQueryTexts(src)
		if len(texts) == 0 {
			continue
		}
		filesWithQueries++
		for _, text := range texts {
			queries++
			wantParams += independentParamCount(text)

			q, panicVal := parseRecovering(text)
			if panicVal != nil {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				panics = append(panics, panicInfo{file: rel, text: text, val: panicVal})
				continue
			}
			tables += len(q.Tables)
			fields += len(q.Fields)
			tempCount += len(q.TempTables)
			gotParams += len(q.Parameters)
			switch q.Staticity {
			case StaticityStatic:
				staticCount++
			case StaticityPartial:
				partialCount++
			}
			if q.Confidence <= 0 || q.Confidence > 1 {
				t.Errorf("%s: Confidence вне (0,1]: %v (text=%q)", path, q.Confidence, text)
			}
			if q.Staticity != StaticityStatic && q.Confidence >= 1 {
				t.Errorf("%s: staticity=%s, но confidence=%v (>=1) — нарушение архитектуры §14",
					path, q.Staticity, q.Confidence)
			}
		}
	}
	elapsed := time.Since(start)

	t.Logf("корпус: файлов=%d файлов_с_запросами=%d текстов=%d static=%d partial=%d таблиц=%d полей=%d ВТ=%d параметров(Parse)=%d параметров(ориентир)=%d время=%s",
		len(paths), filesWithQueries, queries, staticCount, partialCount, tables, fields, tempCount, gotParams, wantParams, elapsed)

	// Критерий приёмки: «ноль паник» — утверждение, а не строка в логе.
	if len(panics) != 0 {
		for _, p := range panics {
			t.Errorf("паника при разборе %s: %v\nтекст: %.300q", p.file, p.val, p.text)
		}
	}

	if queries == 0 {
		t.Fatal("в выгрузке не найдено ни одного текста запроса — извлечение сломано или ONEC_DUMP указывает не туда")
	}
	if tables == 0 {
		t.Error("на всём корпусе не найдено ни одной таблицы — разбор ИЗ/FROM сломан")
	}

	// Независимый ориентир: параметры считаются двумя разными реализациями
	// (Parse и independentParamCount). Расхождение допускается только в
	// пределах 2% — редкие "&" внутри строковых литералов текста запроса
	// (ПОДОБНО "...&...") independentParamCount не отфильтровывает идеально.
	if wantParams > 0 {
		diff := gotParams - wantParams
		if diff < 0 {
			diff = -diff
		}
		if tolerance := wantParams/50 + 1; diff > tolerance {
			t.Errorf("параметров по Parse=%d, по независимому ориентиру=%d, расхождение %d больше допуска %d",
				gotParams, wantParams, diff, tolerance)
		}
	}
}

// parseRecovering вызывает Parse, перехватывая панику, чтобы прогон корпуса
// проверил все тексты, а не только тексты до первой паники.
func parseRecovering(text string) (q *Query, panicVal interface{}) {
	defer func() {
		if r := recover(); r != nil {
			panicVal = r
		}
	}()
	q, _ = Parse(text)
	return
}
