package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Умолчания хранилища. Ноль или отрицательное значение в Options означает
// «взять умолчание», а не «без границы» (ADR-2 §7).
const (
	DefaultBlobTTL     = 30 * time.Minute
	DefaultBusyTimeout = 5 * time.Second

	epochRemoveAttempts = 10
	epochRemovePause    = 100 * time.Millisecond
)

// ErrStoreClosed — хранилище закрыто.
var ErrStoreClosed = errors.New("хранилище закрыто")

// Options — настройки хранилища одного logical project.
type Options struct {
	// ProjectID участвует в построении путей и в имени файлов эпох; его форму
	// проверяет domain — тот же слаг лежит в манифесте и в реестре.
	ProjectID domain.ProjectID
	// StateDirName — имя каталога сгенерированного состояния сервера (у
	// workspace это RegistryDirName). Приходит параметром, а не константой:
	// каталог принадлежит workspace, и вторая правда о нём здесь означала бы
	// два разных места, куда сервер кладёт своё состояние.
	StateDirName string
	// ReaderPoolSize — размер пула читателей; 0 или меньше — DefaultReaderPoolSize.
	ReaderPoolSize int
	// BlobTTL — сколько живёт blob, потерявший ссылки (18.2); 0 — DefaultBlobTTL.
	BlobTTL time.Duration
	// BusyTimeout — per-connection PRAGMA busy_timeout; 0 — DefaultBusyTimeout.
	BusyTimeout time.Duration
	// Now подменяет часы в тестах TTL; nil — time.Now.
	Now func() time.Time
}

