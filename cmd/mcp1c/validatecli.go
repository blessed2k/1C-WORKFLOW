package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/validate"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// validateCLIReport is what `mcp1c validate-bsl` prints: one JSON document.
type validateCLIReport struct {
	Project    string            `json:"project,omitempty"`
	Components []validateCLIComp `json:"components"`
	Checked    int               `json:"checkedFiles"`
	Skipped    []string          `json:"skipped,omitempty"`
	High       int               `json:"high"`
	Medium     int               `json:"medium"`
	Low        int               `json:"low"`
	Files      []validateCLIFile `json:"files"`
	Notes      []string          `json:"notes,omitempty"`
}

type validateCLIComp struct {
	ID        string `json:"id"`
	Root      string `json:"root"`
	Extension bool   `json:"extension"`
}

type validateCLIFile struct {
	Path     string             `json:"path"`
	Module   string             `json:"module,omitempty"`
	Findings []validate.Finding `json:"findings"`
}

// runValidateBSL is `mcp1c validate-bsl`: the checks of validate_bsl over .bsl
// files on disk, for a pipeline step. The file list comes from arguments
// (files or directories) and from -files-from (a file or "-" for stdin), so
// the changes of a merge request reach it as
//
//	git diff --name-only --diff-filter=AM A B -- '*.bsl' | mcp1c validate-bsl -project <dump> -files-from -
//
// The server forks nothing (nosubprocess_test.go), git included, hence the
// pipe. Exit code: 0 no high findings, 1 high findings, 2 usage or setup error.
func runValidateBSL(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp1c validate-bsl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectDir := fs.String("project", "", "каталог проекта с 1c-project.json (типовая и расширения) или каталог выгрузки")
	filesFrom := fs.String("files-from", "", "файл со списком .bsl по строке, - читает stdin")
	minSeverity := fs.String("min-severity", "low", "печатать находки не ниже: high, medium, low")
	var opts options
	registerSyntaxIndexFlag(fs, &opts)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rank, ok := severityRank[*minSeverity]
	if !ok {
		fmt.Fprintf(stderr, "mcp1c validate-bsl: -min-severity %q: ожидается high, medium или low\n", *minSeverity)
		return 2
	}

	files, skipped, err := collectBSLFiles(fs.Args(), *filesFrom, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "mcp1c validate-bsl: %v\n", err)
		return 2
	}
	if len(files) == 0 && len(skipped) == 0 {
		fmt.Fprintln(stderr, "mcp1c validate-bsl: не заданы файлы: пути аргументами или -files-from")
		return 2
	}

	idx := syntax.NewLazy(opts.syntaxIndexPath())
	if err := idx.Err(); err != nil {
		fmt.Fprintf(stderr, "mcp1c validate-bsl: %v\n", err)
		return 2
	}

	report := validateCLIReport{Components: []validateCLIComp{}, Files: []validateCLIFile{}, Skipped: skipped}
	var comps []validate.Component
	if *projectDir != "" {
		comps, report.Project, err = cliComponents(*projectDir)
		if err != nil {
			fmt.Fprintf(stderr, "mcp1c validate-bsl: %v\n", err)
			return 2
		}
	}
	project := validate.NewDiskProject(comps)
	// Files outside the listed components bring their own dump in
	// (ComponentFor walks up to its Configuration.xml), before the metadata
	// is read.
	for _, f := range files {
		project.ComponentFor(f)
	}
	ctx := context.Background()
	var meta validate.MetadataLookup
	if names, err := projectMetadataNames(ctx, nil, project); err == nil && len(names) > 0 {
		meta = func(mdType, name string) bool { return names[mdType+"."+strings.ToLower(name)] }
	} else {
		report.Notes = append(report.Notes, "ссылки на метаданные НЕ проверены: Configuration.xml компонентов не прочитан")
	}
	env := validateEnv(idx, meta, project)
	report.Notes = append(report.Notes, projectNote(env))
	if !project.Complete() {
		report.Notes = append(report.Notes, "основной конфигурации нет среди компонентов: передайте -project с 1c-project.json, где есть типовая")
	}
	for _, c := range project.Components() {
		report.Components = append(report.Components, validateCLIComp{ID: c.ID, Root: c.Root, Extension: c.Extension})
	}

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			report.Skipped = append(report.Skipped, path+": "+err.Error())
			continue
		}
		report.Checked++
		fileEnv := env
		fileEnv.Module = validate.ResolveModule(project, validate.ModuleInput{Path: path}, idx.MemberNames)
		code := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
		var kept []validate.Finding
		for _, f := range validate.Check(code, fileEnv) {
			switch f.Severity {
			case "high":
				report.High++
			case "medium":
				report.Medium++
			default:
				report.Low++
			}
			if severityRank[f.Severity] >= rank {
				kept = append(kept, f)
			}
		}
		if len(kept) > 0 {
			report.Files = append(report.Files, validateCLIFile{Path: path, Module: fileEnv.Module.Display, Findings: kept})
		}
	}

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(stderr, "mcp1c validate-bsl: %v\n", err)
		return 2
	}
	if report.High > 0 {
		return 1
	}
	return 0
}

var severityRank = map[string]int{"low": 0, "medium": 1, "high": 2}

// cliComponents reads the components of a project directory: the manifest
// when there is one, else the directory itself as one dump.
func cliComponents(dir string) ([]validate.Component, string, error) {
	if _, err := os.Stat(filepath.Join(dir, workspace.ManifestFileName)); err == nil {
		m, err := workspace.LoadManifest(dir)
		if err != nil {
			return nil, "", err
		}
		var out []validate.Component
		for _, c := range m.Components {
			out = append(out, validate.Component{ID: string(c.ID), Root: c.AbsRoot, Extension: c.Kind == domain.KindExtension})
		}
		return out, string(m.Project), nil
	}
	if _, err := os.Stat(filepath.Join(dir, "Configuration.xml")); err != nil {
		return nil, "", fmt.Errorf("-project %s: нет ни %s, ни Configuration.xml", dir, workspace.ManifestFileName)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return []validate.Component{{ID: filepath.Base(abs), Root: abs, Extension: validate.IsExtensionDump(abs)}}, "", nil
}

// collectBSLFiles expands directories, reads the list file and keeps .bsl
// files that exist. A listed file that is gone (deleted in the diff) is
// reported as skipped, not as an error.
func collectBSLFiles(args []string, filesFrom string, stdin io.Reader) (files, skipped []string, err error) {
	var paths []string
	paths = append(paths, args...)
	if filesFrom != "" {
		var r io.Reader = stdin
		if filesFrom != "-" {
			f, err := os.Open(filesFrom)
			if err != nil {
				return nil, nil, err
			}
			defer f.Close()
			r = f
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			if p := strings.TrimSpace(sc.Text()); p != "" {
				paths = append(paths, p)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, nil, err
		}
	}
	seen := map[string]bool{}
	add := func(p string) {
		if !strings.EqualFold(filepath.Ext(p), ".bsl") || seen[p] {
			return
		}
		seen[p] = true
		files = append(files, p)
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			if strings.EqualFold(filepath.Ext(p), ".bsl") {
				skipped = append(skipped, p+": файла нет")
			}
			continue
		}
		if !st.IsDir() {
			add(p)
			continue
		}
		werr := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				add(path)
			}
			return nil
		})
		if werr != nil {
			return nil, nil, werr
		}
	}
	sort.Strings(files)
	return files, skipped, nil
}
