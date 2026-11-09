# Lighthouse

Lighthouse is a local-first store for coding-agent results and durable memory. Agents use one constrained Starlark program through the `lighthouse code` CLI. A Go server provides an optional, loopback-only dashboard for reviewing records, metrics, edits, audit events, and data transfer. The canonical store is a portable SQLite file; there is no MCP server or cloud service in the data path.

## How an agent uses it

Install the `lighthouse` executable and the agent skill. Start substantive work with one context-load call, then reuse that context during the conversation. Batch any needed searches or reads into one script and emit only relevant results. After a write, consume the returned committed record, revision, and context patch so the active conversation can refresh its memory. Lighthouse cannot silently update hidden provider context.

```sh
lighthouse code --project my-project < examples/bootstrap.star
```

The code API includes `load_memory`, `search`, `get`, `describe`, `audit`, `save`, `update`, `delete`, `upsert_memory`, `delete_memory`, and `emit`. Documents accept flexible JSON custom fields without agent-controlled SQL schema changes. Deletions are explicit and audited. See [agent integration](docs/agent-integration.md), [code call and memory lifecycle](docs/03-code-call-and-memory.md), and [examples](examples/).

### Runtime, setup, and cost

The CLI and dashboard are Go programs. The code-call script language is **Starlark**, an embedded Python-like language interpreted in the already-running Go process. It is not a Python subprocess, and Lighthouse does not compile scripts on each call. The language has no shell, filesystem, environment, raw SQL, or network builtins. This avoids a runtime install and a per-call compilation step while keeping a typed, constrained API.

A local CLI invocation and SQLite read/write do not themselves consume model credits or call a hosted service. The agent still uses model tokens to plan the script and interpret the output; that usage is determined by the agent provider.

| Option | Per-call setup | Fit for this workload |
| --- | --- | --- |
| Embedded Go + Starlark (current) | Run the existing executable; interpret the short script | No separate interpreter or script compilation; typed capabilities |
| Python | Start an interpreter and load script | Easy for standalone scripts, but adds a runtime/process for every CLI call |
| Lua/LuaJIT | Start interpreter; possibly native dependency | Lightweight runtime, but another API/runtime to install and support |
| Go, Rust, C, C++ | Compile source, or run a prebuilt executable | Fast native programs; per-request compilation is unnecessary overhead here |
| Java | Start JVM and provide a JRE, or package a native image | Larger setup for a local helper; not installed in the checked environment |

These are architectural tradeoffs, not a portable performance benchmark: cold start and compilation vary by machine, toolchain, and packaging. The operation itself is a short local database access. The existing Go binary plus embedded Starlark is the simplest measured-to-implement choice; use a prebuilt Go executable for distribution rather than compiling a new helper on every agent turn.

## Build and run

```sh
go build -o "$HOME/.local/bin/lighthouse" ./cmd/lighthouse
lighthouse init
lighthouse serve --port 8080
```

The optional dashboard binds to `127.0.0.1:8080`; open `http://127.0.0.1:8080` or `http://lighthouse.localhost:8080`. It uses Go templates, CSS, and native HTML forms—no React, HTMX, Node runtime, or browser JavaScript bundle. The overview shows record counts, seven-day activity, security status, and the current SQLite footprint: database file plus active `-wal` file. Transient shared-memory and unrelated filesystem allocation are excluded.

Dashboard mutations require a same-origin form and CSRF token, and changes are audited. Agent CLI operations work while the dashboard is stopped. Lighthouse does not install a startup daemon automatically; run `lighthouse serve` manually or configure your preferred local service manager.

The database defaults to the OS user configuration directory. Set `LIGHTHOUSE_DB` or pass `--db /path/to/file.db` to choose another file. Avoid placing a live database in a synced vault or active Git checkout.

## Import, export, and storage

The dashboard and CLI support JSON, Markdown, YAML, TOML, PDF, and SQLite `.db` transfer. Portable data imports merge records and skip duplicate IDs or memory keys. A `.db` export is a consistent SQLite snapshot; importing a `.db` merges artifacts and active memory rather than replacing the live database. PDF export is a readable report; PDF imports preserve bounded raw source bytes without extracting text.

Lighthouse uses SQLite through the CGo-free `modernc.org/sqlite` driver, with FTS5 and an optional bundled vector extension. Keyword search is available locally. Semantic search requires embeddings to be supplied; Lighthouse does not silently send data to an embedding provider.

## Security boundaries and known limits

- The dashboard binds to loopback and validates allowed hostnames. Mutations use CSRF protection and audit events.
- Starlark has bounded input, output, execution steps, and wall time, and exposes typed operations only. It does not have a hard memory limit; use trusted local scripts.
- Local storage has no built-in encryption or multi-user authentication. Protect the user account and database file accordingly.
- Vector query behavior, automated startup packaging, and broader platform distribution remain areas for further validation.

## Project documentation

- [Product brief](docs/00-product-brief.md)
- [Architecture and runtime choice](docs/01-architecture.md)
- [Data model and search](docs/02-data-model-and-search.md)
- [Code call and memory lifecycle](docs/03-code-call-and-memory.md)
- [Dashboard visual specification](docs/04-dashboard-visual-spec.md)
- [Security and operations](docs/05-security-and-operations.md)
- [Decisions and roadmap](docs/06-decisions-and-roadmap.md)
- [Agent integration setup](docs/agent-integration.md)
- [Architecture diagram](docs/lighthouse-architecture-overview.drawio)
