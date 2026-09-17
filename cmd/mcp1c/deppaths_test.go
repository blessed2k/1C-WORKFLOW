package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFindDependencyPathsTool(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "find_dependency_paths",
		Arguments: map[string]any{
			"fromType": "Catalog", "fromName": "Контрагенты",
			"toType": "AccumulationRegister", "toName": "ТоварыНаСкладах",
		},
	})
	if err != nil {
		t.Fatalf("call find_dependency_paths: %v", err)
	}
	if res.IsError {
		t.Fatalf("find_dependency_paths returned a tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"Документ.РеализацияТоваровУслуг", "движения по регистру", "Справочник.Контрагенты"} {
		if !strings.Contains(got, want) {
			t.Errorf("find_dependency_paths output missing %q; got: %s", want, got)
		}
	}
}

func TestFindDependencyPathsOfflineOnly(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx) // none mode
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "find_dependency_paths",
		Arguments: map[string]any{
			"fromType": "Catalog", "fromName": "Товары",
			"toType": "AccumulationRegister", "toName": "ТоварыНаСкладах",
		},
	})
	if err == nil && !res.IsError {
		t.Errorf("find_dependency_paths should error without a dump; got: %s", contentText(res))
	}
}
