# Plan 08 — Apply DeepSeek Harness Best Practices to Watcher CLI

> Status: **Implemented (Phase 0-3; see section 11 for deferrals)** ·
> Priority: **High** · Source revision:
> `deepseek-harness@22ddee37` (2026-10-23) · Scope: 16 parallel
> subsystem reviews covering docs, core, tools, llm, shell, fs, session,
> UI, testing, extensions, SDK, process, and postmortems.

## 1. Executive Summary

Watcher CLI already has the right raw materials: an agent loop, tool
registry, hooks, permissions, shadow-git checkpoints, LSP tools, repo
maps, compaction, and a Bubble Tea UI. The DeepSeek Harness study shows
the missing value is not more features. It is **making the existing
paths explicit, observable, and fail-closed**.

The highest-leverage changes, in order:

1. Move permission, hook, guard, timeout, and result normalization into
   one tool dispatch pipeline.
2. Make every tool outcome structurally complete: one call, one result,
   explicit denial, timeout, abort, and exit facts.
3. Bound and cursor background job output; stop re-reading and
   re-truncating the same buffer.
4. Replace second-resolution mtime read gates with content versions and
   apply the gate to every mutator.
5. Log tool-call intent before dispatch and repair interrupted turns
   instead of leaving dangling calls.
6. Use one context-pressure calculation and compact before the next
   request, not after a turn.
7. Make the verify card parse its own structured result instead of
   dumping a protocol envelope.
8. Adopt evidence-scoped tests, negative controls, and typed failures.

## 2. What Was Studied

Read-only review of the DeepSeek Harness clone at
`C:/Users/nhhmi/Downloads/deepseek-harness`. Sixteen subagents covered:

| Slice | Main focus |
| --- | --- |
| Root docs | Architecture, seams, lifecycle, event domains |
| Core, guard, todo, plan, goal, workflow | Agent loop and tool pipeline |
| LLM, compaction, context | Providers, retries, token pressure, summaries |
| Shell, subprocess, sandbox, jobs, spill, terminal, ssh, ptc | Execution lifecycle and Windows |
| FS, LSP, skill, attachment, document, browser, computer | Read/edit safety and context |
| Session, session-query, storage, workspace, identity, feedback, settings, credentials | Append-only logs, projections, migrations |
| Apps and client UI | Tool cards, command palette, reasoning UX |
| Testing and defensive patterns | Gates, snapshots, replay, negative controls |
| Extensions, preset, bundle, schedule | Reversible registration and composition |
| SDK, ACP, API, boot, Python, native | Headless automation and teardown |
| Subsystems, cookbook, postmortems | Contracts and production failure lessons |
| `.agents`, `.github`, process docs | Review and pre-push discipline |
| Web UI packages | Card models and popup state machines |
| API, query, projection | Stream cursors and watermarks |

Deliberately skipped: generated `lib/` and `dist/`, `node_modules`,
translations and `.zh.md` copies, image assets, snapshots and recorded
fixtures, desktop packaging, and marketplace assets. Those are either
generated or specific to a Node/Electron product.

## 3. Principles to Adopt

1. **Model-visible means logged.** A model request must be rebuildable
   from durable history. Tool-call intent, results, approvals, retries,
   and compaction must appear in the same log the model saw.
2. **Explicit resolve at boundaries.** Defaults are resolved once into a
   fully specified request. Execution does not hide fallbacks.
3. **Fail closed.** Missing approval service, unanswerable approval,
   unknown sandbox state, or unparseable config denies or errors. It never
   silently allows.
4. **Monotonic guards.** A guard may deny or abstain. It cannot allow, so
   a later listener cannot undo an earlier denial.
5. **One value, one fact.** Exit code, signal, timeout, abort, and
   sandbox enforcement are independent facts, never inferred from prose.
6. **Bounded output with disclosed loss.** Every tool has a byte or
   token budget, a tail-preserving spill, and a visible truncation
   marker.
7. **Presentation is pure.** Tool cards derive from frozen call/result
   data. No I/O, policy, or lifecycle logic in the renderer.
