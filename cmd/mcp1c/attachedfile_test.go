package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// fileLiveSource is a live source that also reads attached files, like HTTPSource.
type fileLiveSource struct {
	fakeLiveSource
	file    *source.AttachedFile
	err     error
	gotType string
	gotRef  string
}

func (f *fileLiveSource) AttachedFile(_ context.Context, objectType, ref string) (*source.AttachedFile, error) {
	f.gotType, f.gotRef = objectType, ref
	return f.file, f.err
}

// callAttachedFile calls get_attached_file on a server that has only that tool.
func callAttachedFile(t *testing.T, lp liveProvider, files *fileStore, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerAttachedFile(srv, lp, files)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_attached_file", Arguments: args})
	if err != nil {
		t.Fatalf("call get_attached_file: %v", err)
	}
	return res
}

func TestGetAttachedFileSavesTheFile(t *testing.T) {
	data := []byte("%PDF-1.4 счёт")
	src := &fileLiveSource{file: &source.AttachedFile{Name: "Счёт 17.pdf", ContentType: "application/pdf", Data: data}}
	lp := &stubProvider{sources: map[string]source.LiveSource{"k-integra": src}, active: "k-integra", order: []string{"k-integra"}}
	files := &fileStore{}
	t.Cleanup(func() { _ = files.Close() })

	res := callAttachedFile(t, lp, files, map[string]any{
		"type": "Справочник.СчетНаОплатуПоставщикаПрисоединенныеФайлы",
		"ref":  "0f7b8c1e-1111-2222-3333-444455556666",
	})
	if res.IsError {
		t.Fatalf("get_attached_file failed: %s", contentText(res))
	}
	var out attachedFileOutput
	if err := json.Unmarshal([]byte(contentText(res)), &out); err != nil {
		t.Fatalf("decode answer: %v; %s", err, contentText(res))
	}
	sum := sha256.Sum256(data)
	if out.Base != "k-integra" || out.Size != len(data) || out.SHA256 != hex.EncodeToString(sum[:]) || out.Name != "Счёт 17.pdf" {
		t.Errorf("answer = %+v", out)
	}
	if src.gotType != "Справочник.СчетНаОплатуПоставщикаПрисоединенныеФайлы" || src.gotRef != "0f7b8c1e-1111-2222-3333-444455556666" {
		t.Errorf("source asked for %q / %q", src.gotType, src.gotRef)
	}
	saved, err := os.ReadFile(out.Path)
	if err != nil || !bytes.Equal(saved, data) {
		t.Fatalf("saved file %s: %v, content %q", out.Path, err, saved)
	}
	if !strings.HasPrefix(filepath.Base(out.Path), "0f7b8c1e-1111-2222-3333-444455556666_") {
		t.Errorf("saved name %q must start with the reference", filepath.Base(out.Path))
	}

	dir := filepath.Dir(out.Path)
	if err := files.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory %s outlived Close: %v", dir, err)
	}
}

func TestGetAttachedFileNeedsAFileReader(t *testing.T) {
	lp := newStubProvider("trade", "trade") // namedLiveSource cannot read files
	res := callAttachedFile(t, lp, &fileStore{}, map[string]any{"type": "Справочник.Файлы", "ref": "x"})
	if !res.IsError || !strings.Contains(contentText(res), "нужен HTTP-коннектор") {
		t.Errorf("want the HTTP-connector error, got %s", contentText(res))
	}
}

func TestGetAttachedFileNamesTheBaseInAnError(t *testing.T) {
	src := &fileLiveSource{err: &source.ConnectorError{Path: "/file/x/y", Status: 400, Code: "not_a_file", Message: "не файл"}}
	lp := &stubProvider{sources: map[string]source.LiveSource{"k-integra": src}, active: "k-integra", order: []string{"k-integra"}}
	res := callAttachedFile(t, lp, &fileStore{}, map[string]any{"type": "Документ.Счет", "ref": "y"})
	if !res.IsError || !strings.Contains(contentText(res), "k-integra") || !strings.Contains(contentText(res), "not_a_file") {
		t.Errorf("want an error naming the base and the code, got %s", contentText(res))
	}
}

func TestSavedFileName(t *testing.T) {
	uuid := "0f7b8c1e-1111-2222-3333-444455556666"
	cases := []struct{ ref, name, want string }{
		{"abc", "Счёт 17.pdf", "abc_Счёт 17.pdf"},
		{"abc", `a<b>:c"d/e\f|g?h*.pdf`, "abc_a_b__c_d_e_f_g_h_.pdf"},
		{"abc", " .. ", "abc_file"},
		{"abc", "", "abc_file"},
		{"", "x.pdf", "x.pdf"},
		{uuid + "-extra", "x.pdf", uuid + "_x.pdf"},
		// Two bytes a letter: 150 letters are 300 bytes, cut to 196 plus the extension.
		{"abc", strings.Repeat("я", 150) + ".pdf", "abc_" + strings.Repeat("я", 98) + ".pdf"},
		// Three bytes a letter: the cut must not split one.
		{"abc", strings.Repeat("中", 100) + ".pdf", "abc_" + strings.Repeat("中", 65) + ".pdf"},
	}
	for _, tc := range cases {
		got := savedFileName(tc.ref, tc.name)
		if got != tc.want {
			t.Errorf("savedFileName(%q, %q) = %q, want %q", tc.ref, tc.name, got, tc.want)
		}
		if len(got) > 255 {
			t.Errorf("savedFileName(%q, ...) is %d bytes, over the 255-byte name limit", tc.ref, len(got))
		}
	}
}

func TestFileStoreCloseWithoutDownloads(t *testing.T) {
	var nilStore *fileStore
	if err := nilStore.Close(); err != nil {
		t.Errorf("nil store: %v", err)
	}
	if err := (&fileStore{}).Close(); err != nil {
		t.Errorf("empty store: %v", err)
	}
}

// A handler still running when the server stops must not make a directory nobody removes.
func TestFileStoreRefusesSaveAfterClose(t *testing.T) {
	files := &fileStore{tempRoot: t.TempDir()}
	if err := files.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if path, err := files.save("abc", "x.pdf", []byte("x")); err == nil {
		t.Errorf("save after Close wrote %s", path)
	}
}

// A process killed without Close leaves its directory; the next one sweeps old ones only.
func TestFileStoreSweepsStaleDirectories(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, filesDirPattern+"old")
	young := filepath.Join(root, filesDirPattern+"young")
	foreign := filepath.Join(root, "other-old")
	for _, dir := range []string{old, young, foreign} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, dir := range []string{old, foreign} {
		if err := os.Chtimes(dir, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	files := &fileStore{tempRoot: root, now: func() time.Time { return now }}
	t.Cleanup(func() { _ = files.Close() })
	if _, err := files.save("abc", "x.pdf", []byte("x")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("stale directory %s survived: %v", old, err)
	}
	for _, dir := range []string{young, foreign} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s must stay: %v", dir, err)
		}
	}
}
