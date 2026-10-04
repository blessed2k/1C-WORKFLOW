// Command findapi: оценка качества find_api на парах «задача словами → готовый
// метод», построенных из реального кода конфигурации (НЕ production-код, не
// регистрируется как MCP tool).
//
// Пара берётся из настоящего вызова метода программного интерфейса в чужом
// модуле. Имя метода в коде скрывается, и модель, не видя его, пишет запрос:
// что должна сделать функция на этом месте. Вторая модель, видя метод и его
// описание, отсеивает пары, где запрос не про этот метод. Как набор строится,
// что в нём есть и что им мерить нельзя: docs/find-api-eval.md.
//
// Шаги (из корня репозитория, каталог проекта: флаг -project или ONEC_DUMP):
//
//	go run ./evals/findapi tasks    -project <выгрузка>   # задачи и вход пишущей модели
//	(пишущая модель: work/writer/in-NN.jsonl → out-NN.jsonl по work/writer/PROMPT.md)
//	go run ./evals/findapi judge                          # вход проверяющей модели
//	(две проверяющие модели независимо: work/judge/in-NN.jsonl → out-NN-a.jsonl и
//	 out-NN-b.jsonl по work/judge/PROMPT.md; пара принята, когда приняли обе)
//	go run ./evals/findapi assemble -out evals/findapi/data/<имя>.jsonl
//	go run ./evals/findapi check    -data evals/findapi/data/<имя>.jsonl
//	go run ./evals/findapi score    -project <выгрузка> -data ... [-baseline ... | -write-baseline ...]
//	go run ./evals/findapi query    -project <выгрузка> разбить строку по разделителю   # выдача на один запрос
//	go run ./evals/findapi drafts   -project <выгрузка> -data ... -drafts ...             # обратная проверка черновика
//	go run ./evals/findapi types    -project <выгрузка>                                   # доля методов с типом результата
//	go run ./evals/findapi core     -project <выгрузка>                                   # ходовые методы библиотеки
//
// Замер поведения агента (зовёт ли он готовый метод или пишет свой), agent.go:
//
//	go run ./evals/findapi agent-tasks -data ... -drafts ... -out <задачи>
//	go run ./evals/findapi agent-run   -tasks <задачи> -arm <плечо> -claude <CLI> -mcp-config <файл> -dump <выгрузка> -out <каталог запусков>
//	go run ./evals/findapi agent-score -tasks <задачи> -runs <каталог запусков> -arms before,after
//
// Рабочий каталог (work) содержит код конфигурации и в git не идёт.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

const (
	defaultWork = "evals/findapi/work"
	tasksFile   = "tasks.jsonl"
	writerDir   = "writer"
	judgeDir    = "judge"
)

const writerPrompt = `# Задание пишущей модели

Ты разработчик 1С. Во входном файле по одной задаче на строку: JSON с полями id и snippet.
В каждом фрагменте кода вызов одной процедуры или функции заменён на ` + maskedName + `: имя скрыто намеренно.

Представь, что этот код пишешь ты и готового метода под рукой нет. Прежде чем писать
вспомогательную функцию самому, ты ищешь готовую в каталоге методов конфигурации.
Напиши поисковый запрос: что метод должен сделать на этом месте.

Правила для поля "query":

- Запрос по-русски, от трёх до десяти слов, как разработчик пишет в поиске: действие и предмет
  («округлить сумму до копеек», «найти подстроку без учёта регистра»).
- Описывай задачу, а не имя. Не угадывай, как метод называется, не пиши слитных идентификаторов.
- Опирайся только на фрагмент: аргументы вызова, что делают с результатом, имена переменных,
  комментарии.
- Запрос про то, что делает скрытый метод, а не про то, что делает вся процедура вокруг него.
- Если по фрагменту нельзя понять, что делает метод, ставь "clear": false, "query": "" и "alt": "".
  Не выдумывай.

Правила для поля "alt":

- Это тот же запрос, каким его написал бы другой разработчик 1С, не видевший этого кода.
- Термины платформы и предметной области оставляй как есть: регламентное задание, план обмена,
  табличная часть, реквизит, электронная подпись, контрагент, код маркировки. Их никто не
  заменяет, а замена меняет смысл (фоновое задание это не регламентное задание).
- Меняй остальное: глагол (получить, прочитать, узнать, вернуть; проверить, убедиться;
  сформировать, собрать, построить), построение фразы, общие слова (значение и данные, список и
  перечень, признак и флаг).
- Смысл обязан остаться тем же. Пересказ, под который подошёл бы другой метод, хуже пустого
  поля. Не получается: оставь "alt" пустым.

Читай только названный входной файл. Другие файлы рабочего каталога и исходники конфигурации
не открывай: в них лежат ответы, и запрос, написанный с подглядыванием, для оценки бесполезен.

Вывод: файл с тем же номером, out-NN.jsonl рядом со входным. По строке JSON на задачу,
каждая задача входа ровно один раз, в том же порядке, id без изменений:

{"id": "t1a2b3c4d", "clear": true, "query": "округлить сумму до копеек", "alt": "привести сумму к двум знакам после запятой"}
`

