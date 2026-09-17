package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

const (
	// RegistryDirName — каталог сгенерированного состояния сервера. Целиком в
	// .gitignore, исключён из индексации, удаление ведёт только к перестроению.
	RegistryDirName = ".mcp1c"
	// RegistryFileName — файл локального реестра внутри RegistryDirName.
	RegistryFileName = "registry.json"
	// RegistryVersion — версия формата реестра.
	RegistryVersion = 1
)

// ProjectEntry — что сервер помнит о проекте между запусками: где он лежит, где
// его индекс, на каком поколении остановился и какие эпохи ждут удаления.
type ProjectEntry struct {
	ID            domain.ProjectID  `json:"id"`
	Root          string            `json:"root"`
	ManifestPath  string            `json:"manifestPath,omitempty"`
	IndexDir      string            `json:"indexDir,omitempty"`
	Epoch         uint64            `json:"epoch,omitempty"`
	Generation    domain.Generation `json:"generation,omitempty"`
	RetiredEpochs []uint64          `json:"retiredEpochs,omitempty"`
	// Temporary — проект, зарегистрированный на лету (set_dump), без манифеста.
	Temporary  bool      `json:"temporary,omitempty"`
	LastOpened time.Time `json:"lastOpened"`
}

// RegistryState — содержимое registry.json.
type RegistryState struct {
	Version       int              `json:"version"`
	ActiveProject domain.ProjectID `json:"activeProject,omitempty"`
	Projects      []ProjectEntry   `json:"projects"`
}

// Registry — локальный реестр .mcp1c/registry.json.
//
// Реестр ведёт сервер: манифест он не меняет никогда. Запись атомарна (temp +
// rename), поэтому читатель — в том числе другой процесс — видит либо старое
// содержимое целиком, либо новое целиком.
type Registry struct {
	dir  string // каталог .mcp1c
	path string // файл registry.json

	mu    sync.Mutex
	state RegistryState
}

// ErrProjectNotFound — обращение к проекту, которого нет в реестре.
var ErrProjectNotFound = errors.New("проект не зарегистрирован")

// OpenRegistry открывает (и при необходимости создаёт) реестр в каталоге dir:
// файл ложится в dir/.mcp1c/registry.json.
func OpenRegistry(dir string) (*Registry, error) {
	if dir == "" {
		return nil, fmt.Errorf("каталог реестра не задан")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("каталог реестра %q: %w", dir, err)
	}
	r := &Registry{
		dir:   filepath.Join(abs, RegistryDirName),
		state: RegistryState{Version: RegistryVersion},
	}
	r.path = filepath.Join(r.dir, RegistryFileName)
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Path возвращает путь к файлу реестра.
func (r *Registry) Path() string { return r.path }

// Dir возвращает каталог .mcp1c, которым владеет реестр.
func (r *Registry) Dir() string { return r.dir }

// Reload перечитывает файл реестра с диска. Отсутствие файла — не ошибка:
// реестр появляется при первой записи.
func (r *Registry) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloadLocked()
}

// reloadLocked перечитывает файл; вызывается под уже взятым замком.
func (r *Registry) reloadLocked() error {
	var data []byte
	err := повторитьПриЗанятомФайле(func() error {
		var err error
		data, err = os.ReadFile(r.path)
		return err
	})
	if errors.Is(err, os.ErrNotExist) {
		r.state = RegistryState{Version: RegistryVersion}
		return nil
	}
	if err != nil {
		return fmt.Errorf("чтение %s: %w", r.path, err)
	}
	var state RegistryState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("разбор %s: %w (удалите каталог %s, он пересоздаётся)", r.path, err, RegistryDirName)
	}
	if state.Version != RegistryVersion {
		return fmt.Errorf("%s: версия реестра %d не поддерживается, ожидалась %d", r.path, state.Version, RegistryVersion)
	}
	r.state = state
	return nil
}

// State возвращает копию состояния реестра.
func (r *Registry) State() RegistryState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.clone()
}

