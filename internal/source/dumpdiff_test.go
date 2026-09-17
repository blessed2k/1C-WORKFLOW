package source

import (
	"context"
	"strings"
	"testing"
)

// testdata/dumpvariant is testdata/dump with Catalog Контрагенты removed,
// Document ПоступлениеТоваровУслуг added, and the export format lowered to 2.20
// with one file left behind at 2.17.
func TestCompareDumpsFindsBothDirections(t *testing.T) {
	diff, err := CompareDumps(context.Background(), "testdata/dump", "testdata/dumpvariant", false)
	if err != nil {
		t.Fatalf("CompareDumps: %v", err)
	}
	if !containsStr(diff.OnlyInA, "Catalog.Контрагенты") {
		t.Errorf("OnlyInA missing the removed catalog: %v", diff.OnlyInA)
	}
	if !containsStr(diff.OnlyInB, "Document.ПоступлениеТоваровУслуг") {
		t.Errorf("OnlyInB missing the added document: %v", diff.OnlyInB)
	}
	if diff.CommonCount == 0 {
		t.Error("CommonCount = 0, the exports share most objects")
	}
}

// TestCompareDumpsWarnsAboutLoss: replacing a working copy with a fresh export
// silently drops whatever only the working copy had. That is the sentence the
// caller needs, not just two lists.
func TestCompareDumpsWarnsAboutLoss(t *testing.T) {
	diff, _ := CompareDumps(context.Background(), "testdata/dump", "testdata/dumpvariant", false)
	if !strings.Contains(diff.Note, "потеряется") {
		t.Errorf("note does not spell out the risk: %q", diff.Note)
	}
}

func TestCompareIdenticalDumps(t *testing.T) {
	diff, _ := CompareDumps(context.Background(), "testdata/dump", "testdata/dump", false)
	if len(diff.OnlyInA) != 0 || len(diff.OnlyInB) != 0 {
		t.Errorf("identical exports differ: %v / %v", diff.OnlyInA, diff.OnlyInB)
	}
	if diff.Note != "состав объектов совпадает" {
		t.Errorf("note = %q", diff.Note)
	}
}

// TestCompareDumpsFormatAudit covers the failure that stops a merged export from
// loading: two format versions, and a file left behind at an older one.
func TestCompareDumpsFormatAudit(t *testing.T) {
	diff, err := CompareDumps(context.Background(), "testdata/dump", "testdata/dumpvariant", true)
	if err != nil {
		t.Fatalf("CompareDumps: %v", err)
	}
	if diff.Format == nil {
		t.Fatal("format audit was requested but not returned")
	}
	if diff.Format.VersionA != "2.21" || diff.Format.VersionB != "2.20" {
		t.Errorf("versions = %q / %q, want 2.21 / 2.20", diff.Format.VersionA, diff.Format.VersionB)
	}
	if !strings.Contains(diff.Format.Note, "различаются") {
		t.Errorf("mismatch not called out: %q", diff.Format.Note)
	}
	if len(diff.Format.OutliersB) == 0 {
		t.Error("the file left at 2.17 was not reported as an outlier")
	}
}

func TestCompareDumpsSkipsFormatByDefault(t *testing.T) {
	diff, _ := CompareDumps(context.Background(), "testdata/dump", "testdata/dumpvariant", false)
	if diff.Format != nil {
		t.Error("format audit walks every file; it must stay opt-in")
	}
}

func TestCompareDumpsMissingPath(t *testing.T) {
	if _, err := CompareDumps(context.Background(), "testdata/dump", "testdata/нет-такой-выгрузки", false); err == nil {
		t.Error("expected an error for a missing export")
	}
}

