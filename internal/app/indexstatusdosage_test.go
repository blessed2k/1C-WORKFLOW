package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// шумныйПроект кладёт на диск проект, у которого cfg-компонент содержит
// filesN модулей объекта с кодом вне процедур: парсер честно не находит в них
// ни метода, ни переменной и выдаёт по одной диагностике index_empty_parse на
// файл. Это единственный дешёвый способ получить УПРАВЛЯЕМОЕ число реальных
// диагностик через сервис, не подсовывая сервису стаб индекса.
func шумныйПроект(t *testing.T, filesN int) *IndexStatusService {
	t.Helper()
	workspaceRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeFile(t, filepath.Join(projectRoot, "cfg", "Configuration.xml"), конфигурацияXML("Тест"))
	for i := 0; i < filesN; i++ {
		writeFile(t,
			filepath.Join(projectRoot, "cfg", "Catalogs", fmt.Sprintf("Шум%d", i), "Ext", "ObjectModule.bsl"),
			"А = 1;\n")
	}
	manifest := map[string]any{
		"version": 1, "project": "noisy",
		"components": []map[string]any{{"id": "cfg", "kind": "configuration", "root": "cfg"}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, workspace.ManifestFileName), string(data))

	reg, err := workspace.OpenRegistry(workspaceRoot)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := reg.Upsert(workspace.ProjectEntry{ID: "noisy", Root: projectRoot}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := reg.SetActiveProject("noisy"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	p, err := NewProjects(workspaceRoot, nil, index.Config{})
	if err != nil {
		t.Fatalf("NewProjects: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return NewIndexStatusService(p)
}

// TestДозировкаДиагностик: счётчик, выборка, разбивка по кодам, признак
// урезания и note. Ожидаемые числа взяты из входа теста, не из кода под ним.
func TestДозировкаДиагностик(t *testing.T) {
	diag := func(code string) domain.Diagnostic {
		return domain.Diagnostic{Code: code, Severity: domain.SeverityWarning, Message: "шум"}
	}
	многоDiag := func(n int) []domain.Diagnostic {
		out := make([]domain.Diagnostic, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, diag("index_empty_parse"))
		}
		return out
	}

	t.Run("пусто — дайджеста нет", func(t *testing.T) {
		got, digest := doseDiagnostics(nil, false)
		if got != nil || digest != nil {
			t.Fatalf("doseDiagnostics(nil) = %v, %+v; ожидались nil, nil", got, digest)
		}
	})

	t.Run("коротко — список целиком, дайджест есть", func(t *testing.T) {
		in := append(многоDiag(2), diag("index_duplicate_symbol_uid"))
		got, digest := doseDiagnostics(in, false)
		if len(got) != 3 {
			t.Fatalf("показано %d, ожидалось 3", len(got))
		}
		if digest == nil {
			t.Fatal("дайджест nil при трёх диагностиках")
		}
		if digest.TotalCount != 3 || digest.ShownCount != 3 {
			t.Errorf("TotalCount/ShownCount = %d/%d, ожидалось 3/3", digest.TotalCount, digest.ShownCount)
		}
		if digest.Truncated {
			t.Error("Truncated = true, урезать было нечего")
		}
		if digest.Note != "" {
			t.Errorf("Note = %q, ожидалась пустая", digest.Note)
		}
		if digest.ByCode["index_empty_parse"] != 2 || digest.ByCode["index_duplicate_symbol_uid"] != 1 {
			t.Errorf("ByCode = %v, ожидалось 2 и 1", digest.ByCode)
		}
	})

	t.Run("длинно — урезано и это видно", func(t *testing.T) {
		got, digest := doseDiagnostics(многоDiag(268), false)
		if len(got) != 10 {
			t.Fatalf("показано %d, ожидалось 10", len(got))
		}
		if digest.TotalCount != 268 || digest.ShownCount != 10 {
			t.Errorf("TotalCount/ShownCount = %d/%d, ожидалось 268/10", digest.TotalCount, digest.ShownCount)
		}
		if !digest.Truncated {
			t.Error("Truncated = false при 268 записях и выборке в 10")
		}
		if digest.ByCode["index_empty_parse"] != 268 {
			t.Errorf("ByCode[index_empty_parse] = %d, ожидалось 268: разбивка считает всё, не выборку",
				digest.ByCode["index_empty_parse"])
		}
		if !strings.Contains(digest.Note, "268") || !strings.Contains(digest.Note, "includeAllDiagnostics") {
			t.Errorf("Note = %q — должна называть полное число и флаг", digest.Note)
		}
	})

	t.Run("флаг — список целиком", func(t *testing.T) {
		got, digest := doseDiagnostics(многоDiag(268), true)
		if len(got) != 268 {
			t.Fatalf("показано %d, ожидалось 268 при includeAllDiagnostics", len(got))
		}
		if digest.Truncated || digest.Note != "" {
			t.Errorf("Truncated=%v Note=%q — при полном списке урезания нет", digest.Truncated, digest.Note)
		}
		if digest.ByCode["index_empty_parse"] != 268 {
			t.Errorf("ByCode = %v, разбивка нужна и при полном списке", digest.ByCode)
		}
	})
}

// TestReindexДиагностикиОдинРаз: критерий приёмки R34 — одна и та же
// диагностика приезжает ровно один раз. Проверяется через сериализованный
// ответ: у элемента компонента ключа diagnostics больше нет вообще, весь
// список лежит в элементе ответа и дозирован (R33).
func TestReindexДиагностикиОдинРаз(t *testing.T) {
	svc := шумныйПроект(t, 12)

	resp, err := svc.Reindex(context.Background(), ReindexInput{Mode: "full"})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	item := resp.Items[0]

	if item.DiagnosticsDigest == nil {
		t.Fatal("DiagnosticsDigest == nil: фикстура на 12 модулей обязана дать диагностики")
	}
	if item.DiagnosticsDigest.TotalCount != 12 {
		t.Fatalf("TotalCount = %d, ожидалось 12 (по одной на модуль фикстуры)", item.DiagnosticsDigest.TotalCount)
	}
	if len(item.Diagnostics) != 10 {
		t.Errorf("в ответе %d диагностик, ожидалось 10 (выборка)", len(item.Diagnostics))
	}
	if !item.DiagnosticsDigest.Truncated {
		t.Error("Truncated = false, хотя показаны не все")
	}
	if item.DiagnosticsDigest.ByCode["index_empty_parse"] != 12 {
		t.Errorf("ByCode = %v, ожидалось 12 записей index_empty_parse", item.DiagnosticsDigest.ByCode)
	}
	for _, d := range item.Diagnostics {
		if d.Component != "cfg" {
			t.Errorf("Component = %q, ожидался cfg: привязка к компоненту не должна теряться", d.Component)
		}
	}

	raw, err := json.Marshal(item.Components)
	if err != nil {
		t.Fatalf("marshal components: %v", err)
	}
	if strings.Contains(string(raw), "diagnostics") {
		t.Errorf("элемент компонента всё ещё несёт diagnostics: %s", raw)
	}
}

// TestReindexIncludeAllDiagnostics: явный флаг отдаёт список целиком, и
// дайджест при этом честно говорит, что урезания не было.
func TestReindexIncludeAllDiagnostics(t *testing.T) {
	svc := шумныйПроект(t, 12)

	resp, err := svc.Reindex(context.Background(), ReindexInput{Mode: "full", IncludeAllDiagnostics: true})
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	item := resp.Items[0]
	if len(item.Diagnostics) != 12 {
		t.Fatalf("в ответе %d диагностик, ожидалось 12 при includeAllDiagnostics", len(item.Diagnostics))
	}
	if item.DiagnosticsDigest == nil || item.DiagnosticsDigest.Truncated {
		t.Errorf("дайджест = %+v, ожидался Truncated=false", item.DiagnosticsDigest)
	}
}

// TestStatusДиагностикиДозируются: index_status идёт тем же путём — после
// reindex шумного проекта статус несёт выборку и дайджест, а не все 12.
func TestStatusДиагностикиДозируются(t *testing.T) {
	svc := шумныйПроект(t, 12)
	ctx := context.Background()

	if _, err := svc.Reindex(ctx, ReindexInput{Mode: "full"}); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	resp, err := svc.Status(ctx, StatusInput{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	st := resp.Items[0]
	if st.DiagnosticsDigest == nil {
		t.Fatal("DiagnosticsDigest == nil после reindex шумного проекта")
	}
	if st.DiagnosticsDigest.TotalCount != 12 {
		t.Fatalf("TotalCount = %d, ожидалось 12", st.DiagnosticsDigest.TotalCount)
	}
	if len(st.Diagnostics) != 10 {
		t.Errorf("в index_status %d диагностик, ожидалось 10", len(st.Diagnostics))
	}

	full, err := svc.Status(ctx, StatusInput{IncludeAllDiagnostics: true})
	if err != nil {
		t.Fatalf("Status(full): %v", err)
	}
	if len(full.Items[0].Diagnostics) != 12 {
		t.Errorf("при includeAllDiagnostics %d диагностик, ожидалось 12", len(full.Items[0].Diagnostics))
	}
}
