package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func testTasks() []task {
	mk := func(id, section, call string) task {
		return task{ID: id, Section: section, Stratum: stratumRest, Call: call, Accept: []string{call},
			Signature: methodName(call) + "()", Doc: "Описание.", Callers: 3, Site: callSite{Module: "М.bsl", Line: 7},
			Snippet: "Процедура П()\n\t" + maskedName + "();\nКонецПроцедуры"}
	}
	return []task{
		mk("t1", sectionBSP, "ОбщегоНазначения.ЗначениеРеквизитаОбъекта"),
		mk("t2", sectionBSP, "СтроковыеФункции.РазложитьСтроку"),
		mk("t3", sectionOther, "Продажи.ЗаполнитьЦены"),
		mk("t4", sectionOther, "Продажи.ПересчитатьСуммы"),
		mk("t5", sectionOther, "Продажи.ПроверитьОстатки"),
		mk("t6", sectionOther, "Продажи.УдалитьСтроки"),
		mk("t7", sectionOther, "Продажи.БезОтвета"),
	}
}

// testWritten: ответы пишущей модели на задачи testTasks.
func testWritten() []writerOut {
	return []writerOut{
		{ID: "t1", Clear: true, Query: "  получить   значение поля по ссылке ", Alt: "прочитать колонку записи из базы"},
		{ID: "t2", Clear: false, Query: ""},
		{ID: "t3", Clear: true, Query: "вызвать ЗаполнитьЦены для документа"},
		{ID: "t4", Clear: true, Query: "пересчитать суммы в строках", Alt: "обновить итоги по товарам"},
		{ID: "t5", Clear: true, Query: "проверить остатки товаров", Alt: "вызвать ПроверитьОстатки"},
		{ID: "t6", Clear: true, Query: "убрать пустые строки"},
	}
}

// vote: вердикт одного проверяющего по запросу задачи.
func vote(task, variant, query, verdict string) judgeOut {
	return judgeOut{ID: judgeID(task, variant, query), Verdict: verdict}
}

// TestAssembleFunnel: в набор идёт только пара с написанным запросом, не
// называющим метод и принятым проверяющей моделью; каждая отсеянная задача
// посчитана на своём шаге воронки.
func TestAssembleFunnel(t *testing.T) {
	judged := []judgeOut{
		vote("t1", variantDirect, "получить значение поля по ссылке", verdictYes),
		vote("t1", variantParaphrase, "прочитать колонку записи из базы", verdictPartial),
		vote("t4", variantDirect, "пересчитать суммы в строках", verdictPartial),
		vote("t4", variantParaphrase, "обновить итоги по товарам", verdictYes),
		vote("t5", variantDirect, "проверить остатки товаров", verdictNo),
	}
	res, err := assemble(testTasks(), testWritten(), judged, 1, 1)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	want := funnel{Tasks: 7, NoAnswer: 1, Unclear: 1, NamedMethod: 1, NotJudged: 1, Partial: 1, Rejected: 1,
		Direct: 1, Paraphrases: 1, Pairs: 2}
	if res.Funnel != want {
		t.Errorf("воронка = %+v, want %+v", res.Funnel, want)
	}
	pairs := res.Pairs
	if len(pairs) != 2 {
		t.Fatalf("пар %d, want 2", len(pairs))
	}
	// Пересказ принят независимо от прямого запроса той же задачи: у t4 прямой
	// запрос отсеян, а пересказ вошёл; у t1 наоборот.
	if alt := pairs[1]; alt.ID != "t4p" || alt.Variant != variantParaphrase || alt.Query != "обновить итоги по товарам" ||
		alt.NameOverlap || alt.Section != sectionOther {
		t.Errorf("пересказ = %+v", alt)
	}
	p := pairs[0]
	if p.Variant != variantDirect || p.ID != "t1" || p.Query != "получить значение поля по ссылке" || p.Section != sectionBSP ||
		!reflect.DeepEqual(p.Accept, []string{"ОбщегоНазначения.ЗначениеРеквизитаОбъекта"}) || p.Callers != 3 {
		t.Errorf("пара = %+v", p)
	}
	if !p.NameOverlap {
		t.Errorf("NameOverlap = false: «значение» есть и в запросе, и в имени метода")
	}
	if p.Split != splitOf(1, p.Accept[0]) {
		t.Errorf("Split = %q, want %q", p.Split, splitOf(1, p.Accept[0]))
	}
	// Прямой запрос, отсеянный как неточный, лежит отдельно от набора.
	if len(res.Soft) != 1 || res.Soft[0].ID != "t4" || res.Soft[0].Query != "пересчитать суммы в строках" {
		t.Errorf("отсеянные как неточные = %+v, want один запрос t4", res.Soft)
	}

	inputs := judgeInputs(testTasks(), testWritten())
	var ids []string
	for _, in := range inputs {
		ids = append(ids, in.ID)
	}
	// Пересказ t5 называет метод и на проверку не идёт.
	wantIDs := []string{
		judgeID("t1", variantDirect, "получить значение поля по ссылке"),
		judgeID("t1", variantParaphrase, "прочитать колонку записи из базы"),
		judgeID("t4", variantDirect, "пересчитать суммы в строках"),
		judgeID("t4", variantParaphrase, "обновить итоги по товарам"),
		judgeID("t5", variantDirect, "проверить остатки товаров"),
		judgeID("t6", variantDirect, "убрать пустые строки"),
	}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Errorf("на проверку ушли %q, want %q", ids, wantIDs)
	}
	if inputs[0].Doc != "Описание." || inputs[0].Call != "ОбщегоНазначения.ЗначениеРеквизитаОбъекта" {
		t.Errorf("вход проверяющей модели = %+v", inputs[0])
	}
}

