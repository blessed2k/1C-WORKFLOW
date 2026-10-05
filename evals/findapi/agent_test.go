package main

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func agentPairs() ([]pair, []draft) {
	mk := func(id, section, split, call string) pair {
		return pair{ID: id, Section: section, Split: split, Accept: []string{call}}
	}
	pairs := []pair{
		mk("b1", "bsp", splitTest, "ОбщегоНазначения.ЗначениеРеквизитаОбъекта"),
		mk("b2", "bsp", splitTest, "ОбщегоНазначенияКлиент.ЗначениеРеквизитаОбъекта"),
		mk("b3", "bsp", splitTest, "Пользователи.ТекущийПользователь"),
		mk("b4", "bsp", splitDev, "СтроковыеФункции.ФорматированнаяСтрока"),
		mk("o1", "other", splitTest, "Продажи.ПроверитьЗаказ"),
		mk("o2", "other", splitTest, "Закупки.ПроверитьЗаказ"),
	}
	var drafts []draft
	for _, p := range pairs {
		drafts = append(drafts, draft{ID: p.ID, Name: "Метод" + p.ID, Comment: "Назначение " + p.ID})
	}
	return pairs, drafts
}

func TestPickAgentTasks(t *testing.T) {
	pairs, drafts := agentPairs()
	tasks, err := pickAgentTasks(pairs, drafts, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	// b1 и b2 одного семейства (близнецы по месту исполнения): берётся один.
	// b4 из настроечной половины не берётся никогда.
	if len(ids) != 4 || ids[2] != "o1" || ids[3] != "o2" || !strings.HasPrefix(ids[0], "b") || ids[1] != "b3" {
		t.Fatalf("задачи %v: ждали одну из b1/b2, затем b3, o1, o2", ids)
	}
	again, _ := pickAgentTasks(pairs, drafts, 2, 1)
	if !reflect.DeepEqual(tasks, again) {
		t.Fatal("повторный выбор с тем же зерном дал другие задачи")
	}
	if _, err := pickAgentTasks(pairs, drafts, 3, 1); err == nil {
		t.Fatal("нехватка задач в секции должна быть ошибкой")
	}
	if _, err := pickAgentTasks(pairs, append(drafts, draft{ID: "нет"}), 2, 1); err == nil {
		t.Fatal("черновик без пары должен быть ошибкой")
	}
}

func TestAgentPromptHidesAnswer(t *testing.T) {
	task := agentTask{ID: "b3", Name: "КтоРаботает", Comment: "Возвращает того, кто сейчас работает в программе.", Accept: []string{"Пользователи.ТекущийПользователь"}}
	prompt := agentPrompt(task, "/выгрузка")
	for _, want := range []string{"КтоРаботает", "Возвращает того, кто сейчас работает", "/выгрузка", agentModule} {
		if !strings.Contains(prompt, want) {
			t.Errorf("в просьбе нет %q", want)
		}
	}
	for _, leak := range []string{"ТекущийПользователь", "find_api", "готов", "БСП", "библиотек"} {
		if strings.Contains(prompt, leak) {
			t.Errorf("просьба подсказывает ответ: %q", leak)
		}
	}
}

func TestAgentEnv(t *testing.T) {
	got := agentEnv([]string{"HOME=/h", "CLAUDE_CODE_SESSION_ID=1", "CLAUDECODE=1", "ANTHROPIC_BASE_URL=x", "AI_AGENT=y", "BAGGAGE=z", "PATH=/bin", "ANTHROPICS=keep"})
	if want := []string{"HOME=/h", "PATH=/bin", "ANTHROPICS=keep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("окружение %v, ждали %v", got, want)
	}
}

func TestCallsMethod(t *testing.T) {
	const call = "ОбщегоНазначения.СообщитьПользователю"
	cases := []struct {
		name string
		code string
		want bool
	}{
		{"прямой вызов", "\tОбщегоНазначения.СообщитьПользователю(Текст);", true},
		{"регистр и пробелы", "общегоназначения . сообщитьпользователю (Текст);", true},
		{"в выражении", "Если Не ОбщегоНазначения.СообщитьПользователю(Т) Тогда", true},
		{"только в комментарии", "// вместо ОбщегоНазначения.СообщитьПользователю(Текст)\nСообщить(Текст);", false},
		{"только в строке", "Т = \"ОбщегоНазначения.СообщитьПользователю(Текст)\";", false},
		{"в продолжении строки", "Т = \"начало\n\t|ОбщегоНазначения.СообщитьПользователю(Текст)\";", false},
		{"другой модуль с тем же началом", "ОбщегоНазначенияКлиент.СообщитьПользователю(Текст);", false},
		{"хвост другого имени", "МоёОбщегоНазначения.СообщитьПользователю(Текст);", false},
		{"метод другого объекта", "Объект.ОбщегоНазначения.СообщитьПользователю(Текст);", false},
		{"имя без вызова", "Ссылка = ОбщегоНазначения.СообщитьПользователю;", false},
		{"после строки с кавычкой в комментарии", "А = 1; // \"\nОбщегоНазначения.СообщитьПользователю(Т);", true},
		{"комментарий внутри литерала", "Т = \"ВЫБРАТЬ\n// к\n|ИЗ Х\"; ОбщегоНазначения.СообщитьПользователю(Т);", true},
		{"пустая строка внутри литерала", "Т = \"ВЫБРАТЬ\n\n|ИЗ ОбщегоНазначения.СообщитьПользователю(Т)\";", false},
	}
	for _, c := range cases {
		if got := callsMethod(stripBSLComments(c.code), c.code, call); got != c.want {
			t.Errorf("%s: %v, ждали %v", c.name, got, c.want)
		}
	}
	manager := "РегистрыСведений.НастройкиОбмена.ТекущиеНастройки"
	if code := "Н = РегистрыСведений.НастройкиОбмена.ТекущиеНастройки();"; !callsMethod(stripBSLComments(code), code, manager) {
		t.Error("вызов метода модуля менеджера не найден")
	}
	optional := "РаботаСФайлами.ДанныеФайла"
	viaModule := []struct {
		name string
		code string
		want bool
	}{
		{"прямо", "Д = ОбщегоНазначения.ОбщийМодуль(\"РаботаСФайлами\").ДанныеФайла(Ф);", true},
		{"через переменную", "Модуль = ОбщегоНазначенияКлиент.ОбщийМодуль(\"РаботаСФайлами\");\nД = Модуль.ДанныеФайла(Ф);", true},
		{"другой модуль", "Модуль = ОбщегоНазначения.ОбщийМодуль(\"РаботаСФайламиСлужебный\");\nД = Модуль.ДанныеФайла(Ф);", false},
		{"в комментарии", "// Д = ОбщегоНазначения.ОбщийМодуль(\"РаботаСФайлами\").ДанныеФайла(Ф);", false},
		{"переменная без вызова метода", "Модуль = ОбщегоНазначения.ОбщийМодуль(\"РаботаСФайлами\");\nД = Модуль.Другое(Ф);", false},
	}
	for _, c := range viaModule {
		if got := callsMethod(stripBSLComments(c.code), c.code, optional); got != c.want {
			t.Errorf("через ОбщийМодуль, %s: %v, ждали %v", c.name, got, c.want)
		}
	}
}

func TestAgentVerdict(t *testing.T) {
	accept := []string{"Пользователи.ТекущийПользователь", "ПользователиКлиент.ТекущийПользователь"}
	cases := []struct {
		name   string
		module string
		want   string
	}{
		{"пустой модуль", agentSkeleton, agentEmpty},
		{"готовый метод", "Функция Кто() Экспорт\n\tВозврат Пользователи.ТекущийПользователь();\nКонецФункции", agentAccepted},
		{"близнец", "Функция Кто() Экспорт\n\tВозврат ПользователиКлиент.ТекущийПользователь();\nКонецФункции", agentAccepted},
		{"свой код", "Функция Кто() Экспорт\n\tВозврат ПараметрыСеанса.ТекущийПользователь;\nКонецФункции", agentOther},
		{"готовый только в комментарии", "// см. Пользователи.ТекущийПользователь()\nФункция Кто() Экспорт\n\tВозврат Неопределено;\nКонецФункции", agentOther},
	}
	for _, c := range cases {
		if got := agentVerdict(c.module, accept); got != c.want {
			t.Errorf("%s: %q, ждали %q", c.name, got, c.want)
		}
	}
}

func TestQualifiedCalls(t *testing.T) {
	code := stripBSLComments("Запрос . Выполнить();\nА = ОбщегоНазначения.ЗначениеРеквизитаОбъекта(С, \"Имя\");\nзапрос.выполнить(); // Б.В()\nГ();")
	if got, want := qualifiedCalls(code), []string{"Запрос.Выполнить", "ОбщегоНазначения.ЗначениеРеквизитаОбъекта"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("вызовы %v, ждали %v", got, want)
	}
}

func TestReadAgentTranscript(t *testing.T) {
	log := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"ищу"},{"type":"tool_use","name":"mcp__1c-workflowtimur__find_api","input":{}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__1c-workflowtimur__find_api"},{"type":"tool_use","name":"Write"}]}}`,
		`оборванная строка {`,
		`{"type":"result","is_error":false,"num_turns":7,"total_cost_usd":0.5}`,
	}, "\n")
	got := readAgentTranscript(strings.NewReader(log))
	if got.Tools["find_api"] != 2 || got.Tools["Write"] != 1 || got.Turns != 7 || !got.Finished || got.Failed {
		t.Fatalf("журнал разобран неверно: %+v", got)
	}
	cut := readAgentTranscript(strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`))
	if cut.Finished || cut.Tools["Read"] != 1 {
		t.Fatalf("оборванный журнал: %+v", cut)
	}
	failed := readAgentTranscript(strings.NewReader(`{"type":"result","is_error":true,"num_turns":1}`))
	if !failed.Failed || !failed.Finished {
		t.Fatalf("ошибочный запуск: %+v", failed)
	}
}

func TestAgentTurnsAndArgs(t *testing.T) {
	task := agentTask{ID: "b3", Name: "КтоРаботает", Comment: "Кто работает."}
	one := agentArm{Model: "м", MCPConfig: "с.json", Dump: "/выгрузка"}
	if turns := agentTurns(task, one); len(turns) != 1 || turns[0] != agentPrompt(task, "/выгрузка") {
		t.Fatalf("один ход: %q", turns)
	}
	two := one
	two.Prefix, two.Followup, two.PluginDir, two.Settings = "/плагин:план ", "Утверждаю.", "/плагин", "{}"
	turns := agentTurns(task, two)
	if len(turns) != 2 || !strings.HasPrefix(turns[0], "/плагин:план Конфигурация") || turns[1] != "Утверждаю." {
		t.Fatalf("два хода: %q", turns)
	}
	first := strings.Join(agentArgs(turns[0], 0, "сессия", two), " ")
	second := strings.Join(agentArgs(turns[1], 1, "сессия", two), " ")
	if !strings.Contains(first, "--session-id сессия") || strings.Contains(first, "--resume") {
		t.Errorf("первый ход открывает сессию: %s", first)
	}
	if !strings.Contains(second, "--resume сессия") || strings.Contains(second, "--session-id") {
		t.Errorf("второй ход продолжает сессию: %s", second)
	}
	for _, want := range []string{"--plugin-dir /плагин", "--settings {}", "--strict-mcp-config --mcp-config с.json", "mcp__1c-workflowtimur"} {
		if !strings.Contains(second, want) {
			t.Errorf("во втором ходе нет %q: %s", want, second)
		}
	}
	a, err := newSessionID()
	b, _ := newSessionID()
	if err != nil || len(a) != 36 || a == b || a[14] != '4' {
		t.Fatalf("идентификатор сессии: %q, %q, %v", a, b, err)
	}
}

func TestPlanNamed(t *testing.T) {
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__1c-workflowtimur__find_api"}]}}`,
		`{"type":"result","is_error":false,"num_turns":4,"result":"| получить | готовое | Пользователи . ТекущийПользователь() |"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write"}]}}`,
		`{"type":"result","is_error":false,"num_turns":3,"result":"Готово."}`,
	}, "\n")
	got := readAgentTranscript(strings.NewReader(log))
	if len(got.Answers) != 2 || got.Turns != 7 || got.Failed || !got.Finished {
		t.Fatalf("два хода разобраны неверно: %+v", got)
	}
	if !namesMethodIn(got.Answers[0], "Пользователи.ТекущийПользователь") || namesMethodIn(got.Answers[1], "Пользователи.ТекущийПользователь") {
		t.Fatal("план назван неверно")
	}
	broken := readAgentTranscript(strings.NewReader(`{"type":"result","is_error":true,"num_turns":1}` + "\n" + `{"type":"result","is_error":false,"num_turns":1}`))
	if !broken.Failed {
		t.Fatal("сбой первого хода должен делать запуск сбойным")
	}
}

