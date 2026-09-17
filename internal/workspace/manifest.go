package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// ManifestFileName — имя файла манифеста логического проекта. Лежит в корне
// проекта и версионируется в Git (архитектура §9).
const ManifestFileName = "1c-project.json"

// ManifestVersion — единственная поддерживаемая версия формата манифеста.
const ManifestVersion = 1

// Manifest — разобранный и проверенный манифест проекта.
type Manifest struct {
	Version     int              `json:"version"`
	Project     domain.ProjectID `json:"project"`
	DisplayName string           `json:"displayName,omitempty"`
	Components  []Component      `json:"components"`
	Exclude     []string         `json:"exclude,omitempty"`

	// Root — канонический абсолютный каталог проекта (где лежит манифест).
	Root string `json:"-"`
	// Path — канонический абсолютный путь файла манифеста.
	Path string `json:"-"`
}

// Component — типизированный корень внутри проекта (архитектура §8).
type Component struct {
	ID                domain.ComponentID   `json:"id"`
	Kind              domain.ComponentKind `json:"kind"`
	Root              string               `json:"root"`
	AppliesTo         domain.ComponentID   `json:"appliesTo,omitempty"`
	ApplyOrder        int                  `json:"applyOrder,omitempty"`
	UsesConfiguration domain.ComponentID   `json:"usesConfiguration,omitempty"`
	Include           []string             `json:"include,omitempty"`
	Exclude           []string             `json:"exclude,omitempty"`

	// AbsRoot — канонический абсолютный путь корня компонента.
	AbsRoot string `json:"-"`
	// DetectedName — имя объекта из выгрузки, если вид определяется по файлам.
	DetectedName string `json:"-"`
	// ExtensionPurpose — назначение расширения из Configuration.xml.
	ExtensionPurpose string `json:"-"`
}

// Layer возвращает слой компонента: базовый у всего, кроме расширений.
func (c Component) Layer() domain.Layer {
	if c.Kind == domain.KindExtension {
		return domain.ExtensionLayer(c.ID, c.ApplyOrder)
	}
	return domain.BaseLayer(c.ID)
}

// Component возвращает компонент по идентификатору.
func (m Manifest) Component(id domain.ComponentID) (Component, bool) {
	for _, c := range m.Components {
		if c.ID == id {
			return c, true
		}
	}
	return Component{}, false
}

// Configuration возвращает основную конфигурацию проекта, если она объявлена.
func (m Manifest) Configuration() (Component, bool) {
	for _, c := range m.Components {
		if c.Kind == domain.KindConfiguration {
			return c, true
		}
	}
	return Component{}, false
}

// Extensions возвращает расширения в порядке применения (applyOrder).
func (m Manifest) Extensions() []Component {
	var out []Component
	for _, c := range m.Components {
		if c.Kind == domain.KindExtension {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ApplyOrder < out[j].ApplyOrder })
	return out
}

// ManifestError — отказ в разборе манифеста. Называет файл и поле, чтобы по
// сообщению было видно, что править.
type ManifestError struct {
	File      string
	Field     string
	Component domain.ComponentID
	Message   string
	Err       error
}

// Error возвращает сообщение вида «файл: поле: что не так».
func (e *ManifestError) Error() string {
	var b strings.Builder
	b.WriteString(e.File)
	if e.Field != "" {
		b.WriteString(": ")
		b.WriteString(e.Field)
	}
	b.WriteString(": ")
	b.WriteString(e.Message)
	return b.String()
}

// Unwrap отдаёт исходную ошибку, если отказ произошёл поверх неё.
func (e *ManifestError) Unwrap() error { return e.Err }

// Diagnostic переводит отказ в диагностику индекса.
func (e *ManifestError) Diagnostic() domain.Diagnostic {
	code := domain.DiagManifestInvalid
	if errors.Is(e.Err, ErrPathOutsideWorkspace) {
		code = domain.DiagPathOutsideWorkspace
	}
	return domain.Diagnostic{
		Code:      code,
		Severity:  domain.SeverityError,
		Message:   e.Message,
		Component: e.Component,
		File:      e.File,
	}
}

// LoadManifest читает и валидирует 1c-project.json в каталоге root.
//
// Валидируются: версия, идентификаторы, уникальность id, существование и
// вложенность корней, соответствие объявленного вида содержимому каталога,
// ссылки appliesTo и usesConfiguration, порядок применения расширений и
// шаблоны include/exclude. Любой отказ — *ManifestError с именем файла и поля.
func LoadManifest(root string) (Manifest, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Manifest{}, &ManifestError{File: root, Message: "каталог проекта не приводится к абсолютному пути", Err: err}
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return Manifest{}, &ManifestError{File: absRoot, Message: "каталог проекта не читается", Err: err}
	}
	file := filepath.Join(realRoot, ManifestFileName)

	data, err := os.ReadFile(file)
	if err != nil {
		return Manifest{}, &ManifestError{File: file, Message: "манифест проекта не прочитан: " + err.Error(), Err: err}
	}

	if err := checkUnknownFields(file, data); err != nil {
		return Manifest{}, err
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, &ManifestError{File: file, Message: "JSON не разобран: " + err.Error(), Err: err}
	}
	m.Root = realRoot
	m.Path = file

	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// knownManifestFields — поля верхнего уровня манифеста.
