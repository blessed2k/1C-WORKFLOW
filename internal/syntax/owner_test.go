package syntax

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
)

// TestLookupOwner covers the owner-aware lookup on the fixture: the owner
// filter, the Type.Member form, the members listing and the type report.
func TestLookupOwner(t *testing.T) {
	ix := loadFixture(t)
	for _, tc := range []struct {
		name        string
		query       string
		owner       string
		limit       int
		wantOwner   string
		wantMatches []string // "owner/name" of every match, in order
		wantMembers []string // "kind:name" of every member, in order
		wantTotal   int
		wantTrunc   bool
		wantType    string // TypeInfo.Name, "" for none
		wantCtors   int
		wantTypeN   int    // TypeInfo.Members
		wantNote    string // substring of Note, "" for none
	}{
		{
			name: "owner narrows a shared method name", query: "Свернуть", owner: "ТаблицаЗначений",
			wantOwner: "ТаблицаЗначений", wantMatches: []string{"ТаблицаЗначений/Свернуть"},
		},
		{
			name: "without owner both same-named methods come back", query: "Свернуть",
			wantMatches: []string{"ТаблицаЗначений/Свернуть", "ТаблицаФормы/Свернуть"},
		},
		{
			name: "owner is case-insensitive", query: "найти", owner: "таблицазначений",
			wantOwner: "ТаблицаЗначений", wantMatches: []string{"ТаблицаЗначений/Найти"},
		},
		{
			name: "Type.Member equals owner+query", query: "ТаблицаЗначений.Свернуть",
			wantOwner: "ТаблицаЗначений", wantMatches: []string{"ТаблицаЗначений/Свернуть"},
		},
		{
			name: "Type.Member by English member name", query: "HTTPСоединение.CallHTTPMethod",
			wantOwner: "HTTPСоединение", wantMatches: []string{"HTTPСоединение/ВызватьHTTPМетод"},
		},
		{
			name: "owner with a dot in its name", query: "ВедущиеВидыРасчета.<Имя плана видов расчета>.Количество",
			wantOwner: "ВедущиеВидыРасчета.<Имя плана видов расчета>", wantMatches: []string{"ВедущиеВидыРасчета.<Имя плана видов расчета>/Количество"},
		},
		{
			name: "dot with an unknown left part searches as before", query: "НетТакогоТипа.Свернуть",
		},
		{
			name: "members listing, by kind then name", owner: "ТаблицаЗначений",
			wantOwner: "ТаблицаЗначений",
			wantMembers: []string{"constructor:По умолчанию", "method:Найти", "method:Свернуть",
				"property:Колонки", "event:ПриИзменении"},
			wantTotal: 5,
		},
		{
			name: "Type. with nothing after the dot lists members", query: "HTTPСоединение.",
			wantOwner:   "HTTPСоединение",
			wantMembers: []string{"constructor:По указанному серверу", "constructor:По умолчанию", "method:ВызватьHTTPМетод"},
			wantTotal:   3,
		},
		{
			name: "members listing cut at limit", owner: "ТаблицаЗначений", limit: 2,
			wantOwner: "ТаблицаЗначений", wantMembers: []string{"constructor:По умолчанию", "method:Найти"},
			wantTotal: 5, wantTrunc: true,
		},
		{
			name: "unknown owner names similar ones", query: "Свернуть", owner: "Таблица",
			wantNote: "ТаблицаЗначений",
		},
		{
			name: "query that is a type reports it with constructors", query: "HTTPСоединение",
			wantType: "HTTPСоединение", wantCtors: 2, wantTypeN: 3,
		},
		{
			name: "global function unchanged, no type", query: "СтрНайти",
			wantMatches: []string{"Глобальный контекст/СтрНайти"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ix.Lookup(tc.query, tc.owner, tc.limit)
			if got.Owner != tc.wantOwner {
				t.Errorf("Owner = %q, want %q", got.Owner, tc.wantOwner)
			}
			var matches []string
			for _, e := range got.Matches {
				matches = append(matches, e.Owner+"/"+e.NameRu)
			}
			if strings.Join(matches, "|") != strings.Join(tc.wantMatches, "|") {
				t.Errorf("Matches = %v, want %v", matches, tc.wantMatches)
			}
			var members []string
			for _, m := range got.Members {
				members = append(members, m.Kind+":"+m.NameRu)
			}
			if strings.Join(members, "|") != strings.Join(tc.wantMembers, "|") {
				t.Errorf("Members = %v, want %v", members, tc.wantMembers)
			}
			if got.Total != tc.wantTotal || got.Truncated != tc.wantTrunc {
				t.Errorf("Total/Truncated = %d/%v, want %d/%v", got.Total, got.Truncated, tc.wantTotal, tc.wantTrunc)
			}
			switch {
			case tc.wantType == "" && got.Type != nil:
				t.Errorf("Type = %+v, want none", got.Type)
			case tc.wantType != "" && got.Type == nil:
				t.Errorf("Type missing, want %q", tc.wantType)
			case tc.wantType != "":
				if got.Type.Name != tc.wantType || len(got.Type.Constructors) != tc.wantCtors || got.Type.Members != tc.wantTypeN {
					t.Errorf("Type = %+v, want %q with %d constructors and %d members", got.Type, tc.wantType, tc.wantCtors, tc.wantTypeN)
				}
				if !strings.Contains(got.Type.Hint, "owner=") {
					t.Errorf("Type.Hint does not say how to list members: %q", got.Type.Hint)
				}
			}
			if (tc.wantNote == "") != (got.Note == "") || !strings.Contains(got.Note, tc.wantNote) {
				t.Errorf("Note = %q, want one containing %q", got.Note, tc.wantNote)
			}
		})
	}
}

