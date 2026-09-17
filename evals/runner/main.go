// Command evalrunner — вспомогательная утилита тикета 16 (НЕ production-код,
// не регистрируется как MCP tool, ничего в internal/* и cmd/mcp1c/* не
// меняет). Прогоняет набор реальных задач из evals/*.json через РЕАЛЬНО
// собранный сервер cmd/mcp1c, поднятый по stdio тем же go-sdk клиентом, что
// cmd/mcp1c/stdio_live_test.go — так метрики (число tool calls, латентность,
// объём ответа) снимаются с фактического JSON-RPC пути, а не с внутреннего
// Go API internal/retrieve.
//
// Использование (из корня репозитория):
//
//	go run ./evals/runner -dump <каталог выгрузки ut_demo> -evals ./evals -out <файл результатов>
//
// Путь к выгрузке — флаг -dump или переменная окружения ONEC_DUMP
// (MCP1C_SPIKE_DUMP), флаг сильнее; в коде путь не хардкодится.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Task — один файл evals/NN-slug.json (формат зафиксирован в тикете 16:
// id/intent/task/target/requiredCategories/expectedTools, плюс наши поля
// genre/notes для читаемости отчёта).
type Task struct {
	ID                 string          `json:"id"`
	Genre              string          `json:"genre"`
	Intent             string          `json:"intent"`
	TaskText           string          `json:"task"`
	Target             json.RawMessage `json:"target"`
	RequiredCategories []string        `json:"requiredCategories"`
	ExpectedTools      []string        `json:"expectedTools"`
	Notes              string          `json:"notes"`
}

// ToolCallMetrics — то, что реально измерено на одном вызове инструмента
// (архитектура §30: число tool calls, объём контекста, время ответа).
type ToolCallMetrics struct {
	Tool           string          `json:"tool"`
	DurationMS     float64         `json:"durationMs"`
	Error          string          `json:"error,omitempty"`
	ResponseChars  int             `json:"responseChars"` // json.Marshal(StructuredContent) — объём переданного контекста
	StructuredJSON json.RawMessage `json:"-"`             // не сериализуется в общий отчёт целиком (дубль в rawResults)
}

// TaskResult — метрики §30 по одной задаче.
type TaskResult struct {
	TaskID               string            `json:"taskId"`
	Genre                string            `json:"genre"`
	Intent               string            `json:"intent"`
	ClassifiedIntent     string            `json:"classifiedIntent,omitempty"`
	IntentMatch          bool              `json:"intentMatch"`
	ToolCalls            []ToolCallMetrics `json:"toolCalls"`
	ToolCallCount        int               `json:"toolCallCount"`
	TotalDurationMS      float64           `json:"totalDurationMs"`
	AnchorsFound         int               `json:"anchorsFound"`
	SufficiencyStatus    string            `json:"sufficiencyStatus,omitempty"`
	RequiredCoverage     []coverageRow     `json:"requiredCoverage,omitempty"`
	RequiredCategoriesOK bool              `json:"requiredCategoriesOK"`
	MissingRequired      []string          `json:"missingRequired,omitempty"`
	Ambiguities          int               `json:"ambiguities"`
	Warnings             []string          `json:"warnings,omitempty"`
	EstimatedTokens      int               `json:"estimatedTokens"`
	UsedChars            int               `json:"usedChars"`
	RequestedChars       int               `json:"requestedChars"`
	WholeFileReadNeeded  bool              `json:"wholeFileReadNeeded"` // всегда false по конструкции (R34) — фиксируется явно, не предполагается
	FalseReferences      string            `json:"falseReferences"`     // текстовая заметка, не число: ложные references для offline-корпуса не считаются автоматически, см. docs/evaluation-report.md
	Err                  string            `json:"error,omitempty"`
}

type coverageRow struct {
	Category      string `json:"category"`
	Status        string `json:"status"`
	ReturnedCount int    `json:"returnedCount"`
	TotalCount    int    `json:"totalCount"`
}

