package workspace

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Раскладка XML-выгрузки конфигурации (DumpConfigToFiles) — ОДНО место, где
// она записана правилом, а не литералом по месту.
//
// Правило выведено из реальной выгрузки (выгрузка с расширениями, 11 657 файлов)
// и подтверждено приёмкой: объявление объекта лежит ФАЙЛОМ рядом с каталогом своих
// модулей ("Documents/Штрафы.xml"), а модули — внутри одноимённого каталога
// ("Documents/Штрафы/Ext/ObjectModule.bsl"). Вложенной раскладки
// "Documents/Штрафы/Штрафы.xml" DumpConfigToFiles НЕ производит.
//
// Зачем это здесь, а не в фикстуре каждого пакета: фикстуры двух пакетов
// (internal/retrieve, internal/app) писали оба пути литералами и разъехались
// с реальной выгрузкой — регрессию, отдававшую обработчик проведения чужого
// документа, поймала приёмка на живой базе, а не зелёный прогон. Пока правило
// живёт литералом в каждом сиде, оно расходится снова.
//
// У правила три лица, и все три обязаны знать одно и то же: построение
// (Dump*), проверка формы (IsDumpConformantPath) и — на стороне
// internal/retrieve — обратный разбор (objectModuleDir). Согласие лиц
// закреплено internal/retrieve/dumplayout_roundtrip_test.go.

// Каталог коллекции по виду объекта берётся из общего словаря видов
// domain.MetaKinds (DumpDir). Список там СНЯТ С ВЫГРУЗКИ (реальная выгрузка,
// 33 коллекции верхнего уровня) и дополнен видами, которых в этой конфигурации
// нет, но которые платформа выкладывает так же. Покрытие выгрузки проверяется
// тестом TestDumpCollectionsCoverRealDump.
//
// Наивного множественного числа в словаре нет намеренно: у FilterCriterion
// оно давало "FilterCriterions", каталога с таким именем не бывает, и путь не
// проходил бы даже собственную проверку формы. Вид, которого нет в
// domain.MetaKinds, получает явный отказ в построении, см. mustCollectionDir.

// DumpCollectionDir — каталог коллекции вида объекта ("Document" ->
// "Documents"). Второе значение — описан ли вид вообще; тот, кто строит путь,
// а не проверяет вид, пользуется Dump*-функциями ниже.
func DumpCollectionDir(mtype string) (string, bool) {
	k, ok := domain.MetaKindByMType(mtype)
	return k.DumpDir, ok
}

// mustCollectionDir — явный отказ вместо догадки. Неописанный вид объекта это
// ошибка вызывающего, а не свойство данных: раньше здесь стояло наивное
// множественное число, и оно молча производило путь, которого в выгрузке не
// бывает. Паника, а не пустая строка: пустую строку вызывающий склеит с
// именем и получит правдоподобный мусор.
func mustCollectionDir(mtype string) string {
	dir, ok := DumpCollectionDir(mtype)
	if !ok {
		panic("workspace: раскладка выгрузки для вида объекта " + mtype +
			" не описана: добавьте вид в domain.MetaKinds, угадывать множественное число нельзя")
	}
	return dir
}

// DumpObjectDir — каталог модулей объекта: "Documents/Штрафы". Это тот же
// каталог, который internal/retrieve выводит из строки source_file
// объявления (objectModuleDir) — здесь он строится в обратную сторону, из
// вида и имени. Паникует на неописанном виде (mustCollectionDir).
func DumpObjectDir(mtype, nameDisplay string) string {
	return mustCollectionDir(mtype) + "/" + nameDisplay
}

// DumpDeclarationPath — путь файла объявления объекта:
// "Documents/Штрафы.xml". Каталог объекта и файл объявления — СОСЕДИ, и
// именно на этом стоит вывод владения модулем.
func DumpDeclarationPath(mtype, nameDisplay string) string {
	return DumpObjectDir(mtype, nameDisplay) + ".xml"
}

// DumpChildSubsystemPath — путь объявления вложенной подсистемы по пути
// объявления её родителя: "Subsystems/А.xml" + "Б" даёт
// "Subsystems/А/Subsystems/Б.xml". Вложенные подсистемы лежат в каталоге
// родителя, в такой же коллекции, на любой глубине.
//
// Остальные лица правила (IsDumpConformantPath, обратный разбор) вложенных
// подсистем пока не знают: объектами индекса они не становятся.
func DumpChildSubsystemPath(parentDeclPath, childName string) string {
	return strings.TrimSuffix(parentDeclPath, ".xml") + "/" + mustCollectionDir("Subsystem") + "/" + childName + ".xml"
}

