package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// ActiveProjectState: снимок одного состояния процесса «активный проект»
// (решение D2, docs/architecture-graph.md §4.1): каталог выгрузки, который
// читают raw-инструменты, и индексный проект, который читают индексные.
// Раньше это были два независимых состояния (dumpState в cmd/mcp1c и
// активный проект реестра), и сервер отвечал по разным проектам, нигде
// этого не показывая.
//
// app хранит только строку каталога: XMLSource строит cmd/mcp1c, потому что
// internal/app не имеет права опираться на internal/source
// (internal/arch.CheckLegacyIsolation).
type ActiveProjectState struct {
	// DumpDir: каталог выгрузки для raw-инструментов, в том написании, в
	// котором его задали (set_dump, --dump) или вывели из манифеста.
	DumpDir string
	// Project: индексный проект процесса; пусто, если его нет.
	Project domain.ProjectID
	// ProjectRoot: каталог с 1c-project.json (для временной записи: её корень).
	ProjectRoot string
	// Bound: raw и индекс указывают на один проект.
	Bound bool
	// Hint: что сделать, когда Bound=false.
	Hint string
}

// activeState: поля состояния под Projects.activeMu. Нулевое значение
// значит «ещё не решено»: правило старта без выгрузки применяется лениво при
// первом обращении, чтобы конструкторы, собирающие Projects литералом
// (impact_testsupport.go), и порядок «реестр заполнили после NewProjects»
// продолжали работать.
type activeState struct {
	decided     bool
	dumpDir     string
	project     domain.ProjectID
	projectRoot string
	// note: почему пара не связана, хотя оба поля заполнены (у проекта нет
	// компонентов, выгрузка осталась прежней) или почему выгрузка не
	// привязалась (манифесты, которые не прочитались).
	note string
	// foreignDump: dumpDir не принадлежит project.
	foreignDump bool
}

// SetDump делает dir raw-источником процесса и привязывает к нему индексный
// проект: тот зарегистрированный проект, у которого компонент (конфигурация
// или расширение) лежит в этом каталоге. Если такого нет, индексного проекта в
// процессе нет, даже если в реестре сохранён активный (иначе индекс отвечал бы
// про другой проект). registry.json не переписывается, он общий для процессов.
//
// Существование Configuration.xml здесь не проверяется: это делает
// вызывающий (set_dump), а старт с --dump исторически принимает каталог как есть.
func (p *Projects) SetDump(dir string) ActiveProjectState {
	id, root, note, ok := p.bindDump(dir)
	p.activeMu.Lock()
	p.active = activeState{decided: true, dumpDir: dir}
	if ok {
		p.active.project, p.active.projectRoot = id, root
	} else {
		p.active.note = note
	}
	state := p.snapshotLocked()
	p.activeMu.Unlock()
	if ok && p.registry != nil {
		if entry, found := p.registry.Project(id); found {
			p.warmWG.Add(1)
			go p.warmProject(entry)
		}
	}
	return state
}

// warmProject открывает привязанный проект заранее, при set_dump и старте с
// --dump: открытие запускает фоновый обход диска (ADR-036), и первый вызов
// индексного инструмента не платит его целиком. В фоне, потому что open
// держит mu на всё время store.Open, а SetDump его ждать не должен. Отказ
// открытия здесь не сообщается: тот же отказ честно вернёт первый вызов
// инструмента.
func (p *Projects) warmProject(entry workspace.ProjectEntry) {
	defer p.warmWG.Done()
	_, _ = p.open(context.Background(), entry)
}

// DumpDir: каталог выгрузки для raw-инструментов; пусто, если его нет.
func (p *Projects) DumpDir() string {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	p.ensureDecidedLocked()
	return p.active.dumpDir
}

// ActiveState возвращает снимок состояния для server_info.
func (p *Projects) ActiveState() ActiveProjectState {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	p.ensureDecidedLocked()
	return p.snapshotLocked()
}

