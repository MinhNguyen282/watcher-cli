package dialog

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestReasoningEffortCommandsDeepSeek(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	model := &catwalk.Model{
		ID:              "deepseek-flash",
		CanReason:       true,
		ReasoningLevels: []string{"low", "high", "max", "none"},
	}

	items := reasoningEffortCommands(&sty, "deepseek", model)
	require.Len(t, items, 5)
	require.Equal(t, "select_reasoning_effort", items[0].ID())
	require.Contains(t, items[0].Filter(), "efforts")

	var efforts []string
	for _, item := range items[1:] {
		action, ok := item.Action().(ActionSelectReasoningEffort)
		require.True(t, ok)
		efforts = append(efforts, action.Effort)
		if action.Effort == "none" {
			require.Equal(t, "disable_thinking", item.ID())
			require.Contains(t, item.Filter(), "efforts none")
			require.Contains(t, item.Filter(), "thinking off")
			continue
		}
		require.Contains(t, item.Filter(), "efforts "+action.Effort)
		require.Contains(t, item.Filter(), "deepseek "+action.Effort)
	}
	require.Equal(t, []string{"low", "high", "max", "none"}, efforts)
}

func TestReasoningEffortCommandsNonDeepSeek(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	model := &catwalk.Model{
		ID:              "o3",
		CanReason:       true,
		ReasoningLevels: []string{"low", "high"},
	}

	items := reasoningEffortCommands(&sty, "openai", model)
	require.Len(t, items, 1, "only DeepSeek gets direct effort shortcuts")
	require.Equal(t, "select_reasoning_effort", items[0].ID())
}

func TestReasoningEffortCommandsWithoutLevels(t *testing.T) {
	t.Parallel()
	sty := styles.CharmtonePantera()
	model := &catwalk.Model{ID: "deepseek-flash", CanReason: true}

	require.Empty(t, reasoningEffortCommands(&sty, "deepseek", model))
}
