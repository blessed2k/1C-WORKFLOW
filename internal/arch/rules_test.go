package arch_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/arch"
)

// Гард, который никогда не падал, ничего не доказывает: на зелёном репозитории
// пустой список нарушений одинаково выдаёт и рабочую проверку, и заглушку.
// Поэтому рядом лежит намеренно сломанная фикстура testdata/broken — модуль, где
// нарушен каждый из запретов, и ровно те же файлы содержат разрешённые случаи
// (SQL внутри store, internal/syntax в новом коде, платформенные файлы).

// нарушениеФикстуры — то, что тест сверяет: запрет, файл и импорт.
type нарушениеФикстуры struct {
	правило string
	файл    string
	импорт  string
}

func загрузитьФикстуру(t *testing.T, имя string) *arch.Module {
	t.Helper()
	модуль, err := arch.Load(filepath.Join("testdata", имя))
	if err != nil {
		t.Fatalf("разбор фикстуры %s: %v", имя, err)
	}
	return модуль
}

// собрать приводит нарушения к сравнимому виду: номер строки в файле — деталь,
// от которой тест не должен зависеть.
func собрать(нарушения []arch.Violation) []нарушениеФикстуры {
	собранные := make([]нарушениеФикстуры, 0, len(нарушения))
	for _, нарушение := range нарушения {
		файл, _, _ := strings.Cut(нарушение.File, ":")
		собранные = append(собранные, нарушениеФикстуры{
			правило: нарушение.Rule,
			файл:    файл,
			импорт:  нарушение.Import,
		})
	}
	sort.Slice(собранные, func(i, j int) bool {
		if собранные[i].файл != собранные[j].файл {
			return собранные[i].файл < собранные[j].файл
		}
		return собранные[i].импорт < собранные[j].импорт
	})
	return собранные
}

func сверить(t *testing.T, получено []нарушениеФикстуры, ожидалось []нарушениеФикстуры) {
	t.Helper()
	if len(получено) != len(ожидалось) {
		t.Fatalf("нарушений %d, ожидалось %d:\n получено: %+v\n ожидалось: %+v",
			len(получено), len(ожидалось), получено, ожидалось)
	}
	for i := range ожидалось {
		if получено[i] != ожидалось[i] {
			t.Errorf("нарушение %d: получено %+v, ожидалось %+v", i, получено[i], ожидалось[i])
		}
	}
}

// TestГардЛовитDomain: domain, потянувший MCP SDK и хранилище, обязан упасть, а
// соседний честный файл того же пакета — нет.
func TestГардЛовитDomain(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	сверить(t, собрать(arch.CheckDomainImports(модуль)), []нарушениеФикстуры{
		{arch.RuleDomainStdlibOnly, "internal/domain/bad.go", "broken/internal/store"},
		{arch.RuleDomainStdlibOnly, "internal/domain/bad.go", "github.com/modelcontextprotocol/go-sdk/mcp"},
	})
}

// TestГардЛовитSQL: database/sql в app — нарушение, тот же импорт в store — нет.
func TestГардЛовитSQL(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	сверить(t, собрать(arch.CheckSQLIsolation(модуль)), []нарушениеФикстуры{
		{arch.RuleSQLOnlyInStore, "internal/app/query.go", "database/sql"},
	})
}

// TestГардЛовитСлоиCmd: транспорт тянет store, parse, resolve, index и
// effective напрямую; импорт app в том же файле законен и в нарушения попасть
// не должен.
func TestГардЛовитСлоиCmd(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	сверить(t, собрать(arch.CheckCommandLayering(модуль)), []нарушениеФикстуры{
		{arch.RuleCmdThroughApp, "cmd/mcp1c/direct.go", "broken/internal/effective"},
		{arch.RuleCmdThroughApp, "cmd/mcp1c/direct.go", "broken/internal/index"},
		{arch.RuleCmdThroughApp, "cmd/mcp1c/direct.go", "broken/internal/parse/bsl"},
		{arch.RuleCmdThroughApp, "cmd/mcp1c/direct.go", "broken/internal/resolve"},
		{arch.RuleCmdThroughApp, "cmd/mcp1c/direct.go", "broken/internal/store"},
	})
}

// TestГардЛовитСтарыйСлой: internal/source в новом коде запрещён,
// internal/syntax в том же файле — разрешённое исключение.
func TestГардЛовитСтарыйСлой(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	сверить(t, собрать(arch.CheckLegacyIsolation(модуль)), []нарушениеФикстуры{
		{arch.RuleNoLegacyInNew, "internal/index/legacy.go", "broken/internal/source"},
	})
}

// TestГардЛовитRetrieveЧерезApp: internal/retrieve, тянущий internal/app
// напрямую, обязан упасть — направление зависимостей обратное (app зовёт
// retrieve, не наоборот).
func TestГардЛовитRetrieveЧерезApp(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	сверить(t, собрать(arch.CheckRetrieveNotApp(модуль)), []нарушениеФикстуры{
		{arch.RuleRetrieveNotApp, "internal/retrieve/direct.go", "broken/internal/app"},
	})
}

