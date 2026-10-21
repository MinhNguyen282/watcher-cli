package shellconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestModelFastSelect verifies the `model fast` slot selection, which
// backs the task sub-agent's cheap model.
func TestModelFastSelect(t *testing.T) {
	t.Parallel()

	result := loadScript(t, `provider add openai --api-key k
model fast openai/gpt-5-mini`)

	models := result["models"].(map[string]any)
	fast := models["fast"].(map[string]any)
	require.Equal(t, "openai", fast["provider"])
	require.Equal(t, "gpt-5-mini", fast["model"])
}

// TestModelFastSelectRejectsUnknownSlot keeps the subcommand surface
// explicit: fast is a valid slot, arbitrary slots are not.
func TestModelFastSelectRejectsUnknownSlot(t *testing.T) {
	t.Parallel()

	_, err := LoadShellConfig(t.Context(), t.TempDir()+"/crushrc", []byte(`model huge openai/gpt-x`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown subcommand")
}