func main() {
	var evalsDir, outPath, repoRoot, binPath string
	flag.StringVar(&evalsDir, "evals", "evals", "каталог с задачами evals/*.json")
	flag.StringVar(&outPath, "out", "", "путь для сырых результатов JSON (опционально)")
	flag.StringVar(&repoRoot, "repo", ".", "корень репозитория (для go build)")
	flag.StringVar(&binPath, "bin", "", "путь к уже собранному бинарнику (пропускает go build)")
	var dumpRoot string
	flag.StringVar(&dumpRoot, "dump", "", "каталог реальной XML-выгрузки (по умолчанию ONEC_DUMP или MCP1C_SPIKE_DUMP)")
	flag.Parse()

	if dumpRoot == "" {
		dumpRoot = firstNonEmptyEnv("ONEC_DUMP", "MCP1C_SPIKE_DUMP")
	}
	if dumpRoot == "" {
		log.Fatal("путь к реальной выгрузке обязателен: флаг -dump или переменная ONEC_DUMP")
	}
	if _, err := os.Stat(dumpRoot); err != nil {
		log.Fatalf("выгрузка %s недоступна: %v", dumpRoot, err)
	}

	tasks, err := loadTasks(evalsDir)
	if err != nil {
		log.Fatalf("загрузка задач из %s: %v", evalsDir, err)
	}
	log.Printf("загружено задач: %d из %s", len(tasks), evalsDir)

	if binPath == "" {
		tmpBin, err := os.MkdirTemp("", "mcp1c-eval-bin")
		if err != nil {
			log.Fatalf("временный каталог для бинарника: %v", err)
		}
		binPath = filepath.Join(tmpBin, "mcp1c-eval")
		t0 := time.Now()
		build := exec.Command("go", "build", "-o", binPath, "./cmd/mcp1c")
		build.Dir = repoRoot
		out, err := build.CombinedOutput()
		if err != nil {
			log.Fatalf("go build ./cmd/mcp1c: %v\n%s", err, out)
		}
		log.Printf("сервер собран за %s: %s", time.Since(t0), binPath)
	}

	workspaceRoot, projectID, cleanup, err := setupWorkspace(dumpRoot)
	if err != nil {
		log.Fatalf("подготовка workspace/проекта: %v", err)
	}
	defer cleanup()
	log.Printf("workspace=%s project=%s (симлинк на %s)", workspaceRoot, projectID, dumpRoot)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	cmdServer := exec.Command(binPath)
	cmdServer.Env = append(os.Environ(), "MCP_1C_PROJECTS_ROOT="+workspaceRoot)

	client := mcp.NewClient(&mcp.Implementation{Name: "evalrunner", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmdServer}, nil)
	if err != nil {
		log.Fatalf("подключение к серверу по stdio: %v", err)
	}
	defer session.Close()

	toolsResp, err := session.ListTools(ctx, nil)
	if err != nil {
		log.Fatalf("ListTools: %v", err)
	}
	toolNames := map[string]bool{}
	for _, t := range toolsResp.Tools {
		toolNames[t.Name] = true
	}
	log.Printf("сервер зарегистрировал %d инструментов", len(toolsResp.Tools))
	for _, want := range []string{"get_context_for_task", "reindex", "index_status"} {
		if !toolNames[want] {
			log.Fatalf("сервер не зарегистрировал обязательный индексный инструмент %s — прогон бессмысленен", want)
		}
	}

	// Холодная полная индексация — ОДИН раз, до цикла по задачам. Не входит в
	// per-task метрики (это setup, не часть агентского запроса), но время
	// записывается отдельно в отчёт (§28 cold full index — прецедент, не
	// повторный замер: тот делает docs/benchmarks.md на этой же выгрузке
	// отдельной задачей другого исполнителя тикета 16).
	reindexStart := time.Now()
	reindexResp, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "reindex", Arguments: map[string]any{"mode": "full"}})
	reindexDur := time.Since(reindexStart)
	if err != nil {
		log.Fatalf("reindex(full): %v", err)
	}
	if reindexResp.IsError {
		log.Fatalf("reindex(full) вернул ошибку: %s", toolResultText(reindexResp))
	}
	log.Printf("холодная полная индексация ut_demo: %s", reindexDur)

	results := make([]TaskResult, 0, len(tasks))
	for _, task := range tasks {
		res := runTask(ctx, session, task)
		results = append(results, res)
		log.Printf("[%s] intent=%s->%s calls=%d dur=%.0fms sufficiency=%s coverage_ok=%v",
			task.ID, task.Intent, res.ClassifiedIntent, res.ToolCallCount, res.TotalDurationMS,
			res.SufficiencyStatus, res.RequiredCategoriesOK)
	}

	report := struct {
		DumpRoot        string       `json:"dumpRoot"`
		TaskCount       int          `json:"taskCount"`
		ColdFullIndexMS float64      `json:"coldFullIndexMs"`
		Results         []TaskResult `json:"results"`
	}{
		DumpRoot:        "<из переменной окружения, не записывается буквально в коммитимый файл>",
		TaskCount:       len(tasks),
		ColdFullIndexMS: float64(reindexDur.Microseconds()) / 1000.0,
		Results:         results,
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatalf("сериализация отчёта: %v", err)
	}
	if outPath != "" {
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			log.Fatalf("запись %s: %v", outPath, err)
		}
		log.Printf("сырые результаты записаны в %s", outPath)
	} else {
		fmt.Println(string(data))
	}

	printSummary(results)
}