8. **Typed failures with a handling owner.** Do not route decisions by
   parsing error messages. Every failure names what can handle it.
9. **Evidence-scoped tests.** Run the narrowest test that would fail for
   the regression, plus one negative control.
10. **Reversible registration and explicit ownership.** Components expose
    a disposer, own their resources, and drain to quiescence.

## 4. Workstreams

### WS1 — Unified tool dispatch pipeline (P0)

**Why.** Each watcher tool currently calls `permissions.Request` itself
(`internal/agent/tools/bash.go`, `edit.go`, `write.go`, `verify.go`,
`ls.go`). Hooks are a tool decorator, checkpoints are another decorator,
and some tools return raw `error` values that can abort a turn. Policy is
spread across every tool body.

**Target stages.** Add `internal/agent/tool_pipeline.go`:

```
Resolve(agent, call)
  -> PreExecute hooks (allow/deny/rewrite is NOT allowed after logging)
  -> Approval (closed outcome, default deny)
  -> Guards (monotonic deny-only)
  -> Execute (timeout wrapper, context fused)
  -> Normalize (always one text/image result)
  -> PostExecute (bounded content, spill, redaction)
  -> Finalize (pairing check, StopTurn only for halt)
```

**Changes.**

- Define `type ToolGuard func(ctx, call) string` plus a merged guard list.
- Define `type ApprovalOutcome` with `allowed-once`, `rejected`,
  `cancelled`, `unavailable`; only `allowed-once` runs the body.
- Move `permissions.Request` calls out of tools and into the pipeline.
  Keep a per-tool policy descriptor for safe read-only commands.
- Keep hook decisions: deny and halt are tool errors; "allow" only skips
  the prompt, never a guard.
- Make every tool return exactly one result, including early errors and
  panics. A tool that fails to start still produces a paired result.
- Add a timeout wrapper that swaps and restores `context.Context`; do not
  race two contexts.
- Record call intent before dispatch (see WS5).

**Tests.**

- Denied approval never executes the body.
- Missing approval service denies.
- A guard denial cannot be overridden by a later allow.
- Timeout produces `timed out`, not a generic exit code.
- Every error path produces one result and no dangling tool call.
- Existing hook and checkpoint tests still pass.

**Do not copy.** The Cordis waterfall, `any`-typed event maps, scope
inversion, or dynamic plugin dispatch. Use plain Go middleware and
explicit structs.

### WS2 — Permission and hook semantics (P0)

**Why.** `internal/agent/hooked_tool.go` can rewrite tool input after the
call was logged, and treats hook "allow" as an implicit approval. The
harness forbids rewriting logged arguments and separates approval policy
from guard policy.

**Changes.**

- Remove `UpdatedInput` rewriting after a call is logged, or log the
  rewritten input as a new decision record before execution.
- Make hook approval call-scoped and one-shot; never persist it for the
  session unless the user explicitly chooses "always allow".
- Keep `WithHookApproval` keyed by tool-call ID.
- Emit an audit record for both ask and decision.
- Define a `never` policy above the answerer chain so no hook can bypass
  it.
- Add a permission test matrix: allow, deny, cancel, missing service,
  duplicate grant, late answer after cancel.

**Files.** `internal/permission/`, `internal/agent/hooked_tool.go`,
`internal/hooks/`, `internal/agent/coordinator.go`.

### WS3 — Shell, jobs, and watch lifecycle (P1)

**Why.** `internal/shell/background.go` uses unbounded buffers retained
for hours; `job_output` re-reads and re-truncates the whole buffer;
`bash` auto-backgrounds with a 100 ms polling loop; `watchdog` scans
synchronously and skips existing log lines. These are reliability and
memory issues, not polish.

**Changes.**

- Replace unbounded `bytes.Buffer` with a bounded ring that drops oldest
  output and preserves a spill path.
- Give readers a cursor. Repeated `job_output` calls return only new
  output by default, with an explicit `from_start` or `offset` option.
- Classify outcomes independently: `exit_code`, `signal`, `timed_out`,
  `aborted`, `kill_reason`. Never infer success from `exit 0` alone.
