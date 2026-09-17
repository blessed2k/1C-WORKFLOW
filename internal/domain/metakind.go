package domain

import "strings"

// MetaKind: вид объекта метаданных во всех словарях, которыми о нём говорят
// источники фактов: MType (имя корневого XML-элемента объявления,
// parse/meta.MetadataObjectFact.MType), каталог коллекции выгрузки
// DumpConfigToFiles и коллекция менеджеров в BSL-коде. Раньше каждый
// потребитель (index, resolve, workspace, parse/bsl) держал свою таблицу
// своего состава, и пробел одной из них в рантайме был неотличим от «объекта
// просто нет». Теперь таблица одна, а намеренные подмножества выражены
// признаками записи.
type MetaKind struct {
	// MType: единственное число на английском ("Catalog").
	MType string
	// DumpDir: каталог коллекции выгрузки, как его пишет платформа
	// ("Catalogs"). Наивного множественного числа нет: у FilterCriterion это
	// "FilterCriteria".
	DumpDir string
	// CollectionRu: коллекция менеджеров в BSL на русском ("Справочники");
	// пусто, если у вида такой коллекции в словаре парсера нет. Английское
	// имя коллекции у всех таких видов совпадает с DumpDir, отдельного поля
	// под него нет.
	CollectionRu string
	// IsRegister: у вида есть наборы записей и менеджеры записи
	// (Движения.Х разрешается среди этих видов).
	IsRegister bool
	// ModuleOwner: индекс привязывает модули и формы вида к объекту-владельцу
	// по каталогу коллекции (internal/index). Не выставлен у Sequence и
	// ExternalDataSource: модуль в таком каталоге получает диагностику
	// index_module_owner_unknown_collection (её держит
	// TestUnknownOwnerCollectionLeavesDiagnostic), а у внешнего источника
	// модули лежат глубже, в Tables/<таблица>, и владелец из первых двух
	// сегментов пути вывелся бы неверно. У видов без модулей признак ни на что
	// не влияет и сохранён как было.
	ModuleOwner bool
}

// metaKinds: список снят с выгрузки (реальная выгрузка с расширениями, 33
// коллекции верхнего уровня) и дополнен видами, которых в ней нет, но которые платформа
// выкладывает так же. Покрытие реальной выгрузки проверяет
// workspace.TestDumpCollectionsCoverRealDump.
var metaKinds = []MetaKind{
	{MType: "AccountingRegister", DumpDir: "AccountingRegisters", CollectionRu: "РегистрыБухгалтерии", IsRegister: true, ModuleOwner: true},
	{MType: "AccumulationRegister", DumpDir: "AccumulationRegisters", CollectionRu: "РегистрыНакопления", IsRegister: true, ModuleOwner: true},
	{MType: "BusinessProcess", DumpDir: "BusinessProcesses", CollectionRu: "БизнесПроцессы", ModuleOwner: true},
	{MType: "CalculationRegister", DumpDir: "CalculationRegisters", CollectionRu: "РегистрыРасчета", IsRegister: true, ModuleOwner: true},
	{MType: "Catalog", DumpDir: "Catalogs", CollectionRu: "Справочники", ModuleOwner: true},
	{MType: "ChartOfAccounts", DumpDir: "ChartsOfAccounts", CollectionRu: "ПланыСчетов", ModuleOwner: true},
	{MType: "ChartOfCalculationTypes", DumpDir: "ChartsOfCalculationTypes", CollectionRu: "ПланыВидовРасчета", ModuleOwner: true},
	{MType: "ChartOfCharacteristicTypes", DumpDir: "ChartsOfCharacteristicTypes", CollectionRu: "ПланыВидовХарактеристик", ModuleOwner: true},
	{MType: "CommandGroup", DumpDir: "CommandGroups"},
	{MType: "CommonAttribute", DumpDir: "CommonAttributes"},
	{MType: "CommonCommand", DumpDir: "CommonCommands", ModuleOwner: true},
	{MType: "CommonForm", DumpDir: "CommonForms", ModuleOwner: true},
	{MType: "CommonModule", DumpDir: "CommonModules", ModuleOwner: true},
	{MType: "CommonPicture", DumpDir: "CommonPictures"},
	{MType: "CommonTemplate", DumpDir: "CommonTemplates"},
	{MType: "Constant", DumpDir: "Constants", CollectionRu: "Константы", ModuleOwner: true},
	{MType: "DataProcessor", DumpDir: "DataProcessors", CollectionRu: "Обработки", ModuleOwner: true},
	{MType: "DefinedType", DumpDir: "DefinedTypes"},
	{MType: "Document", DumpDir: "Documents", CollectionRu: "Документы", ModuleOwner: true},
	{MType: "DocumentJournal", DumpDir: "DocumentJournals", CollectionRu: "ЖурналыДокументов", ModuleOwner: true},
	{MType: "DocumentNumerator", DumpDir: "DocumentNumerators"},
	{MType: "Enum", DumpDir: "Enums", CollectionRu: "Перечисления", ModuleOwner: true},
	{MType: "EventSubscription", DumpDir: "EventSubscriptions"},
	{MType: "ExchangePlan", DumpDir: "ExchangePlans", CollectionRu: "ПланыОбмена", ModuleOwner: true},
	{MType: "ExternalDataSource", DumpDir: "ExternalDataSources", CollectionRu: "ВнешниеИсточникиДанных"},
	{MType: "FilterCriterion", DumpDir: "FilterCriteria", CollectionRu: "КритерииОтбора", ModuleOwner: true},
	{MType: "FunctionalOption", DumpDir: "FunctionalOptions"},
	{MType: "FunctionalOptionsParameter", DumpDir: "FunctionalOptionsParameters"},
	{MType: "HTTPService", DumpDir: "HTTPServices", ModuleOwner: true},
	{MType: "InformationRegister", DumpDir: "InformationRegisters", CollectionRu: "РегистрыСведений", IsRegister: true, ModuleOwner: true},
	{MType: "IntegrationService", DumpDir: "IntegrationServices", ModuleOwner: true},
	{MType: "Interface", DumpDir: "Interfaces"},
	{MType: "Language", DumpDir: "Languages"},
	{MType: "Report", DumpDir: "Reports", CollectionRu: "Отчеты", ModuleOwner: true},
	{MType: "Role", DumpDir: "Roles", ModuleOwner: true},
	{MType: "ScheduledJob", DumpDir: "ScheduledJobs", ModuleOwner: true},
	{MType: "Sequence", DumpDir: "Sequences", CollectionRu: "Последовательности"},
	{MType: "SessionParameter", DumpDir: "SessionParameters"},
	{MType: "SettingsStorage", DumpDir: "SettingsStorages", CollectionRu: "ХранилищаНастроек", ModuleOwner: true},
	{MType: "Style", DumpDir: "Styles"},
	{MType: "StyleItem", DumpDir: "StyleItems"},
	{MType: "Subsystem", DumpDir: "Subsystems"},
	{MType: "Task", DumpDir: "Tasks", CollectionRu: "Задачи", ModuleOwner: true},
	{MType: "WSReference", DumpDir: "WSReferences"},
	{MType: "WebService", DumpDir: "WebServices", ModuleOwner: true},
	{MType: "XDTOPackage", DumpDir: "XDTOPackages"},
}

