# 1C-WORKFLOW

[Русская версия](README.md)

An MCP server for development on the 1C:Enterprise platform (the built-in language, metadata,
queries). It gives an AI agent (Claude Code, Codex or any other MCP client) exact answers about
a specific configuration instead of reading thousands of XML files and guessing from memory.

One `mcp1c` process serves three data sources:

- **offline**: an XML export of the configuration (`DumpConfigToFiles`), read on demand;
- **index**: a persistent SQLite index of symbols, references, the call graph, metadata,
  queries and register access, updated incrementally;
- **live**: a running infobase through a connector extension with an HTTP service, strictly
  read-only.

A single statically linked binary without cgo, stdio transport, MCP protocol 2025-06-18.

Tool descriptions are in English, while notes in answers and server messages are in Russian,
and the server targets configurations with Russian identifiers, as most 1C code is written.

## Why

Tasks where an agent without the server reads a lot and often gets it wrong:

- **Change a procedure signature.** `find_references`, `trace_call_graph` and `find_impact`
  show every caller and everything that depends on the procedure before the edit, and
  `get_context_for_task` assembles the minimal context for a task described in words.
- **Add code to a document's write or posting.** `write_path` lists everything that runs on
  write (object module, event subscriptions, register records, exchange registration), and
  `get_movements review=true` reviews the posting code.
- **Write a query with the right fields.** `get_query_schema` returns exact field names and
  virtual table parameters, `query_advisor` checks the finished text for anti-patterns and
  unindexed filters, and in live mode `execute_query` runs a SELECT against real data.
- **Find out why a user cannot see an object, or why it did not reach an exchange.**
  `visibility_audit`, `rights_audit`, `access_diagnose` and `exchange_audit` walk the chain of
  subsystems, functional options, roles, access profiles and exchange plans.

The server never changes the configuration or the infobase: every tool is read-only, except
`reindex`, which writes the server's own index.

## Requirements

- **OS**: Windows, macOS or Linux, amd64 or arm64.
- **Go 1.26+**: only to build from source or to use `go install`.
- **An XML export of the configuration** in the `DumpConfigToFiles` format (Designer or
  `ibcmd`), optionally exports of extensions, external data processors and reports.
- **An installed 1C:Enterprise 8.3 platform**: its syntax assistant files (`.hbk`) are used
  once to build the syntax index for `bsl_syntax` and `validate_bsl`.
- **For live mode**: an infobase where you can load an extension, and publication of its HTTP
  services (a web server or the standalone server `ibsrv`).

## Installation