var knownManifestFields = map[string]bool{
	"version": true, "project": true, "displayName": true, "components": true, "exclude": true,
}

// knownComponentFields — поля описания компонента.
var knownComponentFields = map[string]bool{
	"id": true, "kind": true, "root": true, "appliesTo": true, "applyOrder": true,
	"usesConfiguration": true, "include": true, "exclude": true,
}

// checkUnknownFields ловит опечатки в именах полей до разбора: молча
// проигнорированное "aplyOrder" означало бы расширение, применённое не в том
// порядке, без единого сообщения. Проверка своя, а не DisallowUnknownFields,
// потому что клиенту нужно имя поля в структуре манифеста и русский текст.
func checkUnknownFields(file string, data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return &ManifestError{File: file, Message: "JSON не разобран: " + err.Error(), Err: err}
	}
	for _, key := range sortedKeys(root) {
		if !knownManifestFields[key] {
			return &ManifestError{File: file, Field: key,
				Message: fmt.Sprintf("поле %q неизвестно, в манифесте бывают %s", key, fieldList(knownManifestFields))}
		}
	}

	raw, ok := root["components"]
	if !ok {
		return nil
	}
	var components []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &components); err != nil {
		return &ManifestError{File: file, Field: "components",
			Message: "components разбирается как список описаний компонентов: " + err.Error(), Err: err}
	}
	for i, c := range components {
		for _, key := range sortedKeys(c) {
			if !knownComponentFields[key] {
				return &ManifestError{File: file, Field: fmt.Sprintf("components[%d].%s", i, key),
					Message: fmt.Sprintf("поле %q неизвестно, у компонента бывают %s", key, fieldList(knownComponentFields))}
			}
		}
	}
	return nil
}

