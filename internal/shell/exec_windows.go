//go:build windows

package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"mvdan.cc/sh/v3/interp"
)

// defaultKillTimeout matches mvdan's DefaultExecHandler default.
const defaultKillTimeout = 2 * time.Second

// isolateProcess keeps shell-tool subprocesses from opening console windows.
func isolateProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}

// processGroupExecHandler owns each Windows process tree with a Job Object.
func processGroupExecHandler(killTimeout time.Duration) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return interp.ExitStatus(127)
		}
		cmd := exec.CommandContext(ctx, path, args[1:]...)
		cmd.Args = args
		cmd.Env = execEnvList(hc.Env)
		cmd.Dir = hc.Dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = hc.Stdin, hc.Stdout, hc.Stderr
		err = runWindowsCommand(cmd, killTimeout)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return interp.ExitStatus(uint8(exitErr.ExitCode()))
		}
		return err
	}
}

func runDispatchedCommand(cmd *exec.Cmd) error {
	return runWindowsCommand(cmd, defaultKillTimeout)
}

// runWindowsCommand kills descendants on cancellation or handle cleanup and
// bounds pipe draining even when a descendant retains stdout/stderr handles.
func runWindowsCommand(cmd *exec.Cmd, waitDelay time.Duration) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create process job: %w", err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("configure process job: %w", err)
	}
	isolateProcess(cmd)
	if waitDelay <= 0 {
		waitDelay = defaultKillTimeout
	}
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error {
		jobErr := windows.TerminateJobObject(job, 1)
		// Cancellation can race the assignment immediately after Start.
		processErr := cmd.Process.Kill()
		if jobErr == nil || errors.Is(processErr, os.ErrProcessDone) {
			return nil
		}
		return processErr
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		if err != nil {
			// Short-lived commands may exit before assignment is possible.
			if state, waitErr := windows.WaitForSingleObject(process, 0); waitErr == nil && state == windows.WAIT_OBJECT_0 {
				err = nil
			}
		}
		windows.CloseHandle(process)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("assign process to job: %w", err)
	}
	return cmd.Wait()
}
