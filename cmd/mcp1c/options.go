package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax"
)

// options holds the runtime configuration resolved from CLI flags and env vars.
//
// Offline mode: dumpDir (switchable at runtime via set_dump); projectsRoot is the
// directory scanned by list_projects. Live mode: a single base (baseURL/user/
// password) and/or basesFile — a JSON file listing many bases, switchable via
// set_base. The server starts even with none configured and answers server_info.
//
// cacheTTL and cacheLimitBytes are the export-cache tunables: process-wide by
// design (the export root and collection kind of an entry have no say in them),
// handed to internal/source in one call when the server is built.
type options struct {
	dumpDir         string        // offline: path to the XML configuration export
	projectsRoot    string        // offline: root directory scanned by list_projects
	baseURL         string        // live: single HTTP-service base URL (legacy / default)
	user            string        // live: 1C user for baseURL
	password        string        // live: 1C password for baseURL (never echoed back)
	basesFile       string        // live: path to a JSON file listing multiple bases
	cacheTTL        time.Duration // export cache: idle TTL; zero disables expiry
	cacheLimitBytes int64         // export cache: memory ceiling; zero disables eviction

	// queryTimeout is how long the connector lets a live query run before it
	// interrupts it in the base; zero sends no limit. Process-wide, handed to
	// internal/source in one call when the server is built.
	queryTimeout time.Duration

	// Object-graph tunables. Thresholds never drop an edge: going
	// over one multiplies the edge confidence, so an attribution chain stays
	// visible with a lower score instead of vanishing silently.
	graphChainDepth   int     // attribution chain length beyond which every link is penalised
	graphHubFanIn     int     // fan-in above which a procedure counts as a hub
	graphDepthPenalty float64 // confidence multiplier per link over graphChainDepth
	graphHubPenalty   float64 // confidence multiplier for a chain crossing a hub
	graphRadiusNodes  int     // node ceiling of one radius answer of the map

	// toolsProfile picks which tools the server advertises (full or core). The
	// zero value reads as the default, so options{} in tests keeps full.
	toolsProfile toolsProfile

	// syntaxIndex is the path to the platform syntax index built by
	// cmd/syntaxgen. Empty resolves to syntax.DefaultPath().
	syntaxIndex string

	// apiCards is the directory with search cards for find_api, built by
	// cmd/apicards. Empty means no cards: options{} in tests never reads the
	// user's own card files. The real process gets the default directory
	// through the flag default.
	apiCards string

	// coreSnapshot is the directory where the most used БСП methods are
	// mirrored for the Claude Code hook (corewarm.go). Empty means no file is
	// written: options{} in tests never touches the user's cache directory.
	// The real process gets the default directory through the flag default.
	coreSnapshot string
}

// registerCoreSnapshotFlag binds --core-snapshot into o; like --api-cards, the
// default location is baked into the flag value so that the zero options stay
// hermetic.
func registerCoreSnapshotFlag(fs *flag.FlagSet, o *options) {
	fs.StringVar(&o.coreSnapshot, "core-snapshot", defaultCoreSnapshotDir(),
		"directory where the most used БСП methods are mirrored for the BSL hook (tools/hooks/bsl_ready_methods.py reads the default one); none disables the file")
}

// coreSnapshotDir resolves the snapshot directory; none and empty turn it off.
func (o options) coreSnapshotDir() string {
	if p := strings.TrimSpace(o.coreSnapshot); p != "none" {
		return p
	}
	return ""
}

// registerAPICardsFlag binds --api-cards (env MCP_1C_API_CARDS) into o. Unlike
// the syntax index, the default location is baked into the flag value, so that
// the zero options stay hermetic.
//
// The environment variable is read by app.DefaultAPICardsDir rather than by
// envOr here: the default directory is shared with cmd/apicards and
// evals/findapi, which write and read the same card files without this package.
func registerAPICardsFlag(fs *flag.FlagSet, o *options) {
	fs.StringVar(&o.apiCards, "api-cards", app.DefaultAPICardsDir(),
		"directory with find_api search cards built by cmd/apicards (env "+app.APICardsEnv+"); none disables them")
}

// apiCardsDir resolves the cards directory; none and empty turn the cards off.
func (o options) apiCardsDir() string {
	if p := strings.TrimSpace(o.apiCards); p != "none" {
		return p
	}
	return ""
}

