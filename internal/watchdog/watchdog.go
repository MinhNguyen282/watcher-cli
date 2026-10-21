package watchdog

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/nxadm/tail"
)

// Watchdog runs a command or tails a log file, scanning the output for
// error signatures and reporting findings through OnFinding.
type Watchdog struct {
	// Command, when non-empty, is run (Command[0] with the rest as
	// arguments) and its merged output is scanned.
	Command []string
	// LogFile, when set, is tailed instead of running a command.
	LogFile string
	// WorkDir is the directory the command runs in.
	WorkDir string
	// Env, when non-nil, is the environment for the command. Nil means
	// the current process environment.
	Env []string
	// Stderr writes live command output for debugging.
	Stderr io.Writer

	Detector *Detector
	// OnFinding receives every detected finding. It is called from the
	// scan loop; keep it fast or spawn work inside it.
	OnFinding func(Finding)
}

// Run executes one watch cycle and returns when the command exits (or
// the tail stops), the context is canceled, or OnFinding is nil.
func (w *Watchdog) Run(ctx context.Context) error {
	det := w.Detector
	if det == nil {
		det = DefaultDetector()
	}

	if w.LogFile != "" {
		return w.runTail(ctx, det)
	}
	if len(w.Command) == 0 {
		return fmt.Errorf("watchdog: no command or log file configured")
	}

	cmd := exec.CommandContext(ctx, w.Command[0], w.Command[1:]...)
	cmd.Dir = w.WorkDir
	if w.Env != nil {
		cmd.Env = w.Env
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("watchdog: failed to open stdout: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("watchdog: failed to start command: %w", err)
	}

	w.scan(ctx, stdout, det)

	if finding := det.Flush(); finding != nil {
		w.report(*finding)
	}

	return cmd.Wait() //nolint:wrapcheck // exit codes are reported by the caller
}

// runTail follows a log file, mirroring the crush logs command.
func (w *Watchdog) runTail(ctx context.Context, det *Detector) error {
	t, err := tail.TailFile(w.LogFile, tail.Config{
		Follow:   true,
		ReOpen:   true,
		Location: &tail.SeekInfo{Offset: 0, Whence: io.SeekEnd},
		Logger:   tail.DiscardingLogger,
	})
	if err != nil {
		return fmt.Errorf("watchdog: failed to tail %s: %w", w.LogFile, err)
	}
	defer t.Stop()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			//nolint:errcheck // best-effort stop on cancel
			t.Stop()
		case <-done:
		}
	}()

	for {
		select {
		case line, ok := <-t.Lines:
			if !ok {
				return nil
			}
			if line.Err != nil {
				continue
			}
			if finding := det.Scan(line.Text); finding != nil {
				w.report(*finding)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// scan feeds a reader line by line into the detector until EOF or
// context cancellation.
func (w *Watchdog) scan(ctx context.Context, r io.Reader, det *Detector) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Text()
		if w.Stderr != nil {
			fmt.Fprintln(w.Stderr, line)
		}
		if finding := det.Scan(line); finding != nil {
			w.report(*finding)
		}
	}
}

func (w *Watchdog) report(f Finding) {
	if w.OnFinding != nil {
		w.OnFinding(f)
	}
}

// AnalysisPrompt renders the finding into the prompt used by the
// analysis LLM.
func AnalysisPrompt(f Finding, workingDir string) string {
	return fmt.Sprintf(`You are debugging a failing run in %[1]s.

Detected problem (%[2]s, severity %[3]s):

%s

Relevant log context:

%s

Analyze the root cause and propose a concrete fix. If you can identify
the exact code to change, provide a unified diff. Keep the explanation
short and actionable.`, workingDir, f.Kind, f.Severity, f.Message, f.Context)
}

// FixPrompt renders the finding into the prompt handed to the coder
// agent when --fix is used.
func FixPrompt(f Finding, workingDir string) string {
	var locs string
	for _, l := range f.Locations {
		locs += fmt.Sprintf("- %s:%d\n", l.File, l.Line)
	}
	if locs == "" {
		locs = "(no source location extracted)\n"
	}
	return fmt.Sprintf(`A watched command in %[1]s just failed with the following output:

%s

Relevant log context:

%s

Suspected source locations:
%s
Fix the root cause in the code. After editing, run the relevant tests or build to verify the fix, and summarize what you changed.`,
		workingDir, f.Message, f.Context, locs)
}
