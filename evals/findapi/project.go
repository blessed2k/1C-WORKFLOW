package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// project: проект с открытым индексом и сервисами, которыми пользуется
// оценка. Сервисы те же, что стоят за инструментами сервера: оценка меряет
// ровно тот ответ, который получит агент.
type project struct {
	root     string
	projects *app.Projects
	api      *app.APIService
	graph    *app.GraphService
}

// defaultWorkspace: где оценка держит свой индекс между запусками. Каталог
// отдельный от рабочего каталога сервера: два процесса, пишущие в один индекс,
// друг друга не ждут.
func defaultWorkspace() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "mcp1c-evals")
}

func defaultSyntaxIndex() string {
	if p := os.Getenv(syntax.EnvPath); p != "" {
		return p
	}
	return syntax.DefaultPath()
}

// openProject открывает проект в рабочем каталоге оценки. Проект, которого
// там ещё нет, регистрируется и индексируется целиком (на типовой
// конфигурации это минуты).
//
// упрощение: уже проиндексированный проект открывается без сверки индекса с
// выгрузкой: сверка на той же конфигурации стоит столько же, сколько полная
// индексация, а выгрузка между прогонами оценки не меняется. После правки
// выгрузки и после смены схемы индекса его пересобирает reindex=true; от прогона на чужой выгрузке
// страхует сверка каталога с базой (compare).
func openProject(ctx context.Context, root, workspace, syntaxIndex string, reindex bool) (*project, error) {
	if root == "" {
		return nil, fmt.Errorf("каталог проекта не задан: флаг -project или переменная ONEC_DUMP")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("рабочий каталог оценки %s: %w", workspace, err)
	}
	projects, err := app.NewProjects(workspace, syntax.NewLazy(syntaxIndex), app.DefaultIndexConfig())
	if err != nil {
		return nil, err
	}
	registered, err := projects.EnsureProjectActive(ctx, root)
	if err != nil {
		projects.Close()
		return nil, err
	}
	if registered || reindex {
		// Пересборка всегда полная: сверка с выгрузкой на типовой конфигурации
		// не быстрее, а индекс прежней схемы обновить может только она.
		in := app.ReindexInput{ProjectRoot: root, Mode: "full"}
		t0 := time.Now()
		resp, err := app.NewIndexStatusService(projects).Reindex(ctx, in)
		if err != nil {
			projects.Close()
			return nil, fmt.Errorf("индексация %s: %w", root, err)
		}
		mode := ""
		if len(resp.Items) > 0 {
			mode = resp.Items[0].Mode
		}
		log.Printf("индекс построен: режим %s, %s", mode, time.Since(t0).Round(time.Second))
	}
	log.Printf("проект %s, рабочий каталог оценки %s", root, workspace)
	return &project{
		root: root, projects: projects,
		api: app.NewAPIService(projects), graph: app.NewGraphService(projects),
	}, nil
}

func (p *project) close() { p.projects.Close() }

// readModule читает модуль с диска по пути из индекса.
//
// упрощение: путь модуля считается от корня проекта, то есть проект состоит
// из одного компонента с корнем ".". Так устроена выгрузка, на которой набор
// строится; проекту с расширениями понадобится корень компонента из манифеста.
func (p *project) readModule(rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(p.root, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	// Выгрузка пишет модули с меткой порядка байтов.
	return strings.TrimPrefix(string(data), string(rune(0xFEFF))), nil
}
