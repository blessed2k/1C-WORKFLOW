package domain_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Ожидания ниже перенесены из таблиц, которые жили в потребителях до
// сведения словаря видов в domain (прогон C2), а не получены из кода под
// тестом. Каждая таблица: своё подмножество, и каждое обязано сохраниться.

// Бывший workspace.dumpCollectionDirs: вид -> каталог коллекции. Самый полный
// список, он и есть объединение всех прежних таблиц.
var прежниеКаталоги = map[string]string{
	"AccountingRegister":         "AccountingRegisters",
	"AccumulationRegister":       "AccumulationRegisters",
	"BusinessProcess":            "BusinessProcesses",
	"CalculationRegister":        "CalculationRegisters",
	"Catalog":                    "Catalogs",
	"ChartOfAccounts":            "ChartsOfAccounts",
	"ChartOfCalculationTypes":    "ChartsOfCalculationTypes",
	"ChartOfCharacteristicTypes": "ChartsOfCharacteristicTypes",
	"CommandGroup":               "CommandGroups",
	"CommonAttribute":            "CommonAttributes",
	"CommonCommand":              "CommonCommands",
	"CommonForm":                 "CommonForms",
	"CommonModule":               "CommonModules",
	"CommonPicture":              "CommonPictures",
	"CommonTemplate":             "CommonTemplates",
	"Constant":                   "Constants",
	"DataProcessor":              "DataProcessors",
	"DefinedType":                "DefinedTypes",
	"Document":                   "Documents",
	"DocumentJournal":            "DocumentJournals",
	"DocumentNumerator":          "DocumentNumerators",
	"Enum":                       "Enums",
	"EventSubscription":          "EventSubscriptions",
	"ExchangePlan":               "ExchangePlans",
	"ExternalDataSource":         "ExternalDataSources",
	"FilterCriterion":            "FilterCriteria",
	"FunctionalOption":           "FunctionalOptions",
	"FunctionalOptionsParameter": "FunctionalOptionsParameters",
	"HTTPService":                "HTTPServices",
	"InformationRegister":        "InformationRegisters",
	"IntegrationService":         "IntegrationServices",
	"Interface":                  "Interfaces",
	"Language":                   "Languages",
	"Report":                     "Reports",
	"Role":                       "Roles",
	"ScheduledJob":               "ScheduledJobs",
	"Sequence":                   "Sequences",
	"SessionParameter":           "SessionParameters",
	"SettingsStorage":            "SettingsStorages",
	"Style":                      "Styles",
	"StyleItem":                  "StyleItems",
	"Subsystem":                  "Subsystems",
	"Task":                       "Tasks",
	"WSReference":                "WSReferences",
	"WebService":                 "WebServices",
	"XDTOPackage":                "XDTOPackages",
}

// Бывший index.ownerTypeToMType: каталог -> вид, у которого индекс
// привязывает модуль к владельцу (признак ModuleOwner).
var прежниеВладельцы = map[string]string{
	"CommonModules":               "CommonModule",
	"Catalogs":                    "Catalog",
	"Documents":                   "Document",
	"DocumentJournals":            "DocumentJournal",
	"Enums":                       "Enum",
	"Reports":                     "Report",
	"DataProcessors":              "DataProcessor",
	"InformationRegisters":        "InformationRegister",
	"AccumulationRegisters":       "AccumulationRegister",
	"AccountingRegisters":         "AccountingRegister",
	"CalculationRegisters":        "CalculationRegister",
	"ChartsOfCharacteristicTypes": "ChartOfCharacteristicTypes",
	"ChartsOfAccounts":            "ChartOfAccounts",
	"ChartsOfCalculationTypes":    "ChartOfCalculationTypes",
	"BusinessProcesses":           "BusinessProcess",
	"Tasks":                       "Task",
	"ExchangePlans":               "ExchangePlan",
	"Constants":                   "Constant",
	"CommonForms":                 "CommonForm",
	"Roles":                       "Role",
	"ScheduledJobs":               "ScheduledJob",
	"CommonCommands":              "CommonCommand",
	"HTTPServices":                "HTTPService",
	"WebServices":                 "WebService",
	"IntegrationServices":         "IntegrationService",
	"SettingsStorages":            "SettingsStorage",
	"FilterCriteria":              "FilterCriterion",
}