// ModuleKind — вид модуля объекта, то есть имя файла модуля внутри "Ext".
// Это ПАРАМЕТР правила, а не хвост-литерал у вызывающего: у одного вида
// объекта модулей несколько (у регистра — набор записей и менеджер, у
// документа — объекта и менеджер), и стоило правилу знать один, как второй
// начали приклеивать к DumpObjectDir строкой на стороне вызывающего — то
// самое расползание, ради устранения которого этот файл и заведён.
type ModuleKind string

const (
	ModuleObject       ModuleKind = "ObjectModule"
	ModuleManager      ModuleKind = "ManagerModule"
	ModuleRecordSet    ModuleKind = "RecordSetModule"
	ModuleValueManager ModuleKind = "ValueManagerModule"
	ModuleCommand      ModuleKind = "CommandModule"
	// ModuleCommon — единственный модуль общего модуля; платформа зовёт его
	// просто Module.bsl, без вида в имени.
	ModuleCommon ModuleKind = "Module"
)

var dumpModuleKinds = map[ModuleKind]bool{
	ModuleObject: true, ModuleManager: true, ModuleRecordSet: true,
	ModuleValueManager: true, ModuleCommand: true, ModuleCommon: true,
}

// DumpModulePath — путь модуля объекта:
// "Documents/Штрафы/Ext/ObjectModule.bsl". Вид модуля называет вызывающий:
// вывести его из вида объекта нельзя, у одного объекта модулей несколько.
func DumpModulePath(mtype, nameDisplay string, kind ModuleKind) string {
	return DumpObjectDir(mtype, nameDisplay) + "/Ext/" + string(kind) + ".bsl"
}

// DumpFormModulePath — путь модуля формы объекта:
// "Documents/Заказ/Forms/ФормаДокумента/Ext/Form/Module.bsl".
func DumpFormModulePath(mtype, nameDisplay, formName string) string {
	return DumpObjectDir(mtype, nameDisplay) + "/Forms/" + formName + "/Ext/Form/Module.bsl"
}

// IsDumpConformantPath — третье лицо того же правила: может ли путь вообще
// быть произведён раскладкой DumpConfigToFiles. Проверяется ФОРМА, не
// конкретный объект: коллекция из известного списка плюс одна из трёх
// раскладок — объявление, модуль объекта, модуль формы.
//
// Нужен затем, что помощник построения ничего не обязывает: сид, написавший
// путь литералом мимо него, компилируется и остаётся зелёным. Здесь эта
// дисциплина становится проверкой (fixture-помощники зовут предикат на
// каждый rel_path), а согласие всех трёх лиц правила закреплено round-trip
// тестом.
//
// Предел назван честно: проверяется форма, а НЕ соответствие имени файла
// объекту. "Documents/Прочее.xml" конформен, хотя объекта "Прочее" может и не
// быть, — на этом стоит фикстура случая «каталог модулей вывести не из чего».
func IsDumpConformantPath(rel string) bool {
	parts := strings.Split(strings.ReplaceAll(rel, "\\", "/"), "/")
	if len(parts) < 2 || parts[1] == "" {
		return false
	}
	if _, known := domain.MetaKindByDumpDir(parts[0]); !known {
		return false
	}
	switch len(parts) {
	case 2:
		// "Documents/Штрафы.xml" — объявление объекта.
		return strings.HasSuffix(parts[1], ".xml") && parts[1] != ".xml"
	case 4:
		// "Documents/Штрафы/Ext/ObjectModule.bsl" — модуль объекта.
		return parts[2] == "Ext" && strings.HasSuffix(parts[3], ".bsl") &&
			dumpModuleKinds[ModuleKind(strings.TrimSuffix(parts[3], ".bsl"))]
	case 7:
		// "Documents/Заказ/Forms/ФормаДокумента/Ext/Form/Module.bsl" — модуль
		// формы; после Forms/<имя> идёт фиксированный хвост.
		return parts[2] == "Forms" && parts[3] != "" && parts[4] == "Ext" &&
			parts[5] == "Form" && parts[6] == "Module.bsl"
	default:
		return false
	}
}
