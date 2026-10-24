package shell

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackgroundShellManager_KillContextTimeoutKeepsJob(t *testing.T) {
	t.Parallel()
	manager := newBackgroundShellManager()
	canceled := false
	job := &BackgroundShell{ID: "stubborn", done: make(chan struct{}), cancel: func() { canceled = true }}
	manager.shells[job.ID] = job
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := manager.KillContext(ctx, job.ID)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.True(t, canceled)
	tracked, ok := manager.Get(job.ID)
	require.True(t, ok)
	require.Same(t, job, tracked)
	close(job.done)
	require.NoError(t, manager.KillContext(t.Context(), job.ID))
	_, ok = manager.Get(job.ID)
	require.False(t, ok)
}

func TestBackgroundShellManager_KillContextCanceled(t *testing.T) {
	t.Parallel()
	manager := newBackgroundShellManager()
	job := &BackgroundShell{ID: "pending", done: make(chan struct{}), cancel: func() {}}
	manager.shells[job.ID] = job
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := manager.KillContext(ctx, job.ID)
	require.ErrorIs(t, err, context.Canceled)
	_, ok := manager.Get(job.ID)
	require.True(t, ok)
	close(job.done)
	// Completed jobs can be removed even when the caller was canceled.
	require.NoError(t, manager.KillContext(ctx, job.ID))
}

