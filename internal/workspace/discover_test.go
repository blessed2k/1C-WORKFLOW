package workspace_test

import (
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestDiscoverПропускаетСгенерированноеИСлужебное — критерий приёмки: .mcp1c
// исключён из обхода. Проверяется не предикатом, а результатом: внутри .mcp1c
// лежит настоящая выгрузка, и если бы обход туда спускался, она попала бы в
// кандидаты.
func TestDiscoverПропускаетСгенерированноеИСлужебное(t *testing.T) {
	root := t.TempDir()

	записать(t, filepath.Join(root, "выгрузки", "ut", "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))
	записать(t, filepath.Join(root, ".mcp1c", "снимок", "Configuration.xml"), конфигурацияXML("Снимок", ""))
	записать(t, filepath.Join(root, ".git", "копия", "Configuration.xml"), конфигурацияXML("Копия", ""))
	записать(t, filepath.Join(root, "build", "выгрузка", "Configuration.xml"), конфигурацияXML("Сборка", ""))
	записать(t, filepath.Join(root, "temp", "выгрузка", "Configuration.xml"), конфигурацияXML("Временное", ""))

	кандидаты, err := workspace.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(кандидаты) != 1 {
		t.Fatalf("кандидатов %d, ожидался один: %+v", len(кандидаты), кандидаты)
	}
	if кандидаты[0].Name != "УправлениеТорговлей" {
		t.Errorf("найдена выгрузка %q, ожидалась УправлениеТорговлей", кандидаты[0].Name)
	}
	if кандидаты[0].Kind != domain.KindConfiguration {
		t.Errorf("kind = %q, ожидалось %q", кандидаты[0].Kind, domain.KindConfiguration)
	}
}

// TestDiscoverНеЧитаетНеиндексируемое — список неиндексируемого по умолчанию,
// проверенный результатом обхода: справка агента и собранные бинарники 1С
// кандидатами не становятся и вообще не открываются, а рядом лежащие исходники
// находятся как обычно.
func TestDiscoverНеЧитаетНеиндексируемое(t *testing.T) {
	root := t.TempDir()

	// Каталог, где нет ничего, кроме неиндексируемого: кандидатом он быть не может.
	записать(t, filepath.Join(root, "поставка", "CLAUDE.md"), "инструкции агенту")
	записать(t, filepath.Join(root, "поставка", "AGENTS.md"), "инструкции агенту")
	// 1Cv8.1CD здесь намеренно нет: каталог базы отсекается отдельным правилом,
	// а тут проверяются именно файловые расширения.
	для := []string{"Обработка.epf", "Отчёт.erf", "Конфигурация.cf", "Расширение.cfe", "База.dt", "Данные.1CD"}
	for _, имя := range для {
		записать(t, filepath.Join(root, "поставка", имя), "двоичный файл, читать нечем")
	}

	// Рядом — те же артефакты вперемешку с настоящими исходниками обработки.
	записать(t, filepath.Join(root, "обработка", "src", "ЗагрузкаЦен.xml"), внешнийОбъектXML("ExternalDataProcessor", "ЗагрузкаЦен"))
	записать(t, filepath.Join(root, "обработка", "src", "ЗагрузкаЦен.epf"), "собранный двоичный файл")
	записать(t, filepath.Join(root, "обработка", "src", "CLAUDE.md"), "инструкции агенту")

	кандидаты, err := workspace.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(кандидаты) != 1 {
		t.Fatalf("кандидатов %d, ожидался один (исходники обработки): %+v", len(кандидаты), кандидаты)
	}
	if кандидаты[0].Kind != domain.KindExternalDataProcessor || кандидаты[0].Name != "ЗагрузкаЦен" {
		t.Errorf("кандидат = %+v, ожидались исходники обработки ЗагрузкаЦен", кандидаты[0])
	}

	// Само правило: что именно исключено из обхода и индексации.
	исключено := []string{
		"CLAUDE.md", "AGENTS.md", "claude.md",
		"Обработка.epf", "Отчёт.erf", "Конфигурация.cf", "Расширение.cfe", "База.dt", "Данные.1CD", "1Cv8.1CD",
		"src/CLAUDE.md", "src/Обработка.EPF",
		".mcp1c/registry.json", ".git/config", "build/выгрузка/Configuration.xml",
	}
	for _, rel := range исключено {
		if !workspace.IsIgnored(rel) {
			t.Errorf("IsIgnored(%q) = false, путь обязан быть исключён", rel)
		}
	}
	индексируется := []string{
		"Configuration.xml",
		"CommonModules/ОбщегоНазначения/Ext/Module.bsl",
		"DataProcessors/ЗагрузкаЦен/Forms/Форма/Ext/Form.xml",
		"tests/yaxunit/ТестыОбмена.bsl",
	}
	for _, rel := range индексируется {
		if workspace.IsIgnored(rel) {
			t.Errorf("IsIgnored(%q) = true, а это обычный исходник", rel)
		}
	}
}

// TestDiscoverНеСпускаетсяВИнформационнуюБазу: сервер
// не трогает информационную базу. Внутри каталога базы лежит выгрузка, и если
// бы обход туда спустился, она стала бы кандидатом.
func TestDiscoverНеСпускаетсяВИнформационнуюБазу(t *testing.T) {
	root := t.TempDir()

	записать(t, filepath.Join(root, "база", "1Cv8.1CD"), "файловая ИБ")
	записать(t, filepath.Join(root, "база", "Configuration.xml"), конфигурацияXML("ИзБазы", ""))
	записать(t, filepath.Join(root, "база", "выгрузка", "Configuration.xml"), конфигурацияXML("ИзБазыГлубже", ""))
	записать(t, filepath.Join(root, "выгрузка", "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))

	кандидаты, err := workspace.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(кандидаты) != 1 {
		t.Fatalf("кандидатов %d, ожидался один — выгрузка вне базы: %+v", len(кандидаты), кандидаты)
	}
	if кандидаты[0].Name != "УправлениеТорговлей" {
		t.Errorf("найдено %q: похоже, обход прочитал файлы информационной базы", кандидаты[0].Name)
	}

	if !workspace.IsInfobaseDir(filepath.Join(root, "база")) {
		t.Error("каталог с 1Cv8.1CD не опознан как информационная база")
	}
	if workspace.IsInfobaseDir(filepath.Join(root, "выгрузка")) {
		t.Error("каталог выгрузки принят за информационную базу")
	}
}

// TestDiscoverПредлагаетУникальныеId — типовая раскладка §9: несколько
// обработок лежат в каталогах tools/<Имя>/src, и имя каталога у всех одно.
// Совпавший id сделал бы манифест невалидным сразу после вставки.
func TestDiscoverПредлагаетУникальныеId(t *testing.T) {
	root := t.TempDir()

	записать(t, filepath.Join(root, "tools", "ЗагрузкаЦен", "src", "ЗагрузкаЦен.xml"), внешнийОбъектXML("ExternalDataProcessor", "ЗагрузкаЦен"))
	записать(t, filepath.Join(root, "tools", "ВыгрузкаОстатков", "src", "ВыгрузкаОстатков.xml"), внешнийОбъектXML("ExternalDataProcessor", "ВыгрузкаОстатков"))
	записать(t, filepath.Join(root, "tools", "ОтчётПоПродажам", "src", "ОтчётПоПродажам.xml"), внешнийОбъектXML("ExternalReport", "ОтчётПоПродажам"))

	кандидаты, err := workspace.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(кандидаты) != 3 {
		t.Fatalf("кандидатов %d, ожидалось 3: %+v", len(кандидаты), кандидаты)
	}

	видели := make(map[domain.ComponentID]string, len(кандидаты))
	for _, c := range кандидаты {
		if err := c.SuggestedID.Validate(); err != nil {
			t.Errorf("предложенный id %q непригоден: %v", c.SuggestedID, err)
		}
		if prev, ok := видели[c.SuggestedID]; ok {
			t.Errorf("id %q предложен дважды: %q и %q", c.SuggestedID, prev, c.Rel)
		}
		видели[c.SuggestedID] = c.Rel
	}

	// Предложение стабильно между запусками: id считается от пути, а не от
	// порядка обхода.
	повтор, err := workspace.Discover(root)
	if err != nil {
		t.Fatalf("повторный Discover: %v", err)
	}
	for i, c := range повтор {
		if c.SuggestedID != кандидаты[i].SuggestedID {
			t.Errorf("для %q id сменился с %q на %q", c.Rel, кандидаты[i].SuggestedID, c.SuggestedID)
		}
	}
}

// TestDiscoverРазличаетВидыКандидатов проверяет, что автопоиск отвечает не
// просто «здесь что-то есть», а называет вид: расширение отличается от
// конфигурации по ConfigurationExtensionPurpose, обработка и отчёт — по
// корневому XML.
func TestDiscoverРазличаетВидыКандидатов(t *testing.T) {
	root := t.TempDir()

	записать(t, filepath.Join(root, "cfg", "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))
	записать(t, filepath.Join(root, "ext", "Configuration.xml"), конфигурацияXML("Доработки", "Customization"))
	записать(t, filepath.Join(root, "epf", "src", "ЗагрузкаЦен.xml"), внешнийОбъектXML("ExternalDataProcessor", "ЗагрузкаЦен"))
	записать(t, filepath.Join(root, "erf", "src", "ОтчётПоПродажам.xml"), внешнийОбъектXML("ExternalReport", "ОтчётПоПродажам"))

	кандидаты, err := workspace.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	виды := make(map[domain.ComponentKind]string, len(кандидаты))
	for _, c := range кандидаты {
		if prev, ok := виды[c.Kind]; ok {
			t.Errorf("вид %q найден дважды: %q и %q", c.Kind, prev, c.Name)
		}
		виды[c.Kind] = c.Name
		if c.SuggestedID == "" {
			t.Errorf("кандидату %q не предложен id", c.Name)
		}
		if err := c.SuggestedID.Validate(); err != nil {
			t.Errorf("предложенный id %q непригоден: %v", c.SuggestedID, err)
		}
	}

	ожидание := map[domain.ComponentKind]string{
		domain.KindConfiguration:         "УправлениеТорговлей",
		domain.KindExtension:             "Доработки",
		domain.KindExternalDataProcessor: "ЗагрузкаЦен",
		domain.KindExternalReport:        "ОтчётПоПродажам",
	}
	for kind, имя := range ожидание {
		if виды[kind] != имя {
			t.Errorf("для вида %q найдено %q, ожидалось %q", kind, виды[kind], имя)
		}
	}
}
