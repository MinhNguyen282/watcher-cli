---
name: shell-builtins
description: Use when creating a new shell builtin command for Crush (internal/shell/), editing an existing one, or when the user needs to understand how commands are intercepted in Crush's embedded shell.
---

# Shell Builtins

Crush's shell (`internal/shell/`) uses `mvdan.cc/sh/v3` for POSIX shell
emulation. Commands can be intercepted before they reach the OS by adding
**builtins** (functions handled in-process).

## How Builtins Work

Builtins are registered in the package-level registry in
`internal/shell/builtins_registry.go` via `shell.RegisterBuiltin`. The
`builtinHandler()` middleware in `internal/shell/run.go` dispatches them. It is
an `interp.ExecHandlerFunc` middleware registered in `standardHandlers()`
**before** the block and exec handlers, so builtins run even for commands that
would otherwise be blocked. The same handler chain is shared by the stateful
`Shell` type and the stateless `Run` entrypoint used by the hook runner,
so builtins are available identically in the `bash` tool and in hooks.

`jq` registers itself in `builtins_registry.go`'s `init`; the config builtins
are registered by `internal/shellconfig` to avoid an import cycle.

## Adding a New Builtin

1. **Implement the handler** in its own file (e.g.,
   `internal/shell/mycommand.go`). It must match `BuiltinHandler`: a
   `context.Context` first parameter, then args, stdin, stdout, and stderr.
   ```go
   func handleMyCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
       // args[0] is the command name ("mycommand"), args[1:] are arguments.
       // Write output to stdout, errors to stderr.
       // Return nil on success, or interp.ExitStatus(n) for non-zero exit codes.
   }
   ```
2. **Register it** with `RegisterBuiltin("mycommand", handleMyCommand)` during
   `init` (use `shell.RegisterBuiltin` when registering from another package,
   as `internal/shellconfig/register.go` does).
3. **Use the provided I/O streams**, never `os.Stdin`/`os.Stdout`. The
   dispatcher fills stdin/stdout/stderr from `interp.HandlerCtx(ctx)`, which
   keeps pipes and redirections working. Read from `stdin` and write to
   `stdout`/`stderr` directly.
4. **Poll `ctx` in every unbounded loop.** Builtins that iterate over
   input, emit values in a generator-style loop, or do any other work
   that can exceed a few milliseconds MUST check `ctx.Err()` on each
   iteration and return it verbatim when non-nil. Hook timeouts rely on
   this: an unbounded builtin that never polls ctx cannot be interrupted
   by a hook's `timeout_sec`, and the hook runner will have to abandon
   the goroutine (see `internal/hooks/runner.go`). Returning `ctx.Err()`
   (not `interp.ExitStatus(n)`) lets callers distinguish "command exited
   non-zero" from "we ran out of time".
   ```go
   for _, item := range items {
       if err := ctx.Err(); err != nil {
           return err
       }
       // ... process item
   }
   ```
5. **Return values**: return `nil` for success, `interp.ExitStatus(n)` for
   non-zero exit codes, or `ctx.Err()` on cancellation. Write error
   messages to `stderr` before returning.
6. **No extra wiring needed**: `builtinHandler()` already dispatches every
   registered builtin before the block and exec handlers.

## Existing Builtins

| Command | File | Description |
|---------|------|-------------|
| `jq` | `jq.go` | JSON processor using `github.com/itchyny/gojq` |
| `provider`, `model`, `mcp`, `lsp`, `db`, `permissions`, `hook`, `option` | `internal/shellconfig/register.go` | Config builtins; no-ops without a `ConfigBuilder` on the context |