- Register a job before waiting; promote a timeout to background instead
  of re-registering.
- Replace the 100 ms ticker with a done-channel wait.
- Scope job IDs by session; require ownership for read and kill.
- Run `watch` scans in their own goroutine with a bounded queue; add a
  deadline and signal forwarding.
- In tail mode, optionally read from the start (`--from-start`) instead
  of always seeking to EOF.
- Either run `watch` commands through the shell or document that the
  parser is argv-only. Do not silently drop pipes.
- Report sandbox mode/denial/enforcement as result facts if sandboxing
  is added.

**Tests.** Ring overflow, cursor reads, exit-vs-timeout, kill by
non-owner, watch finding delivery under slow handlers, tail from start.

### WS4 — Filesystem observation and edit safety (P1)

**Why.** `filetracker` records only a read timestamp; mutators compare
`ModTime().Truncate(time.Second)`. Rewrites within the same second can
bypass the read gate. `multiedit`, `lsp_rename`, and
`lsp_replace_symbol` do not consistently check observation. `write`
does a direct write rather than temp+rename.

**Changes.**

- Store `(session, path, version)` where version is
  `size + sha256(content)` or at least `size + mtime_ns + inode`.
- Record absence as an observation too; a later create is allowed.
- Apply the read gate to every mutator: edit, multiedit, write, rename,
  replace-symbol, download, and MCP file tools.
- Unify stale-read errors into one stable message with an actionable
  retry hint.
- Use temp-file + rename for atomic writes; compute diffs from the
  before/after buffers in the same critical section.
- Add exact total line count and `(line truncated to N chars)` markers to
  `view`.
- Cap repo-map query results by count and characters, with a
  "verify with view/grep" footer.
- Prefer content hashes over mtime for repo-map cache keys.

**Tests.** Same-second rewrite is rejected; unread file is rejected;
absence then create succeeds; atomic write leaves no partial file;
truncation markers present; repo-map queries bounded.

### WS5 — Session log and persistence (P1)

**Why.** `internal/session` stores a mutable `parts` JSON blob and
`Session.Save` overwrites the whole row. A crash can leave a tool call
without a result. The harness treats an append-only event log as truth
and derives everything else.

**Changes.**

- Add an append-only `events` table:
  `(session_id, seq, type, time, data, ignorable)` with
  `PRIMARY KEY(session_id, seq)`.
- Enforce contiguous seq on insert; reject gaps.
- Persist tool-call intent before dispatch and a result for every
  outcome, including `TOOL_OUTCOME_UNKNOWN` after a crash.
- Repair interrupted turns at read time with synthetic `tool/result`,
  `step/end`, and `turn/end {interrupted}` records. Never truncate a
  valid prefix.
- Keep `sessions`, `messages`, and `files` as derived projections or
  caches, not truth.
- Flush at checkpoint boundaries: before a model request, before a
  top-level tool body, and before a step. Use WAL and explicit
  `synchronous` settings.
- Record shadow-git checkpoints as events (`checkpoint/created` with the
  commit SHA); git owns bytes, the log owns ordering.
- Add `application_id` and `user_version` to per-project DBs; stamp the
  schema version last.
- Keep event-format versioning separate from schema migrations. Unknown
  newer/older versions fail with direction-aware errors.
- Add a crash-injection test: kill after call log, reopen, verify
  synthetic result and no dangling call.

**Do not copy.** The full `session-projection` unit registry,
`stateVersion` matrices, or wire-format extension fields. Watcher has no
external session consumers yet.

### WS6 — LLM, retries, context pressure, compaction (P1/P2)

**Why.** Compaction uses `PromptTokens + CompletionTokens` even though
`PromptTokens` is overwritten with context tokens, so it double-counts
output. It compacts after the turn and injects a rewritten prompt that is
not in durable history. DeepSeek effort levels are now metadata-driven,
but the provider option switch still hard-codes many providers.

**Changes.**

- Use one `contextTokens(usage)` calculation for pressure. Do not add
  completion tokens to a context-window value.
