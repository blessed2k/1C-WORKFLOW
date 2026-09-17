package main

import (
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

func sampleTree() source.MetadataTree {
	return source.MetadataTree{
		Configuration: "ДемоКонфигурация",
		TotalObjects:  6,
		Groups: []source.MetadataGroup{
			{Type: "Role", Objects: []string{"ПолныеПрава", "шк_АС_Вид_База", "шк_АС_Вид_Цех"}},
			{Type: "Document", Objects: []string{"РеализацияТоваровУслуг", "ЗаказКлиента"}},
			{Type: "Catalog", Objects: []string{"Номенклатура"}},
		},
	}
}

// TestFilterTreeByType: answering "is this role here" must not require the whole
// tree, which in a production configuration runs to six figures of characters.
func TestFilterTreeByType(t *testing.T) {
	got := filterTree(sampleTree(), "Role", "")
	if len(got.Groups) != 1 || got.Groups[0].Type != "Role" {
		t.Fatalf("expected only the Role group, got %+v", got.Groups)
	}
	if got.TotalObjects != 3 {
		t.Errorf("TotalObjects = %d, want 3", got.TotalObjects)
	}
}

func TestFilterTreeByNameSubstring(t *testing.T) {
	got := filterTree(sampleTree(), "", "шк_ас")
	if got.TotalObjects != 2 {
		t.Fatalf("TotalObjects = %d, want 2; groups: %+v", got.TotalObjects, got.Groups)
	}
}

func TestFilterTreeWithoutFiltersIsUnchanged(t *testing.T) {
	in := sampleTree()
	if got := filterTree(in, "", ""); got.TotalObjects != in.TotalObjects {
		t.Errorf("unfiltered call changed the tree: %d vs %d", got.TotalObjects, in.TotalObjects)
	}
}

// TestObjectExistsHitAndMiss covers the binary question that used to cost a
// 172k-character metadata tree per call.
func TestObjectExistsHitAndMiss(t *testing.T) {
	hit := lookupObject(sampleTree(), "Role", "шк_АС_Вид_База")
	if !hit.Exists {
		t.Errorf("existing role reported as missing: %+v", hit)
	}
	miss := lookupObject(sampleTree(), "Role", "шк_АС_Вид")
	if miss.Exists {
		t.Error("partial name must not count as an exact match")
	}
	if len(miss.Similar) == 0 {
		t.Error("a miss should offer near matches")
	}
	if miss.Count != 3 {
		t.Errorf("Count = %d, want the number of roles (3)", miss.Count)
	}
}

// TestApplyPartsKeepsOnlyRequested: "which forms does this document have" should
// not drag in a hundred attributes and twenty tabular sections.
func TestApplyPartsKeepsOnlyRequested(t *testing.T) {
	obj := &source.ObjectStructure{
		Type:            "Document",
		Name:            "ЗаказНаряд",
		Attributes:      []source.Field{{Name: "Клиент"}, {Name: "Мастер"}},
		TabularSections: []source.TabularSection{{Name: "Товары"}},
		Forms:           []string{"ФормаДокумента", "ФормаДокумента1"},
		Commands:        []string{"Печать"},
		Other:           map[string][]string{"Template": {"Макет"}},
	}
	applyParts(obj, "forms")
	if len(obj.Forms) != 2 {
		t.Errorf("forms were dropped: %+v", obj.Forms)
	}
	if obj.Attributes != nil || obj.TabularSections != nil || obj.Commands != nil || obj.Other != nil {
		t.Errorf("unrequested sections survived: %+v", obj)
	}
}

func TestApplyPartsEmptyKeepsEverything(t *testing.T) {
	obj := &source.ObjectStructure{Attributes: []source.Field{{Name: "Клиент"}}, Forms: []string{"Ф"}}
	applyParts(obj, "")
	if obj.Attributes == nil || obj.Forms == nil {
		t.Error("empty parts must be a no-op")
	}
}

// TestSampleRoleListsTrims is the direct answer to a session that got 642 role
// names in one response: the useful signal is the count.
func TestSampleRoleListsTrims(t *testing.T) {
	names := make([]string, 642)
	for i := range names {
		names[i] = "Роль" + string(rune('А'+i%32))
	}
	out := &rightsAuditOutput{RightsAudit: source.RightsAudit{NotGranting: names}}
	sampleRoleLists(out, false)

	if out.NotGrantingCount != 642 {
		t.Errorf("NotGrantingCount = %d, want 642", out.NotGrantingCount)
	}
	if len(out.NotGranting) != notGrantingSample {
		t.Errorf("returned %d names, want a sample of %d", len(out.NotGranting), notGrantingSample)
	}
	if !strings.Contains(out.Note, "includeNotGranting") {
		t.Errorf("note does not say how to get the full list: %q", out.Note)
	}
}

func TestSampleRoleListsFullOnRequest(t *testing.T) {
	names := make([]string, 50)
	for i := range names {
		names[i] = "Роль"
	}
	out := &rightsAuditOutput{RightsAudit: source.RightsAudit{NotGranting: names}}
	sampleRoleLists(out, true)
	if len(out.NotGranting) != 50 {
		t.Errorf("explicit request must return everything, got %d", len(out.NotGranting))
	}
}

func TestSampleRoleListsShortListUntouched(t *testing.T) {
	out := &rightsAuditOutput{RightsAudit: source.RightsAudit{NotGranting: []string{"А", "Б"}}}
	sampleRoleLists(out, false)
	if len(out.NotGranting) != 2 || out.Note != "" {
		t.Errorf("short list should pass through unchanged: %+v", out)
	}
}

// TestObjectExistsSuggestsDeclinedNames: found in live use — asking for
// Catalog.Компания when the configuration has Компании returned "does not
// exist" with no hint, because neither string contains the other. Russian
// metadata names differ by ending all the time, so a shared prefix has to count.
func TestObjectExistsSuggestsDeclinedNames(t *testing.T) {
	tree := source.MetadataTree{Groups: []source.MetadataGroup{
		{Type: "Catalog", Objects: []string{"Компании", "Номенклатура", "Контрагенты"}},
	}}
	got := lookupObject(tree, "Catalog", "Компания")
	if got.Exists {
		t.Fatal("Компания must not match Компании exactly")
	}
	if len(got.Similar) == 0 || got.Similar[0] != "Компании" {
		t.Errorf("Компании was not offered as a near match: %v", got.Similar)
	}
}

func TestSimilarNameRejectsUnrelated(t *testing.T) {
	if similarName("Номенклатура", "Компания") {
		t.Error("unrelated names must not be offered")
	}
	if !similarName("Товары", "Тов") {
		// an abbreviation the caller typed is a legitimate lead
		t.Error("a substring should still be offered")
	}
	if similarName("Товары", "Товелло") {
		// three shared characters is a prefix of almost anything
		t.Error("too short a shared prefix should not count")
	}
	if !similarName("КотировкиАкций", "КотировкиАкции") {
		t.Error("one differing ending should still match")
	}
}
