package store

import (
	"bytes"
	"context"
	"testing"
)

// Шаг 1 issue #3: хэш и сжатие считаются вне писателя (в пуле разбора), а
// писатель принимает готовый образ. Контракт тот же, что у PutBlob: тот же
// хэш, тот же распакованный образ на чтении, дедупликация по хэшу.
func TestPreparedBlobRoundTripAndDedup(t *testing.T) {
	s := openTestStore(t, Options{})
	ctx := context.Background()
	body := []byte("Функция Общая() Экспорт\n\tВозврат 1;\nКонецФункции\n")

	b, err := PrepareBlob(body)
	if err != nil {
		t.Fatalf("PrepareBlob: %v", err)
	}
	if b.Hash() != HashContent(body) {
		t.Fatalf("хэш подготовленного образа %q, ожидался %q", b.Hash(), HashContent(body))
	}
	if b.Size() != int64(len(body)) {
		t.Fatalf("размер %d, ожидался %d", b.Size(), len(body))
	}
	if err := s.Write(ctx, func(tx *WriteTx) error {
		h1, err := tx.PutPreparedBlob(b)
		if err != nil {
			return err
		}
		// Тот же образ через старый путь: одна строка blob, не две.
		h2, err := tx.PutBlob(body)
		if err != nil {
			return err
		}
		if h1 != b.Hash() || h2 != b.Hash() {
			t.Fatalf("хэши разошлись: %q, %q, ожидался %q", h1, h2, b.Hash())
		}
		got, err := tx.Blob(h1)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("распакованный образ не совпал с исходным")
		}
		return nil
	}); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if n := countRows(t, s, "blob", ""); n != 1 {
		t.Fatalf("blob-строк %d, ожидалась 1", n)
	}
}

// Нулевой PreparedBlob (образ не подготовлен) писатель отвергает: иначе в
// индекс молча лёг бы пустой файл под чужим путём.
func TestPreparedBlobZeroValueRejected(t *testing.T) {
	s := openTestStore(t, Options{})
	err := s.Write(context.Background(), func(tx *WriteTx) error {
		_, err := tx.PutPreparedBlob(PreparedBlob{})
		return err
	})
	if err == nil {
		t.Fatal("нулевой PreparedBlob принят писателем")
	}
}
