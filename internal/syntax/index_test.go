package syntax

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
)

// fixturePath is the hand-written corpus shared with package syntaxtest. The
// syntax tests cannot import syntaxtest (it imports syntax), so they read the
// same file by path.
var fixturePath = filepath.Join("syntaxtest", "testdata", "corpus.json")

func loadFixture(t *testing.T) *Index {
	t.Helper()
	ix, err := LoadFile(fixturePath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	return ix
}

func TestSearch(t *testing.T) {
	ix := loadFixture(t)
	if ix.Count() < 10 {
		t.Fatalf("index too small: %d entries", ix.Count())
	}

	if !hasSig(ix.Search("Сообщить", 5), "Сообщить", "Сообщить(") {
		t.Errorf("Сообщить not found with a signature")
	}
	if len(ix.Search("StrFind", 3)) == 0 {
		t.Errorf("StrFind (СтрНайти) not found by English name")
	}

	res := ix.Search("ЗначениеЗаполнено", 10)
	if len(res) == 0 || res[0].NameRu != "ЗначениеЗаполнено" {
		t.Errorf("exact match should rank first; got %v", names(res))
	}
	if ix.Search("", 5) != nil {
		t.Errorf("empty query should return nil")
	}

	// Language operator (shlang) with a syntax template.
	foundOp := false
	for _, e := range ix.Search("Если", 10) {
		if e.Kind == "operator" && strings.Contains(e.Signature, "Тогда") {
			foundOp = true
		}
	}
	if !foundOp {
		t.Errorf("language operator Если (with signature) not found")
	}

	// Query-language entry (shquery). LEFTJOIN is unambiguous (no property collides).
	foundQuery := false
	for _, e := range ix.Search("LEFTJOIN", 10) {
		if e.Kind == "query" {
			foundQuery = true
		}
	}
	if !foundQuery {
		t.Errorf("query-language entry LEFTJOIN not found")
	}
}

func TestRankPreview(t *testing.T) {
	ix := loadFixture(t)
	for _, q := range []string{"Количество", "Формат", "Дата", "Найти"} {
		var parts []string
		for _, e := range ix.Search(q, 5) {
			owner := e.Owner
			if owner == "" {
				owner = "-"
			}
			parts = append(parts, e.NameRu+"["+e.Kind+"/"+owner+"]")
		}
		t.Logf("%-12s -> %s", q, strings.Join(parts, ", "))
	}
}

// TestLoadFileReadsGzip: syntaxgen writes gzip, fixtures are plain JSON; both
// load through the same call.
func TestLoadFileReadsGzip(t *testing.T) {
	plain, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "syntax-index.json.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(gzip): %v", err)
	}
	if ix.Count() != loadFixture(t).Count() {
		t.Errorf("gzip index has %d entries, plain %d", ix.Count(), loadFixture(t).Count())
	}
}

// TestLoadFileMissingNamesTheFix: a missing index is ErrNotFound, and the message
// carries the path and the command that generates the file.
func TestLoadFileMissingNamesTheFix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "нет-такого.json.gz")
	for _, p := range []string{path, ""} {
		_, err := LoadFile(p)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("LoadFile(%q) = %v, want ErrNotFound", p, err)
		}
		if !strings.Contains(err.Error(), "cmd/syntaxgen") || !strings.Contains(err.Error(), EnvPath) {
			t.Errorf("LoadFile(%q): сообщение без команды генерации или переменной: %v", p, err)
		}
		if p != "" && !strings.Contains(err.Error(), p) {
			t.Errorf("LoadFile(%q): сообщение не называет путь: %v", p, err)
		}
	}
}

// TestLoadFileBrokenIsNotNotFound: a file that exists but does not parse is a
// different failure and must not read as "generate it".
func TestLoadFileBrokenIsNotNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadFile(broken) = %v, want a parse error", err)
	}
}

// TestDefaultPathUnderUserConfigDir pins where syntaxgen writes and the server
// reads by default.
func TestDefaultPathUnderUserConfigDir(t *testing.T) {
	dir, err := os.UserConfigDir()
	if err != nil {
		if DefaultPath() != "" {
			t.Fatalf("DefaultPath() = %q without a user config dir", DefaultPath())
		}
		return
	}
	want := filepath.Join(dir, "mcp1c", "syntax-index.json.gz")
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func hasSig(es []onec.SyntaxEntry, name, sigSub string) bool {
	for _, e := range es {
		if e.NameRu == name && strings.Contains(e.Signature, sigSub) {
			return true
		}
	}
	return false
}

func names(es []onec.SyntaxEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.NameRu
	}
	return out
}
