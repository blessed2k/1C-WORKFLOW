package workspace_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestDiscoverНаРеальнойВыгрузке — критерий приёмки: на настоящей выгрузке
// (ONEC_REAL_DUMP, например каталог выгрузки ut_demo) автопоиск находит
// конфигурацию и не спускается внутрь неё. Внутри выгрузки УТ около сорока
// каталогов метаданных и десятки тысяч файлов: спуск виден и по числу
// кандидатов, и по времени.
func TestDiscoverНаРеальнойВыгрузке(t *testing.T) {
	dump := os.Getenv("ONEC_REAL_DUMP")
	if dump == "" {
		t.Skip("укажите ONEC_REAL_DUMP — каталог XML-выгрузки 1С — чтобы прогнать этот тест")
	}
	if _, err := os.Stat(filepath.Join(dump, workspace.ConfigurationFileName)); err != nil {
		t.Skipf("в %s нет %s: %v", dump, workspace.ConfigurationFileName, err)
	}

	начало := time.Now()
	кандидаты, err := workspace.Discover(dump)
	прошло := time.Since(начало)
	if err != nil {
		t.Fatalf("Discover(%s): %v", dump, err)
	}

	if len(кандидаты) != 1 {
		t.Fatalf("кандидатов %d, ожидался ровно один — сама выгрузка: %+v", len(кандидаты), кандидаты)
	}
	c := кандидаты[0]
	if c.Kind != domain.KindConfiguration {
		t.Errorf("kind = %q, ожидалось %q", c.Kind, domain.KindConfiguration)
	}
	if c.Rel != "" {
		t.Errorf("rel = %q, ожидался корень обхода", c.Rel)
	}
	if c.Name == "" {
		t.Error("имя конфигурации не прочитано из Configuration.xml")
	}
	if c.ExtensionPurpose != "" {
		t.Errorf("конфигурация помечена назначением расширения %q", c.ExtensionPurpose)
	}

	// Обход, остановившийся на корне, укладывается в доли секунды; спуск в
	// выгрузку УТ (48 тысяч файлов) занял бы секунды.
	if прошло > 2*time.Second {
		t.Errorf("обход занял %s: похоже, автопоиск спустился внутрь выгрузки", прошло)
	}

	// Выгрузка, найденная от родительского каталога, тоже остаётся одним
	// кандидатом, а её rel годится в поле root манифеста.
	родитель := filepath.Dir(dump)
	отРодителя, err := workspace.Discover(родитель)
	if err != nil {
		t.Fatalf("Discover(%s): %v", родитель, err)
	}
	нашли := false
	for _, cand := range отРодителя {
		if cand.Root == c.Root {
			нашли = true
			if cand.Rel != filepath.Base(dump) {
				t.Errorf("rel = %q, ожидалось %q", cand.Rel, filepath.Base(dump))
			}
		}
	}
	if !нашли {
		t.Errorf("выгрузка %s не найдена при обходе родительского каталога", dump)
	}
}