// TestAssembleTwoJudges: при двух проверяющих пара принята, только когда «да»
// сказали оба; одного вердикта мало, а несогласие решает строгий.
func TestAssembleTwoJudges(t *testing.T) {
	const q1, q4, q5 = "получить значение поля по ссылке", "пересчитать суммы в строках", "проверить остатки товаров"
	judged := []judgeOut{
		vote("t1", variantDirect, q1, verdictYes), vote("t1", variantDirect, q1, verdictYes),
		vote("t4", variantDirect, q4, verdictYes), vote("t4", variantDirect, q4, verdictPartial),
		vote("t5", variantDirect, q5, verdictYes),
		vote("t6", variantDirect, "убрать пустые строки", verdictNo), vote("t6", variantDirect, "убрать пустые строки", verdictYes),
	}
	res, err := assemble(testTasks(), testWritten(), judged, 1, 2)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(res.Pairs) != 1 || res.Pairs[0].ID != "t1" {
		t.Errorf("пары = %+v, want только t1", res.Pairs)
	}
	if fn := res.Funnel; fn.Partial != 1 || fn.NotJudged != 1 || fn.Rejected != 1 || fn.Direct != 1 {
		t.Errorf("воронка = %+v, want partial 1 (t4), notJudged 1 (t5), rejected 1 (t6), direct 1", fn)
	}
	third := append(judged, vote("t1", variantDirect, q1, verdictYes))
	if _, err := assemble(testTasks(), testWritten(), third, 1, 2); err == nil {
		t.Errorf("третий вердикт при двух проверяющих принят")
	}
}

// TestAssembleOneTaskPerMethodName: из двух задач по одноимённым методам в
// набор идёт первая; вердикт по второй чужим не считается.
func TestAssembleOneTaskPerMethodName(t *testing.T) {
	tasks := testTasks()[:2]
	tasks[1].Call, tasks[1].Accept = "ПодборТоваров.ЗначениеРеквизитаОбъекта", []string{"ПодборТоваров.ЗначениеРеквизитаОбъекта"}
	written := []writerOut{
		{ID: "t1", Clear: true, Query: "получить значение поля по ссылке"},
		{ID: "t2", Clear: true, Query: "прочитать поле объекта"},
	}
	judged := []judgeOut{
		vote("t1", variantDirect, "получить значение поля по ссылке", verdictYes),
		vote("t2", variantDirect, "прочитать поле объекта", verdictYes),
	}
	res, err := assemble(tasks, written, judged, 1, 1)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(res.Pairs) != 1 || res.Pairs[0].ID != "t1" || res.Funnel.SameName != 1 {
		t.Errorf("пары = %+v, воронка = %+v; want одна пара t1 и sameName 1", res.Pairs, res.Funnel)
	}
}

