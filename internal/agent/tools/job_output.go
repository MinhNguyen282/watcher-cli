package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
)

const (
	JobOutputToolName = "job_output"
	// DefaultJobOutputWait bounds a blocking output read, not the job lifetime.
	DefaultJobOutputWait = 10 * time.Second
	// MaxJobOutputWait prevents a tool call from indefinitely waiting on servers.
	MaxJobOutputWait = 30 * time.Second
)

//go:embed job_output.md
var jobOutputDescription string

type JobOutputParams struct {
	ShellID        string `json:"shell_id" description:"The ID of the background shell to retrieve output from"`
	Wait           bool   `json:"wait" description:"If true, wait briefly for completion, then return current output even if still running"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"Wait timeout in seconds when wait=true: default 10, allowed 1-30; 0 uses the default. Does not stop the job"`
	FromStart      bool   `json:"from_start,omitempty" description:"Return output from the beginning instead of continuing from the last read position"`
	Offset         int64  `json:"offset,omitempty" description:"Absolute byte offset to read from. Overrides the saved cursor and is ignored with from_start"`
}

type JobOutputResponseMetadata struct {
	ShellID          string `json:"shell_id"`
	Command          string `json:"command"`
	Description      string `json:"description"`
	Done             bool   `json:"done"`
	WaitTimedOut     bool   `json:"wait_timed_out,omitempty"`
	WorkingDirectory string `json:"working_directory"`
	NextStdoutOffset int64  `json:"next_stdout_offset"`
	NextStderrOffset int64  `json:"next_stderr_offset"`
	Truncated        bool   `json:"truncated,omitempty"`
	ExitCode         int    `json:"exit_code"`
	Signal           string `json:"signal,omitempty"`
	JobTimedOut      bool   `json:"timed_out,omitempty"`
	Aborted          bool   `json:"aborted,omitempty"`
	KillReason       string `json:"kill_reason,omitempty"`
}

func NewJobOutputTool(spillDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		JobOutputToolName,
		jobOutputDescription,
		func(ctx context.Context, params JobOutputParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
			}
			if params.TimeoutSeconds < 0 || params.TimeoutSeconds > int(MaxJobOutputWait/time.Second) {
				return fantasy.NewTextErrorResponse("timeout_seconds must be between 0 and 30 (0 uses the 10-second default)"), nil
			}
			if params.Offset < 0 {
				return fantasy.NewTextErrorResponse("offset must be zero or greater"), nil
			}
			if err := ctx.Err(); err != nil {
				return fantasy.ToolResponse{}, err
			}

			bgManager := shell.GetBackgroundShellManager()
			bgShell, exists, owned := bgManager.GetOwned(params.ShellID, GetSessionFromContext(ctx))
			if !exists || !owned {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("background shell not found: %s", params.ShellID)), nil
			}

			waitTimeout := DefaultJobOutputWait
			if params.TimeoutSeconds > 0 {
				waitTimeout = time.Duration(params.TimeoutSeconds) * time.Second
			}
			waitExpired := false
			if params.Wait {
				waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
				waitExpired = !bgShell.WaitContext(waitCtx)
				cancel()
				if err := ctx.Err(); err != nil {
					return fantasy.ToolResponse{}, err
				}
			}

			stdout, stderr, nextStdout, nextStderr, truncated, done, err := bgShell.ReadFrom(params.FromStart, params.Offset)
			waitTimedOut := waitExpired && !done

			var outputParts []string
			if truncated {
				outputParts = append(outputParts, "[earlier output was dropped by the job buffer limit]")
			}
			if stdout != "" {
				outputParts = append(outputParts, stdout)
			}
			if stderr != "" {
				outputParts = append(outputParts, stderr)
			}

			status := "running"
			outcome := bgShell.Outcome()
			if done {
				status = "completed"
				switch {
				case outcome.TimedOut:
					outputParts = append(outputParts, "Timed out")
				case outcome.Aborted:
					reason := "Aborted"
					if outcome.KillReason != "" {
						reason = fmt.Sprintf("Aborted (%s)", outcome.KillReason)
					}
					outputParts = append(outputParts, reason)
				default:
					if outcome.Signal != "" {
						outputParts = append(outputParts, "Signal "+outcome.Signal)
					}
					if outcome.ExitCode != 0 {
						outputParts = append(outputParts, fmt.Sprintf("Exit code %d", outcome.ExitCode))
					}
				}
				if err != nil && outcome.ExitCode == 0 && outcome.Signal == "" && !outcome.TimedOut && !outcome.Aborted {
					outputParts = append(outputParts, err.Error())
				}
			}

			output := strings.Join(outputParts, "\n")
			output = TruncateOutput(output, spillDir)

			metadata := JobOutputResponseMetadata{
				ShellID:          params.ShellID,
				Command:          bgShell.Command,
				Description:      bgShell.Description,
				Done:             done,
				WaitTimedOut:     waitTimedOut,
				WorkingDirectory: bgShell.WorkingDir,
				NextStdoutOffset: nextStdout,
				NextStderrOffset: nextStderr,
				Truncated:        truncated,
				ExitCode:         outcome.ExitCode,
				Signal:           outcome.Signal,
				JobTimedOut:      outcome.TimedOut,
				Aborted:          outcome.Aborted,
				KillReason:       outcome.KillReason,
			}

			if output == "" {
				if done {
					output = BashNoOutput
				} else {
					output = "No new output since the last read. Use from_start=true to reread retained output."
				}
			}

			notice := ""
			if waitTimedOut {
				notice = fmt.Sprintf("Wait timed out after %s; returning current output. The job is still running. For dev servers, use wait=false and check readiness; do not restart the server just because this wait timed out.\n\n", waitTimeout)
			}
			result := fmt.Sprintf("Status: %s\n\n%s%s", status, notice, output)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		},
	)
}
