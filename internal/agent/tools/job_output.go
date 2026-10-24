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
}

type JobOutputResponseMetadata struct {
	ShellID          string `json:"shell_id"`
	Command          string `json:"command"`
	Description      string `json:"description"`
	Done             bool   `json:"done"`
	WaitTimedOut     bool   `json:"wait_timed_out,omitempty"`
	WorkingDirectory string `json:"working_directory"`
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
			if err := ctx.Err(); err != nil {
				return fantasy.ToolResponse{}, err
			}

			bgManager := shell.GetBackgroundShellManager()
			bgShell, ok := bgManager.Get(params.ShellID)
			if !ok {
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

			stdout, stderr, done, err := bgShell.GetOutput()
			waitTimedOut := waitExpired && !done

			var outputParts []string
			if stdout != "" {
				outputParts = append(outputParts, stdout)
			}
			if stderr != "" {
				outputParts = append(outputParts, stderr)
			}

			status := "running"
			if done {
				status = "completed"
				if err != nil {
					exitCode := shell.ExitCode(err)
					if exitCode != 0 {
						outputParts = append(outputParts, fmt.Sprintf("Exit code %d", exitCode))
					}
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
			}

			if output == "" {
				output = BashNoOutput
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