- Replace `200_000 / 20_000 / 0.2` constants with a validated config:
  threshold ratio, absolute headroom, retention ratio or absolute count,
  and per-model overrides.
- Trigger compaction in a pre-step hook and on provider
  `CONTEXT_WINDOW_EXCEEDED`, not after the turn.
- Summarize with the real system prompt and history as a warm prefix;
  append the instruction as the final user message.
- Persist only summary text; reject `max-tokens` summaries and summaries
  that do not shrink the source.
- Snap summary boundaries to balanced tool-call/result pairs.
- Add a per-provider retry policy: eligible codes, max attempts, jittered
  backoff, `Retry-After` bounds. Record retry intent before waiting.
- Add a usage-trust rule: reuse provider usage only when the request
  envelope matches; otherwise estimate.
- Move provider-specific reasoning fields out of the coordinator switch
  into model/provider metadata where practical. Keep DeepSeek
  `none -> thinking.disabled` and never send an unsupported effort.

**Tests.** Context pressure with large output; compaction at 80% and
overflow; truncated summary rejection; retry code matrix; DeepSeek
low/high/max/none wire fields.

### WS7 — UI, command palette, and CLI polish (P2)

**Why.** The new verify card renders the raw `<verify>` envelope. The
command palette is a flat list, encodes state in titles, and duplicates
reasoning controls. The CLI has no structured headless output.

**Changes.**

- Parse the verify envelope into a card model: header status, profile,
  duration, per-check rows, failed checks first, failure locations, and
  raw output behind expansion. Success renders the header only.
- Show timed-out checks as `timed out`, not `exit -1`.
- Surface truncation as a labeled marker, never silently.
- Group the command palette into stable sections: Session, Model and
  Reasoning, View, Config. Put current values in an annotation column,
  not in the title.
- Use one reasoning control: a selector with the current level checked,
  including "Provider default" and "None" where supported. Keep stable
  IDs and aliases for search.
- Park the highlight on the current value.
- Add a "preparing tool call" state distinct from "running".
- Keep color from being the only status cue; use glyphs and labels.
- CLI: data on stdout, diagnostics on stderr, one JSON object per line
  for `run --json`, stable exit codes, a teardown ladder, explicit data
  directory for automation, and client/server version equality.

**Do not copy.** Web DOM machinery, hover states, i18n dictionaries, or
the full web command catalog.

### WS8 — Testing and quality gates (P1)

**Why.** The harness treats checks as evidence, not ceremony. Watcher's
current `go test ./...` includes environment-dependent failures and no
negative-control habit.

**Changes.**

- Adopt the pass predicate:
  `exitCode == 0 && signal == nil && !aborted && !timedOut`.
- Add a `task check` (or `scripts/check.*`) with named modes:
  `static`, `unit`, `integration`, `e2e`, `coverage`.
- Add negative controls for every new guard: prove it rejects an invalid
  case through the real path.
- Add replay/golden fixtures for the tool pipeline and watch stream;
  avoid sleeps by using sequencing hooks.
- Use `go/ast` and `go/types` for static verifiers rather than regex.
  Start with 2-3 high-value checks, not 100.
- Add a baseline-fingerprint gate for known violations that cannot be
  fixed immediately.
- Keep 100% coverage only for core packages if adopted; make exemptions
  an explicit file list with reasons.
- Add benchmark budgets with `go test -bench ... -benchmem` and
  `benchstat`; treat regressions as alerts, not silent passes.
- Make CI use the same `task check` entry as local.

**Do not copy.** Browser snapshot lanes, 100% per-file coverage on every
package, 19 gate aggregates, or per-machine benchmark calibration.

### WS9 — Process and documentation (P3)

**Changes.**

- Add `docs/defensive-patterns.md` with the rules that match Go:
  independent outcomes, no timing-as-correctness, one-value-one-fact,
  named ownership, quiescent teardown, scrubbed child env, private temp
  dirs, unlink-not-rm for symlinks.
- Add a short error-handling rule to `AGENTS.md`: typed failures, named
  handling owner, visible unknown outcomes, no message parsing.
