// Command apicards строит карточки поиска для find_api: по каждому методу
// программного интерфейса несколько формулировок задачи словами разработчика.
// По ним метод находится, когда задачу описывают другими словами, чем его имя
// и комментарий («оставить в массиве уникальные значения» находит
// СвернутьМассив).
//
// Формулировки пишет языковая модель по сигнатуре и комментарию метода, один
// раз на версию библиотеки или конфигурации. Сервер читает готовый файл
// карточек и модель при поиске не зовёт.
//
// Шаги (из корня репозитория, каталог проекта: флаг -project или ONEC_DUMP):
//
//	go run ./cmd/apicards tasks -project <выгрузка> -section bsp   # пачки для модели
//	(модель: work/in-NN.jsonl → out-NN.jsonl по work/PROMPT.md)
//	go run ./cmd/apicards assemble -name bsp-3.1.11.366            # файл карточек
//
// Файл карточек кладётся в app.DefaultAPICardsDir() (<каталог настроек
// пользователя>/mcp1c/api-cards), где его ищет сервер; -out пишет в другое
// место, серверу тогда нужен флаг --api-cards или MCP_1C_API_CARDS. Рабочий
// каталог содержит комментарии методов конфигурации и в git не идёт.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

const defaultWork = "cmd/apicards/work"

// prompt: задание модели. Пример в нём взят с метода, которого нет в эталоне
// evals/findapi (ТолькоЛатиницаВСтроке): пример из пары эталона подсказывал бы
// модели слова запроса, которым потом меряется поиск. Наборы для ut_demo от
// 03-04.10.2026 собраны с прежним примером (СвернутьМассив, пара настроечной
// половины), см. docs/find-api-eval.md.
const prompt = `# Задание: карточки поиска методов

Ты разработчик 1С, хорошо знающий типовые конфигурации и БСП. Во входном файле по одному
методу программного интерфейса на строку: JSON с полями id, call, signature, context, doc.

Для каждого метода напиши карточку поиска. По ней метод должен находиться, когда разработчик
описывает свою задачу своими словами и НЕ знает, как метод называется.

Поле "tasks": от трёх до пяти коротких формулировок задачи (от трёх до девяти слов каждая),
как их написал бы разработчик в поиске перед тем, как писать такой код самому.

- Формулировки должны отличаться словами: синонимы глаголов и предметов, обиходные названия
  («в строке только английские буквы», «нет ли кириллицы в идентификаторе», «строка из
  латинских символов»).
- Не повторяй имя метода и первую строку его описания: они и так в индексе. Нужны слова,
  которых там НЕТ, но которыми задачу называют.
- Термины платформы и предметной области не искажай: регламентное задание, план обмена,
  табличная часть, реквизит остаются собой.
- Описывай то, что метод делает по описанию. Не приписывай лишнего.

Поле "instead": если метод заменяет типовой самописный код, назови этот код одной фразой
словами разработчика («посимвольный цикл с КодСимвола и сравнением диапазонов»). Если такого
кода нет, пустая строка.

Метод-обработчик события (При..., Перед..., После...), служебный метод или метод с пустым
описанием: напиши столько формулировок, сколько получается по имени и сигнатуре, хотя бы одну.

Вывод: файл с тем же номером, out-NN.jsonl рядом со входным. По строке JSON на метод, каждый
метод входа ровно один раз, в том же порядке, id без изменений:

{"id": "m1a2b3c4d", "tasks": ["в строке только английские буквы", "нет ли кириллицы в идентификаторе", "строка из латинских символов"], "instead": "посимвольный цикл с КодСимвола и сравнением диапазонов"}

Читай только названные тебе входные файлы и пиши только выходные. Других файлов не открывай,
поиском, инструментами и скриптами не пользуйся: карточка пишется по описанию метода во входе,
а не по коду конфигурации и не по чужим наборам запросов.
`

