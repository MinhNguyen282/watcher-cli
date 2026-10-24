package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestReminderMessageIsHiddenAndWrapped(t *testing.T) {
	t.Parallel()

	msg := reminderMessage("A tool call was denied.")
	require.Equal(t, message.User, msg.Role)
	require.Len(t, msg.Parts, 1)

	text, ok := msg.Parts[0].(message.TextContent)
	require.True(t, ok)
	require.True(t, text.Hidden, "reminders must not appear as user turns")
	require.True(t, strings.HasPrefix(text.Text, reminderOpen))
	require.True(t, strings.HasSuffix(text.Text, reminderClose))
	require.Contains(t, text.Text, "A tool call was denied.")
}

func TestInsertReminderBeforeLastUserMessage(t *testing.T) {
	t.Parallel()

	messages := []fantasy.Message{
		fantasy.NewSystemMessage("system"),
		fantasy.NewUserMessage("first"),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "answer"}}},
		fantasy.NewUserMessage("second"),
	}
	reminder := fantasy.NewUserMessage("reminder")

	out := insertReminder(messages, reminder)
	require.Len(t, out, 5)
	require.Equal(t, "reminder", out[3].Content[0].(fantasy.TextPart).Text)
	require.Equal(t, "second", out[4].Content[0].(fantasy.TextPart).Text)
}

func TestInsertReminderWithoutUserMessage(t *testing.T) {
	t.Parallel()

	out := insertReminder([]fantasy.Message{fantasy.NewSystemMessage("system")}, fantasy.NewUserMessage("reminder"))
	require.Len(t, out, 2)
	require.Equal(t, "reminder", out[1].Content[0].(fantasy.TextPart).Text)
}

func TestReminderQueueDrainsOnce(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{reminders: csync.NewMap[string, []string]()}
	require.Empty(t, agent.drainReminders("session-1"))

	agent.queueReminder("session-1", permissionDeniedReminder("edit"))
	agent.queueReminder("session-1", "second reminder")

	reminders := agent.drainReminders("session-1")
	require.Len(t, reminders, 2)
	require.Contains(t, reminders[0], "edit")
	require.Empty(t, agent.drainReminders("session-1"), "reminders are delivered once")

	// A reminder for another session is not delivered here.
	agent.queueReminder("session-2", "other")
	require.Empty(t, agent.drainReminders("session-1"))
	require.Len(t, agent.drainReminders("session-2"), 1)
}

func TestPermissionDeniedReminderText(t *testing.T) {
	t.Parallel()

	text := permissionDeniedReminder("bash")
	require.Contains(t, text, "bash")
	require.Contains(t, text, "do not retry")
}
