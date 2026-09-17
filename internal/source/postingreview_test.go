package source

import (
	"context"
	"strings"
	"testing"
)

func prSource(t *testing.T) *XMLSource {
	t.Helper()
	return NewXMLSource("testdata/pr")
}

func findingCodes(rep *PostingReport) map[string]bool {
	out := map[string]bool{}
	for _, f := range rep.Findings {
		out[f.Code] = true
	}
	return out
}

func TestPostingReviewInlineDefects(t *testing.T) {
	rep, err := prSource(t).PostingReview(context.Background(), "РасходТовара")
	if err != nil {
		t.Fatalf("PostingReview: %v", err)
	}
	if rep.Style != postingInline {
		t.Fatalf("style = %q, want inline", rep.Style)
	}
	codes := findingCodes(rep)
	for _, want := range []string{"BalanceReadWithoutLock", "QueryInLoop", "NoWriteFlag"} {
		if !codes[want] {
			t.Errorf("finding %q missing: %+v", want, rep.Findings)
		}
	}
	// Findings must point at a real line of the module.
	for _, f := range rep.Findings {
		if f.Line <= 0 {
			t.Errorf("finding without a line: %+v", f)
		}
	}
}

// TestPostingReviewDelegated pins the calibration that matters most: in a
// configuration on БСП posting is delegated (168 of 280 documents in УТ), and
// the inline rules must stay silent there. Touching ДополнительныеСвойства of a
// record set is passing options to the mechanism, not forming movements.
func TestPostingReviewDelegated(t *testing.T) {
	rep, err := prSource(t).PostingReview(context.Background(), "ЗаказПоставщику")
	if err != nil {
		t.Fatalf("PostingReview: %v", err)
	}
	if rep.Style != postingDelegated {
		t.Errorf("style = %q, want delegated", rep.Style)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("delegated posting must not produce inline findings: %+v", rep.Findings)
	}
	if rep.Note == "" {
		t.Error("delegated posting must explain where the movements are formed")
	}
	if len(rep.Handlers) != 2 {
		t.Errorf("handlers = %v, want both posting handlers", rep.Handlers)
	}
}

func TestPostingReviewErrors(t *testing.T) {
	s := prSource(t)
	if _, err := s.PostingReview(context.Background(), ""); err == nil {
		t.Error("empty name must be an error")
	}
	if _, err := s.PostingReview(context.Background(), "../../etc/passwd"); err == nil {
		t.Error("a path must be rejected")
	}
	if _, err := s.PostingReview(context.Background(), "НетТакого"); err == nil {
		t.Error("unknown document must be an error")
	}
}

// TestPostingStyleByMethodName pins the classification defect that made the
// report lie on every seventh document of УТ: delegation was recognised by the
// module name, so ИнтеграцияИСПереопределяемый.ОбработкаПроведения counted as
// "no movements at all". Recognising it by the method name moved 38 documents
// from none to delegated (168 -> 206).
func TestPostingStyleByMethodName(t *testing.T) {
	cases := map[string]string{
		"	ИнтеграцияИСПереопределяемый.ОбработкаПроведения(ЭтотОбъект, Отказ);":              postingDelegated,
		"	ИнтеграцияИС.ЗаписатьНаборыЗаписей(ЭтотОбъект);":                                   postingDelegated,
		"	ОстаткиАлкогольнойПродукцииЕГАИС.ОтразитьДвижения(ЭтотОбъект, Отказ);":             postingDelegated,
		"	ПрослеживаемостьПереопределяемый.ПодготовитьНаборыЗаписейКПроведению(ЭтотОбъект);": postingDelegated,
		"	Движение = Движения.ТоварыНаСкладах.Добавить();":                                   postingInline,
		"	СообщитьПользователю(\"Готово\");":                                                 postingNone,
	}
	for line, want := range cases {
		h := handlerBody{name: "ОбработкаПроведения", start: 1, lines: []string{"Процедура ОбработкаПроведения(Отказ, РежимПроведения)", line, "КонецПроцедуры"}}
		if got := handlerStyle(h); got != want {
			t.Errorf("handlerStyle(%q) = %q, want %q", strings.TrimSpace(line), got, want)
		}
	}
}

