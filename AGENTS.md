# Lighthouse agent workflow

Lighthouse is the primary local interface for agent context, retrieval, and writes. Use its CLI from the coding agent's local command runner. Do not add MCP for Lighthouse.

If the `lighthouse` CLI is missing and Go 1.27 or newer is installed, follow [the install and agent setup sections of the README](README.md#install-the-cli) and install it with `go install github.com/paulwritescode/lighthouse/cmd/lighthouse@latest`; ensure `GOBIN` or `GOPATH/bin` is on `PATH`. If installation is unavailable, continue without Lighthouse and do not add MCP.

At the beginning of a substantive task in this repository, make one bootstrap call and reuse that context in the conversation:

```sh
lighthouse code --project lighthouse <<'STAR'
pack = load_memory(limit=12)
emit({
    "snapshot_id": pack["snapshot_id"],
    "revision": pack["revision"],
    "context": pack["context"],
})
STAR
```

For another project, set `--project` to that repository's stable project key. Supply `--session` only when the host gives you a stable conversation identifier; do not invent one. If the CLI is not installed or cannot be run locally, continue the task without Lighthouse and say so only when it affects the result.

For retrieval, combine `search(...)` and `get(...)` in one `lighthouse code` call, then `emit` only the source-backed details needed for the current task. Keep the loaded context in the conversation; do not reload it on every turn.

For a result write, call `save(...)` and `emit` its response. For durable memory, update only information the user asked to retain or confirmed, call `upsert_memory(...)`, and `emit` the response. Both writes return the committed record plus a refreshed `revision`, `snapshot_id`, and `context_patch`. Include the latest patch in the active conversation so it supersedes stale context.

Use the local code call or CLI as the primary interface. The dashboard is an optional visual workspace and can edit, import, and delete records through CSRF-protected same-origin forms. For new or unfamiliar data, inspect with `describe()` and `search()` before changing anything. Add new document properties as JSON `fields`; never alter the SQL schema through agent code. Use `update(id, fields={...})` for targeted edits, `delete(id)` or `delete_memory(id)` only when the user's task calls for deletion, and inspect `audit()` when security-sensitive data changes matter. All writes return the refreshed context patch; apply the latest patch in the active conversation.

Use `lighthouse export --format json|md|yaml|toml|pdf|db` and `lighthouse import --format ...` for portable backups and interchange. PDF is a report; imported PDFs are retained as source attachments without text extraction. Dashboard imports are capped and merged, never used to replace the live database.

See the [README](README.md) for installation, agent setup and security boundaries, and the project website documentation for the complete code API.
