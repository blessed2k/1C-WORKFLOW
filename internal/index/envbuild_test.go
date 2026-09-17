package index

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// TestBuildEnvInputRejectsHydrated — инвариант §3 спецификации: корпус, в
// котором осталась хотя бы одна восстановленная из source_file запись,
// окружение резолвера НЕ собирает. Молчаливый пустой Env здесь означал бы
// публикацию неверных результатов разрешения имён без единого красного
// теста, поэтому ответ — ошибка, а вызывающий обязан абортить транзакцию.
func TestBuildEnvInputRejectsHydrated(t *testing.T) {
	corpus := newComponentCorpus("cfg")
	corpus.files["CommonModules/УтилитыОбщие/Ext/Module.bsl"] = &fileRecord{
		relPath:    "CommonModules/УтилитыОбщие/Ext/Module.bsl",
		hydrated:   true,
		size:       10,
		mtimeNS:    1,
		moduleInfo: bsl.ClassifyModule("CommonModules/УтилитыОбщие/Ext/Module.bsl"),
	}

	_, err := buildEnvInput("proj", "cfg", domain.BaseLayer("cfg"), corpus)
	if err == nil {
		t.Fatal("buildEnvInput на корпусе с hydrated-записью вернул nil error")
	}
	if !strings.Contains(err.Error(), "CommonModules/УтилитыОбщие/Ext/Module.bsl") {
		t.Errorf("ошибка не называет файл: %v", err)
	}
}

// TestBuildEnvInputAcceptsParsed — обратная сторона инварианта: полностью
// разобранный корпус собирается без ошибки, и модуль в нём виден.
func TestBuildEnvInputAcceptsParsed(t *testing.T) {
	corpus := newComponentCorpus("cfg")
	rel := "CommonModules/УтилитыОбщие/Ext/Module.bsl"
	corpus.files[rel] = parseOneFile(rel, []byte("Функция Помощь() Экспорт\nВозврат 1;\nКонецФункции\n"))

	in, err := buildEnvInput("proj", "cfg", domain.BaseLayer("cfg"), corpus)
	if err != nil {
		t.Fatalf("buildEnvInput: %v", err)
	}
	if len(in.Modules) != 1 {
		t.Fatalf("Modules = %d, want 1", len(in.Modules))
	}
}
