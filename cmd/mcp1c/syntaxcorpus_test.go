package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/onec"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// connectTools wires the tools registered by register onto a bare server and
// returns a connected client session.
func connectTools(t *testing.T, register func(*mcp.Server)) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	register(srv)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// brokenCorpus stands in for a lazily built index whose corpus failed to parse:
// the lookups answer, exactly as the real ones do after a failed parse — with
// nothing, indistinguishable from a miss — and only Err knows why. The lookups
// here answer with a hit on purpose: then the only thing that can turn either
// tool into an error is the tool asking Err, and dropping that question makes
// this test fail instead of leaving it green.
type brokenCorpus struct{ err error }

func (b brokenCorpus) Err() error { return b.err }

func (b brokenCorpus) Search(string, int) []onec.SyntaxEntry {
	return []onec.SyntaxEntry{{NameRu: "Сообщить", Kind: "method", Signature: "Сообщить(<ТекстСообщения>)"}}
}

func (b brokenCorpus) GlobalMethod(string) (onec.SyntaxEntry, bool) {
	return onec.SyntaxEntry{NameRu: "Сообщить", Kind: "method", Signature: "Сообщить(<ТекстСообщения>)"}, true
}

// TestBrokenSyntaxCorpusIsAToolError: the corpus is parsed on first use, so the
// index value is never nil and a broken corpus can no longer be spotted by
// looking at the pointer. A tool that leans on the corpus has to ask, because an
// unparsed reference answers "not found" to everything — a wrong answer wearing
// the clothes of a correct one.
func TestBrokenSyntaxCorpusIsAToolError(t *testing.T) {
	ctx := context.Background()
	corpus := brokenCorpus{err: errors.New("gzip: invalid header")}
	cs := connectTools(t, func(srv *mcp.Server) {
		registerCoreTools(srv, func() (string, string) { return "none", "" }, nil, func() source.ConfigSource { return nil }, corpus)
		registerValidateBSL(srv, func() source.ConfigSource { return nil }, corpus)
	})

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"bsl_syntax", map[string]any{"query": "Сообщить"}},
		{"validate_bsl", map[string]any{"code": "Сообщить(1);"}},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		if err == nil && !res.IsError {
			t.Errorf("%s answered from a corpus that failed to parse: %s", tc.tool, contentText(res))
			continue
		}
		msg := contentText(res)
		if err != nil {
			msg = err.Error()
		}
		if !strings.Contains(msg, "syntax") || !strings.Contains(msg, "gzip: invalid header") {
			t.Errorf("%s: the error hides what went wrong with the corpus: %s", tc.tool, msg)
		}
	}
}

// TestHealthySyntaxCorpusAnswers: the guard must cost nothing to a corpus that is
// fine — otherwise the cheapest way to pass the test above is to fail always.
func TestHealthySyntaxCorpusAnswers(t *testing.T) {
	ctx := context.Background()
	corpus := brokenCorpus{err: nil}
	cs := connectTools(t, func(srv *mcp.Server) {
		registerCoreTools(srv, func() (string, string) { return "none", "" }, nil, func() source.ConfigSource { return nil }, corpus)
	})

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "bsl_syntax", Arguments: map[string]any{"query": "Сообщить"}})
	if err != nil {
		t.Fatalf("call bsl_syntax: %v", err)
	}
	if res.IsError {
		t.Fatalf("bsl_syntax refused a healthy corpus: %s", contentText(res))
	}
	if got := contentText(res); !strings.Contains(got, "Сообщить(") {
		t.Errorf("bsl_syntax lost the answer; got: %s", got)
	}
}

// connectServer connects a client to a server built by newServer.
func connectServer(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// TestNewServerReadsSyntaxIndexLazily: the wiring must not read the index. The
// file appears only after newServer returned, and bsl_syntax still answers from
// it; a read at wiring time would have cached "not found" for the whole process.
func TestNewServerReadsSyntaxIndexLazily(t *testing.T) {
	path := filepath.Join(t.TempDir(), "syntax-index.json")
	srv := newServer(options{syntaxIndex: path})

	data, err := os.ReadFile(syntaxtest.FixtureFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cs := connectServer(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "bsl_syntax", Arguments: map[string]any{"query": "СтрНайти"}})
	if err != nil {
		t.Fatalf("call bsl_syntax: %v", err)
	}
	if res.IsError || !strings.Contains(contentText(res), "СтрНайти(") {
		t.Fatalf("bsl_syntax did not answer from an index written after newServer: %s", contentText(res))
	}
}

// TestServerWithoutSyntaxIndex: no index file is not a startup failure. The
// tools that need the corpus answer with an error naming the generator and the
// flag; the rest of the server works.
func TestServerWithoutSyntaxIndex(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "absent.json.gz")
	cs := connectServer(t, newServer(options{syntaxIndex: missing}))

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"bsl_syntax", map[string]any{"query": "Сообщить"}},
		{"validate_bsl", map[string]any{"code": "Сообщить(1);"}},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		msg := ""
		switch {
		case err != nil:
			msg = err.Error()
		case res.IsError:
			msg = contentText(res)
		default:
			t.Errorf("%s answered without an index: %s", tc.tool, contentText(res))
			continue
		}
		for _, want := range []string{"cmd/syntaxgen", "--syntax-index", missing} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: the error does not name %q: %s", tc.tool, want, msg)
			}
		}
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("server_info failed without a syntax index: %v", err)
	}
	if res.IsError {
		t.Fatalf("server_info failed without a syntax index: %s", contentText(res))
	}
}

// TestSyntaxIndexPathResolution: flag or env value first, the user config
// default otherwise.
func TestSyntaxIndexPathResolution(t *testing.T) {
	if got := (options{syntaxIndex: "x.json.gz"}).syntaxIndexPath(); got != "x.json.gz" {
		t.Errorf("explicit path = %q", got)
	}
	if got := (options{}).syntaxIndexPath(); got != syntax.DefaultPath() {
		t.Errorf("default path = %q, want %q", got, syntax.DefaultPath())
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	t.Setenv(syntax.EnvPath, "from-env.json.gz")
	var o options
	registerSyntaxIndexFlag(fs, &o)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if o.syntaxIndexPath() != "from-env.json.gz" {
		t.Errorf("env path = %q", o.syntaxIndexPath())
	}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	o = options{}
	registerSyntaxIndexFlag(fs, &o)
	if err := fs.Parse([]string{"--syntax-index", "from-flag.json.gz"}); err != nil {
		t.Fatal(err)
	}
	if o.syntaxIndexPath() != "from-flag.json.gz" {
		t.Errorf("flag over env = %q", o.syntaxIndexPath())
	}
}
