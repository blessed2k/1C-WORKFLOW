package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExtensionContextTool(t *testing.T) {
	ctx := context.Background()
	// Server backed by the extension dump; baseDump passed as an argument.
	ext, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "ext"))
	base, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "base"))
	srv := newServer(options{dumpDir: ext})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "extension_context",
		Arguments: map[string]any{"baseDump": base},
	})
	if err != nil {
		t.Fatalf("call extension_context: %v", err)
	}
	if res.IsError {
		t.Fatalf("extension_context error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"тст_", "Документ.ЗаказКлиента", "ПриСозданииНаСервере", "ЗаполнитьЗначенияПоУмолчанию();"} {
		if !strings.Contains(got, want) {
			t.Errorf("extension_context missing %q; got: %s", want, got)
		}
	}
}

func TestGetMovementsTool(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_movements",
		Arguments: map[string]any{"name": "РеализацияТоваровУслуг"},
	})
	if err != nil {
		t.Fatalf("call get_movements: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_movements error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"РегистрНакопления.ТоварыНаСкладах", `"writeFlag":true`, "ВидДвижения"} {
		if !strings.Contains(got, want) {
			t.Errorf("get_movements missing %q; got: %s", want, got)
		}
	}
}

func TestRightsAuditTool(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "rights_audit",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err != nil {
		t.Fatalf("call rights_audit: %v", err)
	}
	if res.IsError {
		t.Fatalf("rights_audit error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"ЧтениеТоваров", "Товары.Родитель", "БазовыеПрава"} {
		if !strings.Contains(got, want) {
			t.Errorf("rights_audit missing %q; got: %s", want, got)
		}
	}
}

func TestRightsAuditEffectiveTool(t *testing.T) {
	ctx := context.Background()
	rights, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "rights"))
	srv := newServer(options{dumpDir: rights})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	// Without roles the answer is the object-centric audit, now carrying the
	// profiles that reach the object.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "rights_audit",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err != nil {
		t.Fatalf("call rights_audit: %v", err)
	}
	if res.IsError {
		t.Fatalf("rights_audit error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"Кладовщик", "УправлениеДоступомПереопределяемый"} {
		if !strings.Contains(got, want) {
			t.Errorf("rights_audit missing %q; got: %s", want, got)
		}
	}
	if strings.Contains(got, `"effective"`) {
		t.Errorf("without roles/profile there must be no effective section; got: %s", got)
	}

	// With a profile the effective section explains the RLS cancellation.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "rights_audit",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары", "roles": []string{"ЧтениеВсех"}, "profile": "Кладовщик"},
	})
	if err != nil {
		t.Fatalf("call rights_audit with profile: %v", err)
	}
	if res.IsError {
		t.Fatalf("rights_audit with profile error: %s", contentText(res))
	}
	got = contentText(res)
	for _, want := range []string{`"effective"`, "unrestrictedBy", "ЧтениеВсех", "складываются по ИЛИ"} {
		if !strings.Contains(got, want) {
			t.Errorf("effective section missing %q; got: %s", want, got)
		}
	}
}

func TestInspectToolsOfflineOnly(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx) // none mode
	for _, call := range []mcp.CallToolParams{
		{Name: "extension_context", Arguments: map[string]any{}},
		{Name: "get_movements", Arguments: map[string]any{"name": "X"}},
		{Name: "rights_audit", Arguments: map[string]any{"type": "Catalog", "name": "X"}},
	} {
		res, err := cs.CallTool(ctx, &call)
		if err == nil && !res.IsError {
			t.Errorf("%s should error without a dump", call.Name)
		}
	}
}