// Индексы поиска строятся один раз из metaKinds, отдельной правки не
// требуют.
var (
	metaKindByMType       = map[string]int{}
	metaKindByDumpDir     = map[string]int{}
	metaKindByDumpDirFold = map[string]int{}
	metaKindByCollection  = map[string]int{}
)

func init() {
	for i, k := range metaKinds {
		metaKindByMType[k.MType] = i
		metaKindByDumpDir[k.DumpDir] = i
		metaKindByDumpDirFold[strings.ToLower(k.DumpDir)] = i
		if k.CollectionRu != "" {
			metaKindByCollection[NormalizeName(k.CollectionRu)] = i
			metaKindByCollection[NormalizeName(k.DumpDir)] = i
		}
	}
}

// MetaKinds возвращает копию словаря видов в порядке MType.
func MetaKinds() []MetaKind {
	return append([]MetaKind(nil), metaKinds...)
}

// MetaKindByMType ищет вид по MType ("Catalog"), с учётом регистра.
func MetaKindByMType(mtype string) (MetaKind, bool) {
	return metaKindAt(metaKindByMType, mtype)
}

// MetaKindByDumpDir ищет вид по каталогу коллекции выгрузки ("Catalogs"),
// с учётом регистра: так каталог пишет сама выгрузка.
func MetaKindByDumpDir(dir string) (MetaKind, bool) {
	return metaKindAt(metaKindByDumpDir, dir)
}

// MetaKindByDumpDirFold: то же без учёта регистра (так путь модуля
// опознаёт парсер BSL).
func MetaKindByDumpDirFold(dir string) (MetaKind, bool) {
	return metaKindAt(metaKindByDumpDirFold, strings.ToLower(dir))
}

// MetaKindByCollection ищет вид по коллекции менеджеров, как она написана в
// коде: русское ("Справочники") или английское ("Catalogs") имя, без учёта
// регистра. Вид без коллекции менеджеров не находится.
func MetaKindByCollection(name string) (MetaKind, bool) {
	return metaKindAt(metaKindByCollection, NormalizeName(name))
}

func metaKindAt(index map[string]int, key string) (MetaKind, bool) {
	i, ok := index[key]
	if !ok {
		return MetaKind{}, false
	}
	return metaKinds[i], true
}
