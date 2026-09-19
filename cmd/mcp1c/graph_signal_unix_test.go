//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAdditionalShutdownSignalsIncludesSIGTERM: kill
// (SIGTERM) завершает процесс так же корректно, как Ctrl+C. Регистрация
// сама по себе: additionalShutdownSignals должна назвать SIGTERM, не
// какой-то другой сигнал.
func TestAdditionalShutdownSignalsIncludesSIGTERM(t *testing.T) {
	sigs := additionalShutdownSignals()
	if len(sigs) != 1 || sigs[0] != syscall.SIGTERM {
		t.Fatalf("additionalShutdownSignals() = %v, want [SIGTERM]", sigs)
	}
}

// TestSIGTERMCancelsGraphContext — зовёт ТУ ЖЕ функцию, которой runGraph
// строит sigCtx (graphShutdownSignals, graph.go) — не переписанную копию
// выражения: ревью качества поймало версию этого теста, которая заново
// собирала append([]os.Signal{os.Interrupt}, additionalShutdownSignals()...)
// у себя в теле, и мутация «убрать SIGTERM из вызова в graph.go» оставляла
// такой тест зелёным, потому что он проверял не ту строку. Проверяется
// отправкой РЕАЛЬНОГО SIGTERM ТЕКУЩЕМУ процессу
// (syscall.Kill(os.Getpid(), syscall.SIGTERM)) — законно внутри теста,
// потому что сигнал доставляется процессу, которым и является тестовый
// бинарник, без внешнего запуска. graphweb.Serve уже доказал (internal/
// graphweb/serve_test.go: TestServeStopsOnContextCancelWithoutHangingGoroutine),
// что он одинаково реагирует на отмену ctx независимо от того, что её
// вызвало — SIGINT или SIGTERM неразличимы для Serve, оба видны только как
// закрытие ctx.Done(). Поэтому здесь проверяется ровно недостающее звено:
// что SIGTERM действительно доходит до ctx через graphShutdownSignals(), а
// не то, что Serve умеет гаситься (это уже закрыто).
//
// Windows не тестируется этим файлом (build unix): syscall.Kill в этой
// форме там не определён, а сам приём сигнала процессом службы устроен
// иначе (CTRL_CLOSE_EVENT/CTRL_LOGOFF_EVENT/CTRL_SHUTDOWN_EVENT,
// runtime/os_windows.go переводит их в syscall.SIGTERM — см. doc-комментарий
// graph_signal_windows.go). Ручная проверка на боевой Windows-машине:
// `mcp1c graph --project <корень>`, затем закрыть консольное окно или
// послать `taskkill /F` — процесс обязан завершиться без зависания.
func TestSIGTERMCancelsGraphContext(t *testing.T) {
	ctx, stop := signal.NotifyContext(context.Background(), graphShutdownSignals()...)
	defer stop()

	go func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Errorf("Kill(SIGTERM): %v", err)
		}
	}()

	select {
	case <-ctx.Done():
		// SIGTERM дошёл и отменил контекст — та же реакция, что у Ctrl+C.
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM не отменил контекст за 3с — additionalShutdownSignals не доехал до signal.NotifyContext")
	}
}

// TestGraphSubcommandSIGTERMGracefulShutdown — сквозной e2e, второй вариант
// закрытия дефекта (ревью качества, «если общая функция неудобна — образец
// buildMCP1C + exec.CommandContext уже есть»). Не вместо TestSIGTERMCancelsGraphContext,
// а вместе: тот тест зовёт graphShutdownSignals() напрямую и не заметит,
// если runGraph когда-нибудь ПЕРЕСТАНЕТ звать именно её (общий символ не
// защищает от удаления своего единственного вызова, только от переписанной
// копии выражения) — этот тест реально запускает mcp1c graph подпроцессом
// (тот же bin, что строит buildMCP1C, cmd/mcp1c/main_dispatch_test.go),
// шлёт РЕАЛЬНЫЙ SIGTERM самому подпроцессу и проверяет, что runGraph
// завершается сам, кодом 0, в пределах graphShutdownTimeout — то есть
// проверяет то, что происходит через реальный вызов, а не переизложение
// логики. Фикстура — минимальный валидный проект без реального reindex:
// runGraph отказывает, только если store.Open встречает ошибку, а не
// «данных нет» (пустая, но опубликованная эпоха — законный ответ, тот же
// путь, что описан в internal/store/store.go:Open).
func TestGraphSubcommandSIGTERMGracefulShutdown(t *testing.T) {
	bin := buildMCP1C(t)

	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	metaWriteFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), metaConfigurationXML("Тест"))
	metaFixtureProject(t, workspaceRoot, projectRoot, "graph-sigterm-e2e")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "graph", "--project", workspaceRoot, "--listen", "127.0.0.1:0", "--no-open")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	urlCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); strings.HasPrefix(line, "http://") {
				urlCh <- line
				return
			}
		}
		urlCh <- ""
	}()

	var serverURL string
	select {
	case serverURL = <-urlCh:
	// A ceiling, not an expectation: alone the server prints its URL in about
	// a second, but a full parallel `go test ./...` on a loaded machine or CI
	// runner delayed it past the former 10s and failed an otherwise healthy run.
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("сервер не напечатал URL за 60с; stderr: %s", stderr.String())
	}
	if serverURL == "" {
		cmd.Process.Kill()
		t.Fatalf("сервер завершился, не напечатав URL; stderr: %s", stderr.String())
	}

	// Сервер реально принимает запросы, прежде чем его гасить — иначе тест
	// мог бы «пройти» и на сервере, который напечатал URL и тут же упал.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, getErr := http.Get(serverURL + "api/projects")
		if getErr == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatalf("сервер не ответил на /api/projects: %v", getErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM): %v", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	budget := graphShutdownTimeout + 5*time.Second
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("процесс после SIGTERM завершился с ошибкой (%v) вместо чистого кода 0; stderr: %s", err, stderr.String())
		}
	case <-time.After(budget):
		cmd.Process.Kill()
		t.Fatalf("процесс не завершился за %s после SIGTERM — SIGTERM не гасит runGraph так же корректно, как Ctrl+C; stderr: %s", budget, stderr.String())
	}
}
