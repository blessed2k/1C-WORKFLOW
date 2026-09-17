package index

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// ownerlessModuleKinds — единственные виды модулей, у которых объекта-владельца
// в конфигурации нет (история 8): они лежат вне коллекции выгрузки.
var ownerlessModuleKinds = map[bsl.ModuleKind]bool{
	bsl.ModuleApplication:        true,
	bsl.ModuleSession:            true,
	bsl.ModuleExternalConnection: true,
}

// TestRealDumpModuleOwnerFilled — приёмка истории 8 на реальной выгрузке:
// после полного индекса module.owner_object_id заполнена у КАЖДОГО модуля,
// кроме модулей приложения, сеанса и внешнего соединения. Без ONEC_DUMP
// честно скипается. Прогон холодный и полный, поэтому только точечно
// (-run, -timeout 20m), см. CLAUDE.md «Подводные камни».
func TestRealDumpModuleOwnerFilled(t *testing.T) {
	root := realDumpRoot(t)
	ctx := context.Background()
	st := openTestStore(t)
	m := workspace.Manifest{
		Version: 1, Project: "utdemo", Root: root,
		Components: []workspace.Component{{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root}},
	}
	builtins := syntaxtest.RealOrSkip(t)
	svc := NewService(st, "utdemo", m, builtins, Config{})
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}

	var rels []string
	var walkErrs int
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Пропуск при обходе занижает ЧИСЛО .bsl, с которым потом
			// сверяется индекс, то есть тихо уменьшает обе стороны
			// сравнения. Считаем и валим тест: прогон на неполном обходе
			// ничего не доказывает.
			walkErrs++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("обход выгрузки: %v", err)
	}
	sort.Strings(rels)
	if walkErrs != 0 {
		t.Fatalf("обход выгрузки %s пропустил %d входов по ошибке — объём осмотра занижен", root, walkErrs)
	}
	if len(rels) == 0 {
		t.Fatalf("в выгрузке %s не найдено ни одного .bsl", root)
	}

	total, filled := 0, 0
	var unexpected, missing []string
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		for _, rel := range rels {
			fileID, ok, err := tx.SourceFileID("cfg", rel)
			if err != nil {
				return err
			}
			if !ok {
				missing = append(missing, rel+" (нет source_file)")
				continue
			}
			row, ok, err := tx.ModuleByFile(fileID)
			if err != nil {
				return err
			}
			if !ok {
				missing = append(missing, rel+" (нет строки module)")
				continue
			}
			total++
			if row.OwnerObjectID != 0 {
				filled++
				continue
			}
			if !ownerlessModuleKinds[bsl.ClassifyModule(rel).Kind] {
				unexpected = append(unexpected, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	t.Logf("bsl-файлов обходом: %d, модулей в индексе: %d, с владельцем: %d, без владельца: %d",
		len(rels), total, filled, total-filled)

	// Сколько строк тест реально осмотрел — такое же утверждение, как и
	// отсутствие NULL: без него регрессия «опубликовалось три модуля вместо
	// двенадцати тысяч» оставляет пятиминутный прогон зелёным. Пустой
	// missing и означает total == len(rels): вторая проверка была бы той же
	// самой.
	if len(missing) != 0 {
		t.Errorf("модуль в индексе не найден для %d из %d .bsl-файлов выгрузки, например %v",
			len(missing), len(rels), firstNPaths(missing, 10))
	}
	if len(unexpected) != 0 {
		t.Errorf("модулей без владельца, не относящихся к приложению/сеансу/внешнему соединению: %d, например %v",
			len(unexpected), firstNPaths(unexpected, 10))
	}
}

// firstNPaths — короткий срез путей для сообщения об ошибке (firstN в
// realworld_test.go занят диагностиками).
func firstNPaths(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
