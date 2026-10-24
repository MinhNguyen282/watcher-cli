# Plan 09 — Port Claude Code's Harness Design into Watcher CLI

> Status: **Implemented (partial — see §9)** · Priority: **High** · Source revision: Claude Code
> `2.1.302` (2026-10-16, git `2ef144e6f98c1c99d082b735e4b38f50e3391e55`) ·
> Scope: system-prompt architecture, tool contract, permission model,
> compaction, subagents, hooks, memory, plan mode.

## 1. Executive summary

Claude Code's advantage is not its model calls. It is the **harness**: the
scaffolding around the model that decides what the model sees, what it may
do, what survives a context reset, and what happens when a tool is denied,
slow, or delegated.

Watcher CLI already has the mechanical pieces (`fantasy` loop, tool
registry, single dispatch pipeline, hooks runner, shadow-git checkpoints,
LSP tools, repo map, `verify`, compaction, Bubble Tea UI). What it lacks is
Claude Code's **explicitness**: prompts assembled as typed, cache-scoped
blocks; tool definitions that carry their own permission and search
metadata; a permission model with modes, rules, and provenance; compaction
that preserves a resumable working set; and subagents/hooks as first-class
lifecycle participants.

The eight highest-leverage ports, in dependency order:

1. **Tool spec** — every tool declares rule content, read-only class, search
   hint, deferral, result ceiling, backgrounding policy, and a safe
   classifier projection (today: only `Info()` plus ad-hoc permission calls).
2. **Prompt block model** — named blocks with a static/dynamic boundary and
   cache scopes, plus typed environment updates (today: one monolithic
   `coder.md.tpl`).
3. **Permission model v2** — six modes, tool-scoped allow/deny/ask rules,
   decision provenance, read-only allowlist, sandbox auto-allow.
4. **Compaction v2** — precompute, microcompact, preserved tail, re-read of
   recently viewed files, carry-over of plan/skills/jobs.
5. **Subagents** — typed agents, a `fork` variant that inherits context, and
   a message/handback protocol.
6. **Hook surface** — expand from `PreToolUse` to the lifecycle events that
   actually gate behavior.
7. **Deferred tools / tool search** — keep large MCP schema sets out of the
   context window, with accounting.
8. **Plan mode + memory v2** — plan as an enforced read-only mode, and typed
   memory instead of free-text memory advice.

Do **not** port: telemetry, hosted classifier dependency, cloud/team
features, plugin marketplace, or Claude Code's prompt verbosity about edit
mechanics (see §7).

## 2. Evidence and method

Observed artifact: `C:\Users\nhhmi\.local\bin\claude.exe` (290,211,488
bytes, Windows x64) and its version copy
`~/.local/share/claude/versions/2.1.302`. It is a Bun single-file
executable; the JavaScript application payload is plaintext and begins at
file offset **239,350,106** with `// @bun @bytecode`, running to ~286.0 MB
(≈46.6 MB), followed by the module table and the `---- Bun! ----` trailer.

Method:

1. Carved the payload region, then split it on the per-module header
   `// @bun @bytecode` into **2,428** module bodies (0.6 KB – 5.0 MB each).
   395/400 sampled modules pass `node --check` as ESM; the remainder carry
   trailing serialization metadata.
2. Ran REA `analyze_javascript_application` (static only, never executed).
   Evidence `ev_379b423ba709e231db0f888f4c6adf96824117d56c07b1b6cfeede045afb209b`
   reports 2,428 files, 46,645,074 text bytes, 1,275 parsed JavaScript
   modules, 20 parse failures, 5,469,130 AST nodes, and partial
   application/semantic coverage (call-flow 8,567, data-flow 63,429,
   closure 7,210, object-flow 7,174 retained relations). Coverage is
   explicitly partial; absences below are absences *in the evidence*, not
   proof of absence in the product.
3. Read the prompt and definitions directly out of the carved text.
   Quoted strings below are verbatim from the bundle.

Reproduce (PowerShell/py3, no REA needed):

```python
start, end = 239350106, 286000000
open("bundle.js", "wb").write(open(CLAUDE_EXE, "rb").read()[start:end])
```

Build metadata embedded in the binary (from the `# Doing tasks` block):
`VERSION:"2.1.302"`, `BUILD_TIME:"2026-10-16T15:52:41Z"`,
`GIT_SHA:"2ef144e6f98c1c99d082b735e4b38f50e3391e55"`, `DD_SOURCEMAP_GROUP:"win32"`.

Limitations: the bundle is minified, so control flow is read from prompt
strings, tool object literals, schemas, and constant tables rather than
from demangled source. Counts above are static-analysis counts.

## 3. What Claude Code actually does

### 3.1 System prompt = ordered blocks with a dynamic boundary

The prompt is assembled as a list of strings, joined with blank lines. A
sentinel constant `__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__` marks the point
after which blocks are per-turn. Assembly sorts blocks into
`cacheScope: null | "org" | "global"`, emits an attribution header first,
and logs `tengu_sysprompt_block` with block length and SHA-256 for every
block. Sections observed (verbatim headings):