// registerSyntaxIndexFlag binds --syntax-index (env MCP_1C_SYNTAX_INDEX) into
// o, flag over env. The default is not baked into the flag value: an empty one
// resolves to syntax.DefaultPath() in syntaxIndexPath, so options{} in tests
// gets the same rule as the real process.
func registerSyntaxIndexFlag(fs *flag.FlagSet, o *options) {
	fs.StringVar(&o.syntaxIndex, "syntax-index", envOr(syntax.EnvPath, ""),
		"path to the platform syntax index built by cmd/syntaxgen (default: "+syntax.DefaultPath()+")")
}

// syntaxIndexPath resolves the index path: the flag or env value, else the
// default location syntaxgen writes to.
func (o options) syntaxIndexPath() string {
	if p := strings.TrimSpace(o.syntaxIndex); p != "" {
		return p
	}
	return syntax.DefaultPath()
}

// warnMissingSyntaxIndex notes on stderr that the index file is absent. It only
// stats the file: parsing stays lazy, and the server starts either way.
func warnMissingSyntaxIndex(path string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%s: индекс синтаксиса платформы не найден (%s): bsl_syntax и validate_bsl недоступны; сгенерируйте его: %s\n",
		serverName, path, syntax.GenerateHint)
}

// toolsProfile is the tool-surface profile: full registers every tool of the
// mode, core leaves out the rarely used ones (coreExcludedTools) to spend
// less of the client's context on tool definitions.
type toolsProfile string

const (
	profileFull toolsProfile = "full"
	profileCore toolsProfile = "core"
	// defaultToolsProfile keeps the behaviour the server had before profiles.
	defaultToolsProfile = profileFull
)

// parseToolsProfile reads a profile name; ok is false for anything unknown.
func parseToolsProfile(raw string) (toolsProfile, bool) {
	switch toolsProfile(strings.ToLower(strings.TrimSpace(raw))) {
	case profileFull:
		return profileFull, true
	case profileCore:
		return profileCore, true
	}
	return "", false
}

// orDefault resolves the zero value to the default profile.
func (p toolsProfile) orDefault() toolsProfile {
	if p == "" {
		return defaultToolsProfile
	}
	return p
}

func (p *toolsProfile) String() string {
	if p == nil {
		return string(defaultToolsProfile)
	}
	return string(p.orDefault())
}

// Set never fails: an unknown profile falls back to the default with a note on
// stderr, the same rule as an unparsable env value. A typo must not keep the
// server from starting, and must not silently cut tools either.
func (p *toolsProfile) Set(raw string) error {
	v, ok := parseToolsProfile(raw)
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: профиль инструментов %q неизвестен (допустимо full или core), используется %s\n",
			serverName, raw, defaultToolsProfile)
		v = defaultToolsProfile
	}
	*p = v
	return nil
}

// registerToolsFlag binds --tools (env MCP_1C_TOOLS) into o, flag over env.
func registerToolsFlag(fs *flag.FlagSet, o *options) {
	o.toolsProfile = envToolsProfileOr("MCP_1C_TOOLS", defaultToolsProfile)
	fs.Var(&o.toolsProfile, "tools",
		"tool profile: full (every tool of the mode) or core (without the rarely used ones)")
}

// envToolsProfileOr reads a profile from the environment, falling back to the
// default when the variable is unset or names no known profile.
func envToolsProfileOr(key string, fallback toolsProfile) toolsProfile {
	v, ok := parseToolsProfile(envOr(key, ""))
	if !ok {
		return fallback
	}
	return v
}

// Defaults for the export-cache tunables. They live here, next to the settings
// they belong to, so changing them is one edit in one file instead of a hunt for
// constants at the call sites.
const (
	// defaultCacheTTL is the idle time after which a cached collection is dropped.
	defaultCacheTTL = 10 * time.Minute
	// defaultCacheLimitBytes is the memory ceiling of the whole export cache.
	defaultCacheLimitBytes int64 = 512 << 20
)

// Defaults for the object-graph tunables. They are
// the starting point of the calibration, not its result: ADR-024 records what
// the run on ut_demo showed and any value it moved.
//
// The same four numbers exist a second time in internal/resolve
// (defaultChainDepth and friends): cmd/mcp1c must not import internal/resolve
// (arch rule cmd-through-app), so a single home for them does not exist. They
// are changed as a pair.
const (
	defaultGraphChainDepth   = 6
	defaultGraphHubFanIn     = 50
	defaultGraphDepthPenalty = 0.9
	defaultGraphHubPenalty   = 0.7
	defaultGraphRadiusNodes  = 300
)

