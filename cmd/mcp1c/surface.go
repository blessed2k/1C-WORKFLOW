package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolSurface is the set of tools one server actually advertises. It is filled
// when a session initializes, from the server's own tools/list handler, and is
// the single source for everything that names tools to the model: the
// instructions and the next-step hints inside tool answers. A hint naming a
// tool this server does not have sends the model nowhere.
type toolSurface struct {
	mu    sync.RWMutex
	names map[string]bool // nil until the first initialize: nothing is filtered then
	// stderr receives the note when the tool list cannot be read.
	stderr io.Writer
}

func newToolSurface() *toolSurface {
	return &toolSurface{stderr: os.Stderr}
}

// surfaces maps a server to its surface. Tool handlers only hold the server
// they were registered on, and the indexed-tool registry passes nothing else
// (indexreg.go), so this is how a handler reaches the surface. Entries are
// dropped by the closer newServerWithCloser returns.
var surfaces sync.Map // *mcp.Server -> *toolSurface

// surfaceOf returns the surface of server, or nil for a server that was not
// built by newServerWithCloser (tests wiring single tools): nil filters nothing.
func surfaceOf(server *mcp.Server) *toolSurface {
	if v, ok := surfaces.Load(server); ok {
		return v.(*toolSurface)
	}
	return nil
}

func (s *toolSurface) record(names map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names = names
}

// has reports whether the server advertises name. Unknown (nil surface, or no
// session initialized yet) counts as present: better an extra hint than none.
func (s *toolSurface) has(name string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.names == nil || s.names[name]
}

// filter keeps the tools the server advertises, in order.
func (s *toolSurface) filter(tools []string) []string {
	if tools == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(tools), func(t string) bool { return !s.has(t) })
}

// instructionsMiddleware fills InitializeResult.Instructions from the tools
// registered on the server at the moment a session initializes, and records
// them for the next-step hints. The set is read through the server's own
// tools/list handler, so the text cannot drift from what the client sees.
func (s *toolSurface) instructionsMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || method != "initialize" {
			return res, err
		}
		initResult, ok := res.(*mcp.InitializeResult)
		if !ok {
			return res, err
		}
		ss, _ := req.GetSession().(*mcp.ServerSession)
		listed, lerr := next(ctx, "tools/list", &mcp.ListToolsRequest{Session: ss, Params: &mcp.ListToolsParams{}})
		initResult.Instructions = s.instructionsFrom(listed, lerr)
		return initResult, nil
	}
}

// instructionsFrom builds the text from a tools/list answer. When the list
// cannot be read the client still gets the full map (every line, unfiltered)
// and the reason goes to stderr: routing to a missing tool is the lesser harm
// than no routing at all.
func (s *toolSurface) instructionsFrom(listed mcp.Result, lerr error) string {
	tools, ok := listed.(*mcp.ListToolsResult)
	if lerr != nil || !ok || tools == nil {
		if lerr == nil {
			lerr = fmt.Errorf("неожиданный ответ %T", listed)
		}
		fmt.Fprintf(s.stderr, "%s: список инструментов для instructions не прочитан (%v), отдаётся полный текст без фильтра\n",
			serverName, lerr)
		return buildInstructions(func(string) bool { return true })
	}
	names := map[string]bool{}
	for _, t := range tools.Tools {
		names[t.Name] = true
	}
	s.record(names)
	return buildInstructions(func(name string) bool { return names[name] })
}