// cardIn: что видит модель.
type cardIn struct {
	ID        string `json:"id"`
	Call      string `json:"call"`
	Signature string `json:"signature"`
	Context   string `json:"context,omitempty"`
	Doc       string `json:"doc,omitempty"`
}

// cardOut: что модель отдаёт.
type cardOut struct {
	ID      string   `json:"id"`
	Tasks   []string `json:"tasks"`
	Instead string   `json:"instead"`
}

// maxDocRunes: комментарий длиннее режется: назначение и параметры стоят в
// начале, а таблицы свойств и примеры на формулировку задачи не влияют.
const maxDocRunes = 1500

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "tasks":
		err = runTasks(args)
	case "assemble":
		err = runAssemble(args)
	default:
		usage()
	}
	if err != nil {
		log.Fatalf("ОШИБКА: %v", err)
	}
}

func usage() {
	log.Fatal("использование: apicards tasks|assemble [флаги]; описание шагов в начале cmd/apicards/main.go")
}

// methodID: идентификатор метода во входе модели: по выражению вызова.
func methodID(call string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(call)))
	return "m" + hex.EncodeToString(sum[:4])
}

func runTasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	root := fs.String("project", firstEnv("ONEC_DUMP", "MCP1C_SPIKE_DUMP"), "каталог проекта с 1c-project.json (по умолчанию ONEC_DUMP)")
	workspace := fs.String("workspace", app.DefaultStandaloneWorkspace(), "рабочий каталог с индексом проекта")
	syntaxIndex := fs.String("syntax-index", app.DefaultSyntaxIndexPath(), "индекс синтаксиса платформы (cmd/syntaxgen)")
	reindex := fs.Bool("reindex", false, "пересобрать индекс целиком")
	section := fs.String("section", "bsp", "какие методы брать: bsp (библиотека), other (конфигурация), all")
	work := fs.String("work", defaultWork, "рабочий каталог генератора (вне git)")
	batch := fs.Int("batch", 50, "методов в одной пачке для модели")
	fs.Parse(args)
	if *batch <= 0 {
		return fmt.Errorf("размер пачки должен быть положительным: %d", *batch)
	}
	if stale, _ := filepath.Glob(filepath.Join(*work, "*-*.jsonl")); len(stale) > 0 {
		return fmt.Errorf("в %s остались пачки прежнего запуска (%d файлов): уберите их", *work, len(stale))
	}

	ctx := context.Background()
	projects, _, err := app.OpenStandalone(ctx, app.StandaloneOptions{Root: *root, Workspace: *workspace, SyntaxIndex: *syntaxIndex, Reindex: *reindex})
	if err != nil {
		return err
	}
	defer projects.Close()
	resp, err := app.NewAPIService(projects).Catalog(ctx)
	if err != nil {
		return err
	}
	if len(resp.Items) == 0 {
		return fmt.Errorf("каталог методов пуст: проект не проиндексирован?")
	}
	item := resp.Items[0]
	var methods []app.APIMethodItem
	switch *section {
	case "bsp":
		methods = item.BSP
	case "other":
		methods = item.Other
	case "all":
		methods = append(append(methods, item.BSP...), item.Other...)
	default:
		return fmt.Errorf("неизвестная секция %q: bsp, other или all", *section)
	}

	var in []cardIn
	seen := map[string]string{}
	for _, m := range methods {
		if m.Deprecated {
			continue // устаревшему методу карточка не нужна: искать его незачем
		}
		id := methodID(m.Call)
		if prev, dup := seen[id]; dup {
			return fmt.Errorf("методы %s и %s получили один идентификатор %s", prev, m.Call, id)
		}
		seen[id] = m.Call
		doc := []rune(m.Doc)
		if len(doc) > maxDocRunes {
			doc = doc[:maxDocRunes]
		}
		in = append(in, cardIn{ID: id, Call: m.Call, Signature: m.Signature, Context: m.Context, Doc: string(doc)})
	}
	n := 0
	for from := 0; from < len(in); from += *batch {
		n++
		if err := writeJSONL(filepath.Join(*work, fmt.Sprintf("in-%03d.jsonl", n)), in[from:min(from+*batch, len(in))]); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(*work, "PROMPT.md"), []byte(prompt), 0o644); err != nil {
		return err
	}
	log.Printf("методов %d (секция %s, библиотека %s), пачек %d: %s", len(in), *section, item.BSPVersion, n, *work)
	return nil
}

