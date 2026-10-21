package shadowgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// run runs a git command in dir, failing the test on error.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
	return string(out)
}

// writeFile writes data to path relative to dir.
func writeFile(t *testing.T, dir, rel, data string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	require.NoError(t, err)
	return string(data)
}

// newTestRepo creates a git repository with one committed file.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "--initial-branch=main")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "user.email", "test@example.com")
	writeFile(t, dir, "hello.txt", "v1\n")
	run(t, dir, "add", "hello.txt")
	run(t, dir, "commit", "--no-verify", "--no-gpg-sign", "-m", "initial")
	return dir
}

func meta() Meta {
	return Meta{
		SessionID:  "test-session",
		MessageID:  "msg-1",
		ToolCallID: "call-1",
		ToolName:   "edit",
	}
}

func TestNewDisabledOutsideGitRepo(t *testing.T) {
	t.Parallel()
	svc, err := New(t.TempDir())
	require.NoError(t, err)
	require.False(t, svc.Enabled())

	_, err = svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)
	_, err = svc.Restore(t.Context(), "test-session", "anything")
	require.Error(t, err)
	cps, err := svc.List(t.Context(), "test-session")
	require.NoError(t, err)
	require.Empty(t, cps)
}

func TestSnapshotAndList(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)
	require.True(t, svc.Enabled())

	writeFile(t, dir, "hello.txt", "v2\n")
	cp, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)
	require.False(t, cp.Noop)
	require.NotEmpty(t, cp.ID)
	require.NotEmpty(t, cp.GitCommit)
	require.Equal(t, "test-session", cp.SessionID)
	require.Equal(t, "msg-1", cp.MessageID)
	require.Equal(t, "call-1", cp.ToolCallID)
	require.Equal(t, "edit", cp.ToolName)
	require.Contains(t, cp.Files, "hello.txt")
	require.False(t, cp.CreatedAt.IsZero())

	cps, err := svc.List(t.Context(), "test-session")
	require.NoError(t, err)
	require.Len(t, cps, 1)
	require.Equal(t, cp.ID, cps[0].ID)

	// The snapshot must stay invisible to the main repository.
	require.Contains(t, readFile(t, dir, ".gitignore"), ".watcher/")
	require.NotContains(t, run(t, dir, "status", "--porcelain"), ".watcher")
}

func TestSnapshotNoopWhenClean(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	first, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)
	require.False(t, first.Noop)

	second, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)
	require.True(t, second.Noop)
	require.Equal(t, first.GitCommit, second.GitCommit)

	cps, err := svc.List(t.Context(), "test-session")
	require.NoError(t, err)
	require.Len(t, cps, 1, "a clean snapshot must not create another commit")
}

func TestRestoreRevertsChanges(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	cp1, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	writeFile(t, dir, "hello.txt", "v2\n")
	cp2, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)
	require.NotEqual(t, cp1.GitCommit, cp2.GitCommit)

	res, err := svc.Restore(t.Context(), "test-session", cp1.ID)
	require.NoError(t, err)
	require.True(t, res.Restored)
	require.Empty(t, res.SafetyCheckpointID, "clean tree must not produce a guard checkpoint")
	require.Equal(t, "v1\n", readFile(t, dir, "hello.txt"))
	require.Contains(t, res.Files, "hello.txt")
}

func TestRestoreGuardsDirtyTree(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	cp1, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	// Uncommitted change that predates the restore: it must survive.
	writeFile(t, dir, "hello.txt", "dirty\n")
	res, err := svc.Restore(t.Context(), "test-session", cp1.ID)
	require.NoError(t, err)
	require.NotEmpty(t, res.SafetyCheckpointID,
		"a dirty tree must be preserved as a guard checkpoint")
	require.Equal(t, "v1\n", readFile(t, dir, "hello.txt"))

	// Restoring back to the guard brings the dirty state back.
	back, err := svc.Restore(t.Context(), "test-session", res.SafetyCheckpointID)
	require.NoError(t, err)
	require.Equal(t, "dirty\n", readFile(t, dir, "hello.txt"))
	require.False(t, back.Restored == false)
}

func TestRestoreUnknownCheckpoint(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	_, err = svc.Restore(t.Context(), "test-session", "deadbeef")
	require.Error(t, err)
}

func TestRestoreUntrackedFilesSurvive(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	cp1, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	writeFile(t, dir, "extra.txt", "untracked\n")
	_, err = svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	_, err = svc.Restore(t.Context(), "test-session", cp1.ID)
	require.NoError(t, err)
	// git checkout never deletes untracked files; the extra file stays
	// on disk (documented, accepted behavior).
	require.Equal(t, "untracked\n", readFile(t, dir, "extra.txt"))
}

func TestForkCreatesBranch(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	cp, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	branch, err := svc.Fork(t.Context(), cp.ID, "Session Two")
	require.NoError(t, err)
	require.Equal(t, "shadow-fork-Session-Two", branch)

	s := svc.(*service)
	out, err := s.gitOut("branch", "--list", branch)
	require.NoError(t, err)
	require.Contains(t, out, branch)
}

func TestDiffStat(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	cp1, err := svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	writeFile(t, dir, "hello.txt", "v2\n")
	_, err = svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	stat, err := svc.DiffStat(t.Context(), cp1.ID)
	require.NoError(t, err)
	require.Contains(t, stat, "hello.txt")
}

func TestShadowRepoNeverTracksGitOrWatcher(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	_, err = svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	s := svc.(*service)
	out, err := s.gitOut("ls-files")
	require.NoError(t, err)
	for _, line := range strings.Split(out, "\n") {
		require.False(t, line == ".git" || strings.HasPrefix(line, ".git/"),
			"shadow repo must not track .git: %s", line)
		require.False(t, line == ".watcher" || strings.HasPrefix(line, ".watcher/"),
			"shadow repo must not track .watcher: %s", line)
	}
	require.Contains(t, out, "hello.txt")
}

func TestListOrdersChronologically(t *testing.T) {
	t.Parallel()
	dir := newTestRepo(t)
	svc, err := New(dir)
	require.NoError(t, err)

	_, err = svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)
	writeFile(t, dir, "hello.txt", "v2\n")
	_, err = svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)
	writeFile(t, dir, "hello.txt", "v3\n")
	_, err = svc.Snapshot(t.Context(), meta())
	require.NoError(t, err)

	cps, err := svc.List(t.Context(), "test-session")
	require.NoError(t, err)
	require.Len(t, cps, 3)
	// Git commit timestamps have second granularity, so equal times are
	// possible; the list must simply be non-decreasing.
	require.False(t, cps[0].CreatedAt.After(cps[1].CreatedAt))
	require.False(t, cps[1].CreatedAt.After(cps[2].CreatedAt))
	require.NotEqual(t, cps[0].GitCommit, cps[2].GitCommit)
}