// TestГардЛовитНепортируемость: syscall, unix-путь, сравнение разделителя со
// слэшем и склейка пути литералом в общем файле — четыре нарушения; те же
// конструкции в scan_windows.go и в файле с build tag не считаются, их
// отбирает сама сборка. indirect.go добавляет пятое: та же склейка, но через
// промежуточную переменную, а не прямо в аргументе вызова.
func TestГардЛовитНепортируемость(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	сверить(t, собрать(arch.CheckPortability(модуль)), []нарушениеФикстуры{
		{arch.RulePortableCommon, "internal/parse/meta/indirect.go", ""},
		{arch.RulePortableCommon, "internal/parse/meta/scan.go", ""},
		{arch.RulePortableCommon, "internal/parse/meta/scan.go", ""},
		{arch.RulePortableCommon, "internal/parse/meta/scan.go", ""},
		{arch.RulePortableCommon, "internal/parse/meta/scan.go", "syscall"},
	})
}

// TestГардЛовитСклейкуЧерезПеременную: изолированная проверка на `p := dir +
// "/sub"; os.ReadFile(p)` — без остальных нарушений scan.go рядом, чтобы
// дефект был виден отдельно от общего счёта фикстуры broken.
func TestГардЛовитСклейкуЧерезПеременную(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	пакет := модуль.Package("internal/parse/meta")
	if пакет == nil {
		t.Fatal("пакет internal/parse/meta не найден в фикстуре broken")
	}
	нашлось := false
	for _, файл := range пакет.Files {
		if файл.Rel != "internal/parse/meta/indirect.go" {
			continue
		}
		нашлось = true
	}
	if !нашлось {
		t.Fatal("indirect.go не попал в фикстуру broken")
	}
	for _, нарушение := range собрать(arch.CheckPortability(модуль)) {
		if нарушение.файл == "internal/parse/meta/indirect.go" {
			return
		}
	}
	t.Error("склейка пути через промежуточную переменную не поймана")
}

// TestГардВидитФиктивныйBuildTag: "//go:build go1.1" — не платформенное условие
// (это версия языка, не GOOS), поэтому файл не освобождается от проверки
// портируемости. "//go:build windows" — настоящий платформенный терм и законно
// исключает файл, хотя в нём тот же самый unix-only литерал.
func TestГардВидитФиктивныйBuildTag(t *testing.T) {
	модуль := загрузитьФикстуру(t, "faketag")
	сверить(t, собрать(arch.CheckPortability(модуль)), []нарушениеФикстуры{
		{arch.RulePortableCommon, "internal/fake/fake.go", ""},
	})
}

// TestГардНаНеполномДереве: пакетов, о которых говорят запреты, может ещё не
// быть — это нормальное состояние сборки, а не ошибка.
func TestГардНаНеполномДереве(t *testing.T) {
	модуль := загрузитьФикстуру(t, "partial")
	if модуль.Package("internal/store") != nil {
		t.Fatal("фикстура partial перестала быть неполной: в ней появился internal/store")
	}
	if нарушения := arch.Check(модуль); len(нарушения) != 0 {
		for _, нарушение := range нарушения {
			t.Error(нарушение.String())
		}
	}
}

// TestГардЛовитПоверхностьStoreВResolve: resolve, открывший базу и взявший
// транзакцию, обязан упасть, и отдельно — импорт слоя, который его же зовёт.
// Соседний ok.go берёт из store только тип факта и константы схемы: это
// объявленная поверхность, и в нарушения он попасть не должен.
func TestГардЛовитПоверхностьStoreВResolve(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	сверить(t, собрать(arch.CheckResolveStoreSurface(модуль)), []нарушениеФикстуры{
		{arch.RuleResolveStoreSurface, "internal/resolve/bad.go", ""},
		{arch.RuleResolveStoreSurface, "internal/resolve/bad.go", ""},
		{arch.RuleResolveStoreSurface, "internal/resolve/bad.go", "broken/internal/app"},
	})
}

// TestГардВидитВсеЗапреты: Check не должен молча забыть одно из правил.
func TestГардВидитВсеЗапреты(t *testing.T) {
	модуль := загрузитьФикстуру(t, "broken")
	найденные := map[string]int{}
	for _, нарушение := range arch.Check(модуль) {
		найденные[нарушение.Rule]++
	}
	for _, правило := range []string{
		arch.RuleDomainStdlibOnly, arch.RuleSQLOnlyInStore, arch.RuleCmdThroughApp,
		arch.RuleNoLegacyInNew, arch.RulePortableCommon, arch.RuleRetrieveNotApp,
		arch.RuleResolveStoreSurface,
	} {
		if найденные[правило] == 0 {
			t.Errorf("Check не вернул ни одного нарушения запрета %s", правило)
		}
	}
}