// TestAssembleRejectsForeignAnswers: ответ на задачу, которой нет, повторный
// ответ, вердикт по запросу, которого нет среди текущих, и неизвестный
// вердикт останавливают сборку, а не молча пропадают.
func TestAssembleRejectsForeignAnswers(t *testing.T) {
	stale := vote("t1", variantDirect, "прежняя редакция запроса", verdictYes)
	for name, c := range map[string]struct {
		written []writerOut
		judged  []judgeOut
	}{
		"ответ на чужую задачу":               {written: []writerOut{{ID: "x9", Clear: true, Query: "что-то"}}},
		"вердикт по чужой задаче":             {judged: []judgeOut{vote("x9", variantDirect, "что-то", verdictYes)}},
		"вердикт по прежней редакции запроса": {written: testWritten(), judged: []judgeOut{stale}},
		"повторный ответ":                     {written: []writerOut{{ID: "t1", Clear: true, Query: "а"}, {ID: "t1", Clear: true, Query: "б"}}},
		"неизвестный вердикт":                 {written: testWritten(), judged: []judgeOut{vote("t6", variantDirect, "убрать пустые строки", "maybe")}},
	} {
		if _, err := assemble(testTasks(), c.written, c.judged, 1, 1); err == nil {
			t.Errorf("%s: сборка прошла без ошибки", name)
		}
	}
}

// TestTaskAndJudgeID: идентификатор задачи выводится из метода и места
// вызова, идентификатор пары на проверке ещё и из текста запроса.
func TestTaskAndJudgeID(t *testing.T) {
	a := taskID("uid-1", callSite{Module: "М.bsl", Line: 7})
	if a != taskID("uid-1", callSite{Module: "М.bsl", Line: 7}) {
		t.Errorf("taskID неустойчив")
	}
	for _, other := range []string{taskID("uid-2", callSite{Module: "М.bsl", Line: 7}), taskID("uid-1", callSite{Module: "М.bsl", Line: 8})} {
		if other == a {
			t.Errorf("разные задачи получили один идентификатор %s", a)
		}
	}
	if judgeID(a, variantDirect, "запрос") == judgeID(a, variantDirect, "другой запрос") ||
		judgeID(a, variantDirect, "запрос") == judgeID(a, variantParaphrase, "запрос") {
		t.Errorf("judgeID не различает текст запроса или вид")
	}
}

// TestShippedDataset: набор, лежащий в репозитории, цел и не меньше
// обещанного размера.
func TestShippedDataset(t *testing.T) {
	pairs, err := readJSONL[pair]("data/ut_demo.jsonl")
	if err != nil {
		t.Fatalf("набор не читается: %v", err)
	}
	if err := checkDataset(pairs, datasetLimits{Total: 300, BSP: 150, Other: 100}); err != nil {
		t.Errorf("набор data/ut_demo.jsonl: %v", err)
	}
}

// TestSplitKeepsTwinsTogether: близнецы одного метода попадают в одну
// половину, набор методов делится на обе, а зерно действительно меняет
// деление (не сводится к двум вариантам).
func TestSplitKeepsTwinsTogether(t *testing.T) {
	if a, b := splitOf(1, "ОбщегоНазначения.СообщитьПользователю"), splitOf(1, "ОбщегоНазначенияКлиент.СообщитьПользователю"); a != b {
		t.Errorf("близнецы в разных половинах: %s и %s", a, b)
	}
	layout := func(seed int) string {
		var b strings.Builder
		for i := 0; i < 200; i++ {
			b.WriteString(splitOf(seed, fmt.Sprintf("Модуль.Метод%d", i))[:1])
		}
		return b.String()
	}
	first := layout(1)
	if dev := strings.Count(first, "d"); dev < 70 || dev > 130 {
		t.Errorf("половины перекошены: dev %d из 200", dev)
	}
	distinct := map[string]bool{}
	for seed := 1; seed <= 20; seed++ {
		distinct[layout(seed)] = true
	}
	if len(distinct) < 15 {
		t.Errorf("20 зёрен дали %d разных делений", len(distinct))
	}
}

