package source

import (
	"regexp"
	"strings"
)

// bslEnglish is the one table of the English spelling of BSL keywords and
// platform names the raw analyzers look for. The analyzers name a word in
// Russian and take both spellings from here, so a module written in English
// is read by the same rule, and a missing English word is added in one place.
var bslEnglish = map[string]string{
	// Declarations.
	"Процедура": "Procedure",
	"Функция":   "Function",
	"Асинх":     "Async",
	"Экспорт":   "Export",
	"Возврат":   "Return",
	"Истина":    "True",

	// Loops and query runs.
	"Для":            "For",
	"Пока":           "While",
	"Цикл":           "Do",
	"КонецЦикла":     "EndDo",
	"Выполнить":      "Execute",
	"ВыполнитьПакет": "ExecuteBatch",

	// Locks and balance reads.
	"БлокировкаДанных":  "DataLock",
	"Заблокировать":     "Lock",
	"РегистрНакопления": "AccumulationRegister",
	"Остатки":           "Balance",
	"ОстаткиИОбороты":   "BalanceAndTurnovers",

	// The movements collection and its record sets.
	"Записывать": "Write",
	"Записать":   "Write",
	"Добавить":   "Add",
	"Загрузить":  "Load",
	"Очистить":   "Clear",
	"Прочитать":  "Read",
	"Найти":      "Find",
	"Получить":   "Get",
	"Количество": "Count",
	"Индекс":     "IndexOf",

	// Posting handlers of the object module.
	"ОбработкаПроведения":         "Posting",
	"ОбработкаУдаленияПроведения": "UndoPosting",

	// Stems of method names that hand posting over to a mechanism.
	"Проведени":     "Posting",
	"Движени":       "RegisterRecords",
	"НаборыЗаписей": "RecordSets",
}

// bilingual returns the words followed by their English spellings.
func bilingual(words ...string) []string {
	out := make([]string, 0, 2*len(words))
	out = append(out, words...)
	for _, w := range words {
		if en, ok := bslEnglish[w]; ok {
			out = append(out, en)
		}
	}
	return out
}

// bilingualSet is bilingual as a set of lower-cased words.
func bilingualSet(words ...string) map[string]bool {
	out := map[string]bool{}
	for _, w := range bilingual(words...) {
		out[strings.ToLower(w)] = true
	}
	return out
}

// wordAlt is a regular-expression alternation of the words in both spellings,
// for a rule that is still a regular expression.
func wordAlt(words ...string) string {
	all := bilingual(words...)
	for i, w := range all {
		all[i] = regexp.QuoteMeta(w)
	}
	return strings.Join(all, "|")
}
