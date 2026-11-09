# Lighthouse — Decisions and roadmap

## Decisions recorded

### D-001 — Local-first single-file store

**Decision:** Keep canonical artifacts, memory, metadata, and indexes in a local embedded database file. The CLI/code call is the primary data client; the dashboard is an optional visual workspace.

**Reason:** The user wants a fast, lightweight local store that works without an always-running server.

**Status:** Implemented in the initial project.

### D-002 — No MCP

**Decision:** Agent access is through local executable calls and a constrained code mode. Do not implement an MCP server for v1.

**Reason:** The user explicitly wants a local code-call flow and considers MCP unnecessary/expensive for this use case.

**Status:** Accepted.

### D-003 — Go CLI primary; server-rendered dashboard

**Decision:** Go; constrained code-call CLI for agent reads and writes; standard library HTTP and HTML templates for an optional local dashboard with protected CRUD and data transfer forms.

**Reason:** Keep the runtime small, the app easy to distribute, and avoid shipping a SPA framework.

**Status:** Implemented in the initial project.

### D-004 — Code runner is capability-limited

**Decision:** Generated scripts may use a typed Lighthouse data API but cannot access raw SQL, shell, filesystem, or network.

**Reason:** Batch useful operations in one call while preserving local data boundaries and predictable writes.

**Status:** Implemented using Starlark; hard memory isolation remains out of scope for the in-process runner.

### D-005 — Session context is versioned and explicit

**Decision:** Store snapshots by session and scope; return the current revision at bootstrap and a context patch on successful memory writes.

**Reason:** New chats need current memory; active chats need a visible update when stored memory changes.

**Limit:** The agent host must consume the patch; Lighthouse cannot mutate hidden provider context.

**Status:** Implemented for code-call `save` and `upsert_memory`; the agent consumes the returned patch.

### D-006 — Dashboard adopts House of ANIO styling

**Decision:** Retain the requested typography, spacing, responsive foundations, and finish values; use monochrome black-and-white dashboard surfaces.

**Reason:** The user asked for a close visual match to the attached House of ANIO project.

**Status:** Implemented. See [dashboard visual specification](04-dashboard-visual-spec.md).

### D-007 — SQLite with a CGo-free Go driver and optional vector extension

**Decision:** Start with ordinary SQLite through `modernc.org/sqlite` and its bundled `modernc.org/sqlite/vec` extension. Keep vector retrieval optional.

**Reason:** A standard SQLite file is easy for local tools and the Go driver avoids a CGo build dependency. The vector extension keeps semantic retrieval local without another server process.

**Risk:** Verify the chosen driver and extension compile on supported macOS architectures and confirm the SQL functions at runtime. Keep normal tables portable if vector support changes.

**Status:** Driver and vector path are implemented and the project builds; vector-query runtime behavior remains to be verified.

### D-008 — Embeddings are provider-generated, index-stored

**Decision:** Keep embeddings optional and versioned. The database performs retrieval; a configured provider generates vectors.

**Reason:** A database engine does not generate semantic embeddings by itself, and local privacy should be preserved by default.

**Status:** Accepted behavior; default embedding model/provider is open.

### D-009 — Coding agents are primary; dashboard is a visual workspace

**Decision:** Agents use one local CLI code call for bootstrap, retrieval, and writes. The dashboard serves metrics, search, record inspection, and user-directed data changes.

**Reason:** The active chat needs to receive write results and the refreshed context patch directly. The dashboard is a visual companion.

**Status:** Implemented in the HTTP surface, repository instructions, user-level Codex/Claude profiles, and installed local CLI.

### D-010 — Per-record custom fields instead of agent-controlled SQL schema

**Decision:** Let agents add JSON-compatible document properties inside `metadata_json`; expose `describe`, `update`, and audited delete operations. Do not expose raw SQL or schema-alteration capabilities.

**Reason:** Agent results vary by task, but changing the database schema dynamically increases migration and security risk. Document-scoped fields retain the flexible document-store feel while preserving a stable SQLite schema.

**Status:** Implemented with field validation, full-text indexing, and audit flags for content/custom-field edits.

### D-011 — Local data transfer formats

**Decision:** Provide import/export for JSON, Markdown, YAML, TOML, PDF, and `.db` snapshots. Portable records merge on import; full database imports do not replace the active file.

