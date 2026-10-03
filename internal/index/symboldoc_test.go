package index

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// TestDocFirstLine: края, которые шов инструмента не выражает: разделители,
// CRLF, заголовок секции, срез длинной строки.
func TestDocFirstLine(t *testing.T) {
	long := strings.Repeat("я", docFirstLineMaxRunes+50)
	for _, c := range []struct {
		name, before, want string
	}{
		{"первая строка блока", "// Возвращает цену.\n// Вторая строка.\n", "Возвращает цену."},
		{"пустые строки до объявления", "// Возвращает цену.\n\n\n", "Возвращает цену."},
		{"разделитель и пустой комментарий", "////////////\n//\n// Возвращает цену.\n//\n", "Возвращает цену."},
		{"CRLF", "// Возвращает цену.\r\n//\r\n", "Возвращает цену."},
		{"отступ перед объявлением", "// Возвращает цену.\n\t", "Возвращает цену."},
		{"блок из одних параметров", "// Параметры:\n//  Товар - СправочникСсылка\n", ""},
		{"описание со слова Параметры", "// Параметры сеанса заполняются при входе.\n", "Параметры сеанса заполняются при входе."},
		{"код над объявлением", "КонецПроцедуры\n\n", ""},
		{"комментарий отделён кодом", "// Чужой.\nПерем А;\n", ""},
		{"начало файла", "", ""},
		{"длинная строка", "// " + long + "\n", strings.Repeat("я", docFirstLineMaxRunes)},
	} {
		if got, _ := docComment([]byte(c.before)); got != c.want {
			t.Errorf("%s: первая строка = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDocCommentFull: полный комментарий идёт строка в строку, без «//», без
// разделителей и без пустых строк по краям; блок из одних параметров первой
// строки не даёт, а полный комментарий даёт.
func TestDocCommentFull(t *testing.T) {
	for _, c := range []struct {
		name, before, wantFirst, wantFull string
	}{
		{"описание с параметрами",
			"////////////\n// Возвращает цену.\n//\n// Параметры:\n//  Товар - СправочникСсылка.Товары - товар.\n//\n// Возвращаемое значение:\n//  Число - цена.\n//\n",
			"Возвращает цену.",
			"Возвращает цену.\n\nПараметры:\nТовар - СправочникСсылка.Товары - товар.\n\nВозвращаемое значение:\nЧисло - цена."},
		{"блок из одних параметров", "// Параметры:\n//  Товар - СправочникСсылка\n", "", "Параметры:\nТовар - СправочникСсылка"},
		{"CRLF и директива ниже", "// Возвращает цену.\r\n// На дату.\r\n", "Возвращает цену.", "Возвращает цену.\nНа дату."},
		{"комментария нет", "КонецПроцедуры\n\n", "", ""},
		{"один разделитель", "////////////\n", "", ""},
	} {
		first, full := docComment([]byte(c.before))
		if first != c.wantFirst || full != c.wantFull {
			t.Errorf("%s: docComment = (%q, %q), want (%q, %q)", c.name, first, full, c.wantFirst, c.wantFull)
		}
	}
	long := "// " + strings.Repeat("я", docMaxRunes+100) + "\n"
	if _, full := docComment([]byte(long)); len([]rune(full)) != docMaxRunes {
		t.Errorf("длинный комментарий: %d рун, want %d", len([]rune(full)), docMaxRunes)
	}
}

// TestRegionPath: путь идёт от внешней области к внутренней; смещение вне
// областей даёт пустой путь.
func TestRegionPath(t *testing.T) {
	span := func(start, end int) domain.Span { return domain.Span{StartByte: start, EndByte: end} }
	regions := []bsl.Region{
		{Name: "ПрограммныйИнтерфейс", Span: span(0, 100)},
		{Name: "Данные", Span: span(10, 40)},
		{Name: "Прочее", Span: span(50, 90)},
		{Name: "СлужебныеПроцедурыИФункции", Span: span(100, 200)},
	}
	for _, c := range []struct {
		offset int
		want   string
	}{
		{5, "ПрограммныйИнтерфейс"},
		{20, "ПрограммныйИнтерфейс/Данные"},
		{45, "ПрограммныйИнтерфейс"},
		{60, "ПрограммныйИнтерфейс/Прочее"},
		{100, "СлужебныеПроцедурыИФункции"},
		{250, ""},
	} {
		if got := regionPath(regions, c.offset); got != c.want {
			t.Errorf("regionPath(%d) = %q, want %q", c.offset, got, c.want)
		}
	}
}
