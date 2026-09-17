package workspace

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// MatchPath сообщает, подходит ли путь rel под шаблон include/exclude из
// манифеста. Это единственный матчер шаблонов манифеста: им же шаблоны и
// проверяются на корректность при загрузке, чтобы валидация не расходилась с
// тем, что произойдёт при обходе.
//
// Правила: разделитель — прямой слеш; сегмент `**` совпадает с любым числом
// сегментов, включая ноль; остальные сегменты сравниваются path.Match, то есть
// `*` не переходит через слеш.
func MatchPath(pattern, rel string) (bool, error) {
	segs, err := splitPattern(pattern)
	if err != nil {
		return false, err
	}
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "." {
		rel = ""
	}
	var parts []string
	if rel != "" {
		parts = strings.Split(rel, "/")
	}
	return matchSegments(segs, parts)
}

// splitPattern режет шаблон на сегменты и проверяет синтаксис каждого.
func splitPattern(pattern string) ([]string, error) {
	if pattern == "" {
		return nil, fmt.Errorf("пустой шаблон")
	}
	segs := strings.Split(strings.TrimPrefix(filepath.ToSlash(pattern), "./"), "/")
	for _, seg := range segs {
		if seg == "**" {
			continue
		}
		if strings.Contains(seg, "**") {
			return nil, fmt.Errorf("шаблон %q некорректен: %q — сегмент ** пишется отдельно, между слешами", pattern, seg)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return nil, fmt.Errorf("шаблон %q некорректен: %s", pattern, err)
		}
	}
	return segs, nil
}

// matchSegments сопоставляет сегменты шаблона с сегментами пути.
func matchSegments(segs, parts []string) (bool, error) {
	if len(segs) == 0 {
		return len(parts) == 0, nil
	}
	if segs[0] == "**" {
		// ** съедает от нуля сегментов и дальше по одному.
		for i := 0; i <= len(parts); i++ {
			ok, err := matchSegments(segs[1:], parts[i:])
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	if len(parts) == 0 {
		return false, nil
	}
	ok, err := path.Match(segs[0], parts[0])
	if err != nil || !ok {
		return false, err
	}
	return matchSegments(segs[1:], parts[1:])
}
