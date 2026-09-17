package source

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Живой контур коннектора: гоняет все методы LiveSource против настоящей базы
// через расширение-коннектор. Мок проверяет разбор ответа, этот тест — что база
// такой ответ вообще отдаёт: имена эндпоинтов, имена ключей, реальные типы данных.
//
// Запуск (база должна быть опубликована, расширение загружено):
//
//	# учётные данные базы — в переменных окружения MCP_1C_USER/MCP_1C_PASSWORD
//	MCP_1C_BASE_URL=http://localhost:8314/ut/hs/mcp-1c go test ./internal/source/ -run TestLive -v
//
// Без MCP_1C_BASE_URL тест пропускается, поэтому обычный go test ./... живой базы
// не требует.
func liveSource(t *testing.T) (*HTTPSource, context.Context, func()) {
	t.Helper()
	base := os.Getenv("MCP_1C_BASE_URL")
	if base == "" {
		t.Skip("MCP_1C_BASE_URL не задан: живой базы нет")
	}
	s := NewHTTPSource(base, os.Getenv("MCP_1C_USER"), os.Getenv("MCP_1C_PASSWORD"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	return s, ctx, cancel
}

func TestLiveConfigurationInfo(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	info, err := s.ConfigurationInfo(ctx)
	if err != nil {
		t.Fatalf("ConfigurationInfo: %v", err)
	}
	if info.Name == "" || info.Version == "" {
		t.Errorf("пустые имя или версия конфигурации: %+v", info)
	}
	if info.PlatformVersion == "" {
		t.Errorf("нет версии платформы: %+v", info)
	}
	if info.Mode != "file" && info.Mode != "server" {
		t.Errorf("режим базы = %q, ожидался file или server", info.Mode)
	}
	t.Logf("конфигурация %s %s, платформа %s, режим %s", info.Name, info.Version, info.PlatformVersion, info.Mode)
}

func TestLiveMetadataTree(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	tree, err := s.MetadataTree(ctx)
	if err != nil {
		t.Fatalf("MetadataTree: %v", err)
	}
	if tree.TotalObjects == 0 {
		t.Fatal("дерево метаданных пустое")
	}
	byType := map[string]int{}
	for _, g := range tree.Groups {
		byType[g.Type] = len(g.Objects)
		// Нераспознанный ключ коллекции остаётся русским: check_sync такой тип
		// молча не сравнивает, поэтому ловим это здесь.
		if strings.ContainsAny(g.Type, "абвгдеёжзийклмнопрстуфхцчшщъыьэюяАБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ") {
			t.Errorf("коллекция %q не преобразовалась в английский тип", g.Type)
		}
	}
	for _, want := range []string{
		"Catalog", "Document", "InformationRegister", "Role", "Subsystem", "CommonModule",
		"EventSubscription", "FunctionalOption", "DefinedType", "XDTOPackage", "CommonAttribute",
	} {
		if byType[want] == 0 {
			t.Errorf("в дереве нет объектов типа %s", want)
		}
	}
	// Живое дерево должно покрывать те же типы, что и выгрузка, иначе check_sync их не сверит.
	if len(tree.Groups) < 30 {
		t.Errorf("живой /metadata отдал всего %d типов", len(tree.Groups))
	}
	t.Logf("всего объектов %d, типов %d", tree.TotalObjects, len(tree.Groups))
}

func TestLiveObjectStructure(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	doc, err := s.ObjectStructure(ctx, "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("ObjectStructure документа: %v", err)
	}
	if len(doc.Attributes) == 0 || len(doc.TabularSections) == 0 {
		t.Errorf("у документа нет реквизитов или табличных частей: %d/%d",
			len(doc.Attributes), len(doc.TabularSections))
	}
	if len(doc.TabularSections) > 0 && len(doc.TabularSections[0].Attributes) == 0 {
		t.Errorf("табличная часть %s без реквизитов", doc.TabularSections[0].Name)
	}
	if len(doc.Forms) == 0 {
		t.Errorf("у документа нет форм")
	}

	reg, err := s.ObjectStructure(ctx, "InformationRegister", "ЦеныНоменклатуры")
	if err != nil {
		t.Fatalf("ObjectStructure регистра: %v", err)
	}
	var dims, res int
	for _, a := range reg.Attributes {
		switch a.Kind {
		case "Dimension":
			dims++
		case "Resource":
			res++
		}
	}
	if dims == 0 || res == 0 {
		t.Errorf("у регистра сведений не пришли измерения (%d) или ресурсы (%d)", dims, res)
	}

	enum, err := s.ObjectStructure(ctx, "Enum", "ХозяйственныеОперации")
	if err != nil {
		t.Fatalf("ObjectStructure перечисления: %v", err)
	}
	if len(enum.Other["values"]) == 0 {
		t.Errorf("у перечисления не пришли значения")
	}

	if _, err := s.ObjectStructure(ctx, "Catalog", "ТакогоНетВКонфигурации"); err == nil {
		t.Error("несуществующий объект вернулся без ошибки")
	}
}

func TestLiveExecuteQuery(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	res, err := s.ExecuteQuery(ctx, QueryParams{
		Text:  "ВЫБРАТЬ ПЕРВЫЕ 3 Ссылка, Наименование ИЗ Справочник.Номенклатура",
		Limit: 3,
	})
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if len(res.Columns) != 2 || len(res.Rows) == 0 {
		t.Fatalf("колонки %v, строк %d", res.Columns, len(res.Rows))
	}
	if _, ok := res.Rows[0]["Наименование"]; !ok {
		t.Errorf("строка не разложилась по колонкам: %+v", res.Rows[0])
	}
	if res.Count != len(res.Rows) {
		t.Errorf("count = %d при %d строках", res.Count, len(res.Rows))
	}

	// Параметры запроса должны доезжать до базы под ключом parameters.
	withParam, err := s.ExecuteQuery(ctx, QueryParams{
		Text:   "ВЫБРАТЬ ПЕРВЫЕ 1 Ссылка ИЗ Справочник.Номенклатура ГДЕ ПометкаУдаления = &Пометка",
		Params: map[string]any{"Пометка": false},
		Limit:  1,
	})
	if err != nil {
		t.Fatalf("ExecuteQuery с параметром: %v", err)
	}
	if len(withParam.Columns) != 1 {
		t.Errorf("колонки запроса с параметром = %v", withParam.Columns)
	}

	// Потолок коннектора: больше 1000 строк он не отдаёт и честно ставит truncated.
	// Таблица движений заведомо длиннее потолка, иначе проверка была бы пустой.
	const bigTable = "РегистрНакопления.ТоварыНаСкладах"
	count, err := s.ExecuteQuery(ctx, QueryParams{Text: "ВЫБРАТЬ КОЛИЧЕСТВО(*) КАК Всего ИЗ " + bigTable})
	if err != nil {
		t.Fatalf("ExecuteQuery счётчика: %v", err)
	}
	total, _ := count.Rows[0]["Всего"].(float64)
	if total <= 1000 {
		t.Fatalf("в %s всего %.0f записей, обрезку на такой таблице не проверить", bigTable, total)
	}

	big, err := s.ExecuteQuery(ctx, QueryParams{
		Text:  "ВЫБРАТЬ Регистратор ИЗ " + bigTable,
		Limit: 5000,
	})
	if err != nil {
		t.Fatalf("ExecuteQuery без лимита: %v", err)
	}
	if len(big.Rows) != 1000 || !big.Truncated {
		t.Errorf("в таблице %.0f записей, пришло %d строк, truncated=%v", total, len(big.Rows), big.Truncated)
	}

	// Лимит клиента ниже потолка тоже должен обрезать и честно сообщать об этом.
	small, err := s.ExecuteQuery(ctx, QueryParams{Text: "ВЫБРАТЬ Регистратор ИЗ " + bigTable, Limit: 10})
	if err != nil {
		t.Fatalf("ExecuteQuery с лимитом 10: %v", err)
	}
	if len(small.Rows) != 10 || !small.Truncated {
		t.Errorf("при лимите 10 пришло %d строк, truncated=%v", len(small.Rows), small.Truncated)
	}

	if _, err := s.ExecuteQuery(ctx, QueryParams{Text: "УНИЧТОЖИТЬ ВСЁ"}); err == nil {
		t.Error("не-SELECT прошёл без ошибки")
	}
}

func TestLiveValidateQuery(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	ok, err := s.ValidateQuery(ctx, "ВЫБРАТЬ Ссылка ИЗ Справочник.Номенклатура ГДЕ Наименование = &Имя")
	if err != nil {
		t.Fatalf("ValidateQuery корректного запроса: %v", err)
	}
	if !ok.Valid {
		t.Errorf("корректный запрос признан невалидным: %s", ok.Error)
	}

	bad, err := s.ValidateQuery(ctx, "ВЫБРАТЬ ИЗ ГДЕ")
	if err != nil {
		t.Fatalf("ValidateQuery битого запроса: %v", err)
	}
	if bad.Valid || bad.Error == "" {
		t.Errorf("битый запрос признан валидным: %+v", bad)
	}
}

func TestLiveEventLog(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	res, err := s.EventLog(ctx, EventLogParams{Limit: 5})
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	if len(res.Entries) == 0 {
		t.Fatal("журнал регистрации пуст, проверить нечего")
	}
	if len(res.Entries) > 5 {
		t.Errorf("запрошено 5 записей, пришло %d", len(res.Entries))
	}
	first := res.Entries[0]
	if first.Date == "" || first.Level == "" {
		t.Errorf("запись журнала без даты или уровня: %+v", first)
	}

	// Потолок 500 применяется независимо от того, сколько попросил клиент.
	big, err := s.EventLog(ctx, EventLogParams{Limit: 5000})
	if err != nil {
		t.Fatalf("EventLog с большим лимитом: %v", err)
	}
	if len(big.Entries) > 500 {
		t.Errorf("коннектор отдал %d записей, потолок 500", len(big.Entries))
	}
	if len(big.Entries) < 500 {
		t.Fatalf("в журнале всего %d записей, потолок на такой базе не проверить", len(big.Entries))
	}
	if !big.Truncated {
		t.Error("список обрезан по потолку, но truncated не выставлен")
	}

	// Отбор по уровню: клиент передаёт английское имя, коннектор понимает русское.
	byLevel, err := s.EventLog(ctx, EventLogParams{Level: "Error", Limit: 20})
	if err != nil {
		t.Fatalf("EventLog по уровню: %v", err)
	}
	for _, e := range byLevel.Entries {
		if e.Level != "Ошибка" && e.Level != "Error" {
			t.Errorf("при отборе level=Error пришла запись уровня %q", e.Level)
			break
		}
	}

	_, err = s.EventLog(ctx, EventLogParams{User: "ТакогоПользователяНет", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("отбор по несуществующему пользователю: ожидался HTTP 400, получено %v", err)
	}

	// Нераспознанный уровень тоже отвергается, а не молча снимает отбор.
	_, err = s.EventLog(ctx, EventLogParams{Level: "ТакогоУровняНет", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("неизвестный уровень: ожидался HTTP 400, получено %v", err)
	}

	// Битая дата раньше молча игнорировалась и расширяла выборку. Обе границы идут через
	// одну проверку, но покрываем каждую: молчаливое расширение выборки не должно вернуться.
	_, err = s.EventLog(ctx, EventLogParams{StartDate: "не дата", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("битая start_date: ожидался HTTP 400, получено %v", err)
	}
	_, err = s.EventLog(ctx, EventLogParams{EndDate: "тоже не дата", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("битая end_date: ожидался HTTP 400, получено %v", err)
	}
}

func TestLiveSubsystem(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	tree, err := s.MetadataTree(ctx)
	if err != nil {
		t.Fatalf("MetadataTree: %v", err)
	}
	var name string
	for _, g := range tree.Groups {
		if g.Type == "Subsystem" && len(g.Objects) > 0 {
			name = g.Objects[0]
			break
		}
	}
	if name == "" {
		t.Skip("в конфигурации нет подсистем")
	}

	sub, err := s.Subsystem(ctx, name)
	if err != nil {
		t.Fatalf("Subsystem %s: %v", name, err)
	}
	if sub.Name != name {
		t.Errorf("имя подсистемы = %q, ожидалось %q", sub.Name, name)
	}
	if len(sub.Content) == 0 && len(sub.Subsystems) == 0 {
		t.Errorf("подсистема %s пуста и по составу, и по дочерним", name)
	}

	if _, err := s.Subsystem(ctx, "ТакойПодсистемыНет"); err == nil {
		t.Error("несуществующая подсистема вернулась без ошибки")
	}
}

func TestLivePredefined(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	// ВидыКонтактнойИнформации — справочник БСП, предопределённые в нём есть всегда,
	// поэтому проверка непустая: на справочнике без предопределённых тест доказывал бы
	// только то, что эндпоинт не падает.
	list, err := s.Predefined(ctx, "Catalog", "ВидыКонтактнойИнформации")
	if err != nil {
		t.Fatalf("Predefined: %v", err)
	}
	if list.Count == 0 {
		t.Fatal("предопределённые не пришли")
	}
	if list.Count != len(list.Items) {
		t.Errorf("count = %d при %d элементах", list.Count, len(list.Items))
	}
	for _, it := range list.Items {
		if it.Name == "" || it.Ref == "" {
			t.Errorf("предопределённый элемент без имени или представления: %+v", it)
		}
	}
	if list.Truncated {
		t.Errorf("список из %d элементов помечен обрезанным при потолке 1000", list.Count)
	}
	// Обрезку в другую сторону на этой базе проверить нечем: справочника с более чем
	// 1000 предопределённых в УТ нет, потолок закрыт тестом /query и /eventlog.
	t.Logf("предопределённых элементов: %d, первый %s (%s)", list.Count, list.Items[0].Name, list.Items[0].Ref)

	if _, err := s.Predefined(ctx, "CommonModule", "ОбщегоНазначения"); err == nil {
		t.Error("объект без предопределённых данных вернулся без ошибки")
	}
}

// AnalyzeQuery считается на стороне клиента, но в живом контуре важно, что он
// работает при live-источнике и не ходит в базу.
func TestLiveAnalyzeQuery(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	res, err := s.AnalyzeQuery(ctx, "ВЫБРАТЬ * ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки")
	if err != nil {
		t.Fatalf("AnalyzeQuery: %v", err)
	}
	codes := map[string]bool{}
	for _, w := range res.Warnings {
		codes[w.Code] = true
	}
	// Источник записан без "КАК псевдоним": разбор источников обязан его увидеть,
	// иначе предупреждение о виртуальной таблице без параметров потеряется.
	if !codes["VirtualTableNoParams"] || !codes["SelectStar"] {
		t.Errorf("анализатор не нашёл ожидаемых анти-паттернов: %+v", res.Warnings)
	}
}

// FormStructure и SearchCode в live не поддержаны by design: проверяем, что
// клиент отдаёт понятную ошибку, а не молчаливую пустоту.
func TestLiveUnsupportedOffline(t *testing.T) {
	s, ctx, cancel := liveSource(t)
	defer cancel()

	if _, err := s.FormStructure(ctx, "Catalog", "Номенклатура", "ФормаЭлемента"); err == nil {
		t.Error("FormStructure в live не должен возвращаться без ошибки")
	}
	if _, err := s.SearchCode(ctx, SearchParams{Query: "Процедура"}); err == nil {
		t.Error("SearchCode в live не должен возвращаться без ошибки")
	}
}
