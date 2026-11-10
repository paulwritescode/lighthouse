---
name: lighthouse-memory
description: Load, search, and update local Lighthouse context from Codex, Claude Code, or another coding agent that can run a local command. Use at the start of substantive project work and when retrieving or saving agent results or durable memory. No MCP.
---

# Lighthouse local context

Use the installed `lighthouse` CLI through the agent's local command runner. If it is missing and Go 1.27 or newer is available, install it with `go install github.com/paulwritescode/lighthouse/cmd/lighthouse@latest`; see [the setup guide](https://github.com/paulwritescode/lighthouse#install-the-cli). Check that `GOBIN` or `GOPATH/bin` is on `PATH`. It talks directly to the local SQLite file; the dashboard does not need to run and is an optional visual workspace for review and data management. If installation fails, continue without Lighthouse and never switch to MCP.

At the beginning of substantive coding work, load one compact context pack:

```sh
lighthouse code --project my-project <<'STAR'
pack = load_memory(limit=12)
emit({"snapshot_id": pack["snapshot_id"], "revision": pack["revision"], "context": pack["context"]})
STAR
```

Set `--project` to the current workspace's stable project key. Pass `--session` only if a stable active conversation identifier is available. Keep returned context in the current conversation and reuse it; the Starlark runtime itself is not cached between calls.

For retrieval, batch the query and any local filtering in one script. Search returns structured artifacts; use `get(id)` only when full source is needed. Emit only relevant, source-linked results.

For writes, always emit the returned value so the agent receives the committed record and context refresh:

```python
result = save(title="Short title", content="Source-backed result", kind="result")
emit(result)
```

```python
updated = upsert_memory(key="stable-key", body="One useful durable fact", kind="fact")
emit(updated)
```

For each write, use the stable `--project` key as the primary tag. Add up to 20 secondary tags with `tags=["existing-tag", "topic"]` when the conversation supplies clear topics. Normalize names to lowercase kebab-case, reuse existing tags from search results or the Tags dashboard when they fit, and avoid inventing broad or redundant tags.

Both writes return the record plus refreshed `revision`, `snapshot_id`, and `context_patch`. Apply the latest patch to the active conversation; Lighthouse cannot change hidden provider context. Store durable facts only when the user asked or confirmed. The dashboard can edit data through protected forms, but agent code calls remain the primary interface. Do not add MCP or use direct SQL.

## Flexible documents and changes

Use `describe(project="...")` to learn the fixed artifact fields and custom fields currently present. Use `search(...)` and `get(id)` to inspect source records before changing them. Artifacts accept arbitrary JSON-compatible per-record fields through `save(fields={...})` or `update(id, fields={...})`; these fields do not alter the SQLite schema. Updates return the saved record and refreshed context patch.

Use `delete(id)` or `delete_memory(id)` only when the user's task explicitly calls for deletion. Memory is normally revised with `upsert_memory(key=..., body=...)`. `audit(limit=...)` returns recent changes and flags updates, imports, deletes, and other security-relevant operations. Emit the audit rows when the user asks for change review. Never use raw SQL or filesystem access from the code call.

Use `lighthouse export --format json|md|yaml|toml|pdf|db` and `lighthouse import --format ...` for portability. The dashboard has matching import/export controls. PDF exports are readable reports; imported PDF files are stored as bounded raw attachments without text extraction. SQLite imports merge records and skip duplicate artifact IDs and active memory keys; they do not replace the live database.
