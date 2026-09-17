package source

import (
	"context"
	"strings"
	"testing"
)

func TestRightsAudit(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	ra, err := s.RightsAudit(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}

	if ra.Object != "Справочник.Товары" {
		t.Errorf("Object = %q", ra.Object)
	}
	if ra.TotalRoles != 2 {
		t.Errorf("TotalRoles = %d, want 2", ra.TotalRoles)
	}
	if len(ra.Granting) != 1 || ra.Granting[0].Role != "ЧтениеТоваров" {
		t.Fatalf("Granting = %+v", ra.Granting)
	}

	// Read carries an RLS condition; View does not; Insert (false) is absent.
	byName := map[string]GrantedRight{}
	for _, r := range ra.Granting[0].Rights {
		byName[r.Name] = r
	}
	read, ok := byName["Read"]
	if !ok || len(read.RLS) != 1 || !strings.Contains(read.RLS[0].Condition, "Товары.Родитель") {
		t.Errorf("Read RLS = %+v", read)
	}
	if len(read.RLS[0].Fields) != 1 || read.RLS[0].Fields[0] != "Наименование" {
		t.Errorf("Read RLS fields = %v, want [Наименование]", read.RLS[0].Fields)
	}
	if v, ok := byName["View"]; !ok || len(v.RLS) != 0 {
		t.Errorf("View = %+v, want granted without RLS", v)
	}
	if _, ok := byName["Insert"]; ok {
		t.Errorf("Insert has value=false and must not be granted")
	}

	// БазовыеПрава grants nothing on Товары.
	if len(ra.NotGranting) != 1 || ra.NotGranting[0] != "БазовыеПрава" {
		t.Errorf("NotGranting = %v", ra.NotGranting)
	}
	if roles := ra.RightSummary["Read"]; len(roles) != 1 || roles[0] != "ЧтениеТоваров" {
		t.Errorf("RightSummary[Read] = %v", roles)
	}
}

func TestRightsAuditOtherObject(t *testing.T) {
	// Контрагенты is granted by БазовыеПрава, not by ЧтениеТоваров.
	s := NewXMLSource("testdata/dump")
	ra, err := s.RightsAudit(context.Background(), "Catalog", "Контрагенты")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if len(ra.Granting) != 1 || ra.Granting[0].Role != "БазовыеПрава" {
		t.Errorf("Granting = %+v", ra.Granting)
	}
}

func TestRightsAuditNoRolesDir(t *testing.T) {
	s := NewXMLSource("testdata/ext") // no Roles directory
	ra, err := s.RightsAudit(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("RightsAudit: %v", err)
	}
	if ra.TotalRoles != 0 || len(ra.Granting) != 0 {
		t.Errorf("expected empty audit, got %+v", ra)
	}
}
