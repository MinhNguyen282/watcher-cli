# 0001: One tool dispatch pipeline

## Status

Accepted.

## Context

Permissions, hooks, checkpoints, timeouts, and result normalization used to
live in separate decorators and in every tool body. A tool could return a raw
Go error, skip the permission flow, or rewrite its input after the call was
already logged. That made outcomes impossible to compare and left dangling
tool calls after a crash.

## Decision

All model tool calls go through `internal/agent/tool_pipeline.go`:

1. Resolve hooks and input rewrites.
2. Log the final call intent.
3. Evaluate deny-only guards.
4. Inject the approval stage; only `allowed-once` runs the body.
5. Execute with a per-tool timeout and panic recovery.
6. Normalize every outcome into exactly one tool response.
7. Record a result event and finalize with hook context.

Tool bodies call `tools.RequestApproval`, never the permission service
directly.

## Alternatives considered

- Keep per-tool permission calls and add a linter. Rejected: the policy would
  still be spread across tool bodies and easy to bypass.
- Adopt a dynamic plugin/waterfall runtime. Rejected: too much machinery for
  one in-process Go pipeline.
- Normalize only in the agent loop. Rejected: direct tool tests and headless
  callers would keep the old, inconsistent behavior.

## Consequences

- A denied call never executes the body.
- A missing approval service denies.
- A guard denial cannot be overridden.
- Every call produces one result, including timeout, abort, and panic.
- Tool authors must return a `ToolResponse`; raw errors are converted at the
  pipeline boundary.
