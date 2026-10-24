package tools

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

func planModeService(t *testing.T) (permission.Service, permission.ModeService) {
	t.Helper()
	svc := permission.NewPermissionService(t.TempDir(), false, nil)
	modeService, ok := svc.(permission.ModeService)
	require.True(t, ok, "permission service must expose ModeService")
	return svc, modeService
}

func sessionContext() context.Context {
	return context.WithValue(context.Background(), SessionIDContextKey, "session-1")
}

func TestEnterPlanModeSetsPlanMode(t *testing.T) {
	t.Parallel()

	svc, modeService := planModeService(t)
	state := NewPlanModeState()
	enter := NewEnterPlanModeTool(svc, state)

	response, err := enter.Run(sessionContext(), fantasy.ToolCall{ID: "c1", Name: EnterPlanModeToolName, Input: "{}"})
	require.NoError(t, err)
	require.False(t, response.IsError, response.Content)
	require.Equal(t, permission.ModePlan, modeService.Mode())

	// Entering twice is a no-op with an explanation, not an error.
	response, err = enter.Run(sessionContext(), fantasy.ToolCall{ID: "c2", Name: EnterPlanModeToolName, Input: "{}"})
	require.NoError(t, err)
	require.False(t, response.IsError)
	require.Contains(t, response.Content, "Already in plan mode")
}

func TestExitPlanModeRequiresApproval(t *testing.T) {
	t.Parallel()

	svc, modeService := planModeService(t)
	modeService.SetMode(permission.ModePlan)
	state := NewPlanModeState()
	exit := NewExitPlanModeTool(svc, state)

	// A rejected approval keeps plan mode active.
	ctx := WithApprover(sessionContext(), func(context.Context, permission.CreatePermissionRequest) (ApprovalOutcome, error) {
		return ApprovalRejected, nil
	})
	response, err := exit.Run(ctx, fantasy.ToolCall{ID: "c1", Name: ExitPlanModeToolName, Input: `{"plan":"do the thing"}`})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "not approved")
	require.Equal(t, permission.ModePlan, modeService.Mode())
}

func TestExitPlanModeRestoresPreviousMode(t *testing.T) {
	t.Parallel()

	svc, modeService := planModeService(t)
	modeService.SetMode(permission.ModeAcceptEdits)
	state := NewPlanModeState()

	enter := NewEnterPlanModeTool(svc, state)
	_, err := enter.Run(sessionContext(), fantasy.ToolCall{ID: "c1", Name: EnterPlanModeToolName, Input: "{}"})
	require.NoError(t, err)
	require.Equal(t, permission.ModePlan, modeService.Mode())

	ctx := WithApprover(sessionContext(), func(context.Context, permission.CreatePermissionRequest) (ApprovalOutcome, error) {
		return ApprovalAllowedOnce, nil
	})
	exit := NewExitPlanModeTool(svc, state)
	response, err := exit.Run(ctx, fantasy.ToolCall{ID: "c2", Name: ExitPlanModeToolName, Input: `{"plan":"step one, step two"}`})
	require.NoError(t, err)
	require.False(t, response.IsError, response.Content)
	require.Equal(t, permission.ModeAcceptEdits, modeService.Mode(), "exiting must restore the mode the session was in")
}

func TestExitPlanModeOutsidePlanMode(t *testing.T) {
	t.Parallel()

	svc, _ := planModeService(t)
	state := NewPlanModeState()
	exit := NewExitPlanModeTool(svc, state)

	response, err := exit.Run(sessionContext(), fantasy.ToolCall{ID: "c1", Name: ExitPlanModeToolName, Input: `{"plan":"x"}`})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "Not in plan mode")
}
