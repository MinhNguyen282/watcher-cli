package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/db"
)

// Part tags mirror internal/message's wire format, kept local to
// avoid importing the message package.
const (
	partText       = "text"
	partToolCall   = "tool_call"
	partToolResult = "tool_result"
)

// dbPart is the raw JSON shape of one stored content part.
type dbPart struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// dbToolCall is the JSON shape of a stored tool call part.
type dbToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

// dbToolResult is the JSON shape of a stored tool result part.
type dbToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

// dbMessageRow is the subset of the messages table the extractor
// reads.
type dbMessageRow struct {
	SessionID string
	Role      string
	Parts     string
	CreatedAt int64
}

// ExtractTrace builds an ExecutionTrace for a finished probe run. The
// tool call history is recovered from the probe's own SQLite database
// (each probe gets an isolated data directory), the diff from the
// probe's worktree, and the final output from both the database and
// the captured stdout. Extraction problems never fail the probe: a
// partial trace is still useful to the compose step.
func ExtractTrace(
	ctx context.Context,
	dataDir string,
	spec ProbeSpec,
	stdout, stderr string,
	startedAt time.Time,
	duration time.Duration,
	runErr error,
) *ExecutionTrace {
	// Extraction must work even when the probe's own context was
	// canceled (e.g. a timeout): the database and the worktree still
	// hold the evidence of what the agent did before it was stopped.
	ctx = context.WithoutCancel(ctx)

	trace := &ExecutionTrace{
		ProbeID:     spec.ID,
		TaskPrompt:  spec.Task,
		WorkingDir:  spec.WorkDir,
		Branch:      spec.Branch,
		ProbeOutput: strings.TrimSpace(stderr),
		StartedAt:   startedAt,
		Duration:    duration,
		DurationMs:  duration.Milliseconds(),
	}
	if spec.Harness != nil {
		trace.HarnessID = spec.Harness.ID
	}
	if runErr != nil {
		trace.Error = runErr.Error()
	}
	if stdout != "" {
		trace.FinalOutput = strings.TrimSpace(stdout)
	}

	trace.GitDiff = diffOfWorkdir(ctx, spec.WorkDir)

	parts := readProbeParts(ctx, dataDir)
	trace.ToolCalls = buildToolCalls(parts)
	if final := finalText(parts); final != "" {
		trace.FinalOutput = final
	}
	return trace
}

// readProbeParts loads the raw part records from the probe's message
// store. Every probe runs with an isolated data directory, so its
// database contains exactly the messages of that one run.
func readProbeParts(ctx context.Context, dataDir string) []dbPart {
	dbPath := filepath.Join(dataDir, "crush.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	conn, err := db.ConnectReadOnly(ctx, dbPath)
	if err != nil {
		return nil
	}
	defer conn.Close()

	rows, err := conn.QueryContext(ctx,
		`SELECT session_id, role, parts, created_at FROM messages ORDER BY created_at, rowid`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var parts []dbPart
	for rows.Next() {
		var row dbMessageRow
		if err := rows.Scan(&row.SessionID, &row.Role, &row.Parts, &row.CreatedAt); err != nil {
			continue
		}
		var wrapped []dbPart
		if err := json.Unmarshal([]byte(row.Parts), &wrapped); err != nil {
			continue
		}
		parts = append(parts, wrapped...)
	}
	return parts
}

// buildToolCalls pairs tool call parts with their results and trims
// oversized outputs so traces stay digestible for the editor LLM.
func buildToolCalls(parts []dbPart) []ToolCallRecord {
	const maxOutput = 2_000

	truncate := func(s string) string {
		if len(s) > maxOutput {
			return s[:maxOutput] + "\n... (truncated)"
		}
		return s
	}

	byID := make(map[string]*ToolCallRecord)
	var order []string

	for _, p := range parts {
		switch p.Type {
		case partToolCall:
			var tc dbToolCall
			if err := json.Unmarshal(p.Data, &tc); err != nil || tc.ID == "" {
				continue
			}
			byID[tc.ID] = &ToolCallRecord{
				ID:    tc.ID,
				Name:  tc.Name,
				Input: truncate(tc.Input),
			}
			order = append(order, tc.ID)
		case partToolResult:
			var tr dbToolResult
			if err := json.Unmarshal(p.Data, &tr); err != nil {
				continue
			}
			if rec, ok := byID[tr.ToolCallID]; ok {
				rec.Output = truncate(tr.Content)
				rec.IsError = tr.IsError
			}
		}
	}

	seen := make(map[string]bool)
	records := make([]ToolCallRecord, 0, len(order))
	for _, id := range order {
		rec := byID[id]
		if rec == nil || seen[id] {
			continue
		}
		seen[id] = true
		records = append(records, *rec)
	}
	return records
}

// finalText concatenates the text parts of the assistant messages.
func finalText(parts []dbPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type != partText {
			continue
		}
		var t struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(p.Data, &t); err != nil {
			continue
		}
		sb.WriteString(t.Text)
	}
	return strings.TrimSpace(sb.String())
}

// diffOfWorkdir captures the code changes in a probe worktree. The
// .watcher/ directory is untracked, so harness artifacts never leak
// into the diff.
func diffOfWorkdir(ctx context.Context, workDir string) string {
	if workDir == "" {
		return ""
	}
	cmd := exec.CommandContext(ctx, "git", "diff")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// FormatTraceForLLM renders a trace as compact markdown for the
// propose and compose prompts.
func FormatTraceForLLM(t *ExecutionTrace) string {
	if t == nil {
		return "(no trace)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Probe %s (harness %q)\n", t.ProbeID, t.HarnessID)
	if t.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", t.Error)
	}
	fmt.Fprintf(&sb, "Duration: %s\n", t.Duration.Round(time.Millisecond))

	stats := t.ComputeStats()
	toolNames := make([]string, 0, len(stats.ByTool))
	for name := range stats.ByTool {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)

	fmt.Fprintf(&sb, "Tool calls: %d (errors: %d)\n", stats.ToolCalls, stats.ToolErrors)
	if len(toolNames) > 0 {
		var counts []string
		for _, name := range toolNames {
			counts = append(counts, fmt.Sprintf("%s=%d", name, stats.ByTool[name]))
		}
		fmt.Fprintf(&sb, "By tool: %s\n", strings.Join(counts, ", "))
	}

	if len(t.ToolCalls) > 0 {
		sb.WriteString("\n### Tool call history\n")
		for _, tc := range t.ToolCalls {
			line := fmt.Sprintf("- %s: %s", tc.Name, oneLine(tc.Input, 160))
			if tc.IsError {
				line += " (error: " + oneLine(tc.Output, 160) + ")"
			}
			sb.WriteString(line + "\n")
		}
	}

	if t.GitDiff != "" {
		sb.WriteString("\n### Git diff\n```diff\n")
		diff := t.GitDiff
		if len(diff) > 12_000 {
			diff = diff[:12_000] + "\n... (diff truncated)"
		}
		sb.WriteString(diff)
		sb.WriteString("\n```\n")
	} else {
		sb.WriteString("\n### Git diff\n(no changes)\n")
	}

	if t.FinalOutput != "" {
		sb.WriteString("\n### Final output\n")
		out := t.FinalOutput
		if len(out) > 4_000 {
			out = out[:4_000] + "\n... (truncated)"
		}
		sb.WriteString(out)
		sb.WriteString("\n")
	}
	return sb.String()
}

// oneLine flattens a string for single-line display.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
