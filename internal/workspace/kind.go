package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// ConfigurationFileName — корневой файл XML-выгрузки конфигурации и расширения.
const ConfigurationFileName = "Configuration.xml"

// DetectedKind — что удалось понять о каталоге по его содержимому.
type DetectedKind struct {
	Kind domain.ComponentKind
	// Name — имя из свойств объекта (конфигурации, обработки, отчёта).
	Name string
	// ExtensionPurpose — назначение расширения (ConfigurationExtensionPurpose);
	// непустое ровно у расширений.
	ExtensionPurpose string
}

// DetectKind определяет вид компонента по содержимому каталога:
// configuration/extension — по Configuration.xml (расширение отличается
// непустым ConfigurationExtensionPurpose), EPF/ERF — по корневому XML
// разобранной обработки или отчёта. Виды test-sources и standalone-bsl по
// файлам не определяются: они существуют только объявлением в манифесте.
//
// Здесь читаются ровно два поля-дискриминатора, а не метаданные: разбор
// метаданных принадлежит internal/parse/meta, типизация компонентов —
// workspace.
func DetectKind(dir string) (DetectedKind, error) {
	cfgPath := filepath.Join(dir, ConfigurationFileName)
	if fi, err := os.Stat(cfgPath); err == nil && !fi.IsDir() {
		object, name, extPurpose, err := readRootObject(cfgPath)
		if err != nil {
			return DetectedKind{}, err
		}
		if object != "Configuration" {
			return DetectedKind{}, fmt.Errorf("%s: корневой объект %q, ожидался Configuration", cfgPath, object)
		}
		kind := domain.KindConfiguration
		if extPurpose != "" {
			kind = domain.KindExtension
		}
		return DetectedKind{Kind: kind, Name: name, ExtensionPurpose: extPurpose}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return DetectedKind{}, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		// SkipFile отсекает то, что не читается никогда: бинарные .epf/.erf/.cf
		// и файлы окружения агента. Разобранные исходники обработки — это XML,
		// собранный двоичный файл источником не бывает.
		if e.IsDir() || SkipFile(e.Name()) || !strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		object, objName, _, err := readRootObject(filepath.Join(dir, name))
		if err != nil {
			continue // не наш XML, соседние файлы это не отменяет
		}
		switch object {
		case "ExternalDataProcessor":
			return DetectedKind{Kind: domain.KindExternalDataProcessor, Name: objName}, nil
		case "ExternalReport":
			return DetectedKind{Kind: domain.KindExternalReport, Name: objName}, nil
		}
	}

	return DetectedKind{}, fmt.Errorf("в каталоге %s нет ни %s, ни корневого XML внешней обработки или отчёта", dir, ConfigurationFileName)
}

// maxRootXMLScan — потолок чтения корневого XML с диска. Configuration.xml
// реальной конфигурации весит мегабайты (перечень всех объектов), а нужные
// поля лежат в начале, в Properties: читать с диска больше этого предела не
// нужно даже до того, как разбор XML это заметит.
const maxRootXMLScan = 4 << 20

// readRootObject достаёт из XML-файла вид корневого объекта MetaDataObject и
// его ключевые свойства (имя, назначение расширения), не читая файл целиком.
// Чтение с диска ограничено maxRootXMLScan; сам разбор байтов делает
// meta.DetectRoot — единственная точка чтения этого XML во всём новом коде
// (архитектура §6), workspace здесь только открывает файл.
func readRootObject(path string) (object, name, extensionPurpose string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxRootXMLScan))
	if err != nil {
		return "", "", "", err
	}

	object, name, extensionPurpose, err = meta.DetectRoot(data)
	if err != nil {
		return "", "", "", fmt.Errorf("разбор %s: %w", path, err)
	}
	return object, name, extensionPurpose, nil
}
