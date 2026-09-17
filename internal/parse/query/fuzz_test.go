package query

import (
	"strings"
	"testing"
)

// TestParseGarbageDoesNotPanic — обрывки и мусор не роняют разбор. Не
// исчерпывающий список (для этого есть FuzzParse), а быстрая регрессионная
// проверка конкретных форм обрывков, которые тривиально предсказать заранее:
// незакрытые скобки, незакрытые строки, одинокие ключевые слова, обрыв
// посередине виртуальной таблицы.
func TestParseGarbageDoesNotPanic(t *testing.T) {
	cases := []string{
		"",
		"ВЫБРАТЬ",
		"ВЫБРАТЬ ИЗ",
		"ИЗ ГДЕ ПОМЕСТИТЬ УНИЧТОЖИТЬ",
		"ВЫБРАТЬ Ссылка ИЗ Справочник.Номенклатура.Остатки(",
		"ВЫБРАТЬ Ссылка ИЗ (ВЫБРАТЬ Ссылка ИЗ Справочник.А",
		`ВЫБРАТЬ "незакрытая строка ИЗ Справочник.А`,
		"&",
		"&.",
		"...",
		")))(((",
		"ВЫБРАТЬ &Параметр.Реквизит ИЗ &Параметр2",
		strings.Repeat("(", 500) + "ВЫБРАТЬ Ссылка ИЗ Справочник.А" + strings.Repeat(")", 500),
		"ВЫБРАТЬ Ссылка" + GapMarker + GapMarker + "ИЗ" + GapMarker,
		"\x00\x00\x00",
		"ВЫБРАТЬ * ИЗ Справочник.Номенклатура",
		"СГРУППИРОВАТЬ ПО УПОРЯДОЧИТЬ ПО ИТОГИ ОБЪЕДИНИТЬ ВСЕ",
	}
	for _, text := range cases {
		text := text
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("паника на входе %q: %v", text, r)
				}
			}()
			q, diags := Parse(text)
			if q == nil {
				t.Fatalf("Parse(%q) вернул nil *Query", text)
			}
			_ = diags
		})
	}
}

// FuzzParse — Parse не паникует ни на каком байтовом входе, включая
// невалидный UTF-8, и никогда не нарушает инвариант domain.Provenance для
// собственного результата.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"ВЫБРАТЬ Ссылка ИЗ Справочник.Номенклатура",
		"SELECT a.b AS c FROM Catalog.Items AS a WHERE a.x = &p",
		"ВЫБРАТЬ Сумма ИЗ РегистрНакопления.Продажи.Обороты(&Нач, &Кон)",
		"ВЫБРАТЬ Ссылка ПОМЕСТИТЬ ВТ_X ИЗ Справочник.А; ВЫБРАТЬ Ссылка ИЗ ВТ_X",
		"ВЫБРАТЬ Ссылка ИЗ (ВЫБРАТЬ Ссылка ИЗ Справочник.А) КАК П",
		"\x00\x00",
		"",
		`ВЫБРАТЬ "текст с ""кавычками""" ИЗ Справочник.А`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		q, _ := Parse(text)
		if q == nil {
			t.Fatalf("Parse(%q) вернул nil *Query", text)
		}
		if err := q.Provenance.Validate(q.Confidence); err != nil {
			t.Fatalf("Parse(%q): нарушен инвариант Provenance/Confidence: %v (staticity=%s confidence=%v)",
				text, err, q.Staticity, q.Confidence)
		}
	})
}
