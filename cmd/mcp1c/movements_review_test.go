package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

func prClient(t *testing.T, ctx context.Context) *mcp.ClientSession {
	t.Helper()
	return сессияКлиента(t, options{dumpDir: prDump(t)})
}

func prDump(t *testing.T) string {
	t.Helper()
	dump, err := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "pr"))
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

// TestGetMovementsReviewMatchesPostingReview: review=true carries exactly what
// the removed posting_review tool returned (xs.PostingReview), next to the
// unchanged movements report; without review the field is absent.
func TestGetMovementsReviewMatchesPostingReview(t *testing.T) {
	ctx := context.Background()
	cs := prClient(t, ctx)
	want, err := source.NewXMLSource(prDump(t)).PostingReview(ctx, "РасходТовара")
	if err != nil {
		t.Fatalf("PostingReview: %v", err)
	}
	wantMovements, err := source.NewXMLSource(prDump(t)).Movements(ctx, "РасходТовара")
	if err != nil {
		t.Fatalf("Movements: %v", err)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_movements",
		Arguments: map[string]any{"name": "РасходТовара", "review": true},
	})
	if err != nil || res.IsError {
		t.Fatalf("get_movements review: %v %s", err, contentText(res))
	}
	var got struct {
		source.MovementsReport
		Review *source.PostingReport `json:"review"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Review == nil {
		t.Fatal("review=true, а review в ответе нет")
	}
	if !reflect.DeepEqual(*got.Review, *want) {
		t.Errorf("review отличается от прежнего posting_review:\n got %+v\nwant %+v", *got.Review, *want)
	}
	if !reflect.DeepEqual(got.MovementsReport, *wantMovements) {
		t.Errorf("отчёт о движениях изменился от review=true:\n got %+v\nwant %+v", got.MovementsReport, *wantMovements)
	}
	codes := map[string]bool{}
	for _, f := range got.Review.Findings {
		codes[f.Code] = true
	}
	if got.Review.Style != "inline" {
		t.Errorf("style = %q, want inline", got.Review.Style)
	}
	for _, code := range []string{"BalanceReadWithoutLock", "QueryInLoop", "NoWriteFlag"} {
		if !codes[code] {
			t.Errorf("review без находки %s: %+v", code, got.Review.Findings)
		}
	}

	plain, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_movements", Arguments: map[string]any{"name": "РасходТовара"},
	})
	if err != nil || plain.IsError {
		t.Fatalf("get_movements: %v %s", err, contentText(plain))
	}
	var bare map[string]any
	raw, _ = json.Marshal(plain.StructuredContent)
	_ = json.Unmarshal(raw, &bare)
	if _, has := bare["review"]; has {
		t.Error("без review=true поле review не должно приходить")
	}
}

// TestGetMovementsReviewOfflineOnly: without a dump the review path errors the
// same way the old tool did.
func TestGetMovementsReviewOfflineOnly(t *testing.T) {
	ctx := context.Background()
	res, err := scaffoldClient(t, ctx).CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_movements",
		Arguments: map[string]any{"name": "РасходТовара", "review": true},
	})
	if err == nil && !res.IsError {
		t.Errorf("get_movements review should error without a dump; got: %s", contentText(res))
	}
}