// runTask зовёт get_context_for_task, затем — там, где expectedTools задачи
// называет конкретный инструмент вторым и далее пунктом — зовёт и его тоже
// (то, что реальный агент сделал бы как follow-up), считая ВСЕ вызовы в
// ToolCallCount.
func runTask(ctx context.Context, session *mcp.ClientSession, task Task) TaskResult {
	res := TaskResult{TaskID: task.ID, Genre: task.Genre, Intent: task.Intent}

	call := func(name string, args map[string]any) (map[string]any, ToolCallMetrics) {
		t0 := time.Now()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		dur := time.Since(t0)
		m := ToolCallMetrics{Tool: name, DurationMS: float64(dur.Microseconds()) / 1000.0}
		if err != nil {
			m.Error = err.Error()
			return nil, m
		}
		if out.IsError {
			m.Error = toolResultText(out)
			return nil, m
		}
		raw, merr := json.Marshal(out.StructuredContent)
		if merr != nil {
			m.Error = "маршалинг ответа: " + merr.Error()
			return nil, m
		}
		m.ResponseChars = len(raw)
		m.StructuredJSON = raw
		var parsed map[string]any
		if uerr := json.Unmarshal(raw, &parsed); uerr != nil {
			m.Error = "ответ не объект: " + uerr.Error()
			return nil, m
		}
		return parsed, m
	}

	ctxResp, ctxMetrics := call("get_context_for_task", map[string]any{"task": task.TaskText})
	res.ToolCalls = append(res.ToolCalls, ctxMetrics)
	if ctxMetrics.Error != "" {
		res.Err = ctxMetrics.Error
		res.ToolCallCount = len(res.ToolCalls)
		res.TotalDurationMS = sumDuration(res.ToolCalls)
		return res
	}

	items, _ := ctxResp["items"].([]any)
	if len(items) == 0 {
		res.Err = "get_context_for_task вернул пустой items[]"
		res.ToolCallCount = len(res.ToolCalls)
		res.TotalDurationMS = sumDuration(res.ToolCalls)
		return res
	}
	item, _ := items[0].(map[string]any)

	if intent, ok := item["intent"].(map[string]any); ok {
		res.ClassifiedIntent, _ = intent["primary"].(string)
	}
	res.IntentMatch = res.ClassifiedIntent == task.Intent

	if anchors, ok := item["anchors"].([]any); ok {
		res.AnchorsFound = len(anchors)
	}
	res.SufficiencyStatus, _ = item["sufficiencyStatus"].(string)
	if cov, ok := item["requiredCoverage"].([]any); ok {
		res.RequiredCoverage = make([]coverageRow, 0, len(cov))
		allPresent := true
		for _, c := range cov {
			cm, _ := c.(map[string]any)
			row := coverageRow{
				Category: str(cm["category"]),
				Status:   str(cm["status"]),
			}
			if v, ok := cm["returnedCount"].(float64); ok {
				row.ReturnedCount = int(v)
			}
			if v, ok := cm["totalCount"].(float64); ok {
				row.TotalCount = int(v)
			}
			res.RequiredCoverage = append(res.RequiredCoverage, row)
			if row.Status == "missing" {
				allPresent = false
			}
		}
		res.RequiredCategoriesOK = allPresent
	}
	if mr, ok := item["missingRequired"].([]any); ok {
		for _, v := range mr {
			res.MissingRequired = append(res.MissingRequired, str(v))
		}
	}
	if amb, ok := item["ambiguities"].([]any); ok {
		res.Ambiguities = len(amb)
	}
	if warns, ok := item["warnings"].([]any); ok {
		for _, w := range warns {
			wm, _ := w.(map[string]any)
			res.Warnings = append(res.Warnings, str(wm["code"])+": "+str(wm["message"]))
		}
	}
	if budget, ok := item["budget"].(map[string]any); ok {
		if v, ok := budget["estimatedTokens"].(float64); ok {
			res.EstimatedTokens = int(v)
		}
		if v, ok := budget["usedChars"].(float64); ok {
			res.UsedChars = int(v)
		}
		if v, ok := budget["requestedChars"].(float64); ok {
			res.RequestedChars = int(v)
		}
	}
	res.FalseReferences = "не подсчитано автоматически: требует ручной сверки anchor/references с исходником по каждой задаче, см. docs/evaluation-report.md, раздел «Ложные references»"

	// Первый анкор из ответа — для follow-up по символу (find_references,
	// trace_call_graph) нужен именно uid, который реальный агент взял бы из
	// этого же ответа, а не придумал заново.
	var anchorUID string
	if anchors, ok := item["anchors"].([]any); ok && len(anchors) > 0 {
		if a, ok := anchors[0].(map[string]any); ok {
			anchorUID = str(a["uid"])
		}
	}

	// Follow-up: конкретный инструмент из expectedTools (если он не сам
	// get_context_for_task) — там, где для задачи он реально применим.
	for _, tool := range task.ExpectedTools {
		if tool == "get_context_for_task" {
			continue
		}
		args, ok := followUpArgs(task, tool, anchorUID)
		if !ok {
			continue
		}
		_, m := call(tool, args)
		res.ToolCalls = append(res.ToolCalls, m)
	}

	res.ToolCallCount = len(res.ToolCalls)
	res.TotalDurationMS = sumDuration(res.ToolCalls)
	return res
}