// Project возвращает запись о проекте.
func (r *Registry) Project(id domain.ProjectID) (ProjectEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.state.Projects {
		if p.ID == id {
			return p.clone(), true
		}
	}
	return ProjectEntry{}, false
}

// ActiveProject возвращает активный проект, если он выбран и известен.
func (r *Registry) ActiveProject() (ProjectEntry, bool) {
	r.mu.Lock()
	active := r.state.ActiveProject
	r.mu.Unlock()
	if active == "" {
		return ProjectEntry{}, false
	}
	return r.Project(active)
}

// RecentProjects возвращает проекты от недавно открытых к давним.
func (r *Registry) RecentProjects() []ProjectEntry {
	state := r.State()
	out := state.Projects
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastOpened.After(out[j].LastOpened) })
	return out
}

// Upsert добавляет или обновляет запись о проекте и сохраняет реестр.
// Пустой LastOpened проставляется временем записи.
func (r *Registry) Upsert(e ProjectEntry) error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if e.LastOpened.IsZero() {
		e.LastOpened = time.Now().UTC()
	}
	return r.update(func(s *RegistryState) error {
		for i := range s.Projects {
			if s.Projects[i].ID == e.ID {
				s.Projects[i] = e
				return nil
			}
		}
		s.Projects = append(s.Projects, e)
		return nil
	})
}

// SuggestProjectID предлагает валидный идентификатор проекта по каталогу тем
// же правилом, что и автопоиск компонентов: слаг из имени каталога, а если из
// него слага не выходит (каталог назван кириллицей) — стабильный хеш пути.
func SuggestProjectID(dir string) domain.ProjectID {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	abs = filepath.Clean(abs)
	if id := domain.ProjectID(slugify(filepath.Base(abs))); id.Validate() == nil {
		return id
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(abs)))
	return domain.ProjectID("prj-" + hex.EncodeToString(sum[:4]))
}