- Add an ADR/note folder with a mandatory "Alternatives considered"
  section. One note per durable decision is enough.
- Add a PR template: motivation, behavior vs interface changes, test
  evidence, negative control, known limits.
- State the "narrowest failing check" rule and forbid raw `--force`.

**Do not copy.** Issue lifecycle automation, weighted review ownership,
bilingual doc gates, word budgets, or stack-PR machinery.

### WS10 — Extensibility and config (P4, deferred)

**Changes only if a second consumer appears.**

- Prefer named seams and constructor injection over a plugin runtime.
- Make registration return a disposer; disposers are idempotent and
  LIFO.
- Allow declared dependencies with a `pending` state instead of startup
  ordering hacks.
- If config layering is needed, use base/profile/user patches where a
  patch replaces a whole entry; warn on patches that match no entry.
- Keep preset revisions per session if agent presets are added.
- Reuse the scheduler discipline only if scheduled tasks are added:
  one timer, recompute after commit, latest missed occurrence, no
  automatic retry of failed delivery.

**Do not build.** Cordis fibers, dynamic `node:vm` plugins, HMR,
cross-realm identity, Typert, or generated service faces.

## 5. Phased Roadmap

| Phase | Workstreams | Outcome | Exit criteria |
| --- | --- | --- | --- |
| 0 (1 week) | WS1, WS2, small WS3/WS4/WS7 fixes | One dispatch path; no raw tool errors; verify card no longer dumps XML | Approval/guard/timeout matrix green; tool-call pairing test |
| 1 (2 weeks) | WS3, WS4, WS8 | Bounded, cursor-based jobs; content-version edit gates; check modes | Memory bounded under 1M-line output; same-second rewrite rejected; negative controls |
| 2 (2-3 weeks) | WS5, WS6 | Append-only events; pre-step compaction; retry policy | Crash injection repairs; context pressure test; DeepSeek wire test |
| 3 (1-2 weeks) | WS7, WS9 | UI/CLI polish; process docs | Verify card snapshot; `run --json`; PR template |
| 4 (deferred) | WS10 | Optional seams/config layering | Only if a second consumer exists |

## 6. Acceptance Criteria

- [x] Every tool call produces exactly one result, including denial,
      cancel, timeout, panic, and crash recovery.
- [x] No tool body calls `permissions.Request` directly.
- [x] A guard denial cannot be overridden by a later allow.
- [x] Missing or unanswerable approval denies without executing.
- [x] Job output is bounded, cursor-readable, and owner-scoped.
- [ ] `watch` can scan a pre-existing log and does not block its scanner
      on a slow fix.
- [x] Same-second rewrites and unread files are rejected by every
      mutator.
- [ ] Session rows can be rebuilt from an append-only event log; no
      dangling tool calls after a crash.
- [x] Compaction uses context tokens only and runs before the next
      request.
- [x] Verify results render as structured checks, not raw protocol text.
- [x] `task check` is the single local/CI entry; new guards have
      negative controls.
- [x] Full-suite environmental failures are quarantined or documented,
      not treated as normal.

## 7. Metrics

Track these before and after each phase:

- Tool calls without a paired result.
- Turns wasted on stale-read or edit-match failures.
- Peak memory for background jobs.
- Time from test failure to a parsed failure location.
- Number of permission prompts per task.
- Compaction frequency and post-compaction task success.
- `verify` pass rate on the first attempt after an edit.
- CI runtime and flake rate.

## 8. Non-Goals and Explicit Rejections

- No Cordis/plugin runtime, profile patch language, or dynamic plugin
  loading.
- No desktop, web, marketplace, i18n, or translation machinery.
- No TypeScript-specific type gymnastics, branded remote IDs, or
  generated service faces.
- No 100% per-file coverage on every package.
- No full session-format compatibility matrix until watcher has external
  session consumers.
- No harness-specific request-body extensions or remote transcript
  uploads.
- No rewrite of logged tool arguments after the fact.
- No unbounded retries or hidden fallbacks on unknown state.

## 9. Source Evidence

