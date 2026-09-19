package index

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestParsePoolPacksBlob: пул разбора отдаёт образ файла уже захэшированным
// и сжатым (шаг 1 issue #3), и хэш записи корпуса совпадает с хэшем образа:
// fingerprint и адресация blob обязаны ссылаться на одно число. Писатель
// кладёт такой образ без повторного SHA и deflate, а читатель получает
// байт-в-байт исходный файл.
func TestParsePoolPacksBlob(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"CommonModules/Утилиты/Ext/Module.bsl": []byte("Функция Помощь() Экспорт\n\tВозврат 1;\nКонецФункции\n"),
		"Пусто.bsl": {},
	}
	var tasks []parseTask
	for rel, data := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, parseTask{relPath: rel, absPath: abs})
	}

	results, err := runParsePool(context.Background(), 2, tasks)
	if err != nil {
		t.Fatalf("runParsePool: %v", err)
	}
	if len(results) != len(files) {
		t.Fatalf("результатов %d, ожидалось %d", len(results), len(files))
	}

	st := openTestStore(t)
	if err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		for _, r := range results {
			data := files[r.rec.relPath]
			want := store.HashContent(data)
			if r.rec.contentHash != want {
				t.Errorf("%s: contentHash записи %q, ожидался %q", r.rec.relPath, r.rec.contentHash, want)
			}
			if r.blob.Hash() != want {
				t.Errorf("%s: хэш образа %q, ожидался %q", r.rec.relPath, r.blob.Hash(), want)
			}
			h, err := tx.PutPreparedBlob(r.blob)
			if err != nil {
				return err
			}
			got, err := tx.Blob(h)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, data) {
				t.Errorf("%s: образ из store не совпал с файлом", r.rec.relPath)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("запись образов: %v", err)
	}
}
