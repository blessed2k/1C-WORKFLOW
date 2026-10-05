package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Половины набора. Правила поиска настраиваются на настроечной половине, а
// качество называется по проверочной: число, полученное на тех же запросах,
// на которых правила подбирались, оценкой не является.
const (
	splitDev  = "dev"
	splitTest = "test"
)

// Виды запроса. Прямой написан по фрагменту кода, и в нём часто звучат слова
// имени метода: переменную, куда кладут результат, разработчик называет по
// методу. Пересказ говорит о той же задаче другими словами: так спросит тот,
// кто кода не видел, и именно на нём видно, держит ли поиск синонимы.
const (
	variantDirect     = "direct"
	variantParaphrase = "paraphrase"
	// paraphraseSuffix отличает пересказ от прямого запроса той же задачи в
	// идентификаторе пары и во входе проверяющей модели.
	paraphraseSuffix = "p"
)

// Вердикты проверяющей модели.
const (
	verdictYes     = "yes"
	verdictPartial = "partial"
	verdictNo      = "no"
)

// task: запись генератора: метод, место его вызова и фрагмент кода со
// скрытым именем. Содержит код конфигурации и поэтому живёт только в рабочем
// каталоге, вне git.
type task struct {
	// ID выводится из метода и места вызова (taskID), а не из порядкового
	// номера: ответ модели на задачу прежнего запуска к задаче нового запуска
	// не приклеится.
	ID        string   `json:"id"`
	Section   string   `json:"section"`
	Stratum   string   `json:"stratum"`
	Call      string   `json:"call"`
	Accept    []string `json:"accept"`
	UID       string   `json:"uid"`
	Signature string   `json:"signature"`
	Doc       string   `json:"doc"`
	Callers   int      `json:"callers"`
	Site      callSite `json:"site"`
	Snippet   string   `json:"snippet"`
}

// taskID: идентификатор задачи по методу и месту вызова.
func taskID(uid string, site callSite) string {
	sum := sha256.Sum256([]byte(uid + "\x00" + site.Module + "\x00" + strconv.Itoa(site.Line)))
	return "t" + hex.EncodeToString(sum[:4])
}

// writerIn/writerOut: что видит и что отдаёт модель, пишущая запрос. Ни
// имени метода, ни его описания на входе нет.
type writerIn struct {
	ID      string `json:"id"`
	Snippet string `json:"snippet"`
}

type writerOut struct {
	ID    string `json:"id"`
	Clear bool   `json:"clear"`
	Query string `json:"query"`
	// Alt: та же задача другими словами; пусто, если пересказать нельзя.
	Alt string `json:"alt,omitempty"`
}

// judgeIn/judgeOut: что видит и что отдаёт проверяющая модель: запрос и
// метод с полным описанием.
type judgeIn struct {
	// ID включает отпечаток текста запроса (judgeID): вердикт, вынесенный
	// прежней редакции запроса, к новой не относится.
	ID        string `json:"id"`
	Query     string `json:"query"`
	Call      string `json:"call"`
	Signature string `json:"signature"`
	Doc       string `json:"doc"`
}

type judgeOut struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	// Judge: кто из проверяющих вынес вердикт. В файле ответа поля нет: его
	// ставит чтение по имени файла (judgeOfFile).
	Judge string `json:"-"`
}

// judgeOfFile: метка проверяющего из имени файла ответа: out-NN-a.jsonl
// даёт "a". У файла без метки (out-NN.jsonl) проверяющий один, без имени.
func judgeOfFile(name string) string {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	parts := strings.Split(base, "-")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-1]
}

// readVerdicts читает вердикты всех проверяющих и помечает каждый тем, чей он.
func readVerdicts(pattern string) ([]judgeOut, error) {
	names, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []judgeOut
	for _, name := range names {
		rows, err := readJSONL[judgeOut](name)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].Judge = judgeOfFile(name)
		}
		out = append(out, rows...)
	}
	return out, nil
}

// judgeID: идентификатор пары на проверке: задача, вид запроса и отпечаток
// его текста.
func judgeID(task, variant, query string) string {
	sum := sha256.Sum256([]byte(query))
	return task + "." + variant[:1] + "." + hex.EncodeToString(sum[:3])
}

// pair: строка набора: запрос и методы, которые на него засчитываются.
type pair struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Split   string `json:"split"`
	Stratum string `json:"stratum"`
	Variant string `json:"variant"`
	Query   string `json:"query"`
	// Accept: засчитываемые выражения вызова; первым стоит метод, по вызову
	// которого запрос написан, за ним его близнецы по месту исполнения.
	Accept []string `json:"accept"`
	// NameOverlap: есть ли у запроса общие слова с именем метода. Запросы без
	// общих слов поиск по словам имени не берёт по построению.
	NameOverlap bool `json:"nameOverlap"`
	// Callers: число вызовов метода из других модулей, не больше потолка
	// обхода ссылок (maxRefPages страниц find_references).
	Callers int      `json:"callers"`
	Site    callSite `json:"site"`
}

