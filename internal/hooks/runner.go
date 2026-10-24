package hooks

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
)

// abandonGrace is how long runOne waits after ctx cancellation for the
// shell goroutine to yield before returning control to the caller and
// letting the goroutine finish on its own. Mirrors the historical
// cmd.WaitDelay = time.Second behavior of the previous os/exec path.
const abandonGrace = time.Second

// runShell is the shell executor used by runOne. It is a package-level
// variable so tests can substitute a blocking or non-yielding
// implementation to exercise the abandon-on-timeout path without
// depending on the scheduling behavior of the real interpreter.
var runShell = shell.Run

// compiledHook pairs a HookConfig with its compiled matcher regex. A nil
// matcher means "match every tool".
type compiledHook struct {
	cfg     config.HookConfig
	matcher *regexp.Regexp
}

// Runner executes hook commands and aggregates their results. Hooks are
// kept per event so one runner serves every lifecycle event a session
// fires.
type Runner struct {
	hooksByEvent map[string][]compiledHook
	cwd          string
	projectDir   string
}

// NewRunner creates a Runner from the given hook configs. Each hook's
// Matcher is compiled here so the Runner is self-sufficient; callers do
// not have to pre-compile matchers on the config, and reloads or merges
// that rebuild HookConfig values can't silently strip compiled state.
//
// Hooks whose matcher fails to compile are skipped with a warning rather
// than treated as match-everything. ValidateHooks is expected to have
// caught syntax errors earlier, so this is defense in depth.
// NewRunner builds a PreToolUse-only runner. It is the compatibility
// constructor for callers that only care about tool gating; NewRunnerForEvents
// covers the rest of the lifecycle.
func NewRunner(hooks []config.HookConfig, cwd, projectDir string) *Runner {
	if len(hooks) == 0 {
		return NewRunnerForEvents(nil, cwd, projectDir)
	}
	return NewRunnerForEvents(map[string][]config.HookConfig{EventPreToolUse: hooks}, cwd, projectDir)
}

// NewRunnerForEvents builds a runner from every configured event.
func NewRunnerForEvents(hooksByEvent map[string][]config.HookConfig, cwd, projectDir string) *Runner {
	compiled := make(map[string][]compiledHook, len(hooksByEvent))
	for event, hooks := range hooksByEvent {
		for _, h := range hooks {
			ch := compiledHook{cfg: h}
			if h.Matcher != "" {
				re, err := regexp.Compile(h.Matcher)
				if err != nil {
					slog.Warn(
						"Hook matcher failed to compile; skipping hook",
						"event", event,
						"matcher", h.Matcher,
						"command", h.Command,
						"error", err,
					)
					continue
				}
				ch.matcher = re
			}
			compiled[event] = append(compiled[event], ch)
		}
	}
	return &Runner{
		hooksByEvent: compiled,
		cwd:          cwd,
		projectDir:   projectDir,
	}
}

// Hooks returns the hook configs the runner was created with, in config
// order. Hooks whose matcher failed to compile at construction are
// omitted. Intended for diagnostics; callers should not rely on ordering
// or identity beyond that.
func (r *Runner) Hooks() []config.HookConfig {
	var out []config.HookConfig
	for _, event := range Events {
		for _, h := range r.hooksByEvent[event] {
			out = append(out, h.cfg)
		}
	}
	return out
}

// HasEvent reports whether any hook is configured for the event.
func (r *Runner) HasEvent(event string) bool {
	return r != nil && len(r.hooksByEvent[event]) > 0
}

// Run executes all matching hooks for the given event and tool, returning
// an aggregated result.
func (r *Runner) Run(ctx context.Context, eventName, sessionID, toolName, toolInputJSON string) (AggregateResult, error) {
	matching := r.matchingHooks(eventName, toolName)
	if len(matching) == 0 {
		return AggregateResult{Decision: DecisionNone}, nil
	}

	// Deduplicate by command string.
	seen := make(map[string]bool, len(matching))
	var deduped []config.HookConfig
	for _, h := range matching {
		if seen[h.Command] {
			continue
		}
		seen[h.Command] = true
		deduped = append(deduped, h)
	}

	envVars := BuildEnv(eventName, toolName, sessionID, r.cwd, r.projectDir, toolInputJSON)
	payload := BuildPayload(eventName, sessionID, r.cwd, toolName, toolInputJSON)

	results := make([]HookResult, len(deduped))
	var wg sync.WaitGroup
	wg.Add(len(deduped))

	for i, h := range deduped {
		go func(idx int, hook config.HookConfig) {
			defer wg.Done()
			results[idx] = r.runOne(ctx, hook, envVars, payload)
		}(i, h)
	}
	wg.Wait()

	agg := aggregate(results, toolInputJSON)
	agg.Hooks = make([]HookInfo, len(deduped))
	for i, h := range deduped {
		agg.Hooks[i] = HookInfo{
			Name:         h.DisplayName(),
			Matcher:      h.Matcher,
			Decision:     results[i].Decision.String(),
			Halt:         results[i].Halt,
			Reason:       results[i].Reason,
			InputRewrite: results[i].UpdatedInput != "",
		}
	}
	slog.Info(
		"Hook completed",
		"event", eventName,
		"tool", toolName,
		"hooks", len(deduped),
		"decision", agg.Decision.String(),
	)
	return agg, nil
}

