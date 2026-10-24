package permission

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModeNext(t *testing.T) {
	t.Parallel()

	// The ring follows Modes order and wraps.
	require.Equal(t, ModeAcceptEdits, ModeDefault.Next(true))
	require.Equal(t, ModeAuto, ModeAcceptEdits.Next(true))
	require.Equal(t, ModeBypassPermissions, ModeAuto.Next(true))
	require.Equal(t, ModePlan, ModeBypassPermissions.Next(true))

	// Bypass is skipped when the deployment disabled it.
	require.Equal(t, ModeDefault, ModeAuto.Next(false))
	require.Equal(t, ModeDontAsk, ModePlan.Next(false))

	// An unknown mode restarts at the default.
	require.Equal(t, ModeDefault, Mode("bogus").Next(true))
}
