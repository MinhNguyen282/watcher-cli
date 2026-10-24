# Defensive Patterns

These rules describe how Watcher keeps failures explicit and reversible.
They are written for Go and for the shell/tool boundary.

## Independent outcomes

Do not infer one fact from another. A command result carries `exit_code`,
`signal`, `timed_out`, `aborted`, and `kill_reason` separately. `exit 0` is
not proof that work completed; a timeout is not an exit code; a killed
process is not a user cancellation.

```go
outcome := job.Outcome()
if outcome.TimedOut { /* ... */ }
if outcome.Aborted { /* ... */ }
if outcome.ExitCode != 0 { /* ... */ }
```

## No timing as correctness

Do not decide whether a file changed by comparing a truncated modification
timestamp. Record a content version (size plus hash) and compare that. A
same-second rewrite is still a rewrite.

## One value, one fact

Do not derive a decision by parsing an error string. Use typed errors and
`errors.Is`/`errors.As`. If a failure has no typed form yet, add one before
routing on it.

## Fail closed

Missing approval service, unanswerable approval, unknown sandbox state, or
unparseable policy denies the operation. Only an explicit `allowed-once`
permits a tool body to run.

## Monotonic guards

A guard may deny or abstain. It cannot allow. Once a guard denies a call, no
later guard, hook, or listener can turn it back into an allow.

## Named ownership

Every resource names its owner. Background jobs are owned by the session
that started them; another session can neither read nor kill them.

## Quiescent teardown

Teardown cancels, then waits with a bounded timeout. A process that does not
finish remains tracked so the caller can inspect or retry rather than losing
the handle.

## Scrubbed child environment and private temp dirs

Strip inherited authority variables from child environments. Create private
temporary directories with restrictive permissions. Never write a file the
agent may read through a shared, world-writable path.

## Unlink, do not follow, symlinks

When removing a symlink, remove the link itself. Do not resolve it and
delete its target.

## Bounded output with disclosed loss

Every tool output has a byte budget. Oversized output writes a spill file
and carries a visible truncation marker. Silent truncation is a bug.
