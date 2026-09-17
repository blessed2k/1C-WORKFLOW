package source

import (
	"context"
	"strings"
	"testing"
)

func visSource(t *testing.T) *XMLSource {
	t.Helper()
	return NewXMLSource("testdata/vis")
}

func visCodes(rep *VisibilityReport) map[string]string {
	out := map[string]string{}
	for _, f := range rep.Findings {
		out[f.Code] = f.Where
	}
	return out
}

func TestVisibilityFindsSubsystemPath(t *testing.T) {
	rep, err := visSource(t).VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	// A nested subsystem must be reported by its full path: "Продажи" alone does
	// not tell where in the command interface the object sits.
	if len(rep.Subsystems) != 1 || rep.Subsystems[0].Path != "Продажи.ОптовыеПродажи" {
		t.Fatalf("subsystems = %+v, want [Продажи.ОптовыеПродажи]", rep.Subsystems)
	}
	if !rep.Subsystems[0].InCommandInterface {
		t.Error("a subsystem without IncludeInCommandInterface=false must count as reachable")
	}
}

func TestVisibilityDistinguishesObjectAndAttributeOptions(t *testing.T) {
	rep, err := visSource(t).VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	byName := map[string]FunctionalOptionRef{}
	for _, fo := range rep.FunctionalOptions {
		byName[fo.Name+"|"+fo.Scope] = fo
	}
	whole, ok := byName["ИспользоватьЗаказы|объект целиком"]
	if !ok {
		t.Fatalf("option controlling the whole object not found: %+v", rep.FunctionalOptions)
	}
	if whole.Location != "Константа.ИспользоватьЗаказы" {
		t.Errorf("location must be russified: %q", whole.Location)
	}
	if !whole.Privileged {
		t.Error("PrivilegedGetMode must be reported")
	}
	if whole.Synonym == "" {
		t.Error("synonym must be reported: that is what the user sees in настройках")
	}
	// An option may control an attribute rather than the object: the object stays
	// visible while the attribute disappears from the form.
	if _, ok := byName["ИспользоватьСклады|реквизит Склад"]; !ok {
		t.Errorf("attribute-scoped option not recognised: %+v", rep.FunctionalOptions)
	}
	codes := visCodes(rep)
	if _, ok := codes["HiddenByFunctionalOption"]; !ok {
		t.Error("an option over the whole object must be reported")
	}
	if _, ok := codes["PartHiddenByFunctionalOption"]; !ok {
		t.Error("an option over a part of the object must be reported")
	}
	// Several options over one object work as OR, not AND: the wording must not
	// claim that every one of them has to be on.
	for _, f := range rep.Findings {
		if f.Code == "HiddenByFunctionalOption" && !strings.Contains(f.Message, "ХОТЯ БЫ ОДНА") {
			t.Errorf("the OR semantics must be stated: %q", f.Message)
		}
	}
}

// TestVisibilityTabularSectionScope pins the third form of a content entry: an
// attribute of a tabular section.
func TestVisibilityTabularSectionScope(t *testing.T) {
	if scope, ok := optionScope("Document.ЗаказКлиента.TabularSection.Товары.Attribute.Склад", "Document.ЗаказКлиента"); !ok || scope != "реквизит табличной части Товары.Склад" {
		t.Errorf("scope = %q, ok = %v", scope, ok)
	}
	if scope, ok := optionScope("Document.ЗаказКлиента.Attribute.Склад", "Document.ЗаказКлиента"); !ok || scope != "реквизит Склад" {
		t.Errorf("scope = %q, ok = %v", scope, ok)
	}
	// A different object with the same prefix must not match.
	if _, ok := optionScope("Document.ЗаказКлиентаКорректировка", "Document.ЗаказКлиента"); ok {
		t.Error("a longer object name must not be taken for a part of this one")
	}
}

