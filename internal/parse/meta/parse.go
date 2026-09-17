package meta

import (
	"encoding/xml"
	"fmt"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Коды диагностик пакета.
const (
	// DiagXMLInvalid — файл не разобрался как XML вовсе.
	DiagXMLInvalid = "meta_xml_invalid"
	// DiagEmptyName — объект/подписка/задание без имени: подозрительно, но не
	// повод обрывать разбор остального файла.
	DiagEmptyName = "meta_empty_name"
)

// ParseFile разбирает один файл XML-выгрузки. Чистая функция: не читает диск,
// работает только с переданными байтами. Вид файла определяется Classify по
// relPath; при неизвестном/неподдерживаемом виде возвращает пустые Facts без
// диагностик — это осознанный пропуск (KindUnsupported, KindBinary), а не
// дефект разбора.
func ParseFile(relPath string, src []byte) (Facts, []domain.Diagnostic) {
	switch Classify(relPath) {
	case KindMetadataObject:
		return parseMetadataObject(relPath, src)
	case KindEventSubscription:
		return parseSubscription(relPath, src)
	case KindFormStructure:
		return parseFormStructure(relPath, src)
	case KindRoleRights:
		return parseRoleRights(relPath, src)
	case KindPredefinedData:
		return parsePredefinedData(relPath, src)
	default:
		// KindConfigurationRoot: свойства конфигурации/расширения (имя,
		// версия, назначение) — за пределами этого таска (метаданные
		// компонента уже несёт workspace.Manifest); KindUnsupported/KindBinary:
		// осознанный пропуск.
		return Facts{}, nil
	}
}

// unmarshalXML снимает BOM и разбирает src в v, оборачивая ошибку в
// diagnostic вместо возврата error — у ParseFile нет канала ошибки, есть
// только диагностики.
func unmarshalXML(relPath string, src []byte, v any) []domain.Diagnostic {
	data := stripBOM(src)
	if err := xml.Unmarshal(data, v); err != nil {
		return []domain.Diagnostic{{
			Code:     DiagXMLInvalid,
			Severity: domain.SeverityError,
			Message:  fmt.Sprintf("не удалось разобрать XML: %v", err),
			File:     relPath,
		}}
	}
	return nil
}
