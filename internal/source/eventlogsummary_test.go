package source

import (
	"strings"
	"testing"
)

// TestSummarizeEventLog: repeats of one error that differ only in numbers,
// references and time fall into one group with the count and the first and last
// date; different errors stay apart; the most frequent comes first.
func TestSummarizeEventLog(t *testing.T) {
	entries := []EventLogEntry{
		{Date: "2026-11-01T10:00:00", Level: "Ошибка", Event: "Данные. Проведение", Metadata: "Документ.Счет",
			Comment: "{Документ.Счет.МодульОбъекта(120)}: Не заполнен контрагент в документе Счет 0000-000123 от 01.11.2026"},
		{Date: "2026-11-03T09:30:00", Level: "Ошибка", Event: "Данные. Проведение", Metadata: "Документ.Счет",
			Comment: "{Документ.Счет.МодульОбъекта(120)}: Не заполнен контрагент в документе Счет 0000-000177 от 03.11.2026"},
		{Date: "2026-11-02T08:00:00", Level: "Ошибка", Event: "Данные. Проведение", Metadata: "Документ.Счет",
			Comment: "{Документ.Счет.МодульОбъекта(120)}: Не заполнен контрагент в документе Счет 0000-000150 от 02.11.2026"},
		{Date: "2026-11-02T12:00:00", Level: "Ошибка", Event: "Обмен", Metadata: "",
			Comment: "Объект 6042d8d4-a3d7-4285-9eb4-ae8a86aa2e9d не найден"},
		{Date: "2026-11-02T12:00:00", Level: "Предупреждение", Event: "Данные. Проведение", Metadata: "Документ.Счет",
			Comment: "{Документ.Счет.МодульОбъекта(120)}: Не заполнен контрагент в документе Счет 0000-000150 от 02.11.2026"},
	}
	groups := SummarizeEventLog(entries)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3: %+v", len(groups), groups)
	}
	top := groups[0]
	if top.Count != 3 || top.Level != "Ошибка" || top.Metadata != "Документ.Счет" {
		t.Fatalf("top group = %+v, want the three repeats of the error", top)
	}
	if top.FirstDate != "2026-11-01T10:00:00" || top.LastDate != "2026-11-03T09:30:00" {
		t.Errorf("dates = %s..%s, want 2026-11-01T10:00:00..2026-11-03T09:30:00", top.FirstDate, top.LastDate)
	}
	if !strings.Contains(top.Comment, "0000-000177") {
		t.Errorf("comment = %q, want the one of the latest entry", top.Comment)
	}
	for _, g := range groups[1:] {
		if g.Count != 1 {
			t.Errorf("group %+v, want a single entry: a different level or event is a different group", g)
		}
	}
}

func TestSummarizeEventLogCutsComment(t *testing.T) {
	groups := SummarizeEventLog([]EventLogEntry{{Date: "2026-11-01T10:00:00", Comment: strings.Repeat("я", 2000)}})
	if n := len([]rune(groups[0].Comment)); n != summaryCommentRunes+1 {
		t.Errorf("comment runes = %d, want %d plus the ellipsis", n, summaryCommentRunes)
	}
}