// componentTypeWord переводит имя компонента манифеста ("Documents",
// "Catalogs" — из target-файлов задач) в единственное число вида get_object/
// get_form_handlers ("Document", "Catalog") — те же слова, что и словарь
// get_object_structure легаси-инструмента.
func componentTypeWord(component string) string {
	return strings.TrimSuffix(component, "s")
}

// followUpArgs строит аргументы для follow-up вызова по target задачи, СВЕРЕННЫЕ
// с реальными полями входных структур инструментов (cmd/mcp1c/idx_symbol.go,
// idx_meta.go, idx_impact.go) — не угаданные по названию. anchorUID — uid
// первого anchor из уже полученного ответа get_context_for_task (для
// find_references/trace_call_graph, которым нужен именно uid, не имя).
// Возвращает ok=false, если для этого инструмента у задачи нет структурно
// подходящего target или anchor (тогда follow-up честно пропускается, а не
// зовётся с придуманными аргументами).
func followUpArgs(task Task, tool string, anchorUID string) (map[string]any, bool) {
	var target map[string]any
	if err := json.Unmarshal(task.Target, &target); err != nil {
		return nil, false
	}
	switch tool {
	case "find_register_writes":
		if str(target["kind"]) != "metadata_object" || str(target["type"]) != "AccumulationRegister" {
			return nil, false
		}
		return map[string]any{"register": str(target["name"])}, true
	case "find_impact":
		if str(target["kind"]) != "metadata_object" {
			return nil, false
		}
		return map[string]any{"objectType": str(target["type"]), "objectName": str(target["name"])}, true
	case "get_object":
		if str(target["kind"]) != "metadata_object" {
			return nil, false
		}
		return map[string]any{"type": str(target["type"]), "name": str(target["name"])}, true
	case "get_form_handlers":
		if str(target["kind"]) != "form_element" {
			return nil, false
		}
		return map[string]any{
			"type": componentTypeWord(str(target["component"])),
			"name": str(target["owner"]),
			"form": str(target["form"]),
		}, true
	case "find_references":
		if str(target["kind"]) != "symbol" || anchorUID == "" {
			return nil, false
		}
		return map[string]any{"uid": anchorUID}, true
	case "trace_call_graph":
		if str(target["kind"]) != "symbol" || anchorUID == "" {
			return nil, false
		}
		return map[string]any{"uid": anchorUID, "direction": "callers"}, true
	case "get_query_schema":
		// get_query_schema (легаси-инструмент) принимает готовый текст запроса,
		// не имя символа — у нас его нет заранее, follow-up пропускается
		// честно, не имитируется случайным текстом.
		return nil, false
	}
	return nil, false
}

