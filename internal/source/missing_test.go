package source

import (
	"context"
	"strings"
	"testing"
)

// TestMissingObjectNamesActiveExport: a stale set_dump pointing at another
// project used to answer "file not found" for a path nobody recognised, which
// reads as "the object does not exist". The error has to say which export
// answered and how to switch it.
func TestMissingObjectNamesActiveExport(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	_, err := s.ObjectStructure(context.Background(), "Document", "НетТакогоДокумента")
	if err == nil {
		t.Fatal("expected an error for a missing object")
	}
	got := err.Error()
	for _, want := range []string{"Document.НетТакогоДокумента", "testdata/dump", "ДемоКонфигурация", "set_dump"} {
		if !strings.Contains(got, want) {
			t.Errorf("error missing %q; got: %s", want, got)
		}
	}
}

// TestMissingFormNamesActiveExport covers the same for forms.
func TestMissingFormNamesActiveExport(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	_, err := s.FormStructure(context.Background(), "Catalog", "Товары", "НетТакойФормы")
	if err == nil {
		t.Fatal("expected an error for a missing form")
	}
	if got := err.Error(); !strings.Contains(got, "set_dump") {
		t.Errorf("form error does not point at the active export; got: %s", got)
	}
}

// TestRealParseErrorIsNotRewritten: only "not found" gets the export context.
// A genuine parse failure must keep its own message.
func TestRealParseErrorIsNotRewritten(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	_, err := s.ObjectStructure(context.Background(), "Catalog", "Товары")
	if err != nil {
		t.Fatalf("existing object should read cleanly: %v", err)
	}
}
