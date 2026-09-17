package domain_test

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestNormalizeName проверяет правило идентичности из архитектуры §14:
// name_norm = lower + NFC. Ожидаемые значения записаны здесь вручную, кодовыми
// точками, а не получены тем же преобразованием, что и в коде под тестом.
func TestNormalizeName(t *testing.T) {
	tests := []struct {
		name  string
		вход  string
		ожид  string
		зачем string
	}{
		{
			name:  "кириллица приводится к нижнему регистру",
			вход:  "ОбщегоНазначения",
			ожид:  "общегоназначения",
			зачем: "имена BSL регистронезависимы",
		},
		{
			name:  "латиница приводится к нижнему регистру",
			вход:  "CommonModule",
			ожид:  "commonmodule",
			зачем: "английский синтаксис индексируется так же",
		},
		{
			name:  "разложенная й собирается в одну кодовую точку",
			вход:  "\u0418\u0306\u0442\u043e\u0433", // И + U+0306 (разложенная Й) + "тог"
			ожид:  "йтог",                           // "йтог"
			зачем: "иначе одно и то же имя даёт два разных name_norm",
		},
		{
			name:  "разложенная ё собирается в одну кодовую точку",
			вход:  "\u0415\u0308\u043b\u043a\u0430", // Е + U+0308 (разложенная Ё) + "лка"
			ожид:  "ёлка",                           // "ёлка"
			зачем: "ё в выгрузках встречается в обеих формах",
		},
		{
			name:  "уже нормализованная строка не меняется",
			вход:  "йтог", // "йтог", уже составленное
			ожид:  "йтог",
			зачем: "нормализация идемпотентна",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.NormalizeName(tc.вход)
			if got != tc.ожид {
				t.Errorf("NormalizeName(%q) = %q, ожидалось %q (%s)", tc.вход, got, tc.ожид, tc.зачем)
			}
		})
	}
}

// TestSymbolUIDРазличаетСоставляющиеКлюча закрепляет состав ключа из §14: uid
// считается по (project, component, module_path, name_norm). Совпадение uid у
// символов из разных компонентов означало бы, что ссылки расширения ведут в
// конфигурацию.
func TestSymbolUIDРазличаетСоставляющиеКлюча(t *testing.T) {
	const модуль = "CommonModules/ОбщегоНазначения/Ext/Module.bsl"
	base := domain.NewSymbolUID("ut-main", "cfg", модуль, "проверить")

	if base == "" {
		t.Fatal("uid пуст")
	}
	if повтор := domain.NewSymbolUID("ut-main", "cfg", модуль, "проверить"); повтор != base {
		t.Errorf("uid не детерминирован: %q != %q", повтор, base)
	}

	// Одинаковый модуль, записанный по-разному, обязан дать один uid: иначе
	// ссылки из XML (обратные слеши) и из BSL разойдутся по разным символам.
	написания := []string{
		`.\CommonModules\ОбщегоНазначения\Ext\Module.bsl`,
		"CommonModules//ОбщегоНазначения/Ext/Module.bsl",
		"/CommonModules/ОбщегоНазначения/Ext/Module.bsl",
		"CommonModules/Другой/../ОбщегоНазначения/Ext/Module.bsl",
	}
	for _, написание := range написания {
		if uid := domain.NewSymbolUID("ut-main", "cfg", написание, "проверить"); uid != base {
			t.Errorf("uid для %q = %q, ожидался тот же, что у канонического пути (%q)", написание, uid, base)
		}
	}

	другие := []domain.SymbolUID{
		domain.NewSymbolUID("ut-other", "cfg", модуль, "проверить"),
		domain.NewSymbolUID("ut-main", "ext-fix", модуль, "проверить"),
		domain.NewSymbolUID("ut-main", "cfg", "CommonModules/Другой/Ext/Module.bsl", "проверить"),
		domain.NewSymbolUID("ut-main", "cfg", модуль, "проверить2"),
	}
	for i, uid := range другие {
		if uid == base {
			t.Errorf("uid #%d совпал с базовым при разных составляющих ключа", i)
		}
	}
}
