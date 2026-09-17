package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// C0 (one active project per process): the raw dump and the index answer about
// the same project. Driven through a real in-memory MCP client, like
// idx_objectgraph_test.go.

const apModuleXML = metaBOM + `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" version="2.20">
	<CommonModule uuid="66666666-6666-6666-6666-6666666666NN">
		<Properties>
			<Name>MODNAME</Name>
			<Server>true</Server>
		</Properties>
	</CommonModule>
</MetaDataObject>`

// apWriteProject writes a one-component project whose common module holds a
// procedure and whose document are named after the project, so an answer can
// only come from one of them.
func apWriteProject(t *testing.T, root, id, suffix, uidTail string) {
	t.Helper()
	ogManifest(t, root, id)
	ogWriteProject(t, root, "Конфигурация"+suffix, "Док"+suffix, nil)
	module := "Модуль" + suffix
	xml := strings.ReplaceAll(strings.ReplaceAll(apModuleXML, "MODNAME", module), "NN", uidTail)
	metaWriteFile(t, filepath.Join(root, "cfg", "CommonModules", module+".xml"), xml)
	metaWriteFile(t, filepath.Join(root, "cfg", "CommonModules", module, "Ext", "Module.bsl"),
		metaBOM+"Процедура Процедура"+suffix+"() Экспорт\nКонецПроцедуры\n")
}

