package domain_test

import (
	"go/build"
	"strings"
	"testing"
)

// selfPath — собственный путь пакета: тест-пакет domain_test импортирует его
// законно, это не внешняя зависимость.
const selfPath = "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// TestТолькоStdlib закрепляет границу из спецификации: пакет domain не зависит
// ни от чего, кроме стандартной библиотеки. Ожидаемое значение берётся не из
// кода под тестом, а из списка импортов, прочитанного go/build из исходников.
func TestТолькоStdlib(t *testing.T) {
	ctx := build.Default
	pkg, err := ctx.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("разбор пакета: %v", err)
	}

	импорты := append([]string{}, pkg.Imports...)
	импорты = append(импорты, pkg.TestImports...)
	импорты = append(импорты, pkg.XTestImports...)

	for _, imp := range импорты {
		if imp == selfPath {
			continue
		}
		// Внешний путь импорта всегда содержит точку в первом сегменте
		// (доменное имя), у stdlib её нет. Пакеты самого проекта отсекаются
		// отдельно по префиксу модуля.
		первый := imp
		if i := strings.Index(imp, "/"); i >= 0 {
			первый = imp[:i]
		}
		if strings.Contains(первый, ".") {
			t.Errorf("domain импортирует внешний пакет %q", imp)
		}
		if strings.HasPrefix(imp, "github.com/blessed2k/1C-WORKFLOW/") {
			t.Errorf("domain импортирует пакет проекта %q", imp)
		}
	}
}
