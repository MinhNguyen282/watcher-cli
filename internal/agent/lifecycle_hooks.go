package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/hooks"
)

// instructionsObserver returns a context-file callback for the prompt
// builder. Every agent build loads the same files, so the hook fires once
// per coordinator rather than once per agent.
func (c *coordinator) instructionsObserver(ctx context.Context) func(contextFiles, globalContextFiles []prompt.ContextFile) {
	return func(contextFiles, globalContextFiles []prompt.ContextFile) {
		if c.hookRunner == nil || !c.hookRunner.HasEvent(hooks.EventInstructionsLoaded) {
			return
		}
		c.instructionsOnce.Do(func() {
			paths := make([]string, 0, len(contextFiles)+len(globalContextFiles))
			for _, file := range contextFiles {
				paths = append(paths, file.Path)
			}
			for _, file := range globalContextFiles {
				paths = append(paths, file.Path)
			}
			input, err := json.Marshal(map[string]any{
				"working_directory": c.cfg.WorkingDir(),
				"files":             paths,
			})
			if err != nil {
				return
			}
			hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookGrace)
			defer cancel()
			if _, err := c.hookRunner.Run(hookCtx, hooks.EventInstructionsLoaded, "", "", string(input)); err != nil {
				slog.Warn("InstructionsLoaded hook failed", "error", err)
			}
		})
	}
}

// emitNotificationHook reports a user-facing notification. Observational and
// bounded: a broken hook cannot suppress the notification itself.
func (c *coordinator) emitNotificationHook(ctx context.Context, sessionID, title, message string) {
	if c.hookRunner == nil || !c.hookRunner.HasEvent(hooks.EventNotification) {
		return
	}
	input, err := json.Marshal(map[string]string{
		"session_id": sessionID,
		"title":      title,
		"message":    message,
	})
	if err != nil {
		return
	}
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookGrace)
	defer cancel()
	if _, err := c.hookRunner.Run(hookCtx, hooks.EventNotification, sessionID, "", string(input)); err != nil {
		slog.Warn("Notification hook failed", "session_id", sessionID, "error", err)
	}
}

// SessionEnd announces that a session is finished. It is idempotent per
// session: a delete and a shutdown for the same session fire one hook.
func (c *coordinator) SessionEnd(ctx context.Context, sessionID string) {
	if sessionID == "" || c.hookRunner == nil || !c.hookRunner.HasEvent(hooks.EventSessionEnd) {
		return
	}
	if _, done := c.endedSessions.LoadOrStore(sessionID, struct{}{}); done {
		return
	}
	input, err := json.Marshal(map[string]string{"session_id": sessionID})
	if err != nil {
		return
	}
	hookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookGrace)
	defer cancel()
	if _, err := c.hookRunner.Run(hookCtx, hooks.EventSessionEnd, sessionID, "", string(input)); err != nil {
		slog.Warn("SessionEnd hook failed", "session_id", sessionID, "error", err)
	}
}

// Shutdown ends the coordinator's ownership of its sessions: background
// sub-agents are cancelled and every session this process touched gets one
// SessionEnd.
func (c *coordinator) Shutdown(ctx context.Context) {
	c.cancelAllSubagents()
	if c.touchedSessions == nil {
		return
	}
	for sessionID := range c.touchedSessions.Seq2() {
		c.SessionEnd(ctx, sessionID)
	}
}
