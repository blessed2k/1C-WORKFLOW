package main

import (
	"flag"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// resolveCacheFlags binds the export-cache tunables to a private FlagSet and
// parses args, the way the entrypoint does over flag.CommandLine.
func resolveCacheFlags(t *testing.T, args ...string) options {
	t.Helper()
	var o options
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	registerCacheFlags(fs, &o)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return o
}

// TestCacheTunableDefaults pins the two values named in the brief: 10 minutes of
// idle TTL and a 512 MiB ceiling, with neither flag nor env set.
func TestCacheTunableDefaults(t *testing.T) {
	o := resolveCacheFlags(t)
	if o.cacheTTL != 10*time.Minute {
		t.Errorf("default cacheTTL = %v, want 10m", o.cacheTTL)
	}
	if o.cacheLimitBytes != 536870912 {
		t.Errorf("default cacheLimitBytes = %d, want 536870912 (512 MiB)", o.cacheLimitBytes)
	}
}

func TestCacheTunablesFromFlags(t *testing.T) {
	o := resolveCacheFlags(t, "--cache-ttl=90s", "--cache-limit=1048576")
	if o.cacheTTL != 90*time.Second {
		t.Errorf("cacheTTL = %v, want 90s", o.cacheTTL)
	}
	if o.cacheLimitBytes != 1048576 {
		t.Errorf("cacheLimitBytes = %d, want 1048576", o.cacheLimitBytes)
	}
}

func TestCacheTunablesFromEnv(t *testing.T) {
	t.Setenv("MCP_1C_CACHE_TTL", "45s")
	t.Setenv("MCP_1C_CACHE_LIMIT", "2048")
	o := resolveCacheFlags(t)
	if o.cacheTTL != 45*time.Second {
		t.Errorf("cacheTTL = %v, want 45s from MCP_1C_CACHE_TTL", o.cacheTTL)
	}
	if o.cacheLimitBytes != 2048 {
		t.Errorf("cacheLimitBytes = %d, want 2048 from MCP_1C_CACHE_LIMIT", o.cacheLimitBytes)
	}
}

// TestCacheFlagOverridesEnv: the flag is the more explicit of the two, so it wins.
func TestCacheFlagOverridesEnv(t *testing.T) {
	t.Setenv("MCP_1C_CACHE_TTL", "45s")
	o := resolveCacheFlags(t, "--cache-ttl=2m")
	if o.cacheTTL != 2*time.Minute {
		t.Errorf("cacheTTL = %v, want 2m (flag over env)", o.cacheTTL)
	}
}

// TestCacheTunablesIgnoreUnparsableEnv: a typo in the environment must not take
// the server down at startup, and must not silently mean "disabled" either.
func TestCacheTunablesIgnoreUnparsableEnv(t *testing.T) {
	t.Setenv("MCP_1C_CACHE_TTL", "ten minutes")
	t.Setenv("MCP_1C_CACHE_LIMIT", "512MiB")
	o := resolveCacheFlags(t)
	if o.cacheTTL != 10*time.Minute {
		t.Errorf("cacheTTL = %v, want the 10m default on unparsable env", o.cacheTTL)
	}
	if o.cacheLimitBytes != 536870912 {
		t.Errorf("cacheLimitBytes = %d, want the 512 MiB default on unparsable env", o.cacheLimitBytes)
	}
}

// TestCacheTunablesCanBeDisabled: zero is a meaningful value on both tunables —
// no expiry, no eviction — and must survive the resolve untouched.
func TestCacheTunablesCanBeDisabled(t *testing.T) {
	o := resolveCacheFlags(t, "--cache-ttl=0", "--cache-limit=0")
	if o.cacheTTL != 0 || o.cacheLimitBytes != 0 {
		t.Errorf("explicit zeros lost: ttl=%v limit=%d", o.cacheTTL, o.cacheLimitBytes)
	}
}

// TestNewServerConfiguresExportCache: the tunables are useless unless the server
// hands them to internal/source — without this call the cache keeps its
// off-by-default behaviour (no expiry, no eviction) whatever the flags say.
func TestNewServerConfiguresExportCache(t *testing.T) {
	before := source.CacheSnapshot()
	t.Cleanup(func() { source.ConfigureCache(before.TTL, before.LimitBytes) })

	newServer(options{cacheTTL: 7 * time.Minute, cacheLimitBytes: 3 << 20})

	got := source.CacheSnapshot()
	if got.TTL != 7*time.Minute {
		t.Errorf("cache TTL = %v, want 7m", got.TTL)
	}
	if got.LimitBytes != 3<<20 {
		t.Errorf("cache limit = %d, want %d", got.LimitBytes, int64(3<<20))
	}
}

// resolveGraphFlags binds the object-graph tunables to a private FlagSet the
// way the entrypoint does over flag.CommandLine.
func resolveGraphFlags(t *testing.T, args ...string) options {
	t.Helper()
	var o options
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	registerGraphFlags(fs, &o)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return o
}

// TestGraphTunableDefaults pins the five values named in the spec table of §5:
// chain depth 6, hub fan-in 50, depth penalty 0.9, hub penalty 0.7, radius
// ceiling 300 nodes.
func TestGraphTunableDefaults(t *testing.T) {
	o := resolveGraphFlags(t)
	if o.graphChainDepth != 6 || o.graphHubFanIn != 50 || o.graphRadiusNodes != 300 {
		t.Errorf("chainDepth/hubFanIn/radiusNodes = %d/%d/%d, want 6/50/300",
			o.graphChainDepth, o.graphHubFanIn, o.graphRadiusNodes)
	}
	if o.graphDepthPenalty != 0.9 || o.graphHubPenalty != 0.7 {
		t.Errorf("depthPenalty/hubPenalty = %v/%v, want 0.9/0.7",
			o.graphDepthPenalty, o.graphHubPenalty)
	}
}

// TestGraphTunablesFlagOverridesEnv: the flag is the more explicit of the two,
// and an unparsable env value falls back to the default instead of zeroing it.
func TestGraphTunablesFlagOverridesEnv(t *testing.T) {
	t.Setenv("MCP_1C_GRAPH_CHAIN_DEPTH", "3")
	t.Setenv("MCP_1C_GRAPH_HUB_PENALTY", "not a number")
	o := resolveGraphFlags(t, "--graph-chain-depth=9")
	if o.graphChainDepth != 9 {
		t.Errorf("chainDepth = %d, want 9 (flag over env)", o.graphChainDepth)
	}
	if o.graphHubPenalty != 0.7 {
		t.Errorf("hubPenalty = %v, want the 0.7 default on unparsable env", o.graphHubPenalty)
	}
}

// TestNewServerConfiguresGraphTunables: the four attribution thresholds are as
// useless as the cache tunables unless the server hands them down. Their route
// is options -> app.DefaultIndexConfig -> index.Config -> the resolver, and it
// is not the one the cache takes: the index-tool registry builds its config
// itself, below newServer. Without the wiring the -graph-* flags parse and the
// pipeline keeps running on its own defaults, which is exactly the "tunable
// that never arrived" this test exists to catch.
//
// -graph-radius-nodes is deliberately absent: its consumer is the graph
// service answer ceiling, not the indexing pipeline.
func TestNewServerConfiguresGraphTunables(t *testing.T) {
	before := app.DefaultIndexConfig()
	t.Cleanup(func() {
		app.ConfigureGraphTunables(before.GraphTunables.ChainDepth, before.GraphTunables.HubFanIn,
			before.GraphTunables.DepthPenalty, before.GraphTunables.HubPenalty)
	})

	newServer(options{graphChainDepth: 11, graphHubFanIn: 77, graphDepthPenalty: 0.42, graphHubPenalty: 0.24})

	got := app.DefaultIndexConfig().GraphTunables
	if got.ChainDepth != 11 {
		t.Errorf("ChainDepth = %d, want 11", got.ChainDepth)
	}
	if got.HubFanIn != 77 {
		t.Errorf("HubFanIn = %d, want 77", got.HubFanIn)
	}
	if got.DepthPenalty != 0.42 {
		t.Errorf("DepthPenalty = %v, want 0.42", got.DepthPenalty)
	}
	if got.HubPenalty != 0.24 {
		t.Errorf("HubPenalty = %v, want 0.24", got.HubPenalty)
	}
}

// resolveToolsProfile binds --tools to a private FlagSet and parses args.
func resolveToolsProfile(t *testing.T, args ...string) toolsProfile {
	t.Helper()
	var o options
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	registerToolsFlag(fs, &o)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return o.toolsProfile
}

// TestToolsProfileResolution pins the tunable rules for --tools: full by
// default, flag over env, and an unknown value from either side falls back to
// the default instead of failing the start or cutting tools.
func TestToolsProfileResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
		want toolsProfile
	}{
		{name: "default", want: profileFull},
		{name: "flag", args: []string{"--tools=core"}, want: profileCore},
		{name: "flag case", args: []string{"--tools=CORE"}, want: profileCore},
		{name: "env", env: "core", want: profileCore},
		{name: "flag over env", env: "core", args: []string{"--tools=full"}, want: profileFull},
		{name: "bad env", env: "minimal", want: profileFull},
		{name: "bad flag", env: "core", args: []string{"--tools=everything"}, want: profileFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_1C_TOOLS", tc.env)
			if got := resolveToolsProfile(t, tc.args...); got != tc.want {
				t.Errorf("profile = %q, want %q", got, tc.want)
			}
		})
	}
	if got := (options{}).toolsProfile.orDefault(); got != profileFull {
		t.Errorf("zero options profile = %q, want full", got)
	}
}