func TestSignTest(t *testing.T) {
	cases := []struct {
		plus, minus int
		want        float64
	}{
		{0, 0, 1},
		{3, 3, 1},
		{5, 0, 0.0625},
		{0, 5, 0.0625},
		{9, 1, 0.021484375},
	}
	for _, c := range cases {
		if got := signTest(c.plus, c.minus); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("signTest(%d, %d) = %v, ждали %v", c.plus, c.minus, got, c.want)
		}
	}
}

func TestPrintAgentReport(t *testing.T) {
	tasks := []agentTask{
		{ID: "b1", Section: "bsp", Name: "А", Accept: []string{"М.А"}},
		{ID: "b2", Section: "bsp", Name: "Б", Accept: []string{"М.Б"}},
		{ID: "o1", Section: "other", Name: "В", Accept: []string{"М.В"}},
	}
	byArm := map[string]map[string]agentScore{
		"before": {
			"b1": {Verdict: agentOther, Calls: []string{"Запрос.Выполнить"}},
			"b2": {Verdict: agentAccepted},
			"o1": {Verdict: agentFailed},
		},
		"after": {
			"b1": {Verdict: agentAccepted, Tools: map[string]int{"find_api": 2}},
			"b2": {Verdict: agentAccepted},
			"o1": {Verdict: agentOther},
		},
	}
	var b strings.Builder
	printAgentReport(&b, tasks, []string{"before", "after"}, byArm)
	out := b.String()
	for _, want := range []string{
		"плечо before: готовый метод вызван в 1 из 2 завершённых (bsp 1/2, other 0/0); другое 1, пусто 0, сбой 1",
		"плечо after: готовый метод вызван в 2 из 3 завершённых (bsp 2/2, other 0/1); другое 1",
		"звал find_api в 1",
		"в обоих 1, только во втором 1, только в первом 0, ни в одном 0, без пары 1",
		"before/b1 А (ждали М.А): Запрос.Выполнить",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, out)
		}
	}
}
