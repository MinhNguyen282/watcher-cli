package tools

import (
	"context"
	_ "embed"

	"charm.land/fantasy"
)

// ContextUsageToolName reports how the context window is being spent.
const ContextUsageToolName = "context_usage"

//go:embed context_usage.md
var contextUsageDescription string

type contextUsageParams struct{}

// NewContextUsageTool builds the read-only accounting tool. The report is
// supplied by the agent layer, which owns the prompt, the tool list and the
// session usage.
func NewContextUsageTool(report func(context.Context) string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ContextUsageToolName,
		contextUsageDescription,
		func(ctx context.Context, _ contextUsageParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if report == nil {
				return fantasy.NewTextErrorResponse("Context accounting is unavailable in this session."), nil
			}
			return fantasy.NewTextResponse(report(ctx)), nil
		},
	)
}