// TestPostingLockMustPrecedeRead pins that a lock taken AFTER the balances are
// read is exactly the race the rule looks for, and that a lock mentioned in a
// string literal is not a lock.
func TestPostingLockMustPrecedeRead(t *testing.T) {
	read := "	|	РегистрНакопления.ТоварыНаСкладах.Остатки КАК Остатки\";"
	lock := "	Блокировка = Новый УправлениеБлокировкойДанных;"
	fake := "	ВызватьИсключение \"Не используйте УправлениеБлокировкойДанных здесь\";"
	fill := "	Движение = Движения.ТоварыНаСкладах.Добавить();"

	build := func(body ...string) handlerBody {
		lines := append([]string{"Процедура ОбработкаПроведения(Отказ, РежимПроведения)"}, body...)
		return handlerBody{name: "ОбработкаПроведения", start: 1, lines: append(lines, "КонецПроцедуры")}
	}
	has := func(fs []PostingFinding, code string) bool {
		for _, f := range fs {
			if f.Code == code {
				return true
			}
		}
		return false
	}
	if !has(reviewHandler(build(read, lock, fill), postingInline, nil, ""), "BalanceReadWithoutLock") {
		t.Error("lock taken after the read must still be reported: that is the race")
	}
	if has(reviewHandler(build(lock, read, fill), postingInline, nil, ""), "BalanceReadWithoutLock") {
		t.Error("lock taken before the read must silence the rule")
	}
	if !has(reviewHandler(build(fake, read, fill), postingInline, nil, ""), "BalanceReadWithoutLock") {
		t.Error("a lock named inside a string literal is not a lock")
	}
}

// TestPostingBatchQueryInLoop pins that the batch idiom counts: ВыполнитьПакет
// appears 154 times in the document modules of УТ, and the rule used to see none
// of them.
func TestPostingBatchQueryInLoop(t *testing.T) {
	h := handlerBody{name: "ОбработкаПроведения", start: 1, lines: []string{
		"Процедура ОбработкаПроведения(Отказ, РежимПроведения)",
		"	Для Каждого Строка Из Товары Цикл",
		"		Результат = Запрос.ВыполнитьПакет();",
		"	КонецЦикла;",
		"КонецПроцедуры",
	}}
	found := false
	for _, f := range reviewHandler(h, postingInline, nil, "") {
		if f.Code == "QueryInLoop" {
			found = true
		}
	}
	if !found {
		t.Error("ВыполнитьПакет inside a loop must be reported")
	}
}

// TestPostingWrittenExplicitly pins that writing a set explicitly is as good as
// raising the flag: Движения.X.Записать() is legal and used in УТ.
func TestPostingWrittenExplicitly(t *testing.T) {
	h := handlerBody{name: "ОбработкаПроведения", start: 1, lines: []string{
		"Процедура ОбработкаПроведения(Отказ, РежимПроведения)",
		"	Движение = Движения.ТоварыНаСкладах.Добавить();",
		"	Движения.ТоварыНаСкладах.Записать();",
		"КонецПроцедуры",
	}}
	movements := &MovementsReport{Registers: []RegisterMovement{{Register: "РегистрНакопления.ТоварыНаСкладах", Declared: true, UsedInCode: true}}}
	for _, f := range reviewHandler(h, postingInline, movements, "") {
		if f.Code == "NoWriteFlag" {
			t.Errorf("an explicitly written set must not be reported: %+v", f)
		}
	}
}

// TestPostingFollowsCallsInsideModule: splitting posting out of the handler into
// a private procedure of the same module is an ordinary refactor — real industry configurations
// do exactly that, with a handler of three calls and
// every movement lives one level down. Reading only the handler body classified
// such a document as "движений нет" and skipped every rule below in silence,
// which is worse than a false positive: nothing is reported and nothing looks wrong.
func TestPostingFollowsCallsInsideModule(t *testing.T) {
	rep, err := prSource(t).PostingReview(context.Background(), "СписаниеТовара")
	if err != nil {
		t.Fatalf("PostingReview: %v", err)
	}
	if rep.Style != postingInline {
		t.Fatalf("style = %q, want %q: the movements are one call away", rep.Style, postingInline)
	}
	// The rules must actually run on the nested body, not merely classify it.
	var balance *PostingFinding
	for i, f := range rep.Findings {
		if f.Code == "BalanceReadWithoutLock" {
			balance = &rep.Findings[i]
		}
	}
	if balance == nil {
		t.Fatalf("balance read without a lock must be reported: %+v", rep.Findings)
	}
	// The line must point into the nested procedure, where the read really is.
	// An offset counted from the handler start would land on line 4 or earlier.
	if balance.Line < 9 {
		t.Errorf("line = %d, want the read inside СформироватьДвижения (line 14 of the module)", balance.Line)
	}
}