| Workstream | DeepSeek Harness evidence |
| --- | --- |
| WS1 | `docs/tool-execution-pipeline.md`; `docs/subsystems/tools.md`; `packages/core/tools/src/pipeline.ts`; `packages/core/tools/src/batch.ts` |
| WS2 | `docs/subsystems/approval.md`; `packages/guard/timeout-policy`; `packages/interaction/user-approval` |
| WS3 | `docs/subsystems/shell.md`, `jobs.md`; `packages/shell/bash-local`; `packages/jobs`; `packages/spill`; `packages/sandbox` |
| WS4 | `docs/subsystems/filesystem.md`, `lsp.md`; `packages/fs/fs-observation-policy`; `packages/fs/tool-fs`; `packages/lsp` |
| WS5 | `docs/subsystems/session.md`, `persistence.md`; `packages/session/session-persistence`; `session-persistence-jsonl`; `core/session/src/repair.ts` |
| WS6 | `packages/llm/llm`, `llm-retry`, `llm-deepseek`; `packages/compaction/compaction-basic`; `packages/context` |
| WS7 | `packages/client/ui-tool`, `ui-commands`, `ui-model-selection`; `docs/cli-help.md`; `apps/cli` |
| WS8 | `docs/testing.md`; `docs/defensive-patterns`; `scripts/run-gates.ts`; `scripts/verify-*`; `benchmarks/AGENTS.md` |
| WS9 | `.agents/skills/dsh-code-review`, `dsh-pre-push-checks`, `dsh-error-handling`; `.agents/notes/README.md` |
| WS10 | `docs/cordis-primer.md`; `packages/preset`; `packages/bundle`; `packages/schedule`; `packages/extensions` |

## 10. First Ten Changes

1. Add `internal/agent/tool_pipeline.go` with resolve, guard, approve,
   execute, normalize, finalize stages.
2. Move `permissions.Request` out of bash/edit/write/verify/ls and into
   the pipeline.
3. Make hook approval call-scoped and remove logged-argument rewriting.
4. Replace job buffers with a bounded ring and add cursor reads.
5. Add exit, signal, timeout, and abort fields to job results.
6. Replace mtime read gates with content versions and cover all
   mutators.
7. Parse the verify envelope into a structured card.
8. Move `watch` scanning to a goroutine with a timeout and
   `--from-start`.
9. Add interrupted-turn repair with synthetic tool results.
10. Add a `task check` gate runner and one negative-control test for
    every new guard.

## 11. Implementation Notes (2026-10-25)

Landed:

- `internal/agent/tool_pipeline.go`: hook resolve, intent logging, guard
  chain, approval injection, timeout, panic recovery, and one-result
  normalization. `tools.RequestApproval` is the only approval entry point
  used by tool bodies.
- `internal/agent/tool_events.go` plus `session_event` append-only log,
  contiguous sequence enforcement, synthetic results for dangling calls,
  and repair on the settled-read path.
- Bounded background-job rings with cursor reads, `from_start`, ownership,
  and independent exit/signal/timeout/abort facts.
- Content-version read gates, atomic writes, view line markers and totals,
  repo-map result bounds.
- Context-token-only compaction budgets with configurable headroom or
  ratio, warm-prefix summarization, and truncated-summary rejection.
- Structured verify card and `crush run --json` newline-delimited events.
- `task check`, `docs/defensive-patterns.md`, `docs/testing.md`, ADR folder,
  and PR template.

Not applicable or deferred in this repository:

- `watch` does not exist as a tool or command, so the watch scanner,
  `--from-start`, and slow-handler queue changes have no target. The
  equivalent cursor/`from_start` behavior landed on `job_output`.
- The command palette grouping and reasoning-control consolidation were
  left for a dedicated UI pass; the verify card and CLI JSON output are in.
- Session events are persisted and repaired, but `sessions`, `messages`,
  and `files` remain the read projections; a full projection rebuild was
  not added.
- Retry policy remains provider/fantasy-owned; no duplicate retry loop was
  introduced.
- The repo-map cache still keys on an mtime:size stamp; result output is
  bounded, but a content-hash cache key needs a measured rebuild budget
  first.
