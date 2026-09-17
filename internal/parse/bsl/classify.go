package bsl

import (
	"path"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// ModuleKind — вид модуля BSL. Выводится из пути файла в выгрузке: платформа
// кладёт каждый вид модуля в фиксированное место, и это единственный источник
// вида, доступный самому парсеру.
type ModuleKind string

const (
	// ModuleUnknown — путь не задан или не опознан.
	ModuleUnknown ModuleKind = ""
	// ModuleCommon — общий модуль: CommonModules/X/Ext/Module.bsl.
	ModuleCommon ModuleKind = "common"
	// ModuleManager — модуль менеджера объекта.
	ModuleManager ModuleKind = "manager"
	// ModuleObject — модуль объекта.
	ModuleObject ModuleKind = "object"
	// ModuleRecordSet — модуль набора записей регистра.
	ModuleRecordSet ModuleKind = "record-set"
	// ModuleValueManager — модуль менеджера значения константы.
	ModuleValueManager ModuleKind = "value-manager"
	// ModuleForm — модуль формы.
	ModuleForm ModuleKind = "form"
	// ModuleCommand — модуль команды.
	ModuleCommand ModuleKind = "command"
	// ModuleApplication — модуль управляемого или обычного приложения.
	ModuleApplication ModuleKind = "application"
	// ModuleSession — модуль сеанса.
	ModuleSession ModuleKind = "session"
	// ModuleExternalConnection — модуль внешнего соединения.
	ModuleExternalConnection ModuleKind = "external-connection"
	// ModuleService — модуль web- или HTTP-сервиса.
	ModuleService ModuleKind = "service"
)

// ModuleInfo — вид модуля и его владелец, выведенные из пути файла.
// Резолверу (§19.1) нужны обе величины: вид задаёт контекст исполнения,
// владелец — объект метаданных, которому принадлежат экспортные символы.
type ModuleInfo struct {
	Path          string // путь как передан в Options.File, со слешами
	Kind          ModuleKind
	OwnerType     string // тип владельца в терминах выгрузки: Catalogs, Documents, CommonModules
	OwnerName     string // имя владельца
	OwnerNameNorm string
	FormName      string // имя формы для модуля формы
	FormNameNorm  string
}

// moduleFileKinds — имя файла модуля и вид, который оно задаёт.
// Module.bsl уточняется по владельцу: общий модуль, модуль формы или сервиса.
var moduleFileKinds = map[string]ModuleKind{
	"module.bsl":                    ModuleCommon,
	"managermodule.bsl":             ModuleManager,
	"objectmodule.bsl":              ModuleObject,
	"recordsetmodule.bsl":           ModuleRecordSet,
	"valuemanagermodule.bsl":        ModuleValueManager,
	"commandmodule.bsl":             ModuleCommand,
	"managedapplicationmodule.bsl":  ModuleApplication,
	"ordinaryapplicationmodule.bsl": ModuleApplication,
	"applicationmodule.bsl":         ModuleApplication,
	"sessionmodule.bsl":             ModuleSession,
	"externalconnectionmodule.bsl":  ModuleExternalConnection,
}

// serviceOwners — типы владельцев, у которых Ext/Module.bsl это модуль сервиса,
// а не общий модуль.
var serviceOwners = map[string]bool{
	"webservices":         true,
	"httpservices":        true,
	"integrationservices": true,
}

// ClassifyModule выводит вид модуля и владельца из пути файла относительно
// корня компонента. Путь принимается и со слешами, и с обратными слешами.
func ClassifyModule(relPath string) ModuleInfo {
	info := ModuleInfo{Path: relPath}
	if strings.TrimSpace(relPath) == "" {
		return info
	}
	clean := strings.ReplaceAll(relPath, "\\", "/")
	clean = strings.TrimPrefix(path.Clean(clean), "./")
	parts := strings.Split(clean, "/")
	kind, known := moduleFileKinds[strings.ToLower(parts[len(parts)-1])]
	if !known {
		return info
	}
	info.Kind = kind

	// Владелец: первый сегмент пути — каталог коллекции выгрузки.
	// Всё прочее (внешняя обработка, отдельный BSL) владельца не имеет.
	if len(parts) >= 3 {
		if _, known := domain.MetaKindByDumpDirFold(parts[0]); known {
			info.OwnerType = parts[0]
			info.OwnerName = parts[1]
		}
	}

	// Модуль формы: предпоследний каталог — Form.
	if kind == ModuleCommon && len(parts) >= 2 && strings.EqualFold(parts[len(parts)-2], "Form") {
		info.Kind = ModuleForm
		info.FormName = formName(parts)
	}
	if info.Kind == ModuleCommon && serviceOwners[strings.ToLower(info.OwnerType)] {
		info.Kind = ModuleService
	}

	info.OwnerNameNorm = domain.NormalizeName(info.OwnerName)
	info.FormNameNorm = domain.NormalizeName(info.FormName)
	return info
}

// formName достаёт имя формы: сегмент перед последним «Ext». Для формы объекта
// это Forms/<Имя>/Ext/Form/Module.bsl, для общей — CommonForms/<Имя>/Ext/Form/Module.bsl.
func formName(parts []string) string {
	for i := len(parts) - 1; i >= 1; i-- {
		if strings.EqualFold(parts[i], "Ext") {
			return parts[i-1]
		}
	}
	return ""
}