// Бывший parse/bsl.managerCollections: коллекция менеджеров в коде
// (нормализованная) -> каталог коллекции.
var прежниеКоллекции = map[string]string{
	"справочники":                 "Catalogs",
	"catalogs":                    "Catalogs",
	"документы":                   "Documents",
	"documents":                   "Documents",
	"журналыдокументов":           "DocumentJournals",
	"documentjournals":            "DocumentJournals",
	"перечисления":                "Enums",
	"enums":                       "Enums",
	"отчеты":                      "Reports",
	"reports":                     "Reports",
	"обработки":                   "DataProcessors",
	"dataprocessors":              "DataProcessors",
	"регистрысведений":            "InformationRegisters",
	"informationregisters":        "InformationRegisters",
	"регистрынакопления":          "AccumulationRegisters",
	"accumulationregisters":       "AccumulationRegisters",
	"регистрыбухгалтерии":         "AccountingRegisters",
	"accountingregisters":         "AccountingRegisters",
	"регистрырасчета":             "CalculationRegisters",
	"calculationregisters":        "CalculationRegisters",
	"планывидовхарактеристик":     "ChartsOfCharacteristicTypes",
	"chartsofcharacteristictypes": "ChartsOfCharacteristicTypes",
	"планысчетов":                 "ChartsOfAccounts",
	"chartsofaccounts":            "ChartsOfAccounts",
	"планывидоврасчета":           "ChartsOfCalculationTypes",
	"chartsofcalculationtypes":    "ChartsOfCalculationTypes",
	"бизнеспроцессы":              "BusinessProcesses",
	"businessprocesses":           "BusinessProcesses",
	"задачи":                      "Tasks",
	"tasks":                       "Tasks",
	"планыобмена":                 "ExchangePlans",
	"exchangeplans":               "ExchangePlans",
	"константы":                   "Constants",
	"constants":                   "Constants",
	"критерииотбора":              "FilterCriteria",
	"filtercriteria":              "FilterCriteria",
	"последовательности":          "Sequences",
	"sequences":                   "Sequences",
	"хранилищанастроек":           "SettingsStorages",
	"settingsstorages":            "SettingsStorages",
	"внешниеисточникиданных":      "ExternalDataSources",
	"externaldatasources":         "ExternalDataSources",
}

// Бывший resolve.bslCollectionToMType: каталог -> вид. Подмножество
// объединения, значения обязаны совпасть.
var прежниеВидыРезолвера = map[string]string{
	"Catalogs":                    "Catalog",
	"Documents":                   "Document",
	"DocumentJournals":            "DocumentJournal",
	"Enums":                       "Enum",
	"Reports":                     "Report",
	"DataProcessors":              "DataProcessor",
	"InformationRegisters":        "InformationRegister",
	"AccumulationRegisters":       "AccumulationRegister",
	"AccountingRegisters":         "AccountingRegister",
	"CalculationRegisters":        "CalculationRegister",
	"ChartsOfCharacteristicTypes": "ChartOfCharacteristicTypes",
	"ChartsOfAccounts":            "ChartOfAccounts",
	"ChartsOfCalculationTypes":    "ChartOfCalculationTypes",
	"BusinessProcesses":           "BusinessProcess",
	"Tasks":                       "Task",
	"ExchangePlans":               "ExchangePlan",
	"Constants":                   "Constant",
}

// Бывшие resolve.registerMTypes и parse/bsl.registerCollections.
var прежниеРегистры = map[string]string{
	"InformationRegister":  "InformationRegisters",
	"AccumulationRegister": "AccumulationRegisters",
	"AccountingRegister":   "AccountingRegisters",
	"CalculationRegister":  "CalculationRegisters",
}

// Бывший parse/bsl.dumpCollections (в нижнем регистре). Подмножество
// объединения: Interfaces и Styles в нём не было.
var прежниеКаталогиПарсера = []string{
	"accountingregisters", "accumulationregisters", "businessprocesses",
	"calculationregisters", "catalogs", "chartsofaccounts",
	"chartsofcalculationtypes", "chartsofcharacteristictypes",
	"commonattributes", "commoncommands", "commonforms",
	"commonmodules", "commonpictures", "commontemplates",
	"constants", "dataprocessors", "definedtypes",
	"documentjournals", "documentnumerators", "documents",
	"enums", "eventsubscriptions", "exchangeplans",
	"externaldatasources", "filtercriteria", "functionaloptions",
	"functionaloptionsparameters", "httpservices", "informationregisters",
	"integrationservices", "languages", "reports", "roles",
	"scheduledjobs", "sequences", "sessionparameters",
	"settingsstorages", "styleitems", "subsystems",
	"tasks", "webservices", "wsreferences", "xdtopackages",
	"commandgroups",
}

// TestMetaKindsComposition: состав словаря равен объединению прежних таблиц,
// признаки равны прежним подмножествам.
func TestMetaKindsComposition(t *testing.T) {
	kinds := domain.MetaKinds()
	if len(kinds) != len(прежниеКаталоги) {
		t.Errorf("видов %d, want %d (объединение прежних таблиц)", len(kinds), len(прежниеКаталоги))
	}
	owners, registers := map[string]string{}, map[string]string{}
	collections := map[string]string{}
	for _, k := range kinds {
		if want, ok := прежниеКаталоги[k.MType]; !ok || want != k.DumpDir {
			t.Errorf("вид %s: каталог %q, want %q (есть в прежней таблице: %v)", k.MType, k.DumpDir, want, ok)
		}
		if k.ModuleOwner {
			owners[k.DumpDir] = k.MType
		}
		if k.IsRegister {
			registers[k.MType] = k.DumpDir
		}
		if k.CollectionRu != "" {
			collections[domain.NormalizeName(k.CollectionRu)] = k.DumpDir
			collections[domain.NormalizeName(k.DumpDir)] = k.DumpDir
		}
	}
	assertSameMap(t, "ModuleOwner", owners, прежниеВладельцы)
	assertSameMap(t, "IsRegister", registers, прежниеРегистры)
	assertSameMap(t, "коллекции менеджеров", collections, прежниеКоллекции)

	for dir, mtype := range прежниеВидыРезолвера {
		k, ok := domain.MetaKindByDumpDir(dir)
		if !ok || k.MType != mtype {
			t.Errorf("MetaKindByDumpDir(%q) = %q,%v, want %q (resolve)", dir, k.MType, ok, mtype)
		}
	}
	for _, dir := range прежниеКаталогиПарсера {
		if _, ok := domain.MetaKindByDumpDirFold(dir); !ok {
			t.Errorf("MetaKindByDumpDirFold(%q) не найден (parse/bsl)", dir)
		}
	}
}

