package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Один writer на logical project (ADR-2 §7): параллельные записи сериализуются,
// а не идут внахлёст. Проверяется наблюдением, а не устройством: внутри
// транзакции счётчик одновременных писателей обязан быть равен единице.
func TestWritesAreSerialized(t *testing.T) {
	s := openTestStore(t, Options{})
	const writers = 8
	var inside, maxInside atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.Write(context.Background(), func(tx *WriteTx) error {
				n := inside.Add(1)
				defer inside.Add(-1)
				for {
					m := maxInside.Load()
					if n <= m || maxInside.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				return tx.SetMeta("writer", itoa(i))
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("запись: %v", err)
		}
	}
	if got := maxInside.Load(); got != 1 {
		t.Fatalf("одновременных писателей %d: транзакции не сериализованы", got)
	}
	st, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Каждая write-транзакция публикует своё поколение: 1 (пустая эпоха) + 8.
	if st.GenerationNumber != 1+writers {
		t.Errorf("поколение %d, ожидалось %d", st.GenerationNumber, 1+writers)
	}
}

// Ожидание очереди писателя прерывается контекстом: отменённый вызов не должен
// стоять в очереди за чужой длинной записью.
func TestWriteWaitIsContextAware(t *testing.T) {
	s := openTestStore(t, Options{})
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		s.Write(context.Background(), func(tx *WriteTx) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := s.Write(ctx, func(tx *WriteTx) error { return nil })
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидание очереди писателя вернуло %v, ожидалось истечение контекста", err)
	}
}

// Read-транзакция читателя — это и есть его снапшот (18.1): параллельный commit
// писателя внутри уже открытой транзакции читателю не виден, а следующий вызов
// его видит.
func TestReadTransactionIsSnapshot(t *testing.T) {
	s, _ := seeded(t)
	ctx := context.Background()
	before := countRows(t, s, "component", "")

	writeDone := make(chan error, 1)
	err := s.Read(ctx, func(tx *ReadTx) error {
		// Снапшот фиксируется первым чтением: BEGIN deferred.
		n, err := tx.c.queryInt(tx.ctx, `SELECT COUNT(*) FROM component`)
		if err != nil {
			return err
		}
		if n != before {
			t.Fatalf("до записи компонентов %d, ожидалось %d", n, before)
		}
		go func() {
			writeDone <- s.Write(context.Background(), func(w *WriteTx) error {
				return w.UpsertComponent(Component{ID: "ext", Kind: "extension", Root: "e"})
			})
		}()
		if err := <-writeDone; err != nil {
			return err
		}
		after, err := tx.c.queryInt(tx.ctx, `SELECT COUNT(*) FROM component`)
		if err != nil {
			return err
		}
		if after != before {
			t.Errorf("читатель увидел чужой commit внутри своей транзакции: %d вместо %d", after, before)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if n := countRows(t, s, "component", ""); n != before+1 {
		t.Errorf("следующий вызов не увидел опубликованный компонент: %d", n)
	}
}

// Ошибка внутри write-транзакции откатывает её целиком: COMMIT — единственная
// точка публикации, полузаписанных состояний не существует.
func TestWriteRollsBackOnError(t *testing.T) {
	s, f := seeded(t)
	ctx := context.Background()
	genBefore := countRows(t, s, "meta", "key='current_generation' AND value='2'")
	if genBefore != 1 {
		t.Fatalf("подготовка: поколение не 2")
	}
	boom := errors.New("сбой парсера посреди пакета")
	err := s.Write(ctx, func(tx *WriteTx) error {
		if _, err := tx.InsertSymbol(Symbol{
			IdentityKey: "symbol:cfg:CommonModules/X/в", ComponentID: fxComponent,
			UID: "uid-в", ModuleID: f.moduleID, OriginFileID: f.fileModuleBSL,
			Kind: "procedure", NameNorm: "в", NameDisplay: "В",
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Write вернул %v, ожидалась исходная ошибка", err)
	}
	if n := countRows(t, s, "symbol", "name_norm='в'"); n != 0 {
		t.Error("незакоммиченный символ выжил")
	}
	if n := countRows(t, s, "meta", "key='current_generation' AND value='2'"); n != 1 {
		t.Error("поколение изменилось откатившейся транзакцией")
	}
	assertValid(t, s, "после отката")
}

// Транзакция закрывается по выходу из Write и Read: сохранённый tx не должен
// работать со снапшота, которого уже нет.
func TestTxIsUnusableAfterCallback(t *testing.T) {
	s := openTestStore(t, Options{})
	var escaped *ReadTx
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		escaped = tx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.GenerationNumber(); !errors.Is(err, ErrTxDone) {
		t.Fatalf("завершённая транзакция ответила %v, ожидалась ErrTxDone", err)
	}
}
