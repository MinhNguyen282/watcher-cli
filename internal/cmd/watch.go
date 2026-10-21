package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/harness"
	"github.com/charmbracelet/crush/internal/watchdog"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/spf13/cobra"
	"mvdan.cc/sh/v3/shell"
)

var watchCmd = &cobra.Command{
	Use:   "watch [command...]",
	Short: "Watch a command or log file for failures and fix them",
	Long: `Run a command (or tail a log file) and watch its output for error
signatures: panics, stack traces, test failures, and build errors.
Every finding is reported with its source locations. With --analyze the
finding is sent to your model for a root-cause analysis and a proposed
patch. With --fix the coder agent applies the fix in your repository and
runs the tests.

Examples:
  crush watch "go test ./..."
  crush watch --fix "npm test"
  crush watch --log ./app.log
  crush watch --interval 30s "go test ./..."`,
	Args: cobra.ArbitraryArgs,
	RunE: runWatchCmd,
}

func init() {
	watchCmd.Flags().String("log", "", "Tail a log file instead of running a command")
	watchCmd.Flags().Bool("analyze", false, "Send each finding to the model for root-cause analysis")
	watchCmd.Flags().Bool("fix", false, "Have the coder agent fix each finding and run the tests")
	watchCmd.Flags().Duration("interval", 0, "Re-run the command every interval after it exits")
}

func runWatchCmd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return err
	}
	logFile, _ := cmd.Flags().GetString("log")
	analyze, _ := cmd.Flags().GetBool("analyze")
	fix, _ := cmd.Flags().GetBool("fix")
	interval, _ := cmd.Flags().GetDuration("interval")

	if logFile == "" && len(args) == 0 {
		return fmt.Errorf("provide a command to watch or use --log <file>")
	}

	var command []string
	if logFile == "" {
		command, err = parseCommand(args)
		if err != nil {
			return err
		}
	}

	reporter, err := newWatchReporter(ctx, cmd, cwd, analyze, fix)
	if err != nil {
		return err
	}

	w := &watchdog.Watchdog{
		Command:  command,
		LogFile:  logFile,
		WorkDir:  cwd,
		Stderr:   os.Stderr,
		Detector: watchdog.DefaultDetector(),
		OnFinding: func(f watchdog.Finding) {
			reporter.Report(f)
		},
	}

	for {
		runErr := w.Run(ctx)
		if logFile != "" {
			// Tail mode runs until canceled.
			return runErr
		}
		if ctx.Err() != nil {
			return nil
		}
		if interval <= 0 {
			// Single-run mode: mirror the command's exit code.
			return runErr
		}
		fmt.Fprintf(os.Stderr, "[watchdog] command exited; re-running in %s\n", interval)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// parseCommand splits a single command string on shell rules, and
// passes multiple arguments through verbatim.
func parseCommand(args []string) ([]string, error) {
	if len(args) > 1 {
		return args, nil
	}
	if !strings.ContainsAny(args[0], " \t\"'") {
		return []string{args[0]}, nil
	}
	fields, err := shell.Fields(args[0], nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse command %q: %w", args[0], err)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return fields, nil
}

// watchReporter prints findings and optionally runs the analysis/fix
// flows.
type watchReporter struct {
	ctx     context.Context
	cmd     *cobra.Command
	cwd     string
	analyze bool
	fix     bool
	// store backs the standalone analysis LLM; built lazily.
	store *config.ConfigStore
	// fixStarted guards against concurrent fixes while one is running.
	fixStarted bool
}

func newWatchReporter(ctx context.Context, cmd *cobra.Command, cwd string, analyze, fix bool) (*watchReporter, error) {
	return &watchReporter{ctx: ctx, cmd: cmd, cwd: cwd, analyze: analyze, fix: fix}, nil
}

func (r *watchReporter) Report(f watchdog.Finding) {
	fmt.Fprintf(os.Stderr, "[watchdog] detected %s: %s\n", f.Kind, f.Message)
	for _, loc := range f.Locations {
		fmt.Fprintf(os.Stderr, "[watchdog]   at %s:%d\n", loc.File, loc.Line)
	}

	if r.analyze {
		analysis, err := r.runAnalysis(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[watchdog] analysis failed: %v\n", err)
		} else {
			fmt.Fprintln(os.Stdout, analysis)
		}
	}

	if r.fix {
		if err := r.runFix(f); err != nil {
			fmt.Fprintf(os.Stderr, "[watchdog] fix failed: %v\n", err)
		}
	}
}

// runAnalysis asks the standalone LLM for a root-cause analysis.
func (r *watchReporter) runAnalysis(f watchdog.Finding) (string, error) {
	store, err := r.configStore()
	if err != nil {
		return "", err
	}
	agent, err := harness.NewChatAgent(r.ctx, store,
		"You are an expert debugging assistant. You receive error output from a failing run and produce a root-cause analysis with a concrete patch proposal.")
	if err != nil {
		return "", err
	}
	return harness.ChatOnce(r.ctx, agent, watchdog.AnalysisPrompt(f, r.cwd))
}

// runFix hands the finding to the coder agent in a fresh session.
func (r *watchReporter) runFix(f watchdog.Finding) error {
	if r.fixStarted {
		fmt.Fprintln(os.Stderr, "[watchdog] a fix is already running; skipping this finding")
		return nil
	}
	r.fixStarted = true
	defer func() { r.fixStarted = false }()

	ws, cleanup, err := setupLocalWorkspace(r.cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	appWs, ok := ws.(*workspace.AppWorkspace)
	if !ok {
		return fmt.Errorf("watch --fix requires local mode")
	}

	sess, err := ws.CreateSession(r.ctx, "watchdog-fix")
	if err != nil {
		return fmt.Errorf("failed to create fix session: %w", err)
	}

	fmt.Fprintln(os.Stderr, "[watchdog] fixing with the coder agent...")
	return appWs.App().RunNonInteractive(r.ctx, os.Stdout,
		watchdog.FixPrompt(f, r.cwd), "", "", "", "", true, sess.ID, false)
}

// configStore lazily loads the configuration for the standalone
// analysis LLM.
func (r *watchReporter) configStore() (*config.ConfigStore, error) {
	if r.store != nil {
		return r.store, nil
	}
	dataDir, _ := r.cmd.Flags().GetString("data-dir")
	debug, _ := r.cmd.Flags().GetBool("debug")
	store, err := config.Init(r.ctx, r.cwd, dataDir, debug)
	if err != nil {
		return nil, err
	}
	if !store.Config().IsConfigured() {
		return nil, fmt.Errorf("no providers configured - please run 'crush' to set up a provider interactively")
	}
	r.store = store
	return store, nil
}