// TestMetaKindsLookupRoundTrip: ключи уникальны, каждый поиск возвращает ту
// же запись, неизвестное не находится.
func TestMetaKindsLookupRoundTrip(t *testing.T) {
	seenMType, seenDir, seenColl := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, k := range domain.MetaKinds() {
		if k.MType == "" || k.DumpDir == "" {
			t.Fatalf("пустой ключ в записи %+v", k)
		}
		if seenMType[k.MType] {
			t.Errorf("MType %q повторяется", k.MType)
		}
		seenMType[k.MType] = true
		if seenDir[strings.ToLower(k.DumpDir)] {
			t.Errorf("DumpDir %q повторяется", k.DumpDir)
		}
		seenDir[strings.ToLower(k.DumpDir)] = true

		if got, ok := domain.MetaKindByMType(k.MType); !ok || got != k {
			t.Errorf("MetaKindByMType(%q) = %+v,%v", k.MType, got, ok)
		}
		if got, ok := domain.MetaKindByDumpDir(k.DumpDir); !ok || got != k {
			t.Errorf("MetaKindByDumpDir(%q) = %+v,%v", k.DumpDir, got, ok)
		}
		if got, ok := domain.MetaKindByDumpDirFold(strings.ToUpper(k.DumpDir)); !ok || got != k {
			t.Errorf("MetaKindByDumpDirFold(%q) = %+v,%v", strings.ToUpper(k.DumpDir), got, ok)
		}
		// Точный поиск по каталогу регистр не складывает: так читал
		// каталог-владельца индекс.
		if _, ok := domain.MetaKindByDumpDir(strings.ToLower(k.DumpDir)); ok {
			t.Errorf("MetaKindByDumpDir(%q) нашёл вид без учёта регистра", strings.ToLower(k.DumpDir))
		}

		got, ok := domain.MetaKindByCollection(k.DumpDir)
		if k.CollectionRu == "" {
			if ok {
				t.Errorf("MetaKindByCollection(%q) нашёл вид без коллекции менеджеров: %+v", k.DumpDir, got)
			}
			continue
		}
		if !ok || got != k {
			t.Errorf("MetaKindByCollection(%q) = %+v,%v", k.DumpDir, got, ok)
		}
		ru := domain.NormalizeName(k.CollectionRu)
		if seenColl[ru] {
			t.Errorf("коллекция %q повторяется", k.CollectionRu)
		}
		seenColl[ru] = true
		for _, name := range []string{k.CollectionRu, strings.ToUpper(k.CollectionRu), ru} {
			if got, ok := domain.MetaKindByCollection(name); !ok || got != k {
				t.Errorf("MetaKindByCollection(%q) = %+v,%v", name, got, ok)
			}
		}
	}

	for _, unknown := range []string{"", "ВыдуманныйВид", "Catalogs "} {
		if _, ok := domain.MetaKindByMType(unknown); ok {
			t.Errorf("MetaKindByMType(%q) нашёл вид", unknown)
		}
		if _, ok := domain.MetaKindByDumpDir(unknown); ok {
			t.Errorf("MetaKindByDumpDir(%q) нашёл вид", unknown)
		}
		if _, ok := domain.MetaKindByCollection(unknown); ok {
			t.Errorf("MetaKindByCollection(%q) нашёл вид", unknown)
		}
	}
}

// TestMetaKindsIsCopy: MetaKinds отдаёт копию: правка результата словарь не
// меняет.
func TestMetaKindsIsCopy(t *testing.T) {
	kinds := domain.MetaKinds()
	kinds[0].DumpDir = "Испорчено"
	if again := domain.MetaKinds(); again[0].DumpDir == "Испорчено" {
		t.Fatal("MetaKinds отдаёт общий срез, словарь правится снаружи")
	}
}

func assertSameMap(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	var diff []string
	for k, v := range want {
		if got[k] != v {
			diff = append(diff, k+": "+got[k]+" != "+v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			diff = append(diff, k+": лишний")
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Errorf("%s расходится с прежней таблицей: %v", what, diff)
	}
}
