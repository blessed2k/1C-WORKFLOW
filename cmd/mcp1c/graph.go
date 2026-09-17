package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/graphweb"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// graphShutdownTimeout — сколько graceful shutdown ждёт завершения уже
// идущих HTTP-запросов после SIGINT/SIGTERM, прежде чем Serve вернётся.
const graphShutdownTimeout = 5 * time.Second

// graphShutdownSignals — полный список сигналов, которые обязаны погасить
// graph-режим так же корректно, как Ctrl+C (D04 манифеста): os.Interrupt
// (SIGINT) плюс всё, что называет additionalShutdownSignals (SIGTERM,
// platform-файл с ОДИНАКОВЫМ телом на unix/Windows — graph_signal_unix.go/
// graph_signal_windows.go, образец cmd/mcp1c/rss_*.go). Единственный вызов
// этого выражения: runGraph строит sigCtx через него же, и
// graph_signal_unix_test.go проверяет доставку SIGTERM через НЕГО ЖЕ, а не
// через переписанную копию — ревью качества поймало ровно этот класс
// дефекта (byCap/byFetch на таске 08): тест-копия выражения не защищает
// продакшен-строку, которая реально решает, дойдёт ли сигнал до сервера.
func graphShutdownSignals() []os.Signal {
	return append([]os.Signal{os.Interrupt}, additionalShutdownSignals()...)
}

// projectRootList — повторяемый флаг --project: flag.Var копит все значения
// в порядке появления, тем же приёмом, каким стандартная библиотека сама не
// снабжает флаги (нет builtin repeat-типа).
type projectRootList []string

func (l *projectRootList) String() string { return strings.Join(*l, ",") }

func (l *projectRootList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("--project не может быть пустым")
	}
	*l = append(*l, v)
	return nil
}

// runGraph — точка входа подкоманды `mcp1c graph` (spec §6, тикет 09):
// поднимает отдельный HTTP-процесс поверх internal/app.ObjectGraphService
// (тикет 08) и internal/graphweb (этот тикет). Возвращает код возврата
// процесса; main() зовёт os.Exit(runGraph(os.Args[2:])) — сам он ничего не
// печатает и не завершает процесс, чтобы остаться тестируемым в памяти без
// exec.Command.
func runGraph(args []string) int {
	fs := flag.NewFlagSet("mcp1c graph", flag.ContinueOnError)
	var projectRoots projectRootList
	fs.Var(&projectRoots, "project", "корень проекта (workspace с уже собранным индексом); флаг повторяемый")
	listen := fs.String("listen", "127.0.0.1:0", "адрес, на котором слушать — обязан быть на 127.0.0.1")
	noOpen := fs.Bool("no-open", false, "не открывать карту в браузере автоматически")
	var opts options
	registerGraphFlags(fs, &opts)
	registerSyntaxIndexFlag(fs, &opts) // даёт -graph-radius-nodes; остальные четыре здесь не читаются (таск 07 их зона), но флаг остаётся тем же именем, что у обычного сервера

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(projectRoots) == 0 {
		fmt.Fprintln(os.Stderr, "mcp1c graph: нужен хотя бы один --project <корень>")
		return 2
	}

	host, _, splitErr := net.SplitHostPort(*listen)
	if splitErr != nil {
		fmt.Fprintf(os.Stderr, "mcp1c graph: --listen %q: %v\n", *listen, splitErr)
		return 2
	}
	if host != "127.0.0.1" {
		// R17: «слушает только 127.0.0.1, привязка к 0.0.0.0 невозможна» —
		// проверяется здесь, ДО net.Listen, а не молчаливой заменой хоста:
		// заменить на 127.0.0.1 за спиной значило бы дать пройти опечатке
		// "--listen 0.0.0.0:8080" без единого сообщения.
		fmt.Fprintf(os.Stderr, "mcp1c graph: --listen %q: разрешён только адрес 127.0.0.1 (получено %q)\n", *listen, host)
		return 2
	}

	ctx := context.Background()
	builtins := syntax.NewLazy(opts.syntaxIndexPath())
	newProjects := newProjectsFactory(builtins)

	handles, code := openGraphProjects(ctx, dedupeRoots(projectRoots), newProjects, opts.graphRadiusNodes)
	if code != 0 {
		return code
	}

	ln, listenErr := net.Listen("tcp", *listen)
	if listenErr != nil {
		fmt.Fprintf(os.Stderr, "mcp1c graph: порт %s занят: %v\n", *listen, listenErr)
		return 1
	}

	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	fmt.Println(url)
	// --no-open существует по контракту (spec §6, критерий тикета 09), но
	// автоматическое открытие браузера здесь не реализовано — упрощение,
	// названо явно, а не тихо пропущено:
	//   - nosubprocess_test.go делает «сервер не форкает ничего, ни на одной
	//     платформе» инвариантом ВСЕГО cmd/mcp1c (проверено этим же прогоном:
	//     первая попытка через os/exec.Command его красит), а открыть
	//     браузер без внешнего процесса в Go нечем;
	//   - потолок этого упрощения: URL печатается всегда, пользователь копирует
	//     его сам; апгрейд — отдельная мелкая задача, которая либо заведёт
	//     платформенный файл под исключение из nosubprocess_test.go (по
	//     образцу rss_*.go), либо примет subprocess как осознанное
	//     расширение инварианта — решение не этого тикета.
	if !*noOpen {
		fmt.Fprintln(os.Stderr, "mcp1c graph: автоматическое открытие браузера не реализовано — откройте адрес выше вручную")
	}

	// graphShutdownSignals — os.Interrupt (SIGINT/Ctrl+C) плюс SIGTERM (D04
	// манифеста, поправка оркестратора): вынесено в именованную функцию и
	// вызывается ОТСЮДА И из graph_signal_unix_test.go — ревью качества
	// поймало, что копия того же выражения в теле теста не проверяет ЭТУ
	// строку продакшена (мутация: убрать второй аргумент здесь оставляла
	// тест зелёным). Общий вызов делает такую мутацию невозможной: та же
	// функция, тот же результат в проде и в тесте.
	sigCtx, stop := signal.NotifyContext(ctx, graphShutdownSignals()...)
	defer stop()

	handler := graphweb.NewHandler(handles)
	if err := graphweb.Serve(sigCtx, ln, handler, graphShutdownTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "mcp1c graph: %v\n", err)
		return 1
	}
	return 0
}

