package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Замер поведения агента. Остальные шаги этой программы отвечают на вопрос,
// находит ли поиск готовый метод. Этот отвечает на другой: зовёт ли агент
// готовый метод, когда его просят написать функцию, или пишет свою.
//
// Задача: заголовок функции из файла черновиков (имя и назначение одной
// строкой, написаны моделью вслепую по запросу пары). Агент получает пустой
// модуль и просьбу написать эту функцию; про поиск готового в просьбе ни
// слова. Вырезанный вызов из настоящего модуля задачей не годится: модуль
// лежит в выгрузке, и агент прочёл бы ответ.
//
// Плечо: один и тот же агент (модель, просьба, набор инструментов, файлы
// CLAUDE.md) с разным сервером 1С и разными хуками. Запуски идут по одному,
// каждый отдельным процессом CLI со своим --mcp-config: настройки
// пользователя не меняются.
//
// Счёт машинный только в одну сторону: вызов засчитываемого метода в
// написанном модуле найден. Остальное (другой готовый метод, свой код) читает
// человек: шаг печатает квалифицированные вызовы каждого такого модуля.

// agentModule: имя файла модуля в каталоге запуска.
const agentModule = "Module.bsl"

// agentSkeleton: модуль, который получает агент.
const agentSkeleton = "#Область ПрограммныйИнтерфейс\n\n#КонецОбласти\n\n#Область СлужебныеПроцедурыИФункции\n\n#КонецОбласти\n"

// agentTask: одна задача замера.
type agentTask struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
	// Accept: засчитываемые выражения вызова пары. Агенту не показываются.
	Accept []string `json:"accept"`
}

