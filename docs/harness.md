# Self-Evolving Harness (Harness²)

Crush can improve its own operating instructions. The `--evolve` flag on
`crush run` runs a Propose → Probe → Compose loop inspired by the
Harness² algorithm: instead of hand-writing the perfect `CRUSH.md`, you
ask Crush to observe its own behavior on a task and keep the harness
variant that behaved best.

## How it works

1. **Probe (base).** The task runs with the current harness in an
   isolated `git worktree`, so your working tree is never touched.
2. **Propose.** An editor LLM reads the base run's execution trace (tool
   calls, errors, git diff) and proposes `K` improved harness variants:
   guardrails, per-tool guidance, memories, skills, and helper scripts.
3. **Probe (candidates).** Each candidate runs the same task in its own
   worktree (`--mode parallel`) or chained onto the previous candidate's
   branch (`--mode sequential`).
4. **Compose.** A judge LLM compares behaviors (behavioral contrast —
   no ground truth needed) and picks the winner, merging the best
   harness elements.
5. **Reflect.** One evidence-backed lesson is distilled and stored for
   future tasks.
6. **Apply.** The winner's diff is applied to your working tree. If your
   tree is too dirty to patch, the winner stays on its branch and you
   are told how to merge it.

## Usage

```sh
# Evolve: 2 candidate harnesses, probed in parallel
crush run --evolve --k 2 --mode parallel "Fix the race in the worker pool"

# Evolve without touching the working tree (winner stays on a branch)
crush run --evolve --no-apply "Refactor the auth module"
```

The refined harness is saved to `.watcher/harness_state.json` in your
repository and is automatically appended to the coder's system prompt on
the next run. Lessons accumulate in `.watcher/memory/lessons.md`; every
probe trace is kept under `.watcher/traces/`. Add `.watcher/` to
`.gitignore` if you want the state to stay local, or commit it to share
the learned harness with your team.

## Requirements

- The working directory must be a git repository with at least one
  commit (worktrees are created from `HEAD`).
- The editor LLM uses your currently selected large model. Supported
  provider families: Anthropic, OpenAI, OpenAI-compatible (including
  custom endpoints like Ollama), Google, and OpenRouter.
- Each probe spawns a fresh `crush` process with an isolated data
  directory, so the probe history never pollutes your session list.

## Layout

- `internal/harness/types.go` — harness state, traces, run options.
- `internal/harness/store.go` — `.watcher/` persistence.
- `internal/harness/worktree.go` — git worktree isolation.
- `internal/harness/trace.go` — behavioral evidence extraction.
- `internal/harness/editor.go` — Propose / Compose / Reflect prompts.
- `internal/harness/optimizer.go` — the recursion loop.
- `internal/agent/harness_prompt.go` — system prompt injection point.
- `internal/cmd/harness.go` — CLI wiring and the subprocess probe runner.
