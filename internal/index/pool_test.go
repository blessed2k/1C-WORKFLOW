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
// и сжатым (шаг 1 issue #3), прямо писателю, и хэш записи корпуса совпадает с хэшем образа:
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

	// Образы уходят писателю прямо из пула: store принимает их в той же
	// транзакции, куда потом лягут записи файлов.
	st := openTestStore(t)
	if err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		var hashes []string
		results, err := runParsePool(context.Background(), 2, tasks, func(b store.PreparedBlob) error {
			h, err := tx.PutPreparedBlob(b)
			hashes = append(hashes, h)
			return err
		})
		if err != nil {
			return err
		}
		if len(results) != len(files) || len(hashes) != len(files) {
			t.Fatalf("результатов %d, образов %d, ожидалось %d", len(results), len(hashes), len(files))
		}
		for _, r := range results {
			if r.blob.Hash() != "" {
				t.Errorf("%s: образ остался в результате после приёмника", r.rec.relPath)
			}
			data := files[r.rec.relPath]
			want := store.HashContent(data)
			if r.rec.contentHash != want {
				t.Errorf("%s: contentHash записи %q, ожидался %q", r.rec.relPath, r.rec.contentHash, want)
			}
			got, err := tx.Blob(want)
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

	// Без приёмника образ приезжает в результате, с тем же хэшем.
	results, err := runParsePool(context.Background(), 2, tasks, nil)
	if err != nil {
		t.Fatalf("runParsePool без приёмника: %v", err)
	}
	for _, r := range results {
		if r.blob.Hash() != r.rec.contentHash {
			t.Errorf("%s: хэш образа %q, записи %q", r.rec.relPath, r.blob.Hash(), r.rec.contentHash)
		}
	}
}
