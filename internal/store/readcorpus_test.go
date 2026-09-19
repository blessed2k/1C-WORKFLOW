package store

import (
	"context"
	"testing"
)

// TestSourceFilesByComponent — выборка, на которой стоит гидратация корпуса
// (store выставляет выборку source_file компонента
// rel_path/size/mtime_ns/content_hash/parser_version). Ожидаемое взято из
// seedFixture, а не из кода выборки.
func TestSourceFilesByComponent(t *testing.T) {
	s, _ := seeded(t)

	var got []SourceFileState
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		got, err = tx.SourceFilesByComponent(fxComponent)
		return err
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	want := []string{
		"Catalogs/Y.xml",
		"Catalogs/Y/Forms/ФормаЭлемента/Ext/Form.xml",
		"CommonModules/X.xml",
		"CommonModules/X/Ext/Module.bsl",
	}
	if len(got) != len(want) {
		t.Fatalf("получено %d строк, ожидалось %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].RelPath != w {
			t.Errorf("строка %d: rel_path = %q, want %q", i, got[i].RelPath, w)
		}
	}

	// Значения полей — те, что положила seedFixture: тело "<Module/>" (9 байт),
	// mtime 1, parser_version 1, непустой content_hash.
	var xml SourceFileState
	for _, r := range got {
		if r.RelPath == "CommonModules/X.xml" {
			xml = r
		}
	}
	if xml.Size != int64(len("<Module/>")) {
		t.Errorf("size = %d, want %d", xml.Size, len("<Module/>"))
	}
	if xml.MtimeNS != 1 {
		t.Errorf("mtime_ns = %d, want 1", xml.MtimeNS)
	}
	if xml.ParserVersion != 1 {
		t.Errorf("parser_version = %d, want 1", xml.ParserVersion)
	}
	if xml.ContentHash == "" {
		t.Error("content_hash пуст")
	}

	var other []SourceFileState
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		other, err = tx.SourceFilesByComponent("нет-такого")
		return err
	}); err != nil {
		t.Fatalf("Read(чужой компонент): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("чужой компонент вернул %d строк, want 0", len(other))
	}
}