`# System`, `# Doing tasks`, `# Executing actions with care`,
`# Tone and style`, `# Harness`, `# Environment`, `# Environment update`,
`# Session-specific guidance`, `# Using your tools`,
`# General tips on output and calling tools`, `# Context management`,
`# Focus mode`, `# Memory`, `# Output Style`, `# Language`,
`# MCP Server Instructions`, `# Unavailable MCP Tools`,
`# Messages from the system`, `# Delivering work`, `# Reporting outcomes`,
`# Corrections`, `# Git`, `# Committing changes with git`,
`# Writing commit messages and PR descriptions`, `# Team Coordination`,
`# Background forks`, `# Agent Teammate Communication`, plus style blocks
(`Proactive`, `Concise`, `Explanatory`, `Learning`) selected per user
output style.

Design points worth copying:

- **The harness describes itself.** `# Harness` tells the model that
  rendered text is terminal markdown, that tools run behind a permission
  mode, that a denial means "adjust, don't retry verbatim", that hooks may
  intercept calls, and that independent calls may run in parallel.
- **Environment is data, not prose.** A structured record (working
  directory, is-worktree, is-git-repo, additional directories, platform,
  shell, OS version, scratchpad directory, `gh` availability, agent proxy)
  is rendered into `# Environment`; changes mid-session emit
  `# Environment update` with explicit "this replaces what this session was
  told earlier" semantics.
- **Context management is a promise.** `# Context management` tells the
  model compaction is handled for it and that it should never wrap up early
  because it "feels" long.
- **Focus mode** tells the model the user only sees the final message, so
  intermediate narration is noise.

### 3.2 Tools are a contract, not a function

Every tool is constructed by one helper (`Qt({...})` in the bundle) with a
consistent field set. Observed fields:

| Field | Purpose |
|---|---|
| `name`, `userFacingName`, `getActivityDescription` | identity + UI verbs |
| `ruleContentField` | which input field is the *content* of a permission rule (`Edit` → `file_path`) |
| `searchHint` | one-line capability phrase used by tool search |
| `shouldDefer` / `isDeferred` | keep the schema out of the window until searched |
| `backgrounding` | `"never"` for `Edit`; per-tool background policy |
| `maxResultSizeChars` | hard result ceiling enforced by dispatch |
| `strict` | argument-shape strictness |
| `outputSchema`, `outputSchemaAcrossProcesses` | typed results across subprocess boundaries |
| `coerceInput` | legacy-argument repair (`new_str` → `new_string`) |
| `stripForStorage` | drop large fields (`originalFile`) before persisting |
| `toAutoClassifierInput` | a minimal, safe projection for the permission classifier (`{file_path, adds, removes}`) |
| `async prompt({model, leanPrompt})` | per-model prompt variants |
| `isReadOnly` | parallelism + permission bypass classification |

Prompts are **model-aware**: `bO({model, leanPrompt})` selects a compact
variant for smaller/cheaper models, and a `leanRewrite` variant for models
that need different instruction shapes. There is also a
`functionHooks`/plugin tool API (`$.tool.register`, `$.tool.list`,
`$.tool.call`) so extensions register the same contract.

### 3.3 Permissions: modes, rules, provenance

Modes (`tL`): `["acceptEdits","auto","bypassPermissions","default","dontAsk","plan"]`,
with a numeric rank table (`Pd`; `auto: 3`, `bypassPermissions: 4`) used to
refuse escalation out of a lower mode, and a display table
(`Manual`, `Plan`, `Accept edits`, `Bypass Permissions`, `Don't Ask`,
`Auto`). `permissions.disableBypassPermissionsMode: "disable"` makes bypass
unavailable regardless of flags.

Rules are `{toolName, ruleContent}` with `behavior: allow|deny|ask` and a
`destination` (`localSettings`, project/user/session scopes). Content
validation is per tool: path tools (`Read`, `Write`, `Edit`, `Glob`,
`NotebookRead`, `NotebookEdit`, `Cd`) take path patterns; `Bash` takes a
command prefix or wildcard; `WebFetch` requires `domain:host`, never a URL;
`WebSearch` refuses `*`/`?`.

A decision carries a **provenance chain** of which stage decided:
`rule`, `mode`, `subcommandResults`, `permissionPromptTool`, `hook`,
`asyncAgent`, `sandboxOverride`, `workingDir`, `safetyCheck`, `classifier`,
`other`. Read-only Bash commands are auto-allowed by an allowlist
("Read-only command is allowed"), and when sandboxing is on,
`autoAllowBashIfSandboxed` produces "Auto-allowed with sandbox".

