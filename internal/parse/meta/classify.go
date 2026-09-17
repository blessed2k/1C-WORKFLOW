// Package meta разбирает XML-выгрузку метаданных 1С: объекты, реквизиты и
// табличные части, формы (объявление + структура), общие модули (module
// registry), подписки на события, регламентные задания, роли и права,
// предопределённые элементы, EPF/ERF-исходники.
//
// Пакет — единственная точка чтения этого XML (архитектура §6): второй
// разбор той же сущности запрещён и проверяется гардом internal/arch.
// Существующая точка internal/source/xmltypes.go остаётся compat-слоем и не
// импортируется отсюда — её семантика (writepath.go, rightsaudit.go,
// extpoints.go) воспроизведена заново, самостоятельно.
package meta

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Kind — вид XML-файла выгрузки, определяемый ТОЛЬКО по пути (без чтения
// содержимого): Classify — чистая функция.
type Kind string

const (
	// KindConfigurationRoot — корень конфигурации/расширения: Configuration.xml.
	KindConfigurationRoot Kind = "configuration-root"
	// KindMetadataObject — объект метаданных верхнего уровня: "<Папка>/<Имя>.xml"
	// (Catalogs/Товары.xml, CommonModules/Х.xml, ScheduledJobs/Х.xml, ...) или
	// корень EPF/ERF-исходников ("<Имя>.xml" в корне компонента).
	KindMetadataObject Kind = "metadata-object"
	// KindEventSubscription — EventSubscriptions/<Имя>.xml.
	KindEventSubscription Kind = "event-subscription"
	// KindFormStructure — структура формы: ".../Forms/<Имя>/Ext/Form.xml".
	KindFormStructure Kind = "form-structure"
	// KindRoleRights — Roles/<Имя>/Ext/Rights.xml.
	KindRoleRights Kind = "role-rights"
	// KindPredefinedData — предопределённые элементы: ".../<Имя>/Ext/Predefined.xml".
	KindPredefinedData Kind = "predefined-data"
	// KindUnsupported — файл распознан как выгрузка 1С, но не разбирается этим
	// пакетом (макеты, СКД-схемы, картинки, CommandInterface.xml,
	// ConfigDumpInfo.xml, помощь и т.п.). ParseFile возвращает пустые Facts без
	// диагностик-ошибок: это осознанный пропуск, а не дефект разбора.
	KindUnsupported Kind = "unsupported"
	// KindBinary — бинарный .epf/.erf. Содержимое этого файла НИКОГДА не
	// читается: обнаружение файлов обязано отсеивать такие пути ДО чтения
	// байтов, Classify лишь называет причину для того, кто проверяет это извне.
	KindBinary Kind = "binary"
)

// Classify определяет вид файла по его пути относительно корня компонента.
// Путь канонизируется через domain.NormalizeModulePath — тем же способом,
// каким канонизируются пути BSL-модулей, чтобы разные написания одного пути
// (обратный слеш, двойной слеш) давали один и тот же вид.
func Classify(relPath string) Kind {
	norm := domain.NormalizeModulePath(relPath)
	if norm == "" {
		return KindUnsupported
	}
	if isBinaryExt(norm) {
		return KindBinary
	}
	if !strings.HasSuffix(strings.ToLower(norm), ".xml") {
		return KindUnsupported
	}
	if norm == "Configuration.xml" {
		return KindConfigurationRoot
	}
	if norm == "ConfigDumpInfo.xml" {
		return KindUnsupported
	}

	segs := strings.Split(norm, "/")

	// Формы: суффикс "<Имя>/Ext/Form.xml" независимо от глубины владельца.
	// У объектов конфигурации это ".../Forms/<Имя>/Ext/Form.xml", у общей формы
	// сама CommonForms/<Имя> и есть форма — "Forms" между ними не появляется.
	// Суффикс "Ext/Form.xml" в выгрузке не означает ничего другого.
	if n := len(segs); n >= 3 &&
		strings.EqualFold(segs[n-1], "Form.xml") && strings.EqualFold(segs[n-2], "Ext") {
		return KindFormStructure
	}

	// Права роли: Roles/<Имя>/Ext/Rights.xml — глубина фиксирована, роли не
	// бывают вложенными.
	if len(segs) == 4 && segs[0] == "Roles" &&
		strings.EqualFold(segs[2], "Ext") && strings.EqualFold(segs[3], "Rights.xml") {
		return KindRoleRights
	}

	// Предопределённые элементы: суффикс "<Имя>/Ext/Predefined.xml".
	if n := len(segs); n >= 4 &&
		strings.EqualFold(segs[n-1], "Predefined.xml") && strings.EqualFold(segs[n-2], "Ext") {
		return KindPredefinedData
	}

	// Подписки на события: ровно "EventSubscriptions/<Имя>.xml".
	if len(segs) == 2 && segs[0] == "EventSubscriptions" {
		return KindEventSubscription
	}

	// "Ext" на верхнем уровне компонента — служебная папка конфигурации
	// (MainSectionPicture.xml, CommandInterface.xml, ClientApplicationInterface.xml
	// и подобные), а не категория объектов метаданных: "Ext" никогда не
	// называет объект сам по себе, только служебную подпапку объекта.
	if segs[0] == "Ext" {
		return KindUnsupported
	}

	// Общий вид объекта метаданных: "<Папка>/<Имя>.xml" на верхнем уровне
	// компонента (2 сегмента) либо корень EPF/ERF-исходников (1 сегмент,
	// "<Имя>.xml" лежит прямо в корне компонента).
	if len(segs) == 1 || len(segs) == 2 {
		return KindMetadataObject
	}

	return KindUnsupported
}

// isBinaryExt сообщает, что путь называет бинарную выгрузку внешней
// обработки/отчёта: такие файлы не текстовые и не читаются никогда.
func isBinaryExt(norm string) bool {
	lower := strings.ToLower(norm)
	return strings.HasSuffix(lower, ".epf") || strings.HasSuffix(lower, ".erf")
}