**Prebuilt binary.** Download the archive for your OS from
[Releases](https://github.com/blessed2k/1C-WORKFLOW/releases), unpack it and put `mcp1c`
(`mcp1c.exe`) and `syntaxgen` into a directory on your `PATH`. Checksums are in
`checksums.txt`.

**With `go install`:**

```sh
go install github.com/blessed2k/1C-WORKFLOW/cmd/mcp1c@latest
go install github.com/blessed2k/1C-WORKFLOW/cmd/syntaxgen@latest
```

**From source:**

```sh
git clone https://github.com/blessed2k/1C-WORKFLOW.git
cd 1C-WORKFLOW
CGO_ENABLED=0 go build -o mcp1c ./cmd/mcp1c          # Windows: -o mcp1c.exe
CGO_ENABLED=0 go build -o syntaxgen ./cmd/syntaxgen
```

Check: `mcp1c --version`.

## Platform syntax index

The platform reference belongs to its copyright holder, so neither the repository nor the
release archives contain it: you build the index from your own platform installation.

```sh
syntaxgen <platform dir>/shcntx_ru.hbk \
    <platform dir>/shlang_ru.hbk <platform dir>/shquery_ru.hbk
```

From source it is the same: `go run ./cmd/syntaxgen <.hbk files>`.

| OS | Platform directory |
|---|---|
| Windows | `C:\Program Files\1cv8\<version>\bin` |
| macOS | `/opt/1cv8/<version>` |
| Linux | `/opt/1cv8/x86_64/<version>` |

`shcntx_ru.hbk` is required (global context and types), `shlang_ru.hbk` adds language
statements, `shquery_ru.hbk` adds the query language. Rebuild the index after a platform
upgrade.

The file is written to `<user config dir>/mcp1c/syntax-index.json.gz`, which is where the
server looks by default: `%AppData%` on Windows, `~/Library/Application Support` on macOS,
`$XDG_CONFIG_HOME` or `~/.config` on Linux. For another location use `syntaxgen -out <file>`
and `--syntax-index <file>` (or `MCP_1C_SYNTAX_INDEX`) on the server.

Without the index the server still runs: it prints a warning to stderr, `bsl_syntax` and
`validate_bsl` return an error with the build command, and the index layer cannot tell
platform function calls from calls to your own procedures.

## Quick start with Claude Code

**1. Lay out the project.** The `1c-project.json` manifest describes what the project is made
of:

```text
my-project/
  1c-project.json
  src/               <- XML export of the configuration (Configuration.xml and object folders)
```

```json
{
  "version": 1,
  "project": "my-project",
  "displayName": "My configuration",
  "components": [
    { "id": "cfg", "kind": "configuration", "root": "src" }
  ]
}
```

Component kinds: `configuration`, `extension` (with `appliesTo` and `applyOrder`),
`external-data-processor`, `external-report`, `test-sources`, `standalone-bsl`. A component
root is a relative path and may leave the manifest directory through `..`; absolute paths are
rejected. An example with extensions is in [docs/install.md](docs/install.md) (in Russian).

**2. Add the server.** `--projects-root` is the directory where the server keeps its project
registry and index files (`<projects-root>/.mcp1c/`); without it there are no index tools.

```sh
claude mcp add 1c-workflow -- mcp1c \
  --dump /path/to/my-project/src \
  --projects-root /path/to/workspace
```

`--scope user` makes the server available in every project. Do not commit `.mcp1c/`.

**3. Build the index.** A project is registered the first time with a `reindex` call that
passes `projectRoot` (the directory with `1c-project.json`). The easiest way is to ask the
agent:

```text
Call reindex with projectRoot=/path/to/my-project
```

The first full index of a large standard configuration takes a few minutes, a small one takes
seconds. After that the index is kept up to date incrementally: when an index tool is called,
the server reads the files that changed. On later starts with the same `--dump` the project is
activated automatically.

**4. Work.** An example request:

```text
I want to add a shipment date check to BeforeWrite of the ЗаказКлиента document.
What already runs on write, and where is the safest place for the code?
```

On connect the agent receives a map of the tools by task phase (explore, analyse before a
change, write, verify) and picks the right ones on its own: here `get_context_for_task`,
`write_path` and, once the code is written, `validate_bsl`.

### Other MCP clients

The server is an ordinary stdio process, so any client with the common JSON configuration
works:

```json
{
  "mcpServers": {
    "1c-workflow": {
      "command": "mcp1c",
      "args": ["--dump", "/path/to/my-project/src", "--projects-root", "/path/to/workspace"]
    }
  }
}
```

A Codex CLI example, running through `go run` and a manual JSON-RPC check are in
[docs/install.md](docs/install.md).

## Modes and flags

The mode is chosen at start: with `--base` or `--bases` the server works against a live
infobase, otherwise against an export. The export can be switched at runtime with `set_dump`,
the infobase with `set_base`. The index layer (`--projects-root`) works in both modes and is
built only from files on disk.

Every flag can be set through an environment variable; the flag wins. An environment value
that does not parse falls back to the default.

| Flag | Environment variable | Default | Purpose |
|---|---|---|---|
| `--dump` | `MCP_1C_DUMP` | | XML export directory (offline) |
| `--projects-root` | `MCP_1C_PROJECTS_ROOT` | | directory of the project registry and indexes; without it the index tools are not registered; `list_projects` scans it too |
| `--base` | `MCP_1C_BASE_URL` | | connector HTTP service URL (live) |
| `--user` | `MCP_1C_USER` | | infobase user (live) |
| `--password` | `MCP_1C_PASSWORD` | | password (live); prefer the environment variable |
| `--bases` | `MCP_1C_BASES` | | JSON file with the infobases for `set_base` |
| `--tools` | `MCP_1C_TOOLS` | `full` | profile: `full` or `core` (without rarely used tools, saves context); an unknown value means `full` |
| `--syntax-index` | `MCP_1C_SYNTAX_INDEX` | see above | platform syntax index file |
| `--cache-ttl` | `MCP_1C_CACHE_TTL` | `10m` | how long a parsed export collection may sit idle in memory; `0` disables expiry |
| `--cache-limit` | `MCP_1C_CACHE_LIMIT` | `536870912` | memory ceiling of the export cache in bytes; `0` disables eviction |
| `--graph-chain-depth` | `MCP_1C_GRAPH_CHAIN_DEPTH` | `6` | chain length after which every link lowers the confidence of an object graph edge |
| `--graph-hub-fanin` | `MCP_1C_GRAPH_HUB_FANIN` | `50` | number of callers from which a procedure counts as a hub |
| `--graph-depth-penalty` | `MCP_1C_GRAPH_DEPTH_PENALTY` | `0.9` | confidence multiplier per extra link |
| `--graph-hub-penalty` | `MCP_1C_GRAPH_HUB_PENALTY` | `0.7` | confidence multiplier for a chain through a hub |
| `--graph-radius-nodes` | `MCP_1C_GRAPH_RADIUS_NODES` | `300` | node ceiling of one object map answer |
| `--version` | | | print the version and exit |

The `--bases` file holds an array `[{"name": "...", "url": "...", "user": "...", "password": "..."}]`.
Passwords are stored there in plain text: keep the file outside any repository and readable
only by your user. With several infobases the server starts with none selected, and you pick
one explicitly with `set_base`, so an answer never comes from the wrong infobase.

`server_info` shows the active source, project and profile.

### Object graph map

```sh
mcp1c graph --project /path/to/workspace [--listen 127.0.0.1:0]
```

Serves a local web page with a map of object relations from an already built index
(`--project` points at the `--projects-root` directory and may be repeated). It listens on
`127.0.0.1` only and prints the address; open it in a browser yourself.

## Live infobase connector

The sources of the `MCPКоннектор` extension are in [connector/src](connector/src), the
detailed guide (in Russian) is [connector/README.md](connector/README.md). In short:

1. Load `connector/src` into a test infobase as an extension (Designer,
   `/LoadConfigFromFiles <dir> -Extension "MCPКоннектор"`) and export a `.cfe` if you need
   one. The XML format version depends on the platform version: if the load rejects the
   format, regenerate the description with
   `connector/tools/build_src.py --from-dump <export of your infobase>`.
2. Publish the infobase HTTP services: a web server on Windows or Linux, or the standalone
   server `ibsrv` (macOS included). The service root URL is `mcp-1c`.
3. Start the server:

   ```sh
   MCP_1C_PASSWORD=... mcp1c --base http://<host>/<infobase>/hs/mcp-1c --user <user>
   ```

**Security model.**

- The connector only reads: metadata, the event log, predefined items and query results.
  There are no write endpoints, and `Выполнить()`/`Вычислить()` are never applied to input.
- `/query` runs only text that starts with the keyword `ВЫБРАТЬ`/`SELECT`, anything else gets
  HTTP 403. The server repeats the same check on its side. `/validate-query` only parses the
  query without running it.
- Privileged mode is never enabled: queries run with the rights of the authenticated user
  (HTTP Basic), record-level restrictions included. Create a dedicated user for the connector
  with the minimal read rights on the objects you need (and on the event log if you use it).
- Pass the password through `MCP_1C_PASSWORD`, not as a command-line argument: arguments end
  up in shell history and process lists. `list_bases` never shows passwords.
- Install the connector on test copies, not on a production infobase, and publish the service
  on a trusted network only.

## Tools

The set depends on the mode, the profile and whether `--projects-root` is given. The "core"
column says whether the tool is part of `--tools=core`.

**Source and navigation**

| Tool | Mode | core | Purpose |
|---|---|---|---|
| `server_info` | both | yes | version, active source, project and profile |
| `set_dump`, `list_projects` | offline | yes, no | switch the export; find exports under a root |
| `set_base`, `list_bases` | live | yes | switch the infobase; list configured infobases |
| `get_configuration_info` | both | no | name, version, vendor, object counts per kind |
| `object_exists` | both | yes | whether an object exists, with near matches |
| `get_metadata_tree` | both | yes | objects by kind, filtered by `type` and `like` |
| `get_object_structure` | both | yes | attributes, tabular sections, forms and commands of an object |
| `get_form_structure` | offline | yes | items, attributes, commands and handlers of a form |
| `search_code` | offline | yes | search module text, with the enclosing procedure of each hit |

**Analysis before a change (offline)**

| Tool | core | Purpose |
|---|---|---|
| `context_pack` | yes | structure, exported module interface and usages of an object in one call |
| `find_metadata_usages` | yes | where an object is used as a type, in role rights, report schemas and exchange rules |
| `find_dependency_paths` | yes | chains of relations between two objects |
| `write_path` | yes | what runs on write and posting, in platform order |
| `get_movements` | yes | register records of a document; `review=true` adds a posting code review |
| `exchange_audit` | yes | why an object does or does not reach an exchange |
| `visibility_audit` | yes | why an object is not visible: subsystems, functional options, rights |
| `rights_audit` | yes | roles, rights and RLS on an object, effective rights of a role set and supplied profiles |
| `new_object_checklist` | yes | where else a new object has to be registered |
| `form_impact` | yes | who else changes a form, predicted conflicts, and a check of draft code |
| `command_visibility` | no | per-role command visibility from `CommandInterface.xml` |
| `bsp_extension_points` | no | overridable modules of the standard subsystems library that fit a task |
| `extension_context` | no | borrowed objects and interceptors of an extension, with the original method text |
| `dump_diff` | no | difference in composition and format version between two exports |

**Writing and checking code**

| Tool | Mode | core | Purpose |
|---|---|---|---|
| `get_query_schema` | offline | yes | fields and virtual tables of an object for the query language |
| `bsl_syntax` | both | yes | syntax of a platform method, property or type; `owner=<type>` lists all members of a type compactly, `query=Type.Member` searches within a type, a lookup by type name shows its constructors (needs the syntax index) |
| `validate_bsl` | offline | yes | references to missing metadata and wrong argument counts |
| `query_advisor` | offline | yes | query anti-patterns with rewrites, and index hints |

**Live infobase**

| Tool | Purpose |
|---|---|
| `execute_query` | run a SELECT and return the rows |
| `validate_query`, `analyze_query` | check a query without running it; find heavy constructs |
| `get_event_log` | event log filtered by dates, level and user |
| `get_predefined`, `get_subsystem` | predefined items; subsystem composition |
| `data_health` | whether a register or catalog is actually maintained: row count, first and last record, growth |
| `access_profiles`, `access_diagnose` | access group profiles as data; where a user's access chain breaks |
| `check_sync` | compare the infobase with an XML export before a load or dump |

Every live tool is part of `core`.

**Index tools (both modes, only with `--projects-root`, all in `core`)**

| Tool | Purpose |
|---|---|
| `get_context_for_task` | minimal sufficient context for a task described in words, within a character budget |
| `index_status`, `reindex` | index state; project registration and rebuild |
| `find_symbol`, `get_symbol`, `get_module_structure` | find procedures and functions, exact signature and body, module outline |
| `find_references`, `trace_call_graph`, `find_impact` | references to a symbol, call graph, what depends on a symbol or object |
| `get_object`, `get_form_handlers` | an object from the index with subscriptions, jobs and rights; form handler bindings |
| `find_queries_using`, `find_register_writes` | queries that read an object or field; register access |
| `object_graph` | data neighbourhood of an object: who writes to which registers |

Index tools understand extension layers (`view=effective`). The limits of each tool are
described in [docs/tools-index.md](docs/tools-index.md) (in Russian).

In offline mode the server also provides prompts for common tasks (`add-attribute`,
`new-object`, `why-invisible`, `not-in-exchange`, `change-posting`) and the resources
`onec://metadata`, `onec://object/{type}/{name}`, `onec://standards/naming`,
`onec://query-lang/cheatsheet`, with completion of metadata names.

## Limitations

- **Indexing time and memory.** A cold full index of a large standard configuration (about
  49 thousand files) takes about 4 minutes, with a peak process memory of about 6 GiB and an
  index file of about 2.2 GiB. Measurements are in [docs/benchmarks.md](docs/benchmarks.md).
- **Incremental `reindex` on configurations of about 50 thousand files** currently fails on
  the SQLite bound parameter limit. Workaround: `reindex mode=full`.
- **`search_code` reads the export directly**; a full-text index is not used yet.
- **Index completeness.** Dynamically assembled query texts are not indexed; `find_impact`
  does not walk every kind of relation; the merged view with extensions (`view=effective`)
  does not merge extension forms. Details in [docs/tools-index.md](docs/tools-index.md).
- **Live mode** has no form structure and no code search (neither exists at runtime) and is
  less tested than offline mode.
- **macOS.** The macOS client platform cannot publish an infobase through a web server: live
  mode needs the standalone server `ibsrv` or a publication on Windows or Linux.
- **Windows.** `server_info` does not report process memory.
- **The syntax index** has to be built by you, which needs an installed platform.
- `mcp1c graph` does not open a browser, it only prints the address.

Open directions are listed in [ROADMAP.md](ROADMAP.md).

## Development and tests

```sh
go build ./...
go vet ./...
go test ./...                     # no real export and no syntax index needed
go test -race ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
```

Tests on real data are enabled by environment variables and skipped without them:

| Variable | Purpose |
|---|---|
| `ONEC_DUMP` (or `MCP1C_SPIKE_DUMP`) | root of a real XML export for the index, resolver and `get_context_for_task` tests |
| `MCP_1C_SYNTAX_INDEX` | a real syntax index for the same tests |
| `ONEC_POSTING_INTERCEPTS`, `ONEC_POSTING_NOBASE` | document and interceptor pairs for the posting tests under extensions |
| `ONEC_REAL_DUMP`, `ONEC_REAL_FORM`, `ONEC_REAL_OBJECT`, `ONEC_FIND`, `ONEC_HBK` | offline layer tests on real data |
| `MCP_1C_BASE_URL`, `MCP_1C_USER`, `MCP_1C_PASSWORD` | connector integration tests against a live infobase |

Run real-data tests one package at a time (`-run`): every package builds a full index of the
export, and running them in parallel hits the default `go test` timeout.

Code layout: [CLAUDE.md](CLAUDE.md) (package map and rules for AI agents, in Russian),
[docs/architecture-index.md](docs/architecture-index.md), decisions in [docs/adr](docs/adr).
How to propose a change: [CONTRIBUTING.md](CONTRIBUTING.md). Vulnerabilities:
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE). Licenses of the dependencies linked into the binary are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
