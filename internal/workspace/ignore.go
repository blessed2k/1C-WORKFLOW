package workspace

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// skipDirs — каталоги, которые не обходятся и не индексируются по умолчанию
// (архитектура §8): сгенерированное состояние, служебное окружение агентов,
// системы контроля версий, сборка и логи.
var skipDirs = map[string]bool{
	RegistryDirName: true,
	".git":          true,
	".svn":          true,
	".hg":           true,
	".idea":         true,
	".vscode":       true,
	".claude":       true,
	"skills":        true,
	"node_modules":  true,
	"__pycache__":   true,
	"build":         true,
	"dist":          true,
	"logs":          true,
	"temp":          true,
	"tmp":           true,
	"1cv8log":       true,
}

// skipFileNames — файлы окружения агента: к коду 1С отношения не имеют.
var skipFileNames = map[string]bool{
	"agents.md": true,
	"claude.md": true,
}

// skipExts — бинарные артефакты 1С и логи: индексировать в них нечего,
// исходник для них — XML/BSL выгрузка.
var skipExts = map[string]bool{
	".cf":  true,
	".cfe": true,
	".epf": true,
	".erf": true,
	".dt":  true,
	".1cd": true,
	".lgd": true,
	".lgf": true,
	".log": true,
}

// SkipDir сообщает, что каталог с таким именем не обходится.
func SkipDir(name string) bool { return skipDirs[strings.ToLower(name)] }

// SkipFile сообщает, что файл с таким именем не индексируется.
func SkipFile(name string) bool {
	lower := strings.ToLower(name)
	if skipFileNames[lower] {
		return true
	}
	return skipExts[filepath.Ext(lower)]
}

// IsIgnored сообщает, исключён ли путь из обхода и индексации по умолчанию.
// rel — путь относительно корня компонента; принимаются обе формы разделителя.
func IsIgnored(rel string) bool {
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == "." || rel == "" {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		if SkipDir(part) {
			return true
		}
	}
	last := parts[len(parts)-1]
	return SkipDir(last) || SkipFile(last)
}

// infobaseMarkers — файлы, по которым каталог опознаётся как файловая
// информационная база. В базу индекс не ходит вовсе.
var infobaseMarkers = []string{"1Cv8.1CD", "1Cv8.lgd", "1Cv8.pfl"}

// IsInfobaseDir сообщает, что каталог — файловая информационная база 1С.
func IsInfobaseDir(dir string) bool {
	for _, marker := range infobaseMarkers {
		if fi, err := os.Stat(filepath.Join(dir, marker)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}
