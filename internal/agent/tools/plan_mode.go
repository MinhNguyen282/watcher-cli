package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed enter_plan_mode.md
var enterPlanModeDescription string

//go:embed exit_plan_mode.md
var exitPlanModeDescription string

// Plan-mode tool names.
const (
	EnterPlanModeToolName = "enter_plan_mode"
	ExitPlanModeToolName  = "exit_plan_mode"
)

// PlanModeState remembers the mode a session was in before plan mode so
// exiting restores it instead of guessing.
type PlanModeState struct {
	mu       sync.Mutex
	previous permission.Mode
	active   bool
}

// NewPlanModeState returns an empty state holder.
func NewPlanModeState() *PlanModeState {
	return &PlanModeState{previous: permission.ModeDefault}
}

type enterPlanModeParams struct{}

// ExitPlanModeParams carries the plan presented for approval.
type ExitPlanModeParams struct {
	Plan string `json:"plan" description:"The complete plan to present for approval"`
}

// NewEnterPlanModeTool builds the tool that switches to plan mode.
func NewEnterPlanModeTool(service permission.Service, state *PlanModeState) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		EnterPlanModeToolName,
		enterPlanModeDescription,
		func(ctx context.Context, _ enterPlanModeParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			modeService, ok := service.(permission.ModeService)
			if !ok {
				return fantasy.NewTextErrorResponse("Plan mode is not available: the permission service does not support modes."), nil
			}
			state.mu.Lock()
			defer state.mu.Unlock()

			current := modeService.Mode()
			if current == permission.ModePlan {
				return fantasy.NewTextResponse("Already in plan mode. Investigate, then call exit_plan_mode with the plan."), nil
			}
			if err := permission.ValidateTransition(current, permission.ModePlan, modeService.ModeOptions()); err != nil {
				return fantasy.NewTextErrorResponse("Cannot enter plan mode: " + err.Error()), nil
			}
			state.previous = current
			state.active = true
			modeService.SetMode(permission.ModePlan)
			return fantasy.NewTextResponse(
				"Plan mode is active. The harness now refuses mutating tools and only allows read-only shell commands. " +
					"Investigate the codebase, then call exit_plan_mode with the complete plan.",
			), nil
		},
	)
}

// NewExitPlanModeTool builds the tool that presents the plan and, once
// approved, restores the previous mode.
func NewExitPlanModeTool(service permission.Service, state *PlanModeState) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ExitPlanModeToolName,
		exitPlanModeDescription,
		func(ctx context.Context, params ExitPlanModeParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			modeService, ok := service.(permission.ModeService)
			if !ok {
				return fantasy.NewTextErrorResponse("Plan mode is not available: the permission service does not support modes."), nil
			}
			if modeService.Mode() != permission.ModePlan {
				return fantasy.NewTextErrorResponse("Not in plan mode; nothing to exit."), nil
			}
			if params.Plan == "" {
				return fantasy.NewTextErrorResponse("plan is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session ID is required to exit plan mode")
			}

			outcome, err := RequestApproval(ctx, service, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				ToolCallID:  call.ID,
				ToolName:    ExitPlanModeToolName,
				Action:      "approve plan",
				Description: "Approve the plan and leave plan mode",
				RuleContent: "exit",
				Params:      params,
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if outcome != ApprovalAllowedOnce {
				return fantasy.NewTextErrorResponse(
					"The plan was not approved. Stay in plan mode, revise the plan, and call exit_plan_mode again with the changes.",
				), nil
			}

			state.mu.Lock()
			next := state.previous
			if next == permission.ModePlan || next == "" {
				next = permission.ModeDefault
			}
			state.active = false
			state.mu.Unlock()

			modeService.SetMode(next)
			return fantasy.NewTextResponse(fmt.Sprintf(
				"Plan approved. Plan mode is off; the session is back in %s mode. Start implementing the approved plan.",
				string(next),
			)), nil
		},
	)
}
