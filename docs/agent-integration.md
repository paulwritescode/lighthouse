# Install Lighthouse for local code agents

Lighthouse calls are local CLI processes. This keeps the agent connection direct and avoids an MCP server or a running dashboard. Codex and Claude Code can use it from any session that has access to local commands; the Claude Desktop Code tab runs Claude Code locally.

## 1. Install the command

Install the published executable with Go:

```sh
go install github.com/paulwritescode/lighthouse/cmd/lighthouse@latest
```

To pin the current release, use `@v0.3.0`. Go installs the binary to `GOBIN` or `$(go env GOPATH)/bin`; add that directory to `PATH`. Lighthouse requires Go 1.27 or newer for installation. Confirm the CLI is available:

```sh
lighthouse code --help
```

The default SQLite file is under the OS application config directory. `LIGHTHOUSE_DB` or `--db` can select a different file.

The Go library is available from `github.com/paulwritescode/lighthouse`. Add it to a Go module with `go get github.com/paulwritescode/lighthouse@v0.3.0`, then use `lighthouse.OpenDefault()` to share the CLI's database or `lighthouse.Open(path)` for a separate SQLite file. `go get` adds a library dependency; `go install` installs the CLI.

## 2. Install the agent skill for all projects

Codex discovers user skills from `~/.agents/skills`; Claude Code uses `~/.claude/skills`. If you cloned the repository, copy the bundled skill files. If you installed only the CLI, download the versioned skills directly:

```sh
mkdir -p "$HOME/.agents/skills" "$HOME/.claude/skills"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.3.0/.agents/skills/lighthouse-memory/SKILL.md -o "$HOME/.agents/skills/lighthouse-memory/SKILL.md"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.3.0/.claude/skills/lighthouse-memory/SKILL.md -o "$HOME/.claude/skills/lighthouse-memory/SKILL.md"
```

Add the included user-profile instructions so the agent knows to load context at the start of project work. Append each block once; if the destination already has other instructions, keep them and add this block at the end:

```sh
mkdir -p "$HOME/.codex" "$HOME/.claude"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.3.0/integrations/user-profile/codex-AGENTS.md >> "$HOME/.codex/AGENTS.md"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.3.0/integrations/user-profile/claude-CLAUDE.md >> "$HOME/.claude/CLAUDE.md"
```

For repository-only setup, the project already contains both skill folders plus `AGENTS.md` and `CLAUDE.md`.

## 3. Use one code call per need

At the beginning of substantive project work, ask the agent to load context once. The installed skill describes the exact bootstrap. The agent should reuse that context in the conversation, call again only for missing source details, and apply the newest `context_patch` after a write.

The CLI examples in `examples/` can be run like this:

```sh
lighthouse code --project my-project < examples/bootstrap.star
lighthouse code --project my-project < examples/search.star
```

For writes, agents pass the Starlark script through stdin and emit the returned write object. The response includes the committed record and refreshed context metadata.

The code call also exposes `describe`, `audit`, `update`, `delete`, and `delete_memory`. New document-specific properties go into JSON `fields`, not new SQL columns. Use the agent instruction files for deletion rules and sensitive-change review. The dashboard allows user-directed changes through same-origin CSRF-protected forms.

CLI portability examples:

```sh
lighthouse export --format yaml --output lighthouse-export.yaml
lighthouse import --format yaml --file lighthouse-export.yaml
lighthouse export --format db --output lighthouse-snapshot.db
lighthouse import --format db --file lighthouse-snapshot.db
```

The same formats are available in the dashboard: JSON, Markdown, YAML, TOML, PDF, and SQLite `.db`. PDF reports are human-readable. PDF import retains the raw file as a bounded attachment without text extraction. SQLite imports merge records rather than replacing the open database.

## Host limits

The integration requires the agent session to be able to start a local executable. It does not provide a remote connector. If a desktop chat mode cannot run local commands, use its coding mode/CLI that can, or continue without Lighthouse. No data is sent to an AI provider by the CLI itself; the agent sees only stdout it requested and decides what to include in the conversation.
