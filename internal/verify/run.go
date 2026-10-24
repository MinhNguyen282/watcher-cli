package verify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeout bounds one check.
const DefaultTimeout = 120 * time.Second

// MaxTimeout is the largest accepted per-check timeout.
const MaxTimeout = 10 * time.Minute

// CheckResult is the outcome of one command.
type CheckResult struct {
	Name     string
	Argv     []string
	ExitCode int
	Duration time.Duration
	Output   string
	TimedOut bool
}

// Result is the outcome of a full verification run.
type Result struct {
	Profile  Profile
	Checks   []CheckResult
	Failures []Failure
	Duration time.Duration
}

// Passed reports whether every check exited successfully.
func (r Result) Passed() bool {
	for _, check := range r.Checks {
		if check.ExitCode != 0 || check.TimedOut {
			return false
		}
	}
	return true
}

// Run executes the profile's checks sequentially. The timeout applies to
// each check.
func Run(ctx context.Context, profile Profile, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timeout = min(timeout, MaxTimeout)

	start := time.Now()
	result := Result{Profile: profile}
	for _, check := range profile.Checks {
		if err := ctx.Err(); err != nil {
			break
		}
		checkResult := runCheck(ctx, profile.Root, check, timeout)
		result.Checks = append(result.Checks, checkResult)
		if checkResult.ExitCode != 0 || checkResult.TimedOut {
			result.Failures = append(result.Failures, ParseFailures(profile.Kind, checkResult.Output)...)
		}
	}
	result.Failures = bounded(dedupeFailures(result.Failures), 12)
	result.Duration = time.Since(start)
	return result
}

func runCheck(ctx context.Context, root string, check Check, timeout time.Duration) CheckResult {
	result := CheckResult{Name: check.Name, Argv: check.Argv}
	if len(check.Argv) == 0 {
		result.ExitCode = -1
		result.Output = "empty command"
		return result
	}

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(checkCtx, check.Argv[0], check.Argv[1:]...)
	cmd.Dir = filepath.Join(root, filepath.FromSlash(check.Dir))
	var output tailBuffer
	output.max = 64 << 10
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	result.Duration = time.Since(start)
	result.Output = output.String()
	result.TimedOut = errors.Is(checkCtx.Err(), context.DeadlineExceeded)

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			if result.Output == "" {
				result.Output = err.Error()
			}
		}
	}
	if result.TimedOut {
		result.Output += fmt.Sprintf("\n[check timed out after %s]", timeout)
	}
	return result
}

// tailBuffer keeps the last max bytes of command output, which is where
// test runners print their summaries.
type tailBuffer struct {
	max       int
	data      []byte
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		b.max = 64 << 10
	}
	if len(p) >= b.max {
		b.data = append(b.data[:0], p[len(p)-b.max:]...)
		b.truncated = true
		return len(p), nil
	}
	if len(b.data)+len(p) > b.max {
		drop := len(b.data) + len(p) - b.max
		b.data = append(b.data[:0], b.data[drop:]...)
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	text := strings.TrimSpace(string(b.data))
	if !b.truncated {
		return text
	}
	return fmt.Sprintf("[output truncated to the last %d bytes]\n%s", b.max, text)
}
