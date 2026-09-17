package store

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Crash-тесты — тот шов, который нельзя выразить через сервисы приложения:
// проверяется поведение при УБИЙСТВЕ процесса в конкретной точке, а точка
// находится внутри хранилища. Подопытным служит этот же тестовый бинарник,
// перезапущенный с переменной окружения: так «падение» получается
// детерминированным и одинаковым на macOS и Windows (там Process.Kill — это
// TerminateProcess), а самоубийство сигналом непереносимо.
const (
	crashModeEnv = "MCP1C_STORE_CRASH_MODE"
	crashDirEnv  = "MCP1C_STORE_CRASH_DIR"
	crashProject = "project"
)

func TestMain(m *testing.M) {
	switch os.Getenv(crashModeEnv) {
	case "write":
		crashDuringWrite()
	case "epoch":
		crashBeforePointerPublish()
	}
	os.Exit(m.Run())
}

// crashDuringWrite открывает хранилище, набивает write-транзакцию и зависает,
// НЕ доходя до COMMIT.
func crashDuringWrite() {
	s, err := Open(os.Getenv(crashDirEnv), Options{ProjectID: crashProject, StateDirName: testStateDir})
	if err != nil {
		die(err)
	}
	err = s.Write(context.Background(), func(tx *WriteTx) error {
		for i := 0; i < 20000; i++ {
			if err := tx.c.exec(tx.ctx, `INSERT INTO fts_symbols(rowid,name,signature,doc) VALUES(?,?,?,?)`,
				int64(900000+i), fmt.Sprintf("НедописанныйСимвол%d", i), "Функция()",
				"хвост незакоммиченной транзакции"); err != nil {
				return err
			}
		}
		if err := tx.SetMeta(metaCurrentGeneration, "999"); err != nil {
			return err
		}
		ready()
		return nil
	})
	die(fmt.Errorf("подпроцесс не был убит вовремя: %v", err))
}

// crashBeforePointerPublish доводит новую эпоху до полностью готового состояния
// (validate + checkpoint + close) и зависает ДО записи указателя.
func crashBeforePointerPublish() {
	s, err := Open(os.Getenv(crashDirEnv), Options{ProjectID: crashProject, StateDirName: testStateDir})
	if err != nil {
		die(err)
	}
	hookBeforePublish = ready
	err = s.Rebuild(context.Background(), func(tx *WriteTx) error {
		var f fixture
		return seedFixture(tx, &f)
	})
	die(fmt.Errorf("подпроцесс не был убит вовремя: %v", err))
}

// ready сообщает родителю, что точка падения достигнута, и ждёт убийства.
func ready() {
	fmt.Println("READY")
	os.Stdout.Sync()
	time.Sleep(10 * time.Minute)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "подопытный процесс:", err)
	os.Exit(2)
}

