package workspace_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// проектСМанифестом раскладывает две выгрузки (конфигурацию и расширение) и
// кладёт рядом переданный текст манифеста.
func проектСМанифестом(t *testing.T, манифест string) string {
	t.Helper()
	root := t.TempDir()
	записать(t, filepath.Join(root, "cfg", "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))
	записать(t, filepath.Join(root, "ext", "Configuration.xml"), конфигурацияXML("Доработки", "Customization"))
	записать(t, filepath.Join(root, workspace.ManifestFileName), манифест)
	return root
}

// TestLoadManifestНевалидныйДаётActionableОшибку — критерий приёмки: отказ
// называет и файл, и поле. Ожидаемые подстроки — это имена полей из формата
// манифеста (§9), а не то, что вернул код.
func TestLoadManifestНевалидныйДаётActionableОшибку(t *testing.T) {
	tests := []struct {
		name     string
		манифест string
		поле     string
		вТексте  string
	}{
		{
			name: "неподдерживаемая версия",
			манифест: `{"version": 2, "project": "p",
			  "components": [{"id": "cfg", "kind": "configuration", "root": "cfg"}]}`,
			поле:    "version",
			вТексте: "не поддерживается",
		},
		{
			name: "повторяющийся id компонента",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg"},
			  {"id": "cfg", "kind": "extension", "root": "ext", "appliesTo": "cfg", "applyOrder": 1}]}`,
			поле:    "components[1].id",
			вТексте: "уже занят",
		},
		{
			name: "корень компонента не существует",
			манифест: `{"version": 1, "project": "p",
			  "components": [{"id": "cfg", "kind": "configuration", "root": "нет-такого"}]}`,
			поле:    "components[0].root",
			вТексте: "не существует",
		},
		{
			name: "appliesTo указывает в пустоту",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg"},
			  {"id": "ext", "kind": "extension", "root": "ext", "appliesTo": "нет", "applyOrder": 1}]}`,
			поле:    "components[1].appliesTo",
			вТексте: "которого нет в манифесте",
		},
		{
			name: "порядок применения расширений повторяется",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg"},
			  {"id": "ext", "kind": "extension", "root": "ext", "appliesTo": "cfg", "applyOrder": 1},
			  {"id": "ext2", "kind": "extension", "root": "cfg", "appliesTo": "cfg", "applyOrder": 1}]}`,
			поле:    "components[2].applyOrder",
			вТексте: "уже занят",
		},
		{
			name: "объявленный вид не совпал с содержимым каталога",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "ext"}]}`,
			поле:    "components[0].kind",
			вТексте: "extension",
		},
		{
			name: "неизвестный вид компонента",
			манифест: `{"version": 1, "project": "p",
			  "components": [{"id": "cfg", "kind": "выгрузка", "root": "cfg"}]}`,
			поле:    "components[0].kind",
			вТексте: "неизвестен",
		},
		{
			name: "битый шаблон include",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg", "include": ["[a-"]}]}`,
			поле:    "components[0].include[0]",
			вТексте: "некорректен",
		},
		{
			// Ошибка спрятана за **: если валидация подменяет ** на *, она её
			// не увидит, а обход увидит.
			name: "битый шаблон за двойной звёздочкой",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg", "include": ["**/[a-.bsl"]}]}`,
			поле:    "components[0].include[0]",
			вТексте: "некорректен",
		},
		{
			name: "** внутри сегмента",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg", "exclude": ["src**/temp"]}]}`,
			поле:    "components[0].exclude[0]",
			вТексте: "отдельно",
		},
		{
			name: "опечатка в имени поля компонента",
			манифест: `{"version": 1, "project": "p", "components": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg", "aplyOrder": 1}]}`,
			поле:    "components[0].aplyOrder",
			вТексте: "неизвестно",
		},
		{
			name: "опечатка в имени поля верхнего уровня",
			манифест: `{"version": 1, "project": "p", "componets": [
			  {"id": "cfg", "kind": "configuration", "root": "cfg"}]}`,
			поле:    "componets",
			вТексте: "неизвестно",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := проектСМанифестом(t, tc.манифест)

			_, err := workspace.LoadManifest(root)
			if err == nil {
				t.Fatal("манифест принят, ожидался отказ")
			}
			var me *workspace.ManifestError
			if !errors.As(err, &me) {
				t.Fatalf("ошибка %T, ожидался *workspace.ManifestError: %v", err, err)
			}
			if me.Field != tc.поле {
				t.Errorf("поле = %q, ожидалось %q (сообщение: %s)", me.Field, tc.поле, err)
			}
			текст := err.Error()
			if !strings.Contains(текст, workspace.ManifestFileName) {
				t.Errorf("сообщение не называет файл манифеста: %s", текст)
			}
			if !strings.Contains(текст, tc.поле) {
				t.Errorf("сообщение не называет поле %q: %s", tc.поле, текст)
			}
			if !strings.Contains(текст, tc.вТексте) {
				t.Errorf("сообщение не объясняет причину (%q): %s", tc.вТексте, текст)
			}
		})
	}
}