`auto` mode routes risky calls to a classifier, with circuit breakers that
carry flags: `bypassImmune`, `classifierRouted`, `autoModeDeny`,
`hostPersonOnly`, `localProjectionOnly`. `dangerousRemoval` is
bypass-immune and auto-mode denies it; `claudeSettingsFile` is
host-person-only (the agent cannot edit its own settings). These are
*safety invariants expressed as data*, which is the part worth copying.

### 3.4 Compaction: keep the working set, not the transcript

Observed behavior:

- Triggers: threshold on prompt tokens, idle compaction, and an optional
  **precompute** ("Precompute the compaction summary in the background
  before it is needed"). Environment switches exist for
  `DISABLE_AUTO_COMPACT` and `DISABLE_COMPACT` (the latter also disables
  manual `/compact`).
- `microcompact` drops low-value content (tool output) ahead of a full
  compact; both kinds write explicit transcript markers
  (`compact_boundary`, `microcompact_boundary`) and store
  `compactMetadata.preservedSegment` for the retained tail.
- What the **next** window contains is stated to the model verbatim:

  > the system prompt, project instructions and memory files, unchanged ·
  > up to five of the files you read most recently, re-read from disk (long
  > ones cut short) · the plan, any skills you loaded, and the status of
  > background agents and shells · the path to the full transcript on disk

- The summary contract is quality-gated: preserve difficulties and
  resolutions, options tried and set aside, decisions and constraints **in
  the user's own words**, exactly where the work stands, what is pending,
  and exact names/numbers/links; condense the assistant's own reasoning.
- Project instructions may add compact-time instructions; a structured
  summary template (`<summary>` with sections for primary intent, key
  technical concepts, files/code sections, errors and fixes, problem
  solving, all user messages, pending tasks, current work, next step) is
  used as the default body.

`/context` accounts for `used | free | buffer | deferred` rows, per-section
system-prompt tokens, memory files, MCP tools, deferred built-in tools,
agents, slash commands, and skills.

### 3.5 Subagents: typed, forkable, addressable

- Tool `Task` (`ht = "Task"`) takes `subagent_type`; `Explore` and `Plan`
  are built-in types; `"fork"` is a special type whose description says it
  "inherits your full conversation context, runs in the background, and
  keeps its tool output out of your context".
- Subagents report back through a typed handback ("The subagent ended
  without delivering a report through SubagentHandback"), can be resumed by
  sending a message, and are subject to per-session limits ("this session
  has no room for another agent now, or has started as many as it may
  between two messages from the user").
- `SendMessage` addresses `to: <name> | <agentId> | "main"`; a subagent
  cannot message peers beyond its session, and a stopped agent cannot be
  messaged.
- A **background observer** variant pushes one-way advisory reports
  ("Your background observer (x) sent a report"), explicitly marked as not
  user consent.
- Prompt guidance distinguishes fork (inherit + background) from fresh
  agents, and warns against duplicating work a subagent is already doing.

### 3.6 Hooks: a lifecycle surface, not a single gate

Observed event list (`Xc`):

```
PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch,
Notification, UserPromptSubmit, UserPromptExpansion,
SessionStart, SessionEnd, Stop, StopFailure,
SubagentStart, SubagentStop, PreCompact, PostCompact,
PreModelSwitch, PostModelSwitch,
PermissionRequest, PermissionDenied, Setup,
TeammateIdle, TaskCreated, TaskCompleted,
Elicitation, ElicitationResult, ConfigChange,
WorktreeCreate, WorktreeRemove,
InstructionsLoaded, CwdChanged, FileChanged,
DirectoryAdded, MessageDisplay
```

Common output envelope: `continue`, `suppressOutput`, `stopReason`,
`decision: approve|block`, `systemMessage`, `reason`, plus a
`hookSpecificOutput` union per event (e.g. `Elicitation` may
accept/decline, `WorktreeCreate` returns `worktreePath`). Hooks may emit a
`terminalSequence`, restricted to notification/title OSC codes and BEL.

### 3.7 Memory and system reminders

- Memory is **typed** (`user` vs team-scoped) with a personal memory
  directory written via file tools, and shared stores with an index
  document maintained by a memory tool. The prompt forbids secrets in
  shared stores and defines what to save and when.
- System reminders are a distinct channel: "Sometimes you will get system
  reminders in the middle of a conversation… they show up wrapped in
  `<system-reminder>` tags: at the start of the conversation, inside later
  user messages, and inside tool results." The model is told they are
  application-originated, advisory, and may be ignored if irrelevant.

### 3.8 Plan mode

`EnterPlanMode` / `ExitPlanMode` are tools, not just UI state. Plan mode is
a permission mode with its own display, its own prompt guidance, and an
explicit exit path (the plan is presented for approval). The rank table
prevents silent escalation into a stronger mode.

## 4. Gap analysis against this repository

| Capability | Claude Code | Watcher CLI today | Anchor |
|---|---|---|---|
| Tool metadata | rich per-tool contract | `Info()` + ad-hoc `permission.Request` inside each tool | `internal/agent/tools/*.go`, `internal/agent/coordinator.go:905` |
| Tool dispatch | single path with result ceilings, classifier projection | single pipeline: hooks → guards → approval → timeout → normalize | `internal/agent/tool_pipeline.go` |
| Prompt | block list + dynamic boundary + cache scopes + env updates | one Go template, env inline, static sections | `internal/agent/templates/coder.md.tpl`, `internal/agent/prompt/prompt.go` |
| Prompt cache control | per-block scopes | last tool + last system + last 2 messages | `internal/agent/agent.go:783`, `:900-914` |
| Permission modes | 6 modes + ranked transitions | skip-requests (yolo) + allow-list | `internal/permission/permission.go:66,327` |
| Permission rules | tool+content grammar, destinations, provenance | per-request allow/deny, session approval | `internal/permission/permission.go:188` |
| Compaction | threshold + idle + precompute, microcompact, preserved tail, file re-read, plan/skills/jobs carry | synchronous summarize; summary message replaces history | `internal/agent/agent.go:1106,1220,1367`, `internal/agent/context_budget.go` |
| Subagents | typed, fork-inherit, background, addressable | one `agent` tool (task agent), parallel tool wrapper | `internal/agent/agent_tool.go`, `internal/agent/coordinator.go:1720` |
| Hooks | 33 events, typed per-event output | `PreToolUse` only | `internal/hooks/hooks.go`, `internal/hooks/runner.go` |
| Deferred tools | `shouldDefer` + search + accounting | none; every schema is always sent | `internal/agent/coordinator.go:905` |
| Plan mode | enforced permission mode + enter/exit tools | UI input mode + plan agent prompt, no read-only enforcement | `internal/ui/model/ui.go:4790`, `templates/plan.md.tpl` |
| Memory | typed stores + index | free-text memory instructions only | `templates/coder.md.tpl` (`<memory_instructions>`) |
| Checkpoints | file-history + rewind | shadow-git snapshots on mutating tools | `internal/shadowgit`, `internal/agent/checkpoint_tool.go` |

Watcher CLI is ahead on: LSP-first editing tools, repo map, `verify`,
database/OpenAPI inspectors, background jobs with bounded output, and a
single fail-closed dispatch pipeline. The port should extend those, not
replace them.

## 5. Work packages

### Phase 0 — Tool spec (foundation)

Introduce `tools.Spec` alongside every tool and make the dispatch pipeline
consume it.

```go
type Spec struct {
    Name            string
    RuleContentField string   // "file_path", "command", "domain"
    ReadOnly        bool
    SearchHint      string
    Deferrable      bool
    Backgrounding   Backgrounding // never | allowed | required
    MaxResultChars  int
    ClassifierInput func(json.RawMessage) json.RawMessage // minimal safe projection
    StripForStorage func(json.RawMessage) json.RawMessage
}
```

Files: `internal/agent/tools/spec.go` (new), each `internal/agent/tools/*.go`
gains a `Spec()`, `internal/agent/tool_pipeline.go` enforces
`MaxResultChars`, uses `ReadOnly` for parallel scheduling and permission
bypass, and stores `StripForStorage` output via `ToolEventRecorder`.

Acceptance: table test asserting every registered tool returns a non-empty
`RuleContentField` when it mutates, `ReadOnly` is set for `view`/`grep`/
`glob`/`ls`/LSP read tools, and `MaxResultChars` is enforced for `bash`,
`view`, `grep`, `job_output`.

### Phase 1 — Prompt block model

Replace the single template render with a block assembler:

- `internal/agent/prompt/blocks.go`: `type Block struct { Name, Text string; Scope CacheScope; Dynamic bool }`,
  `Assemble(blocks []Block) (static []Block, dynamic []Block)`.
- Sentinel `DynamicBoundary` inserted between static and dynamic so cache
  markers land at stable positions.
- New sections ported verbatim-in-spirit: `# Harness`, `# Context management`,
  `# Environment` (structured record), `# Environment update`.
- Move tool-mechanics prose out of `coder.md.tpl` into each tool's
  description (see §7). Target: cut `coder.md.tpl` from 464 lines to the
  behavioral sections only.
- Environment diffing: keep the rendered env record in session state; when
  working dir, git state, scratchpad, or shell changes, emit an
  `# Environment update` block with explicit replacement semantics.

Files: `internal/agent/prompt/*`, `internal/agent/templates/coder.md.tpl`,
`internal/agent/agent.go` (system prompt construction), cache-control
placement in `PrepareStep`.

Acceptance: golden test on assembled block order and scope tags; test that
changing `IsGitRepo` mid-session produces an update block and no duplicate
`# Environment`; token accounting test for per-block sizes.

### Phase 2 — Permission model v2

- `internal/permission/mode.go`: the six modes with an explicit rank for
  each (the bundle only shows `auto: 3`, `bypassPermissions: 4`) and a
  `ValidateTransition` used by config, CLI, and UI so a lower mode cannot
  be silently raised.
- `internal/permission/rule.go`: `Rule{Tool, Content, Behavior, Destination}`,
  per-tool content validation (path glob, bash prefix/wildcard, domain), and
  matching for `allow|deny|ask`.
- `internal/permission/provenance.go`: append the deciding stage to the
  request result (`rule`, `mode`, `hook`, `sandbox`, `safetyCheck`,
  `classifier`, `user`), surfaced in tool metadata and the UI.
- Read-only bash allowlist: a small, testable command grammar
  (`git status`, `git diff`, `ls`, `rg --files`, …) that auto-allows under
  `default` and `auto`.
- Settings destinations: `crushrc`/`crush.json` rules are loaded into the
  same matcher; approval prompts offer "this tool / this content / this
  session" (today only session-wide).
- Hard invariants as data: `crushSettingsFile` (tool may not edit its own
  config), `dangerousRemoval` (always ask, never bypass), `outsideReads`.

Files: `internal/permission/*`, `internal/config/load.go` (rules schema),
`internal/agent/tool_pipeline.go` (provenance into metadata),
`internal/ui/dialog/actions.go` (mode cycling + rule persistence).

Acceptance: table tests for precedence (deny > ask > allow; content rule >
tool rule); bypass refused when disabled; provenance recorded for every
denial; config round-trip for rules.

### Phase 3 — Compaction v2

- `internal/agent/compaction/compact.go`: two-tier compaction —
  `Microcompact` (drop/compress old tool results, keep last N turns intact)
  and `Compact` (summary), each writing a typed transcript marker
  (`message.IsCompactBoundary`, `IsMicrocompactBoundary`).
- Preserved tail: store the retained segment (default: last 2 turns plus any
  turn the user touched) and re-send it after the summary.
- Carry-over bundle: plan document, loaded skills, background job status,
  and a `transcript_path` pointer. Re-read up to five most recently viewed
  files from disk (cap by bytes, not lines) and re-inject them.
- Precompute: when remaining budget crosses a soft threshold, start the
  summary in a background call with the same system prompt prefix so the
  provider cache warms; use it if compaction triggers.
- Idle compaction: optional, only when a session is idle and over the soft
  threshold.
- Prompt: add `# Context management` (never wrap up early) and project-level
  compact instructions from context files (`## Compact Instructions`).
- Fix the current behavior that queues a synthetic continuation prompt
  (`agent.go:1231`) — replace with the preserved tail + summary pair.

Files: `internal/agent/agent.go` (StopWhen/Summarize), `internal/agent/compaction/*`
(new), `internal/message/*` (markers), `internal/db/migrations` (marker
columns if needed), `internal/ui/chat/*` (render boundaries).

Acceptance: fake-provider test that a compacted session sends
`system + summary + preserved tail + re-read files`; microcompact reduces
tokens without dropping user messages; markers round-trip through SQLite;
cancel during precompute leaves no half state.

### Phase 4 — Subagents

- `config.Agent` gains `Type`, `InheritContext bool`, `Background bool`,
  `MaxInstances int`, `HandbackSchema`.
- `agent` tool gains `subagent_type` (default `task`), plus built-ins
  `explore` (read-only tools only) and `plan` (read-only + plan document).
- `fork` variant: seed the child session with the parent's message history
  (minus tool outputs beyond the cap) and run it in the background; parent
  keeps working.
- Handback tool (`subagent_handback`) with a typed result; missing handback
  is an explicit error, not an empty success.
- `send_message` tool for parent↔child messaging, with per-session agent
  budget and refusal reasons (no room, stopped, cross-session).
- Ensure subagent tool sets are derived from `Spec.ReadOnly` so a
  read-only agent cannot mutate.

Files: `internal/agent/agent_tool.go`, `internal/agent/coordinator.go:1720`,
`internal/config/config.go` (agent schema), `internal/db` (agent records),
`internal/ui` (agent status rows).

Acceptance: read-only subagent cannot call `bash`/`edit` (guard test);
fork inherits context and stays out of parent context; handback required;
budget refusal returns a typed reason.

### Phase 5 — Hook surface

Add events with real gating value first:
`UserPromptSubmit`, `SessionStart`, `SessionEnd`, `Stop`,
`PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `PreCompact`,
`PostCompact`, `SubagentStart`, `SubagentStop`, `PermissionRequest`,
`PermissionDenied`, `FileChanged`, `CwdChanged`, `InstructionsLoaded`,
`TaskCreated`, `TaskCompleted`.

- `internal/hooks/hooks.go`: event constants + per-event
  `hookSpecificOutput` decoding (keep the existing exit-code protocol:
  2 blocks the call, 49 halts the turn).
- Emit events from `tool_pipeline.go` (pre/post/batch/failure),
  `agent.go` (stop/session/prompt), `compaction` (pre/post), and the
  subagent runner (start/stop).
- Hooks must be able to veto compaction (`PreCompact`) and prompt submission
  (`UserPromptSubmit`) and to add context to a tool result
  (`PostToolUse.additionalContext`).

Acceptance: per-event contract tests with a shell hook fixture; a
`PreCompact` veto leaves the session untouched; batch events fire once per
step, not once per call.

### Phase 6 — Deferred tools and accounting

- `Spec.Deferrable` + `searchHint` feed a `tool_search` tool; deferred
  schemas are withheld from the request until searched, then pinned for the
  session.
- Add a `/context`-equivalent panel: used/free/buffer/deferred rows,
  per-section prompt tokens, per-tool schema tokens, memory files, skills.
- Config switch (`options.tool_search`) with an env override for
  deterministic tests.

Files: `internal/agent/coordinator.go:905` (schema assembly),
`internal/agent/tools/search.go` (new), `internal/ui/dialog/commands.go`.

Acceptance: with 50 fake MCP tools, the first request sends only
non-deferred schemas; searching promotes exactly one; accounting rows sum to
the reported prompt size.

### Phase 7 — Plan mode end-to-end

- Add `enter_plan_mode` / `exit_plan_mode` tools; the plan artifact is a
  file under the data directory (as `plan.md.tpl` already assumes).
- While in plan mode: mutating tools are denied by the pipeline (not by
  prompt text), `bash` restricted to the read-only allowlist, and the UI
  shows a mode indicator.
- `exit_plan_mode` returns the plan for approval and switches mode only on
  approval; mode transitions go through `ValidateTransition`.

Files: `internal/agent/tools/plan_mode.go` (new),
`internal/agent/tool_pipeline.go`, `internal/permission/mode.go`,
`internal/ui/model/ui.go:4790` (replace ad-hoc plan state),
`internal/agent/templates/plan.md.tpl`.

Acceptance: plan mode denies `edit`/`write`/`bash` (mutating) with a
typed denial, allows reads/LSP; exit requires approval; mode persists per
session.

### Phase 8 — Memory v2 and system reminders

- Typed memory store: `memory_save` / `memory_search` / `memory_forget`
  with `type` (user | project | team), path-safe stores, index maintenance,
  and a prohibition on secrets in shared scopes.
- Replace prose reminders with a structured `SystemReminder` message part
  rendered as `<system-reminder>`: context note at turn start, file-changed
  notice after external edits, tool-denial notice, todo drift notice.
- Keep reminders advisory and marked application-originated.

Files: `internal/memory/*` (new or extend `internal/history`),
`internal/message/*` (reminder part), `internal/agent/agent.go` (injection
points), `templates/coder.md.tpl` (`# Memory`).

Acceptance: reminders render exactly once per turn and never as user text;
memory store rejects writes outside allowed roots; index stays consistent
under concurrent saves.

## 6. Sequencing and risk

Order: **0 → 1 → 2 → 4 → 3 → 5 → 7 → 6 → 8**. Phase 0 unblocks 2, 4, 6, 7;
Phase 1 unblocks 3 and 7; Phase 2 unblocks 7.

Risks and mitigations:

- **Prompt regressions.** Every prompt change is measured against the
  existing golden prompt tests plus a small behavior suite (edit success
  rate, tool-call validity, over-narration). Land prompt changes behind
  config flags first.
- **Two sources of truth for permissions.** Keep `permission.Service` as the
  only decision point; modes and rules are inputs, and the pipeline never
  re-decides.
- **Compaction data loss.** Markers must be additive and reversible; keep
  the full transcript and test resume from a boundary.
- **Subagent cost.** Per-session agent budget plus a hard cap on inherited
  context size.
- **Hook latency.** Hooks already run in parallel with a timeout; new
  events must reuse the same runner, not add per-call processes.

## 7. What not to port

- **Prompt verbosity about edit mechanics.** Claude Code repeats whitespace
  advice across `# Doing tasks`, tool descriptions, and `# Session-specific
  guidance`. Watcher CLI's `coder.md.tpl` already over-indexes here; the
  port should move these rules into `edit`/`multiedit` descriptions and
  delete the duplicates.
- **Hosted classifier.** `auto` mode can map to a local heuristic first
  (read-only allowlist, path scope, destructive-command list) with the same
  provenance labels.
- **Telemetry and attribution fingerprints.** Optional and off by default.
- **Cloud/team surface** (`teammate`, `worktree` fan-out, plugin
  marketplace, agent proxy). Keep the tool contract general enough to add
  them later.
- **Claude-specific branding strings.** Keep Watcher/Crush identity; port
  structure, not marketing copy.

## 8. Definition of done

1. Every tool has a `Spec`; the pipeline enforces it.
2. The system prompt is a typed block list with a dynamic boundary and
   per-block cache scopes; environment changes are updates, not rewrites.
3. Permission decisions are rule-driven, mode-ranked, and provenance-tagged.
4. A long session compacts without losing the plan, the working files, or
   the user's own words, and can resume from the boundary.
5. Subagents are typed, bounded, addressable, and cannot exceed their tool
   class.
6. Hooks can gate prompts, tools, compaction, and sessions.
7. Plan mode is enforced by code, not by prompt text.
8. `task fmt`, `task lint`, `go test ./...` clean; golden prompt and hook
   contract fixtures updated.

## 9. Implementation status

Landed in this repository:

### Phase 0 — tool spec (complete)

- `internal/agent/tools/spec.go`: `Spec` table for every registered tool with
  `RuleContentField`, `ReadOnly`/`Mutating`, `SearchHint`, `Deferrable`,
  `MaxResultChars`, `Backgrounding`; unknown tools (MCP, plugins) get a
  conservative default.
- `RuleContentFor` extracts rule content from raw call JSON.
- `TruncateResult` enforces the per-tool ceiling with a rune-safe marker.
- `internal/agent/tool_pipeline.go` enforces truncation in `finalize` and
  records `result_truncated` / `result_limit_chars` in tool metadata.
- Tests: `internal/agent/tools/spec_test.go`.

### Phase 1 — prompt blocks (complete for the coder prompt)

- `internal/agent/prompt/blocks.go`: `Block`, `Set`, `Assemble`,
  `Static()`/`Dynamic()`, `BoundaryOffset()`, `EnvironmentBlock`,
  `EnvironmentUpdate`.
- `coder.md.tpl` split into a static template and
  `coder_dynamic.md.tpl`; `prompt.NewPromptWithDynamic` + `BuildSet` render
  named blocks (static instructions → environment → context tail).
- `internal/agent/env_update.go`: per-session environment snapshot; a change
  produces one `# Environment update` system message, injected ahead of the
  stable prefix in `PrepareStep`.
- Tests: `blocks_test.go`, `env_update_test.go`.

### Phase 2 — permissions (complete)

- `internal/permission/mode.go`: six modes with permissiveness ranks,
  `ParseMode`, `ValidateTransition`, optional `ModeService` capability.
- `internal/permission/rule.go`: `tool(content)` grammar with command,
  path/glob and domain matching, `CompileRules`, `EvaluateRules`.
- `internal/permission/readonly.go`: read-only shell allowlist (single plain
  commands only; no pipelines, redirects or git write flags).
- `internal/permission/provenance.go`: `Stage` + `Decision`; every decision
  names the stage that made it.
- `permissionService.Decide` applies, in order: skip → deny → ask → allow
  rules → allowed-tools list → hook → plan-mode denial → read-only
  auto-approval → session grant → bypass → dontAsk → prompt. `Request`
  records the decision per tool call and publishes the stage on
  notifications.
- Pipeline fills `RuleContent`/`Mutating`/`ReadOnly` from the tool spec and
  attaches `permission_stage`/`permission_reason` to results.
- Config: `permissions.mode`, `allow`, `deny`, `ask`,
  `disable_bypass_permissions`; `app.New` fails closed on an invalid mode or
  rule.
- Tests: `internal/permission/policy_test.go`.

### Phase 3 — compaction (complete)

- `internal/agent/compaction.go`: re-read the five most recently viewed
  files after a summary into a hidden working-set message, plus
  `microcompactMessages` pruning old tool output near the threshold.
- `Summarize` emits the working set and honours `PreCompact` veto /
  `PostCompact` notice.
- The working set quotes the user's last two messages verbatim, so their
  own words survive a summary that would otherwise paraphrase them.
- Tests: `internal/agent/compaction_test.go`.
- Precompute: `internal/agent/compaction_precompute.go` prepares a summary
  once usage reaches 75% of the compaction trigger, caches it against the
  transcript position it covered, and reuses it at compaction time. When new
  messages arrived after the cached position, only the tail is summarized
  and the cached text becomes the update context.
- Compact boundary: `message.CompactBoundary` is a dedicated system-role row
  written after every summary with the summary id, replaced message count,
  pre-compaction prompt tokens and files restored. System rows produce no
  AI history, so the marker is durable without reaching the model.
- Idle compaction: an idle timer per session (`idleCompactionDelay`) fires
  when a session sits at 90% of the trigger, so a user who walks away does
  not come back to a session that compacts mid-turn.
- The retained tail is the summary plus the hidden working set, both stored
  in SQLite; `finishSummaryQueue` continues any prompt queued during compaction.
- `db.Connect` now serializes `goose.Up` with a process-wide mutex: goose
  keeps global provider state, and concurrent migrations on different files
  were the likely source of the intermittently failing session test.

### Phase 4 — subagents (complete)

- `agent` tool accepts `subagent_type`: `task` (default), `explore`
  (tool list filtered to `Spec.ReadOnly` tools), `plan` (the coordinator's
  configured plan agent), and `fork` (copies the last 40 parent messages
  into the child session before running).
- Unknown types are refused with a typed error; the plan agent is resolved
  from the coordinator's built agents, which keeps the plan agent's own
  `agent` tool from recursing through construction.
- Background sub-agents: `run_in_background: true` returns a receipt
  immediately and runs the child in a detached context
  (`internal/agent/subagents.go`). The registry tracks state, output, error
  and parent session; `Cancel` and `CancelAll` cancel the matching runs.
- `send_message` addresses a run by id, name or unique id prefix. An empty
  message waits for the in-flight turn and returns the result; a non-empty
  message continues the same child session and returns its new output. The
  test covering the registry, settling and truncation is
  `internal/agent/subagents_test.go`.
- Completion publishes `notify.TypeSubagentFinished` and the `Notification`
  hook, and the TUI renders it as an info message plus a desktop
  notification.
- Tests: `TestReadOnlyToolNames`, `TestSubagentRegistryFind`,
  `TestSubagentRunSettlesOnceAndReleasesWaiters`,
  `TestDescribeSubagentTruncatesOutput`.

### Phase 5 — hooks (complete)

- `internal/hooks/hooks.go`: 19-event surface with `Events`/`IsEvent`.
- `internal/hooks/runner.go`: per-event runners
  (`NewRunnerForEvents`), `HasEvent`; `NewRunner` remains the
  PreToolUse-only compatibility constructor.
- Pipeline emits `PermissionRequest` (can allow/deny), `PermissionDenied`,
  `PostToolUse`, `PostToolUseFailure`.
- Agent emits `UserPromptSubmit` (can block a turn), `SessionStart` (first
  turn), `Stop`, `PreCompact`, `PostCompact`, `PostToolBatch`; coordinator
  emits `SubagentStart`/`SubagentStop`.
- Pipeline emits `FileChanged` after a successful mutating file tool, and
  `CwdChanged` is emitted when the environment snapshot sees the working
  directory move.
- Config rejects unknown hook events at load time and normalizes all known
  spellings.
- `InstructionsLoaded` fires once per coordinator through the prompt's
  context-file observer, so it reports the files the model actually got.
- `TaskCreated`/`TaskCompleted` are emitted by the tool pipeline from the
  `todos` metadata (`just_added`, `just_completed`), and their context is
  folded back into the result like every observational hook.
- `Notification` fires from the agent's turn-finished notification and from
  sub-agent completion; `MessageDisplay` observes the user prompt and the
  finished assistant message.
- `SessionEnd` is idempotent per session and fires on session delete
  (`Backend.DeleteSession`) and on workspace/app shutdown, bounded to two
  seconds so shutdown never waits on a hook.
- Tests: `TestPipelineEmitsTaskHooks`; the per-event runner contract tests
  live in `internal/hooks`.

### Phase 6 — deferred tools and accounting (complete)

- `internal/agent/tools/tool_search.go`: `tool_search` reveals deferred
  schemas (undeclared tools, i.e. MCP and plugins) ranked by name, hint and
  description; loaded tools stay available for the session.
- `sessionAgent.filterDeferredTools` withholds deferred schemas from the
  request until loaded, driven by `options.tool_search`.
- `context_usage` reports the window, last prompt tokens, system-prompt and
  schema estimates, and the active/loaded/deferred split
  (`internal/agent/context_report.go`).
- Prompt gains a `Tool search` block when the feature is on.
- Tests: `tool_search_test.go`, `context_report_test.go`.

### Phase 8 — memory and reminders (complete)

- `internal/memory`: typed (`user`/`project`/`team`) file-backed store with
  validation, secret guards, size limits and atomic writes.
- Tools `memory_save`, `memory_search`, `memory_forget`; the prompt gains a
  `# Memory` block describing when to use them.
- `internal/agent/reminders.go`: `<system-reminder>` hidden messages are
  inserted before the newest user turn; a denied tool call now produces one
  adjustment reminder instead of silence.
- Tests: `internal/memory/memory_test.go`, `reminders_test.go`.

### Phase 7 — plan mode (complete)

- `internal/agent/tools/plan_mode.go` + `enter_plan_mode.md` /
  `exit_plan_mode.md`: `enter_plan_mode` switches the permission mode,
  `exit_plan_mode` presents the plan for approval and restores the previous
  mode.
- `toolPipeline.modeGuard` denies mutating tools in plan mode even when a
  tool never asks for approval; read-only tools and plain shell commands
  still run.
- Registered for interactive, non-sub-agent sessions only.
- Tests: `internal/agent/tools/plan_mode_test.go`,
  `TestPipelinePlanMode*` in `internal/agent/tool_pipeline_test.go`.

### Still open

- Provider-level per-block cache scopes: blocks are ordered and labelled,
  but `fantasy` still receives one system prompt, so only prefix ordering
  (not per-block cache breakpoints) is realised. Fixing this needs a fantasy
  change (multiple system messages with per-block cache metadata), not a
  local one.
- The in-TUI permission-mode switcher cycles modes and persists
  `permissions.mode`, but it is a command-palette action rather than a
  status-bar indicator; the status bar still shows YOLO only.
- `MessageDisplay` is observational: it can add context and observe, but it
  cannot rewrite the text the TUI renders.