func TestVisibilityNotInAnySubsystem(t *testing.T) {
	rep, err := visSource(t).VisibilityAudit(context.Background(), "Catalog", "Валюты", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	codes := visCodes(rep)
	if _, ok := codes["NotInAnySubsystem"]; !ok {
		t.Errorf("an interactive object outside every subsystem must be reported: %+v", rep.Findings)
	}
}

// TestVisibilityRightsAreOptional pins that the expensive part is opt-in: rights
// cost about a second on a real configuration and rights_audit answers that
// question in full.
func TestVisibilityRightsAreOptional(t *testing.T) {
	rep, err := visSource(t).VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if rep.RolesGranting != -1 {
		t.Errorf("rolesGranting must be -1 when not requested, got %d", rep.RolesGranting)
	}
	for _, f := range rep.Findings {
		if f.Code == "NoRoleGrantsRights" {
			t.Error("rights must not be judged when they were not read")
		}
	}
	withRights, err := visSource(t).VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", true)
	if err != nil {
		t.Fatalf("VisibilityAudit with rights: %v", err)
	}
	if withRights.RolesGranting < 0 {
		t.Error("rolesGranting must be filled when requested")
	}
}

func TestVisibilityErrors(t *testing.T) {
	s := visSource(t)
	if _, err := s.VisibilityAudit(context.Background(), "", "", false); err == nil {
		t.Error("empty arguments must be an error")
	}
	if _, err := s.VisibilityAudit(context.Background(), "Document", "../../etc/passwd", false); err == nil {
		t.Error("a path must be rejected")
	}
	if _, err := s.VisibilityAudit(context.Background(), "Document", "НетТакого", false); err == nil {
		t.Error("unknown object must be an error")
	}
	rep, err := s.VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if !strings.Contains(rep.Note, "подсистем") {
		t.Error("the note must explain what visibility depends on")
	}
}

// TestVisibilityKeepsEveryContentEntry pins the defect that lost a third of the
// content in УТ: one option routinely controls the object in several places, and
// stopping at the first match dropped the rest. Where the "whole object" entry
// came second, the report claimed the object stays visible while the option
// actually hides it (Document.УпаковочныйЛист in УТ).
func TestVisibilityKeepsEveryContentEntry(t *testing.T) {
	rep, err := visSource(t).VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	scopes := map[string]bool{}
	for _, fo := range rep.FunctionalOptions {
		if fo.Name == "ИспользоватьЗаказы" {
			scopes[fo.Scope] = true
		}
	}
	if !scopes["команда Печать"] || !scopes[scopeWholeObject] {
		t.Errorf("both entries of one option must be reported, got %v", scopes)
	}
}

// TestVisibilityCommandInterface pins the reason an object is unreachable that
// actually fires on a real configuration: the subsystem exists but is not
// included in the command interface (333 of 462 subsystems in УТ).
func TestVisibilityCommandInterface(t *testing.T) {
	rep, err := visSource(t).VisibilityAudit(context.Background(), "Catalog", "Склады", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	if len(rep.Subsystems) != 1 || rep.Subsystems[0].InCommandInterface {
		t.Fatalf("subsystem with IncludeInCommandInterface=false must be marked: %+v", rep.Subsystems)
	}
	if _, ok := visCodes(rep)["NotInCommandInterface"]; !ok {
		t.Errorf("an object reachable through no interface subsystem must be reported: %+v", rep.Findings)
	}
}

// TestVisibilitySubsystemOption pins that an option switching off a whole
// section is traced down to the objects inside it: 42 subsystems in УТ are
// controlled this way, with 2645 objects under them.
func TestVisibilitySubsystemOption(t *testing.T) {
	rep, err := visSource(t).VisibilityAudit(context.Background(), "Document", "ЗаказКлиента", false)
	if err != nil {
		t.Fatalf("VisibilityAudit: %v", err)
	}
	found := false
	for _, fo := range rep.FunctionalOptions {
		if fo.Name == "ИспользоватьРаздел" && fo.Scope == "раздел Продажи" {
			found = true
		}
	}
	if !found {
		t.Errorf("an option over the subsystem must reach the objects in it: %+v", rep.FunctionalOptions)
	}
	if _, ok := visCodes(rep)["SectionHiddenByFunctionalOption"]; !ok {
		t.Errorf("section-level option must be reported: %+v", rep.Findings)
	}
}

// TestVisibilityScopeKinds pins that a command, a dimension and a whole tabular
// section are not called "реквизит": 894 entries of 6281 in УТ were mislabelled.
func TestVisibilityScopeKinds(t *testing.T) {
	cases := map[string]string{
		"Document.X.Command.Печать":                       "команда Печать",
		"Document.X.TabularSection.Товары":                "табличная часть Товары",
		"InformationRegister.X.Dimension.Склад":           "измерение Склад",
		"InformationRegister.X.Resource.Количество":       "ресурс Количество",
		"Document.X.TabularSection.Товары.Attribute.Цена": "реквизит табличной части Товары.Цена",
	}
	for entry, want := range cases {
		owner := strings.Join(strings.Split(entry, ".")[:2], ".")
		if got, ok := optionScope(entry, owner); !ok || got != want {
			t.Errorf("optionScope(%q) = %q, want %q", entry, got, want)
		}
	}
}