// TestLoadManifestКореньСоседнимКаталогом — корень компонента ЗА пределами
// каталога проекта принимается: реальная раскладка выгрузок разносит
// конфигурацию и её расширения по соседним каталогам («Базы\Торговля» и
// «Расширения\Торговля\РасширениеА»), и подъём через ".." — единственный способ
// собрать их в один проект, не перекладывая выгрузки.
//
// R62 («сервер не выходит за workspace») этим не снимается, а уточняется:
// границу ОБЪЯВЛЯЕТ манифест, а СТЕРЕЖЁТ её SafeJoin — и стережёт от корня
// компонента, а не от корня проекта (см. TestSafeJoin* и вызовы SafeJoin в
// internal/index, internal/app). Манифест — не пользовательский ввод: он
// лежит рядом с проектом и правится руками.
func TestLoadManifestКореньСоседнимКаталогом(t *testing.T) {
	root := проектСМанифестом(t, `{"version": 1, "project": "p",
	  "components": [{"id": "cfg", "kind": "configuration", "root": "../соседняя-выгрузка"}]}`)

	соседний := filepath.Join(filepath.Dir(root), "соседняя-выгрузка")
	записать(t, filepath.Join(соседний, "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))

	m, err := workspace.LoadManifest(root)
	if err != nil {
		t.Fatalf("соседний каталог как корень компонента отклонён: %v", err)
	}
	comp, ok := m.Component("cfg")
	if !ok {
		t.Fatal("компонент cfg не разобран")
	}
	факт, err := filepath.EvalSymlinks(соседний)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", соседний, err)
	}
	if !strings.EqualFold(comp.AbsRoot, факт) {
		t.Errorf("AbsRoot = %s, ожидался %s", comp.AbsRoot, факт)
	}
}

// TestLoadManifestАбсолютныйКореньОтклоняется — переносимость манифеста:
// подъём через ".." разрешён, а абсолютный путь нет, иначе один и тот же
// манифест перестаёт читаться на второй машине.
func TestLoadManifestАбсолютныйКореньОтклоняется(t *testing.T) {
	снаружи := t.TempDir()
	записать(t, filepath.Join(снаружи, "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))

	манифест := fmt.Sprintf(`{"version": 1, "project": "p",
	  "components": [{"id": "cfg", "kind": "configuration", "root": %s}]}`,
		strconv.Quote(filepath.ToSlash(снаружи)))
	root := проектСМанифестом(t, манифест)

	_, err := workspace.LoadManifest(root)
	if err == nil {
		t.Fatal("абсолютный корень компонента принят")
	}
	if !errors.Is(err, workspace.ErrPathOutsideWorkspace) {
		t.Fatalf("ошибка %v, ожидалась ErrPathOutsideWorkspace", err)
	}
}

// TestLoadManifestВложенныйКореньОтклоняется — критерий приёмки: регистрация
// корня внутри уже зарегистрированного отклоняется, и в отказе назван
// конфликтующий компонент.
func TestLoadManifestВложенныйКореньОтклоняется(t *testing.T) {
	root := t.TempDir()
	записать(t, filepath.Join(root, "cfg", "Configuration.xml"), конфигурацияXML("УправлениеТорговлей", ""))
	записать(t, filepath.Join(root, "cfg", "tests", "yaxunit", "ТестОбмена.bsl"), "Процедура Тест() КонецПроцедуры")
	записать(t, filepath.Join(root, workspace.ManifestFileName), `{"version": 1, "project": "ut", "components": [
	  {"id": "cfg-main", "kind": "configuration", "root": "cfg"},
	  {"id": "tests", "kind": "test-sources", "root": "cfg/tests/yaxunit"}]}`)

	_, err := workspace.LoadManifest(root)
	if err == nil {
		t.Fatal("вложенный корень принят, ожидался отказ")
	}
	текст := err.Error()
	if !strings.Contains(текст, "cfg-main") {
		t.Errorf("отказ не называет конфликтующий компонент: %s", текст)
	}
	if !strings.Contains(текст, "components[1].root") {
		t.Errorf("отказ не называет вложенный компонент: %s", текст)
	}

	var me *workspace.ManifestError
	if !errors.As(err, &me) {
		t.Fatalf("ошибка %T, ожидался *workspace.ManifestError: %v", err, err)
	}
	if me.Component != "tests" {
		t.Errorf("Component = %q, ожидался вложенный компонент %q", me.Component, "tests")
	}
}
