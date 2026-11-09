## Lighthouse local memory

For a substantive coding or project task, if `lighthouse` is installed and local commands are available, load one compact context pack at the beginning:

```sh
lighthouse code --project <stable-project-key> <<'STAR'
pack = load_memory(limit=12)
emit({"snapshot_id": pack["snapshot_id"], "revision": pack["revision"], "context": pack["context"]})
STAR
```

Use the current repository's stable project key. Pass `--session` only when the host provides a stable conversation ID. Reuse the returned context during the conversation. Inspect unfamiliar data with `describe()` and `search()` before editing; use `get(id)` for the complete record. Add arbitrary JSON document properties with `save(fields=...)` or `update(id, fields={...})`; never alter SQLite schema through agent code. Use `delete(id)` and `delete_memory(id)` only when the user's task explicitly calls for deletion. Emit every write response and apply its latest `context_patch` in the active conversation; Lighthouse cannot modify hidden provider context. Use `audit()` to review recent writes and sensitive flags. Do not use MCP or direct SQL. The dashboard is an optional visual workspace with protected edit/import/export actions. Skip Lighthouse for simple standalone questions, tasks with no relevant local project context, or when the user asks not to use it. See `docs/03-code-call-and-memory.md` and `docs/05-security-and-operations.md`.
