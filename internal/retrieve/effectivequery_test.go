package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

const effQueryTask = "Перепиши текст запроса в ДанныеОстатков"

// seedEffQueryFixture: общий модуль ОтчетыСервер с функцией ДанныеОстатков и
// расширение ext-a, которое перехватывает её через &ИзменениеИКонтроль и несёт
// СВОЙ текст запроса (с таблицей СкладскиеЯчейки, которой в базовом тексте
// нет). Для ИзменениеИКонтроль это и есть исполняемый запрос: базовый текст в
// effective-виде заменён текстом расширения.
//
// baseHasQuery=false: у базовой функции текста запроса нет вовсе, запрос есть
// только в перехватчике. raw честно говорит no_query_in_symbol; effective
// обязан найти запрос расширения, а не остановиться на базовой пустоте.
func seedEffQueryFixture(t *testing.T, st *store.Store, baseHasQuery bool) {
	t.Helper()
	rel := commonModulePath("ОтчетыСервер")
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		seedEffComponents(t, tx)
		baseBody := "Функция ДанныеОстатков() Экспорт\n\tЗапрос = Новый Запрос(\"ВЫБРАТЬ Остатки.Товар ИЗ РегистрНакопления.ОстаткиТоваров.Остатки КАК Остатки\");\nКонецФункции"
		sBase, fBase := seedEffMethod(t, tx, "cfg", rel, "ДанныеОстатков", baseBody)
		if baseHasQuery {
			q, err := tx.InsertQuery(store.Query{
				IdentityKey: "cfg\x00query\x00остатки", ComponentID: "cfg", SymbolID: sBase, FileID: fBase,
				Span: spanOf(40, 120), Staticity: "static", Confidence: 1,
				Text: "ВЫБРАТЬ Остатки.Товар ИЗ РегистрНакопления.ОстаткиТоваров.Остатки КАК Остатки",
			})
			if err != nil {
				return err
			}
			if err := tx.InsertQueryReference(store.QueryReference{
				QueryID: q, Kind: "table", NameNorm: "остаткитоваров", SpanStart: 30, SpanEnd: 60,
			}); err != nil {
				return err
			}
		}

		extBody := "&ИзменениеИКонтроль(\"ДанныеОстатков\")\nФункция Расш_ДанныеОстатков() Экспорт\n\tЗапрос = Новый Запрос(\"ВЫБРАТЬ Ячейки.Товар ИЗ Справочник.СкладскиеЯчейки КАК Ячейки\");\nКонецФункции"
		sExt, fExt := seedEffMethod(t, tx, effExt, rel, "Расш_ДанныеОстатков", extBody)
		q, err := tx.InsertQuery(store.Query{
			IdentityKey: effExt + "\x00query\x00ячейки", ComponentID: effExt, SymbolID: sExt, FileID: fExt,
			Span: spanOf(60, 140), Staticity: "static", Confidence: 1,
			Text: "ВЫБРАТЬ Ячейки.Товар ИЗ Справочник.СкладскиеЯчейки КАК Ячейки",
		})
		if err != nil {
			return err
		}
		return tx.InsertQueryReference(store.QueryReference{
			QueryID: q, Kind: "table", NameNorm: "складскиеячейки", SpanStart: 24, SpanEnd: 48,
		})
	})
	if err != nil {
		t.Fatalf("seedEffQueryFixture: %v", err)
	}
}

func snippetsOf(r Result, category, component string) []Snippet {
	var out []Snippet
	for _, s := range r.Snippets {
		if s.Category == category && s.Component == component {
			out = append(out, s)
		}
	}
	return out
}

func relationTo(r Result, to string) *Relation {
	for i := range r.Relations {
		if r.Relations[i].To == to {
			return &r.Relations[i]
		}
	}
	return nil
}

