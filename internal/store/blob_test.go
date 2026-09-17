package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Контракт blob (18.2): content-addressed образ файла, дедупликация по хэшу,
// потеря последней ссылки ставит unreferenced_since, TTL переживает ближайший
// инкремент, повторное появление хэша снимает метку, а вычищенный blob честно
// не находится вместо того, чтобы отдать другой текст.
func TestBlobDedupTTLAndUnmark(t *testing.T) {
	clock := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	ttl := 30 * time.Minute
	s := openTestStore(t, Options{BlobTTL: ttl, Now: func() time.Time { return clock }})
	ctx := context.Background()

	const body = "Функция Общая() Экспорт КонецФункции"
	hash := HashContent([]byte(body))
	var fileA, fileB int64
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if err := tx.UpsertComponent(Component{ID: fxComponent, Kind: "configuration", Root: "."}); err != nil {
			return err
		}
		// Два файла с ОДИНАКОВЫМ содержимым: в 1С это норма (одинаковые модули
		// объектов, пустые Form.xml). Данные обязаны лечь в базу один раз.
		h1, err := tx.PutBlob([]byte(body))
		if err != nil {
			return err
		}
		h2, err := tx.PutBlob([]byte(body))
		if err != nil {
			return err
		}
		if h1 != hash || h2 != hash {
			t.Fatalf("хэши разошлись: %q, %q, ожидался %q", h1, h2, hash)
		}
		if fileA, err = tx.InsertSourceFile(SourceFile{ComponentID: fxComponent,
			RelPath: "A.bsl", Size: int64(len(body)), MtimeNS: 1, ContentHash: hash, ParserVersion: 1}); err != nil {
			return err
		}
		fileB, err = tx.InsertSourceFile(SourceFile{ComponentID: fxComponent,
			RelPath: "B.bsl", Size: int64(len(body)), MtimeNS: 1, ContentHash: hash, ParserVersion: 1})
		return err
	}); err != nil {
		t.Fatalf("наполнение: %v", err)
	}
	if n := countRows(t, s, "blob", ""); n != 1 {
		t.Fatalf("blob-строк %d, ожидалась 1: дедупликации по хэшу нет", n)
	}
	assertValid(t, s, "после дедупликации")

	// Пока есть хоть одна ссылка, метка не ставится.
	if err := s.Write(ctx, func(tx *WriteTx) error { return tx.DeleteSourceFiles(fileA) }); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "blob", "unreferenced_since IS NOT NULL"); n != 0 {
		t.Error("blob помечен неиспользуемым, хотя на него ещё ссылается файл")
	}

	// Последняя ссылка ушла: метка ставится временем текущей транзакции.
	if err := s.Write(ctx, func(tx *WriteTx) error { return tx.DeleteSourceFiles(fileB) }); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "blob", "unreferenced_since=?", clock.Unix()); n != 1 {
		t.Fatalf("unreferenced_since не проставлен временем транзакции")
	}
	assertValid(t, s, "после потери последней ссылки")

	// Внутри TTL образ ещё доступен: resource link переживает ближайший
	// инкремент через TTL, а не через MVCC.
	clock = clock.Add(ttl - time.Minute)
	if err := s.Write(ctx, func(tx *WriteTx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var err error
		got, err = tx.Blob(hash)
		return err
	}); err != nil {
		t.Fatalf("blob исчез раньше TTL: %v", err)
	}
	if string(got) != body {
		t.Errorf("blob вернул не тот текст: %q", string(got))
	}

	// Тот же хэш вернулся — метка обязана сняться.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, err := tx.PutBlob([]byte(body)); err != nil {
			return err
		}
		_, err := tx.InsertSourceFile(SourceFile{ComponentID: fxComponent, RelPath: "C.bsl",
			Size: int64(len(body)), MtimeNS: 2, ContentHash: hash, ParserVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "blob", "unreferenced_since IS NOT NULL"); n != 0 {
		t.Error("метка не снята при повторном появлении хэша")
	}
	assertValid(t, s, "после возврата хэша")

	// Ссылка снова потеряна, TTL истёк — GC удаляет образ, и ссылка на фрагмент
	// честно протухает.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		id, ok, err := tx.SourceFileID(fxComponent, "C.bsl")
		if err != nil || !ok {
			t.Fatalf("файл C.bsl не найден: ok=%v err=%v", ok, err)
		}
		return tx.DeleteSourceFiles(id)
	}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(ttl + time.Minute)
	if err := s.Write(ctx, func(tx *WriteTx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "blob", ""); n != 0 {
		t.Errorf("blob пережил TTL: осталось %d строк", n)
	}
	err := s.Read(ctx, func(tx *ReadTx) error {
		_, err := tx.Blob(hash)
		return err
	})
	if err == nil {
		t.Fatal("вычищенный blob прочитался")
	}
	if !strings.Contains(err.Error(), hash) {
		t.Errorf("ошибка не называет запрошенный хэш: %v", err)
	}
}

// Жизнью blob управляет TTL-GC, а не каскад: удаление образа, на который есть
// живая ссылка, обязано быть отвергнуто (RESTRICT, раздел 15).
func TestBlobDeleteRestrictedWhileReferenced(t *testing.T) {
	s, f := seeded(t)
	err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.c.exec(tx.ctx, `DELETE FROM blob WHERE content_hash=?`, f.hashObjectXML)
	})
	if err == nil {
		t.Fatal("blob с живой ссылкой удалился: RESTRICT не работает")
	}
	assertValid(t, s, "после попытки удалить используемый blob")
}