func (o *Options) fill() {
	if o.ReaderPoolSize <= 0 {
		o.ReaderPoolSize = DefaultReaderPoolSize
	}
	if o.BlobTTL <= 0 {
		o.BlobTTL = DefaultBlobTTL
	}
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = DefaultBusyTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

func (o *Options) busyMillis() int { return int(o.BusyTimeout / time.Millisecond) }

// Store — хранилище индекса одного logical project: текущая эпоха, единственный
// writer и ограниченный пул читателей поверх неё.
type Store struct {
	dir  string // каталог индекса проекта: <root>/<stateDir>/index/<project-id>
	opts Options
	ptr  *pointer

	// writeSem — один writer на проект (ADR-2 §7). Канал, а не sync.Mutex:
	// ожидание очереди обязано прерываться контекстом вызова.
	writeSem chan struct{}

	// closeCtx отменяется в Close с причиной ErrStoreClosed (issue #13). От
	// него зависят контексты идущих записей и чтений: Close прерывает их, а не
	// рвёт соединение из-под транзакции, и отпускает стоящих в очереди.
	closeCtx    context.Context
	cancelClose context.CancelCauseFunc
	// closeDone закрывается, когда первый Close закрыл соединения: второй
	// одновременный Close ждёт его, а не возвращается раньше времени.
	closeDone chan struct{}

	mu               sync.Mutex
	closed           bool
	epoch            int
	writer           *conn
	readers          *readerPool
	needsFullRebuild bool
	removedOrphans   []string
	// keptEpochs — эпохи с данными, на которые указатель не смотрит: они
	// остались на диске и НЕ опубликованы (ADR-023). Наблюдаемость здесь
	// обязательна: иначе «файл цел» отличается от «файл удалён» только тем,
	// что о нём никто не знает.
	keptEpochs   []string
	recoveryNote string
	pointerSeq   uint64
}

// hookBeforePublish вызывается между закрытием собранной эпохи и записью
// указателя. Нужен ровно одному сценарию — crash-тесту «падение после
// validate/checkpoint/close, но ДО публикации» (18.1, случай 2), который иначе
// выразить нечем: точка находится внутри публикации.
var hookBeforePublish func()

// Open открывает хранилище проекта. dir — корень workspace; файлы индекса живут
// в <dir>/<opts.StateDirName>/index/<project-id>/ (ADR-2 §9.1).
//
// При открытии выполняется recovery раздела 18.1 в редакции ADR-023: эпоха, на
// которую указатель не смотрит, НЕ публикуется автоматически, но и не
// удаляется, если в её файле есть данные — она попадает в Status().KeptEpochs.
// Удаляются только доказуемые остатки прерванной сборки. Если достоверной
// записи указателя нет, создаётся пустая эпоха, а Status().NeedsFullRebuild
// сообщает вызывающему, что индекс надо собрать из XML: угадывать по
// validated_at запрещено.
//
// Указатель на эпоху без данных при живой соседней останавливает открытие
// EpochQuarantineError-ом: ни один файл не трогается, решение за человеком.
func Open(dir string, opts Options) (*Store, error) {
	opts.fill()
	indexDir, err := ProjectIndexDir(dir, opts.StateDirName, opts.ProjectID)
	if err != nil {
		return nil, err
	}
	// Права те же, что у workspace на его каталоге состояния: один и тот же
	// каталог не должен получать разный режим в зависимости от того, кто создал
	// его первым.
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return nil, fmt.Errorf("каталог индекса %s: %w", indexDir, err)
	}
	s := &Store{
		dir:       indexDir,
		opts:      opts,
		ptr:       newPointer(indexDir),
		writeSem:  make(chan struct{}, 1),
		closeDone: make(chan struct{}),
	}
	s.closeCtx, s.cancelClose = context.WithCancelCause(context.Background())
	ctx := context.Background()
	if err := s.start(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) start(ctx context.Context) error {
	rep, err := recoverEpochs(s.dir, s.opts.ProjectID, s.ptr)
	if err != nil {
		return err
	}
	s.needsFullRebuild = rep.needFullRebuild
	s.pointerSeq = rep.seq
	s.keptEpochs = append([]string(nil), rep.kept...)
	s.recoveryNote = rep.note
	for _, name := range rep.orphans {
		s.dropUnpublishedEpoch(name)
	}

	if rep.pointerOK {
		n, _ := epochNumber(s.opts.ProjectID, rep.current)
		s.epoch = n
	} else {
		// Аварийная сборка: номер берётся тем же nextFreeEpoch, что и на ветке
		// «схема из будущего». Текущей эпохи здесь ещё нет, поэтому в расчёт
		// идут только файлы на диске. Два выражения одного понятия в этом
		// месте однажды уже стоили индекса.
		next := nextFreeEpoch(rep.maxEpoch, s.epoch)
		seq, err := s.buildEmptyEpoch(ctx, next)
		if err != nil {
			return err
		}
		s.epoch, s.pointerSeq = next, seq
	}
	if err := s.openEpochConns(ctx); err != nil {
		return err
	}
	if _, err := migrate(ctx, s.writer, migrations, SchemaVersion); err != nil {
		var future errSchemaFromFuture
		if !errors.As(err, &future) {
			return err
		}
		// Схема из будущего: по разделу 15 это новая эпоха с полным rebuild, а
		// не попытка открыть непонятное. Старая эпоха не публикуется.
		//
		// Это НЕ ротация: преемник — пустая эпоха с needsFullRebuild, а
		// удаляемый файл остаётся единственной копией данных. Поэтому
		// инвариант ADR-023 действует на всей ветке: и на уборке старой эпохи
		// (dropUnpublishedEpoch вместо removeEpoch), и на самой сборке
		// (buildEmergency), и на выборе её номера.
		old := s.epoch
		s.closeEpochConns()
		next := nextFreeEpoch(rep.maxEpoch, old)
		seq, err := s.buildEmptyEpoch(ctx, next)
		if err != nil {
			return err
		}
		s.epoch, s.pointerSeq = next, seq
		s.needsFullRebuild = true
		s.dropUnpublishedEpoch(epochName(s.opts.ProjectID, old))
		return s.openEpochConns(ctx)
	}
	// Признак «структура новая, данных нет» читается на КАЖДОМ открытии, а не
	// только в том процессе, который применил миграцию: он лежит в meta эпохи и
	// снимается лишь полной переиндексацией. Индекс при этом открывается и
	// читается как есть — требование видно снаружи в
	// StoreStatus.NeedsFullRebuild, и это лучше и молчания, и отказа открыть
	// базу.
	pending, err := readNeedsFullRebuild(ctx, s.writer)
	if err != nil {
		return err
	}
	if pending {
		s.needsFullRebuild = true
	}
	return nil
}

// dropUnpublishedEpoch — единственный путь уборки НЕопубликованной эпохи на
// старте. Инвариант ADR-023 стоит внутри removeUnpublishedEpoch, а исход уборки
// обязан быть виден снаружи: файл, который инвариант спас, попадает в
// KeptEpochs и в RecoveryNote. Молча уцелевший файл отличается от удалённого
// только тем, что о нём никто не знает, — а спасение данных ровно тот случай,
// ради которого инвариант и поставлен.
func (s *Store) dropUnpublishedEpoch(name string) {
	err := removeUnpublishedEpoch(s.epochPath(name), epochRemoveAttempts, epochRemovePause)
	switch {
	case err == nil:
		s.removedOrphans = append(s.removedOrphans, name)
	case errors.Is(err, errEpochHasData):
		s.keptEpochs = append(s.keptEpochs, name)
		s.recoveryNote = appendNote(s.recoveryNote,
			fmt.Sprintf("эпоха %s не удалена: инвариант нашёл в файле данные", name))
	default:
		// Файл удерживается чужим процессом (Windows). Это не повод не
		// открыться: эпоха не опубликована, следующий старт повторит уборку.
		s.recoveryNote = appendNote(s.recoveryNote,
			fmt.Sprintf("эпоха %s не удалена: %v", name, err))
	}
}

// epochPath — путь к файлу эпохи по её базовому имени.
func (s *Store) epochPath(name string) string { return filepath.Join(s.dir, name) }

// currentPath — файл текущей эпохи.
func (s *Store) currentPath() string { return epochFile(s.dir, s.opts.ProjectID, s.epoch) }

// epochBuildMode — кто заказал сборку. От этого зависит судьба чужого файла,
// оказавшегося под номером собираемой эпохи (ADR-023, раздел «Границы»).
type epochBuildMode int

const (
	// buildDeliberate — сознательная пересборка (Rebuild): преемник собирается
	// С ДАННЫМИ и публикуется, поэтому занять номер неопубликованного остатка
	// это ротация, а не потеря.
	buildDeliberate epochBuildMode = iota
	// buildEmergency — аварийная сборка со старта: преемник ПУСТОЙ, с
	// needsFullRebuild. Занять здесь номер эпохи с данными значит уничтожить
	// единственную копию ради пустого файла, поэтому действует инвариант.
	buildEmergency
)

// buildEmptyEpoch создаёт пустую эпоху и публикует её по строгому порядку
// раздела 18.1. Пустая опубликованная эпоха честнее отсутствия базы: сервер
// отвечает и говорит «нужен rebuild», вместо того чтобы не запуститься.
//
// Всегда buildEmergency: пустая эпоха преемником данных не является.
func (s *Store) buildEmptyEpoch(ctx context.Context, n int) (uint64, error) {
	return s.buildEpoch(ctx, n, buildEmergency, nil)
}

// nextFreeEpoch — номер для аварийной сборки: строго выше и текущей эпохи, и
// любого файла эпохи, найденного на диске. Обходить занятые номера дешевле, чем
// упираться в инвариант на чужом файле с данными.
func nextFreeEpoch(maxOnDisk, current int) int {
	n := maxOnDisk
	if current > n {
		n = current
	}
	return n + 1
}

// buildEpoch собирает эпоху отдельным файлом и публикует её строго в порядке
// раздела 18.1: сборка -> validate (инварианты + integrity_check + отметка
// validated_at) -> wal_checkpoint(TRUNCATE) -> закрытие соединения -> атомарная
// запись указателя. Rename поверх открытого SQLite не используется нигде.
//
// Состояние Store здесь НЕ меняется и мьютекс не держится: сборка идёт минуты,
// а всё это время сервер обязан отвечать из last known good. Вызывающий
// подменяет поля эпохи уже после публикации, коротким участком под мьютексом.
func (s *Store) buildEpoch(ctx context.Context, n int, mode epochBuildMode, fill func(*WriteTx) error) (uint64, error) {
	path := epochFile(s.dir, s.opts.ProjectID, n)
	if _, err := os.Stat(path); err == nil {
		// Под номером собираемой эпохи лежит чужой файл. Снести его без
		// инварианта можно ровно в одном случае: сборка сознательная И
		// непубликация этого номера ДОКАЗАНА. Доказательство даёт только
		// прочитанный указатель, назвавший другую эпоху: нечитаемый или
		// уничтоженный указатель не говорит о публикации ничего.
		published, _, pErr := s.ptr.Read()
		clean := removeEpoch
		switch {
		case pErr == nil && published == epochName(s.opts.ProjectID, n):
			return 0, fmt.Errorf("сборка эпохи %d отменена: указатель называет %q опубликованной",
				n, published)
		case pErr != nil, mode == buildEmergency:
			clean = removeUnpublishedEpoch
		}
		if err := clean(path, epochRemoveAttempts, epochRemovePause); err != nil {
			return 0, fmt.Errorf("остаток эпохи %s не удаляется: %w", path, err)
		}
	}
	c, err := createConn(ctx, path, s.opts.busyMillis())
	if err != nil {
		return 0, err
	}
	fail := func(err error) (uint64, error) {
		c.Close()
		removeEpoch(path, epochRemoveAttempts, epochRemovePause)
		return 0, err
	}
	if err := applySchema(ctx, c, n); err != nil {
		return fail(err)
	}
	if fill != nil {
		if err := runWriteTx(ctx, c, &s.opts, fill); err != nil {
			return fail(fmt.Errorf("сборка эпохи %d: %w", n, err))
		}
	}

	// validate: инварианты раздела 15 + integrity_check. Отметка validated_at
	// ставится ТОЛЬКО после успеха и означает «готова», но НЕ «опубликована».
	v, err := validate(ctx, c)
	if err != nil {
		return fail(err)
	}
	if bad := validateFailures(v); len(bad) > 0 {
		return fail(fmt.Errorf("эпоха %d не прошла validate: %v", n, bad))
	}
	ic, err := integrityCheck(ctx, c)
	if err != nil {
		return fail(err)
	}
	if ic != "ok" {
		return fail(fmt.Errorf("эпоха %d: integrity_check=%q", n, ic))
	}
	if err := c.exec(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`,
		metaValidatedAt, itoa(int(s.opts.Now().Unix()))); err != nil {
		return fail(err)
	}

	busy, walPages, moved, err := c.checkpoint(ctx, "TRUNCATE")
	if err != nil {
		return fail(err)
	}
	if busy != 0 {
		return fail(fmt.Errorf("checkpoint(TRUNCATE) вернул busy=%d (страниц %d, перенесено %d): "+
			"эпоха %d не готова к публикации", busy, walPages, moved, n))
	}
	if err := c.Close(); err != nil {
		return fail(err)
	}
	// Указатель публикуется только на полностью закрытую эпоху, рядом с которой
	// не осталось WAL: непустой WAL означает, что закрылись не все соединения.
	walBytes, err := fileSize(path + "-wal")
	if err != nil {
		return 0, fmt.Errorf("не удалось проверить WAL после close: %w", err)
	}
	if walBytes != 0 {
		return 0, fmt.Errorf("после checkpoint(TRUNCATE) и close рядом с эпохой %d остался WAL %d Б: "+
			"публикация отменена", n, walBytes)
	}

	if hookBeforePublish != nil {
		hookBeforePublish()
	}
	seq, err := s.ptr.Publish(epochName(s.opts.ProjectID, n))
	if err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) openEpochConns(ctx context.Context) error {
	w, err := openConn(ctx, s.currentPath(), s.opts.busyMillis())
	if err != nil {
		return fmt.Errorf("соединение писателя: %w", err)
	}
	pool, err := newReaderPool(ctx, s.currentPath(), s.opts.ReaderPoolSize, s.opts.busyMillis())
	if err != nil {
		w.Close()
		return err
	}
	s.writer, s.readers = w, pool
	return nil
}

func (s *Store) closeEpochConns() {
	if s.readers != nil {
		s.readers.Close()
		s.readers = nil
	}
	if s.writer != nil {
		s.writer.Close()
		s.writer = nil
	}
}

// Rebuild собирает НОВУЮ эпоху из fill и переключает на неё проект. Применяется
// там, где инкремент неприменим: полная пересборка, массовая перевыгрузка,
// несовместимая миграция (18.1).
//
// Старая эпоха остаётся last known good до самого момента публикации указателя,
// и всё время сборки сервер продолжает отвечать: мьютекс состояния берётся
// только на подмену эпохи и соединений, а не на сборку. Держать его на сборке
// значило бы остановить чтение на всю полную переиндексацию — ровно то, чего
// требование «отвечать из last known good во время rebuild» запрещает.
//
// После переключения файл старой эпохи удаляется; если ОС его удерживает
// (Windows, незавершённый читатель), он остаётся orphan-ом и будет убран при
// следующем старте — это ожидаемый путь, а не сбой.
func (s *Store) Rebuild(ctx context.Context, fill func(*WriteTx) error) (err error) {
	ctx, done, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer func() { err = done(err) }()
	s.mu.Lock()
	old := s.epoch
	s.mu.Unlock()

	// Сборка идёт БЕЗ мьютекса: читатели работают на старой эпохе.
	seq, err := s.buildEpoch(ctx, old+1, buildDeliberate, fill)
	if err != nil {
		return err
	}

	// Публикация состоялась: подменяем эпоху и соединения. Активные читатели
	// дорабатывают на старой со своими арендованными соединениями.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrStoreClosed
	}
	s.closeEpochConns()
	s.epoch, s.pointerSeq, s.needsFullRebuild = old+1, seq, false
	err = s.openEpochConns(ctx)
	if err == nil {
		// Полная переиндексация — ЕДИНСТВЕННОЕ, что снимает признак, поэтому
		// снятие стоит здесь же, где гасится флаг процесса. Новая эпоха —
		// свежий файл и признака в своей meta не имеет; явное снятие держит
		// связь «полная сборка завершена -> признак снят» в одном месте, а не
		// на памяти о том, что meta новой эпохи пуста.
		err = clearNeedsFullRebuild(ctx, s.writer)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	removeEpoch(epochFile(s.dir, s.opts.ProjectID, old), epochRemoveAttempts, epochRemovePause)
	return nil
}

// Read выполняет fn в ОДНОЙ read-транзакции на весь вызов (18.1): WAL фиксирует
// снапшот на всё время, поэтому span и текст blob не могут разъехаться, а
// параллельный commit писателя читателю не виден.
//
// Close во время чтения отменяет его контекст, как и у писателя; соединение
// закрывается уже после конца транзакции, при возврате в закрытый пул.
func (s *Store) Read(ctx context.Context, fn func(*ReadTx) error) error {
	pool, err := s.pool()
	if err != nil {
		return err
	}
	ctx, done := s.bindClose(ctx)
	c, err := pool.acquire(ctx)
	if err != nil {
		return done(err)
	}
	return done(runRead(ctx, pool, c, fn))
}

// isClosed: единственная проверка «хранилище закрыто» для входов Write,
// Rebuild и Read и для перевода их ошибок в ErrStoreClosed.
func (s *Store) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// bindClose связывает контекст вызова с закрытием хранилища: Close отменяет
// его с причиной ErrStoreClosed. done снимает связь и переводит ошибку
// вызова, завершившегося на закрытом хранилище, в ErrStoreClosed, сохраняя
// исходную в тексте. Смотреть надо на флаг, а не на причину отмены
// контекста: AfterFunc отменяет асинхронно, и пул читателей может закрыться
// раньше, отдав ErrReaderPoolClosed.
func (s *Store) bindClose(ctx context.Context) (context.Context, func(error) error) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(s.closeCtx, func() { cancel(ErrStoreClosed) })
	return ctx, func(err error) error {
		stop()
		cancel(nil)
		if err != nil && !errors.Is(err, ErrStoreClosed) && s.isClosed() {
			return fmt.Errorf("%w: вызов прерван закрытием: %v", ErrStoreClosed, err)
		}
		return err
	}
}

// acquireWriter занимает очередь писателя (ADR-014: писатель один). Ожидание
// прерывается и контекстом вызова, и закрытием хранилища: стоящий в очереди
// после Close получает ErrStoreClosed, а не ждёт конца чужой записи. Отданный
// контекст отменяется закрытием; done освобождает очередь, и только после
// этого Close может закрыть соединение писателя.
func (s *Store) acquireWriter(ctx context.Context) (context.Context, func(error) error, error) {
	select {
	case s.writeSem <- struct{}{}:
	case <-s.closeCtx.Done():
		return nil, nil, ErrStoreClosed
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if s.isClosed() {
		<-s.writeSem
		return nil, nil, ErrStoreClosed
	}
	ctx, done := s.bindClose(ctx)
	return ctx, func(err error) error {
		err = done(err)
		<-s.writeSem
		return err
	}, nil
}

// pool отдаёт текущий пул читателей. Мьютекс держится ровно на чтение поля:
// пул подменяется при переключении эпохи, и ждать под ним нельзя.
func (s *Store) pool() (*readerPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.readers == nil {
		return nil, ErrStoreClosed
	}
	return s.readers, nil
}

// runRead проводит fn через одну read-транзакцию на уже арендованном соединении
// и возвращает соединение пулу — либо уничтожает его, если оно могло остаться в
// неопределённом состоянии.
func runRead(ctx context.Context, pool *readerPool, c *conn, fn func(*ReadTx) error) error {
	if err := c.exec(ctx, "BEGIN"); err != nil {
		pool.discard(context.WithoutCancel(ctx), c)
		return fmt.Errorf("начало read-транзакции: %w", err)
	}
	tx := &ReadTx{ctx: ctx, c: c}
	// Паника вызывающего оставила бы соединение с открытой транзакцией: такое
	// нельзя ни вернуть в пул, ни потерять — оно держит снапшот и растит WAL.
	panicked := true
	defer func() {
		if panicked {
			tx.done = true
			c.exec(context.WithoutCancel(ctx), "ROLLBACK")
			pool.discard(context.WithoutCancel(ctx), c)
		}
	}()
	fnErr := fn(tx)
	panicked = false
	tx.done = true
	// Read-транзакция завершается всегда, даже на ошибке: живой читатель не даёт
	// checkpoint-у переносить страницы, и WAL растёт (ADR-2 §8).
	end := "COMMIT"
	if fnErr != nil {
		end = "ROLLBACK"
	}
	if err := c.exec(context.WithoutCancel(ctx), end); err != nil {
		pool.discard(context.WithoutCancel(ctx), c)
		if fnErr != nil {
			return fnErr
		}
		return fmt.Errorf("завершение read-транзакции: %w", err)
	}
	if ctx.Err() != nil {
		// Отменённый вызов мог оставить соединение с прерванным запросом:
		// возвращать такое в пул нельзя.
		pool.discard(context.WithoutCancel(ctx), c)
	} else {
		pool.release(c)
	}
	return fnErr
}

// Write выполняет fn в ОДНОЙ write-транзакции BEGIN IMMEDIATE. COMMIT —
// единственная точка публикации инкремента (18.1): ни staging-состояний, ни
// собственного MVCC нет, а падение в любой точке = обычный откат силами SQLite.
//
// Store сам доводит транзакцию до конца по разделу 15: учёт blob по затронутым
// хэшам, GC по TTL, reconciliation (5a/5b) и current_generation+1. Шаги (1)-(4)
// — дело вызывающего: только он знает affected set.
//
// Close во время записи отменяет её контекст и ждёт, пока транзакция
// зафиксируется или откатится; прерванная так запись возвращает ErrStoreClosed.
func (s *Store) Write(ctx context.Context, fn func(*WriteTx) error) (err error) {
	ctx, done, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer func() { err = done(err) }()
	s.mu.Lock()
	w := s.writer
	s.mu.Unlock()
	if w == nil {
		return ErrStoreClosed
	}
	return runWriteTx(ctx, w, &s.opts, fn)
}

// runWriteTx — тело write-транзакции, общее для Write и сборки эпохи.
func runWriteTx(ctx context.Context, c *conn, opts *Options, fn func(*WriteTx) error) (err error) {
	if err := c.exec(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("начало write-транзакции: %w", err)
	}
	// Кэш подготовленных выражений и буферы пакетной вставки живут ровно эту
	// транзакцию (issue #3, шаг 2) и закрываются до COMMIT или ROLLBACK.
	c.beginStmtCache()
	tx := &WriteTx{ReadTx: ReadTx{ctx: ctx, c: c}, touched: map[string]struct{}{}, batches: newTxBatches()}
	tx.flush = func() error { return tx.batches.flushAll(tx.ctx, tx.c) }
	defer func() {
		tx.done = true
		// Паника вызывающего не должна оставлять открытую транзакцию на
		// единственном соединении писателя: сервер живёт долго, и после такого
		// КАЖДАЯ следующая запись падала бы на «transaction within a transaction».
		if p := recover(); p != nil {
			c.endStmtCache()
			c.exec(context.WithoutCancel(ctx), "ROLLBACK")
			panic(p)
		}
		if err == nil {
			return
		}
		c.endStmtCache()
		// Откат идёт по контексту без отмены: на отменённом ctx сам ROLLBACK не
		// выполнится, и транзакция осталась бы открытой на соединении писателя.
		if rbErr := c.exec(context.WithoutCancel(ctx), "ROLLBACK"); rbErr != nil {
			err = fmt.Errorf("%w (и откат не прошёл: %v)", err, rbErr)
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	// Хвосты буферов уходят до учёта blob: дальше транзакция только читает
	// и обновляет, а строки из буфера обязаны быть видны reconciliation.
	if err = tx.flush(); err != nil {
		return err
	}
	now := opts.Now().Unix()
	if err = markBlobsFor(ctx, c, now, tx.touched); err != nil {
		return fmt.Errorf("учёт blob: %w", err)
	}
	if err = gcBlobs(ctx, c, now, int64(opts.BlobTTL/time.Second)); err != nil {
		return fmt.Errorf("GC blob: %w", err)
	}
	if err = reconcile(ctx, c); err != nil {
		return fmt.Errorf("reconciliation: %w", err)
	}
	if err = c.exec(ctx, `UPDATE meta SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT) WHERE key=?`,
		metaCurrentGeneration); err != nil {
		return err
	}
	if err = c.endStmtCache(); err != nil {
		return fmt.Errorf("закрытие подготовленных выражений: %w", err)
	}
	if err = c.exec(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("COMMIT: %w", err)
	}
	return nil
}

// NeedsFullRebuild: стоит ли на эпохе признак полной пересборки, без похода
// в БД и без stat файлов: то же поле, что Status().NeedsFullRebuild, но по
// цене мьютекса. Нужен проверке свежести на каждый вызов индексного
// инструмента (ADR-036), где Status слишком дорог для бюджета find_symbol.
func (s *Store) NeedsFullRebuild() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsFullRebuild
}

// StoreStatus — наблюдаемое состояние хранилища для index_status.
type StoreStatus struct {
	ProjectID        domain.ProjectID  `json:"projectId"`
	Epoch            int               `json:"epoch"`
	Generation       domain.Generation `json:"generation"`
	GenerationNumber int64             `json:"generationNumber"`
	SchemaVersion    int               `json:"schemaVersion"`
	NeedsFullRebuild bool              `json:"needsFullRebuild"`
	ValidatedAt      time.Time         `json:"validatedAt"`
	EpochPath        string            `json:"epochPath"`
	DBBytes          int64             `json:"dbBytes"`
	WALBytes         int64             `json:"walBytes"`
	PointerSeq       uint64            `json:"pointerSeq"`
	RemovedOrphans   []string          `json:"removedOrphans,omitempty"`
	// KeptEpochs — файлы эпох с данными, оставленные на месте восстановлением.
	KeptEpochs   []string          `json:"keptEpochs,omitempty"`
	RecoveryNote string            `json:"recoveryNote,omitempty"`
	Pragmas      map[string]string `json:"pragmas"`
	ReaderPool   ReaderPoolStats   `json:"readerPool"`
	// DBUnavailable — свободного читателя не нашлось, и поля из БД (поколение,
	// pragma, validated_at) не заполнены. Само по себе это диагностика: пул
	// исчерпан, и цифры ожиданий рядом объясняют, кем.
	DBUnavailable bool `json:"dbUnavailable,omitempty"`
}

// Status отдаёт фактическое состояние: поколение, размеры файлов эпохи,
// диагностику пула и ФАКТИЧЕСКИЕ значения pragma (ADR-2 §6.4 — заявленное
// против фактического проверяется в коде, а не в документации). Неограниченный
// рост WAL здесь же — наблюдаемый симптом незакрытой read-транзакции (§8).
func (s *Store) Status(ctx context.Context) (StoreStatus, error) {
	s.mu.Lock()
	st := StoreStatus{
		ProjectID:        s.opts.ProjectID,
		Epoch:            s.epoch,
		SchemaVersion:    SchemaVersion,
		NeedsFullRebuild: s.needsFullRebuild,
		EpochPath:        s.currentPath(),
		PointerSeq:       s.pointerSeq,
		RemovedOrphans:   append([]string(nil), s.removedOrphans...),
		KeptEpochs:       append([]string(nil), s.keptEpochs...),
		RecoveryNote:     s.recoveryNote,
	}
	pool, closed := s.readers, s.closed
	s.mu.Unlock()
	if closed || pool == nil {
		return st, ErrStoreClosed
	}
	st.ReaderPool = pool.Stats()

	var err error
	if st.DBBytes, err = fileSize(st.EpochPath); err != nil {
		return st, err
	}
	if st.WALBytes, err = fileSize(st.EpochPath + "-wal"); err != nil {
		return st, err
	}
	// Диагностика пула нужна ровно тогда, когда свободных читателей нет,
	// поэтому Status НЕ становится в очередь за соединением: поля из БД
	// заполняются, только если читатель нашёлся сразу.
	c, ok := pool.tryAcquire()
	if !ok {
		st.DBUnavailable = true
		return st, nil
	}
	err = runRead(ctx, pool, c, func(tx *ReadTx) error {
		gen, err := tx.GenerationNumber()
		if err != nil {
			return err
		}
		st.GenerationNumber = gen
		st.Generation = domain.NewGeneration(uint64(st.Epoch), uint64(gen))
		if v, err := tx.Meta(metaSchemaVersion); err == nil && v != "" {
			st.SchemaVersion, _ = atoi(v)
		}
		if v, err := tx.Meta(metaValidatedAt); err == nil && v != "" {
			if unix, cErr := atoi(v); cErr == nil {
				st.ValidatedAt = time.Unix(int64(unix), 0)
			}
		}
		st.Pragmas = readPragmas(tx.ctx, tx.c)
		return nil
	})
	return st, err
}

// Close закрывает пул читателей и соединение писателя (issue #13).
//
// Идущие записи и чтения получают отмену контекста, стоящие в очереди
// писателя отпускаются с ErrStoreClosed. Соединение писателя закрывается
// только после того, как текущая транзакция зафиксирована или откатилась:
// закрыть его под транзакцией значит отдать её хвост (закрытие выражений,
// COMMIT) закрытому хэндлу sqlite. Арендованные читатели закрываются при
// возврате в пул, поэтому их Close не ждёт. Второй одновременный Close ждёт
// конца первого.
//
// упрощение: Close из тела fn записи или Rebuild виснет навсегда: он ждёт
// очередь писателя, которую держит тот же вызов, а горутину вызывающего Go не
// различает. Потолок: такого вызова в коде нет, и дёшево его не распознать.
// Путь исправления: помечать контекст транзакции и давать Close(ctx), который
// по этой метке возвращает ошибку.
//
// упрощение: ожидание писателя без потолка. Тело, не слушающее контекст,
// держит Close сколько угодно; рабочие тела ходят в SQL с контекстом
// транзакции и на отмене выходят с ошибкой. Путь
// исправления: Close(ctx) с дедлайном и sqlite3_interrupt на соединении
// писателя по его истечении.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return nil
	}
	s.closed = true
	s.cancelClose(ErrStoreClosed)
	s.mu.Unlock()
	defer close(s.closeDone)

	// Очередь писателя занимается без контекста: держатель уже отменён и
	// обязан выйти, а освобождает очередь он только после конца транзакции.
	s.writeSem <- struct{}{}
	defer func() { <-s.writeSem }()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeEpochConns()
	return nil
}

func atoi(s string) (int, error) { return strconv.Atoi(s) }