// activeProjectID отдаёт индексный проект процесса и каталог выгрузки одним
// чтением, чтобы Active объяснял отказ по той же паре, что видит server_info.
func (p *Projects) activeProjectID() (domain.ProjectID, string, string) {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	p.ensureDecidedLocked()
	return p.active.project, p.active.dumpDir, p.active.note
}

// activateInProcess делает проект активным в процессе и переключает raw на
// его выгрузку (reindex projectRoot). manifest может быть nil: тогда он
// читается с диска. Если у проекта нет ни одного компонента, прежняя
// выгрузка остаётся, а пара считается несвязанной.
func (p *Projects) activateInProcess(entry workspace.ProjectEntry, manifest *workspace.Manifest) {
	l := p.layoutOf(entry, manifest)
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	next := activeState{decided: true, dumpDir: l.rawDir(), project: entry.ID, projectRoot: l.root}
	if next.dumpDir == "" && l.err == nil {
		next.dumpDir = p.active.dumpDir
		next.foreignDump = next.dumpDir != ""
		next.note = fmt.Sprintf("у проекта %s в манифесте нет ни одного компонента: raw-инструменты читают прежнюю выгрузку, добавьте компонент в 1c-project.json или выберите выгрузку через set_dump", entry.ID)
	}
	p.active = next
}

// ensureDecidedLocked применяет правило старта без выгрузки. Берётся
// сохранённый в реестре активный проект, а если его нет, но проект зарегистрирован ровно один, то
// он (D2 в docs/architecture-graph.md: единственный проиндексированный проект активен и для raw).
func (p *Projects) ensureDecidedLocked() {
	if p.active.decided {
		return
	}
	p.active.decided = true
	if p.registry == nil {
		return
	}
	entry, ok := p.registry.ActiveProject()
	if !ok {
		if all := p.registry.RecentProjects(); len(all) == 1 {
			entry, ok = all[0], true
		}
	}
	if !ok {
		return
	}
	l := p.layoutOf(entry, nil)
	p.active.project, p.active.projectRoot, p.active.dumpDir = entry.ID, l.root, l.rawDir()
}

// entryLayout: корень проекта и каталоги его компонентов в порядке манифеста.
type entryLayout struct {
	root      string
	dirs      []string
	configDir string
	err       error // манифест обычной записи не прочитался
}

// rawDir: каталог для raw-инструментов. Конфигурация, если она есть, иначе
// первый компонент (расширение или исходники тоже читаются как выгрузка).
func (l entryLayout) rawDir() string {
	if l.configDir != "" {
		return l.configDir
	}
	if len(l.dirs) > 0 {
		return l.dirs[0]
	}
	return ""
}

// layoutOf выводит раскладку записи. Временная запись без манифеста сама и
// есть выгрузка, это единственный случай, когда в счёт идёт Root записи.
func (p *Projects) layoutOf(entry workspace.ProjectEntry, manifest *workspace.Manifest) entryLayout {
	if entry.Temporary {
		return entryLayout{root: entry.Root, dirs: []string{entry.Root}, configDir: entry.Root}
	}
	var m workspace.Manifest
	if manifest != nil {
		m = *manifest
	} else {
		loaded, err := manifestOf(entry)
		if err != nil {
			return entryLayout{root: entry.Root, err: err}
		}
		m = loaded
	}
	l := entryLayout{root: m.Root}
	if l.root == "" {
		l.root = entry.Root
	}
	for _, c := range m.Components {
		l.dirs = append(l.dirs, c.AbsRoot)
	}
	if c, ok := m.Configuration(); ok {
		l.configDir = c.AbsRoot
	}
	return l
}

// manifestOf читает манифест с диска. Кэш открытых проектов (и его мьютекс
// mu) здесь нарочно не используется: open() держит mu всё время store.Open,
// и привязка выгрузки, server_info и set_dump ждали бы открытия индекса.
// Заодно привязка видит текущий манифест, а не снятый при открытии.
func manifestOf(entry workspace.ProjectEntry) (workspace.Manifest, error) {
	return workspace.LoadManifest(entry.Root)
}

