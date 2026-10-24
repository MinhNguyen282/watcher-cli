package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
)

// hookGrace bounds a hook that runs on the terminal path of a turn. A hook
// must never keep a finished turn open.
const hookGrace = 2 * time.Second

// promptBlockedByHook runs UserPromptSubmit for this turn. It returns the
// reason when the turn must not reach the model. Hook failures are logged
// and treated as "no opinion": a broken hook cannot silently freeze the CLI.
func (a *sessionAgent) promptBlockedByHook(ctx context.Context, call SessionAgentCall) (string, bool) {
	if a.hooks == nil || a.isSubAgent || !a.hooks.HasEvent(hooks.EventUserPromptSubmit) {
		return "", false
	}
	input, err := json.Marshal(map[string]string{"prompt": call.Prompt})
	if err != nil {
		return "", false
	}
	result, err := a.hooks.Run(ctx, hooks.EventUserPromptSubmit, call.SessionID, "", string(input))
	if err != nil {
		slog.Warn("UserPromptSubmit hook failed, continuing", "session_id", call.SessionID, "error", err)
		return "", false
	}
	if result.Decision != hooks.DecisionDeny && !result.Halt {
		return "", false
	}
	reason := result.Reason
	if reason == "" {
		reason = "denied by UserPromptSubmit hook"
	}
	return reason, true
}

// emitStopHook notifies observers that the turn ended. It is observational
// and bounded, so it cannot delay or change the terminal event.
func (a *sessionAgent) emitStopHook(ctx context.Context, call SessionAgentCall, assistant *message.Message) {
	if a.hooks == nil || a.isSubAgent || !a.hooks.HasEvent(hooks.EventStop) {
		return
	}
	payload := map[string]string{"session_id": call.SessionID}
	if assistant != nil {
		payload["message_id"] = assistant.ID
		payload["text"] = assistant.Content().String()
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return
	}
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookGrace)
	defer cancel()
	if _, err := a.hooks.Run(hookCtx, hooks.EventStop, call.SessionID, "", string(input)); err != nil {
		slog.Warn("Stop hook failed", "session_id", call.SessionID, "error", err)
	}
}

// preCompactBlocked runs PreCompact and reports whether compaction must be
// skipped. A broken hook does not block compaction.
func (a *sessionAgent) preCompactBlocked(ctx context.Context, sessionID string) (bool, string) {
	if a.hooks == nil || a.isSubAgent || !a.hooks.HasEvent(hooks.EventPreCompact) {
		return false, ""
	}
	input, err := json.Marshal(map[string]string{"session_id": sessionID})
	if err != nil {
		return false, ""
	}
	result, err := a.hooks.Run(ctx, hooks.EventPreCompact, sessionID, "", string(input))
	if err != nil {
		slog.Warn("PreCompact hook failed, continuing", "session_id", sessionID, "error", err)
		return false, ""
	}
	if result.Decision != hooks.DecisionDeny && !result.Halt {
		return false, ""
	}
	reason := result.Reason
	if reason == "" {
		reason = "denied by PreCompact hook"
	}
	return true, reason
}

// emitPostCompact notifies observers that a summary was saved. Observational
// and bounded.
func (a *sessionAgent) emitPostCompact(ctx context.Context, sessionID, summaryMessageID string) {
	if a.hooks == nil || a.isSubAgent || !a.hooks.HasEvent(hooks.EventPostCompact) {
		return
	}
	input, err := json.Marshal(map[string]string{
		"session_id":         sessionID,
		"summary_message_id": summaryMessageID,
	})
	if err != nil {
		return
	}
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookGrace)
	defer cancel()
	if _, err := a.hooks.Run(hookCtx, hooks.EventPostCompact, sessionID, "", string(input)); err != nil {
		slog.Warn("PostCompact hook failed", "session_id", sessionID, "error", err)
	}
}

// emitSessionStart announces a session's first turn. Observational.
func (a *sessionAgent) emitSessionStart(ctx context.Context, call SessionAgentCall) {
	if a.hooks == nil || a.isSubAgent || !a.hooks.HasEvent(hooks.EventSessionStart) {
		return
	}
	if _, seen := a.started.Get(call.SessionID); seen {
		return
	}
	a.started.Set(call.SessionID, true)
	input, err := json.Marshal(map[string]string{"session_id": call.SessionID, "prompt": call.Prompt})
	if err != nil {
		return
	}
	if _, err := a.hooks.Run(ctx, hooks.EventSessionStart, call.SessionID, "", string(input)); err != nil {
		slog.Warn("SessionStart hook failed", "session_id", call.SessionID, "error", err)
	}
}

// emitPostToolBatch reports the tool calls a step produced, once per step.
func (a *sessionAgent) emitPostToolBatch(ctx context.Context, sessionID string, step fantasy.StepResult) {
	if a.hooks == nil || a.isSubAgent || !a.hooks.HasEvent(hooks.EventPostToolBatch) {
		return
	}
	type call struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var calls []call
	for _, msg := range step.Messages {
		for _, part := range msg.Content {
			if toolCall, ok := part.(fantasy.ToolCallPart); ok {
				calls = append(calls, call{ID: toolCall.ToolCallID, Name: toolCall.ToolName})
			}
		}
	}
	if len(calls) == 0 {
		return
	}
	input, err := json.Marshal(map[string]any{"session_id": sessionID, "tool_calls": calls})
	if err != nil {
		return
	}
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookGrace)
	defer cancel()
	if _, err := a.hooks.Run(hookCtx, hooks.EventPostToolBatch, sessionID, "", string(input)); err != nil {
		slog.Warn("PostToolBatch hook failed", "session_id", sessionID, "error", err)
	}
}

// emitCwdChanged reports a working-directory change discovered by the
// environment snapshot.
func (a *sessionAgent) emitCwdChanged(ctx context.Context, sessionID, workingDir string) {
	if a.hooks == nil || a.isSubAgent || !a.hooks.HasEvent(hooks.EventCwdChanged) {
		return
	}
	input, err := json.Marshal(map[string]string{"session_id": sessionID, "working_directory": workingDir})
	if err != nil {
		return
	}
	if _, err := a.hooks.Run(ctx, hooks.EventCwdChanged, sessionID, "", string(input)); err != nil {
		slog.Warn("CwdChanged hook failed", "session_id", sessionID, "error", err)
	}
}
