package source

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// maxITSRuleShare is the share of queries above which a rule is considered noise.
const maxITSRuleShare = 0.10

// TestRealDumpAdvisorRuleFrequency measures how often every advisor rule fires
// on the query literals of a real export (ONEC_REAL_DUMP) and fails when a rule
// taken from the ITS standards fires on more than maxITSRuleShare of them.
func TestRealDumpAdvisorRuleFrequency(t *testing.T) {
	root := os.Getenv("ONEC_REAL_DUMP")
	if root == "" {
		t.Skip("set ONEC_REAL_DUMP to a 1C XML export directory to run this")
	}

	src := NewXMLSource(root)
	counts := map[string]int{}
	total := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".bsl") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, q := range queryLiterals(string(data)) {
			total++
			adv, err := src.AdviseQuery(context.Background(), q)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, w := range adv.Warnings {
				if !seen[w.Code] {
					seen[w.Code] = true
					counts[w.Code]++
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if total == 0 {
		t.Fatalf("no query literals found under %s", root)
	}

	codes := make([]string, 0, len(counts))
	for c := range counts {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return counts[codes[i]] > counts[codes[j]] })
	t.Logf("queries: %d", total)
	for _, c := range codes {
		t.Logf("%-28s %6d  %5.2f%%", c, counts[c], 100*float64(counts[c])/float64(total))
	}
	for _, c := range itsRuleCodes {
		if share := float64(counts[c]) / float64(total); share > maxITSRuleShare {
			t.Errorf("%s fires on %.1f%% of queries (limit %.0f%%): the rule is noise", c, 100*share, 100*maxITSRuleShare)
		}
	}
}

// queryLiterals returns the string literals of a BSL module that hold a query:
// the literal is unescaped ("" -> "), continuation bars are dropped and comment
// lines between the lines of a literal are skipped.
func queryLiterals(module string) []string {
	var out []string
	for i := 0; i < len(module); i++ {
		switch module[i] {
		case '/':
			if i+1 < len(module) && module[i+1] == '/' {
				for i < len(module) && module[i] != '\n' {
					i++
				}
			}
		case '"':
			lit, end := readLiteral(module, i+1)
			i = end
			if indexOfWord(lit, "ВЫБРАТЬ", 0) >= 0 && indexOfWord(lit, "ИЗ", 0) >= 0 {
				out = append(out, lit)
			}
		}
	}
	return out
}

// readLiteral reads a BSL string literal that starts right after its opening
// quote at from, and returns its text and the index of the closing quote.
func readLiteral(s string, from int) (string, int) {
	var b strings.Builder
	i := from
	for i < len(s) {
		switch s[i] {
		case '"':
			if i+1 < len(s) && s[i+1] == '"' {
				b.WriteByte('"')
				i += 2
				continue
			}
			return b.String(), i
		case '\n':
			b.WriteByte('\n')
			i++
			for {
				j := i
				for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r') {
					j++
				}
				if j+1 < len(s) && s[j] == '/' && s[j+1] == '/' {
					for j < len(s) && s[j] != '\n' {
						j++
					}
					i = j + 1
					continue
				}
				if j < len(s) && s[j] == '|' {
					i = j + 1
					break
				}
				return b.String(), j // the literal is not continued
			}
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), len(s)
}
