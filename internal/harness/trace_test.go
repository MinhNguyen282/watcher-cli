package harness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func partsJSON(parts ...dbPart) []dbPart {
	return parts
}

func toolCallPart(id, name, input string) dbPart {
	data, _ := json.Marshal(dbToolCall{ID: id, Name: name, Input: input})
	return dbPart{Type: partToolCall, Data: data}
}

func toolResultPart(id, content string, isError bool) dbPart {
	data, _ := json.Marshal(dbToolResult{ToolCallID: id, Content: content, IsError: isError})
	return dbPart{Type: partToolResult, Data: data}
}

func textPart(text string) dbPart {
	data, _ := json.Marshal(map[string]string{"text": text})
	return dbPart{Type: partText, Data: data}
}

func TestBuildToolCallsPairsResults(t *testing.T) {
	t.Parallel()
	parts := partsJSON(
		toolCallPart("1", "bash", "go test ./..."),
		toolCallPart("2", "edit", `{"path":"a.go"}`),
		toolResultPart("1", "ok", false),
		toolResultPart("2", "file not found", true),
	)
	records := buildToolCalls(parts)
	require.Len(t, records, 2)

	require.Equal(t, "bash", records[0].Name)
	require.Equal(t, "ok", records[0].Output)
	require.False(t, records[0].IsError)

	require.Equal(t, "edit", records[1].Name)
	require.True(t, records[1].IsError)
}

func TestBuildToolCallsTruncatesOutput(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("z", 5000)
	parts := partsJSON(
		toolCallPart("1", "bash", "ls"),
		toolResultPart("1", big, false),
	)
	records := buildToolCalls(parts)
	require.Len(t, records, 1)
	require.Less(t, len(records[0].Output), 2100)
	require.Contains(t, records[0].Output, "(truncated)")
}

func TestBuildToolCallsIgnoresUnknownParts(t *testing.T) {
	t.Parallel()
	data, _ := json.Marshal(map[string]string{"nope": "true"})
	parts := partsJSON(dbPart{Type: "reasoning", Data: data})
	require.Empty(t, buildToolCalls(parts))
}

func TestFinalText(t *testing.T) {
	t.Parallel()
	parts := partsJSON(
		textPart("hello "),
		textPart("world"),
		toolCallPart("1", "bash", "ls"),
	)
	require.Equal(t, "hello world", finalText(parts))
}

func TestFormatTraceForLLM(t *testing.T) {
	t.Parallel()
	trace := &ExecutionTrace{
		ProbeID:    "cand-1",
		HarnessID:  "cand-1",
		TaskPrompt: "Fix it",
		ToolCalls: []ToolCallRecord{
			{ID: "1", Name: "bash", Input: "go test ./...", Output: "ok"},
			{ID: "2", Name: "edit", Input: `{"path":"a.go"}`, IsError: true},
		},
		GitDiff:     "diff --git a/x b/x",
		FinalOutput: "Done.",
		Duration:    2 * time.Second,
	}
	formatted := FormatTraceForLLM(trace)
	require.Contains(t, formatted, "Probe cand-1")
	require.Contains(t, formatted, "Tool calls: 2 (errors: 1)")
	require.Contains(t, formatted, "bash=1, edit=1")
	require.Contains(t, formatted, "diff --git")
	require.Contains(t, formatted, "Done.")
}

func TestFormatTraceForLLMNil(t *testing.T) {
	t.Parallel()
	require.Equal(t, "(no trace)", FormatTraceForLLM(nil))
}

func TestStats(t *testing.T) {
	t.Parallel()
	trace := &ExecutionTrace{ToolCalls: []ToolCallRecord{
		{Name: "bash"},
		{Name: "bash", IsError: true},
		{Name: "edit"},
	}}
	stats := trace.ComputeStats()
	require.Equal(t, 3, stats.ToolCalls)
	require.Equal(t, 1, stats.ToolErrors)
	require.Equal(t, 2, stats.ByTool["bash"])
}
