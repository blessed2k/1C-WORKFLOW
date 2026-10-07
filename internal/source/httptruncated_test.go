package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// answerServer answers every request with the given body.
func answerServer(t *testing.T, answer map[string]any) *HTTPSource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return NewHTTPSource(srv.URL, "", "")
}

func logAnswer(truncated bool) map[string]any {
	entry := map[string]any{"date": "2026-08-28T12:14:00", "level": "Информация", "event": "Рассылка документов"}
	return map[string]any{"events": []any{entry, entry}, "total": 2, "truncated": truncated, "applied": []string{"order"}}
}

// TestEventLogSaysWhenTheWindowIsCut: an agent read the first 500 entries of a
// day, saw the morning run finish and concluded the whole day went fine; the
// failure was in the afternoon, past the cut. A bare truncated=true did not stop
// it, so a cut answer says in words what it is and what not to conclude from it.
func TestEventLogSaysWhenTheWindowIsCut(t *testing.T) {
	ctx := context.Background()

	first, err := answerServer(t, logAnswer(true)).EventLog(ctx, EventLogParams{Order: "asc", Limit: 2})
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	for _, want := range []string{"первые 2", "не весь", "offset"} {
		if !strings.Contains(first.Note, want) {
			t.Errorf("note of a cut window read from the start lacks %q: %q", want, first.Note)
		}
	}

	last, err := answerServer(t, logAnswer(true)).EventLog(ctx, EventLogParams{Order: "desc", Limit: 2})
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	if !strings.Contains(last.Note, "последние 2") {
		t.Errorf("note of a cut window read from the end does not say so: %q", last.Note)
	}

	whole, err := answerServer(t, logAnswer(false)).EventLog(ctx, EventLogParams{Order: "asc", Limit: 2})
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	if whole.Note != "" {
		t.Errorf("a window read in full needs no note, got %q", whole.Note)
	}
}

// TestQuerySaysWhenRowsAreCut: the same trap for rows: a total or "nothing else
// is there" drawn from the first rows of a cut result.
func TestQuerySaysWhenRowsAreCut(t *testing.T) {
	ctx := context.Background()
	answer := func(truncated bool) map[string]any {
		return map[string]any{"columns": []string{"Номер"}, "rows": []any{[]any{"1"}, []any{"2"}}, "total": 2, "truncated": truncated}
	}

	cut, err := answerServer(t, answer(true)).ExecuteQuery(ctx, QueryParams{Text: "ВЫБРАТЬ Номер ИЗ Документ.Счет", Limit: 2})
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	for _, want := range []string{"первые 2", "больше", "limit"} {
		if !strings.Contains(cut.Note, want) {
			t.Errorf("note of a cut result lacks %q: %q", want, cut.Note)
		}
	}

	whole, err := answerServer(t, answer(false)).ExecuteQuery(ctx, QueryParams{Text: "ВЫБРАТЬ Номер ИЗ Документ.Счет"})
	if err != nil {
		t.Fatalf("ExecuteQuery: %v", err)
	}
	if whole.Note != "" {
		t.Errorf("a full result needs no note, got %q", whole.Note)
	}
}
