package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testPool открывает отдельный пул поверх файла эпохи тестового хранилища.
func testPool(t *testing.T, size int) *readerPool {
	t.Helper()
	s := openTestStore(t, Options{})
	p, err := newReaderPool(context.Background(), s.currentPath(), size, 5000)
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// Пул не открывает больше соединений, чем задано, а лишние запросы ждут.
// Без границы каждый параллельный запрос стоил бы своего cache_size и держал
// снапшот, продлевая WAL (ADR-2 §7).
func TestReaderPoolIsBounded(t *testing.T) {
	const size = 2
	p := testPool(t, size)

	held := make([]*conn, 0, size)
	for i := 0; i < size; i++ {
		c, err := p.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := p.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("аренда сверх размера пула вернула %v, ожидалось истечение контекста", err)
	}
	if got := p.Stats().CanceledWait; got != 1 {
		t.Errorf("прерванных ожиданий %d, ожидалось 1: исчерпание пула обязано быть диагностируемо", got)
	}

	p.release(held[0])
	c, err := p.acquire(context.Background())
	if err != nil {
		t.Fatalf("после возврата соединения аренда не прошла: %v", err)
	}
	p.release(c)
	for _, h := range held[1:] {
		p.release(h)
	}
	if st := p.Stats(); st.Size != size {
		t.Errorf("размер пула в диагностике %d, ожидался %d", st.Size, size)
	}
}

// Ожидание свободного соединения прерывается вместе с контекстом: отменённый
// вызов не должен занимать очередь до конца чужого длинного скана.
func TestReaderPoolWaitIsContextAware(t *testing.T) {
	p := testPool(t, 1)
	busy, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var acqErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, acqErr = p.acquire(ctx)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()
	if !errors.Is(acqErr, context.Canceled) {
		t.Fatalf("ожидание вернуло %v, ожидалась отмена контекста", acqErr)
	}
	p.release(busy)
}

// Закрытие пула обязано БУДИТЬ ждущих: иначе остановка проекта оставила бы их
// висеть до отмены контекста, а с context.Background() — навсегда.
func TestReaderPoolCloseWakesWaiters(t *testing.T) {
	p := testPool(t, 1)
	busy, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Внутри теста соединение намеренно НЕ возвращается: пул обязан быть
	// исчерпан, иначе ждущему не на чем ждать. Но отдать его в конце всё же
	// нужно — release закрывает соединение уже закрытого пула, а незакрытый
	// файл БД на Windows не даёт удалить каталог t.TempDir() (уборка теста
	// падала после всех зелёных проверок).
	t.Cleanup(func() { p.release(busy) })

	var wg sync.WaitGroup
	var acqErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, acqErr = p.acquire(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if !errors.Is(acqErr, ErrReaderPoolClosed) {
		t.Fatalf("ждущий получил %v, ожидалось закрытие пула", acqErr)
	}
}

// Закрытый пул не выдаёт соединений: иначе после остановки проекта запросы
// работали бы с закрытой БД.
func TestReaderPoolClosed(t *testing.T) {
	p := testPool(t, 1)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.acquire(context.Background()); !errors.Is(err, ErrReaderPoolClosed) {
		t.Fatalf("закрытый пул вернул %v", err)
	}
}

// Выданное соединение обязано быть рабочим даже под гонкой с закрытием пула:
// проверяется запросом, а не утверждением.
func TestReaderPoolNeverHandsOutClosedConn(t *testing.T) {
	p := testPool(t, 4)
	var wg sync.WaitGroup
	var handedOut, closedErr atomic.Int64
	bad := make(chan error, 32)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				c, err := p.acquire(context.Background())
				if errors.Is(err, ErrReaderPoolClosed) {
					closedErr.Add(1)
					return
				}
				if err != nil {
					bad <- err
					return
				}
				if _, qErr := c.queryInt(context.Background(), `SELECT 1`); qErr != nil {
					bad <- qErr
					p.release(c)
					return
				}
				handedOut.Add(1)
				p.release(c)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(bad)
	for err := range bad {
		t.Errorf("выдано нерабочее соединение: %v", err)
	}
	if handedOut.Load() == 0 {
		t.Fatal("ни одной успешной аренды: тест не проверил выдачу")
	}
	if closedErr.Load() == 0 {
		t.Fatal("ни одна горутина не увидела закрытия: гонка не воспроизведена")
	}
}

// Close не закрывает соединение, которым прямо сейчас пользуются: это уронило
// бы выполняющийся чужой запрос вместо того, чтобы дать ему завершиться.
func TestReaderPoolCloseDoesNotKillLeasedConn(t *testing.T) {
	p := testPool(t, 2)
	leased, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := leased.queryInt(context.Background(), `SELECT 1`); err != nil {
		t.Fatalf("Close закрыл арендованное соединение: %v", err)
	}
	p.release(leased)
	if _, err := p.acquire(context.Background()); !errors.Is(err, ErrReaderPoolClosed) {
		t.Errorf("после возврата в закрытый пул аренда вернула %v", err)
	}
}

// Отменённый запрос портит соединение: возвращать такое в пул нельзя, иначе
// следующий арендатор получит чужую незакрытую транзакцию. Испорченное
// уничтожается и заменяется новым, и новое обязано работать — иначе одна отмена
// навсегда сужала бы пропускную способность проекта.
func TestReaderPoolDiscardReplacesConnection(t *testing.T) {
	const size = 2
	p := testPool(t, size)
	c, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	const heavy = `WITH RECURSIVE r(x) AS (
		SELECT 1 UNION ALL SELECT x+1 FROM r WHERE x < 100000000)
		SELECT COUNT(*) FROM r`
	errc := make(chan error, 1)
	go func() {
		_, qErr := c.queryInt(ctx, heavy)
		errc <- qErr
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case qErr := <-errc:
		if qErr == nil {
			t.Fatal("долгий запрос завершился успешно: отмена не сработала, контракт не проверен")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("запрос не прервался отменой контекста")
	}

	if err := p.discard(context.Background(), c); err != nil {
		t.Fatalf("замена уничтоженного соединения: %v", err)
	}
	if st := p.Stats(); st.Discarded != 1 || st.ReplaceFailed != 0 || st.Size != size {
		t.Fatalf("диагностика после discard: %+v", st)
	}
	var held []*conn
	for i := 0; i < size; i++ {
		got, err := p.acquire(context.Background())
		if err != nil {
			t.Fatalf("аренда %d после замены: %v", i+1, err)
		}
		if _, err := got.queryInt(context.Background(), `SELECT 1`); err != nil {
			t.Fatalf("соединение %d после замены не отвечает: %v", i+1, err)
		}
		held = append(held, got)
	}
	for _, h := range held {
		p.release(h)
	}
}

// Отменённый MCP-вызов не должен возвращать в пул соединение с прерванным
// запросом: Read уничтожает такое соединение, а хранилище продолжает работать.
func TestReadWithCanceledContextDiscardsConnection(t *testing.T) {
	s := openTestStore(t, Options{ReaderPoolSize: 2})
	ctx, cancel := context.WithCancel(context.Background())
	err := s.Read(ctx, func(tx *ReadTx) error {
		cancel()
		_, err := tx.GenerationNumber()
		return err
	})
	if err == nil {
		t.Fatal("чтение с отменённым контекстом завершилось успешно")
	}
	if got := s.readers.Stats().Discarded; got != 1 {
		t.Errorf("уничтожено соединений %d, ожидалось 1", got)
	}
	// Пул сохранил размер и работает.
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		_, err := tx.GenerationNumber()
		return err
	}); err != nil {
		t.Fatalf("после отмены хранилище не читается: %v", err)
	}
	if got := s.readers.Stats().Size; got != 2 {
		t.Errorf("размер пула после замены %d, ожидался 2", got)
	}
}
