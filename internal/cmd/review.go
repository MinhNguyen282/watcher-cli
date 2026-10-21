package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/harness"
	"github.com/charmbracelet/crush/internal/review"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/spf13/cobra"
)

var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Review local changes before pushing",
	Long: `Review uncommitted, staged, or branch changes for bugs, leaked
secrets, missing error handling, and convention violations. A static
checklist always runs; when a provider is configured the diff is also
sent to your model, informed by the lessons learned in
.watcher/memory/lessons.md.

Examples:
  crush review                 # review uncommitted changes
  crush review --staged        # review staged changes
  crush review --base main     # review main...HEAD
  crush review --fix           # have the coder agent fix the findings`,
	Args: cobra.NoArgs,
	RunE: runReviewCmd,
}

func init() {
	reviewCmd.Flags().Bool("staged", false, "Review staged changes instead of the working tree")
	reviewCmd.Flags().String("base", "", "Review changes between this ref and HEAD (e.g. main)")
	reviewCmd.Flags().Bool("fix", false, "Have the coder agent fix high-severity findings")
	reviewCmd.Flags().String("format", "pretty", "Output format: pretty, json, or markdown")
}

func runReviewCmd(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return err
	}
	staged, _ := cmd.Flags().GetBool("staged")
	base, _ := cmd.Flags().GetString("base")
	fix, _ := cmd.Flags().GetBool("fix")
	format, _ := cmd.Flags().GetString("format")

	diff, err := reviewDiff(cwd, staged, base)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		fmt.Fprintln(os.Stderr, "Nothing to review.")
		return nil
	}

	var findings = []review.Finding{}
	findings = append(findings, review.DefaultChecker().Scan(diff)...)

	// The LLM pass is best-effort: without a configured provider the
	// static findings still print.
	llmFindings, llmErr := runLLMReview(ctx, cmd, cwd, diff)
	if llmErr != nil {
		fmt.Fprintf(os.Stderr, "[review] model review skipped: %v\n", llmErr)
	} else {
		findings = append(findings, llmFindings...)
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
		}
		return findings[i].File < findings[j].File
	})

	switch format {
	case "json":
		data, err := json.MarshalIndent(findings, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(data))
	case "markdown":
		printFindingsMarkdown(findings)
	default:
		printFindingsPretty(findings)
	}

	if fix {
		if err := runReviewFix(ctx, cmd, cwd, findings, diff); err != nil {
			return err
		}
	}
	return nil
}

// reviewDiff resolves the diff for the requested mode.
func reviewDiff(cwd string, staged bool, base string) (string, error) {
	var args []string
	switch {
	case staged:
		args = []string{"diff", "--cached"}
	case base != "":
		args = []string{"diff", base + "...HEAD"}
	default:
		args = []string{"diff"}
	}
	out, err := runGitOutCmd(cwd, args...)
	if err != nil {
		return "", fmt.Errorf("failed to read diff: %w", err)
	}
	return out, nil
}

// runGitOutCmd runs git in dir and returns stdout.
func runGitOutCmd(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "),
				strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// runLLMReview sends the diff to the configured model together with
// the repository's learned lessons.
func runLLMReview(ctx context.Context, cmd *cobra.Command, cwd, diff string) ([]review.Finding, error) {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	debug, _ := cmd.Flags().GetBool("debug")
	store, err := config.Init(ctx, cwd, dataDir, debug)
	if err != nil {
		return nil, err
	}
	if !store.Config().IsConfigured() {
		return nil, fmt.Errorf("no providers configured")
	}

	lessons, err := harness.NewStore(cwd).LoadLessons()
	if err != nil {
		lessons = ""
	}

	agent, err := harness.NewChatAgent(ctx, store,
		"You are a senior code reviewer. Your findings are precise, actionable, and free of speculation.")
	if err != nil {
		return nil, err
	}
	response, err := harness.ChatOnce(ctx, agent, review.BuildPrompt(diff, lessons))
	if err != nil {
		return nil, err
	}
	return review.ParseFindings(response)
}

// runReviewFix hands the findings to the coder agent.
func runReviewFix(ctx context.Context, cmd *cobra.Command, cwd string, findings []review.Finding, diff string) error {
	var actionable []review.Finding
	for _, f := range findings {
		if severityRank(f.Severity) <= severityRank(review.SeverityHigh) {
			actionable = append(actionable, f)
		}
	}
	if len(actionable) == 0 {
		fmt.Fprintln(os.Stderr, "[review] No high-severity findings to fix.")
		return nil
	}

	ws, cleanup, err := setupLocalWorkspace(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	appWs, ok := ws.(*workspace.AppWorkspace)
	if !ok {
		return fmt.Errorf("review --fix requires local mode")
	}

	sess, err := ws.CreateSession(ctx, "review-fix")
	if err != nil {
		return fmt.Errorf("failed to create fix session: %w", err)
	}

	prompt := buildFixPrompt(actionable, diff)
	fmt.Fprintln(os.Stderr, "[review] fixing with the coder agent...")
	return appWs.App().RunNonInteractive(ctx, os.Stdout, prompt, "", "", "", "", true, sess.ID, false)
}

func buildFixPrompt(findings []review.Finding, diff string) string {
	var sb strings.Builder
	sb.WriteString("Fix the following review findings in the code:\n")
	for _, f := range findings {
		fmt.Fprintf(&sb, "- [%s] %s:%d (%s): %s\n", f.Severity, f.File, f.Line, f.Category, f.Message)
		if f.Suggestion != "" {
			fmt.Fprintf(&sb, "  suggestion: %s\n", f.Suggestion)
		}
	}
	sb.WriteString("\nThe changes being reviewed:\n```diff\n")
	sb.WriteString(diff)
	sb.WriteString("\n```\n")
	sb.WriteString("After editing, run the relevant tests or build to verify, and summarize what you changed.")
	return sb.String()
}

func severityRank(s review.Severity) int {
	switch s {
	case review.SeverityCritical:
		return 0
	case review.SeverityHigh:
		return 1
	case review.SeverityMedium:
		return 2
	default:
		return 3
	}
}

func printFindingsPretty(findings []review.Finding) {
	if len(findings) == 0 {
		fmt.Fprintln(os.Stdout, "No findings.")
		return
	}
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		fmt.Fprintf(os.Stdout, "[%s] %s (%s) %s\n", strings.ToUpper(string(f.Severity)), f.Message, f.Source, loc)
		if f.Suggestion != "" {
			fmt.Fprintf(os.Stdout, "  → %s\n", f.Suggestion)
		}
	}
}

func printFindingsMarkdown(findings []review.Finding) {
	if len(findings) == 0 {
		fmt.Fprintln(os.Stdout, "No findings.")
		return
	}
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, f.Line)
		}
		fmt.Fprintf(os.Stdout, "- **[%s]** %s (`%s`, %s)\n", strings.ToUpper(string(f.Severity)), f.Message, loc, f.Source)
		if f.Suggestion != "" {
			fmt.Fprintf(os.Stdout, "  - %s\n", f.Suggestion)
		}
	}
}
