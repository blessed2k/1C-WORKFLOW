package app

import (
	"regexp"
	"strings"
)

// Типы параметров и возвращаемого значения метода. В сигнатуре 1С типов нет:
// они записаны только в комментарии, по стандарту оформления («Имя - Тип -
// описание», раздел «Возвращаемое значение:»). Отсюда их и берёт find_api:
// показывает тип результата в ответе и умеет отбирать методы по типу.
//
// Комментарий приходит из индекса без «//» и без отступов в строках, поэтому
// вложенность по отступам недоступна: поля структуры опознаются только по
// звёздочке. Разбор держится точности, а не полноты: лучше не назвать тип,
// чем назвать типом слово описания.

// reAPITypeExpr: выражение типа из комментария: одно или несколько имён типов
// через запятую, у коллекции с уточнением «из» («Массив из Строка»). Текст,
// который в этот вид не укладывается, типом не считается: это описание
// словами.
var reAPITypeExpr = regexp.MustCompile(`^` + apiTypeOne + `(?:\s*,\s*` + apiTypeOne + `)*$`)

const apiTypeOne = `[A-ZА-ЯЁ][\p{L}\p{N}_.]*(?:\s+из\s+[\p{L}\p{N}_.]+)?`

// reAPIDocSection: заголовок раздела комментария; после двоеточия на той же
// строке может стоять начало раздела («Возвращаемое значение: Строка»).
var reAPIDocSection = regexp.MustCompile(`(?i)^(параметры|возвращаемое значение|пример|примеры|варианты вызова|parameters|returns|example)\s*:\s*(.*)$`)

// reAPIDocSee: ссылка на описание в другом месте («см. Метод») в конце
// выражения типа: «Массив из см. Метод», «Структура, см. Метод». Сама ссылка
// типа не называет, а то, что стоит до неё, называет.
var reAPIDocSee = regexp.MustCompile(`(?i)(?:\s*,|\s+из)?\s*см\.\s.*$`)

// apiTypeNotTypes: слова, которые по форме проходят как имя типа, но в
// разделе результата означают значение («Истина - если запись прошла»).
var apiTypeNotTypes = map[string]bool{"истина": true, "ложь": true}

const (
	// apiTypeSectionLines: сколько строк раздела просматривается, не считая
	// строк полей («*»): описание структуры бывает длинным.
	apiTypeSectionLines = 200
	// apiTypeExprMax: потолок длины выражения типа.
	apiTypeExprMax = 160
)

// apiDocTypes достаёт из комментария метода типы возвращаемого значения и
// типы параметров. Чего по стандарту не записано, того нет: пустой результат
// значит «в комментарии типов нет», а не «метод ничего не возвращает».
func apiDocTypes(doc string) (returns []string, params []string) {
	lines := strings.Split(doc, "\n")
	for i, raw := range lines {
		m := reAPIDocSection.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		section := apiDocSectionLines(m[2], lines[i+1:])
		switch strings.ToLower(m[1]) {
		case "возвращаемое значение", "returns":
			for n, l := range section {
				// Тип называет первая строка раздела и строки вариантов
				// («- Тип - описание»). Остальные строки: описание и поля
				// структуры без звёздочки («Ключ - Строка - ...»), в них
				// слева от дефиса стоит имя, а не тип.
				if n > 0 && !strings.HasPrefix(l, "- ") {
					continue
				}
				returns = apiAppendType(returns, apiReturnType(l))
			}
		case "параметры", "parameters":
			for _, l := range section {
				params = apiAppendType(params, apiParamType(l))
			}
		}
	}
	return returns, params
}

// apiAppendType дописывает тип, если он назван и ещё не встречался.
func apiAppendType(types []string, t string) []string {
	if t == "" {
		return types
	}
	for _, have := range types {
		if have == t {
			return types
		}
	}
	return append(types, t)
}

// apiDocSectionLines: строки раздела до следующего заголовка, без пустых и
// без строк полей («*»). first: текст после двоеточия в строке заголовка.
func apiDocSectionLines(first string, lines []string) []string {
	var out []string
	if first = strings.TrimSpace(first); first != "" {
		out = append(out, first)
	}
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		if reAPIDocSection.MatchString(l) || len(out) == apiTypeSectionLines {
			break
		}
		if l != "" && !strings.HasPrefix(l, "*") {
			out = append(out, l)
		}
	}
	return out
}

// apiReturnType: тип из строки раздела «Возвращаемое значение»: «Тип -
// описание», «Тип:» перед описанием полей, «- Тип - описание» у варианта
// результата.
func apiReturnType(line string) string {
	line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
	expr, _, found := strings.Cut(line, " - ")
	if !found {
		expr = strings.TrimSuffix(line, ":")
	}
	return apiTypeExpr(expr)
}

// apiParamType: тип из строки раздела «Параметры»: «Имя - Тип - описание» и
// «- Тип - описание» (вариант типа предыдущего параметра).
func apiParamType(line string) string {
	if rest, variant := strings.CutPrefix(line, "- "); variant {
		expr, _, _ := strings.Cut(rest, " - ")
		return apiTypeExpr(expr)
	}
	name, rest, found := strings.Cut(line, " - ")
	if !found || strings.ContainsAny(strings.TrimSpace(name), " ,.") {
		return ""
	}
	expr, _, _ := strings.Cut(rest, " - ")
	return apiTypeExpr(strings.TrimSuffix(strings.TrimSpace(expr), ":"))
}

// apiTypeExpr: выражение типа в приведённом виде либо пусто, если текст на
// выражение типа не похож.
func apiTypeExpr(expr string) string {
	expr = reAPIDocSee.ReplaceAllString(strings.TrimSpace(expr), "")
	expr = strings.Join(strings.Fields(strings.TrimRight(strings.TrimSpace(expr), " -.;:")), " ")
	if expr == "" || len([]rune(expr)) > apiTypeExprMax || !reAPITypeExpr.MatchString(expr) {
		return ""
	}
	for _, token := range apiTypeTokens(expr) {
		if apiTypeNotTypes[token] {
			return ""
		}
	}
	return expr
}

// apiTypeKey: имя типа для сравнения: нижний регистр, без пробелов, «ё» как
// «е».
func apiTypeKey(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(s), "ё", "е"), " ", "")
}

// apiTypeTokens раскладывает выражение типа на имена типов: «Массив из
// ТаблицаЗначений, Неопределено» это массив, таблицазначений, неопределено.
func apiTypeTokens(expr string) []string {
	var out []string
	for _, part := range strings.Split(expr, ",") {
		for _, t := range strings.Split(" "+part+" ", " из ") {
			if key := apiTypeKey(t); key != "" {
				out = append(out, key)
			}
		}
	}
	return out
}

// apiTypesMatch сообщает, назван ли тип want среди выражений типов. Имя типа
// сравнивается целиком или до точки: «ТаблицаЗначений» находит «Массив из
// ТаблицаЗначений», «ДокументСсылка» находит «ДокументСсылка.ЗаказКлиента»,
// а «Строка» не находит «СтрокаТаблицыЗначений».
func apiTypesMatch(types []string, want string) bool {
	key := apiTypeKey(want)
	if key == "" {
		return true
	}
	for _, expr := range types {
		for _, token := range apiTypeTokens(expr) {
			if token == key || strings.HasPrefix(token, key+".") {
				return true
			}
		}
	}
	return false
}
