package app

import (
	"strings"
	"unicode"
)

// Ранжирование find_api. Поиск лексический: слово запроса сводится к основе
// и ищется как НАЧАЛО слова в имени метода, имени модуля и первой строке
// описания. Начало слова, а не подстрока: основа «цен» иначе находит
// «Сценарий», «Оценка» и «Лицензия», и на общем наборе методов конфигурации
// такой шум занимает весь топ.

// Веса места совпадения: имя метода самый сильный признак того, для чего
// метод, описание самый слабый.
const (
	apiWeightName    = 3
	apiWeightModule  = 2
	apiWeightSummary = 1
)

// apiQueryTerms сводит запрос к основам слов, без повторов и без слов короче
// трёх букв («в», «по», «на»).
func apiQueryTerms(query string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range apiWords(query) {
		r := []rune(w)
		if len(r) <= 2 {
			continue
		}
		if t := apiStem(r); !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// apiStem отрезает окончание: до трёх рун и не короче четырёх, чтобы короткие
// слова («ввод», «цена») остались целыми. Имена в коде стоят в именительном
// падеже, задача описана в каком придётся: «печатную» должна найти «Печать».
func apiStem(word []rune) string {
	cut := len(word) - 4
	if cut > 3 {
		cut = 3
	}
	if cut < 0 {
		cut = 0
	}
	return string(word[:len(word)-cut])
}

// apiWords разбивает текст на слова в нижнем регистре. Идентификатор делится
// по границам CamelCase: «РазложитьСтрокуВМассив» даёт «разложить», «строку»,
// «в», «массив»; подряд идущие заглавные считаются одним словом, пока за ними
// не начнётся следующее («URLАдрес» даёт «url», «адрес»).
func apiWords(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, apiFold(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(text)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r):
			if len(cur) > 0 && unicode.IsUpper(r) {
				prev := cur[len(cur)-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if !unicode.IsLetter(prev) || unicode.IsLower(prev) || nextLower {
					flush()
				}
			} else if len(cur) > 0 && unicode.IsDigit(cur[len(cur)-1]) {
				flush()
			}
			cur = append(cur, r)
		case unicode.IsDigit(r):
			if len(cur) > 0 && !unicode.IsDigit(cur[len(cur)-1]) {
				flush()
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// apiFold: нижний регистр, «ё» как «е»: в запросе её пишут как придётся.
func apiFold(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "ё", "е")
}

// apiSearchText — метод, подготовленный к сравнению с запросом.
type apiSearchText struct {
	name, module, summary []string
}

// apiRank сравнивает метод с основами запроса: coverage — сколько РАЗНЫХ слов
// запроса нашлось хоть где-то, score — сумма весов мест совпадения.
//
// Сортируют сначала по coverage: метод, отвечающий на три слова запроса из
// трёх, полезнее метода, в длинном имени которого одно слово запроса
// встретилось рядом с именем модуля.
func apiRank(terms []string, d apiSearchText) (coverage, score int) {
	for _, t := range terms {
		hit := 0
		if apiHasPrefix(d.name, t) {
			hit += apiWeightName
		}
		if apiHasPrefix(d.module, t) {
			hit += apiWeightModule
		}
		if apiHasPrefix(d.summary, t) {
			hit += apiWeightSummary
		}
		if hit > 0 {
			coverage++
			score += hit
		}
	}
	return coverage, score
}

func apiHasPrefix(words []string, term string) bool {
	for _, w := range words {
		if strings.HasPrefix(w, term) {
			return true
		}
	}
	return false
}