// TestNameOverlap: общие слова ищутся по началу слова и по частям имени
// метода, имя модуля в расчёт не идёт.
func TestNameOverlap(t *testing.T) {
	for _, c := range []struct {
		query, call string
		want        bool
	}{
		{"удаление полей из запроса", "СхемыЗапросов.УдалитьПоляИзЗапроса", true},
		{"получить данные по URL адресу", "Модуль.ПолучитьФайлПоURLАдресу", true},
		{"выборка первых записей", "СхемыЗапросов.УстановитьКоличествоПолучаемыхЗаписей", true},
		{"информация об организации на дату", "ФормированиеПечатныхФорм.СведенияОЮрФизЛице", false},
		{"общего назначения", "ОбщегоНазначения.СкопироватьРекурсивно", false},
		{"сделать копию структуры", "ОбщегоНазначения.СкопироватьРекурсивно", false},
	} {
		if got := nameOverlap(c.query, c.call); got != c.want {
			t.Errorf("nameOverlap(%q, %q) = %v, want %v", c.query, c.call, got, c.want)
		}
	}
}

// TestNamesMethod: запрос называет метод, только когда в нём стоит составной
// идентификатор метода; имя из одного слова это обычное слово запроса.
func TestNamesMethod(t *testing.T) {
	for _, c := range []struct {
		query, call string
		want        bool
	}{
		{"вызвать ЗаполнитьЦены для документа", "Продажи.ЗаполнитьЦены", true},
		{"нужен продажи.заполнитьцены", "Продажи.ЗаполнитьЦены", true},
		{"заполнить цены в документе", "Продажи.ЗаполнитьЦены", false},
		{"сообщить пользователю об ошибке", "Модуль.Сообщить", false},
	} {
		if got := namesMethod(c.query, c.call); got != c.want {
			t.Errorf("namesMethod(%q, %q) = %v, want %v", c.query, c.call, got, c.want)
		}
	}
}

// TestCheckDataset: набор проходит проверку только целым и достаточного
// размера.
func TestCheckDataset(t *testing.T) {
	good := func() []pair {
		return []pair{
			{ID: "t1", Section: sectionBSP, Split: splitDev, Variant: variantDirect, Query: "значение поля", Accept: []string{"М.Метод"}},
			{ID: "t2", Section: sectionBSP, Split: splitTest, Variant: variantParaphrase, Query: "значение поля", Accept: []string{"М.Другой"}},
			{ID: "t3", Section: sectionOther, Split: splitDev, Variant: variantDirect, Query: "цены", Accept: []string{"П.ЗаполнитьЦены"}},
			{ID: "t4", Section: sectionOther, Split: splitTest, Variant: variantDirect, Query: "суммы", Accept: []string{"П.ПересчитатьСуммы"}},
		}
	}
	lim := datasetLimits{Total: 4, BSP: 2, Other: 2}
	if err := checkDataset(good(), lim); err != nil {
		t.Fatalf("целый набор отвергнут: %v", err)
	}
	for name, breakIt := range map[string]func([]pair) []pair{
		"повторный id":   func(p []pair) []pair { p[1].ID = "t1"; return p },
		"чужая секция":   func(p []pair) []pair { p[0].Section = "lib"; return p },
		"чужая половина": func(p []pair) []pair { p[0].Split = "train"; return p },
		"пустой запрос":  func(p []pair) []pair { p[0].Query = " "; return p },
		"нет метода":     func(p []pair) []pair { p[0].Accept = nil; return p },
		"запрос с именем": func(p []pair) []pair {
			p[0].Accept = []string{"М.НужныйМетод"}
			p[0].Query = "вызвать НужныйМетод"
			return p
		},
		"мало пар":            func(p []pair) []pair { return p[:3] },
		"пустая половина bsp": func(p []pair) []pair { p[1].Split = splitDev; return p },
	} {
		err := checkDataset(breakIt(good()), lim)
		if err == nil {
			t.Errorf("%s: набор принят", name)
		}
	}
	if err := checkDataset(good(), datasetLimits{Total: 5}); err == nil || !strings.Contains(err.Error(), "набор мал") {
		t.Errorf("недобор по общему числу: %v", err)
	}
}

// TestWords: идентификатор делится по границам CamelCase, аббревиатура
// остаётся словом.
func TestWords(t *testing.T) {
	got := words("ПолучитьФайлПоURLАдресу и ещё")
	want := []string{"получить", "файл", "по", "url", "адресу", "и", "еще"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("words = %q, want %q", got, want)
	}
}
