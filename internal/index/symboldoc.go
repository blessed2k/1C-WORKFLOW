package index

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// docFirstLineMaxRunes ограничивает первую строку описания: строка кода по
// стандарту не длиннее 120 символов, а минифицированный или сгенерированный
// модуль не должен раздувать индекс одной строкой.
const docFirstLineMaxRunes = 300

// regionPath возвращает путь областей препроцессора, покрывающих смещение, от
// внешней к внутренней: "ПрограммныйИнтерфейс/Данные". Вне областей путь пуст.
//
// Хранится путь, а не одна область: методы программного интерфейса БСП лежат
// во вложенных областях (на УТ 11.5 это 52% методов API), и по самой
// внутренней области нельзя узнать ни верхнюю ("ПрограммныйИнтерфейс"), ни
// промежуточную ("УстаревшиеПроцедурыИФункции").
//
// Области в Module.Regions идут в порядке открытия, то есть внешняя раньше
// вложенной: порядок обхода и есть порядок пути.
func regionPath(regions []bsl.Region, offset int) string {
	var path []string
	for _, r := range regions {
		if offset >= r.Span.StartByte && offset < r.Span.EndByte {
			path = append(path, r.Name)
		}
	}
	return strings.Join(path, domain.RegionPathSeparator)
}

// docFirstLine возвращает первую содержательную строку комментария,
// стоящего прямо над объявлением. before: текст модуля до начала объявления
// (до директивы компиляции или ключевого слова).
//
// Строки над объявлением обходятся снизу вверх, байтами: у метода БСП длинный
// комментарий, и строка на каждую его строчку стоила бы дороже разбора.
// Блок, который открывается заголовком секции («Параметры:»), описания не
// несёт: дальше идёт параметр, а не назначение метода. Заголовок опознаётся
// вместе с двоеточием: описание «Параметры сеанса заполняются...» остаётся
// описанием.
func docFirstLine(before []byte) string {
	lineStart := bytes.LastIndexByte(before, '\n') + 1
	var top []byte // самая верхняя содержательная строка блока
	inBlock := false
	for end := lineStart; end > 0; {
		start := bytes.LastIndexByte(before[:end-1], '\n') + 1
		text := bytes.TrimSpace(before[start : end-1])
		end = start
		if len(text) == 0 && !inBlock {
			continue // пустые строки между комментарием и объявлением
		}
		comment, ok := bytes.CutPrefix(text, []byte("//"))
		if !ok {
			break
		}
		inBlock = true
		comment = bytes.TrimSpace(comment)
		if len(comment) == 0 || bytes.HasPrefix(comment, []byte("//")) {
			continue // пустая строка комментария или разделитель ////////
		}
		top = comment
	}
	for _, header := range docSectionHeaders {
		if bytes.HasPrefix(top, header) {
			return ""
		}
	}
	return truncateRunes(string(top), docFirstLineMaxRunes)
}

// docSectionHeaders: заголовки секций комментария к методу по стандарту
// оформления, с которых описание начинаться не может.
var docSectionHeaders = [][]byte{
	[]byte("Параметры:"), []byte("Возвращаемое значение:"),
	[]byte("Parameters:"), []byte("Returns:"),
}

// truncateRunes обрезает строку до limit рун, не разрывая руну.
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// symbolRegionAndDoc выводит область и первую строку описания символа с
// порядковым номером i в срезе buildSymbols: сначала методы модуля, за ними
// переменные. У переменной описания нет: комментарий над оператором Перем
// относится ко всему оператору, а не к имени.
func symbolRegionAndDoc(mod *bsl.Module, i int) (region, doc string) {
	if i < len(mod.Methods) {
		start := mod.Methods[i].Span.StartByte
		return regionPath(mod.Regions, start),
			docFirstLine(mod.Text(domain.Span{StartByte: 0, EndByte: start}))
	}
	if j := i - len(mod.Methods); j < len(mod.Variables) {
		return regionPath(mod.Regions, mod.Variables[j].Span.StartByte), ""
	}
	return "", ""
}