// funnel: воронка отбора: сколько задач дошло до каждого шага. Шаги считают
// прямой запрос; Paraphrases: сколько пар добавили принятые пересказы.
type funnel struct {
	Tasks int `json:"tasks"`
	// SameName: задачи по методу, одноимённому с уже взятым. Одноимённые
	// методы засчитываются друг за друга, и вторая задача по тому же имени
	// считала бы один случай дважды.
	SameName    int `json:"sameName"`
	NoAnswer    int `json:"noAnswer"`
	Unclear     int `json:"unclear"`
	NamedMethod int `json:"namedMethod"`
	NotJudged   int `json:"notJudged"`
	Partial     int `json:"partial"`
	Rejected    int `json:"rejected"`
	Direct      int `json:"direct"`
	Paraphrases int `json:"paraphrases"`
	// ParaphrasesNotJudged: пересказы, по которым вердиктов меньше, чем
	// проверяющих (пропавшая пачка одной из моделей видна здесь).
	ParaphrasesNotJudged int `json:"paraphrasesNotJudged"`
	Pairs                int `json:"pairs"`
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeJSONL[T](f, path)
}

func decodeJSONL[T any](r io.Reader, name string) ([]T, error) {
	var out []T
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("%s: строка %d: %w", name, n, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func encodeJSONL[T any](rows []T) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

func writeJSONL[T any](path string, rows []T) error {
	body, err := encodeJSONL(rows)
	if err != nil {
		return err
	}
	return writeText(path, string(body))
}

// readGlobJSONL читает все файлы по маске как один список.
func readGlobJSONL[T any](pattern string) ([]T, error) {
	names, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []T
	for _, name := range names {
		rows, err := readJSONL[T](name)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// words разбивает текст на слова в нижнем регистре, идентификаторы делит по
// границам CamelCase. Разбор намеренно свой, а не взятый у ранжировщика
// find_api: признак «у запроса есть общие слова с именем метода» не должен
// меняться вместе с правилами поиска, которые по нему меряются.
func words(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ReplaceAll(strings.ToLower(string(cur)), "ё", "е"))
			cur = cur[:0]
		}
	}
	runes := []rune(text)
	for i, r := range runes {
		if !unicode.IsLetter(r) {
			flush()
			continue
		}
		if len(cur) > 0 && unicode.IsUpper(r) {
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(cur[len(cur)-1]) || nextLower {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// overlapPrefix: слова считаются общими при совпадении первых четырёх букв
// (слово короче сравнивается целиком).
//
// упрощение: начало слова вместо основы сводит и чужие слова (право и
// правило) и не сводит формы с чередованием в корне. Признак нужен только
// чтобы отделить запросы, у которых с именем метода нет вообще ничего общего;
// для большего понадобится морфологический разбор.
const overlapPrefix = 4

func sameStem(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < overlapPrefix || len(rb) < overlapPrefix {
		return a == b
	}
	return string(ra[:overlapPrefix]) == string(rb[:overlapPrefix])
}

// nameOverlap сообщает, есть ли у запроса хоть одно общее слово с именем
// метода (последний сегмент выражения вызова).
func nameOverlap(query, call string) bool {
	name := call[strings.LastIndex(call, ".")+1:]
	for _, q := range words(query) {
		if len([]rune(q)) < 3 {
			continue
		}
		for _, n := range words(name) {
			if sameStem(q, n) {
				return true
			}
		}
	}
	return false
}

// splitOf делит набор пополам по семейству метода: близнецы одного метода
// попадают в одну половину, иначе настроенное на одном проверялось бы на
// втором.
func splitOf(seed int, call string) string {
	if orderKey(seed, callFamily(call))%2 == 0 {
		return splitDev
	}
	return splitTest
}

// maxQueryRunes: запрос длиннее уже не запрос, а пересказ фрагмента.
const maxQueryRunes = 200

// usableQuery приводит запрос модели к одной строке и сообщает, годится ли
// он: не пуст, не длиннее потолка.
func usableQuery(raw string) (string, bool) {
	q := strings.Join(strings.Fields(raw), " ")
	return q, q != "" && len([]rune(q)) <= maxQueryRunes
}

// verdictOf сводит вердикты нескольких проверяющих по одной паре: пара
// принята, только когда «да» сказали все; иначе решает самый строгий. Пусто:
// вердиктов меньше, чем требуется проверяющих.
func verdictOf(votes []string, judges int) string {
	if len(votes) < judges {
		return ""
	}
	out := verdictYes
	for _, v := range votes {
		switch v {
		case verdictNo:
			return verdictNo
		case verdictPartial:
			out = verdictPartial
		}
	}
	return out
}

// assembled: что вышло из сборки.
type assembled struct {
	// Pairs: набор.
	Pairs []pair
	// Soft: прямые запросы, отсеянные проверкой как неточные или общие. В
	// набор не идут; по ним видно, насколько отбор проверяющей моделью
	// облегчил набор.
	Soft   []pair
	Funnel funnel
}

// assemble собирает набор из задач и ответов двух моделей. В набор идёт пара,
// у которой запрос написан (clear), не называет сам метод и признан каждым из
// judges проверяющих запросом именно про этот метод (yes). Пересказ идёт
// отдельной парой на тех же условиях и независимо от прямого запроса.
func assemble(tasks []task, written []writerOut, judged []judgeOut, seed, judges int) (assembled, error) {
	var res assembled
	queries := map[string]writerOut{}
	for _, w := range written {
		if _, dup := queries[w.ID]; dup {
			return res, fmt.Errorf("задача %s: два ответа пишущей модели", w.ID)
		}
		queries[w.ID] = w
	}
	// Считаются проверяющие, а не строки: два «да» одной модели по паре,
	// которую вторая пропустила, двумя голосами не являются.
	votes := map[string][]string{}
	voted := map[string]map[string]bool{}
	for _, j := range judged {
		switch j.Verdict {
		case verdictYes, verdictPartial, verdictNo:
		default:
			return res, fmt.Errorf("запрос %s: неизвестный вердикт %q", j.ID, j.Verdict)
		}
		if voted[j.ID] == nil {
			voted[j.ID] = map[string]bool{}
		}
		if voted[j.ID][j.Judge] {
			return res, fmt.Errorf("запрос %s: проверяющий %q вынес вердикт дважды", j.ID, j.Judge)
		}
		voted[j.ID][j.Judge] = true
		votes[j.ID] = append(votes[j.ID], j.Verdict)
		if len(votes[j.ID]) > judges {
			return res, fmt.Errorf("запрос %s: вердиктов больше, чем проверяющих (%d)", j.ID, judges)
		}
	}

	knownTask, knownQuery := map[string]bool{}, map[string]bool{}
	res.Funnel.Tasks = len(tasks)
	mk := func(t task, id, variant, query string) pair {
		return pair{
			ID: id, Section: t.Section, Split: splitOf(seed, t.Call), Stratum: t.Stratum, Variant: variant,
			Query: query, Accept: t.Accept, NameOverlap: nameOverlap(query, t.Call),
			Callers: t.Callers, Site: t.Site,
		}
	}
	// Одноимённые методы засчитываются друг за друга (equivalents.accepted),
	// и вторая задача по имени, уже давшему пару, считала бы один случай
	// дважды. Имя занимает только задача, от которой в набор что-то вошло.
	takenName := map[string]bool{}
	for _, t := range tasks {
		knownTask[t.ID] = true
		w, ok := queries[t.ID]
		alt, altOK := usableQuery(w.Alt)
		query, queryOK := usableQuery(w.Query)
		if altOK {
			knownQuery[judgeID(t.ID, variantParaphrase, alt)] = true
		}
		if queryOK {
			knownQuery[judgeID(t.ID, variantDirect, query)] = true
		}
		name := strings.ToLower(methodName(t.Call))
		if takenName[name] {
			res.Funnel.SameName++
			continue
		}
		if !ok {
			res.Funnel.NoAnswer++
			continue
		}
		if !w.Clear {
			res.Funnel.Unclear++
			continue
		}
		if altOK && !namesMethod(alt, t.Call) {
			switch verdictOf(votes[judgeID(t.ID, variantParaphrase, alt)], judges) {
			case verdictYes:
				res.Pairs = append(res.Pairs, mk(t, t.ID+paraphraseSuffix, variantParaphrase, alt))
				res.Funnel.Paraphrases++
				takenName[name] = true
			case "":
				res.Funnel.ParaphrasesNotJudged++
			}
		}
		switch {
		case !queryOK:
			res.Funnel.Unclear++
			continue
		case namesMethod(query, t.Call):
			res.Funnel.NamedMethod++
			continue
		}
		switch verdictOf(votes[judgeID(t.ID, variantDirect, query)], judges) {
		case "":
			res.Funnel.NotJudged++
		case verdictPartial:
			res.Funnel.Partial++
			res.Soft = append(res.Soft, mk(t, t.ID, variantDirect, query))
		case verdictNo:
			res.Funnel.Rejected++
		default:
			res.Pairs = append(res.Pairs, mk(t, t.ID, variantDirect, query))
			res.Funnel.Direct++
			takenName[name] = true
		}
	}
	for id := range queries {
		if !knownTask[id] {
			return res, fmt.Errorf("ответ пишущей модели на неизвестную задачу %s: в рабочем каталоге остались ответы прежнего запуска", id)
		}
	}
	for id := range votes {
		if !knownQuery[id] {
			return res, fmt.Errorf("вердикт по запросу %s, которого нет среди текущих: запрос переписан после проверки или остался вердикт прежнего запуска", id)
		}
	}
	sort.Slice(res.Pairs, func(i, j int) bool { return res.Pairs[i].ID < res.Pairs[j].ID })
	sort.Slice(res.Soft, func(i, j int) bool { return res.Soft[i].ID < res.Soft[j].ID })
	res.Funnel.Pairs = len(res.Pairs)
	return res, nil
}

// methodName: имя метода из выражения вызова.
func methodName(call string) string { return call[strings.LastIndex(call, ".")+1:] }

// namesMethod сообщает, назван ли в запросе сам метод его идентификатором.
// Имя из одного слова (Заполнить, Сообщить) за идентификатор не считается:
// это обычное слово, и запрос с ним метод не выдаёт.
func namesMethod(query, call string) bool {
	name := methodName(call)
	return compound(name) && mentions(query, name)
}

// judgeInputs готовит вход проверяющей модели: по каждой задаче с написанным
// запросом прямой запрос и пересказ отдельными строками. Запрос, назвавший
// метод, на проверку не идёт.
func judgeInputs(tasks []task, written []writerOut) []judgeIn {
	queries := map[string]writerOut{}
	for _, w := range written {
		queries[w.ID] = w
	}
	var out []judgeIn
	for _, t := range tasks {
		w, ok := queries[t.ID]
		if !ok || !w.Clear {
			continue
		}
		for _, c := range []struct{ variant, raw string }{{variantDirect, w.Query}, {variantParaphrase, w.Alt}} {
			if q, ok := usableQuery(c.raw); ok && !namesMethod(q, t.Call) {
				out = append(out, judgeIn{ID: judgeID(t.ID, c.variant, q), Query: q, Call: t.Call, Signature: t.Signature, Doc: t.Doc})
			}
		}
	}
	return out
}

// datasetLimits: нижние границы размера набора.
type datasetLimits struct{ Total, BSP, Other int }

// checkDataset проверяет набор: формат каждой строки и размер.
func checkDataset(pairs []pair, lim datasetLimits) error {
	seen := map[string]bool{}
	count := map[string]int{}
	for _, p := range pairs {
		switch {
		case p.ID == "" || seen[p.ID]:
			return fmt.Errorf("пара %q: пустой или повторный id", p.ID)
		case p.Section != sectionBSP && p.Section != sectionOther:
			return fmt.Errorf("пара %s: секция %q", p.ID, p.Section)
		case p.Split != splitDev && p.Split != splitTest:
			return fmt.Errorf("пара %s: половина %q", p.ID, p.Split)
		case p.Variant != variantDirect && p.Variant != variantParaphrase:
			return fmt.Errorf("пара %s: вид запроса %q", p.ID, p.Variant)
		case strings.TrimSpace(p.Query) == "":
			return fmt.Errorf("пара %s: пустой запрос", p.ID)
		case len(p.Accept) == 0 || p.Accept[0] == "":
			return fmt.Errorf("пара %s: нет ожидаемого метода", p.ID)
		case namesMethod(p.Query, p.Accept[0]):
			return fmt.Errorf("пара %s: запрос называет сам метод", p.ID)
		}
		seen[p.ID] = true
		count[p.Section]++
		count[p.Section+"/"+p.Split]++
	}
	if len(pairs) < lim.Total || count[sectionBSP] < lim.BSP || count[sectionOther] < lim.Other {
		return fmt.Errorf("набор мал: пар %d (нужно %d), bsp %d (нужно %d), other %d (нужно %d)",
			len(pairs), lim.Total, count[sectionBSP], lim.BSP, count[sectionOther], lim.Other)
	}
	for _, sec := range []string{sectionBSP, sectionOther} {
		for _, sp := range []string{splitDev, splitTest} {
			if count[sec] > 0 && count[sec+"/"+sp] == 0 {
				return fmt.Errorf("секция %s: половина %s пуста", sec, sp)
			}
		}
	}
	return nil
}
