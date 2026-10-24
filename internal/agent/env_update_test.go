package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

// configStoreForTest builds a config store rooted at dir without touching
// provider credentials or the user's real configuration.
func configStoreForTest(t *testing.T, dir string) (*config.ConfigStore, error) {
	t.Helper()
	crushJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "crush.json"), []byte(crushJSON), 0o644))
	return config.Init(t.Context(), dir, "", false)
}

func TestEnvChanges(t *testing.T) {
	t.Parallel()

	base := envSnapshot{WorkingDir: "/work", Platform: "linux", Date: "1/2/2026"}
	require.Empty(t, envChanges(base, base))

	withDir := base
	withDir.WorkingDir = "/other"
	require.Equal(t, []string{"the working directory"}, envChanges(base, withDir))

	withGit := base
	withGit.IsGitRepo = true
	require.Equal(t, []string{"whether the directory is a git repository"}, envChanges(base, withGit))

	withDate := base
	withDate.Date = "1/3/2026"
	require.Equal(t, []string{"the date"}, envChanges(base, withDate))
}

func TestSnapshotEnvRequiresConfig(t *testing.T) {
	t.Parallel()

	_, ok := snapshotEnv(nil)
	require.False(t, ok)
}

func TestEnvironmentUpdateLifecycle(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	cfg, err := configStoreForTest(t, workingDir)
	require.NoError(t, err)

	agent := &sessionAgent{
		cfg:       cfg,
		envStates: csync.NewMap[string, envSnapshot](),
	}

	// The first call records the baseline: the environment is already in the
	// system prompt, so no notice is emitted.
	update, changed := agent.environmentUpdate(t.Context(), "session-1")
	require.False(t, changed)
	require.Empty(t, update)

	// Same environment, no notice.
	update, changed = agent.environmentUpdate(t.Context(), "session-1")
	require.False(t, changed)
	require.Empty(t, update)

	// A git repository appearing under the working directory is a change
	// the session must be told about.
	require.NoError(t, os.Mkdir(filepath.Join(workingDir, ".git"), 0o755))
	update, changed = agent.environmentUpdate(t.Context(), "session-1")
	require.True(t, changed)
	require.Contains(t, update, "# Environment update")
	require.Contains(t, update, "whether the directory is a git repository")
	require.Contains(t, update, "Is directory a git repo: yes")

	// The update is emitted once, not on every step.
	_, changed = agent.environmentUpdate(t.Context(), "session-1")
	require.False(t, changed)
}
