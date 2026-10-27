package source

import "context"

// Paths of the write actions of the MCPЗапись extension (HTTP service with
// RootURL mcp-write). The extension lives outside this repository; its exchange
// is described in connector/README.md, section «Запись».
const (
	WritePathCreate         = "/create"
	WritePathUpdate         = "/update"
	WritePathPost           = "/post"
	WritePathUnpost         = "/unpost"
	WritePathDeletionMark   = "/deletion-mark"
	WritePathConstant       = "/constant"
	WritePathRegisterWrite  = "/register-write"
	WritePathRegisterDelete = "/register-delete"
)

// WriteClient sends typed write actions to the MCPЗапись extension of a test
// base. It shares the transport with HTTPSource: basic auth, the
// X-MCP-Errors: envelope header and ConnectorError for a failure the extension
// reported (bad input 400, missing object 404, locked object 409, exception 500).
// A failed fill check or a refused write is not a failure: it comes back in the
// answer (ok=false, messages, write_error).
type WriteClient struct {
	http *HTTPSource
}

// NewWriteClient returns a client for the published write service root, e.g.
// http://host/base/hs/mcp-write.
func NewWriteClient(base, user, pass string) *WriteClient {
	return &WriteClient{http: NewHTTPSource(base, user, pass)}
}

// Do posts body to one of the WritePath* actions and decodes the answer into out.
func (c *WriteClient) Do(ctx context.Context, path string, body, out any) error {
	return c.http.post(ctx, path, body, out)
}
