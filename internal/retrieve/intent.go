package retrieve

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Известные intent (классификатор и карта обязательных категорий).
// exchange/extension присутствуют в лексике классификатора (бриф явно
// называет оба: «обмен -> exchange; расширение -> extension»), но НЕ входят
// в исходную карту обязательных категорий (в ней ровно восемь intent,
// exchange/extension среди них нет).
// requiredCategories честно откатывает их на тот же набор, что bugfix/
// unknown — упрощение, названное явно, а не тихая недоделка.
const (
	IntentBugfix          = "bugfix"
	IntentUnknown         = "unknown"
	IntentSignatureChange = "signature-change"
	IntentForm            = "form"
	IntentQuery           = "query"
	IntentPosting         = "posting"
	IntentRights          = "rights"
	IntentRegister        = "register"
	IntentAddAttribute    = "add-attribute"
	IntentExchange        = "exchange"
	IntentExtension       = "extension"
)

// intentLexicon — таблица правил rule-based классификатора: intent -> набор
// характерных слов/оборотов русской лексики задач 1С (§24 шаг 1: «таблица
// правил в коде и тестах»). Порядок внутри списка неважен — совпадение
// любого слова засчитывает intent'у один голос; порядок КЛЮЧЕЙ карты тоже не
// имеет значения для результата (классификация не зависит от порядка
// итерации Go по map — см. classifyIntent, сортировка перед выбором победителя).
var intentLexicon = map[string][]string{
	IntentPosting: {
		"провест", "проведен", "движени", "регистратор", "отмена проведения",
		"обработкапроведения", "приобработкапроведения",
	},
	IntentForm: {
		"форма", "форме", "формы", "элемент формы", "приизменении", "поле формы",
		"командной панели", "форму", "управляемая форма",
	},
	IntentQuery: {
		"запрос", "запросе", "текст запроса", "выборка", "скд",
		"виртуальная таблица", "запросу",
	},
	IntentRights: {
		"право", "прав", "не вижу", "не видит", "доступ", "rls", "профиль",
		"роль", "ограничение доступа",
	},
	IntentRegister: {
		"регистр", "пишет в регистр", "остатки", "накоплени", "движения регистра",
	},
	IntentSignatureChange: {
		"сигнатур", "измени параметры", "поменяй параметры", "добавь параметр",
		"измени экспортн", "новый параметр", "измени функцию", "измени процедуру",
	},
	IntentExchange: {
		"обмен", "план обмена", "выгрузка данных", "correspondent", "узел обмена",
	},
	IntentExtension: {
		"расширени", "перехватчик", "вместо оригинального", "заимствован",
	},
	IntentAddAttribute: {
		"добавь реквизит", "новый реквизит", "добавить реквизит",
		"добавь табличную часть", "добавь измерение", "добавь ресурс",
	},
	IntentBugfix: {
		"исправь", "исправить", "ошибк", "баг", "не работает", "почему падает",
		"дефект", "чинит", "почини",
	},
}

// requiredCategoryMap: обязательные категории по intent. Ключ: Intent.Primary;
// значение: фиксированный порядок
// упаковки (обязательные категории первыми, порядок внутри списка = порядок
// упаковки этой категории).
var requiredCategoryMap = map[string][]string{
	IntentSignatureChange: {"definition", "references", "callers", "interceptors"},
	IntentForm:            {"binding", "handler", "server_calls", "attributes"},
	IntentRegister:        {"writes_movements", "owning_symbols"},
	IntentQuery:           {"query_text", "owner_symbol", "schema", "tables_fields"},
	IntentPosting:         {"posting_handler", "movements", "register_access", "subscriptions"},
	IntentRights:          {"roles", "rights", "rls", "profiles"},
	IntentAddAttribute:    {"structure", "usages", "forms", "rights", "exchanges"},
	IntentBugfix:          {"definition", "callers", "callees"},
	IntentUnknown:         {"definition", "callers", "callees"},
}

// requiredCategories возвращает обязательные категории для intent, с честным
// откатом exchange/extension на набор bugfix (см. doc-комментарий выше).
func requiredCategories(intent string) []string {
	if cats, ok := requiredCategoryMap[intent]; ok {
		return cats
	}
	return requiredCategoryMap[IntentBugfix]
}

