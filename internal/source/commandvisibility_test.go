package source

import (
	"context"
	"testing"
)

// The fixture mirrors the shape found in the real УТ export: a section command
// with per-role overrides, a plain command with only a default, a command hidden
// by default and opened for one role, and a nested subsystem with its own file.
func cmdVisSource() *XMLSource { return NewXMLSource("testdata/cmdvis") }

func findCommand(t *testing.T, cv *CommandVisibility, name string) CommandVisible {
	t.Helper()
	for _, c := range cv.Commands {
		if c.Command == name {
			return c
		}
	}
	t.Fatalf("command %s not found in %+v", name, cv.Commands)
	return CommandVisible{}
}

// TestCommandVisibilityReadsOverrides is the mechanism the rights tools cannot
// see: in these configurations access is limited by hiding commands, not by
// withholding rights.
func TestCommandVisibilityReadsOverrides(t *testing.T) {
	cv, err := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "", false)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	c := findCommand(t, cv, "CommonCommand.ПанельОтчетовПродажи")
	if c.Common != "true" {
		t.Errorf("Common = %q, want true", c.Common)
	}
	if c.ByRole["Кладовщик"] != "false" || c.ByRole["ПолныеПрава"] != "true" {
		t.Errorf("role overrides not parsed: %+v", c.ByRole)
	}
	if c.Subsystem != "Продажи" {
		t.Errorf("Subsystem = %q, want Продажи", c.Subsystem)
	}
}

// TestCommandVisibilityFindsNestedSubsystems: a nested section keeps its own
// CommandInterface.xml, and missing it means missing half the answer.
func TestCommandVisibilityFindsNestedSubsystems(t *testing.T) {
	cv, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "", false)
	c := findCommand(t, cv, "Report.ВаловаяПрибыль.StandardCommand.Open")
	if c.Subsystem != "Отчеты" {
		t.Errorf("nested subsystem = %q, want Отчеты", c.Subsystem)
	}
}

// TestCommandVisibilityResolvesForRole answers the question a session had to
// resolve with two greps over 652 role files: what does this role actually see.
func TestCommandVisibilityResolvesForRole(t *testing.T) {
	cv, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "Кладовщик", false)

	if got := findCommand(t, cv, "CommonCommand.ПанельОтчетовПродажи").Visible; got != "false" {
		t.Errorf("override must win over the default: visible = %q, want false", got)
	}
	if got := findCommand(t, cv, "Document.ЗаказКлиента.StandardCommand.OpenList").Visible; got != "true" {
		t.Errorf("without an override the default applies: visible = %q, want true", got)
	}
	if got := findCommand(t, cv, "Document.РеализацияТоваровУслуг.StandardCommand.Create").Visible; got != "false" {
		t.Errorf("hidden by default and not opened for this role: visible = %q, want false", got)
	}
}

// TestCommandVisibilityAcceptsRolePrefix: the export spells roles as Role.X, the
// caller may pass either form.
func TestCommandVisibilityAcceptsRolePrefix(t *testing.T) {
	withPrefix, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "Role.МенеджерПоПродажам", false)
	bare, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "МенеджерПоПродажам", false)

	a := findCommand(t, withPrefix, "Document.РеализацияТоваровУслуг.StandardCommand.Create").Visible
	b := findCommand(t, bare, "Document.РеализацияТоваровУслуг.StandardCommand.Create").Visible
	if a != "true" || a != b {
		t.Errorf("Role.X and bare name disagree: %q vs %q", a, b)
	}
}

// TestCommandVisibilityRolesOnlyFilters keeps the default answer usable: a real
// configuration has over a thousand commands, of which a few dozen are the ones
// access is tuned with.
func TestCommandVisibilityRolesOnlyFilters(t *testing.T) {
	all, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "", false)
	tuned, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "", true)

	if tuned.Count >= all.Count {
		t.Errorf("rolesOnly did not filter anything: %d vs %d", tuned.Count, all.Count)
	}
	for _, c := range tuned.Commands {
		if len(c.ByRole) == 0 {
			t.Errorf("%s has no role overrides but survived the filter", c.Command)
		}
	}
}

func TestCommandVisibilityScopesToSubsystem(t *testing.T) {
	cv, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "Отчеты", "", false)
	if cv.Count != 1 {
		t.Fatalf("scoped audit returned %d commands, want 1: %+v", cv.Count, cv.Commands)
	}
}

func TestCommandVisibilityListsMentionedRoles(t *testing.T) {
	cv, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "", "", false)
	want := map[string]bool{"Кладовщик": true, "ПолныеПрава": true, "МенеджерПоПродажам": true, "РуководительОтделаПродаж": true}
	if len(cv.Roles) != len(want) {
		t.Fatalf("roles = %v, want %d entries", cv.Roles, len(want))
	}
	for _, r := range cv.Roles {
		if !want[r] {
			t.Errorf("unexpected role %q", r)
		}
	}
}

// TestCommandVisibilityUnknownSubsystem must not look like "nothing is tuned".
func TestCommandVisibilityUnknownSubsystem(t *testing.T) {
	cv, _ := cmdVisSource().CommandVisibilityAudit(context.Background(), "НетТакойПодсистемы", "", false)
	if cv.Count != 0 || cv.Note == "" {
		t.Errorf("unknown subsystem should return an explanatory note: %+v", cv)
	}
}
