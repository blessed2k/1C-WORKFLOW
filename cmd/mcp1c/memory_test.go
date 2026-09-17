package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
)

// sampleCacheStats: three entries, hand-summed — 900 bytes over a 10-minute TTL
// and a 1 MiB ceiling. The order is the one CacheSnapshot documents (root, then
// kind), because that is the order the block is allowed to lean on when dosing.
func sampleCacheStats() source.CacheStats {
	return source.CacheStats{
		TTL:        10 * time.Minute,
		LimitBytes: 1 << 20,
		TotalBytes: 900,
		Entries: []source.CacheEntryStat{
			{Root: "/dumps/erp", Kind: "options", Bytes: 100, Idle: time.Hour},
			{Root: "/dumps/ut_demo", Kind: "roles", Bytes: 500, Idle: 90 * time.Second},
			{Root: "/dumps/ut_demo", Kind: "subscriptions", Bytes: 300, Idle: 2 * time.Second},
		},
	}
}

// TestMemoryBlockReportsRSSUptimeAndCache covers the three things the brief asks
// server_info to show, on a cache whose numbers are known by hand.
func TestMemoryBlockReportsRSSUptimeAndCache(t *testing.T) {
	got := buildMemory(52428800, "current", true, 90*time.Second, sampleCacheStats())

	if got.RSSBytes != 52428800 || got.RSSKind != "current" {
		t.Errorf("rss = %d/%q, want 52428800/current", got.RSSBytes, got.RSSKind)
	}
	if got.Uptime != "1m30s" {
		t.Errorf("uptime = %q, want 1m30s", got.Uptime)
	}
	if got.Go != nil {
		t.Errorf("runtime numbers must stay out while the platform gives RSS; got %+v", got.Go)
	}
	if got.Cache.Entries != 3 || got.Cache.TotalBytes != 900 {
		t.Errorf("cache summary = %d entries / %d bytes, want 3 / 900", got.Cache.Entries, got.Cache.TotalBytes)
	}
	if got.Cache.TTL != "10m0s" || got.Cache.LimitBytes != 1<<20 {
		t.Errorf("cache tunables = %q / %d, want 10m0s / %d", got.Cache.TTL, got.Cache.LimitBytes, int64(1<<20))
	}
	if len(got.Cache.Shown) != 3 || got.Cache.Truncated {
		t.Fatalf("three entries must be shown whole; got %d, truncated=%v", len(got.Cache.Shown), got.Cache.Truncated)
	}
	first := got.Cache.Shown[0]
	if first.Project != "/dumps/erp" || first.Kind != "options" || first.Bytes != 100 || first.Idle != "1h0m0s" {
		t.Errorf("first shown entry = %+v, want the /dumps/erp options entry with idle 1h0m0s", first)
	}
}

// TestMemoryBlockOnEmptyCache: the block is not conditional on the cache having
// anything in it — a cold server still answers with uptime and an empty list.
func TestMemoryBlockOnEmptyCache(t *testing.T) {
	got := buildMemory(1024, "current", true, time.Second, source.CacheStats{})
	if got.Cache.Entries != 0 || got.Cache.TotalBytes != 0 {
		t.Errorf("empty cache summary = %+v, want zeros", got.Cache)
	}
	if got.Cache.Shown == nil {
		t.Error("shown must be an empty list, not null: the field is always there")
	}
	if got.Uptime != "1s" {
		t.Errorf("uptime = %q, want 1s", got.Uptime)
	}
}

// TestMemoryBlockWithoutRSS: on a platform that does not hand out RSS cheaply the
// field is absent — and portable Go numbers take its place, so the block still
// says something about memory (story 14).
func TestMemoryBlockWithoutRSS(t *testing.T) {
	got := buildMemory(0, "", false, time.Minute, sampleCacheStats())
	if got.RSSBytes != 0 || got.RSSKind != "" {
		t.Errorf("rss must be absent, got %d/%q", got.RSSBytes, got.RSSKind)
	}
	if got.Go == nil {
		t.Fatal("without RSS the block must carry the portable runtime numbers")
	}
	if got.Go.HeapAllocBytes <= 0 || got.Go.SysBytes <= 0 {
		t.Errorf("runtime numbers look unread: %+v", got.Go)
	}
	if got.Cache.Entries != 3 {
		t.Errorf("cache summary lost with the RSS: %+v", got.Cache)
	}
}

// TestMemoryBlockIsDosed: server_info is read by a model, so a cache holding one
// entry per collection per export must not turn the answer into a dump. What is
// dropped is said out loud, in the count and in the note.
func TestMemoryBlockIsDosed(t *testing.T) {
	stats := source.CacheStats{TotalBytes: 4200}
	for i := 0; i < 42; i++ {
		stats.Entries = append(stats.Entries, source.CacheEntryStat{Root: "/dumps/ut_demo", Kind: string(rune('a' + i%26)), Bytes: 100})
	}
	got := buildMemory(1024, "current", true, time.Second, stats)

	if got.Cache.Entries != 42 {
		t.Errorf("summary must still count every entry: %d, want 42", got.Cache.Entries)
	}
	if len(got.Cache.Shown) >= 42 {
		t.Fatalf("the whole cache reached the answer: %d rows", len(got.Cache.Shown))
	}
	if !got.Cache.Truncated {
		t.Error("dropped rows must be flagged")
	}
	if !strings.Contains(got.Cache.Note, "42") {
		t.Errorf("the note must say how much was left out; got %q", got.Cache.Note)
	}
}

// TestServerInfoCarriesMemoryBlock: the block has to survive the trip through the
// MCP layer, on a server with nothing configured — an idle process is exactly
// when someone asks where the memory went.
func TestServerInfoCarriesMemoryBlock(t *testing.T) {
	ctx := context.Background()
	cs := connectTools(t, func(srv *mcp.Server) {
		registerCoreTools(srv, func() (string, string) { return "none", "" }, nil, func() source.ConfigSource { return nil }, syntax.NewLazy(syntaxtest.FixtureFile(t)))
	})

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call server_info: %v", err)
	}
	if res.IsError {
		t.Fatalf("server_info returned a tool error: %s", contentText(res))
	}
	var got serverInfoOutput
	if err := json.Unmarshal([]byte(contentText(res)), &got); err != nil {
		t.Fatalf("decode server_info: %v; body: %s", err, contentText(res))
	}
	if got.Memory.Uptime == "" {
		t.Error("memory block without uptime")
	}
	if got.Memory.Cache.Shown == nil {
		t.Error("memory block without the cache list")
	}
	if got.Memory.RSSBytes == 0 && got.Memory.Go == nil {
		t.Error("neither RSS nor the portable runtime numbers: the block says nothing about memory")
	}
	if got.Memory.RSSBytes != 0 && got.Memory.RSSKind == "" {
		t.Error("an RSS number without its kind: current and peak are not interchangeable")
	}
}
