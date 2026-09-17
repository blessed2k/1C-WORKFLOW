package source

import (
	"context"
	"strings"
	"testing"
)

func TestDependencyPathsDirectEdge(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.DependencyPaths(context.Background(), "Catalog", "Товары", "AccumulationRegister", "ТоварыНаСкладах", 0, 0)
	if err != nil {
		t.Fatalf("DependencyPaths: %v", err)
	}
	if rep.From != "Справочник.Товары" || rep.To != "РегистрНакопления.ТоварыНаСкладах" {
		t.Fatalf("endpoints = %q -> %q", rep.From, rep.To)
	}
	if rep.Found != 1 || len(rep.Paths) != 1 {
		t.Fatalf("found = %d, paths = %+v, want 1", rep.Found, rep.Paths)
	}
	p := rep.Paths[0]
	// ТоварыНаСкладах.Товар is typed by СправочникСсылка.Товары: one step.
	if p.Length != 1 || len(p.Steps) != 1 {
		t.Fatalf("length = %d, steps = %+v, want 1", p.Length, p.Steps)
	}
	st := p.Steps[0]
	if st.From != "Справочник.Товары" || st.To != "РегистрНакопления.ТоварыНаСкладах" {
		t.Errorf("step = %q -> %q", st.From, st.To)
	}
	if !strings.Contains(st.Via, "Товар") || !strings.Contains(st.Via, "Измерение") {
		t.Errorf("via = %q, want the dimension ТоварыНаСкладах.Товар", st.Via)
	}
}

func TestDependencyPathsThroughDocument(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.DependencyPaths(context.Background(), "Catalog", "Контрагенты", "AccumulationRegister", "ТоварыНаСкладах", 0, 0)
	if err != nil {
		t.Fatalf("DependencyPaths: %v", err)
	}
	if len(rep.Paths) != 1 {
		t.Fatalf("paths = %+v, want 1", rep.Paths)
	}
	p := rep.Paths[0]
	// Контрагенты -> реквизит Контрагент документа -> движения по регистру.
	if p.Length != 2 {
		t.Fatalf("length = %d, want 2: %+v", p.Length, p.Steps)
	}
	if p.Steps[0].To != "Документ.РеализацияТоваровУслуг" {
		t.Errorf("first step to = %q, want the document", p.Steps[0].To)
	}
	if p.Steps[1].From != "Документ.РеализацияТоваровУслуг" || p.Steps[1].To != "РегистрНакопления.ТоварыНаСкладах" {
		t.Errorf("second step = %q -> %q", p.Steps[1].From, p.Steps[1].To)
	}
	if !strings.Contains(p.Steps[1].Via, "движения") {
		t.Errorf("via = %q, want the movements link", p.Steps[1].Via)
	}
}

func TestDependencyPathsDepthLimit(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	ctx := context.Background()
	// Товары -> ТоварыНаСкладах -> Валюты -> КурсыВалют: three steps.
	rep, err := s.DependencyPaths(ctx, "Catalog", "Товары", "InformationRegister", "КурсыВалют", 0, 0)
	if err != nil {
		t.Fatalf("DependencyPaths: %v", err)
	}
	if len(rep.Paths) != 1 || rep.Paths[0].Length != 3 {
		t.Fatalf("paths = %+v, want one of length 3", rep.Paths)
	}

	rep, err = s.DependencyPaths(ctx, "Catalog", "Товары", "InformationRegister", "КурсыВалют", 2, 0)
	if err != nil {
		t.Fatalf("DependencyPaths with maxDepth=2: %v", err)
	}
	if rep.Found != 0 || len(rep.Paths) != 0 {
		t.Errorf("maxDepth=2 must not reach КурсыВалют; got %+v", rep.Paths)
	}
	if rep.Note == "" {
		t.Error("expected a note explaining that nothing was found")
	}
}

func TestDependencyPathsSeveralShortest(t *testing.T) {
	s := NewXMLSource("testdata/dep")
	ctx := context.Background()
	rep, err := s.DependencyPaths(ctx, "Catalog", "Номенклатура", "AccumulationRegister", "Остатки", 0, 0)
	if err != nil {
		t.Fatalf("DependencyPaths: %v", err)
	}
	// Both documents reference Номенклатура and post to Остатки.
	if rep.Found != 2 {
		t.Fatalf("found = %d, want 2: %+v", rep.Found, rep.Paths)
	}
	middles := map[string]bool{}
	for _, p := range rep.Paths {
		if p.Length != 2 {
			t.Errorf("length = %d, want 2", p.Length)
		}
		middles[p.Steps[0].To] = true
	}
	if !middles["Документ.Приход"] || !middles["Документ.Расход"] {
		t.Errorf("paths must go through both documents; got %v", middles)
	}

	// maxPaths caps the answer and says so.
	rep, err = s.DependencyPaths(ctx, "Catalog", "Номенклатура", "AccumulationRegister", "Остатки", 0, 1)
	if err != nil {
		t.Fatalf("DependencyPaths with maxPaths=1: %v", err)
	}
	if rep.Found != 1 {
		t.Fatalf("found = %d, want 1", rep.Found)
	}
	if rep.Note == "" {
		t.Error("expected a note about the truncated result")
	}
}

func TestDependencyPathsUnrelated(t *testing.T) {
	s := NewXMLSource("testdata/dep")
	rep, err := s.DependencyPaths(context.Background(), "Catalog", "Номенклатура", "Catalog", "Города", 0, 0)
	if err != nil {
		t.Fatalf("DependencyPaths: %v", err)
	}
	if rep.Found != 0 {
		t.Errorf("Города is not linked to Номенклатура; got %+v", rep.Paths)
	}
}

func TestDependencyPathsRequiresArgs(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	ctx := context.Background()
	if _, err := s.DependencyPaths(ctx, "Catalog", "Товары", "AccumulationRegister", "", 0, 0); err == nil {
		t.Error("expected an error for an empty target name")
	}
	if _, err := s.DependencyPaths(ctx, "Catalog", "Товары", "Catalog", "Товары", 0, 0); err == nil {
		t.Error("expected an error when both objects are the same")
	}
}
