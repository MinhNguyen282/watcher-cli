package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

type pipelineTestTool struct {
	name string
	run  func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error)
	opts fantasy.ProviderOptions
}

func (t *pipelineTestTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: t.name, Description: "pipeline test tool"}
}

func (t *pipelineTestTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return t.run(ctx, call)
}

func (t *pipelineTestTool) ProviderOptions() fantasy.ProviderOptions {
	return t.opts
}

func (t *pipelineTestTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.opts = opts
}

func approvalTool(executed *atomic.Int64) *pipelineTestTool {
	return &pipelineTestTool{
		name: "approval-probe",
		run: func(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			outcome, err := tools.RequestApproval(ctx, nil, permission.CreatePermissionRequest{
				SessionID:  "session",
				ToolCallID: call.ID,
				ToolName:   "approval-probe",
			})
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if outcome != tools.ApprovalAllowedOnce {
				return tools.NewPermissionDeniedResponse(), nil
			}
			executed.Add(1)
			return fantasy.NewTextResponse("ran"), nil
		},
	}
}

func newPipeline(inner fantasy.AgentTool, perms permission.Service, timeout time.Duration, guards ...ToolGuard) *toolPipeline {
	return &toolPipeline{
		inner:       inner,
		permissions: perms,
		guards:      guards,
		timeout:     timeout,
	}
}

func TestPipelineMissingApprovalFailsClosed(t *testing.T) {
	t.Parallel()

	var executed atomic.Int64
	pipeline := newPipeline(approvalTool(&executed), nil, time.Second)

	response, err := pipeline.Run(t.Context(), fantasy.ToolCall{ID: "call-1", Name: "approval-probe"})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "permission service unavailable")
	require.Zero(t, executed.Load(), "tool body ran without an approval service")
}

func TestPipelineRejectedApprovalNeverExecutesBody(t *testing.T) {
	t.Parallel()

	var executed atomic.Int64
	perms := permission.NewPermissionService(t.TempDir(), false, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	events := perms.Subscribe(ctx)
	go func() {
		for event := range events {
			perms.Deny(event.Payload)
		}
	}()

	pipeline := newPipeline(approvalTool(&executed), perms, time.Second)
	response, err := pipeline.Run(ctx, fantasy.ToolCall{ID: "call-2", Name: "approval-probe"})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "User denied permission")
	require.Zero(t, executed.Load(), "tool body ran after denial")
}

func TestPipelineGuardDenialIsMonotonic(t *testing.T) {
	t.Parallel()

	var executed atomic.Int64
	inner := &pipelineTestTool{
		name: "guarded",
		run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			executed.Add(1)
			return fantasy.NewTextResponse("ran"), nil
		},
	}
	deny := func(context.Context, fantasy.ToolCall) string { return "always denied" }
	allow := func(context.Context, fantasy.ToolCall) string { return "" }

	pipeline := newPipeline(inner, nil, time.Second, deny, allow)
	response, err := pipeline.Run(t.Context(), fantasy.ToolCall{ID: "call-3", Name: "guarded"})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "always denied")
	require.Zero(t, executed.Load())
}

func TestPipelineTimeoutIsNotAGenericExit(t *testing.T) {
	t.Parallel()

	inner := &pipelineTestTool{
		name: "slow",
		run: func(ctx context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			<-ctx.Done()
			return fantasy.ToolResponse{}, ctx.Err()
		},
	}
	pipeline := newPipeline(inner, nil, 20*time.Millisecond)
	response, err := pipeline.Run(t.Context(), fantasy.ToolCall{ID: "call-4", Name: "slow"})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "timed out")
	require.Contains(t, response.Metadata, `"timed_out":true`)
}

func TestPipelineNormalizesErrorsAndPanics(t *testing.T) {
	t.Parallel()

	errorTool := &pipelineTestTool{
		name: "error",
		run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{}, errors.New("boom")
		},
	}
	response, err := newPipeline(errorTool, nil, time.Second).Run(t.Context(), fantasy.ToolCall{ID: "call-5"})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "boom")

	panicTool := &pipelineTestTool{
		name: "panic",
		run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			panic("kaboom")
		},
	}
	response, err = newPipeline(panicTool, nil, time.Second).Run(t.Context(), fantasy.ToolCall{ID: "call-6"})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "panicked")
	require.True(t, strings.Contains(response.Metadata, `"outcome":"panic"`))
}

