package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// TestTraceCallGraphDirectionSynonyms: in/out are accepted as callers/callees
// and give the same answer; anything else is an input error that lists every
// accepted value.
func TestTraceCallGraphDirectionSynonyms(t *testing.T) {
	p, _ := newBSLFixtureProject(t, "graph-direction")
	symSvc, graphSvc := NewSymbolService(p), NewGraphService(p)
	ctx := context.Background()
	help, err := symSvc.FindSymbol(ctx, FindSymbolInput{Name: "Помощь"})
	if err != nil || len(help.Items) != 1 {
		t.Fatalf("FindSymbol(Помощь): %+v %v", help.Items, err)
	}
	uid := help.Items[0].UID
	for _, pair := range [][2]string{{"in", "callers"}, {"out", "callees"}, {"IN", "callers"}} {
		got, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: uid, Direction: pair[0], Depth: 1})
		if err != nil {
			t.Fatalf("direction=%s: %v", pair[0], err)
		}
		want, err := graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: uid, Direction: pair[1], Depth: 1})
		if err != nil {
			t.Fatalf("direction=%s: %v", pair[1], err)
		}
		if !reflect.DeepEqual(got.Items, want.Items) {
			t.Errorf("direction=%s отличается от %s:\n got %+v\nwant %+v", pair[0], pair[1], got.Items, want.Items)
		}
	}
	_, err = graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: uid, Direction: "sideways"})
	assertDirectionInputError(t, err, "sideways", "callers", "callees", "in", "out")
	// both: слово объектного графа без пары в графе вызовов.
	_, err = graphSvc.TraceCallGraph(ctx, TraceCallGraphInput{UID: uid, Direction: "both"})
	assertDirectionInputError(t, err, "both", "callers", "callees")
}

// TestObjectGraphDirectionSynonyms: callers/callees are accepted as in/out;
// an unknown value is an input error listing every accepted value.
func TestObjectGraphDirectionSynonyms(t *testing.T) {
	svc, ids := buildObjectGraphFixture(t)
	ctx := context.Background()
	for _, pair := range [][2]string{{"callers", store.EdgeDirectionIn}, {"callees", store.EdgeDirectionOut}} {
		got, err := svc.Radius(ctx, RadiusInput{Target: ObjectTarget{ObjectID: ids.o2}, Direction: pair[0]})
		if err != nil {
			t.Fatalf("direction=%s: %v", pair[0], err)
		}
		want, err := svc.Radius(ctx, RadiusInput{Target: ObjectTarget{ObjectID: ids.o2}, Direction: pair[1]})
		if err != nil {
			t.Fatalf("direction=%s: %v", pair[1], err)
		}
		if !reflect.DeepEqual(got.Items, want.Items) || got.TotalCount != want.TotalCount {
			t.Errorf("direction=%s отличается от %s:\n got %+v\nwant %+v", pair[0], pair[1], got.Items, want.Items)
		}
	}
	_, err := svc.Radius(ctx, RadiusInput{Target: ObjectTarget{ObjectID: ids.o2}, Direction: "up"})
	assertDirectionInputError(t, err, "up", "in", "out", "both", "callers", "callees")
}

func assertDirectionInputError(t *testing.T, err error, bad string, accepted ...string) {
	t.Helper()
	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatalf("err = %v (%T), want *app.Error", err, err)
	}
	if appErr.Code != CodeInvalidArgument {
		t.Errorf("code = %s, want %s", appErr.Code, CodeInvalidArgument)
	}
	if !strings.Contains(appErr.Message, `"`+bad+`"`) {
		t.Errorf("Message = %q, want the rejected value", appErr.Message)
	}
	for _, a := range accepted {
		if !strings.Contains(appErr.Hint, a) {
			t.Errorf("Hint = %q, want %q listed", appErr.Hint, a)
		}
	}
}
