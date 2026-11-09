# Changelog

## Unreleased

## 0.4.1 — 2026-11-10

### Changed

- Open first-time setup directly on the local-address question, then ask about the startup service without extra welcome or completion screens.
- Use a compact, colored setup prompt with quick `y` and `n` answers and an animated progress indicator while non-interactive setup work runs.
- Show an animated release-check indicator and clearer blue, green, and red status cues in the control menu.

## 0.4.0 — 2026-11-10

### Added

- Add an interactive terminal control menu and OS-native per-user startup services for macOS, Linux, and Windows.
- Add CLI setup and removal for the local `lighthouse` hosts-file entry, plus service start, stop, restart, and force-stop controls.
- Add a one-question-at-a-time first-run setup wizard before the control menu.
- Adapt CLI colors for light and dark terminals while keeping Lighthouse's blue selection and action accents.

### Fixed

- Keep the CSRF token stable across dashboard restarts so open forms remain valid during service updates.
- Restart an installed dashboard service after updating its binary.

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
