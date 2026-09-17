package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// DiscoverMaxDepth — на сколько уровней вглубь от корня обхода ищутся выгрузки.
// Столько же берёт существующий findProjects в cmd/mcp1c/projects.go.
const DiscoverMaxDepth = 3

// Candidate — предложение автопоиска: что нашлось и чем это выглядит.
// Кандидат ничего не регистрирует: связывание компонентов всегда явное
// (архитектура §8).
type Candidate struct {
	Kind domain.ComponentKind `json:"kind"`
	// Root — канонический абсолютный путь корня кандидата.
	Root string `json:"root"`
	// Rel — путь относительно корня обхода, со слешами (как пишется в манифест).
	Rel string `json:"rel"`
	// Name — имя объекта из выгрузки: конфигурации, обработки, отчёта.
	Name string `json:"name"`
	// ExtensionPurpose — назначение расширения, непустое только у расширений.
	ExtensionPurpose string `json:"extensionPurpose,omitempty"`
	// SuggestedID — предлагаемый идентификатор компонента для манифеста.
	SuggestedID domain.ComponentID `json:"suggestedId"`
}

// Discover обходит root и предлагает кандидатов в компоненты: выгрузки
// конфигураций и расширений, исходники внешних обработок и отчётов.
//
// Найденный корень не раскрывается вглубь: внутри выгрузки лежат сотни
// каталогов метаданных, и ни один из них отдельным компонентом не бывает.
// Служебные и сгенерированные каталоги (.mcp1c, .git, build, temp, каталоги
// информационных баз) не обходятся.
func Discover(root string) ([]Candidate, error) {
	if root == "" {
		return nil, fmt.Errorf("каталог обхода не задан")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("каталог обхода %q: %w", root, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("каталог обхода %q не читается: %w", root, err)
	}

	var out []Candidate
	занятые := make(map[domain.ComponentID]bool)
	err = filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // нечитаемый каталог не повод обрывать обход
		}
		rel, err := filepath.Rel(real, path)
		if err != nil {
			return nil
		}
		if rel != "." && IsIgnored(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if rel != "." && strings.Count(rel, string(os.PathSeparator))+1 > DiscoverMaxDepth {
			return filepath.SkipDir
		}
		// Каталог информационной базы не обходится и не читается: индекс живёт
		// на XML/BSL, в базу он не ходит вовсе.
		if IsInfobaseDir(path) {
			return filepath.SkipDir
		}

		detected, err := DetectKind(path)
		if err != nil {
			return nil // обычный каталог, идём дальше
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == "." {
			relSlash = ""
		}
		id := suggestID(detected, d.Name(), relSlash, занятые)
		занятые[id] = true
		out = append(out, Candidate{
			Kind:             detected.Kind,
			Root:             path,
			Rel:              relSlash,
			Name:             detected.Name,
			ExtensionPurpose: detected.ExtensionPurpose,
			SuggestedID:      id,
		})
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// suggestID предлагает идентификатор компонента: слаг из имени каталога, а
// если из него слага не выходит (кириллическое имя) — вид компонента с
// коротким хешем пути, чтобы предложение оставалось стабильным между запусками.
//
// Предложение обязано быть уникальным в пределах обхода: типовая раскладка
// tools/<Обработка>/src из §9 даёт всем внешним обработкам одно имя каталога
// "src", и одинаковый id сделал бы манифест невалидным сразу после вставки.
func suggestID(detected DetectedKind, dirName, rel string, занятые map[domain.ComponentID]bool) domain.ComponentID {
	свободен := func(id domain.ComponentID) bool { return id.Validate() == nil && !занятые[id] }

	if id := domain.ComponentID(slugify(dirName)); свободен(id) {
		return id
	}
	if id := domain.ComponentID(slugify(detected.Name)); свободен(id) {
		return id
	}
	// Хеш пути стабилен между запусками, поэтому предложенный id не «прыгает»
	// от обхода к обходу.
	sum := sha256.Sum256([]byte(rel))
	base := domain.ComponentID(kindPrefix(detected.Kind) + "-" + hex.EncodeToString(sum[:3]))
	if свободен(base) {
		return base
	}
	for n := 2; ; n++ {
		id := domain.ComponentID(fmt.Sprintf("%s-%d", base, n))
		if свободен(id) {
			return id
		}
	}
}

// kindPrefix — короткое обозначение вида компонента для предложенного id.
func kindPrefix(k domain.ComponentKind) string {
	switch k {
	case domain.KindConfiguration:
		return "cfg"
	case domain.KindExtension:
		return "ext"
	case domain.KindExternalDataProcessor:
		return "epf"
	case domain.KindExternalReport:
		return "erf"
	default:
		return "src"
	}
}

// slugify оставляет от имени то, что годится в идентификатор компонента.
func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}