// isWordRune — буква или цифра (unicode-aware, кириллица включена) — символ,
// который считается частью слова для целей проверки границы.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// containsAtWordBoundary — ищет word/phrase (w) в text как вхождение,
// начинающееся на границе слова: либо в начале строки, либо сразу после
// символа, который сам не буква/цифра. Это чинит конкретный баг
// classifyIntent'а (docs/evaluation-report.md §3): «поправь» матчило
// лексикон rights по подстроке «прав», хотя «прав» здесь — середина слова
// «поправь», а не отдельное слово. Проверяется только ЛЕВАЯ граница
// вхождения — намеренно, не полный `\bw\b`: лексикон intentLexicon
// специально хранит усечённые русские основы («ошибк», «движени»,
// «сигнатур», «проведен» и т.п.) без окончаний, чтобы одной записью
// покрывать несколько словоформ («ошибка»/«ошибку»/«ошибки»); требование
// границы и СПРАВА убило бы это намеренное покрытие словоформ (проверено на
// evals/03: с обеих границ сигнатур-лексикон вообще перестаёт матчить
// «сигнатуру», задача проваливается в IntentUnknown). Левой границы
// достаточно, чтобы исключить именно случай «слово-подстрока в СЕРЕДИНЕ
// другого слова» («прав» в «поправь», «прав» в «справочник»), не трогая
// легитимный стемминг вправо. Для многословных фраз («не вижу», «измени
// параметры») проверяется граница перед ПЕРВЫМ словом всей фразы — сами
// фразы уже фиксированы целиком, пробелы внутри них literal.
func containsAtWordBoundary(text, w string) bool {
	if w == "" {
		return false
	}
	searchFrom := 0
	for {
		idx := strings.Index(text[searchFrom:], w)
		if idx < 0 {
			return false
		}
		start := searchFrom + idx
		if start == 0 {
			return true
		}
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		if !isWordRune(before) {
			return true
		}
		searchFrom = start + 1
		if searchFrom >= len(text) {
			return false
		}
	}
}

// classifyIntent — §24 шаг 1: rule-based классификатор по лексике задачи.
// Голос отдаётся intent'у за КАЖДОЕ характерное слово, найденное подстрокой
// в нормализованном (lower-case) тексте задачи — простая, но детерминированная
// и объяснимая схема (веса — количество совпавших слов, не эвристика по
// порядку). mixed допустим: все intent с ненулевым голосом попадают в
// Scores, но typed expansion и required categories управляются только
// Primary — // упрощение: параллельная expansion сразу по нескольким intent
// пропорционально не окупает сложность в этой волне, а пять сценариев §25 —
// все с одним доминирующим intent.
func classifyIntent(task string, focusHints []string) Intent {
	text := strings.ToLower(task)
	for _, h := range focusHints {
		text += " " + strings.ToLower(h)
	}

	raw := map[string]int{}
	total := 0
	for intent, words := range intentLexicon {
		votes := 0
		for _, w := range words {
			if containsAtWordBoundary(text, w) {
				votes++
			}
		}
		if votes > 0 {
			raw[intent] = votes
			total += votes
		}
	}

	if total == 0 {
		return Intent{Primary: IntentUnknown, Confidence: 0}
	}

	// Победитель — максимум голосов; при равенстве voting — детерминированный
	// tie-break по фиксированному порядку приоритета из брифа («проведение
	// -> posting; форма -> form; запрос -> query; права -> rights; регистр ->
	// register; сигнатура -> signature-change; обмен -> exchange; расширение
	// -> extension; добавить реквизит -> add-attribute»).
	priority := []string{
		IntentPosting, IntentForm, IntentQuery, IntentRights, IntentRegister,
		IntentSignatureChange, IntentExchange, IntentExtension, IntentAddAttribute,
		IntentBugfix,
	}
	best := ""
	bestVotes := 0
	for _, intent := range priority {
		if v := raw[intent]; v > bestVotes {
			best, bestVotes = intent, v
		}
	}

	scores := make(map[string]float64, len(raw))
	for intent, v := range raw {
		scores[intent] = float64(v) / float64(bestVotes)
	}

	return Intent{
		Primary:    best,
		Confidence: float64(bestVotes) / float64(total),
		Scores:     scores,
	}
}
