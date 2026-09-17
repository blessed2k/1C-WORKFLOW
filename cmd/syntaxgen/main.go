// Command syntaxgen builds the BSL syntax index from the syntax-help containers
// (.hbk) of the user's own 1C platform install. The reference text belongs to
// the platform vendor, so the index is generated locally and never committed.
// Run once per platform version:
//
//	go run ./cmd/syntaxgen <platform dir>/shcntx_ru.hbk <platform dir>/shlang_ru.hbk <platform dir>/shquery_ru.hbk
//
// Typical platform directories:
//
//	Windows: C:\Program Files\1cv8\<version>\bin
//	macOS:   /opt/1cv8/<version>
//	Linux:   /opt/1cv8/x86_64/<version>
//
// By default the index goes to syntax.DefaultPath() (<user config dir>/mcp1c/
// syntax-index.json.gz), where the server looks for it; -out writes elsewhere,
// and the server then needs --syntax-index or MCP_1C_SYNTAX_INDEX. shlang_ru
// adds the language operators, shquery_ru the query language; both optional.
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// dedup removes entries that are identical in every meaningful field, collapsing
// members that the platform help documents more than once.
func dedup(entries []onec.SyntaxEntry) []onec.SyntaxEntry {
	seen := make(map[string]struct{}, len(entries))
	out := entries[:0]
	for _, e := range entries {
		key := strings.Join([]string{e.NameRu, e.NameEn, e.Kind, e.Owner, e.Signature, e.Params, e.Returns, e.Description}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
}

// kindForFile maps a syntax-help filename to the default entry kind.
func kindForFile(path string) string {
	lb := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(lb, "shlang"):
		return "operator"
	case strings.Contains(lb, "shquery"):
		return "query"
	default:
		return ""
	}
}

func main() {
	out := flag.String("out", syntax.DefaultPath(), "output index path")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: syntaxgen [-out path] <hbk-file>...")
		os.Exit(2)
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "no user config directory on this system: pass -out")
		os.Exit(2)
	}

	var all []onec.SyntaxEntry
	for _, path := range flag.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
			os.Exit(1)
		}
		entries, err := onec.ParseHBK(data, kindForFile(path))
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("%s: %d entries\n", filepath.Base(path), len(entries))
		all = append(all, entries...)
	}

	before := len(all)
	all = dedup(all)
	fmt.Printf("deduplicated: %d -> %d entries\n", before, len(all))

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	if err := enc.Encode(all); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := gz.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	info, _ := f.Stat()
	fmt.Printf("wrote %s: %d entries, %d bytes (gzipped)\n", *out, len(all), info.Size())
}
