package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Пул читателей одного logical project (ADR-2 §7).
//
// Без границы каждый параллельный запрос открывал бы своё соединение, а замер
// фазы 0A показал, что соединение стоит своего cache_size и держит снапшот,
// продлевая WAL. Поэтому читатели берутся из пула фиксированного размера,
// соединения открываются один раз при старте и больше не открываются — кроме
// замены испорченного.

// DefaultReaderPoolSize — умолчание из ADR-2 §7: столько читателей было в
// конкурентном прогоне фазы 0A, и ни расширение, ни сужение пула замером не
// подтверждены.
const DefaultReaderPoolSize = 4

// ErrReaderPoolClosed — пул закрыт, новые аренды невозможны.
var ErrReaderPoolClosed = errors.New("пул читателей закрыт")

// ReaderPoolStats — диагностика исчерпания пула: молчаливое зависание на
// исчерпанном пуле неотличимо от медленного запроса, поэтому ожидания считаются.
type ReaderPoolStats struct {
	Size          int           `json:"size"`
	Acquired      int64         `json:"acquired"`
	WaitedCount   int64         `json:"waitedCount"`
	WaitedNanos   int64         `json:"waitedNanos"`
	CanceledWait  int64         `json:"canceledWait"`
	MaxWait       time.Duration `json:"maxWaitNanos"`
	Discarded     int64         `json:"discarded"`
	ReplaceFailed int64         `json:"replaceFailed"`
}

// readerPool — ограниченный набор read-соединений.
//
// Владение соединением определяется тем, КТО изъял его из канала free: изъявший
// обязан закрыть его, если пул уже закрыт. Поэтому закрытое соединение не может
// быть выдано: либо его забрал Close и закрыл сам, либо его забрал acquire — и
// тогда Close до него не доберётся.
type readerPool struct {
	free   chan *conn
	closed atomic.Bool
	// open воссоздаёт соединение взамен уничтоженного: пул обязан сохранять
	// свой размер, иначе одна отмена навсегда сужает пропускную способность.
	open func(ctx context.Context) (*conn, error)
	// done закрывается в Close и будит тех, кто ждёт свободное соединение.
	// Без него закрытие пула во время ожидания оставляло бы ждущего висеть до
	// отмены контекста, а с context.Background() — навсегда.
	done chan struct{}

	mu    sync.Mutex
	stats ReaderPoolStats
}

func newReaderPool(ctx context.Context, path string, size, busyTimeoutMillis int) (*readerPool, error) {
	if size <= 0 {
		size = DefaultReaderPoolSize
	}
	p := &readerPool{
		free: make(chan *conn, size),
		done: make(chan struct{}),
		open: func(ctx context.Context) (*conn, error) { return openConn(ctx, path, busyTimeoutMillis) },
	}
	p.stats.Size = size
	for i := 0; i < size; i++ {
		c, err := p.open(ctx)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("соединение читателя %d из %d: %w", i+1, size, err)
		}
		p.free <- c
	}
	return p, nil
}

// acquire выдаёт соединение, ожидая освобождения. Ожидание прерывается вместе с
// контекстом: отменённый MCP-вызов не должен занимать очередь до конца чужого
// длинного скана.
func (p *readerPool) acquire(ctx context.Context) (*conn, error) {
	if p.closed.Load() {
		return nil, ErrReaderPoolClosed
	}
	select {
	case c := <-p.free:
		return p.handOut(c, 0)
	default:
	}
	start := time.Now()
	select {
	case c := <-p.free:
		return p.handOut(c, time.Since(start))
	case <-p.done:
		p.note(time.Since(start), true)
		return nil, ErrReaderPoolClosed
	case <-ctx.Done():
		p.note(time.Since(start), true)
		return nil, fmt.Errorf("ожидание свободного читателя прервано (пул %d, ждали %s): %w",
			p.size(), time.Since(start).Round(time.Millisecond), ctx.Err())
	}
}

// tryAcquire отдаёт свободное соединение или сообщает, что свободных нет, НЕ
// ожидая. Нужен диагностике: она обязана отвечать именно тогда, когда пул
// исчерпан, а обычная аренда в этот момент как раз и встала бы в очередь.
func (p *readerPool) tryAcquire() (*conn, bool) {
	if p.closed.Load() {
		return nil, false
	}
	select {
	case c := <-p.free:
		got, err := p.handOut(c, 0)
		if err != nil {
			return nil, false
		}
		return got, true
	default:
		return nil, false
	}
}

// handOut отдаёт изъятое соединение вызывающему — либо, если пул уже закрыт,
// закрывает его сам.
func (p *readerPool) handOut(c *conn, wait time.Duration) (*conn, error) {
	if p.closed.Load() {
		c.Close()
		p.note(wait, true)
		return nil, ErrReaderPoolClosed
	}
	p.note(wait, false)
	return c, nil
}

// discard уничтожает соединение вместо возврата в пул и открывает новое.
// Вызывается там, где соединение могло остаться в неопределённом состоянии:
// прерванный контекстом запрос, незакрытая транзакция, ошибка уровня соединения.
// Вернуть такое в пул нельзя — следующий арендатор получил бы чужую открытую
// транзакцию. Ошибка замены не теряется: пул сужается, и это видно в Stats.
func (p *readerPool) discard(ctx context.Context, c *conn) error {
	if c == nil {
		return nil
	}
	c.Close()
	p.mu.Lock()
	p.stats.Discarded++
	p.mu.Unlock()
	if p.closed.Load() {
		return nil
	}
	fresh, err := p.open(ctx)
	if err != nil {
		p.mu.Lock()
		p.stats.ReplaceFailed++
		p.stats.Size--
		p.mu.Unlock()
		return fmt.Errorf("замена уничтоженного соединения не удалась, пул сузился: %w", err)
	}
	p.release(fresh)
	return nil
}

// release возвращает соединение в пул. Если пул закрыт, соединение закрывается
// здесь: Close до него уже не доберётся, а молча потерять его значит оставить
// открытым дескриптор до конца процесса.
func (p *readerPool) release(c *conn) {
	if c == nil {
		return
	}
	if p.closed.Load() {
		c.Close()
		return
	}
	p.free <- c
	// Пул мог закрыться, пока соединение возвращалось: тогда его нужно забрать
	// обратно и закрыть — дренаж в Close мог уже пройти мимо.
	if p.closed.Load() {
		select {
		case back := <-p.free:
			back.Close()
		default:
		}
	}
}

func (p *readerPool) note(wait time.Duration, canceled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if canceled {
		p.stats.CanceledWait++
		return
	}
	p.stats.Acquired++
	if wait > 0 {
		p.stats.WaitedCount++
		p.stats.WaitedNanos += wait.Nanoseconds()
		if wait > p.stats.MaxWait {
			p.stats.MaxWait = wait
		}
	}
}

func (p *readerPool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats.Size
}

// Stats — снимок диагностики.
func (p *readerPool) Stats() ReaderPoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// Close закрывает соединения пула. Арендованные не трогаются: закрыть
// соединение, которым прямо сейчас пользуются, значит уронить чужой запрос
// вместо того, чтобы дать ему завершиться. Их закроет release.
func (p *readerPool) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	// Разбудить ждущих ДО дренажа: иначе они будут ждать соединение, которое
	// уже никто не вернёт.
	close(p.done)
	var firstErr error
	for {
		select {
		case c := <-p.free:
			if err := c.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		default:
			return firstErr
		}
	}
}
