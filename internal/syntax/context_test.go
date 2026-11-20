package syntax_test

import (
	"sort"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// TestArity: the count a validator judges calls by. СтрШаблон's range
// parameter and Мин's open list are the false WrongArgCount of the old count;
// a signature whose parameter list does not match it, and an example that
// calls the method another way, give no count at all.
func TestArity(t *testing.T) {
	cases := []struct {
		name     string
		e        onec.SyntaxEntry
		req, max int
		ok       bool
	}{
		{"range parameter", onec.SyntaxEntry{
			NameRu:    "СтрШаблон",
			Signature: "СтрШаблон(<Шаблон>, <Значение1-Значение10>)",
			Params:    "<Шаблон> (обязательный)Тип: Строка.\n<Значение1-Значение10> (необязательный)Тип: Произвольный.",
			Example:   `СтрШаблон("Ошибка в данных в строке %1 (требуется тип %2)", 2, "Дата")`,
		}, 1, 11, true},
		{"open list", onec.SyntaxEntry{
			NameRu:    "Мин",
			Signature: "Мин(<Значение1,...,ЗначениеN>)",
			Params:    "<Значение1,...,ЗначениеN> (обязательный)Тип: Число.",
			Example:   "A = Мин(1, 2, 3);",
		}, 1, -1, true},
		{"plain", onec.SyntaxEntry{
			NameRu:    "Окр",
			Signature: "Окр(<Число>, <Разрядность>, <РежимОкругления>)",
			Params:    "<Число> (обязательный)\n<Разрядность> (необязательный)\n<РежимОкругления> (необязательный)",
		}, 1, 3, true},
		{"no parameters", onec.SyntaxEntry{NameRu: "ТекущаяДата", Signature: "ТекущаяДата()"}, 0, 0, true},
		{"nested structure fields", onec.SyntaxEntry{
			NameRu:    "ПолучитьДанныеВыбора",
			Signature: "ПолучитьДанныеВыбора(<ТипЗначения>, <Параметры>)",
			Params:    "<ТипЗначения> (обязательный)\n<Параметры> (обязательный)\n<СтрокаПоиска> (необязательный)",
		}, 0, 0, false},
		{"another variant in the example", onec.SyntaxEntry{
			NameRu:    "ПолучитьНавигационнуюСсылку",
			Signature: "ПолучитьНавигационнуюСсылку(<Объект>, <Представление>, <ВнешниеДанные>)",
			Params:    "<Объект> (обязательный)\n<Представление> (обязательный)\n<ВнешниеДанные> (обязательный)",
			Example:   "Адрес1 = ПолучитьНавигационнуюСсылку(Объект.Ссылка);",
		}, 0, 0, false},
	}
	for _, c := range cases {
		req, max, ok := syntax.Arity(c.e)
		if ok != c.ok || (ok && (req != c.req || max != c.max)) {
			t.Errorf("%s: Arity = (%d, %d, %v), want (%d, %d, %v)", c.name, req, max, ok, c.req, c.max, c.ok)
		}
	}
}

// TestGlobalPropertyAndMembers: the fixture knows its global properties and
// the members of its types; a placeholder member is not a name.
func TestGlobalPropertyAndMembers(t *testing.T) {
	ix := syntaxtest.Fixture(t)
	if _, ok := ix.GlobalMethod("Сообщить"); !ok {
		t.Fatal("the fixture's global method is not found through the name map")
	}
	if _, ok := ix.GlobalProperty("Сообщить"); ok {
		t.Error("a method is not a property")
	}
	members := ix.MemberNames("ТаблицаЗначений")
	if len(members) == 0 {
		t.Fatal("no members for ТаблицаЗначений")
	}
	for n := range members {
		if n == "" || n[0] == '<' {
			t.Errorf("placeholder member %q leaked", n)
		}
	}
}

// TestRealArityGuardSurvey lists the global methods whose count the example
// guard turns off, so a change of the guard is seen by name.
func TestRealArityGuardSurvey(t *testing.T) {
	ix := syntaxtest.RealOrSkip(t)
	var unknown []string
	for _, name := range ix.GlobalMethodNames() {
		e, _ := ix.GlobalMethod(name)
		if _, _, ok := syntax.Arity(e); !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	t.Logf("global methods without a judged count: %d: %v", len(unknown), unknown)
	for _, must := range []string{"ПолучитьНавигационнуюСсылку"} {
		found := false
		for _, n := range unknown {
			found = found || n == must
		}
		if !found {
			t.Errorf("%s must not have a judged count", must)
		}
	}
	e, _ := ix.GlobalMethod("СтрШаблон")
	if req, max, ok := syntax.Arity(e); !ok || req != 1 || max != 11 {
		t.Errorf("СтрШаблон: (%d, %d, %v)", req, max, ok)
	}
	if p, ok := ix.GlobalProperty("Документы"); !ok || p.Availability == "" {
		t.Errorf("Документы is a global property with availability: %+v", p)
	}
}
