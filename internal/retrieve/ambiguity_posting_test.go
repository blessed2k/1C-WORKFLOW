package retrieve

import (
	"strings"
	"testing"
)

// hasAmbiguitySubject — есть ли в ответе неоднозначность по данному subject.
func hasAmbiguitySubject(r Result, subject string) bool {
	for _, a := range r.Ambiguities {
		if strings.EqualFold(a.Subject, subject) {
			return true
		}
	}
	return false
}

// TestPostingHandlerAmbiguitySuppressed: шум омонимов обработчика проведения. Условие
// узкое: intent posting И есть анкер-объект И subject — имя обработчика
// проведения. В контрольном вызове это снимает 32 одноимённых
// ОбработкаПроведения чужих документов; во всех остальных сочетаниях
// неоднозначность обязана остаться — «выключить ambiguities для posting»
// решением НЕ является.
func TestPostingHandlerAmbiguitySuppressed(t *testing.T) {
	const taskWithObject = "Почему при проведении документа Заказ ОбработкаПроведения не создаёт движения"

	t.Run("posting с анкером-объектом — шума нет", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingFixture(t, st, postingFixtureOpts{})
		res := buildFor(t, st, Request{Task: taskWithObject, ProjectID: "p"})
		if res.Intent.Primary != IntentPosting {
			t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentPosting)
		}
		var hasObjectAnchor bool
		for _, a := range res.Anchors {
			if a.Kind == "metadata_object" {
				hasObjectAnchor = true
			}
		}
		if !hasObjectAnchor {
			t.Fatalf("в ответе нет анкера-объекта — тест проверяет не то условие: %+v", res.Anchors)
		}
		if hasAmbiguitySubject(res, "ОбработкаПроведения") {
			t.Errorf("ambiguities несёт омонимов обработчика проведения: %+v", res.Ambiguities)
		}
	})

	t.Run("posting, другой subject — неоднозначность осталась", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingFixture(t, st, postingFixtureOpts{})
		res := buildFor(t, st, Request{
			Task: "Почему при проведении документа Заказ ЗаполнитьДвижения не создаёт движения", ProjectID: "p"})
		if res.Intent.Primary != IntentPosting {
			t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentPosting)
		}
		if !hasAmbiguitySubject(res, "ЗаполнитьДвижения") {
			t.Errorf("неоднозначность по ЗаполнитьДвижения пропала вместе с шумом: %+v", res.Ambiguities)
		}
	})

	t.Run("posting без анкера-объекта — неоднозначность осталась", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingFixture(t, st, postingFixtureOpts{})
		res := buildFor(t, st, Request{
			Task: "Почему при проведении не создаются движения в ОбработкаПроведения", ProjectID: "p"})
		if res.Intent.Primary != IntentPosting {
			t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentPosting)
		}
		for _, a := range res.Anchors {
			if a.Kind == "metadata_object" {
				t.Fatalf("анкер-объект всё же нашёлся — условие теста не выполнено: %+v", res.Anchors)
			}
		}
		if !hasAmbiguitySubject(res, "ОбработкаПроведения") {
			t.Errorf("без анкера-объекта омонимия обработчика — реальная развилка, её нельзя гасить: %+v", res.Ambiguities)
		}
	})

	t.Run("другой intent — неоднозначность осталась", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingFixture(t, st, postingFixtureOpts{})
		res := buildFor(t, st, Request{Task: "Исправь ошибку в ОбработкаПроведения документа Заказ", ProjectID: "p"})
		if res.Intent.Primary == IntentPosting {
			t.Fatalf("формулировка классифицирована как posting — тест проверяет другой intent: %+v", res.Intent)
		}
		if !hasAmbiguitySubject(res, "ОбработкаПроведения") {
			t.Errorf("ambiguities по обработчику пропали вне intent posting: %+v", res.Ambiguities)
		}
	})
}
