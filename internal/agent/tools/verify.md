Run the project's own build and test checks. Use this after editing
instead of guessing commands such as `go test ./...` or `npm test`.

Discovery supports Go (`go.mod`), Node (`package.json`), Rust
(`Cargo.toml`), Python (`pytest`), and Make (`test` target). By default
the tool selects only changed files and their packages using `git status`
and the repository map. Set `full` to run the whole project.

Parameters:
- `targets`: optional files or directories to verify.
- `full`: run the full suite instead of changed targets.
- `timeout_seconds`: per-check timeout (default 120, max 600).

The response includes a short status for each command and structured
failures with `file:line` and test names. Treat a failed status as a
blocking error: fix the failures and run `verify` again.
