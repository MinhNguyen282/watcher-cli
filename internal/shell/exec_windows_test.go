//go:build windows

package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// TestWindowsJobHelper runs only in subprocesses explicitly marked by arguments.
func TestWindowsJobHelper(t *testing.T) {
	var mode, dir string
	for i, arg := range os.Args {
		if arg == "--watcher-job-helper" && i+2 < len(os.Args) {
			mode, dir = os.Args[i+1], os.Args[i+2]
			break
		}
	}
	if mode == "" {
		return
	}
	if mode == "child" {
		if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0o600); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWindowsJobHelper$", "--", "--watcher-job-helper", "child", dir)
	child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(dir, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		_ = child.Process.Kill()
		os.Exit(2)
	}
	if mode == "exit" {
		os.Exit(0)
	}
	_ = child.Wait()
	os.Exit(0)
}

func startWindowsTree(t *testing.T, mode string) (*BackgroundShellManager, *BackgroundShell, windows.Handle) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	command := fmt.Sprintf("%q -test.run=^TestWindowsJobHelper$ -- --watcher-job-helper %s %q", filepath.ToSlash(executable), mode, filepath.ToSlash(dir))
	manager := newBackgroundShellManager()
	job, err := manager.Start(t.Context(), dir, nil, command, "Windows process tree regression", testMaxJobs)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.KillContext(ctx, job.ID)
	})
	require.Eventually(t, func() bool {
		_, readyErr := os.Stat(filepath.Join(dir, "ready"))
		_, pidErr := os.Stat(filepath.Join(dir, "child.pid"))
		return readyErr == nil && pidErr == nil
	}, 5*time.Second, 10*time.Millisecond)
	raw, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(raw))
	require.NoError(t, err)
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	require.NoError(t, err)
	t.Cleanup(func() { _ = windows.TerminateProcess(process, 1); windows.CloseHandle(process) })
	return manager, job, process
}

func TestWindowsJobKillTerminatesChildWithInheritedPipes(t *testing.T) {
	t.Parallel()
	manager, job, child := startWindowsTree(t, "wait")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	require.NoError(t, manager.KillContext(ctx, job.ID))
	require.True(t, job.IsDone())
	state, err := windows.WaitForSingleObject(child, 2000)
	require.NoError(t, err)
	require.Equal(t, uint32(windows.WAIT_OBJECT_0), state, "Child process must exit, not just its parent")
}

func TestWindowsParentExitDoesNotWaitForOrphanPipes(t *testing.T) {
	t.Parallel()
	_, job, child := startWindowsTree(t, "exit")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.True(t, job.WaitContext(ctx), "Inherited pipes must not keep the job alive indefinitely")
	state, err := windows.WaitForSingleObject(child, 2000)
	require.NoError(t, err)
	require.Equal(t, uint32(windows.WAIT_OBJECT_0), state, "Job Object cleanup must terminate orphaned children")
}
