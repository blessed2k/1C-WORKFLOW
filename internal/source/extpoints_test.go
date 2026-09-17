package source

import (
	"context"
	"strings"
	"testing"
)

func epSource(t *testing.T) *XMLSource {
	t.Helper()
	return NewXMLSource("testdata/ep")
}

// pointNames returns "Модуль.Процедура" for every point of a report.
func pointNames(rep *ExtensionPointsReport) []string {
	var out []string
	for _, p := range rep.Points {
		out = append(out, p.Module+"."+p.Procedure)
	}
	return out
}

func TestExtensionPointsFindsOverridableOnly(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "печать", 10)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	got := strings.Join(pointNames(rep), " ")
	if !strings.Contains(got, "ПечатьПереопределяемый.ПриФормированииСпискаКомандПечати") {
		t.Errorf("overridable point missing: %v", pointNames(rep))
	}
	// A plain common module is not an extension point, however well it matches.
	if strings.Contains(got, "ОбычныйМодуль") {
		t.Errorf("plain module must not be offered as an extension point: %v", pointNames(rep))
	}
	// Non-exported methods are not extension points either.
	if strings.Contains(got, "ВнутренняяФункция") {
		t.Errorf("non-exported method must not be offered: %v", pointNames(rep))
	}
}

func TestExtensionPointsReportsBodyAndContext(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "печать заказа заполнение", 10)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	byName := map[string]ExtensionPoint{}
	for _, p := range rep.Points {
		byName[p.Procedure] = p
	}
	// A stub of comments only is not implemented; a procedure with a statement is.
	if p, ok := byName["ПриФормированииСпискаКомандПечати"]; !ok || !p.Implemented {
		t.Errorf("procedure with a statement must be implemented=true: %+v", p)
	}
	if p, ok := byName["ПередПечатьюДокумента"]; !ok || p.Implemented {
		t.Errorf("comment-only stub must be implemented=false: %+v", p)
	}
	if p, ok := byName["ПриЗаполненииЗаказа"]; !ok || p.Context != "клиент" {
		t.Errorf("client module must be reported as клиент: %+v", p)
	}
	if p := byName["ПриФормированииСпискаКомандПечати"]; !strings.HasPrefix(p.Summary, "Позволяет добавить свои печатные формы") {
		t.Errorf("doc summary not picked up: %q", p.Summary)
	}
	if p := byName["ПриФормированииСпискаКомандПечати"]; !strings.Contains(p.Signature, "Экспорт") {
		t.Errorf("signature must be the declaration as written: %q", p.Signature)
	}
}

// TestExtensionPointsRanking pins that the procedure name outweighs a mention in
// the doc comment, so the point that is actually about the task comes first.
func TestExtensionPointsRanking(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "заполнении", 10)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	if len(rep.Points) == 0 {
		t.Fatal("no points found")
	}
	if rep.Points[0].Procedure != "ПриЗаполненииЗаказа" {
		t.Errorf("best match = %q, want ПриЗаполненииЗаказа", rep.Points[0].Procedure)
	}
}

func TestExtensionPointsLimitAndCount(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "печать заполнение заказа", 1)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	if len(rep.Points) != 1 {
		t.Errorf("limit ignored: %d points", len(rep.Points))
	}
	if rep.Count <= 1 {
		t.Errorf("count must report everything found, not the truncated page: %d", rep.Count)
	}
	if rep.Note == "" {
		t.Error("the note about &Вместо must always be present")
	}
}

func TestExtensionPointsEmptyQuery(t *testing.T) {
	if _, err := epSource(t).ExtensionPoints(context.Background(), "   ", 10); err == nil {
		t.Error("empty query must be an error")
	}
	if _, err := epSource(t).ExtensionPoints(context.Background(), "в по на", 10); err == nil {
		t.Error("a query of noise words only must be an error")
	}
}

// TestExtensionPointsMultilineSignature pins the defect that silently lost 197
// points of 3320 in УТ: when the parameters are wrapped, the Экспорт keyword is
// not on the header line.
func TestExtensionPointsMultilineSignature(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "склады", 10)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	var found *ExtensionPoint
	for i, p := range rep.Points {
		if p.Procedure == "ЗаполнитьИнформациюОСкладах" {
			found = &rep.Points[i]
		}
	}
	if found == nil {
		t.Fatalf("point with a wrapped signature not found: %v", pointNames(rep))
	}
	if found.Line <= 0 {
		t.Error("the declaration line must be reported")
	}
	if !strings.Contains(found.Signature, "Экспорт") || strings.Contains(found.Signature, "  ") {
		t.Errorf("signature must be collected whole and normalised: %q", found.Signature)
	}
}

// TestExtensionPointsBareReturnIsStub pins that "Возврат;" alone is an empty БСП
// stub, not an implementation: it mislabelled 195 points in УТ, each of them
// telling the caller to go and read logic that is not there.
func TestExtensionPointsBareReturnIsStub(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "печать вывод параметры", 20)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	for _, p := range rep.Points {
		switch p.Procedure {
		case "ПередВыводомНаПечать":
			if p.Implemented {
				t.Error("a bare Возврат; is a stub, not an implementation")
			}
		case "ТолькоПараметрыВКомментарии":
			if p.Summary != "" {
				t.Errorf("a comment block that opens with Параметры has no summary, got %q", p.Summary)
			}
		}
	}
}

// TestExtensionPointsStemming pins that a query in any case finds the point:
// metadata is named in the nominative, tasks are described in whatever case fits.
func TestExtensionPointsStemming(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "добавить печатную форму", 10)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	if len(rep.Points) == 0 || !strings.Contains(rep.Points[0].Module, "Печать") {
		t.Errorf("declined query must still find Печать: %v", pointNames(rep))
	}
}

func TestExtensionPointsLimitClamp(t *testing.T) {
	rep, err := epSource(t).ExtensionPoints(context.Background(), "печать", 500)
	if err != nil {
		t.Fatalf("ExtensionPoints: %v", err)
	}
	// An out-of-range limit must clamp to the maximum, not fall back to the
	// default: asking for more must never return less.
	if len(rep.Points) < 3 && rep.Count >= 3 {
		t.Errorf("limit=500 returned %d of %d points", len(rep.Points), rep.Count)
	}
}
