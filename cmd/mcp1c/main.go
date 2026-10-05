// Command mcp1c is the entrypoint for the 1C-WORKFLOW MCP server.
//
// A self-owned MCP server for full-cycle 1C (BSL) development: configuration
// introspection (offline XML export or a live HTTP-service connector), code
// search, query analysis and BSL linting. Phase 0 wires the MCP transport and
// exposes a single server_info tool; data tools are added in later phases.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName is the MCP implementation name reported to clients.
const serverName = "1C-WORKFLOW"

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	// Единственное ветвление по подкоманде во всём mcp1c: `graph`
	// уводит в graph-режим (cmd/mcp1c/graph.go), всё остальное — включая
	// отсутствие аргументов и любой флаг, начинающийся с "-" — ведёт себя
	// РОВНО как раньше, через parseFlags/newServer ниже. Обратная
	// совместимость закрыта TestMainDispatchDefaultsToStdio.
	if len(os.Args) > 1 && os.Args[1] == "graph" {
		os.Exit(runGraph(os.Args[2:]))
	}

	opts, showVersion := parseFlags()
	if showVersion {
		fmt.Println(serverName, version)
		return
	}

	server, closer := newServerWithCloser(opts)
	// Индекс закрывается на выходе, а не бросается на произвол процесса:
	// SQLite в WAL-режиме иначе оставляет за собой -wal, который следующему
	// старту приходится доводить восстановлением.
	defer func() {
		if err := closer.Close(); err != nil {
			log.Printf("%s: закрытие индекса: %v", serverName, err)
		}
	}()

	ss, err := server.Connect(context.Background(), &mcp.StdioTransport{}, nil)
	if err != nil {
		log.Fatalf("%s: failed to start: %v", serverName, err)
	}

	// Wait blocks until the client closes the connection. Any error here means
	// the session ended (client disconnect, broken pipe, EOF) — there is nothing
	// to recover, so we exit cleanly and only surface the reason on stderr.
	if err := ss.Wait(); err != nil {
		log.Printf("%s: session ended: %v", serverName, err)
	}
}

func parseFlags() (options, bool) {
	var opts options
	var showVersion bool

	flag.StringVar(&opts.dumpDir, "dump", envOr("MCP_1C_DUMP", ""),
		"path to the XML configuration export (offline mode)")
	flag.StringVar(&opts.baseURL, "base", envOr("MCP_1C_BASE_URL", ""),
		"HTTP-service base URL of the connector (live mode)")
	flag.StringVar(&opts.user, "user", envOr("MCP_1C_USER", ""),
		"1C user for live mode")
	flag.StringVar(&opts.password, "password", envOr("MCP_1C_PASSWORD", ""),
		"1C password for live mode")
	flag.StringVar(&opts.basesFile, "bases", envOr("MCP_1C_BASES", ""),
		"path to a JSON file listing live bases (multi-base mode, switchable via set_base)")
	flag.StringVar(&opts.projectsRoot, "projects-root", envOr("MCP_1C_PROJECTS_ROOT", ""),
		"workspace root: the project registry of the indexed tools (without it they are not registered) and the root list_projects scans")
	registerCacheFlags(flag.CommandLine, &opts)
	registerGraphFlags(flag.CommandLine, &opts)
	registerToolsFlag(flag.CommandLine, &opts)
	registerSyntaxIndexFlag(flag.CommandLine, &opts)
	registerAPICardsFlag(flag.CommandLine, &opts)
	registerCoreSnapshotFlag(flag.CommandLine, &opts)
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	return opts, showVersion
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
