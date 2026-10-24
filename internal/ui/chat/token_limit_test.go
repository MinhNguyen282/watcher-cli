package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestTokenLimitRendersVisibleRecoveryBanner(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		parts []message.ContentPart
	}{
		{"empty", nil},
		{"thinking_only", []message.ContentPart{message.ReasoningContent{Thinking: "Planning the next change.", StartedAt: 1, FinishedAt: 39}}},
		{"partial_answer", []message.ContentPart{message.TextContent{Text: "The first step is"}}},
		{"unfinished_tool", []message.ContentPart{message.ToolCall{ID: "partial-tool", Name: "edit"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sty := styles.CharmtonePantera()
			msg := &message.Message{ID: tc.name, Role: message.Assistant,
				Parts: append(tc.parts, message.Finish{Reason: message.FinishReasonMaxTokens, Time: testFinishTime})}
			require.True(t, ShouldRenderAssistantMessage(msg))
			item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
			out := ansi.Strip(item.Render(80))
			require.Contains(t, out, tokenLimitTagLabel)
			require.Contains(t, out, tokenLimitTitle)
			require.Contains(t, strings.Join(strings.Fields(out), " "), "output budget")
			require.Contains(t, out, "continue")
			require.NotContains(t, out, "ERROR")
		})
	}
}

func TestTokenLimitBannerAppearsAfterStreamingRender(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	msg := &message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Partial answer"}}}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	require.NotContains(t, item.Render(80), tokenLimitTitle)
	msg.AddFinish(message.FinishReasonMaxTokens, "", "")
	item.SetMessage(msg)
	out := ansi.Strip(item.Render(80))
	require.Contains(t, out, tokenLimitTitle)
	require.Contains(t, out, "Partial answer")
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, tokenLimitTagLabel) {
			require.Greater(t, i, 0)
			require.Empty(t, strings.TrimSpace(lines[i-1]), "The warning must be separated from the partial answer")
		}
		require.LessOrEqual(t, ansi.StringWidth(line), 80)
	}
}
