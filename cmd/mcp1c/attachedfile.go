package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// attachedFileReader is the slice of a live source get_attached_file needs. Only
// the HTTP connector can read a file, so the tool asks for it by interface and
// LiveSource stays as it is.
type attachedFileReader interface {
	AttachedFile(ctx context.Context, objectType, ref string) (*source.AttachedFile, error)
}

type attachedFileInput struct {
	Type string `json:"type" jsonschema:"full name of the attached-file catalog as execute_query refs=true returns it, e.g. Справочник.СчетНаОплатуПоставщикаПрисоединенныеФайлы or Справочник.Файлы"`
	Ref  string `json:"ref" jsonschema:"reference UUID of the file, the ref of an execute_query refs=true row"`
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type attachedFileOutput struct {
	Base        string `json:"base" jsonschema:"live base the file came from"`
	Path        string `json:"path" jsonschema:"local file the content was saved to; open it with your file reading tool"`
	Name        string `json:"name,omitempty" jsonschema:"file name with extension as the base stores it"`
	ContentType string `json:"contentType,omitempty"`
	Size        int    `json:"size" jsonschema:"bytes"`
	SHA256      string `json:"sha256" jsonschema:"hex digest of the content"`
}

// filesDirPattern names the temporary directory of one server process.
const filesDirPattern = "mcp1c-files-"

// staleFilesAge is how old a directory of another process must be to count as
// left behind by a crash.
const staleFilesAge = 24 * time.Hour

// fileStore keeps the files get_attached_file saved during this server process in
// one temporary directory, made on the first download and removed on Close, so
// invoices and scans do not outlive the session on disk. A process killed without
// Close (the client ended it, a fatal error) leaves its directory behind; the next
// server sweeps such directories older than staleFilesAge on its first download.
type fileStore struct {
	mu     sync.Mutex
	dir    string
	closed bool
	// tempRoot and now are seams for tests; empty and nil mean os.TempDir and time.Now.
	tempRoot string
	now      func() time.Time
}

func (f *fileStore) save(ref, name string, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return "", fmt.Errorf("сервер останавливается, файл не сохранён")
	}
	if f.dir == "" {
		f.sweepStale()
		dir, err := os.MkdirTemp(f.tempRoot, filesDirPattern)
		if err != nil {
			return "", fmt.Errorf("временный каталог для файлов: %w", err)
		}
		f.dir = dir
	}
	path := filepath.Join(f.dir, savedFileName(ref, name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("сохранение файла: %w", err)
	}
	return path, nil
}

// sweepStale removes the file directories of earlier processes that ended without
// Close. Only old ones: a younger directory may belong to a server still running.
// A directory that cannot be removed (another user's) is left as it is.
func (f *fileStore) sweepStale() {
	root := f.tempRoot
	if root == "" {
		root = os.TempDir()
	}
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), filesDirPattern) {
			continue
		}
		info, err := e.Info()
		if err != nil || now().Sub(info.ModTime()) < staleFilesAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// Close removes the directory with every saved file; later saves are refused, so
// a handler still running cannot make a directory nobody removes. Safe on a nil
// store and on a store that never saved anything.
func (f *fileStore) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.dir == "" {
		return nil
	}
	err := os.RemoveAll(f.dir)
	f.dir = ""
	return err
}

// maxSavedNameBytes caps the name taken from the base, in UTF-8 bytes: Linux and
// macOS allow 255 bytes per name, and the reference prefix takes 37 more.
const maxSavedNameBytes = 200

// maxSavedRefBytes is the length of a reference UUID.
const maxSavedRefBytes = 36

// savedFileName is the name a downloaded file gets on disk: the reference first,
// so two files with one name do not overwrite each other, then the name from the
// base with the characters no file system accepts replaced.
func savedFileName(ref, name string) string {
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
				return '_'
			}
			return r
		}, s)
		return strings.Trim(s, " .")
	}
	name = clean(name)
	if name == "" {
		name = "file"
	}
	if len(name) > maxSavedNameBytes {
		ext := filepath.Ext(name)
		if len(ext) >= maxSavedNameBytes {
			ext = ""
		}
		name = cutBytes(strings.TrimSuffix(name, ext), maxSavedNameBytes-len(ext)) + ext
	}
	if ref = clean(ref); ref == "" {
		return name
	}
	return cutBytes(ref, maxSavedRefBytes) + "_" + name
}

// cutBytes shortens s to at most n bytes without splitting a character: it cuts at
// the last rune start that is not past n.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := 0
	for i := range s {
		if i > n {
			break
		}
		cut = i
	}
	return s[:cut]
}

// registerAttachedFile wires get_attached_file. files is where the downloads go;
// the caller closes it when the server stops.
func registerAttachedFile(server *mcp.Server, lp liveProvider, files *fileStore) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_attached_file",
		Description: "Downloads a file attached to a document or catalog item (a PDF or scan of a supplier invoice, a contract) from the live base, saves it to a local temporary file and returns its path, name, size and sha256; open the path with your file reading tool to compare the document with the data. Find the file first: execute_query refs=true on the attached-file catalog, e.g. ВЫБРАТЬ Ссылка, Наименование, Расширение, Размер ИЗ Справочник.СчетНаОплатуПоставщикаПрисоединенныеФайлы ГДЕ ВладелецФайла = &Счет, then pass the type and ref of the row. Read-only, under the rights of the connector user; files up to 20 MB; the saved files are removed when the server stops. Pass base=<name> to target one base for this call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in attachedFileInput) (*mcp.CallToolResult, attachedFileOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, attachedFileOutput{}, err
		}
		reader, ok := live.(attachedFileReader)
		if !ok {
			return nil, attachedFileOutput{}, fmt.Errorf("base %s: источник не умеет читать файлы, нужен HTTP-коннектор", name)
		}
		file, err := reader.AttachedFile(ctx, in.Type, in.Ref)
		if err != nil {
			return nil, attachedFileOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		path, err := files.save(in.Ref, file.Name, file.Data)
		if err != nil {
			return nil, attachedFileOutput{}, err
		}
		sum := sha256.Sum256(file.Data)
		return nil, attachedFileOutput{
			Base:        name,
			Path:        path,
			Name:        file.Name,
			ContentType: file.ContentType,
			Size:        len(file.Data),
			SHA256:      hex.EncodeToString(sum[:]),
		}, nil
	})
}