const judgePrompt = `# Задание проверяющей модели

Во входном файле по одной паре на строку: JSON с полями id, query, call, signature, doc.

- query: поисковый запрос разработчика 1С, который искал готовый метод под свою задачу;
- call, signature, doc: метод программного интерфейса конфигурации и его полное описание.

Реши, уместен ли этот метод как ответ на запрос:

- "yes": метод делает именно то, что описано в запросе, и запрос достаточно конкретен:
  разработчик, получив этот метод, был бы удовлетворён, а большинство других методов под
  такой запрос не подошли бы.
- "partial": метод связан с запросом, но запрос слишком общий (под него подошли бы десятки
  разных методов), описывает побочное действие метода или другую операцию над тем же предметом,
  либо подменяет термин другим по смыслу (фоновое задание вместо регламентного).
- "no": метод делает другое.

Суди по описанию метода, а не по сходству слов запроса с его именем. Если описание пустое,
суди по имени и сигнатуре и ставь "yes" только при полной уверенности. При сомнении между
"yes" и "partial" выбирай "partial": в набор должны попасть только бесспорные пары.

Вывод: выходной файл, названный в поручении, рядом со входным. По строке JSON на пару, каждая
пара входа ровно один раз, в том же порядке, id без изменений:

{"id": "t1a2b3c4d.d.0a1b2c", "verdict": "yes"}
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "tasks":
		err = runTasks(args)
	case "judge":
		err = runJudge(args)
	case "assemble":
		err = runAssemble(args)
	case "check":
		err = runCheck(args)
	case "score":
		err = runScore(args)
	case "query":
		err = runQuery(args)
	case "drafts":
		err = runDrafts(args)
	case "types":
		err = runTypes(args)
	case "core":
		err = runCore(args)
	case "agent-tasks":
		err = runAgentTasks(args)
	case "agent-run":
		err = runAgentRun(args)
	case "agent-score":
		err = runAgentScore(args)
	default:
		usage()
	}
	if err != nil {
		log.Fatalf("ОШИБКА: %v", err)
	}
}

func usage() {
	log.Fatal("использование: findapi tasks|judge|assemble|check|score|query|drafts|types|core|agent-tasks|agent-run|agent-score [флаги]; описание шагов в начале evals/findapi/main.go")
}

// projectFlags добавляет флаги, общие для шагов, которым нужен индекс.
func projectFlags(fs *flag.FlagSet) (root, workspace, syntaxIndex *string, reindex *bool) {
	root = fs.String("project", firstEnv("ONEC_DUMP", "MCP1C_SPIKE_DUMP"), "каталог проекта с 1c-project.json (по умолчанию ONEC_DUMP)")
	workspace = fs.String("workspace", app.DefaultStandaloneWorkspace(), "рабочий каталог оценки: здесь её индекс живёт между запусками")
	syntaxIndex = fs.String("syntax-index", app.DefaultSyntaxIndexPath(), "индекс синтаксиса платформы (cmd/syntaxgen)")
	// Без флага карточек нет: прогон не должен зависеть от того, что лежит в
	// каталоге настроек пользователя.
	fs.Func("cards", "каталог карточек поиска (cmd/apicards); server: каталог сервера; none или без флага: поиск без карточек", func(v string) error {
		switch v {
		case "server":
			v = app.DefaultAPICardsDir()
		case "none":
			v = ""
		}
		cardsDir = v
		app.ConfigureAPICards(v)
		return nil
	})
	reindex = fs.Bool("reindex", false, "пересобрать индекс целиком (после правки выгрузки или смены схемы индекса); новый проект индексируется и без флага")
	return
}

// cardsDir: каталог карточек, с которым идёт прогон; пусто: без карточек.
var cardsDir string

// cardsInfo называет наборы карточек прогона одной строкой: имя набора и
// начало отпечатка его содержимого ("bsp-3.1.11.366@3fa1c2d4e5b6,
// ut-11.5.22@9b0c..."); пусто: карточек нет. Отпечаток, а не число карточек:
// набор, пересобранный заново, носит то же имя и то же число.
func cardsInfo() (string, error) {
	packs, err := app.APICardPacks(cardsDir)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(packs))
	for i, h := range packs {
		parts[i] = h.Pack + "@" + short(h.Digest)
	}
	return strings.Join(parts, ", "), nil
}

// checkCards сверяет, что прогон идёт с теми карточками, которыми будет
// подписан: каталог задан, а поиск карточек не прочитал или не привязал ни
// одной к методам этой выгрузки: такой прогон мерил бы поиск без карточек
// под видом поиска с карточками.
func checkCards(ctx context.Context, p *project) error {
	if cardsDir == "" {
		return nil
	}
	resp, err := p.api.FindAPI(ctx, app.FindAPIInput{})
	if err != nil {
		return err
	}
	for _, w := range resp.Warnings {
		if w.Code == "api_cards_unreadable" {
			return fmt.Errorf("карточки из %s не прочитаны: %s", cardsDir, w.Message)
		}
	}
	if resp.Items[0].Cards == 0 {
		return fmt.Errorf("карточки из %s не подошли ни одному методу выгрузки: каталог пуст или наборы от другой конфигурации", cardsDir)
	}
	log.Printf("карточек привязано к методам: %d", resp.Items[0].Cards)
	return nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func runTasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	root, workspace, syntaxIndex, reindex := projectFlags(fs)
	work := fs.String("work", defaultWork, "рабочий каталог генератора (вне git)")
	clean := fs.Bool("clean", false, "удалить пачки и ответы моделей прежнего запуска")
	var opt tasksOptions
	fs.IntVar(&opt.Core, "bsp-core", 100, "сколько самых вызываемых методов библиотеки взять")
	fs.IntVar(&opt.Rest, "bsp-rest", 200, "сколько случайных методов библиотеки взять сверх самых вызываемых")
	fs.IntVar(&opt.Other, "other", 250, "сколько случайных прикладных методов взять")
	fs.IntVar(&opt.Seed, "seed", 1, "зерно выборки")
	fs.IntVar(&opt.Batch, "batch", 50, "задач в одной пачке для модели")
	fs.Parse(args)
	if err := opt.validate(); err != nil {
		return err
	}

	ctx := context.Background()
	p, err := openProject(ctx, *root, *workspace, *syntaxIndex, *reindex)
	if err != nil {
		return err
	}
	defer p.close()

	tasks, err := buildTasks(ctx, p, opt)
	if err != nil {
		return err
	}
	in := make([]writerIn, len(tasks))
	for i, t := range tasks {
		in[i] = writerIn{ID: t.ID, Snippet: t.Snippet}
	}
	n, err := writeBatches(filepath.Join(*work, writerDir), writerPrompt, in, opt.Batch, *clean)
	if err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(*work, tasksFile), tasks); err != nil {
		return err
	}
	log.Printf("задач %d, пачек для пишущей модели %d: %s", len(tasks), n, filepath.Join(*work, writerDir))
	return nil
}

func runJudge(args []string) error {
	fs := flag.NewFlagSet("judge", flag.ExitOnError)
	work := fs.String("work", defaultWork, "рабочий каталог генератора (вне git)")
	batch := fs.Int("batch", 50, "пар в одной пачке для модели")
	clean := fs.Bool("clean", false, "удалить пачки и вердикты прежнего запуска")
	fs.Parse(args)

	tasks, written, err := readWritten(*work)
	if err != nil {
		return err
	}
	dir := filepath.Join(*work, judgeDir)
	in := judgeInputs(tasks, written)
	n, err := writeBatches(dir, judgePrompt, in, *batch, *clean)
	if err != nil {
		return err
	}
	log.Printf("ответов пишущей модели %d из %d задач, на проверку %d пар, пачек %d: %s",
		len(written), len(tasks), len(in), n, dir)
	return nil
}

func readWritten(work string) ([]task, []writerOut, error) {
	tasks, err := readJSONL[task](filepath.Join(work, tasksFile))
	if err != nil {
		return nil, nil, err
	}
	written, err := readGlobJSONL[writerOut](filepath.Join(work, writerDir, "out-*.jsonl"))
	return tasks, written, err
}

// datasetMeta: сведения о наборе рядом с ним: на чём и как построен.
type datasetMeta struct {
	Built  string `json:"built"`
	Seed   int    `json:"seed"`
	Judges int    `json:"judges"`
	Funnel funnel `json:"funnel"`
	// Sections: пар по секциям, половинам и видам запроса.
	Sections map[string]int `json:"sections"`
}

func runAssemble(args []string) error {
	fs := flag.NewFlagSet("assemble", flag.ExitOnError)
	work := fs.String("work", defaultWork, "рабочий каталог генератора (вне git)")
	out := fs.String("out", "", "файл набора (обязателен)")
	soft := fs.String("soft", "", "записать сюда прямые запросы, отсеянные проверкой как неточные (в набор не идут)")
	seed := fs.Int("seed", 1, "зерно деления на половины")
	judges := fs.Int("judges", 2, "сколько проверяющих должны принять пару")
	fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("assemble: флаг -out обязателен")
	}
	if *judges < 1 {
		return fmt.Errorf("assemble: проверяющих должно быть не меньше одного: %d", *judges)
	}

	tasks, written, err := readWritten(*work)
	if err != nil {
		return err
	}
	judged, err := readVerdicts(filepath.Join(*work, judgeDir, "out-*.jsonl"))
	if err != nil {
		return err
	}
	res, err := assemble(tasks, written, judged, *seed, *judges)
	if err != nil {
		return err
	}
	if err := writeJSONL(*out, res.Pairs); err != nil {
		return err
	}
	if *soft != "" {
		if err := writeJSONL(*soft, res.Soft); err != nil {
			return err
		}
	}
	meta := datasetMeta{Built: time.Now().Format("2006-01-02"), Seed: *seed, Judges: *judges, Funnel: res.Funnel, Sections: map[string]int{}}
	for _, p := range res.Pairs {
		meta.Sections[p.Section]++
		meta.Sections[p.Section+"/"+p.Split]++
		meta.Sections[p.Section+"/"+p.Variant]++
	}
	if err := writeJSON(strings.TrimSuffix(*out, filepath.Ext(*out))+".meta.json", meta); err != nil {
		return err
	}
	log.Printf("воронка: %+v", res.Funnel)
	log.Printf("набор: %s, пар %d (%v), отсеяно как неточные %d", *out, len(res.Pairs), meta.Sections, len(res.Soft))
	return nil
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	data := fs.String("data", "", "файл набора (обязателен)")
	var lim datasetLimits
	fs.IntVar(&lim.Total, "min", 0, "не меньше пар всего")
	fs.IntVar(&lim.BSP, "min-bsp", 0, "не меньше пар секции bsp")
	fs.IntVar(&lim.Other, "min-other", 0, "не меньше пар секции other")
	fs.Parse(args)

	pairs, err := readJSONL[pair](*data)
	if err != nil {
		return err
	}
	if err := checkDataset(pairs, lim); err != nil {
		return err
	}
	fmt.Printf("НАБОР В ПОРЯДКЕ: пар %d\n", len(pairs))
	return nil
}

// datasetDigest: отпечаток набора. Перевод строк в отпечаток не входит:
// checkout на Windows с автозаменой переводов строк не должен делать базу
// «базой другого набора».
func datasetDigest(data []byte) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(string(data), "\r", "")))
	return hex.EncodeToString(sum[:])
}

func runScore(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	root, workspace, syntaxIndex, reindex := projectFlags(fs)
	data := fs.String("data", "", "файл набора (обязателен)")
	baseline := fs.String("baseline", "", "файл базы: прогон не ниже базы, иначе ошибка")
	writeBaseline := fs.String("write-baseline", "", "записать итог прогона как новую базу; вместе с -baseline пишется, только если прогон не ниже прежней")
	raw := fs.String("out", "", "записать исход каждого запроса (место и первые методы выдачи)")
	jobs := fs.Int("jobs", 4, "сколько запросов выполнять одновременно; для замера времени вызова ставьте 1")
	split := fs.String("split", "", "мерить только одну половину набора (dev: настройка правил, проверочную половину при настройке не смотрят); с базой не сравнивается")
	fs.Parse(args)
	if *split != "" && (*baseline != "" || *writeBaseline != "") {
		return fmt.Errorf("score: -split меряет половину набора, база снимается и сравнивается только на целом")
	}
	if *jobs < 1 {
		return fmt.Errorf("score: потоков должно быть не меньше одного: %d", *jobs)
	}

	bytes, err := os.ReadFile(*data)
	if err != nil {
		return err
	}
	pairs, err := decodeJSONL[pair](strings.NewReader(string(bytes)), *data)
	if err != nil {
		return err
	}
	if err := checkDataset(pairs, datasetLimits{}); err != nil {
		return err
	}
	if *split != "" {
		var half []pair
		for _, pr := range pairs {
			if pr.Split == *split {
				half = append(half, pr)
			}
		}
		if len(half) == 0 {
			return fmt.Errorf("score: в наборе нет половины %q", *split)
		}
		pairs = half
	}

	ctx := context.Background()
	p, err := openProject(ctx, *root, *workspace, *syntaxIndex, *reindex)
	if err != nil {
		return err
	}
	defer p.close()

	// Ожидаемый метод обязан быть в каталоге: иначе запрос не найдётся ни при
	// каком поиске, и набор мерил бы не поиск, а расхождение с выгрузкой.
	cat, err := readCatalog(ctx, p)
	if err != nil {
		return err
	}
	if err := checkCards(ctx, p); err != nil {
		return err
	}
	var missing []string
	for _, pr := range pairs {
		for _, a := range pr.Accept {
			if !cat.calls[strings.ToLower(a)] {
				missing = append(missing, pr.ID+" "+a)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("в каталоге выгрузки нет %d ожидаемых методов (набор построен на другой выгрузке?): %s",
			len(missing), strings.Join(missing[:min(5, len(missing))], "; "))
	}

	// Запросы независимы и идут в несколько потоков: сервер так же отвечает на
	// параллельные вызовы инструмента. Время вызова при этом растёт от
	// конкуренции за процессор; честная медиана снимается с -jobs 1.
	results := make([]result, len(pairs))
	errs := make([]error, len(pairs))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < *jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				results[i], errs[i] = scoreOne(ctx, p, pairs[i])
			}
		}()
	}
	for i := range pairs {
		next <- i
		if (i+1)%100 == 0 {
			log.Printf("запросов %d из %d", i+1, len(pairs))
		}
	}
	close(next)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("пара %s, запрос %q: %w", pairs[i].ID, pairs[i].Query, err)
		}
	}

	groups, median, err := summarize(pairs, results)
	if err != nil {
		return err
	}
	cards, err := cardsInfo()
	if err != nil {
		return err
	}
	rep := report{Dataset: datasetDigest(bytes), Catalog: cat.info, Cards: cards, Groups: groups, MedianMS: median, Jobs: *jobs}
	fmt.Print(formatReport(rep))
	if *raw != "" {
		if err := writeJSON(*raw, results); err != nil {
			return err
		}
	}
	// Сравнение идёт раньше записи: прогон ниже базы новой базой не становится.
	if *baseline != "" {
		var base report
		data, err := os.ReadFile(*baseline)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &base); err != nil {
			return fmt.Errorf("%s: %w", *baseline, err)
		}
		drops, err := compare(rep, base)
		if err != nil {
			return err
		}
		if len(drops) > 0 {
			return fmt.Errorf("НИЖЕ БАЗЫ:\n  %s", strings.Join(drops, "\n  "))
		}
		fmt.Println("НЕ НИЖЕ БАЗЫ")
	}
	if *writeBaseline != "" {
		if err := writeJSON(*writeBaseline, rep); err != nil {
			return err
		}
		fmt.Printf("БАЗА ЗАПИСАНА: %s\n", *writeBaseline)
	}
	return nil
}

// runQuery печатает выдачу find_api на один запрос: разобрать, почему метод
// стоит там, где стоит, проще по живой выдаче, чем по сводной таблице.
func runQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	root, workspace, syntaxIndex, reindex := projectFlags(fs)
	limit := fs.Int("limit", 0, "limit инструмента; 0: выдача по умолчанию")
	returns := fs.String("returns", "", "отбор по типу возвращаемого значения")
	accepts := fs.String("accepts", "", "отбор по типу параметра")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("query: запрос не задан")
	}
	ctx := context.Background()
	p, err := openProject(ctx, *root, *workspace, *syntaxIndex, *reindex)
	if err != nil {
		return err
	}
	defer p.close()
	resp, err := p.api.FindAPI(ctx, app.FindAPIInput{Query: strings.Join(fs.Args(), " "), Limit: *limit, Returns: *returns, Accepts: *accepts})
	if err != nil {
		return err
	}
	item := resp.Items[0]
	section := func(name string, items []app.APIMethodItem, more []app.APIBriefItem, matched int) {
		fmt.Printf("%s: совпало %d\n", name, matched)
		for i, it := range items {
			fmt.Printf("  %2d. %s: %s\n", i+1, it.Call, it.Summary)
			if it.Returns != "" || it.Calls > 0 {
				fmt.Printf("      возвращает: %s; вызовов из других модулей: %d; пример: %s\n", it.Returns, it.Calls, it.Example)
			}
		}
		for i, it := range more {
			fmt.Printf("  %2d+ %s: %s\n", len(items)+i+1, it.Call, it.Summary)
		}
	}
	section("bsp", item.BSP, item.BSPMore, item.BSPMatched)
	section("other", item.Other, item.OtherMore, item.OtherMatched)
	return nil
}

// scoreOne задаёт find_api запрос набора дважды: без limit (это видит агент)
// и с выдачей до потолка, и находит место ожидаемого метода в каждой. Секция
// читается целиком: полные описания, за ними короткий список. Метод ищется в
// обеих секциях ответа.
func scoreOne(ctx context.Context, p *project, pr pair) (result, error) {
	section := func(items []app.APIMethodItem, more []app.APIBriefItem) []string {
		out := make([]string, 0, len(items)+len(more))
		for _, it := range items {
			out = append(out, it.Call)
		}
		for _, it := range more {
			out = append(out, it.Call)
		}
		return out
	}
	lists := func(item app.APISearchItem, depth int) (bsp, other []string) {
		bsp, other = section(item.BSP, item.BSPMore), section(item.Other, item.OtherMore)
		return bsp[:min(depth, len(bsp))], other[:min(depth, len(other))]
	}
	t0 := time.Now()
	shown, err := p.api.FindAPI(ctx, app.FindAPIInput{Query: pr.Query})
	if err != nil {
		return result{}, err
	}
	took := time.Since(t0)
	deep, err := p.api.FindAPI(ctx, app.FindAPIInput{Query: pr.Query, Limit: scoreDepth})
	if err != nil {
		return result{}, err
	}
	shownBSP, shownOther := lists(shown.Items[0], scoreShown)
	deepBSP, deepOther := lists(deep.Items[0], scoreDepth)
	top := shownBSP
	if pr.Section == sectionOther {
		top = shownOther
	}
	return result{
		ID:         pr.ID,
		Shown:      bestPosition(pr.Accept, shownBSP, shownOther),
		Deep:       bestPosition(pr.Accept, deepBSP, deepOther),
		DurationMS: float64(took.Microseconds()) / 1000, Top: top[:min(5, len(top))],
	}, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeText(path, string(data)+"\n")
}

func writeText(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