// apConnect starts a server with opts and returns a client plus an idempotent
// shutdown: a second server on the same workspace must not open the index
// while the first one still holds it.
func apConnect(t *testing.T, ctx context.Context, opts options) (*mcp.ClientSession, func()) {
	t.Helper()
	srv, closer := newServerWithCloser(opts)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-active-project", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cs.Close()
			ss.Close()
			if err := closer.Close(); err != nil {
				t.Errorf("закрытие индекса: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return cs, stop
}

// apCall calls a tool and returns the error flag and the result as JSON text.
func apCall(t *testing.T, ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if res.IsError {
		return true, contentText(res)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal: %v", name, err)
	}
	return false, string(raw)
}

type apServerInfo struct {
	Mode    string `json:"mode"`
	Source  string `json:"source"`
	Project *struct {
		IndexProject string `json:"indexProject"`
		ProjectRoot  string `json:"projectRoot"`
		DumpDir      string `json:"dumpDir"`
		Bound        bool   `json:"bound"`
		Hint         string `json:"hint"`
	} `json:"project"`
}

func apServerInfoCall(t *testing.T, ctx context.Context, cs *mcp.ClientSession) apServerInfo {
	t.Helper()
	isErr, raw := apCall(t, ctx, cs, "server_info", map[string]any{})
	if isErr {
		t.Fatalf("server_info: %s", raw)
	}
	var out apServerInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("server_info unmarshal: %v (%s)", err, raw)
	}
	if out.Project == nil {
		t.Fatalf("server_info без блока project: %s", raw)
	}
	return out
}

// TestActiveProjectDumpWinsOverRegistry is the reported scenario: projects A
// and B are registered, B is active in registry.json, the server starts with
// --dump pointing at A. Indexed tools must answer about A, and a set_dump onto
// an export no project describes must leave them with no project at all.
func TestActiveProjectDumpWinsOverRegistry(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	apWriteProject(t, rootA, "proj-a", "А", "01")
	apWriteProject(t, rootB, "proj-b", "Б", "02")

	// Register both; the last reindex leaves B active in the registry.
	cs, stop := apConnect(t, ctx, options{projectsRoot: workspaceRoot})
	ogReindex(t, ctx, cs, rootA)
	ogReindex(t, ctx, cs, rootB)
	stop()

	regFile := filepath.Join(workspaceRoot, ".mcp1c", "registry.json")
	regBefore, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !strings.Contains(string(regBefore), `"activeProject": "proj-b"`) &&
		!strings.Contains(string(regBefore), `"activeProject":"proj-b"`) {
		t.Fatalf("предусловие: в реестре активен не proj-b: %s", regBefore)
	}

	dumpA := filepath.Join(rootA, "cfg")
	cs, _ = apConnect(t, ctx, options{projectsRoot: workspaceRoot, dumpDir: dumpA})

	info := apServerInfoCall(t, ctx, cs)
	if info.Source != dumpA || info.Project.IndexProject != "proj-a" || !info.Project.Bound {
		t.Fatalf("server_info при --dump A: source=%s project=%+v", info.Source, *info.Project)
	}
	if info.Project.DumpDir != dumpA {
		t.Errorf("project.dumpDir = %s, want %s", info.Project.DumpDir, dumpA)
	}

	isErr, raw := apCall(t, ctx, cs, "find_symbol", map[string]any{"name": "ПроцедураА"})
	if isErr || !strings.Contains(raw, `"name":"ПроцедураА"`) {
		t.Fatalf("find_symbol ПроцедураА по A: err=%v %s", isErr, raw)
	}
	isErr, raw = apCall(t, ctx, cs, "find_symbol", map[string]any{"name": "ПроцедураБ"})
	if !isErr && strings.Contains(raw, `"name":"ПроцедураБ"`) {
		t.Fatalf("find_symbol нашёл символ проекта B при --dump A: %s", raw)
	}
	isErr, raw = apCall(t, ctx, cs, "get_object", map[string]any{"type": "Document", "name": "ДокА"})
	if isErr {
		t.Fatalf("get_object ДокА по A: %s", raw)
	}
	if isErr, raw = apCall(t, ctx, cs, "get_object", map[string]any{"type": "Document", "name": "ДокБ"}); !isErr {
		t.Fatalf("get_object ДокБ ответил при --dump A: %s", raw)
	}
	// The raw layer reads the same export.
	if isErr, raw = apCall(t, ctx, cs, "get_configuration_info", map[string]any{}); isErr || !strings.Contains(raw, "КонфигурацияА") {
		t.Fatalf("get_configuration_info при --dump A: err=%v %s", isErr, raw)
	}

	// set_dump onto an export that no registered project describes.
	stray, _ := filepath.Abs(filepath.Join("..", "..", "internal", "source", "testdata", "dump"))
	isErr, raw = apCall(t, ctx, cs, "set_dump", map[string]any{"path": stray})
	if isErr || !strings.Contains(raw, `"ok":true`) {
		t.Fatalf("set_dump stray: err=%v %s", isErr, raw)
	}
	var sd struct {
		IndexProject string `json:"indexProject"`
		IndexHint    string `json:"indexHint"`
	}
	if err := json.Unmarshal([]byte(raw), &sd); err != nil {
		t.Fatalf("set_dump unmarshal: %v", err)
	}
	if sd.IndexProject != "" || !strings.Contains(sd.IndexHint, "reindex projectRoot=") {
		t.Fatalf("set_dump stray: indexProject=%q indexHint=%q", sd.IndexProject, sd.IndexHint)
	}

	isErr, raw = apCall(t, ctx, cs, "find_symbol", map[string]any{"name": "ПроцедураА"})
	if !isErr || !strings.Contains(raw, "no_active_project") || !strings.Contains(raw, "reindex projectRoot=") {
		t.Fatalf("find_symbol после set_dump без проекта: err=%v %s", isErr, raw)
	}

	info = apServerInfoCall(t, ctx, cs)
	if info.Project.Bound || info.Project.IndexProject != "" || info.Project.Hint == "" || info.Project.DumpDir != stray {
		t.Fatalf("server_info после set_dump без проекта: %+v", *info.Project)
	}

	// Back onto A: bound again, and registry.json was never rewritten.
	if isErr, raw = apCall(t, ctx, cs, "set_dump", map[string]any{"path": dumpA}); isErr || !strings.Contains(raw, `"indexProject":"proj-a"`) {
		t.Fatalf("set_dump A: err=%v %s", isErr, raw)
	}
	regAfter, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !bytes.Equal(regBefore, regAfter) {
		t.Fatalf("registry.json изменился:\nбыло  %s\nстало %s", regBefore, regAfter)
	}
}

// TestActiveProjectReindexSwitchesRawDump: reindex projectRoot in a server
// started without a dump makes the project active for the raw layer too.
func TestActiveProjectReindexSwitchesRawDump(t *testing.T) {
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	rootA := t.TempDir()
	apWriteProject(t, rootA, "proj-a", "А", "01")

	cs, _ := apConnect(t, ctx, options{projectsRoot: workspaceRoot})
	info := apServerInfoCall(t, ctx, cs)
	if info.Mode != "none" || info.Project.Bound {
		t.Fatalf("server_info до reindex: mode=%s project=%+v", info.Mode, *info.Project)
	}
	ogReindex(t, ctx, cs, rootA)

	info = apServerInfoCall(t, ctx, cs)
	if info.Mode != "offline" || info.Project.IndexProject != "proj-a" || !info.Project.Bound {
		t.Fatalf("server_info после reindex: mode=%s project=%+v", info.Mode, *info.Project)
	}
	if isErr, raw := apCall(t, ctx, cs, "get_configuration_info", map[string]any{}); isErr || !strings.Contains(raw, "КонфигурацияА") {
		t.Fatalf("get_configuration_info после reindex: err=%v %s", isErr, raw)
	}
}
