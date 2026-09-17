package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResourceStandardsNaming(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx)

	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "onec://standards/naming"})
	if err != nil {
		t.Fatalf("read onec://standards/naming: %v", err)
	}
	if len(res.Contents) == 0 || !strings.Contains(res.Contents[0].Text, "ПрограммныйИнтерфейс") {
		t.Errorf("naming resource missing region convention; got: %+v", res.Contents)
	}
}

func TestResourceQueryCheatsheet(t *testing.T) {
	ctx := context.Background()
	cs := scaffoldClient(t, ctx)

	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "onec://query-lang/cheatsheet"})
	if err != nil {
		t.Fatalf("read onec://query-lang/cheatsheet: %v", err)
	}
	if len(res.Contents) == 0 || !strings.Contains(res.Contents[0].Text, "ЕСТЬNULL") {
		t.Errorf("cheatsheet resource missing ЕСТЬNULL; got: %+v", res.Contents)
	}
}
