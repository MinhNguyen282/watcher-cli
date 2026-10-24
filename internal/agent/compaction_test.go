package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestRecentlyViewedFiles(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{ID: "1", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "a", Name: "view", Input: `{"file_path":"/repo/old.go"}`, Finished: true},
		}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "b", Name: "view", Input: `{"file_path":"/repo/new.go"}`, Finished: true},
			message.ToolCall{ID: "c", Name: "view", Input: `{"file_path":"/repo/old.go"}`, Finished: true},
			message.ToolCall{ID: "d", Name: "edit", Input: `{"file_path":"/repo/edited.go"}`, Finished: true},
		}},
	}

	files := recentlyViewedFiles(msgs, 3)
	require.Equal(t, []string{"/repo/new.go", "/repo/old.go"}, files,
		"most recent first, no duplicates, only view calls")

	require.Len(t, recentlyViewedFiles(msgs, 1), 1)
	require.Empty(t, recentlyViewedFiles(msgs, 0))
}

func TestBuildWorkingSetMessageRereadsFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(file, []byte("package main\n"), 0o644))

	msgs := []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "a", Name: "view", Input: `{"file_path":"main.go"}`, Finished: true},
		}},
	}

	text := buildWorkingSetMessage(msgs, dir, "/tmp/transcript.jsonl")
	require.Contains(t, text, "Working set reconstructed after compaction")
	require.Contains(t, text, filepath.Base(file))
	require.Contains(t, text, "package main")
	require.Contains(t, text, "Full transcript: /tmp/transcript.jsonl")
}

func TestBuildWorkingSetMessageSkipsMissingFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	msgs := []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "a", Name: "view", Input: `{"file_path":"gone.go"}`, Finished: true},
		}},
	}

	text := buildWorkingSetMessage(msgs, dir, "")
	require.NotContains(t, text, "gone.go")
	require.NotContains(t, text, "<file path=")
	require.NotContains(t, text, "Full transcript")
}

func TestBuildWorkingSetMessageTruncatesLongFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "big.txt")
	require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("x", workingSetFileBudget*2)), 0o644))

	msgs := []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "a", Name: "view", Input: `{"file_path":"big.txt"}`, Finished: true},
		}},
	}

	text := buildWorkingSetMessage(msgs, dir, "")
	require.Less(t, len(text), workingSetFileBudget+1024)
	require.Contains(t, text, "[truncated]")
}

func TestMicrocompactMessages(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("y", microcompactMinContent+1)
	makeMsgs := func() []fantasy.Message {
		return []fantasy.Message{
			{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{ToolCallID: "old", Output: fantasy.ToolResultOutputContentText{Text: big}},
			}},
			{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{ToolCallID: "recent", Output: fantasy.ToolResultOutputContentText{Text: big}},
			}},
			{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
				fantasy.TextPart{Text: "assistant"},
			}},
		}
	}

	msgs := makeMsgs()
	out, pruned := microcompactMessages(msgs, 2)
	require.Equal(t, 1, pruned, "only the message outside the recent window is pruned")

	old := out[0].Content[0].(fantasy.ToolResultPart)
	require.Contains(t, old.Output.(fantasy.ToolResultOutputContentText).Text, "omitted by microcompaction")

	recent := out[1].Content[0].(fantasy.ToolResultPart)
	require.Equal(t, big, recent.Output.(fantasy.ToolResultOutputContentText).Text)

	// The input slice is not mutated: the stored history stays intact.
	original := msgs[0].Content[0].(fantasy.ToolResultPart)
	require.Equal(t, big, original.Output.(fantasy.ToolResultOutputContentText).Text)
}

func TestMicrocompactMessagesKeepsSmallResults(t *testing.T) {
	t.Parallel()

	msgs := []fantasy.Message{
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			fantasy.ToolResultPart{ToolCallID: "small", Output: fantasy.ToolResultOutputContentText{Text: "ok"}},
		}},
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "done"}}},
	}

	out, pruned := microcompactMessages(msgs, 0)
	require.Zero(t, pruned)
	require.Equal(t, "ok", out[0].Content[0].(fantasy.ToolResultPart).Output.(fantasy.ToolResultOutputContentText).Text)
}
