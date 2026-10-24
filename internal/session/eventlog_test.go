package session

import (
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func newTestEventLog(t *testing.T) (*EventLog, string) {
	t.Helper()

	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	// Release, not Close: the pool keys connections by data directory and
	// would otherwise hand a closed handle to the next test that connects.
	t.Cleanup(func() { require.NoError(t, db.Release(dataDir)) })

	_, err = db.New(conn).CreateSession(t.Context(), db.CreateSessionParams{
		ID:    "event-session",
		Title: "Events",
	})
	require.NoError(t, err)
	return NewEventLog(conn), "event-session"
}

func TestEventLogAppendsContiguousSequence(t *testing.T) {
	t.Parallel()

	log, sessionID := newTestEventLog(t)
	first, err := log.AppendEvent(t.Context(), Event{SessionID: sessionID, Type: EventToolCall, Data: `{"tool_call_id":"a"}`})
	require.NoError(t, err)
	second, err := log.AppendEvent(t.Context(), Event{SessionID: sessionID, Type: EventToolResult, Data: `{"tool_call_id":"a"}`})
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Seq)
	require.Equal(t, int64(2), second.Seq)

	events, err := log.ListEvents(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, events, 2)
}

func TestEventLogRejectsSequenceGap(t *testing.T) {
	t.Parallel()

	log, sessionID := newTestEventLog(t)
	_, err := log.AppendEvent(t.Context(), Event{SessionID: sessionID, Seq: 2, Type: EventToolCall})
	require.ErrorIs(t, err, ErrEventGap)
}

func TestEventLogRepairsDanglingToolCall(t *testing.T) {
	t.Parallel()

	log, sessionID := newTestEventLog(t)
	callData, err := json.Marshal(map[string]any{
		"tool_call_id": "call-crash",
		"tool_name":    "edit",
	})
	require.NoError(t, err)
	_, err = log.AppendEvent(t.Context(), Event{
		SessionID: sessionID,
		Type:      EventToolCall,
		Data:      string(callData),
	})
	require.NoError(t, err)

	repaired, err := log.RepairInterruptedEvents(t.Context(), sessionID)
	require.NoError(t, err)
	require.Equal(t, 1, repaired)

	events, err := log.ListEvents(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, events, 2, "repair must append, not truncate the valid prefix")
	require.Equal(t, EventToolResult, events[1].Type)
	var result struct {
		ToolCallID string `json:"tool_call_id"`
		Outcome    string `json:"outcome"`
		Synthetic  bool   `json:"synthetic"`
	}
	require.NoError(t, json.Unmarshal([]byte(events[1].Data), &result))
	require.Equal(t, "call-crash", result.ToolCallID)
	require.Equal(t, "unknown", result.Outcome)
	require.True(t, result.Synthetic)

	// Repair is idempotent: a second pass finds the synthetic result.
	repaired, err = log.RepairInterruptedEvents(t.Context(), sessionID)
	require.NoError(t, err)
	require.Zero(t, repaired)
}