// TestLookupWithoutOwnerIsSearch: the old call shape answers exactly as Search.
func TestLookupWithoutOwnerIsSearch(t *testing.T) {
	ix := loadFixture(t)
	for _, q := range []string{"Сообщить", "Найти", "Количество", "ЗначениеЗаполнено", "Если", "Свернуть"} {
		got, want := names(ix.Lookup(q, "", 5).Matches), names(ix.Search(q, 5))
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("Lookup(%q) = %v, Search = %v", q, got, want)
		}
	}
}

// TestCompactMember: the members listing carries the type in one line, the first
// description line, and neither parameters nor the rest of the description.
func TestCompactMember(t *testing.T) {
	ix := loadFixture(t)
	byName := map[string]Member{}
	for _, m := range ix.Lookup("", "ТаблицаЗначений", 0).Members {
		byName[m.Kind+":"+m.NameRu] = m
	}
	for _, tc := range []struct {
		key, typ, summary, sig string
	}{
		{"method:Найти", "СтрокаТаблицыЗначений, Неопределено", "Синтетическая запись фикстуры: поиск строки, одноимённый методу массива.", "Найти(<Значение>, <Колонки>)"},
		{"property:Колонки", "КоллекцияКолонокТаблицыЗначений", "Синтетическая запись фикстуры: колонки таблицы.", ""},
		{"method:Свернуть", "", "Синтетическая запись фикстуры: свёртка таблицы по колонкам.", "Свернуть(<КолонкиГруппировок>, <КолонкиСуммирования>)"},
		{"constructor:По умолчанию", "", "", "Новый ТаблицаЗначений"},
	} {
		m, ok := byName[tc.key]
		if !ok {
			t.Errorf("%s: not listed", tc.key)
			continue
		}
		if m.Type != tc.typ || m.Summary != tc.summary || m.Signature != tc.sig {
			t.Errorf("%s = %+v, want type %q, summary %q, signature %q", tc.key, m, tc.typ, tc.summary, tc.sig)
		}
	}
	for _, m := range ix.Lookup("", "HTTPСоединение", 0).Members {
		if m.Kind == "constructor" && m.NameEn != "" {
			t.Errorf("constructor %q keeps the internal id %q as its English name", m.NameRu, m.NameEn)
		}
	}
	long := compact(onec.SyntaxEntry{NameRu: "Длинное", Description: strings.Repeat("я", summaryRunes+50)})
	if n := len([]rune(long.Summary)); n != summaryRunes+1 {
		t.Errorf("summary of a long line has %d runes, want %d (cap plus the ellipsis)", n, summaryRunes+1)
	}
}
