package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// The most used БСП methods ("core methods") reach the agent only where the
// agent actually goes. In the developer's own session logs get_context_for_task
// showed up in 5 of the 17 sessions that used this server, and server_info in
// 15. So server_info carries the list too.
//
// server_info must answer at once, and the list costs seconds on the first
// computation (the word index plus the call counts). It is therefore computed
// in the background, from server start, and server_info only reports what is
// already there. The price: every session opens the active project and builds
// the word index in the background even when no indexed tool is ever called.

// coreMethodsSource is what the warmer needs from the API service. Declared on
// the consumer side so that a test can hand in a stub.
type coreMethodsSource interface {
	CoreMethodsIfReady() (app.APICoreItem, bool)
	CoreMethods(ctx context.Context) (app.Response[app.APICoreItem], error)
}

// defaultCoreSnapshotDir is where the list is mirrored as a file for the
// Claude Code hook (tools/hooks/bsl_ready_methods.py): a hook is a separate
// process and cannot ask the server.
func defaultCoreSnapshotDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "mcp1c", "core")
}

// coreSnapshot is the file the hook reads.
type coreSnapshot struct {
	Root       string              `json:"root"`
	Dump       string              `json:"dump,omitempty"`
	BSPVersion string              `json:"bspVersion,omitempty"`
	Written    string              `json:"written"`
	Modules    []app.APICoreModule `json:"modules"`
}

// coreWarmer serves the list to server_info without making it wait.
type coreWarmer struct {
	api   coreMethodsSource
	state func() app.ActiveProjectState
	// dir is the snapshot directory; empty: no snapshot is written.
	dir string
	// ctx is cancelled by stop; wg waits for the background computation, which
	// must not outlive the index it reads.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	warming bool
	stopped bool
	// done: a computation has finished at least once; failed: why the last one
	// did not produce a list.
	done   bool
	failed string
	// written remembers the last snapshot actually on disk, so that the file
	// is rewritten only when the list or the project changed.
	written string
}

func newCoreWarmer(api coreMethodsSource, state func() app.ActiveProjectState, snapshotDir string) *coreWarmer {
	ctx, cancel := context.WithCancel(context.Background())
	return &coreWarmer{api: api, state: state, dir: snapshotDir, ctx: ctx, cancel: cancel}
}

// report is what server_info shows: the list when there is one, otherwise a
// note saying why not. Every call also refreshes the list in the background:
// the cached one may belong to an index generation that a reindex has since
// replaced, and nothing else would notice.
//
// A computation that ended with an empty list (no БСП in the configuration,
// nothing calls it) is not an error and is not announced.
func (w *coreWarmer) report() (modules []app.APICoreModule, note string) {
	if w == nil || w.api == nil {
		return nil, ""
	}
	defer w.warm()
	if item, ready := w.api.CoreMethodsIfReady(); ready && len(item.Modules) > 0 {
		return item.Modules, ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.failed != "":
		return nil, "Ходовые методы БСП не посчитаны: " + w.failed
	case !w.done:
		return nil, coreNotePending
	}
	return nil, ""
}

// warm computes the list in the background unless a computation is already
// running, and mirrors it into the snapshot file.
func (w *coreWarmer) warm() {
	if w == nil || w.api == nil {
		return
	}
	w.mu.Lock()
	if w.warming || w.stopped {
		w.mu.Unlock()
		return
	}
	w.warming = true
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		failed := ""
		defer func() {
			// A panic in the computation must not take the server down: the
			// list is a hint, and this goroutine starts with every session.
			if r := recover(); r != nil {
				failed = fmt.Sprint("сбой расчёта: ", r)
			}
			w.mu.Lock()
			w.warming, w.done, w.failed = false, true, failed
			w.mu.Unlock()
		}()
		resp, err := w.api.CoreMethods(w.ctx)
		switch {
		case err != nil:
			failed = err.Error()
		case len(resp.Items) > 0:
			w.snapshot(resp.Items[0])
		}
	}()
}

// stop cancels the background computation and waits for it. The computation
// itself is not interruptible once it reads the index (seconds on a large
// configuration), so stop, and with it the shutdown of the server, may take
// that long.
func (w *coreWarmer) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	w.cancel()
	w.wg.Wait()
}

// snapshot mirrors the list into a file for the hook. Best effort: a failed
// write costs the hook its list and nothing else, and is retried on the next
// computation. Called only from the background goroutine, never from a tool
// handler.
//
// The project root is read after the computation, not together with it: a
// set_dump in between would label the list with the wrong root until the next
// computation corrects it.
func (w *coreWarmer) snapshot(item app.APICoreItem) {
	if w.dir == "" || w.state == nil || len(item.Modules) == 0 {
		return
	}
	st := w.state()
	if st.ProjectRoot == "" {
		return
	}
	snap := coreSnapshot{Root: st.ProjectRoot, Dump: st.DumpDir, BSPVersion: item.BSPVersion, Modules: item.Modules}
	body, err := json.Marshal(snap)
	if err != nil {
		return
	}
	sum := sha256.Sum256([]byte(st.ProjectRoot))
	name := hex.EncodeToString(sum[:8]) + ".json"
	key := name + string(body)
	w.mu.Lock()
	same := w.written == key
	w.mu.Unlock()
	if same {
		return
	}
	snap.Written = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(snap)
	if err != nil || os.MkdirAll(w.dir, 0o700) != nil {
		return
	}
	// A temporary file of its own: two servers on one base (two sessions) must
	// not write into the same one.
	tmp, err := os.CreateTemp(w.dir, name+".*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(w.dir, name)) != nil {
		os.Remove(tmp.Name())
		return
	}
	w.mu.Lock()
	w.written = key
	w.mu.Unlock()
}
