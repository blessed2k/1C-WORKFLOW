package retrieve

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// Раскладка выгрузки живёт в ДВУХ направлениях и двух пакетах: вперёд путь
// собирается (workspace.DumpDeclarationPath/DumpObjectDir/DumpModulePath),
// назад каталог модулей выводится из строки source_file
// (retrieve.objectModuleDir). Расхождение направлений — та же форма дефекта,
// что выбор обработчика по подстроке имени в пути модуля, только этажом
// выше: обе стороны по отдельности выглядят разумно, а вместе перестают
// сходиться, и заметно это становится на живой выгрузке.
//
// Один зашитый случай согласия ничего не доказывает про остальные виды
// объектов: каталоги коллекций разные, имя файла модуля разное, а правило
// «последнее имя каталога и есть имя объекта» — общее. Поэтому таблица.
var roundTripCases = []struct {
	mtype string
	name  string
	kind  workspace.ModuleKind
}{
	{"Document", "Штрафы", workspace.ModuleObject},
	{"Document", "ДопНачисления", workspace.ModuleObject},
	{"Catalog", "Номенклатура", workspace.ModuleObject},
	{"InformationRegister", "ТоварыНаСкладах", workspace.ModuleRecordSet},
	{"AccumulationRegister", "ДвиженияДсСотрудников", workspace.ModuleRecordSet},
	{"AccountingRegister", "Хозрасчетный", workspace.ModuleRecordSet},
	{"CalculationRegister", "Начисления", workspace.ModuleRecordSet},
	{"ChartOfAccounts", "Хозрасчетный", workspace.ModuleObject},
	{"ChartOfCharacteristicTypes", "ВидыСубконто", workspace.ModuleObject},
	{"BusinessProcess", "Задание", workspace.ModuleObject},
	{"Task", "ЗадачаИсполнителя", workspace.ModuleObject},
	{"ExchangePlan", "ОбменСБухгалтерией", workspace.ModuleObject},
	{"DataProcessor", "ЗакрытиеМесяца", workspace.ModuleObject},
	{"Report", "ОстаткиТоваров", workspace.ModuleObject},
	{"DocumentJournal", "Продажи", workspace.ModuleManager},
	{"Constant", "Штрафы", workspace.ModuleValueManager},
	{"CommonModule", "ОбщегоНазначения", workspace.ModuleCommon},
}

// TestDumpLayoutRoundTrip — направления правила обязаны сходиться:
// objectModuleDir(DumpDeclarationPath(вид, имя)) == нормализованный
// DumpObjectDir(вид, имя). Плюс главное следствие, на котором стоит поиск
// обработчика проведения (findPostingHandler): путь модуля, собранный
// вперёд, лежит ВНУТРИ выведенного назад каталога.
func TestDumpLayoutRoundTrip(t *testing.T) {
	for _, c := range roundTripCases {
		t.Run(c.mtype+"."+c.name, func(t *testing.T) {
			norm := func(s string) string { return domain.NormalizeName(domain.NormalizeModulePath(s)) }
			decl := workspace.DumpDeclarationPath(c.mtype, c.name)
			wantDir := norm(workspace.DumpObjectDir(c.mtype, c.name))

			gotDir := objectModuleDir(decl, domain.NormalizeName(c.name))
			if gotDir != wantDir {
				t.Fatalf("objectModuleDir(%q) = %q, want %q — направления правила разошлись", decl, gotDir, wantDir)
			}

			// Ровно та проверка, которую делает findPostingHandler.
			modulePath := norm(workspace.DumpModulePath(c.mtype, c.name, c.kind))
			if !strings.HasPrefix(modulePath, gotDir+"/") {
				t.Errorf("модуль %q не лежит внутри выведенного каталога %q — владение модулем не выведется", modulePath, gotDir)
			}

			// Третье лицо правила: предикат обязан принимать всё, что правило
			// само же и производит.
			for _, path := range []string{
				decl,
				workspace.DumpModulePath(c.mtype, c.name, c.kind),
				workspace.DumpFormModulePath(c.mtype, c.name, "ФормаДокумента"),
			} {
				if !workspace.IsDumpConformantPath(path) {
					t.Errorf("IsDumpConformantPath(%q) = false, а путь произведён самим правилом", path)
				}
			}
		})
	}
}

// TestDumpLayoutRejectsInventedLayouts — обратная половина: раскладки, которых
// DumpConfigToFiles не производит, предикат обязан отвергать. Первые две —
// ровно то, что стояло в фикстурах двух пакетов и держало прогон зелёным,
// пока приёмка на живой базе не показала обратное.
func TestDumpLayoutRejectsInventedLayouts(t *testing.T) {
	invented := []string{
		"Documents/Заказ/Заказ.xml",              // вложенное объявление — такого нет
		"Documents/Заказ/ModuleObject.bsl",       // модуль мимо каталога Ext
		"InformationRegisters/Пустой/Пустой.xml", // то же вложенное объявление у регистра
		"M1", // путь-заглушка
		"Documents/Заказ/Ext/ЧтоТоСвоё.bsl",        // вид модуля вне правила
		"Справочники/Номенклатура.xml",             // коллекция не платформенная
		"Documents/Заказ/Forms/Ф/Module.bsl",       // хвост формы укорочен
		"Documents/Заказ/Ext/ObjectModule.bsl/еще", // лишний сегмент
	}
	for _, path := range invented {
		if workspace.IsDumpConformantPath(path) {
			t.Errorf("IsDumpConformantPath(%q) = true, а такой раскладки DumpConfigToFiles не производит", path)
		}
	}
}
