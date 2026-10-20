package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/harness"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/spf13/cobra"
)

// runEvolveCmd runs the self-evolving harness pipeline for
// `crush run --evolve`. It does not initialize a full workspace: the
// optimizer only needs the repository (for worktrees), the user's
// config (for the editor LLM), and the crush binary (for the probe
// subprocesses).
func runEvolveCmd(ctx context.Context, cmd *cobra.Command, prompt string, k int, mode string, applyWinner bool) error {
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return err
	}
	// A git repository is the hard precondition for worktree-based
	// probing; fail fast before any config/provider work.
	if _, err := harness.NewWorktreeManager(cwd); err != nil {
		return fmt.Errorf("--evolve requires a git repository: %w", err)
	}
	dataDir, _ := cmd.Flags().GetString("data-dir")
	debug, _ := cmd.Flags().GetBool("debug")

	store, err := config.Init(cwd, dataDir, debug)
	if err != nil {
		return err
	}
	if !store.Config().IsConfigured() {
		return fmt.Errorf("no providers configured - please run 'crush' to set up a provider interactively")
	}

	return runEvolve(ctx, cwd, store, prompt, k, mode, applyWinner)
}

// runEvolve wires the harness optimizer to the crush binary and the
// user's LLM config, then runs one evolve cycle.
func runEvolve(ctx context.Context, workingDir string, store *config.ConfigStore, prompt string, k int, mode string, applyWinner bool) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[harness] "+format+"\n", args...)
	}

	wtm, err := harness.NewWorktreeManager(workingDir)
	if err != nil {
		return fmt.Errorf("--evolve requires a git repository: %w", err)
	}

	runner := newSubprocessProbeRunner()
	editor := harness.NewChatEditor(harness.ChatFunc(func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		agent, err := harness.NewChatAgent(ctx, store, systemPrompt)
		if err != nil {
			return "", err
		}
		return harness.ChatOnce(ctx, agent, userPrompt)
	}))

	optimizer := &harness.Optimizer{
		Store:     harness.NewStore(workingDir),
		Worktrees: wtm,
		Editor:    editor,
		Runner:    runner,
	}

	opts := harness.DefaultRunOptions()
	opts.Probes = k
	opts.Mode = harness.Mode(mode)
	opts.ApplyWinner = applyWinner
	opts.Log = logf

	logf("Starting evolve cycle: %d candidate(s), mode=%s", k, mode)
	result, err := optimizer.Run(ctx, prompt, opts)
	if err != nil {
		return fmt.Errorf("evolve failed: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\n== Evolve complete ==")
	fmt.Fprint(os.Stderr, result.Summary())
	if result.Best != nil && result.Best.Trace.FinalOutput != "" {
		fmt.Fprintln(os.Stdout, result.Best.Trace.FinalOutput)
	}
	fmt.Fprintln(os.Stderr, "\nHint: add .watcher/ to .gitignore if you want to keep harness state local.")
	return nil
}

// subprocessProbeRunner implements harness.ProbeRunner by spawning
// the crush binary as a subprocess in a probe worktree. Every probe
// gets its own data directory so its message history is isolated and
// recoverable for trace extraction.
type subprocessProbeRunner struct {
	exe  string
	host string
	env  []string
}

func newSubprocessProbeRunner() *subprocessProbeRunner {
	exe, err := os.Executable()
	if err != nil {
		exe = "crush"
	}
	return &subprocessProbeRunner{
		exe:  exe,
		host: clientHost,
		env:  os.Environ(),
	}
}

// RunProbe implements harness.ProbeRunner.
func (r *subprocessProbeRunner) RunProbe(ctx context.Context, spec harness.ProbeSpec) (*harness.ExecutionTrace, error) {
	startedAt := time.Now()
	dataDir := filepath.Join(os.TempDir(), "crush-harness-"+sanitizeProbeID(spec.ID))
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create probe data directory: %w", err)
	}
	defer os.RemoveAll(dataDir)

	// Persist the candidate harness into the probe worktree so the
	// subprocess agent picks it up through its system prompt, and
	// materialize the helper scripts.
	if spec.Harness != nil {
		probeStore := harness.NewStore(spec.WorkDir)
		if err := probeStore.SaveHarness(spec.Harness); err != nil {
			return nil, fmt.Errorf("failed to persist probe harness: %w", err)
		}
		if err := probeStore.WriteScripts(spec.Harness); err != nil {
			return nil, fmt.Errorf("failed to write probe scripts: %w", err)
		}
	}

	args := []string{"--cwd", spec.WorkDir, "--data-dir", dataDir}
	if r.host != "" && r.host != server.DefaultHost() {
		args = append(args, "--host", r.host)
	}
	args = append(args, "run", "--quiet", "--", spec.Task)

	cmd := exec.CommandContext(ctx, r.exe, args...)
	cmd.Env = r.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	trace := harness.ExtractTrace(
		ctx, dataDir, spec, stdout.String(), stderr.String(),
		startedAt, time.Since(startedAt), runErr,
	)
	return trace, nil
}

// sanitizeProbeID maps a probe ID to a safe directory suffix.
func sanitizeProbeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, ch := range id {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			out = append(out, ch)
		case ch == '-' || ch == '_':
			out = append(out, ch)
		}
	}
	if len(out) == 0 {
		return "probe"
	}
	return string(out)
}