func TestBackgroundShellManager_Start(t *testing.T) {
	t.Skip("Skipping this until I figure out why its flaky")
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(ctx, workingDir, nil, "echo 'hello world'", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	if bgShell.ID == "" {
		t.Error("expected shell ID to be non-empty")
	}

	// Wait for the command to complete
	bgShell.Wait()

	stdout, stderr, done, err := bgShell.GetOutput()
	if !done {
		t.Error("expected shell to be done")
	}

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	if !strings.Contains(stdout, "hello world") {
		t.Errorf("expected stdout to contain 'hello world', got: %s", stdout)
	}

	if stderr != "" {
		t.Errorf("expected empty stderr, got: %s", stderr)
	}
}

func TestBackgroundShellManager_Get(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(ctx, workingDir, nil, "echo 'test'", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Retrieve the shell
	retrieved, ok := manager.Get(bgShell.ID)
	if !ok {
		t.Error("expected to find the background shell")
	}

	if retrieved.ID != bgShell.ID {
		t.Errorf("expected shell ID %s, got %s", bgShell.ID, retrieved.ID)
	}

	// Clean up
	manager.Kill(bgShell.ID)
}

func TestBackgroundShellManager_Kill(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start a long-running command
	bgShell, err := manager.Start(ctx, workingDir, nil, "sleep 10", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Kill it
	err = manager.Kill(bgShell.ID)
	if err != nil {
		t.Errorf("failed to kill background shell: %v", err)
	}

	// Verify it's no longer in the manager
	_, ok := manager.Get(bgShell.ID)
	if ok {
		t.Error("expected shell to be removed after kill")
	}

	// Verify the shell is done
	if !bgShell.IsDone() {
		t.Error("expected shell to be done after kill")
	}
}

func TestBackgroundShellManager_KillNonExistent(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()

	err := manager.Kill("non-existent-id")
	if err == nil {
		t.Error("expected error when killing non-existent shell")
	}
}

func TestBackgroundShell_IsDone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(ctx, workingDir, nil, "echo 'quick'", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Wait for the command to complete (Windows is slower to spin up).
	require.Eventually(t, bgShell.IsDone, 5*time.Second, 50*time.Millisecond, "expected shell to be done")

	// Clean up
	manager.Kill(bgShell.ID)
}

func TestBackgroundShell_WithBlockFuncs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	blockFuncs := []BlockFunc{
		CommandsBlocker([]string{"curl", "wget"}),
	}

	bgShell, err := manager.Start(ctx, workingDir, blockFuncs, "curl example.com", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Wait for the command to complete
	bgShell.Wait()

	stdout, stderr, done, execErr := bgShell.GetOutput()
	if !done {
		t.Error("expected shell to be done")
	}

	// The command should have been blocked
	output := stdout + stderr
	if !strings.Contains(output, "not allowed") && execErr == nil {
		t.Errorf("expected command to be blocked, got stdout: %s, stderr: %s, err: %v", stdout, stderr, execErr)
	}

	// Clean up
	manager.Kill(bgShell.ID)
}

func TestBackgroundShellManager_List(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping flacky test on windows")
	}

	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start two shells
	bgShell1, err := manager.Start(ctx, workingDir, nil, "sleep 1", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start first background shell: %v", err)
	}

	bgShell2, err := manager.Start(ctx, workingDir, nil, "sleep 1", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start second background shell: %v", err)
	}

	ids := manager.List()

	// Check that both shells are in the list
	found1 := false
	found2 := false
	for _, id := range ids {
		if id == bgShell1.ID {
			found1 = true
		}
		if id == bgShell2.ID {
			found2 = true
		}
	}

	if !found1 {
		t.Errorf("expected to find shell %s in list", bgShell1.ID)
	}
	if !found2 {
		t.Errorf("expected to find shell %s in list", bgShell2.ID)
	}

	// Clean up
	manager.Kill(bgShell1.ID)
	manager.Kill(bgShell2.ID)
}

func TestBackgroundShellManager_KillAll(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start multiple long-running shells
	shell1, err := manager.Start(ctx, workingDir, nil, "sleep 10", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start shell 1: %v", err)
	}

	shell2, err := manager.Start(ctx, workingDir, nil, "sleep 10", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start shell 2: %v", err)
	}

	shell3, err := manager.Start(ctx, workingDir, nil, "sleep 10", "", testMaxJobs)
	if err != nil {
		t.Fatalf("failed to start shell 3: %v", err)
	}

	// Verify shells are running
	if shell1.IsDone() || shell2.IsDone() || shell3.IsDone() {
		t.Error("shells should not be done yet")
	}

	// Kill all shells
	manager.KillAll(t.Context())

	// Verify all shells are done
	if !shell1.IsDone() {
		t.Error("shell1 should be done after KillAll")
	}
	if !shell2.IsDone() {
		t.Error("shell2 should be done after KillAll")
	}
	if !shell3.IsDone() {
		t.Error("shell3 should be done after KillAll")
	}

	// Verify they're removed from the manager
	if _, ok := manager.Get(shell1.ID); ok {
		t.Error("shell1 should be removed from manager")
	}
	if _, ok := manager.Get(shell2.ID); ok {
		t.Error("shell2 should be removed from manager")
	}
	if _, ok := manager.Get(shell3.ID); ok {
		t.Error("shell3 should be removed from manager")
	}

	// Verify list is empty (or doesn't contain our shells)
	ids := manager.List()
	for _, id := range ids {
		if id == shell1.ID || id == shell2.ID || id == shell3.ID {
			t.Errorf("shell %s should not be in list after KillAll", id)
		}
	}
}

func TestBackgroundShellManager_KillAll_Timeout(t *testing.T) {
	t.Parallel()

	// XXX: can't use synctest here - causes --race to trip.

	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start a shell that traps signals and ignores cancellation.
	_, err := manager.Start(t.Context(), workingDir, nil, "trap '' TERM INT; sleep 60", "", testMaxJobs)
	require.NoError(t, err)

	// Short timeout to test the timeout path.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	t.Cleanup(cancel)

	start := time.Now()
	manager.KillAll(ctx)

	elapsed := time.Since(start)

	// Must return promptly after timeout, not hang for 60 seconds.
	require.Less(t, elapsed, 2*time.Second)
}

func TestBackgroundShell_WaitContext_Completed(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	close(done)

	bgShell := &BackgroundShell{done: done}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	require.True(t, bgShell.WaitContext(ctx))
}

func TestBackgroundShell_WaitContext_Canceled(t *testing.T) {
	t.Parallel()

	bgShell := &BackgroundShell{done: make(chan struct{})}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.False(t, bgShell.WaitContext(ctx))
}

func TestSyncBufferDropsOldestAndReportsLostBytes(t *testing.T) {
	t.Parallel()

	buf := newSyncBuffer(5)
	_, err := buf.WriteString("abcdefgh")
	require.NoError(t, err)
	require.Equal(t, "defgh", buf.String())

	data, next, truncated, total := buf.readFrom(0)
	require.True(t, truncated)
	require.Equal(t, "defgh", data)
	require.Equal(t, int64(8), next)
	require.Equal(t, int64(8), total)
}

func TestBackgroundShellCursorReadsOnlyNewOutput(t *testing.T) {
	t.Parallel()

	job := &BackgroundShell{
		done:   make(chan struct{}),
		stdout: newSyncBuffer(1024),
		stderr: newSyncBuffer(1024),
	}
	_, err := job.stdout.WriteString("first")
	require.NoError(t, err)

	stdout, _, next, _, truncated, _, _ := job.ReadFrom(false, 0)
	require.Equal(t, "first", stdout)
	require.False(t, truncated)

	_, err = job.stdout.WriteString(" second")
	require.NoError(t, err)
	stdout, _, next2, _, _, _, _ := job.ReadFrom(false, 0)
	require.Equal(t, " second", stdout)
	require.Greater(t, next2, next)

	stdout, _, _, _, _, _, _ = job.ReadFrom(false, 0)
	require.Empty(t, stdout, "a third read should have no new bytes")

	stdout, _, _, _, _, _, _ = job.ReadFrom(true, 0)
	require.Equal(t, "first second", stdout)
}

func TestBackgroundShellOwnership(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	job, err := manager.Start(t.Context(), t.TempDir(), nil, "echo owned", "", testMaxJobs, WithSessionID("session-a"))
	require.NoError(t, err)
	job.Wait()

	_, exists, owned := manager.GetOwned(job.ID, "session-a")
	require.True(t, exists)
	require.True(t, owned)

	_, exists, owned = manager.GetOwned(job.ID, "session-b")
	require.True(t, exists)
	require.False(t, owned, "another session must not read this job")
}

func TestBackgroundShellOutcomeReportsExitCode(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	job, err := manager.Start(t.Context(), t.TempDir(), nil, "exit 7", "", testMaxJobs)
	require.NoError(t, err)
	job.Wait()

	outcome := job.Outcome()
	require.Equal(t, 7, outcome.ExitCode)
	require.False(t, outcome.TimedOut)
	require.False(t, outcome.Aborted)
}

const testMaxJobs = 50

// track registers a fake job with the manager. When completedAt is zero the
// job counts as still running.
func track(m *BackgroundShellManager, id string, completedAt int64) {
	bgShell := &BackgroundShell{
		ID:     id,
		done:   make(chan struct{}),
		stdout: &syncBuffer{},
		stderr: &syncBuffer{},
		cancel: func() {},
	}
	if completedAt > 0 {
		bgShell.completedAt.Store(completedAt)
		close(bgShell.done)
	}
	m.shells[id] = bgShell
}

func TestBackgroundShellManager_FinishedJobsDoNotBlockStart(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	for i := range testMaxJobs {
		track(manager, fmt.Sprintf("done-%d", i), time.Now().Unix())
	}

	bgShell, err := manager.Start(t.Context(), t.TempDir(), nil, "echo hi", "", testMaxJobs)
	require.NoError(t, err)
	require.NotEmpty(t, bgShell.ID)
	bgShell.Wait()
}

func TestBackgroundShellManager_RunningJobsBlockStart(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	track(manager, "running-0", 0)
	track(manager, "running-1", 0)

	_, err := manager.Start(t.Context(), t.TempDir(), nil, "echo hi", "", 2)
	require.ErrorContains(t, err, "maximum number of running background jobs (2)")

	bgShell, err := manager.Start(t.Context(), t.TempDir(), nil, "echo hi", "", 3)
	require.NoError(t, err, "a higher limit admits the job")
	bgShell.Wait()
}

// Output stays readable for the retention period and no longer.
func TestBackgroundShellManager_DropsStaleFinishedJobs(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	now := time.Now()
	track(manager, "stale", now.Add(-CompletedJobRetention).Unix()-1)
	track(manager, "fresh", now.Unix())

	bgShell, err := manager.Start(t.Context(), t.TempDir(), nil, "echo hi", "", testMaxJobs)
	require.NoError(t, err)
	bgShell.Wait()

	_, ok := manager.Get("stale")
	require.False(t, ok, "a job past its retention window is dropped")
	_, ok = manager.Get("fresh")
	require.True(t, ok, "a recently finished job stays readable")
}

func TestBackgroundShellManager_PruneDropsOldestFinished(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	now := time.Now().Unix()
	for i := range MaxFinishedJobs + 1 {
		track(manager, fmt.Sprintf("done-%d", i), now+int64(i))
	}
	track(manager, "running", 0)

	bgShell, err := manager.Start(t.Context(), t.TempDir(), nil, "sleep 1", "", testMaxJobs)
	require.NoError(t, err)

	require.Len(t, manager.List(), MaxFinishedJobs+2)
	_, ok := manager.Get("done-0")
	require.False(t, ok, "oldest finished job should have been dropped")
	_, ok = manager.Get("done-1")
	require.True(t, ok, "only the excess is dropped")
	_, ok = manager.Get("running")
	require.True(t, ok, "running jobs must never be dropped")

	require.NoError(t, manager.Kill(bgShell.ID))
}
