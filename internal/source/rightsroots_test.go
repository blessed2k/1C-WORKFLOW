package source

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// rightsXML builds a Rights.xml granting the listed rights on Catalog.Товары.
func rightsXML(rights ...string) string {
	text := `<?xml version="1.0" encoding="UTF-8"?>
<Rights xmlns="http://v8.1c.ru/8.2/roles" version="2.20">
	<setForNewObjects>false</setForNewObjects>
	<object>
		<name>Catalog.Товары</name>`
	for _, r := range rights {
		text += "\n\t\t<right><name>" + r + "</name><value>true</value></right>"
	}
	return text + "\n\t</object>\n</Rights>\n"
}

// rightsRootsFixture: the base export has role Кладовщик (Read); the extension
// borrows it adding Update and brings its own role Расш_Приемщик.
func rightsRootsFixture(t *testing.T) *XMLSource {
	t.Helper()
	base, ext := t.TempDir(), t.TempDir()
	write := func(dir, role, text string) {
		path := filepath.Join(dir, "Roles", role, "Ext", "Rights.xml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write(base, "Кладовщик", rightsXML("Read"))
	write(ext, "Кладовщик", rightsXML("Update"))
	write(ext, "Расш_Приемщик", rightsXML("Read", "Insert"))

	s := NewXMLSource(base)
	s.OtherComponents = func() []ComponentRoot { return []ComponentRoot{{Name: "addon", Dir: ext}} }
	return s
}

func rightNames(rights []GrantedRight) []string {
	out := make([]string, len(rights))
	for i, r := range rights {
		out[i] = r.Name
	}
	return out
}

// TestRightsAuditCountsExtensionRoles: a role defined in an extension, and the
// rights an extension adds to a borrowed base role, are access the user really
// has. The audit read the base export only and reported neither.
func TestRightsAuditCountsExtensionRoles(t *testing.T) {
	audit, err := rightsRootsFixture(t).RightsAudit(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if audit.TotalRoles != 2 {
		t.Errorf("TotalRoles = %d, want 2 (Кладовщик и Расш_Приемщик)", audit.TotalRoles)
	}
	if len(audit.Granting) != 2 {
		t.Fatalf("Granting = %+v, want two roles", audit.Granting)
	}

	borrowed, own := audit.Granting[0], audit.Granting[1]
	if borrowed.Role != "Кладовщик" || !reflect.DeepEqual(rightNames(borrowed.Rights), []string{"Read", "Update"}) {
		t.Errorf("borrowed role: %+v, want Кладовщик with Read and Update", borrowed)
	}
	if borrowed.Component != "" || !reflect.DeepEqual(borrowed.Extensions, []string{"addon"}) {
		t.Errorf("borrowed role must stay a base role extended by addon: %+v", borrowed)
	}
	if own.Role != "Расш_Приемщик" || own.Component != "addon" || !reflect.DeepEqual(rightNames(own.Rights), []string{"Read", "Insert"}) {
		t.Errorf("extension role: %+v, want Расш_Приемщик of addon with Read and Insert", own)
	}
	if got := audit.RightSummary["Update"]; !reflect.DeepEqual(got, []string{"Кладовщик"}) {
		t.Errorf("RightSummary[Update] = %v, want [Кладовщик]", got)
	}
}

// TestEffectiveRightsKnowExtensionRoles: a role set naming an extension role is
// not "unknown", and a borrowed role counts with what the extension added.
func TestEffectiveRightsKnowExtensionRoles(t *testing.T) {
	eff, err := rightsRootsFixture(t).EffectiveRights(context.Background(), "Catalog", "Товары", []string{"Расш_Приемщик", "Кладовщик"}, "")
	if err != nil {
		t.Fatalf("EffectiveRights: %v", err)
	}
	if len(eff.UnknownRoles) != 0 {
		t.Errorf("UnknownRoles = %v, want none", eff.UnknownRoles)
	}
	granted := map[string][]string{}
	for _, r := range eff.Rights {
		granted[r.Name] = r.GrantedBy
	}
	want := map[string][]string{
		"Read":   {"Расш_Приемщик", "Кладовщик"},
		"Insert": {"Расш_Приемщик"},
		"Update": {"Кладовщик"},
	}
	if !reflect.DeepEqual(granted, want) {
		t.Errorf("effective rights = %v, want %v", granted, want)
	}
}

// TestRightsAuditExtensionLiftsRestriction: roles combine by OR, and so do the
// layers of one role. A right the base role restricts by RLS and the extension
// grants without a restriction is unrestricted.
func TestRightsAuditExtensionLiftsRestriction(t *testing.T) {
	base, ext := t.TempDir(), t.TempDir()
	restricted := `<?xml version="1.0" encoding="UTF-8"?>
<Rights xmlns="http://v8.1c.ru/8.2/roles" version="2.20">
	<object>
		<name>Catalog.Товары</name>
		<right><name>Read</name><value>true</value>
			<restrictionByCondition><condition>ГДЕ Ложь</condition></restrictionByCondition>
		</right>
	</object>
</Rights>
`
	for dir, text := range map[string]string{base: restricted, ext: rightsXML("Read")} {
		path := filepath.Join(dir, "Roles", "Кладовщик", "Ext", "Rights.xml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	s := NewXMLSource(base)

	alone, err := s.RightsAudit(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if len(alone.Granting) != 1 || len(alone.Granting[0].Rights[0].RLS) != 1 {
		t.Fatalf("base role alone must be restricted: %+v", alone.Granting)
	}

	s.OtherComponents = func() []ComponentRoot { return []ComponentRoot{{Name: "addon", Dir: ext}} }
	both, err := s.RightsAudit(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if len(both.Granting) != 1 || len(both.Granting[0].Rights) != 1 || len(both.Granting[0].Rights[0].RLS) != 0 {
		t.Errorf("extension grants Read without RLS, the restriction must be gone: %+v", both.Granting)
	}
}
