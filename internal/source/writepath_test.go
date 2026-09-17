package source

import (
	"context"
	"strings"
	"testing"
)

// wpSource opens the write-path fixture.
func wpSource(t *testing.T) *XMLSource {
	t.Helper()
	return NewXMLSource("testdata/wp")
}

// stepsOf returns the steps of one kind, as "event|source" strings.
func stepsOf(rep *WritePathReport, kind string) []string {
	var out []string
	for _, s := range rep.Steps {
		if s.Kind == kind {
			out = append(out, s.Event+"|"+s.Source)
		}
	}
	return out
}

func hasWarning(rep *WritePathReport, code string) bool {
	for _, w := range rep.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestWritePathOrdersEvents(t *testing.T) {
	rep, err := wpSource(t).WritePath(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	if rep.Object != "Документ.ЗаказКлиента" {
		t.Errorf("object = %q", rep.Object)
	}
	// ПередЗаписью must come before ОбработкаПроведения, and the object module
	// handler of an event before the subscriptions on that same event.
	var seq []string
	for _, s := range rep.Steps {
		seq = append(seq, s.Event+":"+s.Kind)
	}
	joined := strings.Join(seq, " ")
	wantOrder := []string{
		"ПередЗаписью:objectModule",
		"ПередЗаписью:subscription",
		"ОбработкаПроведения:objectModule",
		"ОбработкаПроведения:movements",
	}
	pos := -1
	for _, want := range wantOrder {
		at := strings.Index(joined, want)
		if at < 0 {
			t.Fatalf("step %q missing in %q", want, joined)
		}
		if at <= pos {
			t.Errorf("step %q out of order in %q", want, joined)
		}
		pos = at
	}
	// Order numbers are contiguous from 1.
	for i, s := range rep.Steps {
		if s.Order != i+1 {
			t.Errorf("step %d has order %d", i, s.Order)
		}
	}
}

func TestWritePathFindsSubscriptions(t *testing.T) {
	rep, err := wpSource(t).WritePath(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	got := stepsOf(rep, "subscription")
	joined := strings.Join(got, " ")
	// Every source form must be found: a concrete type, a concrete type that is
	// not first in the list and carries a d5p1: prefix, a bare TypeSet kind that
	// fires for every document, and a DefinedType that expands to this document.
	for _, want := range []string{
		"ЗаказКлиентаПередЗаписью", // <v8:Type>
		"УведомитьОЗаказе",         // <v8:Type>, second in list, d5p1: prefix
		"ВсеДокументыПриЗаписи",    // <v8:TypeSet>cfg:DocumentObject
		"ФайлыПередЗаписью",        // <v8:TypeSet>cfg:DefinedType.X
		"CommonModule.Уведомления.ОтправитьУведомление",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("subscription %q missing in %v", want, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("subscriptions = %d, want 5: %v", len(got), got)
	}
}

// TestWritePathRecordSetModule pins the module file name: a register keeps its
// handlers in RecordSetModule.bsl, and reading ObjectModule.bsl there loses the
// whole module.
func TestWritePathRecordSetModule(t *testing.T) {
	rep, err := wpSource(t).WritePath(context.Background(), "InformationRegister", "ЦеныНоменклатуры")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	got := stepsOf(rep, "objectModule")
	if len(got) != 1 || !strings.Contains(got[0], "ПередЗаписью") {
		t.Errorf("register module handlers = %v, want ПередЗаписью", got)
	}
}

// TestWritePathCaseInsensitiveType guards against a lower-cased type silently
// producing a half-filled report.
func TestWritePathCaseInsensitiveType(t *testing.T) {
	s := wpSource(t)
	upper, err := s.WritePath(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	lower, err := s.WritePath(context.Background(), "document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("WritePath lower: %v", err)
	}
	if len(lower.Steps) != len(upper.Steps) {
		t.Errorf("lower-cased type gave %d steps, canonical gave %d", len(lower.Steps), len(upper.Steps))
	}
}

func TestWritePathRejectsPaths(t *testing.T) {
	if _, err := wpSource(t).WritePath(context.Background(), "Document", "../../etc/passwd"); err == nil {
		t.Error("a path in the object name must be rejected")
	}
}

// TestMutatesData pins the rule that decides whether a handler changes data:
// "=" is comparison as well as assignment, and commented-out or quoted code is
// not code.
func TestMutatesData(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"присваивание", "Источник.Комментарий = \"x\";", true},
		{"сравнение в условии", "Если Источник.Проведен = Истина Тогда\nКонецЕсли;", false},
		{"сравнение через И", "Если Источник.ПометкаУдаления = Ложь И Источник.Дата = ТекущаяДата() Тогда\nКонецЕсли;", false},
		{"строка табличной части", "СтрокаТЧ = Источник.Товары.Добавить();", true},
		{"очистка табличной части", "Источник.Товары.Очистить();", true},
		{"через переменную", "ОбъектДокумента = Источник;\nОбъектДокумента.Комментарий = \"x\";", true},
		{"запись источника", "Источник.Записать();", true},
		{"только чтение", "Значение = Источник.Комментарий;", false},
		{"закомментировано", "// Источник.Комментарий = \"x\";", false},
		{"в строковом литерале", "Текст = \"Источник.Комментарий = 1\";", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mutatesData(cleanBSL(tc.body)); got != tc.want {
				t.Errorf("mutatesData(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestWritePathWarnsOnSubscriptionOrder(t *testing.T) {
	rep, err := wpSource(t).WritePath(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	// Two handlers on ПередЗаписью change the source data (ОтправитьУведомление и
	// ПересчитатьСуммы), so which of them wins is undefined.
	if !hasWarning(rep, "SubscriptionOrder") {
		t.Errorf("two data-changing subscriptions on ПередЗаписью must warn: %+v", rep.Warnings)
	}
	// A read-only stack of handlers must NOT warn: that is the noise this rule
	// was narrowed to avoid (92% of documents in a БСП configuration).
	reg, err := wpSource(t).WritePath(context.Background(), "InformationRegister", "ЦеныНоменклатуры")
	if err != nil {
		t.Fatalf("WritePath register: %v", err)
	}
	if hasWarning(reg, "SubscriptionOrder") {
		t.Errorf("register with no competing writers must not warn: %+v", reg.Warnings)
	}
}

func TestWritePathWarnsOnHandlerCode(t *testing.T) {
	rep, err := wpSource(t).WritePath(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	if !hasWarning(rep, "NoExchangeLoadCheck") {
		t.Errorf("handler without ОбменДанными.Загрузка must warn: %+v", rep.Warnings)
	}
	if !hasWarning(rep, "WriteInsideHandler") {
		t.Errorf("handler calling Источник.Записать() must warn: %+v", rep.Warnings)
	}
	// The clean handler must not be blamed.
	for _, w := range rep.Warnings {
		if w.Code == "NoExchangeLoadCheck" && strings.Contains(w.Where, "ПроверитьЛимит") {
			t.Errorf("handler that does check ОбменДанными.Загрузка was flagged: %+v", w)
		}
	}
}

func TestWritePathReportsExchangeRegistration(t *testing.T) {
	rep, err := wpSource(t).WritePath(context.Background(), "Document", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	got := stepsOf(rep, "exchangeRegistration")
	if len(got) != 1 || !strings.Contains(got[0], "ОбменСБухгалтерией") {
		t.Fatalf("exchange steps = %v", got)
	}
	// The document is in the plan with AutoRecord=Deny, but a subscription on its
	// write path calls ЗарегистрироватьИзменения — that is how БСП does it, and
	// warning here would fire on every object of a real configuration.
	if hasWarning(rep, "ManualExchangeRegistration") {
		t.Errorf("registration by subscription must silence the warning: %+v", rep.Warnings)
	}
	// The register is in the same plan with Deny and has no such subscription.
	reg, err := wpSource(t).WritePath(context.Background(), "InformationRegister", "ЦеныНоменклатуры")
	if err != nil {
		t.Fatalf("WritePath register: %v", err)
	}
	if !hasWarning(reg, "ManualExchangeRegistration") {
		t.Errorf("Deny without any registering subscription must warn: %+v", reg.Warnings)
	}
}

// TestWritePathRegistrationRulesSilenceWarning: a plan built on Конвертацию
// данных registers the object by its ППД rules, and no code is involved. The
// catalog sits in the same plan with the same Deny as the register above and
// has no registering subscription either — the only difference is that the
// rules cover it. Warning here says "объект не уйдёт в обмен" about an object
// that does, which is the expensive kind of wrong: it sends the reader writing
// registration code that already exists in the rules.
func TestWritePathRegistrationRulesSilenceWarning(t *testing.T) {
	rep, err := wpSource(t).WritePath(context.Background(), "Catalog", "Контрагенты")
	if err != nil {
		t.Fatalf("WritePath: %v", err)
	}
	if hasWarning(rep, "ManualExchangeRegistration") {
		t.Errorf("ППД registration rules must silence the warning: %+v", rep.Warnings)
	}
	// The step must still be reported, and say who actually registers.
	var detail string
	var found int
	for _, s := range rep.Steps {
		if s.Kind == "exchangeRegistration" {
			found++
			detail = s.Detail
		}
	}
	if found != 1 {
		t.Fatalf("exchange step must stay visible, got %d", found)
	}
	if !strings.Contains(detail, "правилами регистрации ППД") {
		t.Errorf("step must name the real registrar instead of demanding code: %q", detail)
	}
}

func TestWritePathUnknownObject(t *testing.T) {
	if _, err := wpSource(t).WritePath(context.Background(), "Document", "НетТакого"); err == nil {
		t.Error("missing object must be an error")
	}
	if _, err := wpSource(t).WritePath(context.Background(), "", ""); err == nil {
		t.Error("empty arguments must be an error")
	}
}

// TestObjectSuffix pins the register special case: registers subscribe as record
// sets, not as objects.
func TestObjectSuffix(t *testing.T) {
	cases := map[string]string{
		"Document":             "DocumentObject",
		"Catalog":              "CatalogObject",
		"InformationRegister":  "InformationRegisterRecordSet",
		"AccumulationRegister": "AccumulationRegisterRecordSet",
	}
	for in, want := range cases {
		if got := objectSuffix(in); got != want {
			t.Errorf("objectSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
