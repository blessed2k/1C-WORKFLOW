package app

import (
	"regexp"
	"strings"
	"unicode"
)

// Ранжирование find_api. Поиск лексический: слово запроса ищется среди слов
// имени метода, имени модуля и комментария метода. Слова сравниваются целиком,
// а не подстрокой: подстрока «цен» находит «Сценарий», «Оценка» и «Лицензия»,
// и на общем наборе методов конфигурации такой шум занимает весь топ.
//
// Правила меряются эталоном evals/findapi (docs/find-api-eval.md): подбираются
// по настроечной половине набора, качество называется по проверочной. Менять
// их без прогона эталона нельзя.

// apiMaxTerms: потолок слов запроса: запрос длиннее описывает уже не одну
// задачу, а на каждое слово поиск держит по байту на метод. Не больше 32:
// слова запроса, найденные в имени метода, поиск отмечает битами uint32
// (apiRanked.nameMask).
const apiMaxTerms = 32

// apiStopWords: союзы, предлоги и местоимения, которые в запросе не значат
// ничего, а в описаниях методов есть почти везде. Слова короче трёх букв
// отсеиваются длиной и сюда не входят.
var apiStopWords = map[string]bool{
	"или": true, "для": true, "при": true, "что": true, "это": true, "как": true, "его": true,
	"под": true, "над": true, "без": true, "если": true, "либо": true, "чтобы": true, "также": true,
}

// apiQueryTerms сводит запрос к словам: без слов короче трёх букв («в», «по»,
// «на»), без служебных слов и без повторов. Повтор считается по словоформе:
// «цена цену» это одно слово, иначе оно весило бы в охвате дважды. Слова сверх
// apiMaxTerms отбрасываются.
func apiQueryTerms(query string) []string {
	var out []string
words:
	for _, w := range apiWords(query) {
		if len([]rune(w)) <= 2 || apiStopWords[w] {
			continue
		}
		for _, kept := range out {
			if apiSameWord(kept, w) {
				continue words
			}
		}
		if out = append(out, w); len(out) == apiMaxTerms {
			break
		}
	}
	return out
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

// Сравнение слов. Имя метода стоит в одной форме («Удалить», «Индексацию»),
// задачу описывают в какой придётся («удаление», «индексировать»), и усечение
// окончания их не сводит: основа запроса выходит длиннее слова в имени. Два
// слова считаются одним, когда у них общее начало, а всё, чем они расходятся,
// складывается из русских словообразующих суффиксов и окончаний:
//
//	удал|ение ~ удал|ить, формат|ирование ~ формат|, индекс|ировать ~ индекс|ацию
//
// Остаток, не похожий на суффикс, значит другое слово: пропи|сью и проп|уск,
// стро|ку и стро|итель, цен|а и цен|тр.
//
// Правило грубое в обе стороны, и это известно. Оно не сводит корни с
// чередованием («выборка» и «выбрать») и синонимы: их в запрос дописывает тот,
// кто ищет. И оно сводит чужие слова, у которых остаток случайно похож на
// суффикс: «права» и «правило», «карта» и «картина», «строку» и «строить».
// Прежнее правило давало те же ложные пары, новых правило не добавило.

// apiInflection: падежные и родовые окончания существительных и
// прилагательных; «s» и «es» для латинских имён (file и files, http и https).
const apiInflection = `(?:ями|ами|ого|ему|ому|ыми|ими|ием|иях|иям|ой|ей|ый|ий|ые|ие|ых|их|ым|им|ую|юю|ая|яя|ое|ее|ия|ию|ем|ом|ам|ям|ах|ях|ов|ев|ью|es|а|я|е|и|й|ю|у|ы|о|ь|s)?`

var (
	// reAPISuffix: остаток слова после общего начала. По порядку: «ир» и
	// глагольный суффикс (-ова-, -ива-, -ыва-, -ева-); тематическая гласная;
	// суффикс отглагольного существительного, инфинитива, причастия или
	// прилагательного; суффикс -к- с беглой гласной (загруз|ка, ошиб|ок,
	// настро|йки, настро|ек); окончание.
	reAPISuffix = regexp.MustCompile(`^(?:ир)?(?:ов|ив|ыв|ев)?(?:а|я|е|и)?(?:ни|ти|ци|ть|нн|н|ск|ем|им|ющ|вш|л)?(?:(?:й|о|е)?к)?` + apiInflection + `$`)
	// reAPIInflection: остаток из одного окончания.
	reAPIInflection = regexp.MustCompile(`^` + apiInflection + `$`)
)

// apiMinCommonPrefix: общее начало короче трёх букв словом не считается, а
// при общем начале ровно в три буквы остатком может быть только окончание:
// «цена» и «цену» одно слово, «вид» и «видимость», «цена» и «ценник» разные.
const apiMinCommonPrefix = 3

// apiSameWord сообщает, одно ли слово записано в a и b разными формами. Слова
// приходят уже свёрнутыми apiFold. Сравнение симметрично.
func apiSameWord(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	common := 0
	for common < len(ra) && common < len(rb) && ra[common] == rb[common] {
		common++
	}
	if common < apiMinCommonPrefix {
		return false
	}
	restA, restB := string(ra[common:]), string(rb[common:])
	if common == apiMinCommonPrefix {
		return reAPIInflection.MatchString(restA) && reAPIInflection.MatchString(restB)
	}
	return reAPISuffix.MatchString(restA) && reAPISuffix.MatchString(restB)
}
