# Lighthouse — Agent code call and context lifecycle

## Product rule

Coding agents are the primary interface for retrieval and structured writes. The local executable exposes one constrained Starlark code call so an agent can inspect the available document fields, batch operations, and return only relevant data. The browser dashboard is an optional visual workspace for reviewing, editing, importing, and exporting the same SQLite data.

Lighthouse does not use MCP. Codex and Claude Code can call the executable through the local command runner available to their desktop or CLI sessions. A host that cannot start local commands cannot use this integration directly; do not add MCP as an implicit fallback.

## One-call interface

The caller sends one Starlark script over stdin and reads structured JSON from stdout:

Starlark syntax is intentionally Python-like, but the script is interpreted by the Go-embedded Starlark runtime. Lighthouse does not start Python, Lua, or another process to execute a code call; it does not compile the submitted script each time. Build the Lighthouse CLI once for the local machine, then invoke it when an agent needs database context or a write.

```sh
lighthouse code --project my-project --session optional-session < examples/bootstrap.star
```

The session flag is optional. Use a stable conversation identifier only when the host provides one; otherwise omit it. The project flag sets the default scope for memory and search operations.

Available builtins:

| Builtin | Purpose |
|---|---|
| `load_memory(project="", session="", limit=10)` | Build and persist a compact context snapshot. Returns its ID, revision, context text, memories, and recent artifacts. |
| `search(query, project="", limit=10)` | Full-text artifact search. |
| `get(id)` | Retrieve an artifact by stable source ID. |
| `describe(project="")` | Inspect the artifact collection's fixed fields and the custom document fields currently in use. |
| `audit(limit=25)` | Review recent mutations, their actor, scope, revision, and sensitive-action flag. |
| `save(title, content, project="", agent="agent", kind="result", fields={})` | Store a document with optional arbitrary JSON-compatible custom fields and return the committed artifact plus a refreshed context snapshot. |
| `update(id, fields={...})` | Update core attributes or merge any custom fields into the document. |
| `delete(id)` | Permanently delete an artifact and audit the sensitive operation. |
| `upsert_memory(key, body, project="", kind="fact", source_id="")` | Create or update durable memory and return it with a refreshed context snapshot. |
| `delete_memory(id, project="")` | Retract a memory item and audit the sensitive operation. |
| `emit(value)` | Serialize one Starlark value to JSON on stdout. |

All builtins use validated domain operations. Generated code has no raw SQL, filesystem, environment, shell, or network access. Input, output, instruction count, collection sizes, and wall time are bounded. The in-process runner is intended for trusted local agent scripts, not hostile code; it has no hard memory limit. Custom fields are stored in each artifact's JSON metadata; agents cannot change the SQL schema. New fields must be JSON-compatible and stay within the configured size and field-count limits.

## Chat bootstrap

At the start of a substantive coding task, the agent makes one call:

```python
pack = load_memory(limit=12)
emit({
    "snapshot_id": pack["snapshot_id"],
    "revision": pack["revision"],
    "context": pack["context"],
})
```

When the caller supplies `--project`, it becomes the default scope. The agent includes this returned context in the active conversation and reuses it across turns. It should not reload the full pack on every turn. If more detail is needed, it can batch a search and selective source reads into one later code call.

The loaded conversation text is the live cache. Lighthouse stores durable rows and snapshots, but does not push updates into a provider or mutate hidden prompt state.

## Retrieval

```python
matches = search("deployment decision", limit=8)
emit([
    {"id": item["id"], "title": item["title"], "kind": item["kind"]}
    for item in matches
])
```

The agent should emit only details relevant to its current question. Use `get(id)` when the selected result's complete source is needed. Preserve IDs and provenance in summaries so the source remains checkable.

## Writes and context refresh

Every successful data write advances the project scope revision and returns a freshly loaded context snapshot. The agent must emit and consume the returned `context_patch`:

```python
saved = save(
    title="SQLite storage choice",
    content="The source-backed result to retain.",
    project="lighthouse",
    agent="codex",
    kind="decision",
)
emit(saved)
```

```python
updated = upsert_memory(
    key="storage-choice",
    body="Lighthouse uses a local SQLite file.",
    project="lighthouse",
    kind="decision",
)
emit(updated)
```

The response includes the committed `artifact` or `memory`, `revision`, `snapshot_id`, and `context_patch`. The agent treats the latest returned patch as current and includes it in the active conversation. If a script performs multiple writes, the last write's patch is the one to retain. A result write does not silently become durable memory; memory promotion is explicit and should reflect user-confirmed information.

For unfamiliar project data, call `describe()` and `search()` in one script before editing. Fetch complete source with `get(id)`. Use `update(id, fields={...})` to change a document, including an added field such as `{"confidence": 0.9}`. Use `audit()` to inspect earlier changes. Delete only the requested ID; destructive changes are permanently applied to artifacts or mark memory as retracted.

If the database write commits but a refresh fails, the response still returns the committed record plus `refresh_error`. The agent should report the saved record and retry a context bootstrap instead of retrying the write blindly.

## Agent-app setup

The command is local-process based, not tied to a specific AI API. Add the Lighthouse skill/instructions to each agent's local profile or project so its desktop app and CLI know when to invoke it. The repository contains:

- `AGENTS.md` for Codex and compatible code agents;
- `CLAUDE.md` and `.claude/skills/lighthouse-memory/SKILL.md` for Claude Code;
- `.agents/skills/lighthouse-memory/SKILL.md` for Codex;
- copyable usage samples in `examples/`.

Install the `lighthouse` binary on `PATH` once. The agent uses `lighthouse code` for bootstrap, retrieval, and writes. It does not need the dashboard daemon. Use `lighthouse export --format json|md|yaml|toml|pdf|db` and `lighthouse import --format ...` for portability; the dashboard provides the same choices. PDF exports are human-readable reports; imported PDF files are retained as source attachments without text extraction. See [agent integration setup](agent-integration.md).

Do not promise universal automatic startup hooks. Agent desktop/CLI capability and lifecycle rules can differ by version. If automatic startup is desired, add only host-supported instructions or hooks and verify their current behavior.
