# Lighthouse — Security and operations

## Local boundary

Lighthouse is designed for one local user. The database and dashboard must not be exposed to the LAN or internet by default. The server binds to `127.0.0.1` only. It uses no cloud account or remote service unless the user explicitly configures an embedding provider or later sync feature.

## Generated-code boundary

Code mode executes agent-authored logic, so capability limits are a core design requirement:

- Expose typed methods, not a raw DB handle, `os/exec`, file APIs, arbitrary SQL, or ambient process environment.
- No network calls from the sandbox.
- Enforce per-call timeout, instruction budget, maximum input/output bytes, and maximum result count. The current embedded runner does not enforce a hard memory/heap budget.
- Validate all arguments and JSON payload sizes at the Go service boundary.
- Use parameterized SQL inside the trusted repository implementation.
- Keep read and write capabilities explicit; writes run in transactions and return revisions.
- Treat stored agent content as untrusted text. It is data, never instructions for Lighthouse or its renderer.
- Render document content escaped by default. Do not allow stored HTML/JavaScript to execute in the dashboard.
- Record a concise mutation audit trail with actor class and revision; do not log secrets or full scripts by default.

An embedded language runtime is a policy boundary, not a substitute for resource limits. Before shipping, verify the chosen runtime's sandbox behavior and cancellation support. If it cannot reliably stop runaway code, run calls in a restricted child process with OS-level limits, despite the added implementation cost.

## Dashboard boundary

- Bind only to loopback. Reject non-loopback bind addresses.
- Reads are available over GET; mutations use POST forms with a per-process CSRF token, a `SameSite=Strict` HttpOnly cookie, and same-origin validation when an Origin header is present.
- Validate `Host` against loopback/Lighthouse hostnames to reduce DNS-rebinding and cross-origin local access risks. Do not enable permissive CORS.
- Cap multipart import size, derive format from an allowlisted extension, ignore uploaded paths, and write only to generated temporary files.
- Serve local static assets; no third-party analytics, fonts, or scripts. Keep stored text escaped by `html/template`.
- Export is a read operation. Import, edit, and delete are audited. Artifact content/custom-field changes, memory changes, imports, deletes, and scope moves are marked as sensitive review events.
- Provide a visible local-only security summary and an audit trail. A loopback dashboard is not authentication: any local process with browser access can potentially interact with it.

## Database safety

- Use transactions for multi-row writes and memory revision changes.
- Keep writes short; do not hold a transaction open while generating embeddings or waiting on a model.
- Use the selected engine's backup/checkpoint APIs for consistent backups.
- Run schema migrations before serving requests; preserve a backup before destructive migrations.
- Verify recovery from forced termination and a full disk during the implementation spike.
- Do not let agents write arbitrary database files or modify the schema directly.
- Flexible custom fields are validated JSON stored inside `metadata_json`; agents cannot define SQL columns, indexes, triggers, or executable code.
- Data imports merge records. Artifact IDs and active memory keys deduplicate incoming records; imported document data remains untrusted.

## Privacy and embeddings

- Database contents are local by default. Do not send stored content outside the machine without a configured provider.
- Label embedding provider and destination in settings and in each embedding record.
- Provide a no-embedding mode; keyword search and exact search remain available.
- Before remote embedding, send only the text needed for that embedding operation and clearly indicate the provider choice.
- Never store API keys in SQLite rows or in the dashboard's HTML. Use the user's environment or OS credential store only if a remote provider is later enabled.

## Backup and recovery

- Provide `backup`, `restore`, and `export` commands with clear destination paths.
- Backups include a manifest with schema version and embedding model metadata.
- Restore is validated before replacing the active database; the old database is renamed aside rather than deleted.
- Export includes original artifacts and provenance. A separate option can include derived summaries and vectors.
- JSON, Markdown, YAML, and TOML are interchange formats. Markdown exports include a machine-readable Lighthouse payload. PDF is a readable report; PDF import keeps a bounded raw attachment and does not extract text.
- `.db` export uses SQLite `VACUUM INTO` after a WAL checkpoint. `.db` import opens the upload read-only and merges artifacts and active memories into the current database; it does not replace the live database or import its audit log.
- Do not assume a synced folder is a safe live database location. Export snapshots can be synced safely after completion.
