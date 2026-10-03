package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// StandaloneOptions: как открыть проект вне сервера.
type StandaloneOptions struct {
	// Root: каталог проекта с 1c-project.json.
	Root string
	// Workspace: рабочий каталог, где живёт индекс (то же, что --projects-root
	// у сервера). Программе вне сервера нужен свой: два процесса, пишущие в
	// один индекс, друг друга не ждут.
	Workspace string
	// SyntaxIndex: индекс синтаксиса платформы (cmd/syntaxgen).
	SyntaxIndex string
	// Reindex: пересобрать индекс целиком.
	Reindex bool
}

// DefaultStandaloneWorkspace: рабочий каталог программ вне сервера (оценка
// качества, генераторы): каталог кэша пользователя, отдельно от рабочего
// каталога сервера.
func DefaultStandaloneWorkspace() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "mcp1c-evals")
}

// DefaultSyntaxIndexPath: где искать индекс синтаксиса платформы: переменная
// окружения сервера, затем путь по умолчанию.
func DefaultSyntaxIndexPath() string {
	if p := os.Getenv(syntax.EnvPath); p != "" {
		return p
	}
	return syntax.DefaultPath()
}

// OpenStandalone открывает проект для программы вне сервера: оценки качества
// или генератора, которым нужны те же сервисы, что стоят за инструментами.
// Проект, которого в рабочем каталоге ещё нет, регистрируется и индексируется
// целиком (на типовой конфигурации это минуты). indexed сообщает, что индекс
// строился в этом вызове.
//
// упрощение: уже проиндексированный проект открывается без сверки индекса с
// выгрузкой: сверка на типовой конфигурации стоит столько же, сколько полная
// индексация, а выгрузка между запусками таких программ не меняется. После
// правки выгрузки и после смены схемы индекса его пересобирает Reindex.
func OpenStandalone(ctx context.Context, opt StandaloneOptions) (p *Projects, indexed bool, err error) {
	if strings.TrimSpace(opt.Root) == "" {
		return nil, false, fmt.Errorf("каталог проекта не задан")
	}
	if err := os.MkdirAll(opt.Workspace, 0o755); err != nil {
		return nil, false, fmt.Errorf("рабочий каталог %s: %w", opt.Workspace, err)
	}
	p, err = NewProjects(opt.Workspace, syntax.NewLazy(opt.SyntaxIndex), DefaultIndexConfig())
	if err != nil {
		return nil, false, err
	}
	registered, err := p.EnsureProjectActive(ctx, opt.Root)
	if err != nil {
		p.Close()
		return nil, false, err
	}
	if !registered && !opt.Reindex {
		return p, false, nil
	}
	// Пересборка всегда полная: сверка с выгрузкой не быстрее, а индекс
	// прежней схемы обновить может только она.
	if _, err := NewIndexStatusService(p).Reindex(ctx, ReindexInput{ProjectRoot: opt.Root, Mode: "full"}); err != nil {
		p.Close()
		return nil, false, fmt.Errorf("индексация %s: %w", opt.Root, err)
	}
	return p, true, nil
}
