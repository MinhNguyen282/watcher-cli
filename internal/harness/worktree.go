package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is one isolated probe sandbox backed by `git worktree`.
type Worktree struct {
	ID     string
	Dir    string
	Branch string
}

// WorktreeManager creates and removes git worktrees used to isolate
// probe runs from each other and from the user's working tree. All
// operations go through the git CLI.
type WorktreeManager interface {
	// Add creates a new worktree on a fresh branch starting at
	// baseRef (a branch, tag, or commit).
	Add(ctx context.Context, id, baseRef string) (*Worktree, error)
	// Remove deletes the worktree. When keepBranch is true the branch
	// is left behind (used for a winner the caller could not apply).
	Remove(ctx context.Context, wt *Worktree, keepBranch bool) error
	// Commit records the probe's changes on its branch. Only already
	// tracked files are committed, so harness artifacts under
	// .watcher/ stay out of branch history. It is a no-op when the
	// probe made no changes.
	Commit(ctx context.Context, wt *Worktree, message string) error
	// Diff returns the uncommitted diff of a worktree.
	Diff(ctx context.Context, wt *Worktree) (string, error)
	// ApplyDiff applies a diff to the repository's main working tree.
	ApplyDiff(ctx context.Context, diff string) (bool, error)
}

// gitWorktrees implements WorktreeManager against a real repository.
type gitWorktrees struct {
	repoDir string
}

// NewWorktreeManager returns a manager for the given repository
// directory, or an error when it is not a git repository.
func NewWorktreeManager(repoDir string) (WorktreeManager, error) {
	ctx := context.Background()
	if repoDir == "" {
		return nil, fmt.Errorf("repository directory is empty")
	}
	if err := runGit(ctx, repoDir, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%q is not a git repository: %w", repoDir, err)
	}
	if err := runGit(ctx, repoDir, "rev-parse", "HEAD"); err != nil {
		return nil, fmt.Errorf("repository has no commits: %w", err)
	}
	return &gitWorktrees{repoDir: repoDir}, nil
}

func (g *gitWorktrees) Add(ctx context.Context, id, baseRef string) (*Worktree, error) {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	branch := "crush-harness-" + sanitizeFileName(id)
	if branch == "crush-harness-" {
		branch = "crush-harness-probe"
	}
	// Append a short random suffix so repeated runs never collide with
	// branches left behind by a previous failed run.
	branch += "-" + shortRandom()

	dir := filepath.Join(os.TempDir(), "crush-harness-"+sanitizeFileName(id)+"-"+shortRandom())
	if err := runGit(ctx, g.repoDir, "worktree", "add", "-b", branch, dir, baseRef); err != nil {
		return nil, fmt.Errorf("failed to create worktree %s: %w", dir, err)
	}
	return &Worktree{ID: id, Dir: dir, Branch: branch}, nil
}

func (g *gitWorktrees) Remove(ctx context.Context, wt *Worktree, keepBranch bool) error {
	if wt == nil {
		return nil
	}
	var errs []error
	if wt.Dir != "" {
		if err := runGit(ctx, g.repoDir, "worktree", "remove", "--force", wt.Dir); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove worktree %s: %w", wt.Dir, err))
		}
	}
	if !keepBranch && wt.Branch != "" {
		if err := runGit(ctx, g.repoDir, "branch", "-D", wt.Branch); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete branch %s: %w", wt.Branch, err))
		}
	}
	return errors.Join(errs...)
}

func (g *gitWorktrees) Diff(ctx context.Context, wt *Worktree) (string, error) {
	if wt == nil || wt.Dir == "" {
		return "", fmt.Errorf("worktree has no directory")
	}
	out, err := runGitOut(ctx, wt.Dir, "diff")
	if err != nil {
		return "", fmt.Errorf("failed to diff worktree %s: %w", wt.Dir, err)
	}
	return out, nil
}

func (g *gitWorktrees) Commit(ctx context.Context, wt *Worktree, message string) error {
	if wt == nil || wt.Dir == "" {
		return nil
	}
	// Stage changes to tracked files only: harness artifacts under
	// .watcher/ are untracked and must stay out of branch history.
	if err := runGit(ctx, wt.Dir, "add", "-u"); err != nil {
		return fmt.Errorf("failed to stage probe changes: %w", err)
	}
	err := runGit(ctx, wt.Dir, "commit", "--no-verify", "-m", message)
	if err == nil {
		return nil
	}
	// "Nothing to commit" is the expected outcome for a probe that
	// made no code changes; everything else is a real failure.
	if strings.Contains(err.Error(), "nothing to commit") {
		return nil
	}
	// Retry with a machine identity so the bookkeeping commit works
	// even when the user has not configured a git identity.
	retryErr := runGit(ctx, wt.Dir,
		"-c", "user.name=Crush Harness", "-c", "user.email=harness@crush.local",
		"commit", "--no-verify", "-m", message)
	if retryErr == nil {
		return nil
	}
	return fmt.Errorf("failed to commit probe changes: %w", err)
}

func (g *gitWorktrees) ApplyDiff(ctx context.Context, diff string) (bool, error) {
	if strings.TrimSpace(diff) == "" {
		return true, nil
	}
	// `git apply` refuses to touch a dirty tree it cannot safely
	// patch; the caller keeps the winner branch in that case so the
	// user can merge it manually.
	if err := runGitStdin(ctx, g.repoDir, diff, "apply", "--whitespace=nowarn", "-"); err != nil {
		return false, nil
	}
	return true, nil
}

// runGit executes a git command in dir, discarding output.
func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := runGitOut(ctx, dir, args...)
	return err
}

// runGitOut executes a git command in dir and returns stdout.
func runGitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "),
				strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runGitStdin executes a git command in dir feeding stdin.
func runGitStdin(ctx context.Context, dir, stdin string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "),
			strings.TrimSpace(string(out)))
	}
	return nil
}