// bindDump ищет зарегистрированный проект, которому принадлежит каталог
// выгрузки: канонический путь совпадает с корнем компонента манифеста, а для
// временной записи с её Root. Манифесты читаются здесь, при привязке, а не на
// каждый вызов инструмента. note перечисляет записи, чей манифест не
// прочитался: они не привязываются, и пользователю надо знать почему.
func (p *Projects) bindDump(dir string) (id domain.ProjectID, root, note string, ok bool) {
	if p.registry == nil || strings.TrimSpace(dir) == "" {
		return "", "", "", false
	}
	canon, err := canonicalRoot(dir)
	if err != nil {
		return "", "", "", false
	}
	var broken []string
	for _, e := range p.registry.RecentProjects() {
		l := p.layoutOf(e, nil)
		if l.err != nil {
			broken = append(broken, fmt.Sprintf("манифест проекта %s не прочитан: %v", e.ID, l.err))
			continue
		}
		for _, d := range l.dirs {
			if equalRootPath(d, canon) {
				return e.ID, l.root, "", true
			}
		}
	}
	return "", "", strings.Join(broken, "; "), false
}

// canonicalRoot приводит каталог к абсолютному пути с раскрытыми симлинками:
// одна нормализация для реестра, привязки выгрузки и project=.
func canonicalRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("путь не приводится к абсолютному: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("каталог не читается: %w", err)
	}
	return real, nil
}

// snapshotLocked собирает снимок и подсказку под уже взятым activeMu.
func (p *Projects) snapshotLocked() ActiveProjectState {
	st := ActiveProjectState{
		DumpDir:     p.active.dumpDir,
		Project:     p.active.project,
		ProjectRoot: p.active.projectRoot,
	}
	st.Bound = st.Project != "" && st.DumpDir != "" && !p.active.foreignDump
	switch {
	case p.registry == nil && st.Project == "":
		st.Hint = p.noActiveProjectError().Hint
	case st.Project == "" && st.DumpDir != "":
		st.Hint = unboundDumpHint(st.DumpDir, p.active.note)
	case st.Project == "":
		st.Hint = p.noActiveProjectError().Hint
	case p.active.note != "":
		st.Hint = p.active.note
	case st.DumpDir == "":
		st.Hint = fmt.Sprintf("у проекта %s не нашлось каталога выгрузки (манифест не прочитан): raw-инструментам читать нечего, выберите выгрузку через set_dump", st.Project)
	}
	return st
}

// unboundDumpHint: подсказка, когда выгрузка задана, а проекта для неё нет.
func unboundDumpHint(dump, note string) string {
	hint := fmt.Sprintf("выгрузка %s не описана ни в одном зарегистрированном проекте: вызовите reindex projectRoot=<каталог с 1c-project.json>, в манифесте которого эта выгрузка указана компонентом (без манифеста индекс не строится)", dump)
	if note != "" {
		hint += "; " + note
	}
	return hint
}

// unboundDumpError: no_active_project для индексных инструментов в том же случае.
func unboundDumpError(dump, note string) *Error {
	return NewError(CodeNoActiveProject, "нет индексного проекта для активной выгрузки", unboundDumpHint(dump, note))
}

// UseProject делает зарегистрированный проект активным в этом процессе, не
// переписывая registry.json: graph-режим открывает конкретный проект
// workspace (--project <workspace>#<id>), когда их в реестре несколько (D3 в
// docs/architecture-graph.md, веха В2: две базы одного workspace на одной карте).
func (p *Projects) UseProject(id domain.ProjectID) error {
	if p.registry == nil {
		return p.noActiveProjectError()
	}
	entry, ok := p.registry.Project(id)
	if !ok {
		return NewError(CodeNotFound, fmt.Sprintf("проект %s не зарегистрирован в workspace", id),
			"зарегистрируйте его через reindex(projectRoot=...) или проверьте id в .mcp1c/registry.json")
	}
	p.activateInProcess(entry, nil)
	return nil
}
