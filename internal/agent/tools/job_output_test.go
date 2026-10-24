package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

func startOutputTestJob(t *testing.T, command string) *shell.BackgroundShell {
	t.Helper()
	manager := shell.GetBackgroundShellManager()
	job, err := manager.Start(context.Background(), t.TempDir(), nil, command, "output timeout regression", testMaxJobs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Kill(job.ID) })
	return job
}

func TestJobOutputWaitTimesOutWithoutStoppingJob(t *testing.T) {
	t.Parallel()
	requireSleep(t)
	job := startOutputTestJob(t, "echo OUTPUT_READY; sleep 30")
	require.Eventually(t, func() bool { stdout, _, _, _ := job.GetOutput(); return stdout != "" }, 3*time.Second, 10*time.Millisecond)
	tool := NewJobOutputTool(t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	started := time.Now()
	response, err := tool.Run(ctx, fantasy.ToolCall{Input: fmt.Sprintf(`{"shell_id":%q,"wait":true,"timeout_seconds":1}`, job.ID)})
	require.NoError(t, err)
	require.Less(t, time.Since(started), 3*time.Second)
	require.False(t, response.IsError)
	require.Contains(t, response.Content, "Status: running")
	require.Contains(t, response.Content, "Wait timed out after 1s")
	require.Contains(t, response.Content, "OUTPUT_READY")
	var metadata JobOutputResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(response.Metadata), &metadata))
	require.True(t, metadata.WaitTimedOut)
	require.False(t, metadata.Done)
	require.False(t, job.IsDone())
	_, tracked := shell.GetBackgroundShellManager().Get(job.ID)
	require.True(t, tracked)
	// An immediate follow-up read must still work on the same live job.
	response, err = tool.Run(t.Context(), fantasy.ToolCall{Input: fmt.Sprintf(`{"shell_id":%q,"wait":false}`, job.ID)})
	require.NoError(t, err)
	require.NotContains(t, response.Content, "Wait timed out")
	require.Contains(t, response.Content, "Status: running")
}

func TestJobOutputCompletedBeforeTimeout(t *testing.T) {
	t.Parallel()
	job := startOutputTestJob(t, "echo FINISHED; exit 7")
	tool := NewJobOutputTool(t.TempDir())
	response, err := tool.Run(t.Context(), fantasy.ToolCall{Input: fmt.Sprintf(`{"shell_id":%q,"wait":true,"timeout_seconds":2}`, job.ID)})
	require.NoError(t, err)
	require.Contains(t, response.Content, "Status: completed")
	require.Contains(t, response.Content, "Exit code 7")
	require.NotContains(t, response.Content, "Wait timed out")
	var metadata JobOutputResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(response.Metadata), &metadata))
	require.True(t, metadata.Done)
	require.False(t, metadata.WaitTimedOut)
}

func TestJobOutputCallerCancellationPreservesJob(t *testing.T) {
	t.Parallel()
	requireSleep(t)
	job := startOutputTestJob(t, "echo OUTPUT_READY; sleep 30")
	tool := NewJobOutputTool(t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := tool.Run(ctx, fantasy.ToolCall{Input: fmt.Sprintf(`{"shell_id":%q,"wait":true}`, job.ID)})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, job.IsDone())
	_, tracked := shell.GetBackgroundShellManager().Get(job.ID)
	require.True(t, tracked)
}

func TestJobOutputRejectsInvalidWaitTimeout(t *testing.T) {
	t.Parallel()
	tool := NewJobOutputTool(t.TempDir())
	for _, timeout := range []int{-1, 31, 999999999} {
		response, err := tool.Run(t.Context(), fantasy.ToolCall{Input: fmt.Sprintf(`{"shell_id":"missing","wait":true,"timeout_seconds":%d}`, timeout)})
		require.NoError(t, err)
		require.True(t, response.IsError)
		require.Contains(t, response.Content, "timeout_seconds must be")
	}
}
