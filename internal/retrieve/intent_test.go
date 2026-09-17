package retrieve

import "testing"

// TestClassifyIntentTable — таблица правил rule-based классификатора (§24
// шаг 1, «таблица правил в коде и тестах») — каждая строка это реальная
// формулировка задачи 1С, ожидаемый Primary — независимо выбран по смыслу
// формулировки, не подогнан под то, что вернул код.
func TestClassifyIntentTable(t *testing.T) {
	cases := []struct {
		name string
		task string
		want string
	}{
		{"bugfix", "Исправь ошибку в РаботаСЗаказами.ЗаполнитьСтатус", IntentBugfix},
		{"signature-change", "Измени сигнатуру экспортной ОбщегоНазначения27.ПолучитьЦену", IntentSignatureChange},
		{"register", "Кто пишет в регистр ТоварыНаСкладах", IntentRegister},
		{"form", "Поменяй обработчик ПриИзменении поля Склад на форме документа Заказ", IntentForm},
		{"add-attribute", "Добавь реквизит СрокДоставки в Документ.ЗаказКлиента и оцени impact", IntentAddAttribute},
		{"posting", "Почему неправильно проводится документ, движения не создаются", IntentPosting},
		{"query", "Почему запрос возвращает не те строки, разбери текст запроса", IntentQuery},
		{"rights", "Пользователь не видит документ, проверь право на доступ", IntentRights},
		{"unknown", "Расскажи мне о погоде", IntentUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyIntent(c.task, nil)
			if got.Primary != c.want {
				t.Fatalf("classifyIntent(%q).Primary = %q, want %q (scores=%v)", c.task, got.Primary, c.want, got.Scores)
			}
			if c.want != IntentUnknown && got.Confidence <= 0 {
				t.Fatalf("classifyIntent(%q).Confidence = %v, want > 0", c.task, got.Confidence)
			}
			if c.want == IntentUnknown && got.Confidence != 0 {
				t.Fatalf("classifyIntent(%q).Confidence = %v, want 0 для unknown", c.task, got.Confidence)
			}
		})
	}
}

// TestClassifyIntentWordBoundary — регрессия на баг из evaluation-report.md
// §3: substring-матчинг ловил лексикон rights («прав») внутри чужого слова
// («поправь»), из-за чего задача про исправление сигнатуры и её вызывающих
// классифицировалась как rights. containsAtWordBoundary должен исключать
// вхождение, начинающееся в середине другого слова, но не ломать легитимное
// голосование за rights, когда «прав»/«право» — реально отдельное слово.
func TestClassifyIntentWordBoundary(t *testing.T) {
	t.Run("поправь_вызывающих_не_rights", func(t *testing.T) {
		task := "исправь функцию и поправь всех вызывающих"
		got := classifyIntent(task, nil)
		if got.Primary == IntentRights {
			t.Fatalf("classifyIntent(%q).Primary = rights (баг: «прав» подстрокой из «поправь»), scores=%v", task, got.Scores)
		}
		if got.Primary != IntentBugfix && got.Primary != IntentSignatureChange {
			t.Fatalf("classifyIntent(%q).Primary = %q, want bugfix или signature-change (по факту голосов после фикса), scores=%v", task, got.Primary, got.Scores)
		}
	})

	t.Run("право_само_по_себе_всё_ещё_rights", func(t *testing.T) {
		task := "право"
		got := classifyIntent(task, nil)
		if got.Primary != IntentRights {
			t.Fatalf("classifyIntent(%q).Primary = %q, want rights (точное слово не должно было сломаться)", task, got.Primary)
		}
	})

	t.Run("справочник_не_голосует_rights", func(t *testing.T) {
		// «справочник» содержит «прав» в середине слова (с-ПРАВ-очник) —
		// тот же класс бага, что «поправь»: без границы слова ошибочно
		// засчитывался голос за rights только от упоминания каталога.
		task := "справочник"
		got := classifyIntent(task, nil)
		if got.Primary == IntentRights {
			t.Fatalf("classifyIntent(%q).Primary = rights (баг: «прав» подстрокой из «справочник»), scores=%v", task, got.Scores)
		}
	})
}

// TestRequiredCategoriesTableCoversAllIntents — каждый intent классификатора
// обязан иметь непустую карту обязательных категорий (даже если это откат на
// bugfix/unknown для exchange/extension — сам список не должен быть пуст).
func TestRequiredCategoriesTableCoversAllIntents(t *testing.T) {
	intents := []string{
		IntentBugfix, IntentUnknown, IntentSignatureChange, IntentForm, IntentQuery,
		IntentPosting, IntentRights, IntentRegister, IntentAddAttribute, IntentExchange, IntentExtension,
	}
	for _, intent := range intents {
		cats := requiredCategories(intent)
		if len(cats) == 0 {
			t.Errorf("requiredCategories(%q) пуст", intent)
		}
	}
}

// TestCategoryWeightTableCoversUsedCategories — каждая категория, реально
// использованная requiredCategoryMap, обязана иметь вес в единой таблице
// categoryWeight (spec: «веса живут в коде одной таблицей») — незарегистрированная
// категория молча падает на дефолт 0.5, что ломает приоритет обязательных
// категорий над контекстными при паковке.
func TestCategoryWeightTableCoversUsedCategories(t *testing.T) {
	for intent, cats := range requiredCategoryMap {
		for _, cat := range cats {
			if _, ok := categoryWeight[cat]; !ok {
				t.Errorf("requiredCategoryMap[%q] содержит категорию %q без веса в categoryWeight", intent, cat)
			}
		}
	}
}
