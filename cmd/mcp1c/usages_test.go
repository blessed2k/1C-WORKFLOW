package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFindMetadataUsagesTool(t *testing.T) {
	ctx := context.Background()
	cs := dumpClient(t, ctx)

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "find_metadata_usages",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err != nil {
		t.Fatalf("call find_metadata_usages: %v", err)
	}
	if res.IsError {
		t.Fatalf("find_metadata_usages returned a tool error: %s", contentText(res))
	}
	got := contentText(res)
	for _, want := range []string{"РегистрНакопления.ТоварыНаСкладах", "Товар", "ЧтениеТоваров", "Read"} {
		if !strings.Contains(got, want) {
			t.Errorf("find_metadata_usages output missing %q; got: %s", want, got)
		}
	}
}

func TestFindMetadataUsagesOfflineOnly(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx) // none mode
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "find_metadata_usages",
		Arguments: map[string]any{"type": "Catalog", "name": "Товары"},
	})
	if err == nil && !res.IsError {
		t.Errorf("find_metadata_usages should error without a dump; got: %s", contentText(res))
	}
}
