package source

import (
	"context"
	"strings"
	"testing"
)

func clItems(rep *ChecklistReport) map[string]ChecklistItem {
	out := map[string]ChecklistItem{}
	for _, it := range rep.Items {
		out[it.Place] = it
	}
	return out
}

// TestChecklistFindsWhatIsMissing is the point of the tool: the new document is
// created but registered nowhere the reference document is.
func TestChecklistFindsWhatIsMissing(t *testing.T) {
	s := NewXMLSource("testdata/vis")
	rep, err := s.NewObjectChecklist(context.Background(), "Document", "ЗаказПоставщику", "ЗаказКлиента")
	if err != nil {
		t.Fatalf("NewObjectChecklist: %v", err)
	}
	items := clItems(rep)
	for _, place := range []string{"Подсистемы", "Функциональные опции", "Журналы документов", "Критерии отбора"} {
		it, ok := items[place]
		if !ok {
			t.Errorf("place %q missing from the checklist: %+v", place, rep.Items)
			continue
		}
		if it.Status != statusMissing {
			t.Errorf("%q: status = %q, want %q (reference %v, actual %v)", place, it.Status, statusMissing, it.Reference, it.Actual)
		}
		if len(it.Reference) == 0 {
			t.Errorf("%q: the reference registration must be shown, it is what has to be repeated", place)
		}
		if it.Hint == "" {
			t.Errorf("%q: a hint must say what breaks without it", place)
		}
	}
	if rep.Missing == 0 {
		t.Error("an object registered nowhere must produce missing items")
	}
}

// TestChecklistSkipsWhatReferenceDoesNotUse pins that the checklist is built
// from the reference object, not from a fixed list: a place the reference is not
// registered in is not something to repeat.
func TestChecklistSkipsWhatReferenceDoesNotUse(t *testing.T) {
	s := NewXMLSource("testdata/vis")
	rep, err := s.NewObjectChecklist(context.Background(), "Catalog", "Валюты", "Склады")
	if err != nil {
		t.Fatalf("NewObjectChecklist: %v", err)
	}
	if _, ok := clItems(rep)["Журналы документов"]; ok {
		t.Error("a catalog reference has no journals, so the place must not appear at all")
	}
}

// TestChecklistComparesAgainstTheReference pins what the status means: the new
// object is measured against the places the reference is registered in, not
// against "is it registered anywhere". A brand-new document already lands in the
// roles and subsystems every object of its kind lands in, and counting those as
// done is the opposite of what a checklist is for.
func TestChecklistComparesAgainstTheReference(t *testing.T) {
	missing, status := checklistCompare([]string{"РольА", "РольБ", "РольВ"}, []string{"РольА"})
	if status != statusPartial {
		t.Errorf("status = %q, want %q", status, statusPartial)
	}
	if len(missing) != 2 {
		t.Errorf("missingIn = %v, want the two roles not reached", missing)
	}
	// A registration that shares nothing with the reference is not done, however
	// long it is: that is the УдаленныйДоступOData case.
	if _, status := checklistCompare([]string{"РольА"}, []string{"УдаленныйДоступOData"}); status != statusMissing {
		t.Errorf("status = %q, want %q", status, statusMissing)
	}
	if _, status := checklistCompare([]string{"РольА"}, []string{"рольа"}); status != statusDone {
		t.Error("comparison must be case-insensitive")
	}
}

func TestChecklistErrors(t *testing.T) {
	s := NewXMLSource("testdata/vis")
	if _, err := s.NewObjectChecklist(context.Background(), "Document", "ЗаказКлиента", ""); err == nil {
		t.Error("the reference object is required")
	}
	if _, err := s.NewObjectChecklist(context.Background(), "Document", "НетТакого", "ЗаказКлиента"); err == nil {
		t.Error("unknown object must be an error")
	}
	if _, err := s.NewObjectChecklist(context.Background(), "Document", "ЗаказКлиента", "../../etc/passwd"); err == nil {
		t.Error("a path must be rejected")
	}
}

// TestChecklistSubscriptionsMustBeNamed pins the defect that made the place
// structurally unable to say "не сделано": a subscription bound to a bare type
// set fires for every object of the kind, including one created a minute ago,
// and counting those marked a brand-new document as done.
func TestChecklistSubscriptionsMustBeNamed(t *testing.T) {
	s := NewXMLSource("testdata/wp") // the fixture with all three source forms
	named, err := s.checklistSubscriptions(context.Background(), "Document.ЗаказКлиента")
	if err != nil {
		t.Fatalf("checklistSubscriptions: %v", err)
	}
	joined := strings.Join(named, " ")
	if !strings.Contains(joined, "ЗаказКлиентаПередЗаписью") {
		t.Errorf("a subscription naming the document must count: %v", named)
	}
	if !strings.Contains(joined, "ФайлыПередЗаписью") {
		t.Errorf("a subscription through a defined type names the document too: %v", named)
	}
	if strings.Contains(joined, "ВсеДокументыПриЗаписи") {
		t.Errorf("a subscription on the bare kind fires for everything and must not count: %v", named)
	}
}

// TestChecklistSubsystemsCarryTheFlag pins that a placement which never reaches
// the command interface is not passed off as a finished one: in УТ 112 documents
// of 280 are placed only in such subsystems.
func TestChecklistSubsystemsCarryTheFlag(t *testing.T) {
	s := NewXMLSource("testdata/vis")
	got, err := s.checklistSubsystems(context.Background(), "Catalog.Склады")
	if err != nil {
		t.Fatalf("checklistSubsystems: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "не в командном интерфейсе") {
		t.Errorf("subsystems = %v, want the placement marked as unreachable", got)
	}
}

func TestChecklistRejectsSelfReference(t *testing.T) {
	s := NewXMLSource("testdata/vis")
	if _, err := s.NewObjectChecklist(context.Background(), "Document", "ЗаказКлиента", "ЗаказКлиента"); err == nil {
		t.Error("comparing an object with itself produces a meaningless report and must be refused")
	}
}
