package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestQueryAdvisorTool(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "query_advisor",
		Arguments: map[string]any{
			"text": `ВЫБРАТЬ Т.Ссылка ИЗ Справочник.Товары КАК Т
				ЛЕВОЕ СОЕДИНЕНИЕ (ВЫБРАТЬ П.Ссылка ИЗ Справочник.Товары КАК П) КАК В ПО В.Ссылка = Т.Ссылка
				ГДЕ Т.ЕдиницаИзмерения = &Е`,
		},
	})
	if err != nil {
		t.Fatalf("call query_advisor: %v", err)
	}
	if res.IsError {
		t.Fatalf("query_advisor returned a tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"JoinWithSubquery", "UnindexedFilter", "ЕдиницаИзмерения"} {
		if !strings.Contains(got, want) {
			t.Errorf("query_advisor output missing %q; got: %s", want, got)
		}
	}
}

func TestQueryAdvisorOfflineOnly(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx) // none mode
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "query_advisor",
		Arguments: map[string]any{"text": "ВЫБРАТЬ 1"},
	})
	if err == nil && !res.IsError {
		t.Errorf("query_advisor should error without a dump; got: %s", contentText(res))
	}
}
