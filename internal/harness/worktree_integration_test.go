package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWorktreeManagerIntegration exercises the real git plumbing
// against a scratch repository: worktree creation, diffing, the
// bookkeeping commit, applying the diff back to the main tree, and
// cleanup.
func TestWorktreeManagerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping git integration test in short mode")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}

	git("init", "-q")
	git("config", "user.name", "Harness Test")
	git("config", "user.email", "harness-test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644))
	git("add", "a.txt")
	git("commit", "-q", "-m", "init")

	mgr, err := NewWorktreeManager(repo)
	require.NoError(t, err)

	ctx := context.Background()
	wt, err := mgr.Add(ctx, "itest", "HEAD")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = mgr.Remove(context.Background(), wt, false)
	})

	// Edit inside the worktree and commit the probe's change.
	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "a.txt"), []byte("two\n"), 0o644))
	diff, err := mgr.Diff(ctx, wt)
	require.NoError(t, err)
	require.Contains(t, diff, "+two")
	require.NoError(t, mgr.Commit(ctx, wt, "crush-harness: probe itest"))

	// The main tree is untouched until the diff is applied.
	data, err := os.ReadFile(filepath.Join(repo, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "one\n", string(data))

	applied, err := mgr.ApplyDiff(ctx, diff)
	require.NoError(t, err)
	require.True(t, applied)
	data, err = os.ReadFile(filepath.Join(repo, "a.txt"))
	require.NoError(t, err)
	// With core.autocrlf in effect, git may have normalized the
	// line endings on write; accept both forms.
	require.True(t, string(data) == "two\n" || string(data) == "two\r\n",
		"unexpected content %q", string(data))

	// Cleanup removes the worktree and its branch.
	require.NoError(t, mgr.Remove(ctx, wt, false))
	cmd := exec.Command("git", "rev-parse", "--verify", wt.Branch)
	cmd.Dir = repo
	_, err = cmd.CombinedOutput()
	require.Error(t, err, "branch should have been deleted")
}

// TestWorktreeManagerRejectsNonRepo verifies the constructor's
// guardrails.
func TestWorktreeManagerRejectsNonRepo(t *testing.T) {
	t.Parallel()
	_, err := NewWorktreeManager(t.TempDir())
	require.Error(t, err)
}

// TestWorktreeManagerKeepsBranchOnRequest verifies that a kept branch
// survives worktree removal.
func TestWorktreeManagerKeepsBranchOnRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping git integration test in short mode")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	git("init", "-q")
	git("config", "user.name", "Harness Test")
	git("config", "user.email", "harness-test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644))
	git("add", "a.txt")
	git("commit", "-q", "-m", "init")

	mgr, err := NewWorktreeManager(repo)
	require.NoError(t, err)
	ctx := context.Background()
	wt, err := mgr.Add(ctx, "keep", "HEAD")
	require.NoError(t, err)

	require.NoError(t, mgr.Remove(ctx, wt, true))
	git("rev-parse", "--verify", wt.Branch)
}