// registerGraphFlags binds the five object-graph tunables with the same
// flag-then-env pattern as the cache ones. parseFlags calls it over
// flag.CommandLine.
func registerGraphFlags(fs *flag.FlagSet, o *options) {
	fs.IntVar(&o.graphChainDepth, "graph-chain-depth",
		envIntOr("MCP_1C_GRAPH_CHAIN_DEPTH", defaultGraphChainDepth),
		"attribution chain length beyond which every extra link lowers edge confidence")
	fs.IntVar(&o.graphHubFanIn, "graph-hub-fanin",
		envIntOr("MCP_1C_GRAPH_HUB_FANIN", defaultGraphHubFanIn),
		"fan-in above which a procedure counts as a hub and lowers edge confidence")
	fs.Float64Var(&o.graphDepthPenalty, "graph-depth-penalty",
		envFloatOr("MCP_1C_GRAPH_DEPTH_PENALTY", defaultGraphDepthPenalty),
		"confidence multiplier applied per chain link over -graph-chain-depth")
	fs.Float64Var(&o.graphHubPenalty, "graph-hub-penalty",
		envFloatOr("MCP_1C_GRAPH_HUB_PENALTY", defaultGraphHubPenalty),
		"confidence multiplier applied to a chain that crosses a hub")
	fs.IntVar(&o.graphRadiusNodes, "graph-radius-nodes",
		envIntOr("MCP_1C_GRAPH_RADIUS_NODES", defaultGraphRadiusNodes),
		"node ceiling of one radius answer of the object map")
}

// registerCacheFlags binds --cache-ttl and --cache-limit into o with the same
// flag-then-env pattern as --dump and --base. parseFlags calls it over
// flag.CommandLine; keeping the two names and their defaults here, next to the
// fields they fill, is what makes this file the one place the tunables live.
func registerCacheFlags(fs *flag.FlagSet, o *options) {
	fs.DurationVar(&o.cacheTTL, "cache-ttl", envDurationOr("MCP_1C_CACHE_TTL", defaultCacheTTL),
		"idle TTL of the export cache (e.g. 10m); 0 disables expiry")
	fs.Int64Var(&o.cacheLimitBytes, "cache-limit", envInt64Or("MCP_1C_CACHE_LIMIT", defaultCacheLimitBytes),
		"memory ceiling of the export cache, in bytes; 0 disables eviction")
}

// defaultQueryTimeout is the limit of one live query. A query written by a
// language model can join a register with every document table of the base; the
// HTTP timeout of the client only stops the waiting, not the query.
const defaultQueryTimeout = 30 * time.Second

// registerLiveFlags binds --query-timeout into o with the same flag-then-env
// pattern as the cache tunables. parseFlags calls it over flag.CommandLine.
func registerLiveFlags(fs *flag.FlagSet, o *options) {
	fs.DurationVar(&o.queryTimeout, "query-timeout", envDurationOr("MCP_1C_QUERY_TIMEOUT", defaultQueryTimeout),
		"how long a live query may run before the connector interrupts it (e.g. 30s); 0 sends no limit")
}

// envDurationOr reads a duration from the environment, falling back to the
// default when the variable is unset or unparsable. A typo in the environment
// must not take the server down at startup, and must not read as "disabled"
// either: zero is a value the user asks for explicitly, never one they trip into.
func envDurationOr(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(envOr(key, ""))
	if err != nil {
		return fallback
	}
	return v
}

// envInt64Or reads a byte count from the environment, with the same fallback
// rule as envDurationOr.
func envInt64Or(key string, fallback int64) int64 {
	v, err := strconv.ParseInt(envOr(key, ""), 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

// envIntOr reads a plain integer from the environment, with the same fallback
// rule as envDurationOr: a typo reads as "not set", never as zero.
func envIntOr(key string, fallback int) int {
	v, err := strconv.Atoi(envOr(key, ""))
	if err != nil {
		return fallback
	}
	return v
}

// envFloatOr reads a confidence multiplier from the environment, same rule.
func envFloatOr(key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(envOr(key, ""), 64)
	if err != nil {
		return fallback
	}
	return v
}
