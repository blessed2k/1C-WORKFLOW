package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// coreStub is a core-methods source whose computation the test controls.
type coreStub struct {
	mu    sync.Mutex
	item  app.APICoreItem
	ready bool
	err   error
	panic bool
	calls int
	// gate, when set, holds every computation until the test closes it.
	gate chan struct{}
}

func (s *coreStub) CoreMethodsIfReady() (app.APICoreItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.item, s.ready
}

func (s *coreStub) CoreMethods(ctx context.Context) (app.Response[app.APICoreItem], error) {
	s.mu.Lock()
	s.calls++
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return app.Response[app.APICoreItem]{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panic {
		panic("index is broken")
	}
	if s.err != nil {
		return app.Response[app.APICoreItem]{}, s.err
	}
	s.ready = true
	return app.Response[app.APICoreItem]{Items: []app.APICoreItem{s.item}}, nil
}

func (s *coreStub) set(f func(*coreStub)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *coreStub) computed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func coreList(method string) app.APICoreItem {
	return app.APICoreItem{BSPVersion: "3.1.11.366", Modules: []app.APICoreModule{{Module: "ОбщегоНазначения", Methods: []string{method}}}}
}

func testWarmer(t *testing.T, stub *coreStub) (*coreWarmer, string) {
	t.Helper()
	dir := t.TempDir()
	w := newCoreWarmer(stub, func() app.ActiveProjectState {
		return app.ActiveProjectState{Project: "ut", ProjectRoot: "/проекты/ут", DumpDir: "/проекты/ут/src"}
	}, dir)
	t.Cleanup(w.stop)
	return w, dir
}

func readSnapshot(t *testing.T, dir string) coreSnapshot {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("snapshot files: %v, want one", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var snap coreSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot %s: %v", data, err)
	}
	return snap
}

// TestCoreWarmer: server_info does not wait for the list. While it is being
// computed report says so and starts one computation, not one per call; once
// it is there report returns it and the hook's file is written.
func TestCoreWarmer(t *testing.T) {
	stub := &coreStub{gate: make(chan struct{}), item: coreList("ЗначениеРеквизитаОбъекта")}
	w, dir := testWarmer(t, stub)

	for i := 0; i < 3; i++ {
		if modules, note := w.report(); modules != nil || note != coreNotePending {
			t.Fatalf("before the computation: %+v, note %q", modules, note)
		}
	}
	close(stub.gate)
	w.wg.Wait()
	if n := stub.computed(); n != 1 {
		t.Errorf("computations started: %d, want 1", n)
	}

	modules, note := w.report()
	w.wg.Wait()
	if len(modules) != 1 || modules[0].Module != "ОбщегоНазначения" || note != "" {
		t.Fatalf("after the computation: %+v, note %q", modules, note)
	}
	snap := readSnapshot(t, dir)
	if snap.Root != "/проекты/ут" || snap.Dump != "/проекты/ут/src" || snap.BSPVersion != "3.1.11.366" || len(snap.Modules) != 1 || snap.Written == "" {
		t.Errorf("snapshot = %+v", snap)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

// TestCoreWarmerRefreshes: every report refreshes the list in the background,
// so a list computed before a reindex does not stick: the next computation
// brings the new one, and the hook's file follows.
func TestCoreWarmerRefreshes(t *testing.T) {
	stub := &coreStub{item: coreList("СтарыйМетод")}
	w, dir := testWarmer(t, stub)
	w.report()
	w.wg.Wait()
	if got := readSnapshot(t, dir).Modules[0].Methods[0]; got != "СтарыйМетод" {
		t.Fatalf("first snapshot: %q", got)
	}

	stub.set(func(s *coreStub) { s.item = coreList("НовыйМетод") })
	w.report() // returns what was ready and starts the refresh
	w.wg.Wait()
	if got := readSnapshot(t, dir).Modules[0].Methods[0]; got != "НовыйМетод" {
		t.Errorf("snapshot after the refresh: %q, want the new list", got)
	}
	if n := stub.computed(); n != 2 {
		t.Errorf("computations: %d, want one per report", n)
	}
}

// TestCoreWarmerEmptyList: a computation that found nothing (no БСП) is not an
// error and is not announced as pending forever; no file is written.
func TestCoreWarmerEmptyList(t *testing.T) {
	stub := &coreStub{}
	w, dir := testWarmer(t, stub)
	w.report()
	w.wg.Wait()
	if modules, note := w.report(); modules != nil || note != "" {
		t.Errorf("empty list: %+v, note %q; want silence", modules, note)
	}
	w.wg.Wait()
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Errorf("files written for an empty list: %v", files)
	}
}

// TestCoreWarmerSurvivesFailure: a failed or panicking computation does not
// take the server down, is named in the note instead of "still computing", and
// the next report retries. A nil warmer (a server without indexed projects) is
// inert.
func TestCoreWarmerSurvivesFailure(t *testing.T) {
	stub := &coreStub{err: errors.New("нет активного проекта")}
	w, dir := testWarmer(t, stub)
	w.report()
	w.wg.Wait()
	if _, note := w.report(); !strings.Contains(note, "не посчитаны: нет активного проекта") {
		t.Errorf("after a failure the note is %q", note)
	}
	w.wg.Wait()

	stub.set(func(s *coreStub) { s.err, s.panic = nil, true })
	w.report()
	w.wg.Wait()
	if _, note := w.report(); !strings.Contains(note, "сбой расчёта") {
		t.Errorf("after a panic the note is %q", note)
	}
	w.wg.Wait()
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Errorf("files written after failures: %v", files)
	}

	// Recovery: the computation works again, the list arrives.
	stub.set(func(s *coreStub) {
		s.panic, s.item = false, coreList("ЗначениеРеквизитаОбъекта")
	})
	w.report()
	w.wg.Wait()
	if modules, note := w.report(); len(modules) != 1 || note != "" {
		t.Errorf("after recovery: %+v, note %q", modules, note)
	}

	var none *coreWarmer
	if modules, note := none.report(); modules != nil || note != "" {
		t.Errorf("nil warmer: %+v, %q", modules, note)
	}
	none.warm()
	none.stop()
}

// TestCoreWarmerRetriesSnapshot: a snapshot that could not be written is
// written by the next computation: a failed write is not remembered as done.
func TestCoreWarmerRetriesSnapshot(t *testing.T) {
	stub := &coreStub{item: coreList("ЗначениеРеквизитаОбъекта")}
	w, dir := testWarmer(t, stub)
	// The snapshot directory is a file: MkdirAll fails.
	blocked := filepath.Join(dir, "занято")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.dir = filepath.Join(blocked, "core")
	w.report()
	w.wg.Wait()

	w.dir = filepath.Join(dir, "core")
	w.report()
	w.wg.Wait()
	if snap := readSnapshot(t, w.dir); len(snap.Modules) != 1 {
		t.Errorf("snapshot after a retry: %+v", snap)
	}
}

// TestCoreWarmerStop: stop cancels a running computation and nothing starts
// after it.
func TestCoreWarmerStop(t *testing.T) {
	stub := &coreStub{gate: make(chan struct{})}
	w, _ := testWarmer(t, stub)
	w.warm()
	w.stop()
	w.warm()
	w.report()
	w.wg.Wait()
	if n := stub.computed(); n != 1 {
		t.Errorf("computations started: %d, want 1 (none after stop)", n)
	}
}

// TestCoreSnapshotDirResolution: the zero options write no snapshot, so a test
// never touches the user's cache directory; none switches the file off.
func TestCoreSnapshotDirResolution(t *testing.T) {
	if got := (options{}).coreSnapshotDir(); got != "" {
		t.Errorf("zero options write the snapshot to %q, want nowhere", got)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, defaultCoreSnapshotDir()},
		{[]string{"--core-snapshot=/cache/core"}, "/cache/core"},
		{[]string{"--core-snapshot=none"}, ""},
		{[]string{"--core-snapshot="}, ""},
	} {
		var o options
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		registerCoreSnapshotFlag(fs, &o)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatalf("parse %v: %v", tc.args, err)
		}
		if got := o.coreSnapshotDir(); got != tc.want {
			t.Errorf("%v: dir = %q, want %q", tc.args, got, tc.want)
		}
	}
}
