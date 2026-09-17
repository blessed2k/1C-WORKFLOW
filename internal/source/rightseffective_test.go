package source

import (
	"context"
	"strings"
	"testing"
)

func effective(t *testing.T, roles []string, profile string) *EffectiveRights {
	t.Helper()
	er, err := NewXMLSource("testdata/rights").EffectiveRights(context.Background(), "Catalog", "Товары", roles, profile)
	if err != nil {
		t.Fatalf("EffectiveRights: %v", err)
	}
	return er
}

func rightByName(er *EffectiveRights, name string) (EffectiveRight, bool) {
	for _, r := range er.Rights {
		if r.Name == name {
			return r, true
		}
	}
	return EffectiveRight{}, false
}

func TestEffectiveRightsSingleRoleKeepsRLS(t *testing.T) {
	er := effective(t, []string{"ЧтениеСвоих"}, "")
	if er.Object != "Справочник.Товары" {
		t.Errorf("Object = %q", er.Object)
	}
	read, ok := rightByName(er, "Read")
	if !ok {
		t.Fatalf("Read not granted; got %+v", er.Rights)
	}
	if !read.Restricted {
		t.Errorf("Read must stay restricted for a single restricting role; got %+v", read)
	}
	if len(read.RLS) != 1 || read.RLS[0].Role != "ЧтениеСвоих" {
		t.Errorf("RLS = %+v", read.RLS)
	}
	if read.Note != "" {
		t.Errorf("no cancellation happened, Note must be empty; got %q", read.Note)
	}
}

func TestEffectiveRightsUnrestrictedRoleCancelsRLS(t *testing.T) {
	er := effective(t, []string{"ЧтениеСвоих", "ЧтениеВсех"}, "")
	read, ok := rightByName(er, "Read")
	if !ok {
		t.Fatalf("Read not granted; got %+v", er.Rights)
	}
	if read.Restricted {
		t.Errorf("a role granting Read without RLS must cancel the restriction; got %+v", read)
	}
	if len(read.UnrestrictedBy) != 1 || read.UnrestrictedBy[0] != "ЧтениеВсех" {
		t.Errorf("UnrestrictedBy = %v, want [ЧтениеВсех]", read.UnrestrictedBy)
	}
	// The restriction of the other role is still reported, so it is visible what
	// was cancelled, and the note says why it no longer limits anything.
	if len(read.RLS) != 1 || read.RLS[0].Role != "ЧтениеСвоих" {
		t.Errorf("RLS = %+v, want the cancelled restriction of ЧтениеСвоих", read.RLS)
	}
	if !strings.Contains(read.Note, "ЧтениеВсех") || !strings.Contains(read.Note, "ЧтениеСвоих") {
		t.Errorf("Note must name both sides; got %q", read.Note)
	}
}

func TestEffectiveRightsUnionsAcrossRoles(t *testing.T) {
	er := effective(t, []string{"ЧтениеСвоих", "РедактированиеТоваров"}, "")
	read, _ := rightByName(er, "Read")
	if len(read.GrantedBy) != 2 {
		t.Errorf("Read grantedBy = %v, want both roles", read.GrantedBy)
	}
	// Both roles restrict Read, so it stays restricted with both conditions.
	if !read.Restricted || len(read.RLS) != 2 {
		t.Errorf("Read = %+v, want restricted with two conditions", read)
	}
	if _, ok := rightByName(er, "Update"); !ok {
		t.Errorf("Update from РедактированиеТоваров missing; got %+v", er.Rights)
	}
	if _, ok := rightByName(er, "View"); !ok {
		t.Errorf("View from ЧтениеСвоих missing; got %+v", er.Rights)
	}
	// Delete has value=false and must not appear.
	if _, ok := rightByName(er, "Delete"); ok {
		t.Errorf("Delete is not granted (value=false) and must not appear")
	}
}

func TestEffectiveRightsUnknownAndSilentRoles(t *testing.T) {
	// Роль есть в выгрузке, но на этом объекте не даёт ничего; и роль, которой нет.
	er := effective(t, []string{"ЧтениеВсех", "НетТакойРоли"}, "")
	if len(er.UnknownRoles) != 1 || er.UnknownRoles[0] != "НетТакойРоли" {
		t.Errorf("UnknownRoles = %v", er.UnknownRoles)
	}

	other, err := NewXMLSource("testdata/rights").EffectiveRights(context.Background(),
		"Catalog", "Подразделения", []string{"ЧтениеСвоих", "РедактированиеТоваров"}, "")
	if err != nil {
		t.Fatalf("EffectiveRights: %v", err)
	}
	if len(other.NoRights) != 1 || other.NoRights[0] != "ЧтениеСвоих" {
		t.Errorf("rolesGrantingNothing = %v, want [ЧтениеСвоих]", other.NoRights)
	}
}

func TestEffectiveRightsSetForNewObjectsIsNotNoAccess(t *testing.T) {
	// ПолныеПрава exports as a few lines: Rights.xml records deviations from the
	// role's defaults, and with setForNewObjects the role covers objects it never
	// lists. Reporting it as "grants nothing" would be plainly wrong.
	er := effective(t, []string{"ПолныеПрава"}, "")
	if len(er.NoRights) != 0 {
		t.Errorf("rolesGrantingNothing = %v, must not claim a setForNewObjects role grants nothing", er.NoRights)
	}
	if len(er.Undetermined) != 1 || er.Undetermined[0] != "ПолныеПрава" {
		t.Fatalf("undetermined = %v, want [ПолныеПрава]", er.Undetermined)
	}
	if !strings.Contains(er.Note, "setForNewObjects") {
		t.Errorf("Note must explain why the answer is undetermined; got %q", er.Note)
	}
}