func runAssemble(args []string) error {
	fs := flag.NewFlagSet("assemble", flag.ExitOnError)
	work := fs.String("work", defaultWork, "рабочий каталог генератора (вне git)")
	name := fs.String("name", "", "имя файла карточек без расширения, например bsp-3.1.11.366 (обязательно)")
	out := fs.String("out", "", "куда писать файл; по умолчанию каталог карточек сервера")
	source := fs.String("source", "", "чем написаны карточки (модель), для заголовка файла")
	fs.Parse(args)
	if *name == "" {
		return fmt.Errorf("assemble: флаг -name обязателен")
	}

	inputs, err := readGlob[cardIn](filepath.Join(*work, "in-*.jsonl"))
	if err != nil {
		return err
	}
	outputs, err := readGlob[cardOut](filepath.Join(*work, "out-*.jsonl"))
	if err != nil {
		return err
	}
	cards, missing, err := buildCards(inputs, outputs)
	if err != nil {
		return err
	}

	path := *out
	if path == "" {
		dir := app.DefaultAPICardsDir()
		if dir == "" {
			return fmt.Errorf("assemble: каталог карточек сервера не определён, задайте файл флагом -out")
		}
		path = filepath.Join(dir, *name+".jsonl")
	}
	header := app.APICardsHeader{Pack: *name, Built: time.Now().Format("2006-01-02"), Source: *source, Cards: len(cards)}
	if err := app.WriteAPICards(path, header, cards); err != nil {
		return err
	}
	log.Printf("методов во входе %d, карточек %d, без карточки %d: %s", len(inputs), len(cards), missing, path)
	return nil
}

// buildCards сводит вход модели и её ответы в карточки, по выражению вызова.
// missing: сколько методов входа остались без карточки (модель пропустила
// строку или не написала ни одной формулировки). Ответ на метод, которого нет
// во входе, и повторный ответ останавливают сборку: это след прежнего запуска
// или сбившийся id, и молча приклеить такую карточку к методу нельзя.
func buildCards(inputs []cardIn, outputs []cardOut) (cards []app.APICard, missing int, err error) {
	written := map[string]cardOut{}
	for _, o := range outputs {
		if _, dup := written[o.ID]; dup {
			return nil, 0, fmt.Errorf("метод %s: две карточки", o.ID)
		}
		written[o.ID] = o
	}
	known := map[string]bool{}
	for _, in := range inputs {
		known[in.ID] = true
		o, ok := written[in.ID]
		if !ok {
			missing++
			continue
		}
		card := app.APICard{Call: in.Call, Instead: strings.Join(strings.Fields(o.Instead), " ")}
		for _, t := range o.Tasks {
			if t = strings.Join(strings.Fields(t), " "); t != "" {
				card.Tasks = append(card.Tasks, t)
			}
		}
		if len(card.Tasks) == 0 && card.Instead == "" {
			missing++
			continue
		}
		cards = append(cards, card)
	}
	for id := range written {
		if !known[id] {
			return nil, 0, fmt.Errorf("карточка для неизвестного метода %s: в рабочем каталоге остались ответы прежнего запуска", id)
		}
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].Call < cards[j].Call })
	return cards, missing, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func writeJSONL[T any](path string, rows []T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func readGlob[T any](pattern string) ([]T, error) {
	names, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []T
	for _, name := range names {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for n := 1; sc.Scan(); n++ {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var v T
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				f.Close()
				return nil, fmt.Errorf("%s: строка %d: %w", name, n, err)
			}
			out = append(out, v)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
