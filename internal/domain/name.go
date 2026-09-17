package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
)

// SymbolUID — стабильная внешняя ссылка на символ (архитектура §14):
// hash(project, component, module_path, name_norm).
type SymbolUID string

// NormalizeName приводит имя 1С к каноническому виду name_norm: составление
// (NFC) плюс нижний регистр. Имена BSL регистронезависимы, а одна и та же буква
// в выгрузке встречается и составленной, и разложенной — без нормализации это
// два разных имени.
//
// упрощение: составляются только кириллические сочетания, которые реально
// встречаются в идентификаторах 1С (и/У + U+0306, е + U+0308 и их заглавные
// формы). Полный NFC требует таблиц Unicode из golang.org/x/text/unicode/norm —
// это внешняя зависимость, а domain обязан жить на stdlib. Потолок: имя с иным
// комбинирующим знаком останется разложенным и совпадёт само с собой, но не со
// своей составленной формой. Путь наверх — разрешить зависимость x/text и
// заменить тело composeCyrillic одним вызовом norm.NFC.
func NormalizeName(s string) string {
	return strings.ToLower(composeCyrillic(s))
}

// composed — пары «базовая буква + комбинирующий знак» → составленная буква.
// Список ровно тот, что встречается в кириллических идентификаторах: краткая
// над и/у и диерезис над е/і.
var composed = map[rune]map[rune]rune{
	0x0306: { // combining breve
		'И': 'Й', 'и': 'й',
		'У': 'Ў', 'у': 'ў',
	},
	0x0308: { // combining diaeresis
		'Е': 'Ё', 'е': 'ё',
		'І': 'Ї', 'і': 'ї',
	},
}

// composeCyrillic сворачивает известные пары «буква + комбинирующий знак».
func composeCyrillic(s string) string {
	if !strings.ContainsFunc(s, isCombining) {
		return s
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for _, r := range runes {
		if len(out) > 0 {
			if pairs, ok := composed[r]; ok {
				if c, ok := pairs[out[len(out)-1]]; ok {
					out[len(out)-1] = c
					continue
				}
			}
		}
		out = append(out, r)
	}
	return string(out)
}

// isCombining сообщает, что руна — комбинирующий знак из известных нам.
func isCombining(r rune) bool {
	_, ok := composed[r]
	return ok
}

// NormalizeModulePath приводит путь модуля к канонической форме: разделитель —
// прямой слеш, без ведущего слеша, без "." и ".." в середине. Регистр
// сохраняется: в именах файлов выгрузки он значим и показывается пользователю.
//
// Канонизация обязательна до подсчёта uid: "CommonModules/Х/Ext/Module.bsl",
// ".\CommonModules\Х\Ext\Module.bsl" и "CommonModules//Х/Ext/Module.bsl" — один
// и тот же модуль, и uid у его символов обязан быть один.
func NormalizeModulePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// NewSymbolUID считает стабильный идентификатор символа. Составляющие
// разделяются нулевым байтом: он не встречается в путях и именах 1С, поэтому
// разные разбиения не могут дать одну и ту же строку под хеш. Путь модуля
// канонизируется, имя ожидается уже нормализованным (NormalizeName).
func NewSymbolUID(project ProjectID, component ComponentID, modulePath, nameNorm string) SymbolUID {
	h := sha256.New()
	for _, part := range []string{string(project), string(component), NormalizeModulePath(modulePath), nameNorm} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	// 128 бит хеша: коллизия на масштабе миллионов символов исключена, а строка
	// вдвое короче полного sha256 — uid лежит в каждой строке индекса.
	return SymbolUID(hex.EncodeToString(sum[:16]))
}
