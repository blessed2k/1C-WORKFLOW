package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestChecklistTool(t *testing.T) {
	ctx := context.Background()
	res, err := visClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "new_object_checklist",
		Arguments: map[string]any{"objectType": "Document", "name": "ЗаказПоставщику", "like": "ЗаказКлиента"},
	})
	if err != nil {
		t.Fatalf("call new_object_checklist: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"Подсистемы", "Журналы документов", "не сделано"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got: %s", want, got)
		}
	}
}

func TestChecklistOfflineOnly(t *testing.T) {
	ctx := context.Background()
	res, err := scaffoldClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "new_object_checklist",
		Arguments: map[string]any{"objectType": "Document", "name": "А", "like": "Б"},
	})
	if err == nil && !res.IsError {
		t.Errorf("new_object_checklist should error without a dump; got: %s", contentText(res))
	}
}
