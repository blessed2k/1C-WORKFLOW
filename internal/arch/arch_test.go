package arch_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/arch"
)

// Гарды на самом репозитории. Каждый тест — один запрет из §6 архитектуры:
// нарушение видно сразу и с указанием файла, а не через полгода в ревью.

func загрузитьРепозиторий(t *testing.T) *arch.Module {
	t.Helper()
	корень, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("корень репозитория: %v", err)
	}
	модуль, err := arch.Load(корень)
	if err != nil {
		t.Fatalf("разбор модуля: %v", err)
	}
	if len(модуль.Packages) == 0 {
		t.Fatal("в модуле не нашлось ни одного пакета: гард проверял бы пустоту")
	}
	return модуль
}

func сообщить(t *testing.T, нарушения []arch.Violation) {
	t.Helper()
	for _, нарушение := range нарушения {
		t.Error(нарушение.String())
	}
}

func TestГраницаDomain(t *testing.T) {
	сообщить(t, arch.CheckDomainImports(загрузитьРепозиторий(t)))
}

func TestИзоляцияSQL(t *testing.T) {
	сообщить(t, arch.CheckSQLIsolation(загрузитьРепозиторий(t)))
}

func TestСлоиТранспорта(t *testing.T) {
	сообщить(t, arch.CheckCommandLayering(загрузитьРепозиторий(t)))
}

func TestИзоляцияСтарогоСлоя(t *testing.T) {
	сообщить(t, arch.CheckLegacyIsolation(загрузитьРепозиторий(t)))
}

func TestКроссплатформенностьОбщегоКода(t *testing.T) {
	сообщить(t, arch.CheckPortability(загрузитьРепозиторий(t)))
}

// TestПоверхностьStoreВResolve: резолвер берёт из store типы фактов и
// константы схемы и ничего сверх того (веха В1, атрибуция объектного графа).
func TestПоверхностьStoreВResolve(t *testing.T) {
	сообщить(t, arch.CheckResolveStoreSurface(загрузитьРепозиторий(t)))
}

// TestLoadСобираетБитыеФайлыОтдельно: файл с синтаксической ошибкой не должен
// пропадать молча из результата Load — иначе гард на дереве с одним битым
// файлом рядом с настоящим нарушением в другом файле того же пакета остаётся
// зелёным, будто всё чисто. Это тест на сам internal/arch (механику Load), а
// не на одно из архитектурных правил.
//
// Фикстура собирается во временном каталоге, а не лежит в testdata: файл с
// синтаксической ошибкой в .go, закоммиченный в репозиторий, сам не проходит
// gofmt -l и путает проверку, требуемую этим же тикетом.
func TestLoadСобираетБитыеФайлыОтдельно(t *testing.T) {
	корень := t.TempDir()
	пишем := func(rel, содержимое string) {
		t.Helper()
		путь := filepath.Join(корень, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(путь), 0o755); err != nil {
			t.Fatalf("mkdir для %s: %v", rel, err)
		}
		if err := os.WriteFile(путь, []byte(содержимое), 0o644); err != nil {
			t.Fatalf("запись %s: %v", rel, err)
		}
	}
	пишем("go.mod", "module parsefail\n\ngo 1.26\n")
	пишем("internal/pkg/good.go",
		"package pkg\n\n// Good — валидный файл рядом с битым.\nfunc Good() int { return 1 }\n")
	пишем("internal/pkg/bad.go",
		"package pkg\n\nfunc Bad( {\n")

	модуль, err := arch.Load(корень)
	if err != nil {
		t.Fatalf("разбор временного модуля: %v", err)
	}
	if len(модуль.Skipped) != 1 {
		t.Fatalf("Skipped = %d файлов, ожидался 1: %+v", len(модуль.Skipped), модуль.Skipped)
	}
	пропущенный := модуль.Skipped[0]
	if пропущенный.Rel != "internal/pkg/bad.go" {
		t.Errorf("Skipped[0].Rel = %q, ожидалось internal/pkg/bad.go", пропущенный.Rel)
	}
	if пропущенный.Err == nil {
		t.Error("Skipped[0].Err пуст, ожидалась ошибка парсинга")
	}
	// good.go рядом обязан разобраться нормально: пакет не пропадает целиком
	// из-за соседнего битого файла.
	пакет := модуль.Package("internal/pkg")
	if пакет == nil || len(пакет.Files) != 1 || пакет.Files[0].Rel != "internal/pkg/good.go" {
		t.Fatalf("пакет internal/pkg не собрался как ожидалось: %+v", пакет)
	}
}
