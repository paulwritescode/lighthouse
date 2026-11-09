# Changelog

## 0.3.0 — 2026-11-09

- Align the Go module path with the public GitHub repository and document versioned `go install` for the CLI.
- Add a public Go library package for opening the shared/default database and performing artifact and memory CRUD, search, context loading, counts, and audit reads.
- Add record selection and bulk update/delete for individual selections or all records matching the active filters.
- Clarify that bulk writes are individually audited and can report partial completion.

## 0.2.0 — 2026-11-09

- Add current SQLite footprint to the overview, counting the main file and active write-ahead log.
- Refine the overview into a compact, ruled metric strip and improve reduced-motion-aware disclosure feedback.
- Clarify that agent code calls run embedded Starlark within the Go CLI, not Python or MCP.
- Expand setup, cost, transfer, security-boundary, and runtime-choice documentation.

## 0.1.0 — Initial implementation

- Local Go CLI and SQLite-backed agent result and memory store.
- Constrained code calls, search, memory refresh, audit history, and optional vector support.
- Loopback Go-template dashboard, record management, and local import/export.
