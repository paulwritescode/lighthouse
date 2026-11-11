# Changelog

## Unreleased

### Added

- Render saved result and memory Markdown as readable dashboard content, with full memory and project detail pages.
- Paginate audit history and expand individual events to inspect their full summaries and IDs.

### Changed

- Refresh the dashboard with a white canvas, native system typography, black pill controls, and quieter borders.

## 0.6.1 — 2026-11-10

### Fixed

- Apply dashboard result and memory sorting before the display limit, so sorted views include the correct matching records.

## 0.6.0 — 2026-11-10

### Added

- Add one primary tag and multiple secondary tags to results and memory, with normalization, dashboard filtering, sorting, grouping, agent code-call support, and CLI `--tags` input.
- Add a Data tools danger zone that removes and recreates the local database after CSRF, explicit confirmation, and a native macOS administrator prompt.

### Changed

- Explain the base SQLite file and write-ahead log size when no results or memory are saved.


## 0.5.0 — 2026-11-10

### Added

- Add a dark-default dashboard with a persistent light-mode switch and matching logo and favicon.
- Add dedicated Projects, Activity, and Data tools pages, with project groups derived from existing result and memory keys.
- Add record-mix and project-distribution charts alongside the seven-day activity chart.

### Changed

- Rebuild dashboard spacing, tables, forms, and empty states around a compact visual system that works on desktop and mobile.
- Place memory edit and delete controls alongside each record and group bulk actions below the record tables.
- Use browser confirmation dialogs for destructive dashboard actions while keeping server-side confirmation and CSRF checks.
- Show create and update notices on result and memory pages.

## 0.4.4 — 2026-11-10

### Changed

- Tighten the interactive control menu by removing blank section gaps and outer vertical padding.
- Keep the current action visibly selected in a standard 24-line terminal.

## 0.4.3 — 2026-11-10

### Fixed

- Re-enable the macOS user LaunchAgent before loading it, including when an earlier service removal left it disabled.
- Retry a transient `launchctl bootstrap` error while a previous service instance finishes unloading.
- Explain that the startup service must be installed from the signed-in account without `sudo`.

## 0.4.2 — 2026-11-10

### Fixed

- Accept local dashboard form submissions from browsers that send `Origin: null` when the host-only CSRF cookie and hidden token both match. Bulk deletion and other protected forms now work in those browsers while foreign origins and invalid tokens remain blocked.
- Parse explicit origins as HTTP URLs and require an exact host match.

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
