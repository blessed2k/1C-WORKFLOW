package source

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DumpDiff compares two XML exports. The question it answers comes up on every
// re-export: what is in the working copy that the fresh export does not have,
// and can the two be merged at all. Doing it by hand means regex over
// Configuration.xml and two hash sets in PowerShell.
type DumpDiff struct {
	DumpA          string       `json:"dumpA"`
	DumpB          string       `json:"dumpB"`
	ConfigurationA string       `json:"configurationA,omitempty"`
	ConfigurationB string       `json:"configurationB,omitempty"`
	OnlyInA        []string     `json:"onlyInA" jsonschema:"objects present in A and missing in B"`
	OnlyInB        []string     `json:"onlyInB" jsonschema:"objects present in B and missing in A"`
	CommonCount    int          `json:"commonCount" jsonschema:"objects present in both"`
	Format         *FormatAudit `json:"format,omitempty" jsonschema:"XML format versions, when withFormat was requested"`
	Note           string       `json:"note,omitempty"`
}

// FormatAudit reports the export format version of both dumps. A single file
// left at an older version is a classic reason a merged export refuses to load,
// and it is only visible by walking every file.
type FormatAudit struct {
	VersionA  string   `json:"versionA,omitempty"`
	VersionB  string   `json:"versionB,omitempty"`
	FilesA    int      `json:"filesA"`
	FilesB    int      `json:"filesB"`
	OutliersA []string `json:"outliersA,omitempty" jsonschema:"files in A whose version differs from the prevailing one"`
	OutliersB []string `json:"outliersB,omitempty" jsonschema:"files in B whose version differs from the prevailing one"`
	Note      string   `json:"note,omitempty"`
}

// formatScanLimit caps the format walk: a full trade configuration has tens of
// thousands of XML files, and the answer stops changing long before that.
const formatScanLimit = 20000

// versionAttr matches the export format version on the ROOT element. The `<?xml
// version="1.0"?>` declaration carries the same attribute name and would
// otherwise report every file as format 1.0.
var versionAttr = regexp.MustCompile(`<[A-Za-z][^>]*?\sversion="(\d+\.\d+)"`)

// CompareDumps diffs the object composition of two exports, optionally auditing
// their format versions.
func CompareDumps(ctx context.Context, dirA, dirB string, withFormat bool) (*DumpDiff, error) {
	a, b := NewXMLSource(dirA), NewXMLSource(dirB)

	treeA, err := a.MetadataTree(ctx)
	if err != nil {
		return nil, fmt.Errorf("выгрузка A (%s): %w", dirA, err)
	}
	treeB, err := b.MetadataTree(ctx)
	if err != nil {
		return nil, fmt.Errorf("выгрузка B (%s): %w", dirB, err)
	}

	out := &DumpDiff{
		DumpA: dirA, DumpB: dirB,
		ConfigurationA: treeA.Configuration, ConfigurationB: treeB.Configuration,
		OnlyInA: []string{}, OnlyInB: []string{},
	}

	setA, setB := objectSet(treeA), objectSet(treeB)
	for name := range setA {
		if setB[name] {
			out.CommonCount++
		} else {
			out.OnlyInA = append(out.OnlyInA, name)
		}
	}
	for name := range setB {
		if !setA[name] {
			out.OnlyInB = append(out.OnlyInB, name)
		}
	}
	sort.Strings(out.OnlyInA)
	sort.Strings(out.OnlyInB)

	if withFormat {
		fa, filesA, outA := scanFormat(dirA)
		fb, filesB, outB := scanFormat(dirB)
		out.Format = &FormatAudit{
			VersionA: fa, VersionB: fb,
			FilesA: filesA, FilesB: filesB,
			OutliersA: outA, OutliersB: outB,
		}
		if fa != "" && fb != "" && fa != fb {
			out.Format.Note = fmt.Sprintf(
				"версии формата различаются (%s против %s): перенос потребует приведения к одной, иначе загрузка откажет", fa, fb)
		}
	}

	switch {
	case len(out.OnlyInA) == 0 && len(out.OnlyInB) == 0:
		out.Note = "состав объектов совпадает"
	case len(out.OnlyInA) > 0:
		out.Note = fmt.Sprintf("в A есть %d объектов, которых нет в B — это то, что потеряется при замене A на B", len(out.OnlyInA))
	}
	return out, nil
}

func objectSet(tree *MetadataTree) map[string]bool {
	set := make(map[string]bool)
	for _, g := range tree.Groups {
		for _, name := range g.Objects {
			set[g.Type+"."+name] = true
		}
	}
	return set
}

// scanFormat returns the prevailing format version, how many files were read and
// the files that disagree with the majority.
func scanFormat(dir string) (string, int, []string) {
	counts := map[string]int{}
	perFile := map[string]string{}
	read := 0

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".xml") {
			return nil
		}
		if read >= formatScanLimit {
			return filepath.SkipAll
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		buf := make([]byte, 4096) // the version attribute lives in the root tag
		n, _ := f.Read(buf)
		if m := versionAttr.FindSubmatch(buf[:n]); m != nil {
			v := string(m[1])
			counts[v]++
			rel, _ := filepath.Rel(dir, path)
			perFile[filepath.ToSlash(rel)] = v
			read++
		}
		return nil
	})

	prevailing, best := "", 0
	for v, c := range counts {
		if c > best {
			prevailing, best = v, c
		}
	}
	var outliers []string
	for file, v := range perFile {
		if v != prevailing {
			outliers = append(outliers, file+" ("+v+")")
		}
	}
	sort.Strings(outliers)
	if len(outliers) > 20 {
		outliers = outliers[:20]
	}
	return prevailing, read, outliers
}
