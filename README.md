# Lighthouse

Lighthouse is a local-first store for coding-agent results and durable memory. Agents use one constrained Starlark program through the `lighthouse code` CLI. A Go server provides an optional, loopback-only dashboard for reviewing records, metrics, edits, audit events, and data transfer. The canonical store is a portable SQLite file; there is no MCP server or cloud service in the data path.

## How an agent uses it

Install the `lighthouse` executable and the agent skill. Start substantive work with one context-load call, then reuse that context during the conversation. Batch any needed searches or reads into one script and emit only relevant results. After a write, consume the returned committed record, revision, and context patch so the active conversation can refresh its memory. Lighthouse cannot silently update hidden provider context.

```sh
lighthouse code --project my-project < examples/bootstrap.star
```

The code API includes `load_memory`, `search`, `get`, `describe`, `audit`, `save`, `update`, `delete`, `upsert_memory`, `delete_memory`, and `emit`. Documents accept flexible JSON custom fields without agent-controlled SQL schema changes. Deletions are explicit and audited. See [Set up your coding agent](#set-up-your-coding-agent) and the [examples](examples/).

### Runtime, setup, and cost

The CLI and dashboard are Go programs. The code-call script language is **Starlark**, an embedded Python-like language interpreted in the already-running Go process. It is not a Python subprocess, and Lighthouse does not compile scripts on each call. The language has no shell, filesystem, environment, raw SQL, or network builtins. This avoids a runtime install and a per-call compilation step while keeping a typed, constrained API.

A local CLI invocation and SQLite read/write do not themselves consume model credits or call a hosted service. The agent still uses model tokens to plan the script and interpret the output; that usage is determined by the agent provider.

| Option | Per-call setup | Fit for this workload |
| --- | --- | --- |
| Embedded Go + Starlark (current) | Run the existing executable; interpret the short script | No separate interpreter or script compilation; typed capabilities |
| Python | Start an interpreter and load script | Easy for standalone scripts, but adds a runtime/process for every CLI call |
| Lua/LuaJIT | Start interpreter; possibly native dependency | Lightweight runtime, but another API/runtime to install and support |
| Go, Rust, C, C++ | Compile source, or run a prebuilt executable | Fast native programs; per-request compilation is unnecessary overhead here |
| Java | Start JVM and provide a JRE, or package a native image | Larger setup for a local helper; not installed in the checked environment |

These are architectural tradeoffs, not a portable performance benchmark: cold start and compilation vary by machine, toolchain, and packaging. The operation itself is a short local database access. The existing Go binary plus embedded Starlark is the simplest measured-to-implement choice; use a prebuilt Go executable for distribution rather than compiling a new helper on every agent turn.

## Install the CLI

Install the published command without cloning the repository:

```sh
go install github.com/paulwritescode/lighthouse/cmd/lighthouse@latest
```

`go install` downloads the Lighthouse module and its dependencies, builds the `lighthouse` command for your machine, and places it in Go's binary directory. It does not install Go, configure a startup service, or start the dashboard. You do not need Git, a source checkout, Node.js, Python, a separate SQLite installation, or a database server.

### Homebrew on macOS

On macOS, Homebrew can install the same tagged CLI release in one command from the [Lighthouse tap](https://github.com/paulwritescode/homebrew-lighthouse):

```sh
brew install paulwritescode/lighthouse/lighthouse
```

Homebrew adds the tap and trusts this formula during installation, builds the pinned release with Go, and manages the installed executable. The shorter `brew install lighthouse` selects an unrelated Ethereum client from Homebrew Core. Use `brew upgrade paulwritescode/lighthouse/lighthouse` for later formula updates. If you installed a startup service, run `lighthouse service restart` after upgrading so it uses the new binary.

### Prerequisites

- **Go 1.27 or newer.** Check with `go version`. If `go` is missing, install it from [go.dev/dl](https://go.dev/dl/) and open a new terminal.
- **Internet access for the initial install.** Normal use of the installed CLI is local; Lighthouse does not send your database to a cloud service.
- **Write access to Go's binary directory:** `GOBIN` if set, otherwise `$(go env GOPATH)/bin` (usually `~/go/bin`). No administrator privileges are needed for a per-user install.
- **That directory on your `PATH`**, so you can type `lighthouse`.

To pin a release for repeatable setups, replace `@latest` with a version:

```sh
go install github.com/paulwritescode/lighthouse/cmd/lighthouse@v0.4.1
```

The first time you run `lighthouse`, an interactive setup guides you through the local hostname and optional sign-in service one question at a time. System-level changes are opt-in. After setup, running `lighthouse` opens the control menu. Run `lighthouse setup` to revisit the setup questions.

If a startup service is already installed, restart it after a manual `go install` with `lighthouse service restart`. The built-in `lighthouse update` command installs into the directory of the running binary and handles stopping and restarting an installed service itself.

### Put it on your PATH

Check whether your shell can find the command:

```sh
command -v lighthouse
```

If that prints nothing, add Go's binary directory to `PATH`. On macOS and Linux with zsh, add this line to `~/.zshrc` (use your `GOBIN` value instead if you set one), then run `source ~/.zshrc`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

For Bash, add the same line to `~/.bashrc` or `~/.bash_profile`. On Windows, add the directory printed by `go env GOBIN` (or `go env GOPATH` plus `\bin`) to your user `Path`, open a new PowerShell window, and check with `Get-Command lighthouse`.

### Verify and initialize

```sh
lighthouse --help
lighthouse init
```

`lighthouse init` creates the default local database and prints its path. Change the location with `LIGHTHOUSE_DB` or `--db /path/to/file.db`. Keep the live database out of synced folders and active Git checkouts.

### Install from source (contributors)

Only needed to modify Lighthouse or build the current checkout:

```sh
git clone https://github.com/paulwritescode/lighthouse.git
cd lighthouse
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/lighthouse" ./cmd/lighthouse
```

Put `~/.local/bin` on `PATH`, or run `go install ./cmd/lighthouse` from the repository root to use Go's standard binary directory.

### Troubleshooting

- **`go: command not found`:** install Go 1.27 or newer and open a new terminal.
- **The module cannot be downloaded:** check internet access, and that any proxy or firewall allows your configured Go module proxy and source host.
- **`lighthouse` not found after install:** check `go env GOBIN` and `go env GOPATH`, add the effective directory to `PATH`, and open a new terminal.
- **An older version runs:** install the version you want explicitly, then use `command -v lighthouse` (or `Get-Command lighthouse`) to see which copy your shell runs.

`go get` adds Lighthouse as a dependency to a Go module; use `go install` to install the CLI executable.

## Set up your coding agent

Installing the CLI is separate from installing agent instructions. Codex and Claude Code need the Lighthouse skill so they know when and how to call the CLI. Codex reads user skills from `~/.agents/skills` and Claude Code from `~/.claude/skills`.

If you installed only the CLI, download the versioned skill and profile files:

```sh
mkdir -p "$HOME/.agents/skills/lighthouse-memory" "$HOME/.claude/skills/lighthouse-memory" "$HOME/.codex" "$HOME/.claude"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.4.1/.agents/skills/lighthouse-memory/SKILL.md -o "$HOME/.agents/skills/lighthouse-memory/SKILL.md"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.4.1/.claude/skills/lighthouse-memory/SKILL.md -o "$HOME/.claude/skills/lighthouse-memory/SKILL.md"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.4.1/integrations/user-profile/codex-AGENTS.md >> "$HOME/.codex/AGENTS.md"
curl -fsSL https://raw.githubusercontent.com/paulwritescode/lighthouse/v0.4.1/integrations/user-profile/claude-CLAUDE.md >> "$HOME/.claude/CLAUDE.md"
```

Append the profile files once; if the destination already has instructions, they are kept and the new block is added at the end. If you cloned the repository, copy `.agents/skills/lighthouse-memory` and `.claude/skills/lighthouse-memory` into those directories and append the files in `integrations/user-profile/` instead. For repository-only use, the project already contains both skill folders plus `AGENTS.md` and `CLAUDE.md`.

The integration needs the agent session to be able to start a local executable. It is not a remote connector. The CLI never sends data to an AI provider; the agent sees only the stdout it requested.

## Use Lighthouse as a Go library

The module also exposes a public Go package for applications that want direct database CRUD without starting the HTTP dashboard or invoking a subprocess:

```sh
go get github.com/paulwritescode/lighthouse@v0.4.1
```

```go
import (
    "context"
    "log"

    "github.com/paulwritescode/lighthouse"
)

func main() {
ctx := context.Background()
db, err := lighthouse.Open("./agent-results.db")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

saved, err := db.CreateArtifact(ctx, lighthouse.ArtifactInput{
    Title: "Build result",
    Content: "The local build completed.",
    Project: "example-project",
    Agent: "codex",
    Kind: "result",
    Fields: map[string]any{"exit_code": 0},
})
if err != nil {
    log.Fatal(err)
}
}
```

The package exports artifact and memory create, read/search/list, update, and delete methods, context loading, counts, and audit access. It stores flexible document properties in JSON fields and owns its SQLite schema; callers should use the public API rather than writing SQL to Lighthouse tables. See [the library package docs](https://pkg.go.dev/github.com/paulwritescode/lighthouse).

Use `lighthouse.OpenDefault()` to open the same database used by the CLI and dashboard, or pass an application-specific SQLite file to `lighthouse.Open(path)`.

## Run the dashboard

The optional dashboard binds to `127.0.0.1:8080`; open `http://127.0.0.1:8080` or `http://lighthouse.localhost:8080`. The CLI can also add a `lighthouse` entry to the local hosts file so you can use `http://lighthouse:8080`. It uses Go templates, CSS, and native HTML forms—no React, HTMX, Node runtime, or browser JavaScript bundle. The overview shows record counts, seven-day activity, security status, and the current SQLite footprint: database file plus active `-wal` file. Transient shared-memory and unrelated filesystem allocation are excluded.

The Results and Memory screens support individual CRUD, manually selected batches, and all records matching the active filters. Bulk result edits can set project, kind, or agent; bulk memory edits can set project or kind. The Results screen links to batch import. Bulk deletes require confirmation; bulk writes are audited per record and capped at 10,000 records. The tables show the newest 50 results and 100 memory items; “select every matching” applies the filters to the full set up to the cap.

Dashboard mutations require a same-origin form and CSRF token, and changes are audited. The CSRF key is stored with user-only permissions so restarting the dashboard during an update does not invalidate forms in open tabs. If a form is rejected, refresh that page and submit again. Agent CLI operations work while the dashboard is stopped.

### Startup service and local hostname

Run `lighthouse` to open the interactive control menu, or use the commands directly:

```sh
lighthouse service install
lighthouse service status
lighthouse service start
lighthouse service stop
lighthouse service restart
lighthouse service force-stop
lighthouse service remove
```

Install asks for confirmation, creates a per-user service that starts at sign-in, and starts it immediately. Remove asks for confirmation, stops Lighthouse, and removes the startup entry. The service uses the current database path and loopback-only dashboard. macOS uses a LaunchAgent in `~/Library/LaunchAgents`; Linux uses a systemd user unit in `~/.config/systemd/user`; Windows creates a Task Scheduler logon task for the current user. Linux installations need the user's systemd session available at sign-in. No administrator permission is needed for startup services.

`force-stop` confirms before terminating the managed dashboard. macOS sends `SIGKILL`; systemd escalates a timed-out stop to `SIGKILL`; Windows ends the scheduled task instance. The service can be started again with `lighthouse service start`.

Register the short local URL with a separate confirmation:

```sh
lighthouse host register
lighthouse host status
lighthouse host remove
```

Registration adds only a marked Lighthouse block to the OS hosts file. macOS and Linux ask for the administrator password through `sudo`; Windows shows its standard elevation prompt. Removal deletes only that marked block. You can always use `http://127.0.0.1:8080` or `http://lighthouse.localhost:8080` without registering a hosts entry.

`lighthouse update` installs the latest published release. When a startup service is installed, the CLI stops it before replacing the binary and starts it again afterward. If installation fails, it attempts to restart the previous version. Updating while a dashboard is running manually leaves that foreground process in place; stop it with Ctrl+C and start it again to load the new binary.

The database defaults to the OS user configuration directory. Set `LIGHTHOUSE_DB` or pass `--db /path/to/file.db` to choose another file. Avoid placing a live database in a synced vault or active Git checkout.

## Import, export, and storage

The dashboard and CLI support JSON, Markdown, YAML, TOML, PDF, and SQLite `.db` transfer. Portable data imports merge records and skip duplicate IDs or memory keys. A `.db` export is a consistent SQLite snapshot; importing a `.db` merges artifacts and active memory rather than replacing the live database. PDF export is a readable report; PDF imports preserve bounded raw source bytes without extracting text.

Lighthouse uses SQLite through the CGo-free `modernc.org/sqlite` driver, with FTS5 and an optional bundled vector extension. Keyword search is available locally. Semantic search requires embeddings to be supplied; Lighthouse does not silently send data to an embedding provider.

## Security boundaries and known limits

- The dashboard binds to loopback and validates allowed hostnames. Mutations use CSRF protection and audit events.
- Starlark has bounded input, output, execution steps, and wall time, and exposes typed operations only. It does not have a hard memory limit; use trusted local scripts.
- Local storage has no built-in encryption or multi-user authentication. Protect the user account and database file accordingly.
- Semantic search requires embeddings to be supplied by the caller; Lighthouse does not generate or upload them automatically.

## Documentation

The full documentation (installation, quickstart, agent setup, the code API, the dashboard, import and export, architecture, security, and why Lighthouse uses a code call instead of MCP) lives on the Lighthouse website. Its source is in [paulwritescode/lighthouse-website](https://github.com/paulwritescode/lighthouse-website).

Release history is in [CHANGELOG.md](CHANGELOG.md). Runnable script samples are in [examples/](examples/).
