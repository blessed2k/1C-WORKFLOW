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

// openProject открывает проект в рабочем каталоге оценки (app.OpenStandalone).
func openProject(ctx context.Context, root, workspace, syntaxIndex string, reindex bool) (*project, error) {
	if root == "" {
		return nil, fmt.Errorf("каталог проекта не задан: флаг -project или переменная ONEC_DUMP")
	}
	t0 := time.Now()
	projects, indexed, err := app.OpenStandalone(ctx, app.StandaloneOptions{Root: root, Workspace: workspace, SyntaxIndex: syntaxIndex, Reindex: reindex})
	if err != nil {
		return nil, err
	}
	if indexed {
		log.Printf("индекс построен: %s", time.Since(t0).Round(time.Second))
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
