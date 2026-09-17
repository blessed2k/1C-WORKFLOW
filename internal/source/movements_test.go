package source

import (
	"context"
	"strings"
	"testing"
)

func TestMovements(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	rep, err := s.Movements(context.Background(), "РеализацияТоваровУслуг")
	if err != nil {
		t.Fatalf("Movements: %v", err)
	}
	if rep.Document != "Документ.РеализацияТоваровУслуг" {
		t.Errorf("Document = %q", rep.Document)
	}
	if len(rep.Registers) != 2 {
		t.Fatalf("registers = %d: %+v", len(rep.Registers), rep.Registers)
	}

	byName := map[string]RegisterMovement{}
	for _, r := range rep.Registers {
		byName[r.Register] = r
	}

	tovary := byName["РегистрНакопления.ТоварыНаСкладах"]
	if !tovary.Declared || !tovary.UsedInCode || !tovary.WriteFlag {
		t.Errorf("ТоварыНаСкладах flags = %+v", tovary)
	}
	// The variable Движение is reused for Продажи below in the module: fields
	// must NOT leak between registers (Сумма belongs to Продажи only).
	want := map[string]bool{"ВидДвижения": true, "Период": true, "Товар": true, "Количество": true}
	if len(tovary.FieldsSet) != len(want) {
		t.Errorf("ТоварыНаСкладах fields = %v, want %v", tovary.FieldsSet, want)
	}
	for _, f := range tovary.FieldsSet {
		if !want[f] {
			t.Errorf("field %q leaked into ТоварыНаСкладах", f)
		}
	}

	sales := byName["РегистрНакопления.Продажи"]
	if !sales.Declared || !sales.UsedInCode || !sales.WriteFlag {
		t.Errorf("Продажи flags = %+v", sales)
	}
	wantSales := map[string]bool{"Период": true, "Сумма": true}
	if len(sales.FieldsSet) != len(wantSales) {
		t.Errorf("Продажи fields = %v, want %v", sales.FieldsSet, wantSales)
	}
	for _, f := range sales.FieldsSet {
		if !wantSales[f] {
			t.Errorf("field %q leaked into Продажи", f)
		}
	}

	// Commented-out register and collection methods must not become registers.
	for reg := range byName {
		if strings.Contains(reg, "СтарыйРегистр") || strings.Contains(reg, "Количество") {
			t.Errorf("phantom register %q", reg)
		}
	}
}

func TestMovementsNoModule(t *testing.T) {
	// ЗаказКлиента exists only in testdata/ext without RegisterRecords/module.
	s := NewXMLSource("testdata/ext")
	rep, err := s.Movements(context.Background(), "ЗаказКлиента")
	if err != nil {
		t.Fatalf("Movements: %v", err)
	}
	if len(rep.Registers) != 0 {
		t.Errorf("expected no registers, got %+v", rep.Registers)
	}
}

func TestMovementsRequiresName(t *testing.T) {
	s := NewXMLSource("testdata/dump")
	if _, err := s.Movements(context.Background(), ""); err == nil {
		t.Error("expected error for empty name")
	}
}
