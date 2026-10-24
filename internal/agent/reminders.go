package agent

import (
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
)

// reminderOpen and reminderClose wrap application-originated context that
// arrives mid-conversation. The model is told in the system prompt that
// these are not user messages.
const (
	reminderOpen  = "<system-reminder>"
	reminderClose = "</system-reminder>"
)

// reminderMessage builds a hidden user message carrying a system reminder.
// Hidden keeps it out of the transcript the user reads while still reaching
// the model.
func reminderMessage(text string) message.Message {
	return message.Message{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{
				Text:   reminderOpen + "\n" + strings.TrimSpace(text) + "\n" + reminderClose,
				Hidden: true,
			},
		},
	}
}

// permissionDeniedReminder explains a denial once, at the next step, instead
// of leaving the model to guess why its call vanished.
func permissionDeniedReminder(toolName string) string {
	return fmt.Sprintf(
		"A tool call (%s) was denied by the user or by policy. "+
			"Treat that as a decision, not a transient failure: adjust your approach, "+
			"and do not retry the same call with the same arguments.",
		toolName,
	)
}

// insertReminder places a reminder immediately before the newest user
// message, so it reads as context for that turn rather than as a new
// instruction from the user.
func insertReminder(messages []fantasy.Message, reminder fantasy.Message) []fantasy.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != fantasy.MessageRoleUser {
			continue
		}
		out := make([]fantasy.Message, 0, len(messages)+1)
		out = append(out, messages[:i]...)
		out = append(out, reminder)
		out = append(out, messages[i:]...)
		return out
	}
	return append(messages, reminder)
}

// queueReminder records a reminder for the next step of a session.
func (a *sessionAgent) queueReminder(sessionID, text string) {
	if sessionID == "" || strings.TrimSpace(text) == "" {
		return
	}
	existing, _ := a.reminders.Get(sessionID)
	a.reminders.Set(sessionID, append(existing, text))
}

// drainReminders returns and clears the reminders queued for a session.
func (a *sessionAgent) drainReminders(sessionID string) []string {
	reminders, ok := a.reminders.Get(sessionID)
	if !ok || len(reminders) == 0 {
		return nil
	}
	a.reminders.Del(sessionID)
	return reminders
}

// reminderMessageFor turns the queued reminders into one hidden message.
func reminderMessageFor(reminders []string) (message.Message, bool) {
	if len(reminders) == 0 {
		return message.Message{}, false
	}
	return reminderMessage(strings.Join(reminders, "\n\n")), true
}
