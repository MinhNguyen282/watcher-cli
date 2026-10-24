package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/charmbracelet/crush/internal/db"
)

// EventFormatVersion identifies the durable event payload shape. It is
// separate from the SQLite schema version: a reader can understand an older
// schema's events as long as the format version matches.
const EventFormatVersion = 1

// Event types written by the agent loop and dispatch pipeline.
const (
	EventToolCall   = "tool/call"
	EventToolResult = "tool/result"
	EventTurnEnd    = "turn/end"
)

// ErrEventGap means an append would leave a hole in a session's sequence.
var ErrEventGap = errors.New("session event sequence gap")

// Event is one append-only record in a session's durable history.
type Event struct {
	SessionID string `json:"session_id"`
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	CreatedAt int64  `json:"created_at"`
	Data      string `json:"data"`
	Ignorable bool   `json:"ignorable"`
}

// EventLogService is the durable append-only history used to rebuild what
// the model saw. It is optional so existing session.Service implementations
// keep working.
type EventLogService interface {
	AppendEvent(ctx context.Context, event Event) (Event, error)
	ListEvents(ctx context.Context, sessionID string) ([]Event, error)
	RepairInterruptedEvents(ctx context.Context, sessionID string) (int, error)
}

// AppendEvent appends one event at the next contiguous sequence number.
func (s *service) AppendEvent(ctx context.Context, event Event) (Event, error) {
	if s.db == nil {
		return Event{}, errors.New("session database unavailable")
	}
	if event.SessionID == "" {
		return Event{}, errors.New("session ID is required")
	}
	if event.Type == "" {
		return Event{}, errors.New("event type is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("beginning event transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var maxSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM session_events WHERE session_id = ?`,
		event.SessionID,
	).Scan(&maxSeq); err != nil {
		return Event{}, fmt.Errorf("reading event sequence: %w", err)
	}
	next := maxSeq + 1
	if event.Seq != 0 && event.Seq != next {
		return Event{}, fmt.Errorf("%w: got %d, want %d", ErrEventGap, event.Seq, next)
	}
	event.Seq = next
	if event.CreatedAt == 0 {
		event.CreatedAt = time.Now().Unix()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_events (session_id, seq, type, created_at, data, ignorable)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.SessionID, event.Seq, event.Type, event.CreatedAt, event.Data, boolToInt(event.Ignorable),
	); err != nil {
		return Event{}, fmt.Errorf("inserting event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("committing event: %w", err)
	}
	return event, nil
}

// ListEvents returns a session's event log in sequence order.
func (s *service) ListEvents(ctx context.Context, sessionID string) ([]Event, error) {
	if s.db == nil {
		return nil, errors.New("session database unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, seq, type, created_at, data, ignorable
		FROM session_events
		WHERE session_id = ?
		ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var events []Event
	for rows.Next() {
		var event Event
		var ignorable int
		if err := rows.Scan(&event.SessionID, &event.Seq, &event.Type, &event.CreatedAt, &event.Data, &ignorable); err != nil {
			return nil, fmt.Errorf("scanning event: %w", err)
		}
		event.Ignorable = ignorable != 0
		events = append(events, event)
	}
	return events, rows.Err()
}

// RepairInterruptedEvents gives every tool call in the log a result, even
// when the process died between dispatch and completion. It never truncates
// a valid prefix; synthetic results are appended after it.
func (s *service) RepairInterruptedEvents(ctx context.Context, sessionID string) (int, error) {
	events, err := s.ListEvents(ctx, sessionID)
	if err != nil {
		return 0, err
	}

	results := make(map[string]struct{})
	for _, event := range events {
		if event.Type != EventToolResult {
			continue
		}
		var data struct {
			ToolCallID string `json:"tool_call_id"`
		}
		if json.Unmarshal([]byte(event.Data), &data) == nil && data.ToolCallID != "" {
			results[data.ToolCallID] = struct{}{}
		}
	}

	repaired := 0
	for _, event := range events {
		if event.Type != EventToolCall {
			continue
		}
		var call struct {
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
		}
		if json.Unmarshal([]byte(event.Data), &call) != nil || call.ToolCallID == "" {
			continue
		}
		if _, ok := results[call.ToolCallID]; ok {
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"tool_call_id": call.ToolCallID,
			"tool_name":    call.ToolName,
			"outcome":      "unknown",
			"synthetic":    true,
			"content":      "tool call was interrupted before it produced a result",
		})
		if err != nil {
			return repaired, err
		}
		if _, err := s.AppendEvent(ctx, Event{
			SessionID: sessionID,
			Type:      EventToolResult,
			Data:      string(payload),
		}); err != nil {
			return repaired, err
		}
		results[call.ToolCallID] = struct{}{}
		repaired++
	}
	if repaired > 0 {
		slog.Warn("Repaired interrupted tool calls in event log",
			"session_id", sessionID, "count", repaired)
	}
	return repaired, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Ensure the concrete service satisfies the optional event interface.
var _ EventLogService = (*service)(nil)

// NewEventLog is a small adapter for callers that only have a database
// handle and want to append events without the full session service.
type EventLog struct {
	db *sql.DB
}

// NewEventLog creates an event log over the given database.
func NewEventLog(conn *sql.DB) *EventLog {
	return &EventLog{db: conn}
}

// AppendEvent appends one event at the next contiguous sequence number.
func (l *EventLog) AppendEvent(ctx context.Context, event Event) (Event, error) {
	return (&service{db: l.db, q: db.New(l.db)}).AppendEvent(ctx, event)
}

// ListEvents returns a session's events in sequence order.
func (l *EventLog) ListEvents(ctx context.Context, sessionID string) ([]Event, error) {
	return (&service{db: l.db, q: db.New(l.db)}).ListEvents(ctx, sessionID)
}

// RepairInterruptedEvents appends synthetic results for dangling calls.
func (l *EventLog) RepairInterruptedEvents(ctx context.Context, sessionID string) (int, error) {
	return (&service{db: l.db, q: db.New(l.db)}).RepairInterruptedEvents(ctx, sessionID)
}

var _ EventLogService = (*EventLog)(nil)
