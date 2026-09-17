package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// elicitingClient connects a client whose elicitation handler is handle. Passing
// nil handle means a client WITHOUT the capability, which is the case that must
// keep the previous behaviour.
func elicitingClient(t *testing.T, ctx context.Context, opts options,
	handle func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	srv := newServer(opts)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })

	var copts *mcp.ClientOptions
	if handle != nil {
		copts = &mcp.ClientOptions{ElicitationHandler: handle}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "9.9"}, copts)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestServerInfoReportsClientElicitation(t *testing.T) {
	ctx := context.Background()

	withCap := elicitingClient(t, ctx, options{}, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "cancel"}, nil
	})
	res, err := withCap.CallTool(ctx, &mcp.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call server_info: %v", err)
	}
	got := contentText(res)
	if !strings.Contains(got, `"supported":true`) {
		t.Errorf("server_info must report the declared elicitation capability; got: %s", got)
	}
	if !strings.Contains(got, "test-client") {
		t.Errorf("server_info must name the client; got: %s", got)
	}

	without := elicitingClient(t, ctx, options{}, nil)
	res, err = without.CallTool(ctx, &mcp.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call server_info: %v", err)
	}
	if got := contentText(res); !strings.Contains(got, `"supported":false`) {
		t.Errorf("a client without the capability must be reported as such; got: %s", got)
	}
}

func TestSetDumpElicitsPath(t *testing.T) {
	ctx := context.Background()
	root, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata"))

	var asked *mcp.ElicitParams
	cs := elicitingClient(t, ctx, options{projectsRoot: root},
		func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			asked = req.Params
			dump, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "dump"))
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"path": dump}}, nil
		})

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_dump", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call set_dump: %v", err)
	}
	got := contentText(res)
	if !strings.Contains(got, `"ok":true`) || !strings.Contains(got, "ДемоКонфигурация") {
		t.Fatalf("set_dump must apply the elicited path; got: %s", got)
	}
	if asked == nil {
		t.Fatal("the client was never asked")
	}
	// The prompt must offer the discovered exports, not just a blank field.
	schema, _ := asked.RequestedSchema.(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	path, _ := props["path"].(map[string]any)
	if _, hasEnum := path["enum"]; !hasEnum {
		t.Errorf("projects were discoverable under --projects-root, so the prompt must offer them: %+v", path)
	}
}

func TestSetDumpDeclinedKeepsPreviousState(t *testing.T) {
	ctx := context.Background()
	cs := elicitingClient(t, ctx, options{}, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	})

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_dump", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call set_dump: %v", err)
	}
	got := contentText(res)
	if strings.Contains(got, `"ok":true`) {
		t.Errorf("a declined prompt must not switch the source; got: %s", got)
	}

	// And the source is still none.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call server_info: %v", err)
	}
	if got := contentText(res); !strings.Contains(got, `"mode":"none"`) {
		t.Errorf("mode must stay none; got: %s", got)
	}
}

func TestSetBaseElicitsName(t *testing.T) {
	ctx := context.Background()
	basesFile := filepath.Join(t.TempDir(), "bases.json")
	body := `[{"name":"wms","url":"http://wms"},{"name":"ut","url":"http://ut"}]`
	if err := os.WriteFile(basesFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var offered []any
	cs := elicitingClient(t, ctx, options{basesFile: basesFile},
		func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			schema, _ := req.Params.RequestedSchema.(map[string]any)
			props, _ := schema["properties"].(map[string]any)
			name, _ := props["base"].(map[string]any)
			offered, _ = name["enum"].([]any)
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"base": "ut"}}, nil
		})

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_base", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call set_base: %v", err)
	}
	got := contentText(res)
	if !strings.Contains(got, `"ok":true`) || !strings.Contains(got, "http://ut") {
		t.Fatalf("set_base must apply the elicited base; got: %s", got)
	}
	if len(offered) != 2 {
		t.Errorf("both configured bases must be offered; got %v", offered)
	}
}

func TestSetDumpWithoutElicitationBehavesAsBefore(t *testing.T) {
	ctx := context.Background()
	cs := elicitingClient(t, ctx, options{}, nil) // client without the capability

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_dump", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call set_dump: %v", err)
	}
	got := contentText(res)
	if strings.Contains(got, `"ok":true`) {
		t.Fatalf("set_dump without a path must not succeed; got: %s", got)
	}
	if !strings.Contains(got, "list_projects") {
		t.Errorf("the message must tell the model how to recover; got: %s", got)
	}
}

// TestSetDumpWithoutPathNamesListProjectsOnlyWhenRegistered: under
// --tools=core list_projects is absent, and the message must not route to it.
func TestSetDumpWithoutPathNamesListProjectsOnlyWhenRegistered(t *testing.T) {
	ctx := context.Background()
	cs := elicitingClient(t, ctx, options{toolsProfile: profileCore}, nil)
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "set_dump", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call set_dump: %v", err)
	}
	got := contentText(res)
	if strings.Contains(got, "list_projects") {
		t.Errorf("core has no list_projects, the message must not name it: %s", got)
	}
	if !strings.Contains(got, "Configuration.xml") {
		t.Errorf("the message must still say what to pass: %s", got)
	}
}
