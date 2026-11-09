# Lighthouse — Product brief

## Purpose

Lighthouse is a local archive and retrieval layer for AI coding-agent work. It helps Paul keep useful outputs from Codex, Claude, and other coding agents, find prior work later, and give a new or continuing chat a compact, current project memory.

## Problem

Useful agent results are scattered across chats, terminals, markdown files, and project folders. A new chat often starts without the facts, decisions, code references, or open questions established in earlier work. Copying all prior output into each prompt is noisy, expensive, and difficult to keep current.

Lighthouse keeps complete source records on the local machine and prepares a smaller context pack on demand. The pack is an explicit retrieval result, not a claim that the model remembers every database record.

## Users

- **Coding agents:** primary users. Codex, Claude Code, and other agents with local command access load scoped context, search source-backed records, and write results or user-confirmed memory through one bounded code call.
- **Paul / local operator:** uses the agent-facing CLI as the primary data path and the optional dashboard to review metrics, inspect, edit, import, and export stored records.

## Outcomes

- A useful agent result can be captured with provenance and found by exact text, metadata, or semantic similarity.
- A chat can load a concise context pack at its beginning and refer to its revision during the session.
- A successful write advances the scoped revision and returns a refreshed context patch for the current chat.
- The user can inspect, correct, export, and delete what is stored without needing a cloud service.
- Dashboard interaction stays responsive on a local machine and does not require a JavaScript application bundle.

## Main capabilities

1. Capture an artifact with source agent, project, session, timestamp, content, and optional metadata.
2. Search artifacts by words, project, agent, date, type, and semantic similarity.
3. Create a session context snapshot from selected memory items and source excerpts.
4. Execute a short multi-step code call that can read and write through a limited Lighthouse API.
5. Return an updated snapshot revision and a compact context patch after a memory-changing write.
6. Inspect metrics, search and browse records, and create, edit, or delete individual or selected batches through a locally served dashboard protected by same-origin and CSRF checks.
7. Export and back up the database in a documented, portable way.

## Product boundaries

- Local-only by default. There is no hosted account, remote sync, or always-on server in v1.
- No MCP server or MCP dependency.
- No unrestricted SQL surface exposed to generated code.
- No automatic promotion of model guesses to durable facts. Durable memory must retain provenance and distinguish source text, user-confirmed facts, and agent-generated summaries.
- No promise that the system can modify an AI provider's hidden prompt cache or silently replace text already in an active conversation.
- No requirement to retain every conversation transcript. Capture is deliberate, and the operator can delete records.
- The dashboard is optional and is not the storage engine. It is bound to loopback and audits user-directed changes.

## Success measures for a later implementation

- Start the CLI and answer simple local lookups without launching a service.
- The dashboard can be closed without affecting persistence.
- Search results include the source artifact and provenance needed to verify the claim.
- A single code call can perform several reads and return a bounded, size-limited result.
- A write is atomic and reports the resulting memory revision.
- The application works offline except when an explicitly selected embedding provider needs network access.
- The built binary, static assets, and runtime memory use are measured on the user's Mac before setting hard numeric budgets.