// startAndKill запускает подопытный процесс, дожидается READY и убивает его.
func startAndKill(t *testing.T, mode, dir string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("путь к тестовому бинарнику: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), crashModeEnv+"="+mode, crashDirEnv+"="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "READY" {
				done <- "ready"
				return
			}
		}
		done <- "eof"
	}()
	select {
	case s := <-done:
		if s != "ready" {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("подопытный процесс завершился, не дойдя до точки падения")
		}
	case <-time.After(120 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("подопытный процесс не дошёл до точки падения за 120 с")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	cmd.Wait()
}

// Падение посреди write-транзакции: восстановления не требуется вовсе — SQLite
// откатывает незакоммиченную транзакцию сам, индекс остаётся на прежнем
// поколении, а незакоммиченные строки (включая FTS) не выживают.
func TestCrashDuringWriteTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("crash-тест запускает подпроцесс, пропущен в -short")
	}
	root := t.TempDir()
	opts := Options{ProjectID: crashProject, StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var f fixture
	if err := s.Write(ctx, func(tx *WriteTx) error { return seedFixture(tx, &f) }); err != nil {
		t.Fatal(err)
	}
	stBefore, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	symbolsBefore := countRows(t, s, "fts_symbols", "")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	startAndKill(t, "write", root)

	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("хранилище не открывается после падения: %v", err)
	}
	defer s2.Close()
	stAfter, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stAfter.Generation != stBefore.Generation {
		t.Errorf("поколение изменилось незакоммиченной транзакцией: было %q, стало %q",
			stBefore.Generation, stAfter.Generation)
	}
	if n := countRows(t, s2, "fts_symbols", ""); n != symbolsBefore {
		t.Errorf("незакоммиченные строки FTS выжили: было %d, стало %d", symbolsBefore, n)
	}
	var ic string
	if err := s2.Read(ctx, func(tx *ReadTx) error {
		var err error
		ic, err = integrityCheck(tx.ctx, tx.c)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ic != "ok" {
		t.Errorf("integrity_check после падения: %q", ic)
	}
	assertValid(t, s2, "после падения посреди write-транзакции")
}

// Падение ПОСЛЕ validate/checkpoint/close, но ДО публикации указателя:
// полностью готовая эпоха всё равно НЕ публикуется — fingerprint источников с
// момента сборки мог устареть, поэтому работа продолжается на старой эпохе.
// Её файл при этом остаётся на диске (ADR-023): «не опубликована» перестало
// означать «удалить».
func TestCrashBeforePointerPublish(t *testing.T) {
	if testing.Short() {
		t.Skip("crash-тест запускает подпроцесс, пропущен в -short")
	}
	root := t.TempDir()
	opts := Options{ProjectID: crashProject, StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.UpsertComponent(Component{ID: "cfg", Kind: "configuration", Root: "."})
	}); err != nil {
		t.Fatal(err)
	}
	stBefore, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	startAndKill(t, "epoch", root)

	dir, err := ProjectIndexDir(root, testStateDir, crashProject)
	if err != nil {
		t.Fatal(err)
	}
	epoch2 := filepath.Join(dir, epochName(crashProject, 2))
	if _, err := os.Stat(epoch2); err != nil {
		t.Fatalf("файл эпохи 2 не создан, тест ничего не проверяет: %v", err)
	}

	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("хранилище не открывается после падения: %v", err)
	}
	defer s2.Close()
	stAfter, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stAfter.Epoch != stBefore.Epoch {
		t.Errorf("после падения работаем на эпохе %d, ожидалась прежняя %d", stAfter.Epoch, stBefore.Epoch)
	}
	if stAfter.Generation != stBefore.Generation {
		t.Errorf("поколение изменилось: было %q, стало %q", stBefore.Generation, stAfter.Generation)
	}
	if stAfter.NeedsFullRebuild {
		t.Error("работа на старой эпохе объявлена невозможной, хотя указатель цел")
	}
	// ADR-023 поменял здесь исход: готовая, но неопубликованная эпоха больше
	// НЕ удаляется — в её файле есть данные. Она остаётся на диске
	// неопубликованной и видна в статусе как kept. Работа при этом идёт на
	// старой эпохе, как и раньше: автопубликация по-прежнему запрещена.
	if _, err := os.Stat(epoch2); err != nil {
		t.Errorf("эпоха с данными удалена при старте вопреки инварианту: %v", err)
	}
	if len(stAfter.RemovedOrphans) != 0 {
		t.Errorf("удалена эпоха с данными: %v", stAfter.RemovedOrphans)
	}
	if len(stAfter.KeptEpochs) != 1 || stAfter.KeptEpochs[0] != epochName(crashProject, 2) {
		t.Errorf("оставленная эпоха не отражена в статусе: %v", stAfter.KeptEpochs)
	}
	// Данные старой эпохи целы: компонент на месте, символов не появилось.
	if n := countRows(t, s2, "component", ""); n != 1 {
		t.Errorf("компонентов %d, ожидался 1", n)
	}
	if n := countRows(t, s2, "symbol", ""); n != 0 {
		t.Errorf("факты неопубликованной эпохи просочились в рабочую: символов %d", n)
	}
	assertValid(t, s2, "после падения перед публикацией указателя")
}
