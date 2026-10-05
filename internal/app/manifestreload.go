package app

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// manifestChange: чем манифест на диске отличается от снятого при открытии
// проекта, по компонентам.
type manifestChange struct {
	Added   []domain.ComponentID
	Removed []domain.ComponentID
	Changed []domain.ComponentID
}

func (c manifestChange) empty() bool {
	return len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Changed) == 0
}

// needsFull: инкремент идёт по компонентам нового манифеста и факты убранного
// компонента не снимает, а у изменённого (другой корень, порядок применения,
// шаблоны) оставил бы факты прежнего описания. Чисто их убирает только новая
// эпоха.
func (c manifestChange) needsFull() bool {
	return len(c.Removed) > 0 || len(c.Changed) > 0
}

// warning называет изменение в ответе reindex: человек правил манифест и
// должен видеть, что правка применилась и почему прогон стал полным.
func (c manifestChange) warning(forcedFull bool) Warning {
	var parts []string
	for _, group := range []struct {
		title string
		ids   []domain.ComponentID
	}{{"добавлены", c.Added}, {"убраны", c.Removed}, {"изменены", c.Changed}} {
		if len(group.ids) == 0 {
			continue
		}
		names := make([]string, len(group.ids))
		for i, id := range group.ids {
			names[i] = string(id)
		}
		parts = append(parts, group.title+" "+strings.Join(names, ", "))
	}
	w := Warning{
		Code:    "manifest_reloaded",
		Message: "состав компонентов в 1c-project.json изменился: " + strings.Join(parts, "; ") + ". Индексация идёт по новому составу",
	}
	if forcedFull {
		w.Hint = "прогон выполнен полной пересборкой по всем компонентам: инкремент не снимает факты убранного или изменённого компонента"
	}
	return w
}

// diffManifests сравнивает компоненты двух манифестов. Смена общих шаблонов
// exclude касается каждого компонента, который остался.
func diffManifests(old, next workspace.Manifest) manifestChange {
	var change manifestChange
	excludeChanged := !reflect.DeepEqual(old.Exclude, next.Exclude)
	for _, c := range next.Components {
		prev, ok := old.Component(c.ID)
		switch {
		case !ok:
			change.Added = append(change.Added, c.ID)
		case excludeChanged || !reflect.DeepEqual(prev, c):
			change.Changed = append(change.Changed, c.ID)
		}
	}
	for _, c := range old.Components {
		if _, ok := next.Component(c.ID); !ok {
			change.Removed = append(change.Removed, c.ID)
		}
	}
	return change
}

// reloadManifest перечитывает 1c-project.json открытого проекта перед reindex.
// Манифест снимается при открытии проекта и живёт в паре store+пайплайн до
// конца процесса, поэтому правка состава компонентов раньше требовала
// перезапуска сервера.
//
// При изменении пайплайн пересоздаётся над тем же хранилищем: это то же, что
// даёт перезапуск, а хранилище не закрывается, и вызовы, уже державшие прежнюю
// пару, дочитывают свои транзакции. Прежний пайплайн останавливается до
// возврата, чтобы его отложенная пересборка не пошла по старому манифесту.
//
// Пара без файла манифеста (собрана тестовой обвязкой над чужой выгрузкой)
// не перечитывается: читать нечего.
func (p *Projects) reloadManifest(op *openProject) (*openProject, manifestChange, error) {
	if op.Manifest.Path == "" {
		return op, manifestChange{}, nil
	}
	manifest, err := workspace.LoadManifest(op.Entry.Root)
	if err != nil {
		return nil, manifestChange{}, fmt.Errorf("проект %s: манифест %s: %w", op.Entry.ID, op.Entry.Root, err)
	}

	p.mu.Lock()
	current, ok := p.opened[op.Entry.ID]
	if !ok || p.closed {
		p.mu.Unlock()
		return nil, manifestChange{}, fmt.Errorf("проект %s: реестр проектов уже закрыт", op.Entry.ID)
	}
	if reflect.DeepEqual(manifest, current.Manifest) {
		p.mu.Unlock()
		return current, manifestChange{}, nil
	}
	change := diffManifests(current.Manifest, manifest)
	next := &openProject{
		Entry: current.Entry, Manifest: manifest, Store: current.Store,
		Service: index.NewService(current.Store, current.Entry.ID, manifest, p.builtins, p.idxCfg),
	}
	p.opened[op.Entry.ID] = next
	p.mu.Unlock()

	if err := current.Service.Close(); err != nil {
		return nil, manifestChange{}, fmt.Errorf("проект %s: остановка прежнего пайплайна: %w", op.Entry.ID, err)
	}
	return next, change, nil
}