// matchingHooks returns the hooks registered for the event whose matcher
// matches the tool name (or has no matcher, which matches everything). For
// events without a tool, the matcher is tested against an empty name, so a
// matcher-bearing hook does not fire for them.
func (r *Runner) matchingHooks(eventName, toolName string) []config.HookConfig {
	registered := r.hooksByEvent[eventName]
	matched := make([]config.HookConfig, 0, len(registered))
	for _, h := range registered {
		if h.matcher == nil || h.matcher.MatchString(toolName) {
			matched = append(matched, h.cfg)
		}
	}
	return matched
}

// runOne executes a single hook command and returns its result.
//
// Execution goes through Crush's embedded POSIX shell (shell.Run) so the
// same interpreter, builtins, and coreutils are visible to hooks as to
// the bash tool. BlockFuncs are intentionally omitted: hooks are
// user-authored config that carry the same trust as a shell alias.
//
// A hook that fails to yield after its deadline has passed is abandoned
// after abandonGrace so the caller never blocks longer than
// timeout + abandonGrace. Ownership of the stdout and stderr buffers is
// strictly single-goroutine:
//   - before receiving from `done`, only the goroutine writes to them;
//   - after `done` delivers a value, the goroutine is finished and the
//     outer frame reads them;
//   - on the abandon path, the goroutine may still be writing and the
//     outer frame must not touch them again.
func (r *Runner) runOne(parentCtx context.Context, hook config.HookConfig, envVars []string, payload []byte) HookResult {
	timeout := hook.TimeoutDuration()
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runShell(ctx, shell.RunOptions{
			Command: hook.Command,
			Cwd:     r.cwd,
			Env:     envVars,
			Stdin:   bytes.NewReader(payload),
			Stdout:  &stdout,
			Stderr:  &stderr,
		})
	}()

	var err error
	select {
	case err = <-done:
		// Normal path: goroutine has finished, buffers are safe to read.
	case <-ctx.Done():
		select {
		case err = <-done:
			// Interpreter yielded within the grace period; safe to read.
		case <-time.After(abandonGrace):
			slog.Warn(
				"Hook did not yield after cancel; abandoning goroutine",
				"command", hook.Command,
				"timeout", timeout,
			)
			// The goroutine may still be writing to stdout/stderr; do
			// not read either buffer below this point.
			return HookResult{Decision: DecisionNone}
		}
	}

	if shell.IsInterrupt(err) {
		// Distinguish timeout from parent cancellation.
		if parentCtx.Err() != nil {
			slog.Debug("Hook cancelled by parent context", "command", hook.Command)
		} else {
			slog.Warn("Hook timed out", "command", hook.Command, "timeout", timeout)
		}
		return HookResult{Decision: DecisionNone}
	}

	if err != nil {
		exitCode := shell.ExitCode(err)
		switch exitCode {
		case 2:
			// Exit code 2 = block this tool call. Stderr is the reason.
			reason := strings.TrimSpace(stderr.String())
			if reason == "" {
				reason = "blocked by hook"
			}
			return HookResult{
				Decision: DecisionDeny,
				Reason:   reason,
			}
		case HaltExitCode:
			// Exit code 49 = halt the whole turn. Stderr is the reason.
			reason := strings.TrimSpace(stderr.String())
			if reason == "" {
				reason = "turn halted by hook"
			}
			return HookResult{
				Decision: DecisionDeny,
				Halt:     true,
				Reason:   reason,
			}
		default:
			// Other non-zero exits are non-blocking errors.
			slog.Warn(
				"Hook failed with non-blocking error",
				"command", hook.Command,
				"exit_code", exitCode,
				"stderr", strings.TrimSpace(stderr.String()),
				"error", err,
			)
			return HookResult{Decision: DecisionNone}
		}
	}

	// Exit code 0 — parse stdout JSON.
	result := parseStdout(stdout.String())
	slog.Debug(
		"Hook executed",
		"command", hook.Command,
		"decision", result.Decision.String(),
	)
	return result
}
