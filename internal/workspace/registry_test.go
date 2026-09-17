package workspace_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestRegistryПереживаетКонкурентнуюЗапись — критерий приёмки: реестр пишется
// атомарно (temp + rename), поэтому читатель никогда не видит обрезанный файл,
// а ни одна запись не теряется. Прогонять с -race.
func TestRegistryПереживаетКонкурентнуюЗапись(t *testing.T) {
	dir := t.TempDir()
	r, err := workspace.OpenRegistry(dir)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}

	const писателей = 8
	const записей = 12

	var писатели_wg, читатели_wg sync.WaitGroup
	стоп := make(chan struct{})

	// Читатели открывают реестр заново, как это делал бы другой процесс.
	var ошибкаЧтения error
	var muЧтение sync.Mutex
	for range 4 {
		читатели_wg.Add(1)
		go func() {
			defer читатели_wg.Done()
			for {
				select {
				case <-стоп:
					return
				default:
				}
				if _, err := workspace.OpenRegistry(dir); err != nil {
					muЧтение.Lock()
					ошибкаЧтения = err
					muЧтение.Unlock()
					return
				}
			}
		}()
	}

	for w := range писателей {
		писатели_wg.Add(1)
		go func(w int) {
			defer писатели_wg.Done()
			for i := range записей {
				id := domain.ProjectID(fmt.Sprintf("p%d-%d", w, i))
				err := r.Upsert(workspace.ProjectEntry{
					ID:       id,
					Root:     filepath.Join(dir, string(id)),
					IndexDir: filepath.Join(dir, ".mcp1c", "index", string(id)),
				})
				if err != nil {
					t.Errorf("Upsert(%s): %v", id, err)
					return
				}
			}
		}(w)
	}

	// Сначала дописать, потом остановить читателей.
	писатели_wg.Wait()
	close(стоп)
	читатели_wg.Wait()

	muЧтение.Lock()
	defer muЧтение.Unlock()
	if ошибкаЧтения != nil {
		t.Fatalf("читатель увидел неконсистентный реестр: %v", ошибкаЧтения)
	}

	// Файл на диске обязан содержать все записи: read-modify-write под одним
	// замком, публикация — rename.
	снова, err := workspace.OpenRegistry(dir)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	state := снова.State()
	if len(state.Projects) != писателей*записей {
		t.Fatalf("в реестре %d проектов, ожидалось %d", len(state.Projects), писателей*записей)
	}
	for w := range писателей {
		for i := range записей {
			id := domain.ProjectID(fmt.Sprintf("p%d-%d", w, i))
			if _, ok := снова.Project(id); !ok {
				t.Errorf("проект %s потерян", id)
			}
		}
	}

	// Временных файлов после атомарной записи остаться не должно.
	entries, err := os.ReadDir(filepath.Join(dir, workspace.RegistryDirName))
	if err != nil {
		t.Fatalf("чтение каталога реестра: %v", err)
	}
	for _, e := range entries {
		if e.Name() != workspace.RegistryFileName {
			t.Errorf("в каталоге реестра остался посторонний файл %q", e.Name())
		}
	}
}

// TestRegistryВременныйПроектПоКириллическомуКаталогу — set_dump регистрирует
// проект по каталогу выгрузки, а каталоги 1С почти всегда названы кириллицей.
// Идентификатор для такого проекта обязан сгенерироваться, иначе
// зарегистрировать его нечем.
func TestRegistryВременныйПроектПоКириллическомуКаталогу(t *testing.T) {
	dir := t.TempDir()
	выгрузка := filepath.Join(dir, "Выгрузка УТ 11.5")
	if err := os.MkdirAll(выгрузка, 0o755); err != nil {
		t.Fatalf("подготовка выгрузки: %v", err)
	}

	r, err := workspace.OpenRegistry(dir)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}

	entry, err := r.RegisterTemporary(выгрузка)
	if err != nil {
		t.Fatalf("RegisterTemporary: %v", err)
	}
	if err := entry.ID.Validate(); err != nil {
		t.Fatalf("сгенерированный id %q непригоден: %v", entry.ID, err)
	}
	if !entry.Temporary {
		t.Error("проект от set_dump обязан быть помечен временным")
	}

	// Повторная регистрация того же каталога даёт тот же проект, а не второй.
	снова, err := r.RegisterTemporary(выгрузка)
	if err != nil {
		t.Fatalf("повторный RegisterTemporary: %v", err)
	}
	if снова.ID != entry.ID {
		t.Errorf("id сменился с %q на %q", entry.ID, снова.ID)
	}
	if len(r.State().Projects) != 1 {
		t.Errorf("проектов в реестре %d, ожидался один", len(r.State().Projects))
	}
	if err := r.SetActiveProject(entry.ID); err != nil {
		t.Errorf("SetActiveProject: %v", err)
	}

	// Разные каталоги — разные проекты.
	другая := filepath.Join(dir, "Выгрузка БП 3.0")
	if err := os.MkdirAll(другая, 0o755); err != nil {
		t.Fatalf("подготовка второй выгрузки: %v", err)
	}
	вторая, err := r.RegisterTemporary(другая)
	if err != nil {
		t.Fatalf("RegisterTemporary второй выгрузки: %v", err)
	}
	if вторая.ID == entry.ID {
		t.Errorf("двум разным каталогам предложен один id %q", вторая.ID)
	}
}

// TestRegistryАктивныйПроектПереживаетПерезапуск проверяет то, ради чего реестр
// и существует: сервер, поднявшись заново, помнит выбранный проект и место
// его индекса.
func TestRegistryАктивныйПроектПереживаетПерезапуск(t *testing.T) {
	dir := t.TempDir()
	r, err := workspace.OpenRegistry(dir)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}

	if _, ok := r.ActiveProject(); ok {
		t.Error("в пустом реестре не может быть активного проекта")
	}
	if err := r.SetActiveProject("ut-main"); err == nil {
		t.Error("активным назначен проект, которого нет в реестре")
	}

	entry := workspace.ProjectEntry{
		ID:           "ut-main",
		Root:         filepath.Join(dir, "ut"),
		ManifestPath: filepath.Join(dir, "ut", workspace.ManifestFileName),
		IndexDir:     filepath.Join(dir, ".mcp1c", "index", "ut-main"),
		Epoch:        3,
		Generation:   domain.NewGeneration(3, 17),
		RetiredEpochs: []uint64{
			1, 2,
		},
	}
	if err := r.Upsert(entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := r.SetActiveProject("ut-main"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	снова, err := workspace.OpenRegistry(dir)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	got, ok := снова.ActiveProject()
	if !ok {
		t.Fatal("активный проект не восстановлен")
	}
	if got.ID != entry.ID || got.IndexDir != entry.IndexDir {
		t.Errorf("активный проект = %+v, ожидался %+v", got, entry)
	}
	if got.Generation != entry.Generation {
		t.Errorf("generation = %q, ожидалось %q", got.Generation, entry.Generation)
	}
	if len(got.RetiredEpochs) != 2 {
		t.Errorf("retiredEpochs = %v, ожидались 1 и 2", got.RetiredEpochs)
	}
	if got.LastOpened.IsZero() {
		t.Error("lastOpened не заполнен: по нему строится список последних проектов")
	}
}