type recordingEventRecorder struct {
	intents atomic.Int64
	results atomic.Int64
}

func (r *recordingEventRecorder) RecordToolCallIntent(context.Context, fantasy.ToolCall) {
	r.intents.Add(1)
}

func (r *recordingEventRecorder) RecordToolResult(context.Context, fantasy.ToolCall, fantasy.ToolResponse, string) {
	r.results.Add(1)
}

func TestPipelineRecordsOneIntentAndOneResult(t *testing.T) {
	t.Parallel()

	var executed atomic.Int64
	pipeline := newPipeline(&pipelineTestTool{
		name: "recorded",
		run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			executed.Add(1)
			return fantasy.NewTextResponse("ok"), nil
		},
	}, nil, time.Second)
	recorder := &recordingEventRecorder{}
	pipeline.recorder = recorder

	response, err := pipeline.Run(t.Context(), fantasy.ToolCall{ID: "call-7", Name: "recorded"})
	require.NoError(t, err)
	require.False(t, response.IsError)
	require.Equal(t, int64(1), executed.Load())
	require.Equal(t, int64(1), recorder.intents.Load())
	require.Equal(t, int64(1), recorder.results.Load())
}

func TestPipelinePlanModeDeniesMutatingTools(t *testing.T) {
	t.Parallel()

	var executed atomic.Int64
	inner := &pipelineTestTool{
		name: tools.EditToolName,
		run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			executed.Add(1)
			return fantasy.NewTextResponse("edited"), nil
		},
	}
	perms := permission.NewPermissionService(t.TempDir(), false, nil)
	modeService, ok := perms.(permission.ModeService)
	require.True(t, ok)
	modeService.SetMode(permission.ModePlan)

	pipeline := newPipeline(inner, perms, time.Second)
	response, err := pipeline.Run(t.Context(), fantasy.ToolCall{
		ID:    "call-plan",
		Name:  tools.EditToolName,
		Input: `{"file_path":"main.go"}`,
	})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "plan mode is read-only")
	require.Zero(t, executed.Load(), "a mutating tool ran in plan mode")
}

func TestPipelinePlanModeAllowsReadOnlyTools(t *testing.T) {
	t.Parallel()

	var executed atomic.Int64
	inner := &pipelineTestTool{
		name: tools.GrepToolName,
		run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			executed.Add(1)
			return fantasy.NewTextResponse("match"), nil
		},
	}
	perms := permission.NewPermissionService(t.TempDir(), false, nil)
	perms.(permission.ModeService).SetMode(permission.ModePlan)

	pipeline := newPipeline(inner, perms, time.Second)
	response, err := pipeline.Run(t.Context(), fantasy.ToolCall{ID: "call-read", Name: tools.GrepToolName, Input: `{"pattern":"x"}`})
	require.NoError(t, err)
	require.False(t, response.IsError, response.Content)
	require.EqualValues(t, 1, executed.Load())
}

func TestPipelineEmitsTaskHooks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runner := hooks.NewRunnerForEvents(map[string][]config.HookConfig{
		hooks.EventTaskCreated:   {{Command: `echo '{"context":"task-created"}'`}},
		hooks.EventTaskCompleted: {{Command: `echo '{"context":"task-completed"}'`}},
	}, dir, dir)

	inner := &pipelineTestTool{
		name: tools.TodosToolName,
		run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse("updated"), tools.TodosResponseMetadata{
				JustAdded:     []string{"write the test"},
				JustCompleted: []string{"read the code"},
				Completed:     1,
				Total:         2,
			}), nil
		},
	}
	pipeline := newPipeline(inner, nil, time.Second)
	pipeline.runner = runner

	response, err := pipeline.Run(t.Context(), fantasy.ToolCall{
		ID:    "call-todos",
		Name:  tools.TodosToolName,
		Input: `{"todos":[]}`,
	})
	require.NoError(t, err)
	require.False(t, response.IsError, response.Content)
	require.Contains(t, response.Content, "task-created")
	require.Contains(t, response.Content, "task-completed")
}
