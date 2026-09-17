// Package arch читает исходники модуля и проверяет архитектурные запреты,
// которые иначе нарушаются незаметно: направление зависимостей, изоляцию SQL,
// изоляцию старого слоя и кроссплатформенность общего кода.
//
// Пакет намеренно обходится стандартной библиотекой (go/parser, go/ast) вместо
// go/packages: гард обязан работать на дереве, где часть пакетов ещё не создана
// или не компилируется, а go/packages в такой ситуации возвращает ошибки вместо
// фактов. Отсутствующий пакет — не нарушение, а нормальное состояние сборки.
package arch

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Module — разобранное дерево исходников одного Go-модуля.
type Module struct {
	// Path — путь модуля из go.mod.
	Path string
	// Root — каталог, из которого читали.
	Root string
	// Packages — пакеты модуля, в порядке обхода каталогов.
	Packages []Package
	// Skipped — файлы, которые не разобрались parser.ParseFile (синтаксическая
	// ошибка), и поэтому не участвуют ни в одном пакете и ни в одной проверке.
	// Гард на дереве с битым файлом не должен молча выглядеть зелёным — вызывающий
	// обязан явно решить, что делать с непустым списком (упасть тестом, залогировать).
	Skipped []SkippedFile
}

// SkippedFile — файл, пропущенный при разборе модуля из-за синтаксической
// ошибки: parser.ParseFile не смог его прочитать, поэтому ни один архитектурный
// запрет к нему не применялся.
type SkippedFile struct {
	// Rel — путь файла относительно корня модуля, через слэш.
	Rel string
	// Err — ошибка, которую вернул parser.ParseFile.
	Err error
}

// Package — каталог с Go-файлами.
type Package struct {
	// ImportPath — полный путь импорта пакета.
	ImportPath string
	// Rel — путь каталога относительно корня модуля, через слэш
	// ("internal/domain"); для корневого каталога — пустая строка.
	Rel string
	// Files — файлы пакета, включая тестовые.
	Files []File
}

// File — один разобранный файл.
type File struct {
	// Rel — путь файла относительно корня модуля, через слэш.
	Rel string
	// Imports — пути импортов файла.
	Imports []string
	// Test — файл заканчивается на _test.go.
	Test bool
	// BuildTag — в шапке файла есть //go:build, и его условие реально
	// ограничивает платформу (GOOS-терм внутри выражения). Фиктивный тег вроде
	// "//go:build go1.1" сюда не попадает и от проверок не освобождает.
	BuildTag bool
	// Platform — суффикс имени файла, ограничивающий платформу
	// ("windows", "unix", "darwin", ...); пустой, если суффикса нет.
	Platform string

	syntax *ast.File
	fset   *token.FileSet
}

// Violation — одно нарушение запрета. Сообщение рассчитано на то, что его
// прочитает человек, увидевший упавший тест впервые.
type Violation struct {
	// Rule — короткое имя запрета.
	Rule string
	// File — файл относительно корня модуля.
	File string
	// Import — импорт, если запрет про зависимости.
	Import string
	// Message — что именно нарушено и почему это запрещено.
	Message string
}

func (v Violation) String() string {
	if v.Import != "" {
		return fmt.Sprintf("[%s] %s: импорт %q — %s", v.Rule, v.File, v.Import, v.Message)
	}
	return fmt.Sprintf("[%s] %s: %s", v.Rule, v.File, v.Message)
}

// пропускаемыеКаталоги не содержат исходников модуля.
var пропускаемыеКаталоги = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"testdata":     true,
}

