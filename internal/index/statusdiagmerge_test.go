package index

import (
	"context"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestStatusMergesHydrationAndStoreDiagnostics — тихая потеря, найденная
// ревью таска 09: Status брал диагностики из store ТОЛЬКО при пустом
// lastDiagnostics, а ensureHydrated пишет туда info и в штатном случае
// («компонент индексируется впервые»). Проект, где один компонент ещё не
// индексировался, а у второго в эпохе лежат диагностики, на свежем процессе
// показывал одну info и СКРЫВАЛ персистентный список.
//
// Ожидаемые значения заведены здесь руками: ровно одна строка diagnostic в
// эпохе (bsl_bad_annotation_argument) плюс ровно одна диагностика гидратации
// (index_corpus_not_hydrated) — в ответе обязаны быть обе.
func TestStatusMergesHydrationAndStoreDiagnostics(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	root := writeFixtureComponent(t)

	if err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "."}); err != nil {
			return err
		}
		return tx.InsertDiagnostic(store.Diagnostic{
			ComponentID: "cfg", Severity: string(domain.SeverityWarning),
			Code: "bsl_bad_annotation_argument", Message: "аргумент аннотации не разобран",
		})
	}); err != nil {
		t.Fatalf("seed diagnostic: %v", err)
	}

	m := workspace.Manifest{
		Version: 1, Project: "proj", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
			{ID: "ext", Kind: domain.KindExtension, Root: ".", AbsRoot: root, AppliesTo: "cfg"},
		},
	}
	svc := NewService(st, "proj", m, nil, Config{})
	t.Cleanup(func() { svc.Close() })

	// Компонент ext в индексе отсутствует — гидратация даёт info, не отказ.
	svc.opMu.Lock()
	svc.ensureHydrated(ctx, "ext")
	svc.opMu.Unlock()

	status, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	byCode := map[string]int{}
	for _, d := range status.LastDiagnostics {
		byCode[d.Code]++
	}
	if byCode["index_corpus_not_hydrated"] != 1 {
		t.Errorf("index_corpus_not_hydrated = %d, want 1: %+v", byCode["index_corpus_not_hydrated"], status.LastDiagnostics)
	}
	if byCode["bsl_bad_annotation_argument"] != 1 {
		t.Errorf("персистентная диагностика скрыта диагностикой гидратации: byCode = %v, LastDiagnostics = %+v",
			byCode, status.LastDiagnostics)
	}
}
