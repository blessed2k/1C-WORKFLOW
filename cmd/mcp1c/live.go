package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// baseConfig describes one live 1C base (an entry in the bases JSON file).
type baseConfig struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// liveState holds the configured bases and the currently selected one. It lets a
// single server target any of many bases, switchable at runtime via set_base.
type liveState struct {
	mu      sync.RWMutex
	bases   map[string]baseConfig
	order   []string
	current *source.HTTPSource
	name    string
}

func newLiveState() *liveState {
	return &liveState{bases: make(map[string]baseConfig)}
}

func (l *liveState) add(b baseConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.bases[b.Name]; !ok {
		l.order = append(l.order, b.Name)
	}
	l.bases[b.Name] = b
}

// loadFile adds bases from a JSON array file: [{"name","url","user","password"}].
func (l *liveState) loadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // tolerate a UTF-8 BOM
	var list []baseConfig
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse bases file %s: %w", path, err)
	}
	for _, b := range list {
		if b.Name != "" && b.URL != "" {
			l.add(b)
		}
	}
	return nil
}

func (l *liveState) setBase(name string) (baseConfig, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bases[name]
	if !ok {
		return baseConfig{}, fmt.Errorf("base %q is not configured (see list_bases)", name)
	}
	l.current = source.NewHTTPSource(b.URL, b.User, b.Password)
	l.name = name
	return b, nil
}

func (l *liveState) firstName() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.order) == 0 {
		return ""
	}
	return l.order[0]
}

// count reports how many bases are configured. With more than one, the server
// deliberately starts with none selected: silently defaulting to the first base
// makes every later answer look valid while coming from the wrong database.
func (l *liveState) count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.order)
}

// names lists the configured base names, for error messages that have to tell
// the caller what to choose from.
func (l *liveState) names() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := append([]string(nil), l.order...)
	sort.Strings(out)
	return out
}

// liveFor returns a one-shot LiveSource for the named base without touching the
// active selection, so a call can name its own target instead of relying on
// server state that a reconnect may have reset.
func (l *liveState) liveFor(name string) (source.LiveSource, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	b, ok := l.bases[name]
	if !ok {
		return nil, fmt.Errorf("base %q is not configured; configured bases: %s", name, strings.Join(l.names0(), ", "))
	}
	return source.NewHTTPSource(b.URL, b.User, b.Password), nil
}

// names0 is names() without locking, for callers that already hold the lock.
func (l *liveState) names0() []string {
	out := append([]string(nil), l.order...)
	sort.Strings(out)
	return out
}

func (l *liveState) currentName() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.name
}

// source returns the current base as a ConfigSource (nil if none selected).
func (l *liveState) source() source.ConfigSource {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.current == nil {
		return nil
	}
	return l.current
}

// live returns the current base as a LiveSource (nil if none selected).
func (l *liveState) live() source.LiveSource {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.current == nil {
		return nil
	}
	return l.current
}

func (l *liveState) desc() (string, string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.current == nil {
		return "live", "(no base selected)"
	}
	return "live", l.name
}

type baseInfo struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Current bool   `json:"current,omitempty"`
}

func (l *liveState) listInfo() []baseInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()
	names := append([]string(nil), l.order...)
	sort.Strings(names)
	out := make([]baseInfo, 0, len(names))
	for _, n := range names {
		out = append(out, baseInfo{Name: n, URL: l.bases[n].URL, Current: n == l.name})
	}
	return out
}

type setBaseInput struct {
	// Optional for the same reason as set_dump's path: with no name the tool asks
	// the user which base to work on rather than guessing or failing.
	Name string `json:"name,omitempty" jsonschema:"name of a configured base (see list_bases); omit it to be asked"`
}

type setBaseOutput struct {
	OK      bool   `json:"ok"`
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
}

type listBasesOutput struct {
	Current string     `json:"current,omitempty"`
	Count   int        `json:"count"`
	Bases   []baseInfo `json:"bases"`
}

// registerBaseSwitching wires list_bases and set_base for multi-base live mode.
func registerBaseSwitching(server *mcp.Server, ls *liveState) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_bases",
		Description: "Lists the configured live 1C bases (name and URL, passwords never shown). Switch the active one with set_base.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listBasesOutput, error) {
		bases := ls.listInfo()
		return nil, listBasesOutput{Current: ls.currentName(), Count: len(bases), Bases: bases}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_base",
		Description: "Switch the active live base to a configured one by name (see list_bases). All live tools then target it. If the name is unknown, call it without arguments: a client that supports elicitation will ask the user which base to take. Picking the base is the user's call — a wrong guess means working against the wrong 1C database.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in setBaseInput) (*mcp.CallToolResult, setBaseOutput, error) {
		if in.Name == "" && req != nil {
			names := make([]string, 0)
			for _, b := range ls.listInfo() {
				names = append(names, b.Name)
			}
			picked, err := elicitChoice(ctx, req.Session, "base",
				"Не выбрана рабочая база 1С. С какой работаем?", "База", names)
			if err != nil {
				return nil, setBaseOutput{Message: err.Error()}, nil
			}
			in.Name = picked
		}
		b, err := ls.setBase(in.Name)
		if err != nil {
			return nil, setBaseOutput{Message: err.Error()}, nil
		}
		return nil, setBaseOutput{OK: true, Name: b.Name, URL: b.URL}, nil
	})
}