// Load читает модуль из каталога root. Пропускаются служебные каталоги,
// testdata и вложенные модули со своим go.mod (они правилам основного модуля
// не подчиняются).
func Load(root string) (*Module, error) {
	абсолютный, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	путьМодуля, err := модульИзGoMod(filepath.Join(абсолютный, "go.mod"))
	if err != nil {
		return nil, err
	}
	модуль := &Module{Path: путьМодуля, Root: абсолютный}

	err = filepath.WalkDir(абсолютный, func(текущий string, запись fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !запись.IsDir() {
			return nil
		}
		if текущий != абсолютный {
			if пропускаемыеКаталоги[запись.Name()] || strings.HasPrefix(запись.Name(), ".") {
				return fs.SkipDir
			}
			if _, err := os.Stat(filepath.Join(текущий, "go.mod")); err == nil {
				return fs.SkipDir // вложенный модуль
			}
		}
		пакет, пропущенные, err := разобратьКаталог(абсолютный, текущий, путьМодуля)
		if err != nil {
			return err
		}
		if пакет != nil {
			модуль.Packages = append(модуль.Packages, *пакет)
		}
		модуль.Skipped = append(модуль.Skipped, пропущенные...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return модуль, nil
}

// модульИзGoMod достаёт путь модуля из директивы module.
func модульИзGoMod(путь string) (string, error) {
	содержимое, err := os.ReadFile(путь)
	if err != nil {
		return "", fmt.Errorf("go.mod не прочитан: %w", err)
	}
	for _, строка := range strings.Split(string(содержимое), "\n") {
		строка = strings.TrimSpace(строка)
		if остаток, есть := strings.CutPrefix(строка, "module "); есть {
			return strings.TrimSpace(остаток), nil
		}
	}
	return "", fmt.Errorf("в %s нет директивы module", путь)
}

// разобратьКаталог собирает пакет из одного каталога; nil — Go-файлов нет.
// Файлы, не прошедшие parser.ParseFile, в пакет не попадают, но и не пропадают
// молча — они возвращаются отдельным списком.
func разобратьКаталог(корень, каталог, путьМодуля string) (*Package, []SkippedFile, error) {
	записи, err := os.ReadDir(каталог)
	if err != nil {
		return nil, nil, err
	}
	относительный, err := filepath.Rel(корень, каталог)
	if err != nil {
		return nil, nil, err
	}
	относительный = filepath.ToSlash(относительный)
	if относительный == "." {
		относительный = ""
	}
	пакет := &Package{Rel: относительный, ImportPath: путьМодуля}
	if относительный != "" {
		пакет.ImportPath = путьМодуля + "/" + относительный
	}

	var пропущенные []SkippedFile
	fset := token.NewFileSet()
	for _, запись := range записи {
		if запись.IsDir() || !strings.HasSuffix(запись.Name(), ".go") {
			continue
		}
		исходник, err := os.ReadFile(filepath.Join(каталог, запись.Name()))
		if err != nil {
			return nil, nil, err
		}
		разобранный, err := parser.ParseFile(fset, filepath.Join(каталог, запись.Name()), исходник, parser.SkipObjectResolution)
		if err != nil {
			// Несобирающийся файл — не повод молчать обо всём остальном:
			// гард обязан работать на дереве в процессе стройки. Но и не повод
			// делать вид, что его не было — он попадает в Module.Skipped.
			пропущенные = append(пропущенные, SkippedFile{
				Rel: path.Join(относительный, запись.Name()),
				Err: err,
			})
			continue
		}
		файл := File{
			Rel:      path.Join(относительный, запись.Name()),
			Test:     strings.HasSuffix(запись.Name(), "_test.go"),
			BuildTag: естьBuildTag(исходник),
			Platform: платформаИзИмени(запись.Name()),
			syntax:   разобранный,
			fset:     fset,
		}
		for _, импорт := range разобранный.Imports {
			путь, err := strconv.Unquote(импорт.Path.Value)
			if err != nil {
				continue
			}
			файл.Imports = append(файл.Imports, путь)
		}
		пакет.Files = append(пакет.Files, файл)
	}
	if len(пакет.Files) == 0 {
		return nil, пропущенные, nil
	}
	return пакет, пропущенные, nil
}

// естьBuildTag ищет в шапке файла (до объявления package) директиву
// //go:build, чьё условие реально ограничивает платформу — то есть внутри
// выражения есть хотя бы один GOOS-терм из платформыФайлов, в любой позиции
// относительно &&/||/!. Само наличие директивы недостаточно: "//go:build
// go1.1" ничего не ограничивает по платформе и не должно освобождать файл от
// проверки портируемости.
func естьBuildTag(исходник []byte) bool {
	шапка := исходник
	if i := bytes.Index(исходник, []byte("\npackage ")); i >= 0 {
		шапка = исходник[:i]
	}
	for _, строка := range strings.Split(string(шапка), "\n") {
		строка = strings.TrimSpace(строка)
		if !constraint.IsGoBuild(строка) {
			continue
		}
		выражение, err := constraint.Parse(строка)
		if err != nil {
			continue
		}
		if содержитПлатформенныйТерм(выражение) {
			return true
		}
	}
	return false
}

// содержитПлатформенныйТерм обходит дерево разобранного //go:build выражения
// и ищет хотя бы один терм, совпадающий с платформыФайлов — тем самым списком
// GOOS/групп, которым Go ограничивает файлы по имени.
func содержитПлатформенныйТерм(выражение constraint.Expr) bool {
	switch e := выражение.(type) {
	case *constraint.TagExpr:
		return платформыФайлов[e.Tag]
	case *constraint.NotExpr:
		return содержитПлатформенныйТерм(e.X)
	case *constraint.AndExpr:
		return содержитПлатформенныйТерм(e.X) || содержитПлатформенныйТерм(e.Y)
	case *constraint.OrExpr:
		return содержитПлатформенныйТерм(e.X) || содержитПлатформенныйТерм(e.Y)
	default:
		return false
	}
}

// платформыФайлов — суффиксы, по которым Go сам ограничивает файл платформой.
var платформыФайлов = map[string]bool{
	"windows": true, "unix": true, "darwin": true, "linux": true,
	"freebsd": true, "openbsd": true, "netbsd": true, "dragonfly": true,
	"solaris": true, "aix": true, "android": true, "ios": true,
	"js": true, "wasip1": true, "plan9": true,
}

// платформаИзИмени возвращает платформенный суффикс имени файла.
func платформаИзИмени(имя string) string {
	основа := strings.TrimSuffix(имя, ".go")
	основа = strings.TrimSuffix(основа, "_test")
	i := strings.LastIndex(основа, "_")
	if i < 0 {
		return ""
	}
	суффикс := основа[i+1:]
	if платформыФайлов[суффикс] {
		return суффикс
	}
	return ""
}

// Rel приводит путь импорта к пути относительно корня модуля. Второе значение —
// принадлежит ли импорт этому модулю.
func (m *Module) Rel(импорт string) (string, bool) {
	if импорт == m.Path {
		return "", true
	}
	if остаток, есть := strings.CutPrefix(импорт, m.Path+"/"); есть {
		return остаток, true
	}
	return "", false
}

// Stdlib отвечает, ведёт ли импорт в стандартную библиотеку. Внешний путь всегда
// содержит точку в первом сегменте (доменное имя), у stdlib её нет; пакеты
// самого модуля отсекаются отдельно.
func (m *Module) Stdlib(импорт string) bool {
	if _, свой := m.Rel(импорт); свой {
		return false
	}
	первый := импорт
	if i := strings.Index(импорт, "/"); i >= 0 {
		первый = импорт[:i]
	}
	return !strings.Contains(первый, ".")
}

// Package возвращает пакет по пути относительно корня модуля; nil, если такого
// пакета в дереве нет. Отсутствие пакета — законное состояние: часть модулей
// появится в следующих тасках.
func (m *Module) Package(rel string) *Package {
	for i := range m.Packages {
		if m.Packages[i].Rel == rel {
			return &m.Packages[i]
		}
	}
	return nil
}

// UnderPrefix возвращает пакеты, лежащие в поддереве rel (включая сам rel).
func (m *Module) UnderPrefix(rel string) []*Package {
	var найденные []*Package
	for i := range m.Packages {
		if m.Packages[i].Rel == rel || strings.HasPrefix(m.Packages[i].Rel, rel+"/") {
			найденные = append(найденные, &m.Packages[i])
		}
	}
	return найденные
}
