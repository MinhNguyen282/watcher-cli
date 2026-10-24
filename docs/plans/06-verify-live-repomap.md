# Plan 06 — Verification Loop + Live Incremental Repo Map

> Priority: **#1 and #2** · Difficulty: ⭐⭐⭐ · Value: High (every coding
> task runs through these paths) · Harness2 fit: Strong

## 1. Goals

Close the two gaps that make Watcher guess during coding tasks:

1. **Verification is deterministic.** A new `verify` tool detects the
   project's own build/test commands, selects the narrowest useful target
   set from the current changes and the repo map, runs the checks, and
   returns structured failures. The model no longer has to invent commands
   or read raw 30k-character output.
2. **Repository navigation stays correct while editing.** The repo map
   revalidates on every query, reparses only changed files, respects
   `.gitignore`, records which files are tests, and can answer "which tests
   cover this change?" and "what is impacted?".

Non-goals: replacing LSP, building a full language server, or auto-fixing
failing tests. `verify` reports; the coder agent still decides what to edit.

## 2. Current Gaps (verified in code)

| Gap | Evidence |
| --- | --- |
| The prompt says "run tests" but there is no project-aware tool | `internal/agent/templates/coder.md.tpl:29`; tool list in `internal/agent/tools/` has no verify/test tool |
| Failure output is truncated head+tail, not parsed | `internal/agent/tools/bash.go:426` `TruncateOutput` |
| The map is memoized for the life of the coordinator | `internal/repomap/service.go:91-105` returns `s.graph` without revalidation |
| A change rebuilds every file | `internal/repomap/service.go:47-85` walks and parses all files |
| `HasTests` is declared but never set | `internal/repomap/types.go:56`; no assignment in `walk.go` |
| Repository walk uses a fixed skip list, not `.gitignore` | `internal/repomap/walk.go:15-40` |

## 3. Design

### 3.1 Live incremental repo map (`internal/repomap`)

**File inventory**

- If the root is a git worktree, list candidates with
  `git ls-files -z --cached --others --exclude-standard`. This respects
  `.gitignore` exactly and avoids walking ignored directories.
- Otherwise fall back to the existing `fastwalk` inventory with the fixed
  skip list.
- Drop files larger than `maxIndexedFileSize`; keep the existing language
  filter.
- Set `FileInfo.HasTests` from `_test.go` and common test-file suffixes
  (`.test.*`, `.spec.*`, `test_*.py`, `*_test.py`).

**Per-file fragments**

Add a private fragment type to `Service`:

```go
type fileFragment struct {
    info    FileInfo
    symbols map[string]Symbol
    edges   []Edge
}
```

`Build` becomes incremental:

1. Inventory files.
2. For every file whose `Stamp` matches the cached fragment, reuse it.
3. Parse only new or changed files into a temporary `Graph`, then collect
   that file's symbols and edges into a fragment.
4. Drop fragments for deleted files.
5. Assemble a fresh `Graph` from all fragments, call `finalize`, save cache.

**Revalidation**

`Load` must compare the on-disk inventory with `s.graph` on every call.
When nothing changed it returns the same `*Graph` pointer (existing test
contract). When something changed it performs the incremental build.
Serialize builds with a mutex so concurrent tool calls cannot interleave.

Cold cache path: load `graph.json`, verify stamps, reconstruct fragments
from the cached graph so the first edit is still incremental.

**New queries**

- `Graph.TestsFor(targets []string) []TestFile`: resolve targets to files,
  then return test files in the same Go package directory, matching JS/TS
  test siblings, or matching Python test names.
- `repo_map` tool: add `tests_for` and `impact` (blast radius, default
  depth 3, files plus covering tests).

### 3.2 `verify` package (`internal/verify`)

Keep discovery, targeting, execution, and parsing out of the tool layer so
they are unit-testable.