func TestRightsAuditSetForNewObjectsBucket(t *testing.T) {
	ra, err := NewXMLSource("testdata/rights").RightsAudit(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if containsStr(ra.NotGranting, "ПолныеПрава") {
		t.Errorf("ПолныеПрава must not be listed as not granting; got %v", ra.NotGranting)
	}
	if len(ra.Undetermined) != 1 || ra.Undetermined[0] != "ПолныеПрава" {
		t.Errorf("undetermined = %v, want [ПолныеПрава]", ra.Undetermined)
	}
	// The profiles that reach the object come along with the object-centric view.
	if len(ra.Profiles) == 0 {
		t.Fatalf("profiles must be reported; got %+v", ra)
	}
	found := false
	for _, p := range ra.Profiles {
		if p.Profile == "Кладовщик" && len(p.Roles) == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("profile Кладовщик with both granting roles missing; got %+v", ra.Profiles)
	}
}

func TestEffectiveRightsRequiresRoles(t *testing.T) {
	s := NewXMLSource("testdata/rights")
	if _, err := s.EffectiveRights(context.Background(), "Catalog", "Товары", nil, ""); err == nil {
		t.Error("expected an error without roles and without a profile")
	}
}

func TestEffectiveRightsByProfile(t *testing.T) {
	er := effective(t, nil, "Кладовщик")
	if er.Profile != "Кладовщик" {
		t.Errorf("Profile = %q", er.Profile)
	}
	if len(er.Roles) != 2 || er.Roles[0] != "ЧтениеСвоих" {
		t.Errorf("Roles = %v, want the profile's roles", er.Roles)
	}
	read, _ := rightByName(er, "Read")
	if !read.Restricted {
		t.Errorf("both roles of the profile restrict Read; got %+v", read)
	}

	// The human name resolves too.
	byDesc := effective(t, nil, "Кладовщик склада")
	if len(byDesc.Roles) != 2 {
		t.Errorf("profile by Наименование not resolved: %+v", byDesc)
	}
}

func TestEffectiveRightsUnknownProfileListsAvailable(t *testing.T) {
	_, err := NewXMLSource("testdata/rights").EffectiveRights(context.Background(),
		"Catalog", "Товары", nil, "Директор")
	if err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
	// The error must say what IS available, otherwise the caller cannot recover.
	for _, want := range []string{"Кладовщик", "Аудитор", "Расчётчик зарплаты"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must list available profiles, missing %q: %v", want, err)
		}
	}
}

func TestAccessProfilesParsesBothBSPStyles(t *testing.T) {
	profiles, err := NewXMLSource("testdata/rights").AccessProfiles()
	if err != nil {
		t.Fatalf("AccessProfiles: %v", err)
	}
	if len(profiles) != 3 {
		t.Fatalf("profiles = %+v, want 3 (two BSP 3.1 + one BSP 2.x)", profiles)
	}

	byName := map[string]AccessProfile{}
	for _, p := range profiles {
		key := p.Name
		if key == "" {
			key = p.Description
		}
		byName[key] = p
	}

	skl, ok := byName["Кладовщик"]
	if !ok {
		t.Fatalf("Кладовщик not parsed; got %+v", profiles)
	}
	if skl.Description != "Кладовщик склада" {
		t.Errorf("Description = %q", skl.Description)
	}
	if len(skl.Roles) != 2 {
		t.Errorf("Roles = %v, want two", skl.Roles)
	}
	if skl.Module != "УправлениеДоступомПереопределяемый" || skl.Procedure == "" {
		t.Errorf("origin = %q / %q, want module and procedure reported", skl.Module, skl.Procedure)
	}
	if len(skl.MissingRoles) != 0 {
		t.Errorf("MissingRoles = %v, want none", skl.MissingRoles)
	}

	// A profile referencing a role absent from the export is a finding.
	aud, ok := byName["Аудитор"]
	if !ok {
		t.Fatalf("Аудитор not parsed; got %+v", profiles)
	}
	if len(aud.MissingRoles) != 1 || aud.MissingRoles[0] != "ПросмотрЖурналаРегистрации" {
		t.Errorf("MissingRoles = %v, want [ПросмотрЖурналаРегистрации]", aud.MissingRoles)
	}

	// BSP 2.x style: no Имя, no constructor, different procedure name.
	old, ok := byName["Расчётчик зарплаты"]
	if !ok {
		t.Fatalf("BSP 2.x style profile not parsed; got %+v", profiles)
	}
	if len(old.Roles) != 1 || old.Roles[0] != "РедактированиеТоваров" {
		t.Errorf("Roles = %v", old.Roles)
	}
	if old.Module != "ПрофилиГруппДоступаЗУП" {
		t.Errorf("Module = %q", old.Module)
	}
}

func TestAccessProfilesNoneWhenNoSuchCode(t *testing.T) {
	// The shared dump has a common module but no Роли.Добавить anywhere: the
	// answer must be "no profiles", not an error and not invented data.
	profiles, err := NewXMLSource("testdata/dump").AccessProfiles()
	if err != nil {
		t.Fatalf("AccessProfiles: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("profiles = %+v, want none", profiles)
	}
}