// newProjectsFactory строит фабрику app.Projects, не называя в исходнике
// cmd/mcp1c тип internal/index.Config (RuleCmdThroughApp, internal/arch):
// app.DefaultIndexConfig() вызывается один раз здесь и захватывается
// замыканием — тот же приём, каким уже пользуется newIndexToolDeps
// (cmd/mcp1c/indexreg.go) для обычного сервера.
func newProjectsFactory(builtins *syntax.Index) func(root string) (*app.Projects, error) {
	idxCfg := app.DefaultIndexConfig()
	return func(root string) (*app.Projects, error) {
		return app.NewProjects(root, builtins, idxCfg)
	}
}

// dedupeRoots убирает повторные --project с одним и тем же (после
// приведения к абсолютному пути) корнем: без этого /api/projects показал бы
// один и тот же проект дважды под одним и тем же ID. Порядок первого
// появления сохраняется — он же порядок в /api/projects.
func dedupeRoots(roots []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		key := root
		if abs, err := filepath.Abs(root); err == nil {
			key = abs
		}
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, root)
	}
	return out
}

// openGraphProjects валидирует и открывает каждый --project корень ДО того,
// как поднят слушатель (spec §6: «индекса нет — ошибка с точной командой
// reindex и ненулевым кодом возврата, ничего не индексируя»). Возвращает
// ненулевой код при первом отказе — частично поднятая карта с половиной
// проектов хуже честного отказа целиком.
func openGraphProjects(ctx context.Context, roots []string, newProjects func(string) (*app.Projects, error), radiusCap int) ([]graphweb.ProjectHandle, int) {
	handles := make([]graphweb.ProjectHandle, 0, len(roots))
	for _, root := range roots {
		info, statErr := os.Stat(root)
		if statErr != nil {
			fmt.Fprintf(os.Stderr, "mcp1c graph: каталог проекта %q не найден: %v\n", root, statErr)
			return nil, 1
		}
		if !info.IsDir() {
			fmt.Fprintf(os.Stderr, "mcp1c graph: путь %q не каталог\n", root)
			return nil, 1
		}

		root := root
		open := func(ctx context.Context) (*app.Projects, error) { return newProjects(root) }

		p, err := open(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp1c graph: проект %s: %v\n", root, err)
			return nil, 1
		}
		op, err := p.Active(ctx)
		if err != nil {
			p.Close()
			// noActiveProjectError (internal/app/projects.go) уже называет
			// точную команду reindex в подсказке — переспрашивать нечего.
			fmt.Fprintf(os.Stderr, "mcp1c graph: проект %s: %v\n", root, err)
			return nil, 1
		}
		id := op.Entry.ID
		p.Close()

		handles = append(handles, graphweb.ProjectHandle{
			ID: id, Root: root, RadiusNodesCap: radiusCap, Open: open,
		})
	}
	return handles, 0
}
