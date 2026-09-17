package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// copyExportFixture makes a throwaway copy of the test export so a test can age
// it without touching the fixture.
func copyExportFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := dumpFixturePath(t)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	return dst
}

func callSetDump(t *testing.T, path string) string {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := newServer(options{}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_dump", Arguments: map[string]any{"path": path}})
	if err != nil {
		t.Fatalf("call set_dump: %v", err)
	}
	return contentText(res)
}

// TestSetDumpWarnsOnStaleExport: reading a two-week-old export and reporting
// "this code is not in the configuration" is a guess presented as a fact. That
// happened, so the age has to come back with the switch itself.
func TestSetDumpWarnsOnStaleExport(t *testing.T) {
	dir := copyExportFixture(t)
	old := time.Now().Add(-20 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "Configuration.xml"), old, old); err != nil {
		t.Fatalf("age the export: %v", err)
	}

	got := callSetDump(t, dir)
	if !strings.Contains(got, `"warning"`) {
		t.Errorf("no staleness warning for a 20-day-old export; got: %s", got)
	}
	if !strings.Contains(got, "check_sync") {
		t.Errorf("warning does not say how to verify; got: %s", got)
	}
	if !strings.Contains(got, `"ageDays":20`) {
		t.Errorf("age not reported; got: %s", got)
	}
}

// TestSetDumpQuietOnFreshExport keeps the warning meaningful: a fresh export
// must not carry one, or it becomes noise to scroll past.
func TestSetDumpQuietOnFreshExport(t *testing.T) {
	dir := copyExportFixture(t)
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "Configuration.xml"), now, now); err != nil {
		t.Fatalf("touch the export: %v", err)
	}

	got := callSetDump(t, dir)
	if strings.Contains(got, `"warning"`) {
		t.Errorf("fresh export should not warn; got: %s", got)
	}
	if !strings.Contains(got, `"exportedAt"`) {
		t.Errorf("export date should still be reported; got: %s", got)
	}
}
