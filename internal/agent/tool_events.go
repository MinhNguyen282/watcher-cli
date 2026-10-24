package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/session"
)

// maxEventContentBytes keeps a tool result from duplicating megabytes of
// output into the event log. The model-visible result remains complete; the
// log keeps enough to identify and diagnose the call.
const maxEventContentBytes = 8 << 10

// sessionToolEventRecorder writes tool-call intent and result events to the
// append-only session log. Both writes are best-effort: losing observability
// must not fail the model's tool call.
type sessionToolEventRecorder struct {
	log session.EventLogService
}

func newSessionToolEventRecorder(log session.EventLogService) ToolEventRecorder {
	return &sessionToolEventRecorder{log: log}
}

func (r *sessionToolEventRecorder) RecordToolCallIntent(ctx context.Context, call fantasy.ToolCall) {
	r.append(ctx, session.EventToolCall, map[string]any{
		"tool_call_id": call.ID,
		"tool_name":    call.Name,
		"input":        call.Input,
	})
}

func (r *sessionToolEventRecorder) RecordToolResult(ctx context.Context, call fantasy.ToolCall, response fantasy.ToolResponse, outcome string) {
	content := response.Content
	if len(content) > maxEventContentBytes {
		content = content[:maxEventContentBytes] + "\n... [event log content truncated] ..."
	}
	r.append(ctx, session.EventToolResult, map[string]any{
		"tool_call_id": call.ID,
		"tool_name":    call.Name,
		"outcome":      outcome,
		"is_error":     response.IsError,
		"stop_turn":    response.StopTurn,
		"content":      content,
	})
}

func (r *sessionToolEventRecorder) append(ctx context.Context, eventType string, payload map[string]any) {
	sessionID := tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("Failed to encode session event", "type", eventType, "error", err)
		return
	}
	if _, err := r.log.AppendEvent(ctx, session.Event{
		SessionID: sessionID,
		Type:      eventType,
		Data:      string(data),
	}); err != nil {
		slog.Warn("Failed to append session event",
			"session_id", sessionID, "type", eventType, "error", err)
	}
}
