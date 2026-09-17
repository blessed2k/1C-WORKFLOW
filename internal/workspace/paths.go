// Package workspace описывает логический проект 1С на диске: манифест
// 1c-project.json, локальный реестр .mcp1c/registry.json, автопоиск кандидатов
// и единственную функцию валидации путей.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// ErrPathOutsideWorkspace — путь ведёт за пределы зарегистрированного корня.
// Код ошибки для клиента — domain.DiagPathOutsideWorkspace.
var ErrPathOutsideWorkspace = errors.New(domain.DiagPathOutsideWorkspace)

// PathError объясняет, какой именно путь и почему отклонён.
type PathError struct {
	Root   string
	Rel    string
	Reason string
}

// Error возвращает сообщение, по которому видно, что делать.
func (e *PathError) Error() string {
	return fmt.Sprintf("%s: %q относительно корня %q: %s", domain.DiagPathOutsideWorkspace, e.Rel, e.Root, e.Reason)
}

// Unwrap связывает ошибку с ErrPathOutsideWorkspace для errors.Is.
func (e *PathError) Unwrap() error { return ErrPathOutsideWorkspace }

// SafeJoin — единственная функция валидации путей во всём индексном коде.
// Она приводит корень и относительный путь к канонической форме, раскрывает
// симлинки и отвечает абсолютным путём только тогда, когда результат физически
// лежит внутри корня.
//
// Отклоняются: абсолютный путь, путь с томом или UNC, подъём через ".." и
// симлинк, ведущий наружу. Путь может ещё не существовать — тогда проверяется
// ближайший существующий предок: файл, который предстоит создать, обязан
// оказаться внутри того же корня.
func SafeJoin(root, rel string) (string, error) {
	if root == "" {
		return "", &PathError{Root: root, Rel: rel, Reason: "корень не задан"}
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", &PathError{Root: root, Rel: rel, Reason: "путь абсолютный, а нужен относительный корню компонента"}
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", &PathError{Root: root, Rel: rel, Reason: "корень не приводится к абсолютному пути: " + err.Error()}
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", &PathError{Root: root, Rel: rel, Reason: "корень не читается: " + err.Error()}
	}

	// filepath.Join сам чистит путь, но подъём выше корня надо отбить до того,
	// как он схлопнется в осмысленный путь снаружи.
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", &PathError{Root: root, Rel: rel, Reason: `путь поднимается выше корня через ".."`}
	}

	joined := filepath.Join(realRoot, clean)
	real, err := resolveExisting(joined)
	if err != nil {
		return "", &PathError{Root: root, Rel: rel, Reason: "путь не разрешается: " + err.Error()}
	}
	if !inside(realRoot, real) {
		return "", &PathError{Root: root, Rel: rel, Reason: "разрешённый путь " + real + " лежит вне корня (симлинк наружу или подъём)"}
	}
	return real, nil
}

// resolveComponentRoot приводит объявленный в манифесте корень компонента к
// каноническому абсолютному пути.
//
// Отличие от SafeJoin ровно одно и оно намеренное: подъём через ".." здесь
// РАЗРЕШЁН. SafeJoin стережёт границу песочницы для путей, приходящих из
// запросов и с диска, а корень компонента — часть манифеста: файл лежит рядом
// с проектом, правится руками и версионируется. Реальная раскладка выгрузок
// разносит конфигурацию и её расширения по соседним каталогам («Базы\Торговля» и
// «Расширения\Торговля\РасширениеА»), и требование «всё внутри корня проекта»
// означало бы либо перекладывание выгрузок, либо один-единственный проект на
// весь каталог верхнего уровня.
//
// Что остаётся запрещено: абсолютный путь, том и UNC. Манифест обязан
// оставаться переносимым между машинами — на маке и на рабочей машине дерево
// одинаковой формы, но с разными корнями.
//
// Сама песочница от этого не слабеет: каждый компонент дальше выступает
// СВОИМ корнем для SafeJoin (internal/index/pipeline.go, discover.go,
// internal/app/symbol.go — все зовут SafeJoin от Component.AbsRoot, ни один
// не от Manifest.Root), а validate манифеста по-прежнему запрещает
// совпадающие и вложенные друг в друга корни компонентов.
func resolveComponentRoot(projectRoot, rel string) (string, error) {
	if projectRoot == "" {
		return "", &PathError{Root: projectRoot, Rel: rel, Reason: "корень проекта не задан"}
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", &PathError{Root: projectRoot, Rel: rel,
			Reason: "путь абсолютный, а корень компонента задаётся относительно каталога манифеста (иначе манифест перестаёт быть переносимым)"}
	}

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", &PathError{Root: projectRoot, Rel: rel, Reason: "корень проекта не приводится к абсолютному пути: " + err.Error()}
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", &PathError{Root: projectRoot, Rel: rel, Reason: "корень проекта не читается: " + err.Error()}
	}

	real, err := resolveExisting(filepath.Join(realRoot, filepath.FromSlash(rel)))
	if err != nil {
		return "", &PathError{Root: projectRoot, Rel: rel, Reason: "путь не разрешается: " + err.Error()}
	}
	return real, nil
}

// resolveExisting раскрывает симлинки в пути, часть которого может ещё не
// существовать: раскрывается ближайший существующий предок, недостающий хвост
// приписывается к нему.
func resolveExisting(path string) (string, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	parent := filepath.Dir(path)
	if parent == path {
		return "", fmt.Errorf("не найден ни один существующий предок пути %q", path)
	}
	realParent, err := resolveExisting(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(realParent, filepath.Base(path)), nil
}

// inside сообщает, лежит ли path внутри root (или совпадает с ним).
// На Windows регистр в путях не значим, поэтому сравнение там нечувствительное.
func inside(root, path string) bool {
	if equalPath(root, path) {
		return true
	}
	if runtime.GOOS == "windows" {
		root, path = strings.ToLower(root), strings.ToLower(path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// equalPath сравнивает пути с учётом регистрозависимости платформы.
func equalPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