// RegisterTemporary регистрирует временный проект по каталогу выгрузки — это
// то, что делает set_dump. Идентификатор генерируется: каталог выгрузки часто
// назван кириллицей, и требовать от пользователя слаг тут не за что.
func (r *Registry) RegisterTemporary(dir string) (ProjectEntry, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ProjectEntry{}, fmt.Errorf("каталог %q: %w", dir, err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	entry := ProjectEntry{
		ID:        SuggestProjectID(abs),
		Root:      abs,
		Temporary: true,
	}
	if err := r.Upsert(entry); err != nil {
		return ProjectEntry{}, err
	}
	saved, _ := r.Project(entry.ID)
	return saved, nil
}

// Remove убирает проект из реестра; активным он после этого не остаётся.
func (r *Registry) Remove(id domain.ProjectID) error {
	return r.update(func(s *RegistryState) error {
		for i := range s.Projects {
			if s.Projects[i].ID == id {
				s.Projects = append(s.Projects[:i], s.Projects[i+1:]...)
				if s.ActiveProject == id {
					s.ActiveProject = ""
				}
				return nil
			}
		}
		return fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	})
}

// SetActiveProject делает проект активным. Проект обязан быть зарегистрирован:
// активным нельзя назначить то, чего сервер не знает.
func (r *Registry) SetActiveProject(id domain.ProjectID) error {
	return r.update(func(s *RegistryState) error {
		for i := range s.Projects {
			if s.Projects[i].ID == id {
				s.Projects[i].LastOpened = time.Now().UTC()
				s.ActiveProject = id
				return nil
			}
		}
		return fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	})
}

// update применяет изменение под замком и атомарно сохраняет результат.
// Замок держится на время записи: иначе две правки, сделанные одновременно,
// опубликовали бы каждая своё состояние, и одна из них потерялась бы.
//
// Перед изменением файл перечитывается, поэтому правка, сделанная другим
// процессом, не затирается целиком. Окно между чтением и rename остаётся:
// если два процесса пишут в один каталог одновременно, последний rename
// побеждает по своей записи. Одновременная работа двух серверов над одним
// каталогом не предполагается — реестр локальное состояние одного сервера.
func (r *Registry) update(fn func(*RegistryState) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.reloadLocked(); err != nil {
		return err
	}
	next := r.state.clone()
	if next.Version == 0 {
		next.Version = RegistryVersion
	}
	if err := fn(&next); err != nil {
		return err
	}
	sort.SliceStable(next.Projects, func(i, j int) bool { return next.Projects[i].ID < next.Projects[j].ID })
	if err := r.write(next); err != nil {
		return err
	}
	r.state = next
	return nil
}

// write публикует состояние атомарно: временный файл в том же каталоге, fsync,
// затем rename. Rename поверх существующего файла атомарен и на POSIX, и на
// Windows (os.Rename использует MoveFileEx с заменой).
func (r *Registry) write(state RegistryState) error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return fmt.Errorf("создание каталога %s: %w", r.dir, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("сериализация реестра: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(r.dir, RegistryFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("временный файл реестра: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // после успешного rename файла уже нет, ошибка не важна

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("запись %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("сброс на диск %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие %s: %w", tmpName, err)
	}
	if err := повторитьПриЗанятомФайле(func() error { return os.Rename(tmpName, r.path) }); err != nil {
		return fmt.Errorf("публикация реестра %s: %w", r.path, err)
	}
	return nil
}

// попытокПриЗанятомФайле и паузаПриЗанятомФайле — потолок ожидания того, что
// чужая короткая операция над registry.json закончится: 2 с суммарно. Это
// именно потолок, а не цена: цикл выходит на первой удачной попытке, и в
// обычной работе первая же и удаётся. Потолок подобран по самому тяжёлому
// сценарию, который проверяется (TestRegistryПереживаетКонкурентнуюЗапись:
// восемь писателей против четырёх читателей, перечитывающих файл в цикле без
// пауз); с меньшим потолком записи в реестр там терялись.
const (
	попытокПриЗанятомФайле = 100
	паузаПриЗанятомФайле   = 20 * time.Millisecond
)

// повторитьПриЗанятомФайле повторяет операцию над файлом реестра, пока файл
// занят другим процессом.
//
// Обещание реестра — «читатель видит либо старое содержимое целиком, либо
// новое целиком» — на POSIX держится само: rename поверх открытого файла там
// проходит, а открытый файл остаётся читаемым. На Windows ни то, ни другое не
// верно: пока читатель держит registry.json открытым, публикация падает с
// «Access is denied», а пока идёт rename, чтение падает со «The process cannot
// access the file because it is being used by another process». Обе ошибки
// временные и живут микросекунды — ждать их дешевле, чем терять запись
// реестра или отвечать вызывающему отказом на ровном месте.
//
// Ошибка возвращается последняя: постоянный отказ в правах становится
// медленнее на 2 с, но не исчезает. Отсутствие файла не ретраится — это
// нормальное состояние пустого реестра, а не занятость.
//
// Проверено и отвергнуто: открывать registry.json на чтение с
// FILE_SHARE_DELETE через syscall в windows-файле. Одну из двух ошибок это
// снимает, но вторую усугубляет — файл, удалённый при живом handle, остаётся
// в состоянии delete-pending и занимает имя, из-за чего следующий rename
// падает так же. Отдельный платформенный файл с syscall при том же итоге не
// окупается.
func повторитьПриЗанятомФайле(op func() error) error {
	var err error
	for попытка := 0; попытка < попытокПриЗанятомФайле; попытка++ {
		if err = op(); err == nil || errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(паузаПриЗанятомФайле)
	}
	return err
}

// clone возвращает глубокую копию состояния: наружу не должны утекать срезы,
// которые вызывающий сможет изменить под замком реестра.
func (s RegistryState) clone() RegistryState {
	out := s
	out.Projects = make([]ProjectEntry, len(s.Projects))
	for i, p := range s.Projects {
		out.Projects[i] = p.clone()
	}
	return out
}

// clone возвращает копию записи о проекте.
func (e ProjectEntry) clone() ProjectEntry {
	out := e
	if e.RetiredEpochs != nil {
		out.RetiredEpochs = append([]uint64(nil), e.RetiredEpochs...)
	}
	return out
}
