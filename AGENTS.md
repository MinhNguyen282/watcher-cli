# Watcher CLI Development Guide

## What this repository is

Watcher is a personal fork of [Charm Crush](https://github.com/charmbracelet/crush),
a terminal coding assistant written in Go. Upstream names are intentionally
retained throughout the code, config, and data directories:

- Go module: `github.com/charmbracelet/crush`
- CLI command and cobra root: `crush`
- Config files: `crushrc`, `.crushrc`, `crush.json`, `.crush.json`
- Per-project data directory: `.crush/`
- The README installs the built binary as `watcher`

When adding code, follow the existing `crush` naming. Only the installed
executable name and user-facing README distinguish the fork.

Build requirements: Go 1.27+ (`go.mod` says `go 1.27.0`) with
`CGO_ENABLED=0` and `GOEXPERIMENT=greenteagc`. The Taskfile and goreleaser set
those env vars; if you build outside them, set them yourself.

## Commands

| Command | What it does |
| --- | --- |
| `task build` | Builds `crush` (`crush.exe` on Windows) with version ldflags. |
| `go build .` / `go run .` | Same package without the Taskfile wrapper. Running with no args opens the TUI. |
| `task run` | Builds, then executes `./crush`; extra args go after `--`. |
| `task dev` | `go run .` with `CRUSH_PROFILE=true`; pprof is served on `localhost:6060`. |
| `task test` | `go test -race -failfast ./...`. |
| `go test ./internal/agent -run TestCoderAgent` | Single package/test. |
| `task check MODE=static\|unit\|integration\|e2e\|coverage\|all` | Single local/CI entry point via `scripts/check.sh`. `all` = `static` + `unit`. |
| `go test ./internal/ui/diffview -update` | Regenerates golden files through `charmbracelet/x/exp/golden`. |
| `task fmt` | `gofumpt -w .` on the whole repository. |
| `task fmt:html` | Prettier for the stats page assets in `internal/cmd/stats/`. |
| `task lint` | `scripts/check_log_capitalization.sh` plus golangci-lint. |
| `task lint:fix` | golangci-lint with `--fix` only; it does **not** run the log-capitalization check. |
| `task modernize` | Runs `golang.org/x/tools` `modernize -fix` over the repo. |
| `task schema` | Regenerates `schema.json` from the config structs. |
| `task sqlc` | Regenerates sqlc query code from `internal/db/sql/`. |
| `task hyper` | Regenerates `internal/agent/hyper/provider.json` via `go generate`. |
| `task test:record` | **Destructive:** deletes `internal/agent/testdata` and re-records all VCR cassettes. Needs provider credentials and network. |
| `task install` | `go install` with version ldflags after fetching tags. |
| `task deps` | Updates Fantasy and Catwalk with `GOPROXY=direct`. |

CI additionally requires `go mod tidy` to leave the tree clean and runs
`sh scripts/check.sh all` on Linux, macOS, and Windows. Run `go mod tidy` after
dependency changes. Lint in CI uses golangci-lint v2.13.1 with
`.golangci.yml`.

`internal/ui/diffview` has its own Taskfile with `test:update` (regenerate
goldens) and `test:print*` tasks (dump goldens to stdout for debugging).

For tests that guard permissions, filesystem access, hooks, or job lifecycle,
add a negative control that proves the invalid case is rejected. A happy-path
test alone is not evidence for a guard. See `docs/testing.md`.

## Architecture

### Startup and control flow

```
main.go
  -> internal/cmd (cobra)                 CLI commands and workspace setup
      -> config.Init                      ConfigStore + merged configuration
      -> db.Connect                       SQLite connection + goose migrations
      -> skills.DiscoverFromConfig        Skill snapshot
      -> app.New                          Services, LSP, MCP; coder coordinator when configured
      -> workspace.NewAppWorkspace        In-process Workspace
  -> internal/ui/model.New               TUI (Bubble Tea v2)

CRUSH_CLIENT_SERVER=1:
  -> ensureServer / connectToServer       Auto-spawn or reuse a detached server
  -> client.NewClient                     HTTP client over Unix socket/named pipe
  -> workspace.NewClientWorkspace         Remote Workspace implementation
```

The TUI and the agent-running paths (`run`, interactive) talk only to
`workspace.Workspace`, never to `app.App` directly. Two implementations must
stay in sync:

- `internal/workspace/app_workspace.go` calls `app.App` in process.
- `internal/workspace/client_workspace.go` calls `internal/client` and receives
  server events over SSE.

Maintenance commands such as `session`, `stats`, and `logs` may open the
configured data directory's database, config, or log files directly and do not
create a workspace.

In client/server mode the server owns the real workspace lifecycle
(`internal/backend`), channel routing, and MCP state. The client owns local UI
state and reconnects with `workspace.ConnectionEvent` on degraded links.

Internally, services publish to `pubsub` brokers; `app.Subscribe` forwards those
events to the Bubble Tea program as `tea.Msg`. `ClientWorkspace` translates SSE
events into the same message types, so UI components do not know which transport
they are on.

### Layers

| Package | Role |
| --- | --- |
| `internal/cmd` | Cobra commands: root TUI, `run`, `server`, `review`, `watch`, `models`, `session`, `stats`, `login`/`logout`, `projects`, `dirs`, `logs`, `schema`, `update-providers`. |
| `internal/app` | Wires services, LSP manager, skills, shadow git, MCP init, herdr, event subscriptions, and the coder coordinator when a model is configured. |
| `internal/backend` | Transport-agnostic workspace/session/permission operations with client refcounting and idle shutdown. |
| `internal/server` | HTTP/1.1 and h2c API over TCP, Unix socket, or Windows named pipe; endpoint table in `endpoints.go`; generated docs at `/v1/docs/`. |
| `internal/client` | HTTP SDK used by `ClientWorkspace`; also parses the SSE event stream. |
| `internal/proto` | Shared wire types for the client/server protocol. |
| `internal/workspace` | Frontend-facing `Workspace` interface, local and remote implementations, reconnect logic. |
| `internal/agent` | Session agents, coordinator, prompts, compaction, sub-agents, tool dispatch pipeline. |
| `internal/agent/tools` | Built-in tools plus MCP tool wrappers (`mcp_<server>_<tool>`). |
| `internal/harness` | Self-evolving harness (`--evolve`): probes, editor LLM, worktrees, store. |
| `internal/repomap` | Pure-Go repository symbol graph injected into the prompt and exposed as `repo_map`. |
| `internal/verify` | Detects project checks (Go, Node, Rust, Python, Make), selects affected targets, parses failures. |
| `internal/review` | Static diff checker plus best-effort LLM review for `crush review`. |
| `internal/watchdog` | Tails command output or log files, extracts source locations, drives `crush watch`. |
| `internal/shadowgit` | Hidden git checkpoints under `.watcher/shadow-git/` for rewinding agent edits. |
| `internal/memory` | Durable typed notes (`user`, `project`, `team`) used by the memory tools. |
| `internal/jevrouting` | Observe-only TypeSafe routing observer (`options.jev_observe`). |
| `internal/herdr` | Reports pane/agent state to the herdr terminal multiplexer when running inside it. |
| `internal/config` | `ConfigStore`, loading/merging, providers, model selection, runtime overrides. |
| `internal/shellconfig` | Bash-backed config builtins (`provider`, `model`, `mcp`, `lsp`, `hook`, `permissions`, `option`, `db`). |
| `internal/db` | sqlc-generated query code and embedded goose migrations. |
| `internal/session`, `internal/message`, `internal/history`, `internal/filetracker` | SQLite-backed domain services. |
| `internal/permission` | Permission modes, rules, approval decisions, decision provenance. |
| `internal/hooks` | Hook runner, event constants, stdin/stdout protocol, aggregation. |
| `internal/lsp` | Lazy LSP manager built on powernap, auto-discovery, diagnostics. |
| `internal/skills` | Agent Skills discovery, builtin embedding, manager, tracker. |
| `internal/commands` | Custom slash commands and MCP prompts. |
| `internal/shell` | Embedded POSIX shell (`mvdan.cc/sh`) used by the bash tool and hooks. |
| `internal/pubsub` | In-process broker used for UI/agent event fan-out. |
| `internal/event` | PostHog telemetry (opt-out via config/`CRUSH_DISABLE_METRICS`). |

Core dependencies: `charm.land/fantasy` (provider abstraction, agent loop, tool
types), `charm.land/bubbletea/v2` and `charm.land/lipgloss/v2` (TUI and styles),
`charm.land/glamour/v2` (markdown), `github.com/charmbracelet/x/powernap`
(LSP), `mvdan.cc/sh/v3` (embedded shell), and `sqlc` + `goose` (SQLite queries
and migrations).

### Agent and tool flow

`agent.Coordinator` owns named agents (`coder`, `task`, `plan`) and builds one
`SessionAgent` per named agent. The coder uses the `large` model slot, the task
agent uses `fast`, and plan uses `large`. The `fast` slot falls back to `small`
when unset. Per-session run state (queues, reminders, dispatch locks) lives
inside the shared `SessionAgent`.

System prompts are Go templates under `internal/agent/templates/`. The coder
template has a static prefix plus a dynamic tail (`coder_dynamic.md.tpl`) for
environment, skills, and context files, so provider prompt caching sees a
stable prefix. The harness section and repository-map summary are appended
after the template is built. Context files are discovered from
`options.context_paths` and the default names (`AGENTS.md`, `CRUSH.md`,
`CLAUDE.md`, `GEMINI.md`, `.cursorrules`, `.cursor/rules/`,
`.github/copilot-instructions.md`, and case variants).

Every model tool call goes through `internal/agent/tool_pipeline.go`, in this
order:

1. Run `PreToolUse` hooks and apply input rewrites.
2. Record the final call intent.
3. Evaluate deny-only guards (a denial is final).
4. Inject the approval stage; only `allowed-once` runs the body.
5. Execute with a per-tool timeout and panic recovery.
6. Normalize every outcome (success, error, denial, timeout, abort, panic) into
   one `fantasy.ToolResponse`.
7. Record the result event.

Tool bodies must call `tools.RequestApproval` instead of the permission service
directly. They return `fantasy.ToolResponse`; raw Go errors are converted at the
pipeline boundary.

Default tool timeout is 10 minutes. `bash`, `job_output`, and `question` manage
their own lifetimes (zero timeout). `verify` gets 15 minutes.

Tool metadata is centralized in `internal/agent/tools/spec.go`
(`toolSpecs`, `SpecFor`). It declares read-only vs mutating intent, the input
field used for permission rule content, result ceilings, deferrable tools, and
whether a tool may be backgrounded. Unknown tools (MCP, plugins) default to
mutating, no content rule, never backgrounded.

### Message persistence and events

`internal/message` writes are debounced (33 ms). Streaming deltas are coalesced
into one SQL write and one pubsub event. Terminal updates (finish, error, tool
call structure) flush synchronously. Callers that must read the latest state
after `Update` need `Flush`/`FlushAll`: tests, session switches, and shutdown
all do this.

Sessions and messages live in SQLite. Migrations are embedded from
`internal/db/migrations/*.sql` and applied by goose at startup. Queries live in
`internal/db/sql/*.sql`; generated files (`*.sql.go`, `db.go`, `models.go`,
`querier.go`) come from sqlc via `sqlc.yaml`. Do not hand-edit generated files.

## Documentation map

Read the relevant doc before changing that area:

| Document | Contents |
| --- | --- |
| `internal/ui/AGENTS.md` | **Required reading for any TUI change.** Rendering pipeline, dialogs, focus, caching, styling. |
| `docs/config/README.md` | Full `crushrc` reference, precedence, legacy JSON. |
| `docs/hooks/README.md` | Hook config, stdin payload, output envelope, exit codes, compatibility. |
| `docs/testing.md` | Check modes, negative controls, quarantined environmental failures. |
| `docs/defensive-patterns.md` | Invariants for outcomes, guards, ownership, teardown. |
| `docs/adr/0001-tool-dispatch-pipeline.md` | Why all tool calls go through one pipeline. |
| `docs/harness.md` | `crush run --evolve` behavior and `.watcher/` layout. |
| `docs/jev-routing.md` | Observe-only Jev routing and its limits. |
| `docs/database-inspector.md` | `db_schema`, `db_query`, `openapi_spec` limits. |
| `docs/plans/README.md` | Roadmap proposals. Status checkboxes are not release guarantees and can be stale. |

## Configuration

`crushrc` is a Bash script executed with Crush-specific builtins. It is loaded
once at startup; the builtins only collect config while a `ConfigBuilder` is on
the context. The same builtins are no-ops during normal bash tool execution.

Discovery rules:

- Per-project files are searched from the working directory upward, stopping at
  the git worktree root.
- Within a directory the priority is `.crushrc` > `crushrc` > `.crush.json` >
  `crush.json`. Everything found is merged, with project settings overriding
  global ones.
- Global config lives at `~/.config/crush/crushrc` (`%USERPROFILE%\.config\crush\crushrc`
  on Windows). `CRUSH_GLOBAL_CONFIG` and `CRUSH_GLOBAL_DATA` override the
  config/data roots and are the standard way to isolate tests.
- Machine-owned JSON under the data directory is loaded last and has the highest
  priority. The data directory is never scanned for a `crushrc`.
- `crush dirs` prints the effective config and data paths.
- The binary autoloads `.env` via godotenv at process start (blank import in
  `main.go`), before `--cwd` can change directory, so provider keys come from
  the directory where the process was launched.

Runtime-only overrides (`--yolo`, `--permission-mode`, `--channels`) live in
`ConfigStore.Overrides()` and are never persisted.

The config pointer returned by `ConfigStore.Config()` is treated as immutable
after publication. Typed mutators clone, mutate, and swap a new `Config`. Do not
mutate a returned `*config.Config` in place.

Model slots are configured with `model large`, `model small`, and
`model fast`. Crush also supports custom providers with `provider add`,
including local Ollama/LM Studio/Llama.cpp/OMLX discovery.

To add an option: add the field to `Options` in `internal/config/config.go`,
add the crushrc key to `optionSpecs` in `internal/shellconfig/options.go` (or a
special case in `handleOption` for nested `option ui ...` values), and run
`task schema` to update `schema.json`. Crushrc keys are kebab-case and map to
snake_case JSON keys; some booleans are stored inverted (for example `metrics`
maps to `disable_metrics`).

## Local state

| Path | Contents |
| --- | --- |
| `<data dir>/` (default `.crush/`) | SQLite database, workspace config, logs, memory, custom commands. Override with `options.data_directory` or `--data-dir`. |
| `<data dir>/logs/crush.log` | Application log. `crush logs` reads it. |
| `<data dir>/memory/memory.json` | Durable agent memories. |
| `<data dir>/commands/` | Project custom slash commands. |
| `.watcher/harness_state.json` | Saved harness; its rendered section is appended to the coder and plan system prompts. |
| `.watcher/memory/lessons.md` | Lessons used by `crush review` and the harness. |
| `.watcher/traces/` | Probe traces from `--evolve`. |
| `.watcher/repomap/graph.json` | Repository map cache. |
| `.watcher/shadow-git/` | Hidden checkpoint repository. |
| `~/.config/crush/commands/`, `~/.crush/commands/` | User custom slash commands. |

`shadowgit` appends `.watcher/` to the project `.gitignore` on init so
checkpoints never appear in `git status`.

## Adding a tool

A tool is visible only if it is registered in every relevant place:

1. Add `internal/agent/tools/<name>.go` with a `New<Name>Tool` constructor, a
   params struct with `json` and `description` tags, and an embedded
   `<name>.md` description. Use `<name>.md.tpl` when the description needs
   runtime data (for example `gh` availability or result limits) and render it
   with `renderTemplate`.
2. Register it in `coordinator.buildTools` in `internal/agent/coordinator.go`.
   Tools not in the agent's `AllowedTools` are filtered out at the end.
3. Add the name to `allToolNames()` in `internal/config/config.go`. Update
   `resolveReadOnlyTools` / `resolvePlanTools` if the default `task` or `plan`
   agents should have it.
4. Add a `Spec` to `toolSpecs` in `internal/agent/tools/spec.go`. Otherwise the
   tool is treated as mutating with no permission-rule content field and cannot
   be backgrounded.
5. Add a renderer case in `NewToolMessageItem` in `internal/ui/chat/tools.go`,
   or accept the generic renderer.
6. Add tests, including a negative control for the permission or guard path
   when the tool touches files, processes, or the network.

MCP tool names are always `mcp_<server>_<tool>`; they are wrapped by
`internal/agent/tools/mcp-tools.go` and rendered by the MCP renderer.

## Permissions and approvals

Modes are parsed by `internal/permission`:

`default`, `plan` (read-only), `acceptEdits`, `auto` (read-only tools),
`dontAsk` (deny anything that would prompt), `bypassPermissions`.

Rules are tool-scoped: `allow`, `deny`, and `ask` accept `tool` or
`tool(content)` form, for example `bash(git status:*)`. `deny` wins over
`allow`; `ask` forces a prompt even when a broader allow matches.
`disable_bypass_permissions` removes bypass mode from the deployment.

Approval is fail-closed. Only `ApprovalAllowedOnce` permits execution. Missing
approver, cancelled answerer, unknown policy, and errors all deny. A denial is
monotonic: no later guard, hook, or listener can convert it into an allow.
Restricted sub-agents cannot use `question`, plan-mode tools, or `send_message`.

## Hooks

Hook events are defined in `internal/hooks/hooks.go`. That list is authoritative;
the "currently just one event" wording near the top of `docs/hooks/README.md` is
outdated. Events split into gating events (allow/deny/rewrite) and
observational events (context only).

Key behavior:

- Hooks run before permission checks. `decision: "allow"` is affirmative
  pre-approval and skips the prompt; silence falls through.
- Hooks run in parallel but compose in config order. First deny wins;
  `updated_input` patches shallow-merge in order with later patches winning.
- Exit code `2` blocks the call (stderr is the reason). Exit `49` halts the
  turn. Other non-zero codes are non-blocking warnings.
- Sub-agents skip hook interception; the outer `agent` tool call is still
  hooked.
- Hook commands run through the embedded POSIX shell, so inline shell and
  shebang-less scripts work on Windows. Shebang scripts dispatch to the named
  interpreter; `.ps1` must be invoked explicitly.
- Default timeout is 30 seconds. A timed-out in-process hook is abandoned and
  treated as "no opinion"; long-running work should honor context cancellation
  or run in a subprocess.

Adding a hook event requires three edits: the constant and `Events` entry in
`internal/hooks/hooks.go`, the duplicated `hookEventNames` list in
`internal/config/load.go` (the duplication avoids a `config` to `hooks` import
cycle), and an emission site in the agent lifecycle (for example
`internal/agent/prompt_hooks.go`, `lifecycle_hooks.go`, or
`tool_pipeline.go`).

## Skills and shell builtins

Skills follow the Agent Skills standard (`SKILL.md` with YAML frontmatter:
`name`, `description`, optional `user-invocable`, `disable-model-invocation`,
`license`, `compatibility`, `metadata`). Discovery scans `options.skills_paths`
plus global locations (`$CRUSH_SKILLS_DIR` when set, otherwise
`~/.config/crush/skills`, `~/.config/agents/skills`, `~/.agents/skills`,
`~/.claude/skills`, plus AppData equivalents on Windows) and project locations
(`.agents/skills`, `.crush/skills`, `.claude/skills`, `.cursor/skills`) under
both the working directory and the git worktree root. `options.disabled_skills`
hides skills from the agent. Builtin skills are embedded from
`internal/skills/builtin/*/SKILL.md` via `//go:embed builtin/*` and are
addressed as `crush://skills/<name>/SKILL.md`; the View tool resolves that
prefix from the embedded filesystem. User skills override builtins with the
same name.

To add a builtin skill: create the directory and `SKILL.md` (directory name
must match `name`), add an assertion to `TestDiscoverBuiltin` in
`internal/skills/skills_test.go`, then `go build . && go test ./internal/skills/...`.
No explicit wiring is needed.

Shell builtins live in the registry in `internal/shell/builtins_registry.go`
(`jq` is registered there in `init`; `shellconfig` registers the config
builtins via `RegisterBuiltin` to avoid an import cycle). The `builtinHandler`
middleware in `internal/shell/run.go` dispatches them before the block and exec
handlers. Handlers receive the handler context's stdin/stdout/stderr so pipes
and redirections work. Every unbounded loop must poll `ctx.Err()` and return it
verbatim. Add a new builtin by writing a `BuiltinHandler` function and
registering it during `init`. The same handler chain serves the bash tool and
the hook runner.

## Testing conventions

- Use testify `require`, `t.Parallel()`, and `t.TempDir()`. Temp dirs do not
  need manual cleanup.
- Isolate configuration and data: set `CRUSH_GLOBAL_CONFIG` and
  `CRUSH_GLOBAL_DATA` to temp dirs (and `XDG_DATA_HOME` when the code path
  consults it) before `config.Init`.
- Agent conversation tests replay HTTP cassettes with `charm.land/x/vcr` from
  `internal/agent/testdata/<TestName>/`. Missing cassettes are recorded once,
  which requires credentials from `internal/agent/.env.sample` (for example
  `CRUSH_HYPER_API_KEY`) and network access. `task test:record` deletes all
  cassettes first; do not run it casually.
- `internal/agent/agenttest` builds a real coordinator without a full
  `app.App` for backend/agent integration tests.
- `TestCoderAgent` skips on Windows. Shell tests that need POSIX utilities
  (`sh`, `sleep`, `cat`) are environmental failures on Windows, not product
  failures; report them as quarantined and keep unit-scoped checks green.
- Golden-file tests use `charmbracelet/x/exp/golden`; regenerate with `-update`
  on the narrow package, for example
  `go test ./internal/ui/diffview -update`.
- `config.UseMockProviders` and `config.ResetProviders` no longer exist in this
  tree. Do not copy that pattern from old docs.
- Put regression tests at the narrowest scope that fails, then add one negative
  control, then run the broader suite.

## Code style

- Format with gofumpt. `task fmt` does this repository-wide; a narrower
  `gofumpt -w <path>` is fine while iterating.
- Imports are grouped stdlib, external, internal (goimports style).
- Log messages start with a capital letter. `task lint` enforces this with
  `scripts/check_log_capitalization.sh`; `task lint:fix` does not.
- Comments on their own lines start with a capital letter, end with a period,
  and wrap at 78 columns.
- JSON field tags are snake_case. File permissions use octal notation
  (`0o755`, `0o644`).
- Pass `context.Context` as the first parameter. Use typed errors and
  `errors.Is`/`errors.As`; never route decisions by parsing an error string.
- Prefer small interfaces defined in the consuming package, struct embedding
  for composition, and typed constants for enums.
- Keep `ConfigStore.Config()` snapshots immutable.
- For the stats page and OAuth callback page (`internal/oauth/callback/page.*`),
  format HTML/CSS/JS with prettier. Keep Go template actions on one line inside
  HTML tags; prettier reflows multi-line actions in a way the template parser
  rejects.

## Failure handling invariants

These come from `docs/defensive-patterns.md` and are enforced in tests:

- Treat `exit_code`, `signal`, `timed_out`, `aborted`, and `kill_reason` as
  independent facts. `exit 0` is not proof that work completed; a timeout is
  not an exit code; a killed process is not a user cancellation.
- Do not use modification times as change detection. Compare a content version
  (size plus hash).
- Guards may deny or abstain, never allow. Once a guard denies a call, no later
  guard, hook, or listener can turn it into an allow.
- Name the owner of every resource. Background jobs belong to the session that
  started them and cannot be read or killed by another session.
- Teardown cancels, then waits with a bounded timeout. Unfinished work stays
  tracked so the caller can inspect or retry.
- Every tool output has a byte budget. Oversized output is bounded explicitly:
  shell/job output may spill to a file, and model-facing results are cut with a
  visible truncation marker. Silent truncation is a bug.

## Styling system

`internal/ui/styles/` has three layers:

- `quickstyle.go` builds `Styles` from `quickStyleOpts` design tokens. It must
  stay token-driven; do not hardcode charmtone colors here except Chroma syntax
  highlighting.
- `themes.go` defines concrete themes by calling `quickStyle` with palettes and
  applying narrow overrides.
- `styles.go` defines the `Styles` struct.

Add a named built-in theme by adding an options function and registering it in
`builtinThemes` (and `builtinThemeOverrides` if it needs narrow overrides) in
`themes.go`; `LoadTheme` resolves built-in and user theme files, while
`ThemeForProvider` selects the default theme for a provider (Hyper vs the
default Charmtone Pantera).

## UI work

Read `internal/ui/AGENTS.md` before touching the TUI. The invariants that cause
the most damage when missed:

- `UI` is the only Bubble Tea model. Sub-components are imperative renderers;
  they do not have standard `Update` loops.
- Never do IO or expensive work in `Update`; return a `tea.Cmd`. Never mutate
  model state inside a `Cmd`; do it in `Update`.
- Use `github.com/charmbracelet/x/ansi` for width/cutting/stripping, never byte
  slicing.
- Dialog messages are intercepted before other routing. In lipgloss v2,
  `Width(n)` includes border and padding; size content to the content area and
  use padding, not margin.
- The list renders only visible items. `TotalHeight` renders everything and is
  only for scrollbar geometry; use `Overflows` for "does it fit?" checks.

## Gotchas

1. **Client/server is opt-in.** `CRUSH_CLIENT_SERVER=1` switches the frontend
   to `ClientWorkspace`. Without it, everything runs in process. Tests exist in
   `internal/server`, `internal/workspace`, and `internal/cmd/clientserverrace`.
2. **Server staleness is detected by version and BuildID.** `crush` auto-spawns
   a server and restarts it when the client BuildID differs. Development
   BuildIDs derive from the executable's modification time, so every rebuild
   invalidates a running development server. Server logs live under the cache
   directory in `server-<host>/crush.log`. The server lingers 60s after its last
   workspace and gives a dropped client 10s to reconnect; tune with
   `CRUSH_SERVER_IDLE_TIMEOUT`, `CRUSH_SERVER_DETACH_GRACE`, and
   `CRUSH_SERVER_READY_TIMEOUT`.
3. **Adding a workspace operation touches several layers.** Update the
   `workspace.Workspace` interface, `AppWorkspace`, `proto` types,
   `server/endpoints.go` (the endpoint table drives registration and docs),
   `client`, and `ClientWorkspace`. Keep both implementations in sync.
4. **Server endpoint docs are generated from `endpoints.go`.** Add or change an
   endpoint there, never by hand-wiring a mux path elsewhere.
5. **Message reads can race the write debounce.** Call `Flush`/`FlushAll` before
   reading state you just wrote.
6. **`crushrc` builtins are no-ops during bash tool execution.** They only
   collect config while the config loader has installed a `ConfigBuilder`.
7. **Unknown tools are treated as mutating.** A tool missing from `toolSpecs`
   gets the conservative default, which affects plan mode, permission rules,
   and result ceilings.
8. **Interactive-only tools.** `question` and enter/exit plan mode are not
   available to sub-agents. Sub-agents also skip hooks.
9. **Tool output ceilings are real.** `view`, `bash`, and `verify` return at
   most 200k chars; default is 100k. Oversized output is cut with a visible
   marker. Do not silently truncate.
10. **The repo map is best-effort.** It builds lazily, caches under
    `.watcher/repomap/`, and never fails startup. Go uses `go/parser` (high
    confidence); other languages use a low-confidence fallback scanner.
11. **`--evolve` needs a git repository with at least one commit.** It creates
    worktrees, spawns the current executable as probe subprocesses with isolated
    data directories, and writes `.watcher/` state. The winner is applied to the
    working tree unless `--no-apply` is set.
12. **Shadow checkpoints are best-effort.** A non-git working directory yields a
    disabled service, not an error. `.watcher/` is added to `.gitignore` on init.
13. **Provider catalog refresh hits the network.** A failed refresh falls back to
    the cached or embedded catalog. `CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1` and
    `crush update-providers embedded` are available for offline or pinned work.
14. **No git repository changes defaults.** Crush limits `ls` and completion
    depth and item counts when it cannot detect a worktree.
15. **`docs/plans` is a roadmap.** Several checkboxes and "implemented" notes do
    not match the tree. Verify against code, not plan status.
16. **The hooks doc's event list is stale.** `internal/hooks/hooks.go` defines
    the events this build actually runs.
17. **Old docs mention a mock-provider pattern that is gone.** Use VCR cassettes,
    `agenttest`, and isolated config directories instead.
18. **CI builds and lints on Linux, macOS, and Windows.** Keep platform-specific
    code behind build tags (`*_windows.go`, `*_other.go`, `*_unix.go`) as the
    existing files do, and avoid importing POSIX-only packages unconditionally.

## Committing

- Use semantic commits: `fix:`, `feat:`, `chore:`, `refactor:`, `docs:`,
  `sec:`, and similar.
- Keep the subject to one line when possible; add a body only when the reasoning
  is not obvious.
- Do not commit generated noise or local state (`.crush/`, `.watcher/` unless
  intentionally shared).