**Limit:** PDF exports are human-readable reports. Imported PDFs are preserved as bounded base64 source attachments and are not text-extracted.

**Status:** Implemented in CLI and dashboard.

## Open decisions for implementation

1. Which local embedding provider is available and light enough on Paul's Mac? If none, is an explicitly configured remote provider acceptable?
2. Which host startup mechanisms can reliably bootstrap context in current Codex and Claude Code versions without adding MCP?
3. What initial data size and response-size budget should be used? Measure real captures before setting hard product limits.
4. Should the dashboard be packaged with an optional macOS LaunchAgent installer, or remain manually started with `lighthouse serve`?

## Roadmap

### Phase 0 — Validation spike (partially complete)

- Build a tiny Go program that opens a local database file, runs a migration, stores JSON-like records, commits a revision, and performs native vector search.
- Confirm the current Go SDK package, supported macOS architectures, durability behavior, multi-connection access, and backup/restore.
- Verify Starlark invocation, read/write API behavior, timeout and output limits, and document that in-process execution does not impose a hard memory ceiling.
- Decide the database engine and capture the decision here before building UI.

### Phase 1 — Durable capture and retrieval (initial implementation)

- Implement migrations and record/memory repositories.
- Add CLI capture, exact search, lexical search, context load, and export/backup.
- Preserve provenance and version memory changes.
- Add optional embedding provider interface and vector indexing without blocking writes.

### Phase 2 — Safe agent-first code-call workflow (initial implementation)

- Implement bounded read/write code calls, structured outputs, audit events, and context revision patches.
- Add project-level Codex and Claude instructions/skills that make the local code call the primary agent interface.
- Add startup hooks only after each host's lifecycle behavior has been confirmed.

### Phase 3 — Lightweight local dashboard (initial implementation)

- Implement overview metrics and activity chart, searchable result tables, flexible document editor, memory review/editing, audit flags, and import/export.
- Match the monochrome visual specification and preserve the exact finish overrides.
- Keep HTML server-rendered; no client-side runtime is required.

### Phase 4 — Quality and packaging

- Exercise backup/restore, corruption/recovery, large artifacts, concurrent reader/writer use, prompt-size limits, and sandbox failure handling.
- Package a signed or otherwise trustworthy local executable with bundled static assets and font license.
- Document install, manual start/stop, upgrade, and data-location behavior.

### D-012 — Go executable with embedded Starlark code calls

**Decision:** Keep the agent interface as a Starlark script executed in-process by the already-built Go CLI. Do not launch Python/Lua or compile generated Go, Rust, C, C++, or Java helpers for each request.

**Reason:** Lighthouse calls perform short local SQLite operations. Embedded Starlark provides batchable code-shaped calls with typed capabilities, while installation and execution need only the Lighthouse executable. It avoids MCP transport and tool-manual overhead. A local call has no separate hosted execution charge; model generation and interpretation still use the agent provider's normal tokens.

**Status:** Implemented. The script syntax is Python-like but is Starlark; it does not invoke Python.

### D-013 — Show live database footprint in overview

**Decision:** Report the SQLite main-file size and active WAL sidecar size, and show their sum as the current database footprint.

**Reason:** WAL contains committed changes until checkpointed, so main-file-only size can under-report the current on-disk database footprint. Transient `-shm` and filesystem allocation overhead are excluded.

**Status:** Implemented in dashboard overview.

### D-014 — Publish a Go library and installable CLI from the canonical module path

**Decision:** Use `github.com/paulwritescode/lighthouse` as the Go module path, expose a small public library API at the module root, and keep the CLI in `cmd/lighthouse` for `go install`.

**Reason:** Go users should be able to add Lighthouse with `go get` and call supported CRUD/context operations as a library, while agents and operators can install the standalone CLI without cloning the repository.

**Status:** Implemented; CLI installation and library import are documented.

### D-015 — Bulk dashboard CRUD uses explicit selection or filters

**Decision:** Keep individual edit/delete controls and add selected-row and all-matching batch updates/deletes for results and memory. Import remains the batch-create path.

**Reason:** Operators can change one item, a chosen set, or a filtered set without introducing a client-side framework or raw SQL controls.

**Limit:** Bulk operations require POST + CSRF; deletes need an additional confirmation; each item is independently audited and batches cap at 10,000 records. A failure can leave earlier items committed and reports the completed count.

**Status:** Implemented in the server-rendered dashboard.
