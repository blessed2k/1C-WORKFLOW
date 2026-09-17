package meta

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// DetectRoot читает вид корневого объекта MetaDataObject (Configuration,
// ExternalDataProcessor, ExternalReport, ...), его имя (Properties/Name) и
// назначение расширения (Properties/ConfigurationExtensionPurpose) из сырых
// байтов XML-выгрузки, не разбирая файл целиком: поток обрывается сразу
// после конца Properties, дальше в Configuration.xml реальной конфигурации
// идёт перечень всех объектов весом в мегабайты, который классификации
// каталога не нужен.
//
// Единственный потребитель сегодня — workspace.DetectKind: классификация
// каталога компонента (конфигурация/расширение/EPF/ERF) читает только эти
// два поля-дискриминатора, полный разбор Configuration.xml через ParseFile
// для этого не нужен и был бы на порядки дороже.
func DetectRoot(src []byte) (object, name, extensionPurpose string, err error) {
	data := stripBOM(src)

	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	var field string
	// Перед Properties у объекта идёт InternalInfo со своими вложенными
	// элементами; читать поля можно только внутри Properties, иначе чужой
	// элемент с подходящим именем подменит имя объекта.
	inProperties := false
	for {
		tok, tokErr := dec.Token()
		if tokErr == io.EOF {
			break
		}
		if tokErr != nil {
			return "", "", "", fmt.Errorf("разбор корневого XML: %w", tokErr)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch depth {
			case 1:
				if t.Name.Local != "MetaDataObject" {
					return "", "", "", fmt.Errorf("корневой элемент %q, ожидался MetaDataObject", t.Name.Local)
				}
			case 2:
				object = t.Name.Local
			case 3:
				inProperties = t.Name.Local == "Properties"
			case 4:
				if inProperties && (t.Name.Local == "Name" || t.Name.Local == "ConfigurationExtensionPurpose") {
					field = t.Name.Local
				}
			}
		case xml.CharData:
			switch field {
			case "Name":
				name += string(t)
			case "ConfigurationExtensionPurpose":
				extensionPurpose += string(t)
			}
		case xml.EndElement:
			field = ""
			if depth == 3 && t.Name.Local == "Properties" {
				// Свойства кончились: всё, что нужно, уже прочитано.
				return object, name, extensionPurpose, nil
			}
			depth--
		}
	}
	if object == "" {
		return "", "", "", fmt.Errorf("корневой объект не найден")
	}
	return object, name, extensionPurpose, nil
}
