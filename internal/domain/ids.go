// Package domain описывает сущности и инварианты индекса 1С: идентичность,
// spans, символы, ссылки, разрешение имён, происхождение фактов и поколения.
//
// Пакет не зависит ни от чего, кроме стандартной библиотеки: это граница,
// закреплённая тестом. Ни SQL, ни MCP SDK, ни разбор файлов здесь не живут.
package domain

import (
	"fmt"
	"regexp"
)

// ProjectID — идентификатор логического проекта 1С (слаг из манифеста).
type ProjectID string

// ComponentID — идентификатор компонента внутри проекта (слаг из манифеста).
type ComponentID string

// slugRE — допустимая форма идентификатора проекта и компонента: строчная
// латиница, цифры, дефис, подчёркивание и точка; начинается с буквы или цифры.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// maxSlugLen — потолок длины идентификатора: он попадает в имена файлов индекса.
const maxSlugLen = 64

// ValidateSlug проверяет форму идентификатора и объясняет отказ.
func ValidateSlug(s string) error {
	if s == "" {
		return fmt.Errorf("идентификатор пуст")
	}
	if len(s) > maxSlugLen {
		return fmt.Errorf("идентификатор %q длиннее %d символов", s, maxSlugLen)
	}
	if !slugRE.MatchString(s) {
		return fmt.Errorf("идентификатор %q: допустимы строчная латиница, цифры, %q, %q и %q, первым идёт буква или цифра", s, "-", "_", ".")
	}
	return nil
}

// Validate проверяет идентификатор проекта.
func (p ProjectID) Validate() error {
	if err := ValidateSlug(string(p)); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	return nil
}

// Validate проверяет идентификатор компонента.
func (c ComponentID) Validate() error {
	if err := ValidateSlug(string(c)); err != nil {
		return fmt.Errorf("id компонента: %w", err)
	}
	return nil
}

// ComponentKind — вид типизированного корня проекта (архитектура §8).
type ComponentKind string

const (
	// KindConfiguration — основная конфигурация (XML-выгрузка с Configuration.xml).
	KindConfiguration ComponentKind = "configuration"
	// KindExtension — расширение конфигурации (Configuration.xml с ConfigurationExtensionPurpose).
	KindExtension ComponentKind = "extension"
	// KindExternalDataProcessor — исходники внешней обработки (EPF).
	KindExternalDataProcessor ComponentKind = "external-data-processor"
	// KindExternalReport — исходники внешнего отчёта (ERF).
	KindExternalReport ComponentKind = "external-report"
	// KindTestSources — папка тестов (YAxUnit и прочие), только объявлением.
	KindTestSources ComponentKind = "test-sources"
	// KindStandaloneBSL — отдельные BSL-файлы вне выгрузки, только объявлением.
	KindStandaloneBSL ComponentKind = "standalone-bsl"
)

// ComponentKinds перечисляет виды компонентов v1-модели в порядке архитектуры §8.
func ComponentKinds() []ComponentKind {
	return []ComponentKind{
		KindConfiguration,
		KindExtension,
		KindExternalDataProcessor,
		KindExternalReport,
		KindTestSources,
		KindStandaloneBSL,
	}
}

// Valid сообщает, известен ли вид компонента.
func (k ComponentKind) Valid() bool {
	for _, known := range ComponentKinds() {
		if k == known {
			return true
		}
	}
	return false
}

// DetectableFromFiles сообщает, определяется ли вид компонента по содержимому
// каталога. test-sources и standalone-bsl определяются только объявлением.
func (k ComponentKind) DetectableFromFiles() bool {
	switch k {
	case KindConfiguration, KindExtension, KindExternalDataProcessor, KindExternalReport:
		return true
	default:
		return false
	}
}

// View — вид выдачи по слоям расширений (архитектура §20).
type View string

const (
	// ViewRaw — факты как они лежат в своём слое, без слияния.
	ViewRaw View = "raw"
	// ViewEffective — слои слиты по applyOrder, каждый факт несёт слой-источник.
	ViewEffective View = "effective"
)

// Valid сообщает, известен ли вид выдачи.
func (v View) Valid() bool { return v == ViewRaw || v == ViewEffective }

// Layer — слой, которому принадлежит факт: базовая конфигурация или расширение
// с его порядком применения. ApplyOrder = 0 означает базовый слой.
type Layer struct {
	Component  ComponentID `json:"component"`
	ApplyOrder int         `json:"applyOrder,omitempty"`
}

// BaseLayer возвращает базовый слой компонента (конфигурация, EPF, тесты).
func BaseLayer(c ComponentID) Layer { return Layer{Component: c} }

// ExtensionLayer возвращает слой расширения с его порядком применения.
func ExtensionLayer(c ComponentID, applyOrder int) Layer {
	return Layer{Component: c, ApplyOrder: applyOrder}
}

// IsBase сообщает, базовый ли это слой.
func (l Layer) IsBase() bool { return l.ApplyOrder == 0 }
