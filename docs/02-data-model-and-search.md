# Lighthouse — Data model and retrieval

## Modeling principle

Keep original material and derived memory separate. Search can return a generated summary, but a user or agent must be able to inspect the underlying artifact and see where the summary came from.

The proposed schema is conceptual; final DDL depends on the storage-engine spike. Use migrations and stable IDs. Store structured metadata as JSON where fields vary, but keep commonly filtered fields as typed columns.

## Core entities

### `artifacts`

One source result or mutable document. Core fields are fixed; additional per-record properties live in JSON metadata:

- `id` — stable text/UUID identifier.
- `title`, `content`, `kind`.
- `source_agent` — e.g. codex, claude-code, other, user.
- `project_key` — optional stable project identifier, with a display name.
- `session_key` — optional originating conversation/session identifier.
- `created_at`, `captured_at`.
- `metadata_json` — model, task, source path, labels, confidence, or agent-defined JSON fields. Fields are scoped to the document and do not create SQLite columns.
- `content_hash` — deduplication and change detection.
- `trust_class` — user-confirmed, source quote, agent-generated, imported/unknown.

Preserve provenance when a document is updated. Lighthouse refreshes its content hash, rebuilds the full-text index, advances the scope revision, and records an audit event. Content and custom-field changes are flagged for review because they can change what an agent retrieves.

### `memory_items`

Small reusable facts, decisions, preferences, or open questions derived from artifacts. Suggested fields:

- `id`, `scope_type` (global, project, session), `scope_key`.
- `kind` (fact, decision, preference, constraint, open_question, summary).
- `body`, `status` (active, superseded, retracted).
- `source_artifact_id` plus an optional source span or excerpt.
- `authorship` and `confidence` when applicable.
- `created_at`, `updated_at`, `supersedes_id`, `revision`.

A summary is not automatically a verified fact. The UI and agent result must make provenance and trust class visible.

### `context_snapshots`

A bounded, versioned context pack for a specific chat/session and scope. Suggested fields:

- `id`, `session_key`, `scope_key`, `revision`.
- `rendered_context`, `created_at`, `source_set_hash`.
- token/character estimate and included-item count.
- status (`current`, `stale`, `archived`).

Store included memory IDs and artifact IDs in a join table so the snapshot can be reconstructed and audited. A snapshot is a convenience view; the artifacts and memory items remain canonical.

### `embeddings`

One vector per searchable chunk and embedding model/version. Keep model name, dimensionality, creation time, and source hash beside it. A model change requires a separate embedding version rather than mixing incompatible dimensions in one index.

### `change_events`

A compact local audit trail for mutations: operation ID, target, actor class, revision, timestamp, summary, and a sensitive-review flag. Do not store access tokens or complete generated code here by default. Deletes, imports, custom-field/content edits, and memory changes are flagged.

## Retrieval pipeline

1. Normalize the user question and identify project/session scope when available.
2. Run exact/lexical search over title and content.
3. If embeddings are configured, create a query embedding and run vector nearest-neighbor search.
4. Combine and rank candidates; remove duplicates and superseded memory items.
5. Apply explicit scope and trust-class filters.
6. Return a small set of excerpts with record IDs, source names, dates, and provenance.
7. Assemble a bounded context snapshot, recording the source set and revision.

Always preserve source IDs and citation metadata in the response to the agent. Semantic similarity is a retrieval signal, not proof that two statements are equivalent.

## Hybrid search

Lighthouse should support both lexical and semantic search. Exact terms, filenames, identifiers, error strings, and code symbols often work better with keyword search; conceptual questions benefit from semantic retrieval. The search API should return separate lexical and vector scores plus a normalized combined score, rather than hiding why an item ranked highly.

The implementation uses SQLite FTS5 for lexical search and stores optional vectors alongside artifacts. The current vector query path uses the bundled `sqlite-vec` SQL function. Verify both FTS5 and vector query behavior in the target Go build before relying on them in a release.

## Embedding policy

- Capturing an artifact succeeds even if embedding generation fails.
- Each chunk tracks `pending`, `ready`, `failed`, or `not_requested`.
- Store provider and model identifiers for reproducibility.
- Chunking must preserve the parent artifact ID and offsets/section heading.
- Remote embedding providers are opt-in because artifact content can be private.
- Re-indexing is idempotent and can be resumed after a crash.

## Data retention and portability

- Provide import/export for JSON, Markdown, YAML, TOML, PDF, and full SQLite snapshots.
- JSON/YAML/TOML are structured interchange. Markdown carries a machine-readable Lighthouse payload. PDF is a review report; imported PDFs remain raw bounded attachments because text extraction is not bundled.
- Provide a consistent backup command that uses the database engine's online backup/checkpoint mechanism; never copy a live database and its sidecars casually.
- Delete operations mark dependent indexes for cleanup in the same transaction or a recoverable maintenance queue.
- Version schema migrations; include a restore path and preserve the original file before a migration.
