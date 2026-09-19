package store

import (
	"context"
	"errors"
	"os"
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
//
// Фоновая запись завершается до выхода из теста: так проверка ошибки
// удерживающей записи не зависит от того, успел ли t.Cleanup её закрыть
// (issue #12; Close под идущей записью с issue #13 отменяет её и ждёт отката).
func TestWriteWaitIsContextAware(t *testing.T) {
	s := openTestStore(t, Options{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	started := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- s.Write(context.Background(), func(tx *WriteTx) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	// Страховка от зависания: если ожидание очереди контекст не слушает, вызов
	// ниже встал бы навсегда за удерживающей записью. Через заведомо больший
	// срок, чем дедлайн, очередь отпускается, и тест краснеет на проверке
	// ошибки, а не на таймауте всего пакета.
	safety := time.AfterFunc(5*time.Second, releaseOnce)
	defer safety.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := s.Write(ctx, func(tx *WriteTx) error { return nil })
	// Вернуться обязано само ожидание, пока очередь ещё занята. Иначе
	// DeadlineExceeded пришёл бы уже от BEGIN на истёкшем контексте после
	// освобождения очереди, и проверка ошибки ниже этого не отличила бы.
	var waitedForRelease bool
	select {
	case <-release:
		waitedForRelease = true
	default:
	}
	releaseOnce()
	if hErr := <-holderDone; hErr != nil {
		t.Fatalf("удерживающая запись: %v", hErr)
	}
	if waitedForRelease {
		t.Fatalf("ожидание очереди писателя не прервалось контекстом: вызов дождался освобождения очереди (%v)", err)
	}
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

// openClosableStore открывает хранилище в своём каталоге и отдаёт каталог:
// тестам закрытия нужно переоткрыть его после Close и проверить, что запись
// либо зафиксирована целиком, либо откатилась.
func openClosableStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := Open(root, Options{ProjectID: "project", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

// reopenedMeta переоткрывает хранилище и читает ключ meta и номер поколения.
func reopenedMeta(t *testing.T, root, key string) (string, int64) {
	t.Helper()
	s, err := Open(root, Options{ProjectID: "project", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}
	defer s.Close()
	var v string
	var gen int64
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		var err error
		if v, err = tx.Meta(key); err != nil {
			return err
		}
		gen, err = tx.GenerationNumber()
		return err
	}); err != nil {
		t.Fatalf("чтение после переоткрытия: %v", err)
	}
	return v, gen
}

// Close во время идущей записи (issue #13): соединение писателя закрывается
// только после того, как транзакция зафиксирована или откатилась. Раньше Close
// рвал соединение под транзакцией, и её хвост (закрытие выражений, COMMIT)
// падал в sqlite на закрытом хэндле.
//
// Два случая: тело записи не слушает контекст (Close обязан дождаться его),
// и тело ждёт отмены (Close обязан её прислать, иначе ждал бы вечно).
func TestCloseDuringWrite(t *testing.T) {
	cases := []struct {
		name string
		// honorsCancel: тело записи выходит по отмене контекста транзакции.
		honorsCancel bool
	}{
		{name: "тело не слушает контекст", honorsCancel: false},
		{name: "тело выходит по отмене", honorsCancel: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, root := openClosableStore(t)
			release := make(chan struct{})
			releaseOnce := sync.OnceFunc(func() { close(release) })
			// Страховка от зависания теста целиком: краснеть обязана проверка
			// ниже, а не таймаут пакета.
			safety := time.AfterFunc(5*time.Second, releaseOnce)
			defer safety.Stop()

			started := make(chan struct{})
			var fnReturned atomic.Bool
			writeDone := make(chan error, 1)
			go func() {
				writeDone <- s.Write(context.Background(), func(tx *WriteTx) error {
					if err := tx.SetMeta("issue13", "записано"); err != nil {
						return err
					}
					close(started)
					if tc.honorsCancel {
						select {
						case <-tx.ctx.Done():
						case <-release:
						}
						fnReturned.Store(true)
						return tx.ctx.Err()
					}
					<-release
					fnReturned.Store(true)
					return nil
				})
			}()
			<-started

			closeDone := make(chan error, 1)
			go func() { closeDone <- s.Close() }()

			if !tc.honorsCancel {
				// Тело держит транзакцию: Close обязан ждать, а не рвать
				// соединение из-под неё.
				select {
				case <-closeDone:
					t.Fatal("Close вернулся, пока тело записи ещё держит транзакцию")
				case <-time.After(50 * time.Millisecond):
				}
				releaseOnce()
			}

			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("Close: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Close не вернулся: запись не отменена или не дождана")
			}
			if !fnReturned.Load() {
				t.Fatal("Close вернулся раньше, чем тело записи отработало")
			}
			wErr := <-writeDone
			if tc.honorsCancel && !errors.Is(wErr, ErrStoreClosed) {
				t.Fatalf("прерванная закрытием запись вернула %v, ожидалась ErrStoreClosed", wErr)
			}

			// Атомарность: либо запись зафиксирована целиком (и Write вернул
			// nil), либо откатилась и не оставила ни строки, ни поколения.
			v, gen := reopenedMeta(t, root, "issue13")
			switch {
			case wErr == nil && (v != "записано" || gen != 2):
				t.Fatalf("Write вернул nil, а после переоткрытия meta=%q, поколение %d", v, gen)
			case wErr != nil && (v != "" || gen != 1):
				t.Fatalf("Write вернул %v, а после переоткрытия meta=%q, поколение %d", wErr, v, gen)
			}
		})
	}
}

// Запись после Close и запись, стоявшая в очереди в момент Close, получают
// ErrStoreClosed, а не падают и не виснут (issue #13). Тело такой записи не
// вызывается.
func TestWriteAfterClose(t *testing.T) {
	t.Run("после закрытия", func(t *testing.T) {
		s, _ := openClosableStore(t)
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		called := false
		if err := s.Write(context.Background(), func(tx *WriteTx) error {
			called = true
			return nil
		}); !errors.Is(err, ErrStoreClosed) {
			t.Fatalf("Write после Close вернул %v, ожидалась ErrStoreClosed", err)
		}
		if err := s.Rebuild(context.Background(), func(tx *WriteTx) error {
			called = true
			return nil
		}); !errors.Is(err, ErrStoreClosed) {
			t.Fatalf("Rebuild после Close вернул %v, ожидалась ErrStoreClosed", err)
		}
		if called {
			t.Fatal("тело записи вызвано на закрытом хранилище")
		}
		if err := s.Close(); err != nil {
			t.Fatalf("повторный Close: %v", err)
		}
	})

	t.Run("в очереди за идущей записью", func(t *testing.T) {
		s, _ := openClosableStore(t)
		release := make(chan struct{})
		releaseOnce := sync.OnceFunc(func() { close(release) })
		safety := time.AfterFunc(5*time.Second, releaseOnce)
		defer safety.Stop()

		started := make(chan struct{})
		holderDone := make(chan error, 1)
		go func() {
			holderDone <- s.Write(context.Background(), func(tx *WriteTx) error {
				close(started)
				<-release
				return nil
			})
		}()
		<-started

		queuedDone := make(chan error, 1)
		var queuedCalled atomic.Bool
		go func() {
			queuedDone <- s.Write(context.Background(), func(tx *WriteTx) error {
				queuedCalled.Store(true)
				return nil
			})
		}()
		closeDone := make(chan error, 1)
		go func() { closeDone <- s.Close() }()

		// Стоящая в очереди запись отпускается самим закрытием, пока
		// удерживающая ещё идёт: ждать её конца незачем.
		select {
		case err := <-queuedDone:
			if !errors.Is(err, ErrStoreClosed) {
				t.Fatalf("запись из очереди вернула %v, ожидалась ErrStoreClosed", err)
			}
		case <-release:
			t.Fatal("запись из очереди не отпущена закрытием до конца удерживающей")
		}
		releaseOnce()
		if err := <-closeDone; err != nil {
			t.Fatalf("Close: %v", err)
		}
		<-holderDone
		if queuedCalled.Load() {
			t.Fatal("тело записи из очереди вызвано после Close")
		}
	})
}

// Читатель, чей запрос идёт в момент Close, ведёт себя так же, как писатель
// (issue #13): его контекст отменяется, соединение закрывается после конца
// транзакции, а сам Read возвращает ErrStoreClosed, не падая.
func TestCloseDuringRead(t *testing.T) {
	s, _ := openClosableStore(t)
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	safety := time.AfterFunc(5*time.Second, releaseOnce)
	defer safety.Stop()

	started := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- s.Read(context.Background(), func(tx *ReadTx) error {
			if _, err := tx.GenerationNumber(); err != nil {
				return err
			}
			close(started)
			select {
			case <-tx.ctx.Done():
			case <-release:
			}
			// Обращение к соединению после Close: оно ещё принадлежит
			// читателю и обязано быть живым.
			_, err := tx.c.queryInt(context.WithoutCancel(tx.ctx), `SELECT COUNT(*) FROM meta`)
			if err != nil {
				return err
			}
			return tx.ctx.Err()
		})
	}()
	<-started
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-readDone:
		if !errors.Is(err, ErrStoreClosed) {
			t.Fatalf("прерванное закрытием чтение вернуло %v, ожидалась ErrStoreClosed", err)
		}
	case <-release:
		t.Fatal("чтение не прервано закрытием")
	}
	if err := s.Read(context.Background(), func(*ReadTx) error { return nil }); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Read после Close вернул %v, ожидалась ErrStoreClosed", err)
	}
}

// Close во время Rebuild (issue #13, риск ADR-023): закрытие не должно
// оставить проект на пустой эпохе или без прежних данных. Два окна: отмена во
// время наполнения (до публикации) и закрытие после checkpoint, прямо перед
// записью указателя. В первом указатель остаётся на прежней эпохе с её
// данными, во втором смотрит на полностью собранную новую, а прежняя не
// удаляется.
func TestCloseDuringRebuild(t *testing.T) {
	cases := []struct {
		name string
		// afterCheckpoint: Close приходит из hookBeforePublish, а не из fill.
		afterCheckpoint bool
		wantEpoch       int
		wantSeed        string
	}{
		{name: "до публикации", afterCheckpoint: false, wantEpoch: 1, wantSeed: "прежняя"},
		{name: "после checkpoint", afterCheckpoint: true, wantEpoch: 2, wantSeed: "новая"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, root := openClosableStore(t)
			ctx := context.Background()
			if err := s.Write(ctx, func(tx *WriteTx) error { return tx.SetMeta("seed", "прежняя") }); err != nil {
				t.Fatalf("запись прежней эпохи: %v", err)
			}
			st, err := s.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			oldEpoch := st.Epoch

			closeDone := make(chan error, 1)
			// startClose запускает Close и ждёт, пока хранилище пометится
			// закрытым: сам Close вернётся только после выхода Rebuild.
			startClose := func() {
				go func() { closeDone <- s.Close() }()
				<-s.closeCtx.Done()
			}
			if tc.afterCheckpoint {
				hookBeforePublish = startClose
				t.Cleanup(func() { hookBeforePublish = nil })
			}
			rbErr := s.Rebuild(ctx, func(tx *WriteTx) error {
				if err := tx.SetMeta("seed", "новая"); err != nil {
					return err
				}
				if !tc.afterCheckpoint {
					startClose()
					<-tx.ctx.Done()
					return tx.ctx.Err()
				}
				return nil
			})
			if !errors.Is(rbErr, ErrStoreClosed) {
				t.Fatalf("Rebuild под Close вернул %v, ожидалась ErrStoreClosed", rbErr)
			}
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("Close: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Close не вернулся после выхода Rebuild")
			}

			// Прежняя эпоха цела в обоих окнах: публикация её не трогала, а
			// отменённая сборка удаляет только свой файл.
			if _, err := os.Stat(epochFile(s.dir, s.opts.ProjectID, oldEpoch)); err != nil {
				t.Fatalf("файл прежней эпохи: %v", err)
			}
			re, err := Open(root, Options{ProjectID: "project", StateDirName: testStateDir})
			if err != nil {
				t.Fatalf("повторное Open: %v", err)
			}
			defer re.Close()
			rst, err := re.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if rst.Epoch != tc.wantEpoch {
				t.Fatalf("указатель смотрит на эпоху %d, ожидалась %d", rst.Epoch, tc.wantEpoch)
			}
			if rst.NeedsFullRebuild {
				t.Fatal("после переоткрытия эпоха пустая и требует полной пересборки")
			}
			var seed string
			if err := re.Read(ctx, func(tx *ReadTx) error {
				var err error
				seed, err = tx.Meta("seed")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if seed != tc.wantSeed {
				t.Fatalf("данные опубликованной эпохи %q, ожидались %q", seed, tc.wantSeed)
			}
		})
	}
}

// Read, ждавший свободного читателя в момент Close, возвращает
// ErrStoreClosed, а не ErrReaderPoolClosed: отмена контекста через
// context.AfterFunc приходит асинхронно, и пул может закрыться раньше неё.
// Гонка недетерминирована, поэтому сценарий повторяется.
func TestReadWaitingForPoolDuringClose(t *testing.T) {
	for i := 0; i < 30; i++ {
		root := t.TempDir()
		s, err := Open(root, Options{ProjectID: "project", StateDirName: testStateDir, ReaderPoolSize: 1})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		started := make(chan struct{})
		holderDone := make(chan error, 1)
		go func() {
			holderDone <- s.Read(context.Background(), func(tx *ReadTx) error {
				close(started)
				<-tx.ctx.Done()
				return nil
			})
		}()
		<-started
		waiterDone := make(chan error, 1)
		go func() {
			waiterDone <- s.Read(context.Background(), func(*ReadTx) error { return nil })
		}()
		// Дать ждущему встать в очередь пула.
		time.Sleep(time.Millisecond)
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := <-waiterDone; !errors.Is(err, ErrStoreClosed) {
			t.Fatalf("итерация %d: ждавший читатель вернул %v, ожидалась ErrStoreClosed", i, err)
		}
		<-holderDone
	}
}

// Второй Close, пришедший во время первого, ждёт его конца (issue #13):
// вернувшийся Close означает закрытые соединения, для любого вызывающего.
func TestConcurrentCloseWaitsForFirst(t *testing.T) {
	s, _ := openClosableStore(t)
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	safety := time.AfterFunc(5*time.Second, releaseOnce)
	defer safety.Stop()

	started := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- s.Write(context.Background(), func(tx *WriteTx) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	first := make(chan error, 1)
	go func() { first <- s.Close() }()
	<-s.closeCtx.Done()
	second := make(chan error, 1)
	go func() { second <- s.Close() }()
	select {
	case <-second:
		t.Fatal("второй Close вернулся, пока первый ещё ждёт запись")
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce()
	for _, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Close не вернулся")
		}
	}
	<-writeDone
}
