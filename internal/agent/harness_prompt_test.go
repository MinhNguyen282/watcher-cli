package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendHarnessPrompt(t *testing.T) {
	t.Run("no harness file", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "base prompt", appendHarnessPrompt("base prompt", t.TempDir()))
	})

	t.Run("renders saved harness", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".watcher"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".watcher", "harness_state.json"),
			[]byte(`{"id":"x","rendered_system_prompt":"<self-evolving-harness>\nrules\n</self-evolving-harness>"}`),
			0o644,
		))

		got := appendHarnessPrompt("base prompt", dir)
		require.Contains(t, got, "base prompt")
		require.Contains(t, got, "<self-evolving-harness>")
	})

	t.Run("malformed file is ignored", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".watcher"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".watcher", "harness_state.json"),
			[]byte("{not json"),
			0o644,
		))
		require.Equal(t, "base prompt", appendHarnessPrompt("base prompt", dir))
	})

	t.Run("empty rendered section is ignored", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".watcher"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".watcher", "harness_state.json"),
			[]byte(`{"id":"x","rendered_system_prompt":"  "}`),
			0o644,
		))
		require.Equal(t, "base prompt", appendHarnessPrompt("base prompt", dir))
	})

	t.Run("env override wins", func(t *testing.T) {
		dir := t.TempDir()
		statePath := filepath.Join(dir, "custom.json")
		require.NoError(t, os.WriteFile(statePath,
			[]byte(`{"id":"x","rendered_system_prompt":"custom rules"}`), 0o644))
		t.Setenv(harnessStateEnv, statePath)

		got := appendHarnessPrompt("base prompt", t.TempDir())
		require.Contains(t, got, "custom rules")
	})
}
