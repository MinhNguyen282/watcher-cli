package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/shadowgit"
	"github.com/stretchr/testify/require"
)

// recordingTool is a fake fantasy.AgentTool that records whether a
// shadow checkpoint already existed when it ran.
type recordingTool struct {
	name string
	svc  shadowgit.Service
	ran  bool
	// checkpointsAtRun is the number of checkpoints visible to the
	// shadow service at the moment Run was called. A non-zero value
	// proves the wrapper snapshotted before delegating.
	checkpointsAtRun int
}

func (r *recordingTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: r.name}
}

func (r *recordingTool) ProviderOptions() fantasy.ProviderOptions {
	return nil
}

func (r *recordingTool) SetProviderOptions(opts fantasy.ProviderOptions) {}

func (r *recordingTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	r.ran = true
	if r.svc != nil {
		cps, err := r.svc.List(ctx, tools.GetSessionFromContext(ctx))
		if err == nil {
			r.checkpointsAtRun = len(cps)
		}
	}
	return fantasy.ToolResponse{Content: "ok"}, nil
}

// runGit runs a git command in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
	return string(out)
}

// newCheckpointRepo creates a git repository with one committed file.
func newCheckpointRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("v1\n"), 0o644))
	runGit(t, dir, "add", "hello.txt")
	runGit(t, dir, "commit", "--no-verify", "--no-gpg-sign", "-m", "initial")
	return dir
}

func TestCheckpointToolSnapshotsBeforeRun(t *testing.T) {
	t.Parallel()
	dir := newCheckpointRepo(t)
	svc, err := shadowgit.New(dir)
	require.NoError(t, err)
	require.True(t, svc.Enabled())

	// A change exists before the tool runs: the pre-run snapshot must
	// capture it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("v2\n"), 0o644))

	inner := &recordingTool{name: tools.EditToolName, svc: svc}
	wrapped := wrapCheckpointTools([]fantasy.AgentTool{inner}, svc)
	require.Len(t, wrapped, 1)
	require.IsType(t, &checkpointTool{}, wrapped[0])

	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "sess-1")
	resp, err := wrapped[0].Run(ctx, fantasy.ToolCall{ID: "call-1", Name: tools.EditToolName})
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Content)
	require.True(t, inner.ran)
	require.Equal(t, 1, inner.checkpointsAtRun,
		"the snapshot must exist before the wrapped tool runs")

	cps, err := svc.List(t.Context(), "sess-1")
	require.NoError(t, err)
	require.Len(t, cps, 1)
	require.Equal(t, "call-1", cps[0].ToolCallID)
}

func TestCheckpointToolLeavesReadOnlyToolsUnwrapped(t *testing.T) {
	t.Parallel()
	dir := newCheckpointRepo(t)
	svc, err := shadowgit.New(dir)
	require.NoError(t, err)

	inner := &recordingTool{name: tools.GrepToolName}
	wrapped := wrapCheckpointTools([]fantasy.AgentTool{inner}, svc)
	require.Len(t, wrapped, 1)
	_, ok := wrapped[0].(*checkpointTool)
	require.False(t, ok, "read-only tools must not be wrapped")
	require.Same(t, inner, wrapped[0])
}

func TestCheckpointToolDisabledServicePassesThrough(t *testing.T) {
	t.Parallel()
	svc, err := shadowgit.New(t.TempDir())
	require.NoError(t, err)
	require.False(t, svc.Enabled())

	inner := &recordingTool{name: tools.EditToolName}
	wrapped := wrapCheckpointTools([]fantasy.AgentTool{inner}, svc)
	require.Same(t, inner, wrapped[0], "a disabled service must not wrap tools")

	wrapped = wrapCheckpointTools([]fantasy.AgentTool{inner}, nil)
	require.Same(t, inner, wrapped[0], "a nil service must not wrap tools")
}

func TestCheckpointToolSnapshotFailureDoesNotBlockTool(t *testing.T) {
	t.Parallel()
	// A canceled context makes Snapshot fail; the tool must still run.
	dir := newCheckpointRepo(t)
	svc, err := shadowgit.New(dir)
	require.NoError(t, err)

	inner := &recordingTool{name: tools.EditToolName}
	wrapped := wrapCheckpointTools([]fantasy.AgentTool{inner}, svc)[0]

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, "sess-1")
	resp, err := wrapped.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: tools.EditToolName})
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Content)
	require.True(t, inner.ran)
}