// sortedKeys возвращает ключи в порядке, не зависящем от обхода map: иначе
// один и тот же битый манифест давал бы разные сообщения.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fieldList перечисляет допустимые поля для сообщения об ошибке.
func fieldList(set map[string]bool) string {
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// validate проверяет манифест целиком и возвращает первый отказ.
func (m *Manifest) validate() error {
	fail := func(field string, comp domain.ComponentID, format string, args ...any) *ManifestError {
		return &ManifestError{File: m.Path, Field: field, Component: comp, Message: fmt.Sprintf(format, args...)}
	}

	if m.Version != ManifestVersion {
		return fail("version", "", "версия %d не поддерживается, ожидалась %d", m.Version, ManifestVersion)
	}
	if err := m.Project.Validate(); err != nil {
		return fail("project", "", "%s", err)
	}
	if len(m.Components) == 0 {
		return fail("components", "", "в проекте нет ни одного компонента")
	}
	for i, pattern := range m.Exclude {
		if err := validateGlob(pattern); err != nil {
			return fail(fmt.Sprintf("exclude[%d]", i), "", "%s", err)
		}
	}

	видимые := make(map[domain.ComponentID]int, len(m.Components))
	порядок := make(map[int]domain.ComponentID)

	for i := range m.Components {
		c := &m.Components[i]
		поле := func(name string) string { return fmt.Sprintf("components[%d].%s", i, name) }

		if err := c.ID.Validate(); err != nil {
			return fail(поле("id"), c.ID, "%s", err)
		}
		if prev, ok := видимые[c.ID]; ok {
			return fail(поле("id"), c.ID, "идентификатор %q уже занят компонентом components[%d]", c.ID, prev)
		}
		видимые[c.ID] = i

		if !c.Kind.Valid() {
			return fail(поле("kind"), c.ID, "вид %q неизвестен, допустимы %s", c.Kind, kindList())
		}
		if c.Root == "" {
			return fail(поле("root"), c.ID, "корень компонента не задан")
		}

		abs, err := resolveComponentRoot(m.Root, c.Root)
		if err != nil {
			return &ManifestError{File: m.Path, Field: поле("root"), Component: c.ID,
				Message: fmt.Sprintf("корень %q не разрешается: %s", c.Root, err), Err: err}
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return &ManifestError{File: m.Path, Field: поле("root"), Component: c.ID,
				Message: fmt.Sprintf("корень %q не существует (ожидался каталог %s)", c.Root, abs), Err: err}
		}
		if !fi.IsDir() {
			return fail(поле("root"), c.ID, "корень %q — файл, а нужен каталог", c.Root)
		}
		c.AbsRoot = abs

		for j, pattern := range c.Include {
			if err := validateGlob(pattern); err != nil {
				return fail(поле(fmt.Sprintf("include[%d]", j)), c.ID, "%s", err)
			}
		}
		for j, pattern := range c.Exclude {
			if err := validateGlob(pattern); err != nil {
				return fail(поле(fmt.Sprintf("exclude[%d]", j)), c.ID, "%s", err)
			}
		}

		if c.Kind == domain.KindExtension {
			if c.AppliesTo == "" {
				return fail(поле("appliesTo"), c.ID, "расширение обязано указывать конфигурацию, к которой применяется")
			}
			if c.ApplyOrder <= 0 {
				return fail(поле("applyOrder"), c.ID, "порядок применения %d: у расширения он начинается с 1", c.ApplyOrder)
			}
			if другой, ok := порядок[c.ApplyOrder]; ok {
				return fail(поле("applyOrder"), c.ID, "порядок применения %d уже занят расширением %q: порядок слоёв обязан быть однозначным", c.ApplyOrder, другой)
			}
			порядок[c.ApplyOrder] = c.ID
		} else {
			if c.AppliesTo != "" {
				return fail(поле("appliesTo"), c.ID, "поле имеет смысл только у расширения, а вид компонента — %q", c.Kind)
			}
			if c.ApplyOrder != 0 {
				return fail(поле("applyOrder"), c.ID, "поле имеет смысл только у расширения, а вид компонента — %q", c.Kind)
			}
		}

		// Вид, объявленный в манифесте, обязан совпадать с содержимым каталога.
		if c.Kind.DetectableFromFiles() {
			detected, err := DetectKind(abs)
			if err != nil {
				return &ManifestError{File: m.Path, Field: поле("kind"), Component: c.ID,
					Message: fmt.Sprintf("вид объявлен как %q, но каталог %s не похож на выгрузку: %s", c.Kind, c.Root, err), Err: err}
			}
			if detected.Kind != c.Kind {
				return fail(поле("kind"), c.ID, "вид объявлен как %q, а каталог %s содержит %q", c.Kind, c.Root, detected.Kind)
			}
			c.DetectedName = detected.Name
			c.ExtensionPurpose = detected.ExtensionPurpose
		}
	}

	// Ссылки между компонентами проверяются, когда известны все идентификаторы.
	for i, c := range m.Components {
		поле := func(name string) string { return fmt.Sprintf("components[%d].%s", i, name) }
		ссылки := []struct {
			имяПоля string
			ссылка  domain.ComponentID
		}{
			{"appliesTo", c.AppliesTo},
			{"usesConfiguration", c.UsesConfiguration},
		}
		for _, r := range ссылки {
			имяПоля, ссылка := r.имяПоля, r.ссылка
			if ссылка == "" {
				continue
			}
			j, ok := видимые[ссылка]
			if !ok {
				return fail(поле(имяПоля), c.ID, "ссылка на компонент %q, которого нет в манифесте", ссылка)
			}
			if m.Components[j].Kind != domain.KindConfiguration {
				return fail(поле(имяПоля), c.ID, "ссылка на компонент %q вида %q, а нужна конфигурация", ссылка, m.Components[j].Kind)
			}
			if ссылка == c.ID {
				return fail(поле(имяПоля), c.ID, "компонент ссылается сам на себя")
			}
		}
	}

	return m.validateRoots()
}

// validateRoots отбивает вложенные и повторяющиеся корни: индексировать один и
// тот же файл под двумя компонентами нельзя — факты получат разный
// component_id и разойдутся.
func (m *Manifest) validateRoots() error {
	for i, c := range m.Components {
		for j, other := range m.Components {
			if i == j {
				continue
			}
			if equalPath(c.AbsRoot, other.AbsRoot) {
				if i > j {
					continue // о совпадении достаточно сказать один раз
				}
				return &ManifestError{File: m.Path, Field: fmt.Sprintf("components[%d].root", j), Component: other.ID,
					Message: fmt.Sprintf("корень %q совпадает с корнем компонента %q: один каталог не может принадлежать двум компонентам", other.Root, c.ID)}
			}
			if inside(c.AbsRoot, other.AbsRoot) {
				return &ManifestError{File: m.Path, Field: fmt.Sprintf("components[%d].root", j), Component: other.ID,
					Message: fmt.Sprintf("корень %q вложен в корень компонента %q (%s): вложенные корни в одном проекте не регистрируются", other.Root, c.ID, c.Root)}
			}
		}
	}
	return nil
}

// validateGlob проверяет шаблон include/exclude тем же матчером, который
// применит его при обходе: разойтись валидация и обход не могут.
func validateGlob(pattern string) error {
	_, err := MatchPath(pattern, "CommonModules/Проверка/Ext/Module.bsl")
	return err
}

// kindList перечисляет допустимые виды компонентов для сообщения об ошибке.
func kindList() string {
	kinds := domain.ComponentKinds()
	names := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}
