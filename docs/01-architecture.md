# Lighthouse — Architecture and technology

## Architecture

```text
Codex / Claude Code local agent
        │ one bounded CLI code call
        ▼
Go CLI + constrained code runtime ────── domain services ── embedded local DB file
Browser ── optional loopback HTTP dashboard                    ├── artifacts + memory
                                                               ├── full-text index
                                                               ├── vector index
                                                               └── revisions + provenance

Optional local embedding provider ── embedding service ── database
```

The executable has two primary entry points:

- `lighthouse code ...` for primary agent operations; it works with the dashboard stopped.
- Other CLI commands support direct operator actions.
- `lighthouse serve` for the dashboard. It binds to loopback, serves HTML and static files, and closes cleanly on shutdown.

The dashboard binds to `127.0.0.1:8080` by default. The friendly browser address is `http://lighthouse.localhost:8080`; `http://lighthouse:8080` requires a local hostname mapping.

Both entry points use the same validated store operations. The code-call CLI remains the primary agent client; the HTTP dashboard supports same-origin, CSRF-protected edits and import/export while binding only to loopback.

## Language and server

**Go is the v1 language choice.** The application is local I/O-bound, so a measured speed win from Rust is unlikely to justify a less familiar implementation. Go provides a small, portable executable and a simple HTTP server in its standard library. The codebase should stay idiomatic and avoid a large framework stack.

- HTTP: `net/http` and `http.ServeMux` unless implementation needs prove a router is useful.
- Views: standard `html/template` templates. No client-side SPA.
- Interactivity: ordinary HTML forms, POST mutations, and server-rendered inline SVG. No JavaScript framework is shipped.
- Assets: compile/embed CSS and templates into the executable. No Node runtime in production.
- Code calls: a constrained Starlark runtime is the primary agent data interface; see [code call and memory lifecycle](03-code-call-and-memory.md).

### Runtime choice for local agent calls

The executable is compiled once for the user's machine; a code call is a Starlark script interpreted in-process by that Go executable. The script is not Python, and there is no child interpreter startup or script compilation per call. This keeps setup to one installed CLI, while the runtime exposes validated Lighthouse operations rather than shell, SQL, files, or network access. Python is convenient for one-off scripts but adds a runtime/process; Lua requires another runtime and API; native Go, Rust, C, and C++ code requires building a separate helper; Java adds a JRE/JVM or native-image packaging. Since this workload is short local database I/O, those options do not justify their setup cost. Keep Go + embedded Starlark unless measured workload evidence changes the decision.

Local database operations do not make hosted model calls or directly consume model credits. The agent's code generation and interpretation still use its provider's normal model tokens. The actual spend depends on that provider and conversation, not the Lighthouse CLI.

## Storage selection

Use one local file, under the user's application-support directory, with a `--db` override for development and portability. Do not put the live database inside a synced Obsidian vault or active Git checkout by default: SQLite-style WAL sidecars and concurrent access can behave poorly when another process syncs or copies a database mid-write.

### Engine: SQLite with Go driver

The initial implementation uses `modernc.org/sqlite`, a CGo-free SQLite driver, plus its bundled `modernc.org/sqlite/vec` extension for vector functions. This keeps the canonical database a normal local SQLite file and avoids requiring a separate vector server. The selected driver and extension still need build and runtime verification on the target Macs. Sources: [SQLite driver](https://pkg.go.dev/modernc.org/sqlite), [bundled vector extension](https://pkg.go.dev/modernc.org/sqlite/vec).

The database remains portable SQLite. Verify driver builds, FTS5 availability, vector functions, concurrent dashboard reads and agent writes, and backup behavior on macOS arm64 and amd64 before release.

### Fallback

The vector extension is an optional retrieval capability. Normal artifact and memory records remain readable with ordinary SQLite tooling.

### Do not conflate products

- **SQLite** is the mature, embedded baseline.
- **libSQL** is the older SQLite fork with a SQLite-compatible API/file story and additional features.
- **Turso Database** remains a possible future alternative if its Go integration better fits measured requirements.

The schema and service boundary should avoid unnecessary engine-specific SQL so the candidate can be replaced if the validation spike fails.

## Search and embedding services

- Keyword search is always available. Use the chosen engine's supported full-text feature or a simple indexed fallback.
- Semantic search is optional if an embedding provider is configured. The database stores vectors and performs nearest-neighbor retrieval; the provider creates embeddings.
- Search combines semantic and lexical candidates, then applies project, source, date, and type filters before returning a small result set.
- Re-embedding a document is an asynchronous maintenance task. Saving the source artifact must not wait for an embedding service.
- Default behavior must not send saved text to a remote embedding endpoint. A local provider is preferred; remote providers require explicit configuration and must be visibly identified.

## Dependency direction

```text
CLI / HTTP handlers
        ↓
Application services (capture, search, context, code calls)
        ↓
Domain types and policy
        ↓
Repository interfaces
        ↓
Embedded database adapter + search indexes
```

HTTP handlers and the code runtime do not issue SQL. They invoke the same validated application services. The data layer does not know about browser templates, agent host names, or conversation UI.

## Lifecycle

- No background service starts on login by default.
- `serve` runs only when manually launched or explicitly started by the user.
- CLI reads and writes can operate while the dashboard is closed.
- When both are open, database settings and a short busy timeout must accommodate concurrent reads and brief writes.
- On shutdown, stop accepting HTTP requests, finish or cancel bounded code calls, checkpoint/close the database as supported by the selected engine, and exit.
