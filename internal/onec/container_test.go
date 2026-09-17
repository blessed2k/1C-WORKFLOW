package onec

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestParseHBK parses the whole syntax-help container into entries. Env-gated.
func TestParseHBK(t *testing.T) {
	path := os.Getenv("ONEC_HBK")
	if path == "" {
		t.Skip("set ONEC_HBK")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ParseHBK(data, "")
	if err != nil {
		t.Fatalf("ParseHBK: %v", err)
	}
	t.Logf("parsed %d syntax entries", len(entries))

	byRu := map[string]SyntaxEntry{}
	kinds := map[string]int{}
	withSig := 0
	for _, e := range entries {
		byRu[e.NameRu] = e
		kinds[e.Kind]++
		if e.Signature != "" {
			withSig++
		}
	}
	t.Logf("kinds: %v; with signature: %d", kinds, withSig)

	if e, ok := byRu["КопироватьДанныеФормы"]; ok {
		t.Logf("КопироватьДанныеФормы: en=%q owner=%q kind=%q\n  sig=%q\n  returns=%q\n  desc=%q",
			e.NameEn, e.Owner, e.Kind, e.Signature, e.Returns, e.Description)
	} else {
		t.Error("КопироватьДанныеФормы not found")
	}
	for _, name := range []string{"Сообщить", "Найти", "ЗначениеЗаполнено"} {
		if e, ok := byRu[name]; ok {
			t.Logf("%s: [%s of %s] sig=%q", name, e.Kind, e.Owner, e.Signature)
		}
	}
}

// TestExploreHBK categorizes FileStorage .st files and dumps ones containing
// ONEC_FIND, to reverse-engineer the .st schema. Env-gated exploration.
func TestExploreHBK(t *testing.T) {
	path := os.Getenv("ONEC_HBK")
	if path == "" {
		t.Skip("set ONEC_HBK")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ReadContainer(data)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(entries["FileStorage"]), int64(len(entries["FileStorage"])))
	if err != nil {
		t.Fatalf("FileStorage zip: %v", err)
	}

	// Path taxonomy: count by first segment and by .st filename prefix.
	seg := map[string]int{}
	prefix := map[string]int{}
	for _, zf := range zr.File {
		s := zf.Name
		if i := strings.Index(s, "/"); i >= 0 {
			seg[s[:i]]++
		} else {
			seg["<root>"]++
		}
		base := s
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if i := strings.Index(base, "_"); i >= 0 {
			prefix[base[:i+1]]++
		} else {
			prefix["<noprefix>"]++
		}
	}
	t.Logf("first-segment counts: %v", seg)
	t.Logf("filename-prefix counts: %v", prefix)

	target := os.Getenv("ONEC_FIND")
	if target == "" {
		return
	}
	found := 0
	for _, zf := range zr.File {
		if !strings.Contains(zf.Name, target) {
			continue
		}
		rc, _ := zf.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		t.Logf("=== %s (%d bytes) ===\n%s", zf.Name, len(b), string(b))
		found++
		if found >= 3 {
			break
		}
	}
	t.Logf("matched %d files containing %q", found, target)
}

// TestReadContainerReal parses a real 1C container (.hbk/.cf) when ONEC_HBK is
// set, printing entry count and a few names/snippets. Skipped by default.
func TestReadContainerReal(t *testing.T) {
	path := os.Getenv("ONEC_HBK")
	if path == "" {
		t.Skip("set ONEC_HBK to a .hbk/.cf/.cfe file to run this")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ReadContainer(data)
	if err != nil {
		t.Fatalf("ReadContainer: %v", err)
	}
	t.Logf("entries: %d", len(entries))

	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		if i >= 10 {
			break
		}
		c := entries[n]
		snippet := strings.ReplaceAll(string(c), "\r", " ")
		snippet = strings.ReplaceAll(snippet, "\n", " ")
		if len(snippet) > 140 {
			snippet = snippet[:140]
		}
		t.Logf("  %q (%d bytes): %s", n, len(c), snippet)
	}

	fs, ok := entries["FileStorage"]
	if !ok {
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(fs), int64(len(fs)))
	if err != nil {
		t.Logf("FileStorage is not a zip: %v", err)
		return
	}
	t.Logf("FileStorage: %d zip entries", len(zr.File))
	for i, zf := range zr.File {
		if i >= 12 {
			break
		}
		t.Logf("  zip: %s (%d bytes)", zf.Name, zf.UncompressedSize64)
	}
	// Dump the largest .st file — typically a rich API member page.
	var big *zip.File
	for _, zf := range zr.File {
		if !strings.HasSuffix(zf.Name, ".st") {
			continue
		}
		if big == nil || zf.UncompressedSize64 > big.UncompressedSize64 {
			big = zf
		}
	}
	if big != nil {
		rc, _ := big.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		s := string(b)
		if len(s) > 2500 {
			s = s[:2500]
		}
		t.Logf("=== largest .st: %s (%d bytes) ===\n%s", big.Name, big.UncompressedSize64, s)
	}
}
