package agent

import (
	"context"
	"log/slog"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/shadowgit"
)

// checkpointTool wraps a file-mutating fantasy.AgentTool so a shadow
// checkpoint is taken before the tool runs. The snapshot therefore
// captures the state right before the mutation, which is the state the
// session had at the message containing this tool call — exactly the
// unit the /rewind UI operates on.
type checkpointTool struct {
	inner fantasy.AgentTool
	svc   shadowgit.Service
}

// mutationToolNames lists the tools that can modify the working tree.
var mutationToolNames = map[string]bool{
	tools.BashToolName:          true,
	tools.EditToolName:          true,
	tools.MultiEditToolName:     true,
	tools.WriteToolName:         true,
	tools.DownloadToolName:      true,
	tools.RenameToolName:        true,
	tools.ReplaceSymbolToolName: true,
}

// wrapCheckpointTools wraps every mutating tool in the slice with a
// checkpointTool. Read-only tools are returned unchanged. A nil or
// disabled service returns the slice untouched.
func wrapCheckpointTools(ts []fantasy.AgentTool, svc shadowgit.Service) []fantasy.AgentTool {
	if svc == nil || !svc.Enabled() {
		return ts
	}
	out := make([]fantasy.AgentTool, 0, len(ts))
	for _, tool := range ts {
		if mutationToolNames[tool.Info().Name] {
			tool = &checkpointTool{inner: tool, svc: svc}
		}
		out = append(out, tool)
	}
	return out
}

func (t *checkpointTool) Info() fantasy.ToolInfo {
	return t.inner.Info()
}

func (t *checkpointTool) ProviderOptions() fantasy.ProviderOptions {
	return t.inner.ProviderOptions()
}

func (t *checkpointTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.inner.SetProviderOptions(opts)
}

func (t *checkpointTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	meta := shadowgit.Meta{
		SessionID:  tools.GetSessionFromContext(ctx),
		MessageID:  tools.GetMessageFromContext(ctx),
		ToolCallID: call.ID,
		ToolName:   call.Name,
	}
	if _, err := t.svc.Snapshot(ctx, meta); err != nil {
		// Checkpoints are best-effort: a failure must never block the
		// tool the agent asked to run.
		slog.Warn("Shadow checkpoint skipped",
			"tool", call.Name, "error", err)
	}
	return t.inner.Run(ctx, call)
}