// pickAgentTasks берёт по perSection задач на секцию из проверочной половины.
// Порядок задан зерном и идентификатором пары (orderKey), на одно семейство
// метода берётся одна задача: два черновика про один метод дали бы один и тот
// же исход дважды.
func pickAgentTasks(pairs []pair, drafts []draft, perSection, seed int) ([]agentTask, error) {
	byID := map[string]pair{}
	for _, p := range pairs {
		byID[p.ID] = p
	}
	rows := append([]draft(nil), drafts...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := orderKey(seed, rows[i].ID), orderKey(seed, rows[j].ID)
		if a != b {
			return a < b
		}
		return rows[i].ID < rows[j].ID
	})
	taken := map[string]int{}
	families := map[string]bool{}
	var out []agentTask
	for _, d := range rows {
		p, ok := byID[d.ID]
		if !ok {
			return nil, fmt.Errorf("черновик %s: такой пары в наборе нет", d.ID)
		}
		if p.Split != splitTest || len(p.Accept) == 0 || taken[p.Section] >= perSection {
			continue
		}
		family := callFamily(p.Accept[0])
		if families[family] {
			continue
		}
		families[family] = true
		taken[p.Section]++
		out = append(out, agentTask{ID: d.ID, Section: p.Section, Name: d.Name, Comment: d.Comment, Accept: p.Accept})
	}
	for _, section := range []string{"bsp", "other"} {
		if taken[section] < perSection {
			return nil, fmt.Errorf("секция %s: в проверочной половине %d задач, нужно %d", section, taken[section], perSection)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// agentPrompt: просьба агенту. Одна на оба плеча; готовые методы и поиск в
// ней не упоминаются: мерится, вспомнит ли о них агент сам.
func agentPrompt(t agentTask, dump string) string {
	return fmt.Sprintf(`Конфигурация 1С лежит выгрузкой в каталоге %s. В неё добавляется новый общий модуль, его файл %s лежит в текущем каталоге и пока пуст.

Напиши в этом модуле экспортный метод %s. Назначение: %s

Параметры и тип результата определи по назначению сам. Нужен рабочий код метода целиком, сохранённый в файл модуля. Вопросов не задавай: задачу никто не уточнит.`,
		dump, agentModule, t.Name, strings.TrimSpace(t.Comment))
}

// agentEnv убирает из окружения переменные сеанса Claude Code, из которого
// запущен прогон: дочерний CLI иначе считал бы себя частью этого сеанса.
func agentEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "CLAUDE") || strings.HasPrefix(upper, "ANTHROPIC_") || upper == "AI_AGENT" || upper == "BAGGAGE" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// agentRunResult: чем кончился запуск. Лежит рядом с журналом; по нему
// повторный прогон решает, что задача уже сделана.
type agentRunResult struct {
	Exit     int     `json:"exit"`
	Seconds  float64 `json:"seconds"`
	TimedOut bool    `json:"timedOut"`
}

func runAgentTasks(args []string) error {
	fs := flag.NewFlagSet("agent-tasks", flag.ExitOnError)
	data := fs.String("data", "", "файл набора (обязателен)")
	drafts := fs.String("drafts", "", "файл черновиков (обязателен)")
	n := fs.Int("n", 12, "задач на секцию")
	seed := fs.Int("seed", 1, "зерно выборки")
	out := fs.String("out", "", "куда записать задачи (обязателен)")
	fs.Parse(args)
	if *data == "" || *drafts == "" || *out == "" {
		return errors.New("agent-tasks: флаги -data, -drafts и -out обязательны")
	}
	pairs, err := readJSONLFile[pair](*data)
	if err != nil {
		return err
	}
	rows, err := readJSONLFile[draft](*drafts)
	if err != nil {
		return err
	}
	tasks, err := pickAgentTasks(pairs, rows, *n, *seed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := writeJSONL(*out, tasks); err != nil {
		return err
	}
	fmt.Printf("задач: %d → %s\n", len(tasks), *out)
	return nil
}

func runAgentRun(args []string) error {
	fs := flag.NewFlagSet("agent-run", flag.ExitOnError)
	tasksPath := fs.String("tasks", "", "файл задач от agent-tasks (обязателен)")
	arm := fs.String("arm", "", "имя плеча, оно же имя каталога запусков (обязателен)")
	cli := fs.String("claude", "", "исполняемый файл CLI Claude Code (обязателен)")
	mcpConfig := fs.String("mcp-config", "", "файл с сервером 1С этого плеча (обязателен); остальные серверы пользователя не подключаются")
	settings := fs.String("settings", "", "настройки плеча: файл или JSON; пусто: настройки пользователя как есть")
	model := fs.String("model", "claude-opus-5-5", "модель агента")
	dump := fs.String("dump", firstEnv("ONEC_DUMP", "MCP1C_SPIKE_DUMP"), "каталог выгрузки, называется агенту в просьбе")
	out := fs.String("out", "", "каталог запусков, вне репозитория (обязателен): его CLAUDE.md не должен попасть агенту")
	timeout := fs.Duration("timeout", 15*time.Minute, "потолок времени одного хода")
	limit := fs.Int("limit", 0, "сколько задач запустить за этот вызов; 0: все оставшиеся")
	prefix := fs.String("prefix", "", "что поставить перед просьбой, например команду скилла: \"/1c-dev:plan \"")
	followup := fs.String("followup", "", "второй ход в той же сессии, например \"Утверждаю план, делай.\"; пусто: один ход")
	pluginDir := fs.String("plugin-dir", "", "каталог плагина, подключаемого на время запуска")
	fs.Parse(args)
	if *tasksPath == "" || *arm == "" || *cli == "" || *mcpConfig == "" || *out == "" || *dump == "" {
		return errors.New("agent-run: флаги -tasks, -arm, -claude, -mcp-config, -dump и -out обязательны")
	}
	tasks, err := readJSONLFile[agentTask](*tasksPath)
	if err != nil {
		return err
	}
	started := 0
	for _, t := range tasks {
		dir := filepath.Join(*out, *arm, t.ID)
		if agentRunDone(dir) {
			continue
		}
		if *limit > 0 && started >= *limit {
			break
		}
		started++
		res, err := agentRunOne(dir, t, agentArm{
			CLI: *cli, MCPConfig: *mcpConfig, Settings: *settings, Model: *model, Dump: *dump,
			Timeout: *timeout, Prefix: *prefix, Followup: *followup, PluginDir: *pluginDir,
		})
		if err != nil {
			return fmt.Errorf("задача %s: %w", t.ID, err)
		}
		fmt.Printf("%s/%s %s: выход %d за %.0f с\n", *arm, t.ID, t.Name, res.Exit, res.Seconds)
	}
	fmt.Printf("плечо %s: запущено %d\n", *arm, started)
	return nil
}

// agentRunDone: запуск уже состоялся и кончился сам (не по потолку времени и
// не сбоем CLI).
func agentRunDone(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		return false
	}
	var res agentRunResult
	return json.Unmarshal(raw, &res) == nil && res.Exit == 0 && !res.TimedOut
}

// agentArm: чем плечо отличается от другого и как зовётся CLI.
type agentArm struct {
	CLI, MCPConfig, Settings, Model, Dump string
	Timeout                               time.Duration
	// Prefix ставится перед просьбой: так вызывается скилл.
	Prefix string
	// Followup: второй ход в той же сессии. Скилл, который кончается планом на
	// утверждение, кода на первом ходу не пишет; второй ход его утверждает.
	Followup string
	// PluginDir: плагин, подключаемый на время запуска (скилл ещё не установлен).
	PluginDir string
}

// agentTurns: ходы запуска по порядку.
func agentTurns(t agentTask, arm agentArm) []string {
	turns := []string{arm.Prefix + agentPrompt(t, arm.Dump)}
	if arm.Followup != "" {
		turns = append(turns, arm.Followup)
	}
	return turns
}

// agentArgs: аргументы CLI для хода номер turn (с нуля) сессии session.
func agentArgs(prompt string, turn int, session string, arm agentArm) []string {
	args := []string{"-p", prompt}
	if turn == 0 {
		args = append(args, "--session-id", session)
	} else {
		args = append(args, "--resume", session)
	}
	args = append(args,
		"--model", arm.Model,
		"--output-format", "stream-json", "--verbose",
		"--strict-mcp-config", "--mcp-config", arm.MCPConfig,
		"--permission-mode", "acceptEdits",
		"--allowedTools", "Read,Write,Edit,Glob,Grep,mcp__1c-workflowtimur",
		"--disallowedTools", "Bash,Agent,Task,WebSearch,WebFetch",
	)
	if arm.Settings != "" {
		args = append(args, "--settings", arm.Settings)
	}
	if arm.PluginDir != "" {
		args = append(args, "--plugin-dir", arm.PluginDir)
	}
	return args
}

// newSessionID: случайный идентификатор сессии в виде UUID версии 4.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// agentRunOne готовит каталог задачи заново и запускает в нём CLI: по одному
// процессу на ход, журналы ходов пишутся подряд в один файл.
func agentRunOne(dir string, t agentTask, arm agentArm) (agentRunResult, error) {
	var res agentRunResult
	if err := os.RemoveAll(dir); err != nil {
		return res, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join(dir, agentModule), []byte(agentSkeleton), 0o644); err != nil {
		return res, err
	}
	stdout, err := os.Create(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		return res, err
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr.txt"))
	if err != nil {
		return res, err
	}
	defer stderr.Close()
	session, err := newSessionID()
	if err != nil {
		return res, err
	}

	begin := time.Now()
	for turn, prompt := range agentTurns(t, arm) {
		ctx, cancel := context.WithTimeout(context.Background(), arm.Timeout)
		cmd := exec.CommandContext(ctx, arm.CLI, agentArgs(prompt, turn, session, arm)...)
		cmd.Dir = dir
		cmd.Env = agentEnv(os.Environ())
		cmd.Stdout, cmd.Stderr = stdout, stderr
		runErr := cmd.Run()
		res.TimedOut = ctx.Err() != nil
		cancel()
		var exit *exec.ExitError
		switch {
		case runErr == nil:
		case errors.As(runErr, &exit):
			res.Exit = exit.ExitCode()
		default:
			return res, runErr
		}
		if res.Exit != 0 || res.TimedOut {
			break
		}
	}
	res.Seconds = time.Since(begin).Seconds()
	raw, err := json.Marshal(res)
	if err != nil {
		return res, err
	}
	return res, os.WriteFile(filepath.Join(dir, "result.json"), raw, 0o644)
}

// stripBSLComments убирает комментарии и содержимое строковых литералов:
// метод, названный в комментарии или в тексте сообщения, вызовом не является.
func stripBSLComments(code string) string {
	var b strings.Builder
	inString := false
	for _, line := range strings.Split(code, "\n") {
		rs := []rune(line)
		// Строка-продолжение литерала начинается с "|"; пустая строка и строка
		// комментария между продолжениями литерал не закрывают.
		if inString {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") {
				b.WriteByte('\n')
				continue
			}
			if !strings.HasPrefix(trimmed, "|") {
				inString = false
			}
		}
		for i := 0; i < len(rs); i++ {
			switch {
			case rs[i] == '"':
				inString = !inString
				b.WriteRune('"')
			case inString:
			case rs[i] == '/' && i+1 < len(rs) && rs[i+1] == '/':
				i = len(rs)
			default:
				b.WriteRune(rs[i])
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// callPattern: выражение вызова как оно пишется в коде: части через точку с
// возможными пробелами, за ними скобка. Регистр букв не различается.
func callPattern(call string) *regexp.Regexp {
	parts := strings.Split(call, ".")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`(?i)` + strings.Join(parts, `\s*\.\s*`) + `\s*\(`)
}

// identRune: руна может быть частью идентификатора BSL.
func identRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// callsMethod: в тексте модуля есть вызов метода call. Совпадение, перед
// которым стоит часть идентификатора или точка, не считается: это хвост
// другого имени или метод другого объекта.
//
// code: текст без комментариев (stripBSLComments), raw: исходный текст. По
// raw ищется вызов общего модуля через ОбщегоНазначения.ОбщийМодуль("Имя"),
// так БСП велит звать необязательные подсистемы: имя модуля там строковый
// литерал, а литералы stripBSLComments вычищает.
func callsMethod(code, raw, call string) bool {
	for _, loc := range callPattern(call).FindAllStringIndex(code, -1) {
		before := []rune(code[:loc[0]])
		if n := len(before); n == 0 || !(identRune(before[n-1]) || before[n-1] == '.') {
			return true
		}
	}
	return callsViaCommonModule(stripComments(raw), call)
}

// stripComments убирает комментарии, оставляя строковые литералы.
func stripComments(code string) string {
	var b strings.Builder
	for _, line := range strings.Split(code, "\n") {
		inString := false
		rs := []rune(line)
		for i := 0; i < len(rs); i++ {
			if rs[i] == '"' {
				inString = !inString
			} else if !inString && rs[i] == '/' && i+1 < len(rs) && rs[i+1] == '/' {
				break
			}
			b.WriteRune(rs[i])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// callsViaCommonModule: метод call общего модуля вызван через
// ОбщегоНазначения[Клиент].ОбщийМодуль("Модуль"), прямо или через переменную.
// Модули менеджеров (два и больше звена до метода) так не зовутся.
func callsViaCommonModule(code, call string) bool {
	parts := strings.Split(call, ".")
	if len(parts) != 2 {
		return false
	}
	module, method := regexp.QuoteMeta(parts[0]), regexp.QuoteMeta(parts[1])
	getter := `ОбщегоНазначения(?:Клиент)?\s*\.\s*ОбщийМодуль\s*\(\s*"` + module + `"\s*\)`
	if regexp.MustCompile(`(?i)` + getter + `\s*\.\s*` + method + `\s*\(`).MatchString(code) {
		return true
	}
	assign := regexp.MustCompile(`(?i)([\p{L}_][\p{L}\p{N}_]*)\s*=\s*` + getter)
	for _, m := range assign.FindAllStringSubmatch(code, -1) {
		if callsMethod(code, "", m[1]+"."+parts[1]) {
			return true
		}
	}
	return false
}

var agentQualifiedCall = regexp.MustCompile(`([\p{L}_][\p{L}\p{N}_]*(?:\s*\.\s*[\p{L}_][\p{L}\p{N}_]*)+)\s*\(`)

// qualifiedCalls: вызовы через точку, встреченные в коде, без повторов, в
// порядке появления. Это подсказка читающему человеку, а не вердикт: сюда
// попадают и методы платформы (Запрос.Выполнить).
func qualifiedCalls(code string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range agentQualifiedCall.FindAllStringSubmatch(code, -1) {
		call := strings.Join(strings.Fields(m[1]), "")
		if key := strings.ToLower(call); !seen[key] {
			seen[key] = true
			out = append(out, call)
		}
	}
	return out
}

var agentDeclared = regexp.MustCompile(`(?im)^\s*(?:Функция|Процедура|Function|Procedure)\s+[\p{L}_]`)

// Исходы задачи в одном плече.
const (
	agentAccepted = "готовый" // вызван засчитываемый метод
	agentOther    = "другое"  // метод написан без него: читать человеку
	agentEmpty    = "пусто"   // в модуле не объявлено ни одного метода
	agentMissing  = "нет"     // запуска нет
	agentFailed   = "сбой"    // CLI завершился ошибкой или по потолку времени
)

// agentVerdict: исход по тексту модуля.
func agentVerdict(module string, accept []string) string {
	code := stripBSLComments(module)
	if !agentDeclared.MatchString(code) {
		return agentEmpty
	}
	for _, call := range accept {
		if callsMethod(code, module, call) {
			return agentAccepted
		}
	}
	return agentOther
}

// agentTranscript: что видно в журнале запуска.
type agentTranscript struct {
	// Tools: число вызовов инструмента по короткому имени (find_api, Read).
	Tools map[string]int
	Turns int
	Cost  float64
	// Failed: CLI сам назвал запуск ошибочным (отказ входа, обрыв).
	Failed bool
	// Finished: в журнале есть итоговая запись.
	Finished bool
	// Answers: итоговый текст каждого хода по порядку.
	Answers []string
}

// readAgentTranscript разбирает журнал stream-json. Строки, которые не
// разбираются, пропускаются: журнал оборванного запуска кончается на полуслове.
func readAgentTranscript(r io.Reader) agentTranscript {
	out := agentTranscript{Tools: map[string]int{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			Type    string  `json:"type"`
			IsError bool    `json:"is_error"`
			Result  string  `json:"result"`
			Turns   int     `json:"num_turns"`
			Cost    float64 `json:"total_cost_usd"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" {
					out.Tools[shortTool(c.Name)]++
				}
			}
		case "result":
			out.Finished = true
			out.Failed = out.Failed || ev.IsError
			out.Turns += ev.Turns
			out.Cost += ev.Cost
			out.Answers = append(out.Answers, ev.Result)
		}
	}
	return out
}

// shortTool: имя инструмента без приставки сервера (mcp__сервер__find_api).
func shortTool(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return name[i+len("__"):]
	}
	return name
}

// signTest: двусторонний точный знаковый тест по несогласным парам: plus
// задач выиграло одно плечо, minus другое. Возвращает вероятность такого или
// большего перекоса при равных плечах.
func signTest(plus, minus int) float64 {
	n := plus + minus
	if n == 0 {
		return 1
	}
	k := plus
	if minus < k {
		k = minus
	}
	tail := 0.0
	for i := 0; i <= k; i++ {
		tail += binomial(n, i)
	}
	return math.Min(1, 2*tail/math.Pow(2, float64(n)))
}

func binomial(n, k int) float64 {
	c := 1.0
	for i := 1; i <= k; i++ {
		c = c * float64(n-k+i) / float64(i)
	}
	return c
}

// agentScore: исход задачи в плече вместе с тем, что видно в журнале.
type agentScore struct {
	Task    string         `json:"task"`
	Section string         `json:"section"`
	Arm     string         `json:"arm"`
	Verdict string         `json:"verdict"`
	Tools   map[string]int `json:"tools,omitempty"`
	Turns   int            `json:"turns,omitempty"`
	Calls   []string       `json:"calls,omitempty"`
	// Planned: запуск шёл в два хода, первый ход кончился планом.
	Planned bool `json:"planned,omitempty"`
	// PlanNamed: план первого хода назвал засчитываемый метод.
	PlanNamed bool `json:"planNamed,omitempty"`
}

// namesMethodIn: в тексте назван метод call, без оглядки на скобки и регистр.
func namesMethodIn(text, call string) bool {
	return strings.Contains(strings.ToLower(strings.Join(strings.Fields(text), "")), strings.ToLower(call))
}

// scoreAgentRun: исход одной задачи по каталогу её запуска.
func scoreAgentRun(dir string, t agentTask, arm string) agentScore {
	s := agentScore{Task: t.ID, Section: t.Section, Arm: arm, Verdict: agentMissing}
	f, err := os.Open(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		return s
	}
	tr := readAgentTranscript(f)
	f.Close()
	s.Tools, s.Turns = tr.Tools, tr.Turns
	if len(tr.Answers) > 1 {
		s.Planned = true
		for _, call := range t.Accept {
			s.PlanNamed = s.PlanNamed || namesMethodIn(tr.Answers[0], call)
		}
	}
	if !agentRunDone(dir) || !tr.Finished || tr.Failed {
		s.Verdict = agentFailed
		return s
	}
	module, err := os.ReadFile(filepath.Join(dir, agentModule))
	if err != nil {
		s.Verdict = agentEmpty
		return s
	}
	s.Verdict = agentVerdict(string(module), t.Accept)
	if s.Verdict == agentOther {
		s.Calls = qualifiedCalls(stripBSLComments(string(module)))
	}
	return s
}

func runAgentScore(args []string) error {
	fs := flag.NewFlagSet("agent-score", flag.ExitOnError)
	tasksPath := fs.String("tasks", "", "файл задач от agent-tasks (обязателен)")
	runs := fs.String("runs", "", "каталог запусков (обязателен)")
	arms := fs.String("arms", "before,after", "два плеча через запятую: с чем сравниваем и что сравниваем")
	out := fs.String("out", "", "куда записать исходы по задачам (JSONL); пусто: не писать")
	fs.Parse(args)
	names := strings.Split(*arms, ",")
	if *tasksPath == "" || *runs == "" || len(names) != 2 {
		return errors.New("agent-score: флаги -tasks и -runs обязательны, в -arms ровно два плеча")
	}
	tasks, err := readJSONLFile[agentTask](*tasksPath)
	if err != nil {
		return err
	}
	var all []agentScore
	byArm := map[string]map[string]agentScore{}
	for _, arm := range names {
		byArm[arm] = map[string]agentScore{}
		for _, t := range tasks {
			s := scoreAgentRun(filepath.Join(*runs, arm, t.ID), t, arm)
			byArm[arm][t.ID] = s
			all = append(all, s)
		}
	}
	printAgentReport(os.Stdout, tasks, names, byArm)
	if *out != "" {
		return writeJSONL(*out, all)
	}
	return nil
}

// printAgentReport печатает сводку по плечам, парное сравнение и модули,
// которые должен прочесть человек.
func printAgentReport(w io.Writer, tasks []agentTask, arms []string, byArm map[string]map[string]agentScore) {
	for _, arm := range arms {
		verdicts := map[string]int{}
		sections := map[string][2]int{}
		usedFind, usedValidate, planned, planNamed := 0, 0, 0, 0
		for _, t := range tasks {
			s := byArm[arm][t.ID]
			verdicts[s.Verdict]++
			if s.Verdict == agentFailed || s.Verdict == agentMissing {
				continue
			}
			sec := sections[t.Section]
			sec[1]++
			if s.Verdict == agentAccepted {
				sec[0]++
			}
			sections[t.Section] = sec
			if s.Tools["find_api"] > 0 {
				usedFind++
			}
			if s.Tools["validate_bsl"] > 0 {
				usedValidate++
			}
			if s.Planned {
				planned++
			}
			if s.PlanNamed {
				planNamed++
			}
		}
		finished := len(tasks) - verdicts[agentFailed] - verdicts[agentMissing]
		fmt.Fprintf(w, "плечо %s: готовый метод вызван в %d из %d завершённых (bsp %d/%d, other %d/%d); другое %d, пусто %d, сбой %d, нет запуска %d; звал find_api в %d, validate_bsl в %d\n",
			arm, verdicts[agentAccepted], finished, sections["bsp"][0], sections["bsp"][1], sections["other"][0], sections["other"][1],
			verdicts[agentOther], verdicts[agentEmpty], verdicts[agentFailed], verdicts[agentMissing], usedFind, usedValidate)
		if planned > 0 {
			fmt.Fprintf(w, "  запусков с планом %d, план назвал засчитываемый метод в %d\n", planned, planNamed)
		}
	}
	base, next := byArm[arms[0]], byArm[arms[1]]
	both, neither, gained, lost, unpaired := 0, 0, 0, 0, 0
	for _, t := range tasks {
		a, b := base[t.ID].Verdict, next[t.ID].Verdict
		if a == agentMissing || a == agentFailed || b == agentMissing || b == agentFailed {
			unpaired++
			continue
		}
		switch {
		case a == agentAccepted && b == agentAccepted:
			both++
		case b == agentAccepted:
			gained++
		case a == agentAccepted:
			lost++
		default:
			neither++
		}
	}
	fmt.Fprintf(w, "пары %s → %s: в обоих %d, только во втором %d, только в первом %d, ни в одном %d, без пары %d; знаковый тест p=%.2f\n",
		arms[0], arms[1], both, gained, lost, neither, unpaired, signTest(gained, lost))
	fmt.Fprintln(w, "читать человеку (метод написан без засчитываемого вызова):")
	for _, arm := range arms {
		for _, t := range tasks {
			if s := byArm[arm][t.ID]; s.Verdict == agentOther {
				fmt.Fprintf(w, "  %s/%s %s (ждали %s): %s\n", arm, t.ID, t.Name, t.Accept[0], strings.Join(s.Calls, ", "))
			}
		}
	}
}
