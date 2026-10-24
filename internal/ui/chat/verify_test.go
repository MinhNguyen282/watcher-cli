package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestVerifyToolMessageItemRendersRunAndFailures(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	call := message.ToolCall{
		ID:       "verify1",
		Name:     "verify",
		Input:    `{"full":true}`,
		Finished: true,
	}
	result := &message.ToolResult{
		ToolCallID: "verify1",
		Content:    "<verify status=\"failed\" profile=\"go\">\n- internal/a/a_test.go:12 [TestA]: boom\n</verify>",
	}

	item := NewVerifyToolMessageItem(&sty, call, result, false)
	verify, ok := item.(*VerifyToolMessageItem)
	require.True(t, ok)
	require.False(t, verify.expandedContent, "verify items must default to collapsed")

	out := ansi.Strip(verify.RawRender(120))
	require.Contains(t, out, "Verify")
	require.Contains(t, out, "full")
	require.Contains(t, out, "TestA")
}