func sumDuration(calls []ToolCallMetrics) float64 {
	var sum float64
	for _, c := range calls {
		sum += c.DurationMS
	}
	return sum
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func toolResultText(r *mcp.CallToolResult) string {
	var parts []string
	for _, block := range r.Content {
		if t, ok := block.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) == 0 {
		return "(без текстового содержимого)"
	}
	return strings.Join(parts, "; ")
}

func loadTasks(dir string) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var tasks []Task
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		var t Task
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// setupWorkspace готовит временный workspace для активного проекта индекса:
// каталог проекта с симлинком на реальную выгрузку (сама выгрузка НЕ
// изменяется — ни один файл в ней не пишется) плюс манифест 1c-project.json
// и .mcp1c/registry.json с этим проектом как активным. Использует
// internal/workspace как обычный клиент этого пакета (тот же приём, что
// internal/retrieve/realdump_test.go и cmd/mcp1c/idx_impact_realdump_test.go
// — импорт internal/* тестовым/вспомогательным кодом легитимен, production-
// файлы этих пакетов не редактируются).
func setupWorkspace(dumpRoot string) (workspaceRoot, projectID string, cleanup func(), err error) {
	absDump, err := filepath.Abs(dumpRoot)
	if err != nil {
		return "", "", nil, err
	}

	base, err := os.MkdirTemp("", "mcp1c-eval-workspace")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { os.RemoveAll(base) }

	projectRoot := filepath.Join(base, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		cleanup()
		return "", "", nil, err
	}
	// workspace.SafeJoin (единственная точка валидации путей, interfaces.md)
	// намеренно отклоняет симлинк, ведущий за пределы корня проекта — это
	// защита от выхода за workspace, а не то, что можно обойти в раннере.
	// Поэтому выгрузка не симлинкается, а клонируется физически внутрь
	// project/configuration: на APFS (macOS) `cp -Rc` — copy-on-write
	// clonefile(2), доли секунды и почти без реального расхода места; на
	// остальных ФС/системах — обычное рекурсивное копирование как честный
	// fallback. Сама реальная выгрузка ни разу не открывается на запись.
	configRoot := filepath.Join(projectRoot, "configuration")
	if err := cloneDump(absDump, configRoot); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("клонирование выгрузки: %w", err)
	}

	const id = domain.ProjectID("ut-demo-eval")
	manifest := workspace.Manifest{
		Version: workspace.ManifestVersion,
		Project: id,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: "configuration"},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := os.WriteFile(filepath.Join(projectRoot, workspace.ManifestFileName), data, 0o644); err != nil {
		cleanup()
		return "", "", nil, err
	}

	// Манифест реально валиден только если LoadManifest его принимает —
	// проверяем здесь же, а не полагаемся на то, что сервер молча не найдёт
	// проект и ответит no_active_project без объяснения, откуда оно взялось.
	if _, err := workspace.LoadManifest(projectRoot); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("собранный манифест не проходит LoadManifest: %w", err)
	}

	reg, err := workspace.OpenRegistry(base)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: id, Root: projectRoot}); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := reg.SetActiveProject(id); err != nil {
		cleanup()
		return "", "", nil, err
	}

	return base, string(id), cleanup, nil
}

// cloneDump копирует выгрузку src в dst. На macOS сначала пробует `cp -Rc`
// (APFS clonefile — copy-on-write, быстро и почти без расхода места);
// при недоступности (не macOS, не APFS, `cp` без -c) откатывается на
// обычное рекурсивное копирование содержимого через io.Copy.
func cloneDump(src, dst string) error {
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("cp", "-Rc", src, dst)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			log.Printf("cp -Rc недоступен (%v: %s), откат на обычное копирование", err, strings.TrimSpace(string(out)))
			os.RemoveAll(dst) // cp мог успеть создать частичный результат
		}
	}
	return plainCopyTree(src, dst)
}

// plainCopyTree — переносимый (без CGO, без platform-specific syscalls)
// рекурсивный копир на голом os/io — единственный fallback, не зависящий от
// внешней команды cp и её флагов.
func plainCopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Ссылки внутри самой выгрузки (если есть) не разворачиваются в
			// клоне — сохраняются как обычные файлы через их текущее
			// содержимое, чтобы не завести собственный источник escape.
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			return os.WriteFile(target, data, 0o644)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func printSummary(results []TaskResult) {
	fmt.Println()
	fmt.Println("=== Сводка ===")
	fmt.Printf("%-32s %-18s %6s %9s %-22s %s\n", "task", "intent", "calls", "ms", "sufficiency", "coverage_ok")
	for _, r := range results {
		fmt.Printf("%-32s %-18s %6d %9.1f %-22s %v\n",
			r.TaskID, r.ClassifiedIntent, r.ToolCallCount, r.TotalDurationMS, r.SufficiencyStatus, r.RequiredCategoriesOK)
	}
}