// TestEffectiveQueryInterceptorQueries: query под view=effective несёт
// перехватчик символа-анкера (query_intercepts) и запросы из модуля
// расширения в query_text/schema со слоем расширения. raw остаётся прежним.
func TestEffectiveQueryInterceptorQueries(t *testing.T) {
	t.Run("запрос в базе и в перехватчике", func(t *testing.T) {
		st := openFixtureStore(t)
		seedEffQueryFixture(t, st, true)

		raw := buildFor(t, st, Request{Task: effQueryTask, ProjectID: "p"})
		if raw.Intent.Primary != IntentQuery {
			t.Fatalf("Intent.Primary = %q, want %q", raw.Intent.Primary, IntentQuery)
		}
		if len(snippetsOf(raw, "query_text", "cfg")) != 1 {
			t.Fatalf("raw: ожидался один базовый query_text, получено %+v", raw.Snippets)
		}
		if n := len(snippetsOf(raw, "query_text", effExt)); n != 0 || len(signaturesOfKind(raw, "ИзменениеИКонтроль")) != 0 {
			t.Fatalf("view=raw не несёт фактов расширения: snippets=%+v signatures=%+v", raw.Snippets, raw.Signatures)
		}
		if relationTo(raw, "складскиеячейки") != nil {
			t.Fatalf("view=raw не несёт схемы запроса расширения: %+v", raw.Relations)
		}

		eff := buildFor(t, st, Request{Task: effQueryTask, ProjectID: "p", View: "effective"})
		if hasWarning(eff, "effective_view_partial_coverage") {
			t.Fatalf("query под effective не строится как raw, предупреждения быть не должно: %+v", eff.Warnings)
		}
		ics := signaturesOfKind(eff, "ИзменениеИКонтроль")
		if len(ics) != 1 || ics[0].Component != effExt || !strings.Contains(ics[0].Text, "Расш_ДанныеОстатков") {
			t.Fatalf("effective: ожидался перехватчик &ИзменениеИКонтроль из %s, получено %+v", effExt, ics)
		}
		extQ := snippetsOf(eff, "query_text", effExt)
		if len(extQ) != 1 || !strings.Contains(extQ[0].Text, "СкладскиеЯчейки") {
			t.Fatalf("effective: ожидался query_text перехватчика со слоем %s, получено %+v", effExt, eff.Snippets)
		}
		if !strings.Contains(extQ[0].WhyIncluded, "ИзменениеИКонтроль") {
			t.Errorf("WhyIncluded запроса расширения обязан называть перехват: %q", extQ[0].WhyIncluded)
		}
		if len(snippetsOf(eff, "query_text", "cfg")) != 1 {
			t.Fatalf("effective: базовый query_text обязан остаться, получено %+v", eff.Snippets)
		}
		rel := relationTo(eff, "складскиеячейки")
		if rel == nil || rel.Component != effExt {
			t.Fatalf("effective: схема запроса расширения обязана нести слой %s: %+v", effExt, rel)
		}
	})

	t.Run("запрос только в перехватчике", func(t *testing.T) {
		st := openFixtureStore(t)
		seedEffQueryFixture(t, st, false)

		raw := buildFor(t, st, Request{Task: effQueryTask, ProjectID: "p"})
		if !hasWarning(raw, "no_query_in_symbol") {
			t.Fatalf("raw: у базовой функции запросов нет, ожидался no_query_in_symbol: %+v", raw.Warnings)
		}

		eff := buildFor(t, st, Request{Task: effQueryTask, ProjectID: "p", View: "effective"})
		if hasWarning(eff, "no_query_in_symbol") {
			t.Fatalf("effective: запрос есть в перехватчике, no_query_in_symbol был бы ложью: %+v", eff.Warnings)
		}
		if extQ := snippetsOf(eff, "query_text", effExt); len(extQ) != 1 {
			t.Fatalf("effective: ожидался query_text перехватчика, получено %+v", eff.Snippets)
		}
		requireCoverageStatus(t, eff, "owner_symbol", CompleteInline)
	})
}

// TestEffectiveExchangeExtensionInterceptors: exchange и extension делят
// builder с bugfix и под effective несут настоящий факт перехвата анкера
// (interceptors), поэтому они effective-aware (ADR-035 п.7), а не «построены
// как raw». raw фактов перехвата не несёт.
func TestEffectiveExchangeExtensionInterceptors(t *testing.T) {
	cases := []struct{ intent, task string }{
		{IntentExchange, "Обмен по плану обмена падает в ДанныеОстатков"},
		{IntentExtension, "Перехватчик расширения для ДанныеОстатков"},
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			st := openFixtureStore(t)
			seedEffQueryFixture(t, st, true)
			raw := buildFor(t, st, Request{Task: tc.task, ProjectID: "p"})
			if raw.Intent.Primary != tc.intent {
				t.Fatalf("Intent.Primary = %q, want %q", raw.Intent.Primary, tc.intent)
			}
			if n := len(signaturesOfKind(raw, "ИзменениеИКонтроль")); n != 0 {
				t.Fatalf("raw не несёт фактов перехвата: %+v", raw.Signatures)
			}
			eff := buildFor(t, st, Request{Task: tc.task, ProjectID: "p", View: "effective"})
			ics := signaturesOfKind(eff, "ИзменениеИКонтроль")
			if len(ics) != 1 || ics[0].Component != effExt {
				t.Fatalf("effective: ожидался перехватчик анкера из %s, получено %+v", effExt, eff.Signatures)
			}
			if hasWarning(eff, "effective_view_partial_coverage") {
				t.Fatalf("intent %q учитывает расширения, предупреждение было бы ложью: %+v", tc.intent, eff.Warnings)
			}
		})
	}
}

// TestPartialCoverageWarningForUnlistedIntent: intent вне effectiveAwareIntent
// (новый, со своим builder'ом) получает effective_view_partial_coverage при
// view=effective и не получает при raw. Мутация «effectiveAwareIntent всегда
// true» красит этот тест: иначе предел покрытия исчез бы молча.
func TestPartialCoverageWarningForUnlistedIntent(t *testing.T) {
	const unlisted = "новый-intent"
	got := partialCoverageWarning("effective", unlisted)
	if len(got) != 1 || got[0].Code != "effective_view_partial_coverage" || !strings.Contains(got[0].Message, unlisted) {
		t.Fatalf("partialCoverageWarning(effective, %q) = %+v, want одно предупреждение с именем intent", unlisted, got)
	}
	if w := partialCoverageWarning("raw", unlisted); len(w) != 0 {
		t.Fatalf("raw не несёт предупреждения: %+v", w)
	}
	for _, known := range []string{IntentBugfix, IntentUnknown, IntentSignatureChange, IntentForm, IntentPosting,
		IntentRegister, IntentQuery, IntentAddAttribute, IntentRights, IntentExchange, IntentExtension} {
		if w := partialCoverageWarning("effective", known); len(w) != 0 {
			t.Fatalf("intent %q effective-aware, предупреждение лишнее: %+v", known, w)
		}
	}
}