```go
type Profile struct {
    Root    string
    Kind    Kind        // go, node, rust, python, make
    Checks  []Check     // ordered, deduplicated
}

type Check struct {
    Name string   // build, test, lint
    Argv []string
    Dir  string
}

func Detect(root string) (Profile, error)
func SelectTargets(profile Profile, graph *repomap.Graph, changed []string, requested []string) (Profile, error)
func ParseFailures(kind Kind, output string) []Failure
```

**Detection order**

1. `go.mod`: `go test` (full or selected packages).
2. `package.json`: detect `npm`, `pnpm`, `yarn`, or `bun` from lockfiles;
   run `test`, then `typecheck`/`lint` when present (skip npm's placeholder
   "no test specified" script).
3. `Cargo.toml`: `cargo test`; use the nearest crate manifest for changed
   files.
4. `pyproject.toml`, `setup.cfg`, `pytest.ini`, or `tox.ini`:
   `python -m pytest` (selected paths when available).
5. `Makefile` with a `test:` target: `make test`.

**Target selection**

- Explicit `targets` argument wins, after confinement to the repo root.
- Otherwise collect changed files from `git status --porcelain`.
- Go: map changed `.go` files to package directories and run
  `go test ./dir` for each, collapsing to `./...` when the module root is
  affected.
- Rust: run the crate manifest nearest each changed file.
- Python: pass changed test files, else changed source paths, else full.
- Node and Make: run the discovered script; the script owns selection.

**Failure parsing**

Normalize into `Failure{File, Line, Test, Message}` for Go test, Jest and
Vitest, pytest, Cargo, and generic `file:line:col: error` lines. Return a
bounded list (top 12) plus counts. When nothing parses, return the last 80
lines of output instead of the whole stream.

### 3.3 Tool and wiring

- `internal/agent/tools/verify.go` plus `verify.md`.
- Parameters: `targets []string`, `full bool`, `timeout_seconds int`.
- One permission request per invocation describing the commands.
- Execute sequentially with `exec.CommandContext`; default 120s, max 600s.
- Register in `coordinator.buildTools` and add `verify` to
  `allToolNames()` only. It stays out of the read-only and plan masks.
- Add a short `<verification>` rule to `coder.md.tpl` telling the model to
  use `verify` instead of guessing test commands.
- Update the repo map tool description with the new queries.

## 4. Acceptance Criteria

- [x] `repo_map` reflects an edit made after the first query in the same
      process.
- [x] Editing one file reparses only that file (assert with a parse counter
      or by checking reused fragments).
- [x] `.gitignore` entries are excluded when the root is a git repository.
- [x] `HasTests` is true for `_test.go`, `.test.ts`, and `test_*.py`.
- [x] `tests_for` returns the covering test file for a changed source file.
- [x] `verify` detects Go, Node, Rust, Python, and Make projects.
- [x] `verify` selects changed Go packages and returns parsed failures.
- [x] `verify` is available to the coder agent but not to plan/task agents.
- [x] `go test ./internal/repomap/... ./internal/verify/... ./internal/agent/tools/...`
      passes.

## 5. Files

| File | Change |
| --- | --- |
| `internal/repomap/service.go` | Incremental build, revalidation, fragment cache |
| `internal/repomap/walk.go` | Git-aware inventory, `HasTests` |
| `internal/repomap/graph.go` | `TestsFor`, impact helper |
| `internal/repomap/types.go` | Test-file helpers if needed |
| `internal/agent/tools/repo_map.go` | `tests_for`, `impact` |
| `internal/verify/*.go` | Detection, targeting, execution, parsing |
| `internal/agent/tools/verify.go` | Tool wrapper |
| `internal/agent/tools/verify.md` | Tool description |
| `internal/agent/coordinator.go` | Register the tool |
| `internal/config/config.go` | Add `verify` to `allToolNames` |
| `internal/agent/templates/coder.md.tpl` | Verification rule |
| `docs/plans/README.md` | Status entry |
