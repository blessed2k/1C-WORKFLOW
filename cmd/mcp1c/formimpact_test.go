package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFormImpactTool(t *testing.T) {
	ctx := context.Background()
	abs := func(p string) string {
		a, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", p))
		return a
	}
	srv := newServer(options{dumpDir: abs("fi/base")})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "form_impact",
		Arguments: map[string]any{
			"type": "Document", "name": "ЗаказКлиента", "form": "ФормаДокумента",
			"dumps": []string{abs("fi/base"), abs("fi/extA"), abs("fi/extB"), abs("fi/extC")},
		},
	})
	if err != nil {
		t.Fatalf("call form_impact: %v", err)
	}
	if res.IsError {
		t.Fatalf("form_impact error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"InsteadWithoutContinueOverPrint", "NameCollision", "РасширениеБ", "ПродолжитьВызов", "Печать"} {
		if !strings.Contains(got, want) {
			t.Errorf("form_impact output missing %q; got: %s", want, got)
		}
	}
}

func TestFormImpactToolOfflineOnly(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx)
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "form_impact",
		Arguments: map[string]any{"type": "Document", "name": "X", "form": "F"},
	})
	if err == nil && !res.IsError {
		t.Errorf("form_impact should error without a dump")
	}
}

// TestFormImpactToolDraft checks the draft path over the MCP protocol: the code
// is not in any export yet, but its conflicts are found.
func TestFormImpactToolDraft(t *testing.T) {
	ctx := context.Background()
	abs := func(p string) string { a, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", p)); return a }
	srv := newServer(options{dumpDir: abs("fi/base")})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "form_impact",
		Arguments: map[string]any{
			"type": "Document", "name": "ЗаказКлиента", "form": "ФормаДокумента",
			"dumps":          []string{abs("fi/base"), abs("fi/extA")},
			"draftExtension": "МоёРасширение",
			"draftPrefix":    "моё_",
			"draftCode": `&Вместо("ПриСозданииНаСервере")
Процедура моё_Вместо(Отказ, СтандартнаяОбработка)
	Элементы.Добавить("Номенклатура", Тип("ПолеФормы"), Элементы.ГруппаШапка);
КонецПроцедуры`,
		},
	})
	if err != nil {
		t.Fatalf("call form_impact: %v", err)
	}
	if res.IsError {
		t.Fatalf("form_impact error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"InsteadWithoutContinueOverPrint", "NameCollision", "МоёРасширение", "guidance"} {
		if !strings.Contains(got, want) {
			t.Errorf("draft check output missing %q; got: %s", want, got)
		}
	}
}
